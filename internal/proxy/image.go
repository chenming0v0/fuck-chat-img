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
// 支持 url / base64 / file_id 形式
func imageContentHash(c map[string]interface{}) string {
	h := sha256.New()
	// type 也纳入哈希以区分不同结构
	if t, ok := c["type"].(string); ok {
		h.Write([]byte(t))
	}
	// 各种可能字段
	for _, k := range []string{"image_url", "url", "image", "data", "file_id", "detail"} {
		if v, ok := c[k]; ok {
			b, _ := json.Marshal(v)
			h.Write([]byte(k))
			h.Write(b)
		}
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// extractImageRef 从 content item 提取图片引用(优先 url, 其次 base64 data URL)
func extractImageRef(c map[string]interface{}) (url string, b64 string, ok bool) {
	if u, has := c["image_url"]; has {
		switch v := u.(type) {
		case string:
			return v, "", true
		case map[string]interface{}:
			if su, ok2 := v["url"].(string); ok2 {
				return su, "", true
			}
		}
	}
	if u, has := c["url"]; has {
		if s, ok2 := u.(string); ok2 {
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
			return s, "", true
		}
	}
	return "", "", false
}

// recognizeImage 用图片模型轮询识别单张图片, 返回文本描述
// 若所有图片模型都失败则返回 error (满足"图片识别失败直接返回报错")
// imgModels 已由 nextImageModels 按策略(round_robin/failover)选定.
func recognizeImage(imgModels []UpstreamModelRT, prompt string, imageURL, imageB64 string, client *http.Client) (string, string, error) {
	if len(imgModels) == 0 {
		return "", "", errors.New("未配置图片模型")
	}
	var lastErr error
	emptyCount := 0
	for _, m := range imgModels {
		// 单模型重试次数, 默认 1 次
		retries := m.MaxRetries
		if retries < 1 {
			retries = 1
		}
		var desc string
		var err error
		for attempt := 0; attempt < retries; attempt++ {
			desc, err = callImageModel(m, prompt, imageURL, imageB64, client)
			if err == nil && strings.TrimSpace(desc) != "" {
				return desc, m.DisplayName(), nil
			}
		}
		if err == nil && strings.TrimSpace(desc) == "" {
			// 调用成功但返回空内容
			emptyCount++
			lastErr = fmt.Errorf("[%s] 返回空内容", m.DisplayName())
		} else if err != nil {
			lastErr = fmt.Errorf("[%s] %v", m.DisplayName(), err)
		}
	}
	if lastErr == nil {
		lastErr = errors.New("所有图片模型均返回空结果")
	} else if emptyCount == len(imgModels) {
		// 所有模型都返回空内容
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
				}
			} else {
				b, _ := json.Marshal(e)
				sb.Write(b)
			}
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
