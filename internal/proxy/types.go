package proxy

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/fuck-chat-img/fci/internal/config"
)

// sharedHTTPClient 全局复用的 HTTP 客户端(连接池复用, 避免每次请求新建)
var sharedHTTPClient = &http.Client{
	Timeout: time.Duration(config.Get().RequestTimeout) * time.Second,
	Transport: &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 20,
		IdleConnTimeout:     90 * time.Second,
	},
}

// sharedStreamHTTPClient 流式请求专用(更长超时)
var sharedStreamHTTPClient = &http.Client{
	Timeout: time.Duration(config.Get().RequestTimeout*2) * time.Second,
	Transport: &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 20,
		IdleConnTimeout:     90 * time.Second,
	},
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
