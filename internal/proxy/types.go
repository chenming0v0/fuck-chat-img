package proxy

import (
	"encoding/json"
	"errors"
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
var sharedHTTPClient = newHTTPClient(config.Get().RequestTimeout)

// sharedStreamHTTPClient 流式请求专用
// 注意: 流式客户端不能设 http.Client.Timeout —— Timeout 字段会覆盖"读取整个响应体"的时间,
// 长 SSE 流(含思考链/长输出)会被中途切断. 改为在 Transport.ResponseHeaderTimeout 控制首字节超时,
// 整体生命周期由上游 EOF + 客户端 context(见各 handler 的 WithContext)共同管理.
var sharedStreamHTTPClient = newStreamHTTPClient(config.Get().RequestTimeout)

func newHTTPClient(timeoutSec int) *http.Client {
	return &http.Client{
		Timeout: time.Duration(timeoutSec) * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 20,
			IdleConnTimeout:     90 * time.Second,
		},
	}
}

// newStreamHTTPClient 流式客户端: 仅控制响应头超时, 不限制响应体读取时长
// 首字节超时内未返回 header 视为上游不可用; 头返回后整个流的生命周期交给 EOF + context.
func newStreamHTTPClient(timeoutSec int) *http.Client {
	return &http.Client{
		Timeout: 0, // 不限整体超时, 避免切断长 SSE 流
		Transport: &http.Transport{
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   20,
			IdleConnTimeout:       90 * time.Second,
			ResponseHeaderTimeout: time.Duration(timeoutSec) * time.Second,
		},
	}
}

// resetHTTPClients 按当前配置重建全局 HTTP 客户端(主要供测试使用, 让 RequestTimeout 改动生效)
func resetHTTPClients() {
	cfg := config.Get()
	sharedHTTPClient.CloseIdleConnections()
	sharedStreamHTTPClient.CloseIdleConnections()
	sharedHTTPClient = newHTTPClient(cfg.RequestTimeout)
	sharedStreamHTTPClient = newStreamHTTPClient(cfg.RequestTimeout)
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

// clientGone 检测客户端是否已断开连接(主动取消/关闭浏览器/网络中断)
// 用于流式转发循环中及时停止读取上游, 避免上游 token 浪费、缓存污染、历史误报成功.
// 同时检查 context 取消与 Writer 写入错误(底层 broken pipe)两种信号.
func clientGone(c *gin.Context, lastWriteErr error) bool {
	if c.Request.Context().Err() != nil {
		return true
	}
	return lastWriteErr != nil
}

// clientDisconnectedMsg 客户端断连时返回的错误消息, 否则返回空串
func clientDisconnectedMsg(disconnected bool) string {
	if disconnected {
		return "client disconnected"
	}
	return ""
}

// pickImgModel 在已累计的 imgModelUsed 与本次失败返回的 used 之间选择更可靠的一个.
// 错误路径下本次 used 通常为空(全部模型失败), 优先保留前面已成功识别累计的值,
// 避免历史记录的 ImageModelUsed 字段被空值覆盖.
func pickImgModel(existing, fallback string) string {
	if existing != "" {
		return existing
	}
	return fallback
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
		return m, errors.New("empty main model config")
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
// 语义: 传入"当前仍活跃的模型组集合"(值为 true), 集合外的项被删除.
// 注意: 调用方必须传完整的活跃集合, 否则会误删仍存在的游标.
// 若只需删除单个模型组的游标, 请使用 DeleteRRIndex(更直接, 不易误用).
func CleanupRRIndex(activeNames map[string]bool) {
	rrMu.Lock()
	defer rrMu.Unlock()
	for name := range rrIndex {
		if !activeNames[name] {
			delete(rrIndex, name)
		}
	}
}

// DeleteRRIndex 删除指定模型组的轮询游标(用于模型组被删除时精确清理)
// 比 CleanupRRIndex 更安全, 不会因传错参数而误清空其它模型组的游标.
func DeleteRRIndex(name string) {
	rrMu.Lock()
	defer rrMu.Unlock()
	delete(rrIndex, name)
}
