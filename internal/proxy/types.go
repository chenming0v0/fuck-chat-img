package proxy

import (
	"encoding/json"
	"strings"
	"sync"
)

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

// nextImageModels 按轮询策略返回尝试顺序
//   - round_robin: 每次请求只挑 1 个模型(按顺序轮转), 失败即报错, 不再尝试下一个
//   - failover:    返回全部模型(原顺序), 逐个尝试直到成功
func nextImageModels(g *modelGroupRuntime) []UpstreamModelRT {
	if len(g.ImageModels) == 0 {
		return nil
	}
	if g.ImageStrategy == "failover" {
		// 故障转移: 原顺序返回全部, 由调用方逐个尝试
		out := make([]UpstreamModelRT, len(g.ImageModels))
		copy(out, g.ImageModels)
		return out
	}
	// round_robin: 只挑 1 个, 轮转
	rrMu.Lock()
	start := rrIndex[g.Name] % len(g.ImageModels)
	rrIndex[g.Name] = (start + 1) % len(g.ImageModels)
	rrMu.Unlock()
	return []UpstreamModelRT{g.ImageModels[start]}
}

// ForgetGroupRR 删除模型组时清理其轮询游标, 避免内存泄漏
func ForgetGroupRR(name string) {
	rrMu.Lock()
	delete(rrIndex, name)
	rrMu.Unlock()
}
