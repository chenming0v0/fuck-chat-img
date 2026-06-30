package proxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/fuck-chat-img/fci/internal/cache"
	"github.com/fuck-chat-img/fci/internal/model"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// responsesRequest OpenAI /v1/responses 请求体
type responsesRequest struct {
	Model       string          `json:"model"`
	Input       json.RawMessage `json:"input"`
	Stream      bool            `json:"stream"`
	StreamOpts  struct {
		Include []string `json:"include"`
	} `json:"stream_options"`
	raw json.RawMessage
}

// HandleResponses 处理 /v1/responses
func HandleResponses(c *gin.Context) {
	bodyBytes, err := io.ReadAll(c.Request.Body)
	if err != nil {
		writeErr(c, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	var req responsesRequest
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		writeErr(c, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	req.raw = bodyBytes

	if req.Model == "" {
		writeErr(c, http.StatusBadRequest, "missing model")
		return
	}

	grp, err := lookupGroup(req.Model)
	if err != nil {
		writeErr(c, http.StatusNotFound, err.Error())
		return
	}

	// 缓存键: 输出影响参数指纹(stream/stream_options/max_output_tokens/temperature/tools...) + input 规范化
	contentCanonical := normalizeResponsesInput(req.Input)
	canonical := composeCacheCanonical(paramsFingerprint(req.raw), contentCanonical)
	cacheKey := cache.Key(grp.Name, canonical)

	start := time.Now()
	reqID := "resp_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:24]
	userID := extractUserID(c)

	// 1. 命中缓存直接回放(从 entry 回填真实元数据)
	if cache.Enabled() {
		if e, ok := cache.Get(cacheKey); ok {
			cache.RecordHit()
			replayCacheEntry(c, e)
			outSummary := cacheHitOutputSummary(e)
			recordHistory(reqID, userID, grp, &req, e.HasImage, true, true, e.ImageModelUsed, grp.MainText.DisplayName(), e.ImageCount, 0, 0, time.Since(start), outSummary, "cache hit")
			return
		}
		cache.RecordMiss()
	}

	// 2. 解析 input, 提取并识别图片
	hasImage, imgCount, imgModelUsed, modifiedInput, imgErr := processImagesForInput(grp, req.Input)
	if imgErr != nil {
		recordHistory(reqID, userID, grp, &req, hasImage, false, false, imgModelUsed, "", imgCount, 0, 0, time.Since(start), nil, imgErr.Error())
		writeErr(c, http.StatusBadGateway, imgErr.Error())
		return
	}

	// 3. 用修改后的 input 重建请求体, 转发到主对话模型
	newBody := rebuildResponsesBody(req.raw, modifiedInput, grp)

	if req.Stream {
		handleResponsesStream(c, grp, newBody, reqID, userID, &req, hasImage, imgCount, imgModelUsed, cacheKey, start)
		return
	}
	handleResponsesNonStream(c, grp, newBody, reqID, userID, &req, hasImage, imgCount, imgModelUsed, cacheKey, start)
}

// responsesImgConfig Responses 协议的图片处理配置: 文本项类型为 input_text
func responsesImgConfig(g *modelGroupRuntime) imgProcessConfig {
	return imgProcessConfig{
		replaceImage: g.ReplaceImage,
		textType:     "input_text",
	}
}

// processImagesForInput 遍历 input, 识别所有图片并用文本描述替换/追加
// 兼容三种 content 形态:
//   - 数组(标准 content array): 直接图片项 + tool_result 内嵌字符串图片
//   - 字符串(Codex role=tool 输出等): 解析 JSON 后递归识别
func processImagesForInput(g *modelGroupRuntime, input json.RawMessage) (hasImage bool, imgCount int, imgModelUsed string, modified []byte, err error) {
	if len(input) == 0 {
		return false, 0, "", input, nil
	}
	var arr []map[string]interface{}
	isArray := true
	if err := json.Unmarshal(input, &arr); err != nil {
		isArray = false
		var single map[string]interface{}
		if err2 := json.Unmarshal(input, &single); err2 != nil {
			return false, 0, "", input, nil
		}
		arr = []map[string]interface{}{single}
	}

	client := sharedHTTPClient
	cfg := responsesImgConfig(g)

	for i := range arr {
		// 1. 字符串 content(Codex role=tool 等): 走递归处理器(用 input_text 配置)
		if s, ok := arr[i]["content"].(string); ok && s != "" {
			newStr, hasImg, cnt, used, perr := processImagesInStringContentCfg(g, s, cfg)
			if perr != nil {
				return hasImage || hasImg, imgCount + cnt, used, input, perr
			}
			if hasImg {
				hasImage = true
				imgCount += cnt
				imgModelUsed = used
				arr[i]["content"] = newStr
			}
			continue
		}
		cont, ok := arr[i]["content"].([]interface{})
		if !ok {
			continue
		}
		// 收集需要追加的文本项(避免在 range 循环中修改切片)
		var extraItems []interface{}
		for j := 0; j < len(cont); j++ {
			cm, ok := cont[j].(map[string]interface{})
			if !ok {
				continue
			}
			typ, _ := cm["type"].(string)
			// 2. tool_result / tool_use 项: 内嵌 content 递归处理(用 input_text 配置, 保证产出类型合法)
			if typ == "tool_result" || typ == "tool_use" {
				if sub, ok := cm["content"].([]interface{}); ok {
					newV, r := processImagesInValueCfg(g, sub, cfg)
					if r.err != nil {
						return hasImage || r.hasImage, imgCount + r.imgCount, r.imgModel, input, r.err
					}
					if r.hasImage {
						hasImage = true
						imgCount += r.imgCount
						if r.imgModel != "" {
							imgModelUsed = r.imgModel
						}
						cm["content"] = newV
					}
				} else if ss, ok := cm["content"].(string); ok && ss != "" {
					newStr, hasImg, cnt, used, perr := processImagesInStringContentCfg(g, ss, cfg)
					if perr != nil {
						return hasImage || hasImg, imgCount + cnt, used, input, perr
					}
					if hasImg {
						hasImage = true
						imgCount += cnt
						imgModelUsed = used
						cm["content"] = newStr
					}
				}
				continue
			}
			// 3. 直接图片项(OpenAI image_url / Responses input_image / Claude image)
			if typ != "input_image" && typ != "image" && typ != "image_url" {
				continue
			}
			url, b64, ok := extractImageRef(cm)
			if !ok {
				continue
			}
			hasImage = true
			imgCount++
			imgs := nextImageModels(g)
			desc, used, e := recognizeImage(imgs, g.ImageStrategy, g.ImagePrompt, url, b64, client)
			if e != nil {
				return hasImage, imgCount, used, input, e
			}
			imgModelUsed = used
			textItem := map[string]interface{}{
				"type": "input_text",
				"text": "[图片识别结果]\n" + desc,
			}
			if g.ReplaceImage {
				cont[j] = textItem
			} else {
				extraItems = append(extraItems, textItem)
			}
		}
		if len(extraItems) > 0 {
			arr[i]["content"] = append(cont, extraItems...)
		}
	}

	if !isArray {
		modified, err = json.Marshal(arr[0])
	} else {
		modified, err = json.Marshal(arr)
	}
	if err != nil {
		return hasImage, imgCount, imgModelUsed, input, err
	}
	return hasImage, imgCount, imgModelUsed, modified, nil
}

// rebuildResponsesBody 用修改后的 input 重建请求体
func rebuildResponsesBody(raw json.RawMessage, newInput []byte, g *modelGroupRuntime) []byte {
	var obj map[string]interface{}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return raw
	}
	var in interface{}
	_ = json.Unmarshal(newInput, &in)
	obj["input"] = in
	obj["model"] = g.MainText.Model
	b, err := json.Marshal(obj)
	if err != nil {
		return raw
	}
	return b
}

func handleResponsesNonStream(c *gin.Context, g *modelGroupRuntime, body []byte, reqID string, userID uint, req *responsesRequest, hasImage bool, imgCount int, imgModelUsed string, cacheKey string, start time.Time) {
	httpReq, err := http.NewRequest(http.MethodPost, g.MainText.ResponsesURL(), bytes.NewReader(body))
	if err != nil {
		recordHistory(reqID, userID, g, req, hasImage, false, false, imgModelUsed, "", imgCount, 0, 0, time.Since(start), nil, err.Error())
		writeErr(c, http.StatusInternalServerError, err.Error())
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+g.MainText.APIKey)
	resp, err := sharedHTTPClient.Do(httpReq)
	if err != nil {
		recordHistory(reqID, userID, g, req, hasImage, false, false, imgModelUsed, "", imgCount, 0, 0, time.Since(start), nil, err.Error())
		writeErr(c, http.StatusBadGateway, "上游请求失败: "+err.Error())
		return
	}
	defer resp.Body.Close()
	respBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		recordHistory(reqID, userID, g, req, hasImage, false, false, imgModelUsed, "", imgCount, 0, 0, time.Since(start), respBytes, fmt.Sprintf("上游 %d", resp.StatusCode))
		writeErr(c, resp.StatusCode, fmt.Sprintf("上游返回 %d: %s", resp.StatusCode, truncate(string(respBytes), 800)))
		return
	}
	if cache.Enabled() {
		cache.PutWithMeta(cacheKey, g.Name, respBytes, hasImage, imgCount, imgModelUsed)
	}
	pt, ct := extractUsage(respBytes)
	recordHistory(reqID, userID, g, req, hasImage, true, false, imgModelUsed, g.MainText.DisplayName(), imgCount, pt, ct, time.Since(start), respBytes, "")
	c.Data(http.StatusOK, "application/json", respBytes)
}

func handleResponsesStream(c *gin.Context, g *modelGroupRuntime, body []byte, reqID string, userID uint, req *responsesRequest, hasImage bool, imgCount int, imgModelUsed string, cacheKey string, start time.Time) {
	httpReq, err := http.NewRequest(http.MethodPost, g.MainText.ResponsesURL(), bytes.NewReader(body))
	if err != nil {
		recordHistory(reqID, userID, g, req, hasImage, false, false, imgModelUsed, "", imgCount, 0, 0, time.Since(start), nil, err.Error())
		writeErr(c, http.StatusInternalServerError, err.Error())
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Authorization", "Bearer "+g.MainText.APIKey)
	// 流式必须使用 sharedStreamHTTPClient(更长超时)
	resp, err := sharedStreamHTTPClient.Do(httpReq)
	if err != nil {
		recordHistory(reqID, userID, g, req, hasImage, false, false, imgModelUsed, "", imgCount, 0, 0, time.Since(start), nil, err.Error())
		writeErr(c, http.StatusBadGateway, "上游请求失败: "+err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBytes, _ := io.ReadAll(resp.Body)
		recordHistory(reqID, userID, g, req, hasImage, false, false, imgModelUsed, "", imgCount, 0, 0, time.Since(start), respBytes, fmt.Sprintf("上游 %d", resp.StatusCode))
		writeErr(c, resp.StatusCode, fmt.Sprintf("上游返回 %d: %s", resp.StatusCode, truncate(string(respBytes), 800)))
		return
	}

	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	flusher, _ := c.Writer.(http.Flusher)

	var collected [][]byte
	var pt, ct int
	reader := bufio.NewReaderSize(resp.Body, 64*1024)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			// 真流式: 收到一行立即写出并 Flush
			c.Writer.Write(line)
			if flusher != nil {
				flusher.Flush()
			}
			collected = append(collected, line)
			if bytes.HasPrefix(bytes.TrimSpace(line), []byte("data:")) {
				pt, ct = updateUsageFromSSE(line, pt, ct)
			}
		}
		if err != nil {
			break
		}
	}
	if flusher != nil {
		flusher.Flush()
	}
	if cache.Enabled() && len(collected) > 0 {
		cache.PutStreamWithMeta(cacheKey, g.Name, collected, hasImage, imgCount, imgModelUsed)
	}
	recordHistory(reqID, userID, g, req, hasImage, true, false, imgModelUsed, g.MainText.DisplayName(), imgCount, pt, ct, time.Since(start), nil, "")
}

// updateUsageFromSSE 从 SSE data 行提取 usage
func updateUsageFromSSE(line []byte, pt, ct int) (int, int) {
	s := strings.TrimSpace(string(line))
	if !strings.HasPrefix(s, "data:") {
		return pt, ct
	}
	s = strings.TrimPrefix(s, "data:")
	s = strings.TrimSpace(s)
	if s == "[DONE]" {
		return pt, ct
	}
	var obj map[string]interface{}
	if err := json.Unmarshal([]byte(s), &obj); err != nil {
		return pt, ct
	}
	if u, ok := obj["usage"].(map[string]interface{}); ok {
		if v, ok := u["input_tokens"].(float64); ok {
			pt = int(v)
		}
		if v, ok := u["output_tokens"].(float64); ok {
			ct = int(v)
		}
		if v, ok := u["prompt_tokens"].(float64); ok {
			pt = int(v)
		}
		if v, ok := u["completion_tokens"].(float64); ok {
			ct = int(v)
		}
	}
	return pt, ct
}

// extractUsage 从非流式响应提取 token 用量
func extractUsage(b []byte) (int, int) {
	var obj map[string]interface{}
	if err := json.Unmarshal(b, &obj); err != nil {
		return 0, 0
	}
	u, ok := obj["usage"].(map[string]interface{})
	if !ok {
		return 0, 0
	}
	pt, ct := 0, 0
	if v, ok := u["input_tokens"].(float64); ok {
		pt = int(v)
	}
	if v, ok := u["output_tokens"].(float64); ok {
		ct = int(v)
	}
	if v, ok := u["prompt_tokens"].(float64); ok {
		pt = int(v)
	}
	if v, ok := u["completion_tokens"].(float64); ok {
		ct = int(v)
	}
	return pt, ct
}

// replayCacheEntry 回放缓存条目(区分流式/非流式)
// 流式命中时逐事件 Flush, 保持真流式语义; 非流式命中直接返回 JSON.
func replayCacheEntry(c *gin.Context, e *cache.Entry) {
	if e.IsStream {
		c.Writer.Header().Set("Content-Type", "text/event-stream")
		c.Writer.Header().Set("Cache-Control", "no-cache")
		c.Writer.Header().Set("Connection", "keep-alive")
		c.Writer.Header().Set("X-Accel-Buffering", "no")
		flusher, _ := c.Writer.(http.Flusher)
		for _, ev := range e.StreamEvents {
			c.Writer.Write(ev)
			if flusher != nil {
				flusher.Flush()
			}
		}
		if flusher != nil {
			flusher.Flush()
		}
		return
	}
	c.Data(http.StatusOK, "application/json", e.Value)
}

// cacheHitOutputSummary 缓存命中时构造历史 OutputSummary
// 流式 entry 的 Value 为空, 需拼接 StreamEvents 作为摘要; 非流式直接用 Value.
func cacheHitOutputSummary(e *cache.Entry) []byte {
	if e == nil {
		return nil
	}
	if !e.IsStream {
		return e.Value
	}
	// 流式: 拼接所有事件, 截断到 2000 字符
	var sb strings.Builder
	for _, ev := range e.StreamEvents {
		sb.Write(ev)
		if sb.Len() >= 2000 {
			break
		}
	}
	return []byte(truncate(sb.String(), 2000))
}

// writeErr 写 OpenAI 风格错误(用于 chat / responses 端点)
func writeErr(c *gin.Context, status int, msg string) {
	c.JSON(status, gin.H{
		"error": gin.H{
			"message": msg,
			"type":    "fuck_chat_img_error",
			"code":    status,
		},
	})
}

// writeAnthropicErr 写 Anthropic /v1/messages 风格错误
// Anthropic 错误结构: {"type":"error","error":{"type":"...","message":"..."}}
// Claude SDK 等客户端依赖此结构解析错误, 不能用 OpenAI 格式.
func writeAnthropicErr(c *gin.Context, status int, msg string, errType string) {
	if errType == "" {
		errType = "api_error"
	}
	c.JSON(status, gin.H{
		"type": "error",
		"error": gin.H{
			"type":    errType,
			"message": msg,
		},
	})
}

// lookupGroup 从 DB 查询模型组并构建运行时对象
func lookupGroup(name string) (*modelGroupRuntime, error) {
	var mg model.ModelGroup
	if err := model.DB.Where("name = ? AND enabled = ?", name, true).First(&mg).Error; err != nil {
		return nil, fmt.Errorf("模型组 '%s' 不存在或未启用", name)
	}
	main, err := ParseMain(mg.MainTextModel)
	if err != nil {
		return nil, fmt.Errorf("主对话模型配置无效: %v", err)
	}
	imgs, err := ParseImages(mg.ImageModels)
	if err != nil {
		return nil, fmt.Errorf("图片模型配置无效: %v", err)
	}
	return &modelGroupRuntime{
		Name:          mg.Name,
		MainText:      main,
		ImageModels:   imgs,
		ImageStrategy: mg.ImageStrategy,
		ImagePrompt:   mg.ImagePrompt,
		ReplaceImage:  mg.ReplaceImage,
	}, nil
}

// recordHistory 异步记录历史(responses 端点)
// userID / imgCount 透传, 实现用户隔离与图片数量统计.
func recordHistory(reqID string, userID uint, g *modelGroupRuntime, req *responsesRequest, hasImage, success, cacheHit bool, imgModelUsed, mainModelUsed string, imgCount, pt, ct int, dur time.Duration, respBytes []byte, errMsg string) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				// 防止异步 goroutine panic 导致进程崩溃
			}
		}()
		h := model.History{
			UserID:           userID,
			RequestID:        reqID,
			ModelGroup:       g.Name,
			Endpoint:         "responses",
			HasImage:         hasImage,
			ImageCount:       imgCount,
			CacheHit:         cacheHit,
			ImageModelUsed:   imgModelUsed,
			MainModelUsed:    mainModelUsed,
			Success:          success,
			ErrorMessage:     errMsg,
			PromptTokens:     pt,
			CompletionTokens: ct,
			TotalTokens:      pt + ct,
			LatencyMs:        dur.Milliseconds(),
			InputSummary:     truncate(string(req.Input), 2000),
			OutputSummary:    truncate(string(respBytes), 2000),
		}
		model.DB.Create(&h)
	}()
}
