package proxy

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/fuck-chat-img/fci/internal/auth"
	"github.com/fuck-chat-img/fci/internal/config"
	"github.com/gin-gonic/gin"
)

// sharedHTTPClient 全局复用的 HTTP 客户端(连接池复用, 避免每次请求新建)
// 注意: 包初始化时捕获 config.Get().RequestTimeout; 测试若改动超时需调用 resetHTTPClients.
var sharedHTTPClient = newHTTPClient(config.Get().RequestTimeout, false)

// sharedStreamHTTPClient 流式请求专用(更长超时, 避免长 SSE 流被切断)
var sharedStreamHTTPClient = newHTTPClient(config.Get().RequestTimeout*2, true)

func newHTTPClient(timeoutSec int, stream bool) *http.Client {
	return &http.Client{
		Timeout: time.Duration(timeoutSec) * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 20,
			IdleConnTimeout:     90 * time.Second,
		},
	}
}

// resetHTTPClients 按当前配置重建全局 HTTP 客户端(主要供测试使用, 让 RequestTimeout 改动生效)
func resetHTTPClients() {
	cfg := config.Get()
	sharedHTTPClient.CloseIdleConnections()
	sharedStreamHTTPClient.CloseIdleConnections()
	sharedHTTPClient = newHTTPClient(cfg.RequestTimeout, false)
	sharedStreamHTTPClient = newHTTPClient(cfg.RequestTimeout*2, true)
}

// extractUserID 从 gin.Context 提取用户ID(由 MiddlewareProxyAuth/MiddlewareAuth 写入)
// 用于把代理请求历史归属到具体用户, 实现 History 的用户隔离.
// FCI_PROXY_KEY 匿名访问场景下返回 0.
func extractUserID(c *gin.Context) uint {
	if v, exists := c.Get(auth.ContextKeyUserID); exists {
		if uid, ok := v.(uint); ok {
			return uid
		}
	}
	return 0
}

// UpstreamModelRT 运行时上游模型配置(来自 ModelGroup 的 JSON 字段反序列化)
type UpstreamModelRT struct {
	BaseURL    string `json:"base_url"`
	APIKey     string `json:"api_key"`
	Model      string `json:"model"`
	APIType    string `json:"api_type"`
	ExtraURL   string `json:"extra_url"`
	MaxRetries int    `json:"max_retries"`
	Weight     int    `json:"weight"`
}

// DisplayName 用于日志/历史展示
func (u UpstreamModelRT) DisplayName() string {
	if u.Model == "" {
		return u.BaseURL
	}
	return u.Model + "@" + hostOf(u.BaseURL)
}

// ChatURL 图片模型/对话模型的 chat completions 入口
func (u UpstreamModelRT) ChatURL() string {
	if u.ExtraURL != "" {
		return u.ExtraURL
	}
	base := strings.TrimRight(u.BaseURL, "/")
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	return base + "/chat/completions"
}

// ResponsesURL 主对话模型的 responses 入口
func (u UpstreamModelRT) ResponsesURL() string {
	if u.ExtraURL != "" {
		return u.ExtraURL
	}
	base := strings.TrimRight(u.BaseURL, "/")
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	return base + "/responses"
}

// MessagesURL Claude/Anthropic 兼容的 messages 入口
// 默认走 base + /messages, 上游应当是 Claude 兼容服务(如 Anthropic API 或 claude-code-router)
func (u UpstreamModelRT) MessagesURL() string {
	if u.ExtraURL != "" {
		return u.ExtraURL
	}
	base := strings.TrimRight(u.BaseURL, "/")
	if base == "" {
		base = "https://api.anthropic.com/v1"
	}
	return base + "/messages"
}

func hostOf(u string) string {
	u = strings.TrimPrefix(u, "https://")
	u = strings.TrimPrefix(u, "http://")
	if i := strings.Index(u, "/"); i >= 0 {
		u = u[:i]
	}
	return u
}

// modelGroupRuntime 运行时模型组
type modelGroupRuntime struct {
	Name          string
	MainText      UpstreamModelRT
	ImageModels   []UpstreamModelRT
	ImageStrategy string
	ImagePrompt   string
	ReplaceImage  bool
}

// ParseMain 解析主对话模型
func ParseMain(s string) (UpstreamModelRT, error) {
	var m UpstreamModelRT
	if s == "" {
		return m, &json.SyntaxError{}
	}
	err := json.Unmarshal([]byte(s), &m)
	return m, err
}

// ParseImages 解析图片模型数组
func ParseImages(s string) ([]UpstreamModelRT, error) {
	var arr []UpstreamModelRT
	if s == "" {
		return arr, nil
	}
	err := json.Unmarshal([]byte(s), &arr)
	return arr, err
}

// roundRobinState 图片模型轮询游标(全局, 按模型组名隔离)
var (
	rrMu    sync.Mutex
	rrIndex = map[string]int{}
)

// nextImageModels 按策略返回尝试顺序
// round_robin: 从轮询游标位置开始依次尝试所有模型
// failover: 只返回第一个模型(主模型), 失败后才尝试下一个(由 recognizeImage 处理)
func nextImageModels(g *modelGroupRuntime) []UpstreamModelRT {
	if len(g.ImageModels) == 0 {
		return nil
	}
	if g.ImageStrategy == "failover" {
		// failover: 始终从第一个模型开始尝试
		return g.ImageModels
	}
	// round_robin: 从轮询游标位置开始
	rrMu.Lock()
	start := rrIndex[g.Name] % len(g.ImageModels)
	rrIndex[g.Name] = (start + 1) % len(g.ImageModels)
	rrMu.Unlock()
	out := make([]UpstreamModelRT, 0, len(g.ImageModels))
	for i := 0; i < len(g.ImageModels); i++ {
		out = append(out, g.ImageModels[(start+i)%len(g.ImageModels)])
	}
	return out
}

// CleanupRRIndex 清理已不存在的模型组的轮询游标(防止内存泄漏)
func CleanupRRIndex(activeNames map[string]bool) {
	rrMu.Lock()
	defer rrMu.Unlock()
	for name := range rrIndex {
		if !activeNames[name] {
			delete(rrIndex, name)
		}
	}
}
