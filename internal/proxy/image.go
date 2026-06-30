package proxy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// imageContentHash 计算图片内容的稳定哈希, 用于缓存键
// 支持 url / base64 / file_id 形式, 以及 Claude 的 source.data 格式
// 注意: 仅按"图片实际内容"哈希, 不区分外层包裹格式(相同图片任意格式产生相同哈希)
func imageContentHash(c map[string]interface{}) string {
	h := sha256.New()
	// 提取真实图片内容后再哈希(忽略外层 type/字段名差异)
	url, b64, ok := extractImageRef(c)
	if ok {
		if url != "" {
			h.Write([]byte("url:"))
			h.Write([]byte(url))
		}
		if b64 != "" {
			// b64 可能是 data URL 或纯 base64, 统一抽取纯 base64 部分
			raw := stripDataURLPrefix(b64)
			h.Write([]byte("b64:"))
			h.Write([]byte(raw))
		}
		return hex.EncodeToString(h.Sum(nil))[:32]
	}
	// 兜底: 没有可识别图片字段时, 对整个对象做 canonical 哈希
	if t, ok := c["type"].(string); ok {
		h.Write([]byte(t))
	}
	for _, k := range []string{"image_url", "url", "image", "data", "file_id", "detail", "source"} {
		if v, ok := c[k]; ok {
			b, _ := json.Marshal(v)
			h.Write([]byte(k))
			h.Write(b)
		}
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// stripDataURLPrefix 从 data:image/...;base64,XXX 中提取纯 base64 部分
// 若不是 data URL 则原样返回
func stripDataURLPrefix(s string) string {
	if !strings.HasPrefix(s, "data:") {
		return s
	}
	// data:image/png;base64,XXXX
	if i := strings.Index(s, ","); i >= 0 {
		return s[i+1:]
	}
	return s
}

// extractImageRef 从 content item 提取图片引用(优先 url, 其次 base64 data URL)
// 支持 OpenAI Chat(image_url)、Responses(input_image)、Claude(image, source) 等格式
//
// 关键: 任何形如 "data:image/...;base64,XXX" 的值都返回为 b64(而非 url),
// 这样相同图片无论以 OpenAI image_url.url / Codex 字符串内 data URL / Claude source.data
// 哪种形式出现, 都会经 imageContentHash 写入 "b64:<纯base64>" 而产生相同哈希,
// 进而保证跨格式缓存命中(用户强调: "一定要把缓存给我做好").
func extractImageRef(c map[string]interface{}) (url string, b64 string, ok bool) {
	if u, has := c["image_url"]; has {
		switch v := u.(type) {
		case string:
			if strings.HasPrefix(v, "data:") {
				return "", v, true
			}
			return v, "", true
		case map[string]interface{}:
			if su, ok2 := v["url"].(string); ok2 {
				if strings.HasPrefix(su, "data:") {
					return "", su, true
				}
				return su, "", true
			}
		}
	}
	if u, has := c["url"]; has {
		if s, ok2 := u.(string); ok2 {
			if strings.HasPrefix(s, "data:") {
				return "", s, true
			}
			return s, "", true
		}
	}
	if d, has := c["data"]; has {
		if s, ok2 := d.(string); ok2 {
			if strings.HasPrefix(s, "data:") {
				return "", s, true
			}
			return "", "data:image/png;base64," + s, true
		}
	}
	if i, has := c["image"]; has {
		if s, ok2 := i.(string); ok2 {
			if strings.HasPrefix(s, "data:") {
				return "", s, true
			}
			return "", "data:image/png;base64," + s, true
		}
	}
	// Claude 格式: {type: image, source: {type: base64|url, media_type, data|url}}
	if src, has := c["source"]; has {
		if m, ok2 := src.(map[string]interface{}); ok2 {
			st, _ := m["type"].(string)
			switch st {
			case "base64":
				if data, _ := m["data"].(string); data != "" {
					mt, _ := m["media_type"].(string)
					if mt == "" {
						mt = "image/png"
					}
					return "", "data:" + mt + ";base64," + data, true
				}
			case "url":
				if u, _ := m["url"].(string); u != "" {
					return u, "", true
				}
			}
		}
	}
	return "", "", false
}

// isImageContentItem 判断 map 是否为图片 content item(任意已知格式)
func isImageContentItem(c map[string]interface{}) bool {
	typ, hasType := c["type"].(string)
	if hasType {
		switch typ {
		case "image_url", "input_image", "image":
			return true
		default:
			return false
		}
	}
	// 无 type 字段时, source 字段是 Claude 图片标志
	if _, has := c["source"]; has {
		return true
	}
	return false
}

// ImageModelsConfig 图片模型配置(运行时使用)
type ImageModelsConfig struct {
	Group     *modelGroupRuntime
	Prompt    string
	Strategy  string
}

// recognizeImage 用图片模型轮询识别单张图片, 返回文本描述
// 若所有图片模型都失败则返回 error (满足"图片识别失败直接返回报错")
func recognizeImage(imgModels []UpstreamModelRT, strategy string, prompt string, imageURL, imageB64 string, client *http.Client) (string, string, error) {
	if len(imgModels) == 0 {
		return "", "", errors.New("未配置图片模型")
	}
	var lastErr error
	for _, m := range imgModels {
		desc, err := callImageModel(m, prompt, imageURL, imageB64, client)
		if err == nil {
			if strings.TrimSpace(desc) != "" {
				return desc, m.DisplayName(), nil
			}
			// err==nil 但 desc 为空: 单独构造有意义的错误, 避免 fmt.Errorf("%v", nil) 产生 "<nil>"
			lastErr = fmt.Errorf("[%s] 返回空结果", m.DisplayName())
		} else {
			lastErr = fmt.Errorf("[%s] %v", m.DisplayName(), err)
		}
		// round_robin 与 failover 的差异已在 nextImageModels(起点不同)体现,
		// 此处统一逐个尝试直到成功; strategy 参数保留以备未来扩展.
		_ = strategy
	}
	if lastErr == nil {
		lastErr = errors.New("所有图片模型均返回空结果")
	}
	return "", "", fmt.Errorf("图片识别失败: %w", lastErr)
}

// callImageModel 调用单个图片模型(走 OpenAI Chat Completions 兼容接口)
func callImageModel(m UpstreamModelRT, prompt string, imageURL, imageB64 string, client *http.Client) (string, error) {
	if prompt == "" {
		prompt = "请详细描述这张图片的内容,包括其中的文字、物体、场景、颜色等关键信息。"
	}
	content := []map[string]interface{}{
		{"type": "text", "text": prompt},
	}
	if imageURL != "" {
		content = append(content, map[string]interface{}{
			"type": "image_url",
			"image_url": map[string]string{"url": imageURL},
		})
	} else if imageB64 != "" {
		content = append(content, map[string]interface{}{
			"type": "image_url",
			"image_url": map[string]string{"url": imageB64},
		})
	}
	body := map[string]interface{}{
		"model":    m.Model,
		"messages": []map[string]interface{}{{"role": "user", "content": content}},
		// 显式声明非流式: 部分兼容网关默认走流式会返回 text/event-stream, 导致 json.Unmarshal 失败
		"stream": false,
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	url := m.ChatURL()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+m.APIKey)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("图片模型 HTTP %d: %s", resp.StatusCode, truncate(string(respBytes), 500))
	}
	var cr struct {
		Choices []struct {
			Message struct {
				Content interface{} `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBytes, &cr); err != nil {
		return "", err
	}
	if len(cr.Choices) == 0 {
		return "", errors.New("图片模型返回空 choices")
	}
	return contentToString(cr.Choices[0].Message.Content), nil
}

// contentToString 处理 content 可能是 string 或数组的形式
func contentToString(c interface{}) string {
	switch v := c.(type) {
	case string:
		return v
	case []interface{}:
		var sb strings.Builder
		for _, e := range v {
			if m, ok := e.(map[string]interface{}); ok {
				if t, _ := m["text"].(string); t != "" {
					sb.WriteString(t)
					continue
				}
			}
			b, _ := json.Marshal(e)
			sb.Write(b)
		}
		return sb.String()
	default:
		b, _ := json.Marshal(c)
		return string(b)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
