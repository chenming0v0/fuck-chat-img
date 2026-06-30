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

// responsesRequestBody 仅提取需要的字段, 其余原样透传
type responsesRequest struct {
	Model    string          `json:"model"`
	Input    json.RawMessage `json:"input"`
	Stream   bool            `json:"stream"`
	StreamOpts struct {
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

	// 计算缓存键(基于规范化后的 input)
	canonical := normalizeResponsesInput(req.Input)
	cacheKey := cache.Key(grp.Name, canonical)

	start := time.Now()
	reqID := "resp_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:24]

	// 1. 命中缓存直接返回
	if cache.Enabled() {
		if e, ok := cache.Get(cacheKey); ok {
			cache.RecordHit()
			replayCacheEntry(c, e)
			recordHistory(reqID, grp, &req, true, true, true, "", "", 0, 0, time.Since(start), e.Value, "cache hit")
			return
		}
		cache.RecordMiss()
	}

	// 2. 解析 input, 提取并识别图片
	hasImage, imgCount, imgModelUsed, modifiedInput, imgErr := processImagesForInput(grp, req.Input)
	if imgErr != nil {
		// 图片识别失败 -> 直接报错(满足需求3)
		recordHistory(reqID, grp, &req, hasImage, false, false, imgModelUsed, "", 0, 0, time.Since(start), nil, imgErr.Error())
		writeErr(c, http.StatusBadGateway, imgErr.Error())
		return
	}

	// 3. 用修改后的 input 重建请求体, 转发到主对话模型
	newBody := rebuildResponsesBody(req.raw, modifiedInput, grp)

	if req.Stream {
		handleResponsesStream(c, grp, newBody, reqID, &req, hasImage, imgCount, imgModelUsed, cacheKey, start)
		return
	}
	handleResponsesNonStream(c, grp, newBody, reqID, &req, hasImage, imgCount, imgModelUsed, cacheKey, start)
}

// processImagesForInput 遍历 input, 识别所有图片并用文本描述替换/追加
// 兼容三种 content 形态:
//   - 数组(标准 content array): 直接图片项 + tool_result 内嵌字符串图片
//   - 字符串(Codex role=tool 输出等): 解析 JSON 后递归识别
func processImagesForInput(g *modelGroupRuntime, input json.RawMessage) (hasImage bool, imgCount int, imgModelUsed string, modified []byte, err error) {
	if len(input) == 0 {
		return false, 0, "", input, nil
	}
	// input 可能是数组或单条对象
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

	for i := range arr {
		// 1. 字符串 content(Codex role=tool 等): 走递归处理器
		if s, ok := arr[i]["content"].(string); ok && s != "" {
			newStr, hasImg, cnt, used, perr := processImagesInStringContent(g, s)
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
			// 2. tool_result / tool_use 项: 内嵌 content 递归处理
			if typ == "tool_result" || typ == "tool_use" {
				if sub, ok := cm["content"].([]interface{}); ok {
					newV, r := processImagesInValue(g, sub)
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
					newStr, hasImg, cnt, used, perr := processImagesInStringContent(g, ss)
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
				// 用文本替换图片项
				cont[j] = textItem
			} else {
				// 追加文本到图片之后(收集, 循环结束后统一追加)
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

func handleResponsesNonStream(c *gin.Context, g *modelGroupRuntime, body []byte, reqID string, req *responsesRequest, hasImage bool, imgCount int, imgModelUsed string, cacheKey string, start time.Time) {
	httpReq, err := http.NewRequest(http.MethodPost, g.MainText.ResponsesURL(), bytes.NewReader(body))
	if err != nil {
		recordHistory(reqID, g, req, hasImage, false, false, imgModelUsed, "", 0, 0, time.Since(start), nil, err.Error())
		writeErr(c, http.StatusInternalServerError, err.Error())
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+g.MainText.APIKey)
	resp, err := sharedHTTPClient.Do(httpReq)
	if err != nil {
		recordHistory(reqID, g, req, hasImage, false, false, imgModelUsed, "", 0, 0, time.Since(start), nil, err.Error())
		writeErr(c, http.StatusBadGateway, "上游请求失败: "+err.Error())
		return
	}
	defer resp.Body.Close()
	respBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		recordHistory(reqID, g, req, hasImage, false, false, imgModelUsed, "", 0, 0, time.Since(start), respBytes, fmt.Sprintf("上游 %d", resp.StatusCode))
		writeErr(c, resp.StatusCode, fmt.Sprintf("上游返回 %d: %s", resp.StatusCode, truncate(string(respBytes), 800)))
		return
	}
	// 写入缓存(非流式)
	if cache.Enabled() {
		cache.Put(cacheKey, g.Name, respBytes)
	}
	pt, ct := extractUsage(respBytes)
	recordHistory(reqID, g, req, hasImage, true, false, imgModelUsed, g.MainText.DisplayName(), pt, ct, time.Since(start), respBytes, "")
	c.Data(http.StatusOK, "application/json", respBytes)
}

func handleResponsesStream(c *gin.Context, g *modelGroupRuntime, body []byte, reqID string, req *responsesRequest, hasImage bool, imgCount int, imgModelUsed string, cacheKey string, start time.Time) {
	httpReq, err := http.NewRequest(http.MethodPost, g.MainText.ResponsesURL(), bytes.NewReader(body))
	if err != nil {
		recordHistory(reqID, g, req, hasImage, false, false, imgModelUsed, "", 0, 0, time.Since(start), nil, err.Error())
		writeErr(c, http.StatusInternalServerError, err.Error())
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Authorization", "Bearer "+g.MainText.APIKey)
	resp, err := sharedStreamHTTPClient.Do(httpReq)
	if err != nil {
		recordHistory(reqID, g, req, hasImage, false, false, imgModelUsed, "", 0, 0, time.Since(start), nil, err.Error())
		writeErr(c, http.StatusBadGateway, "上游请求失败: "+err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBytes, _ := io.ReadAll(resp.Body)
		recordHistory(reqID, g, req, hasImage, false, false, imgModelUsed, "", 0, 0, time.Since(start), respBytes, fmt.Sprintf("上游 %d", resp.StatusCode))
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
			// 透传 SSE 行
			c.Writer.Write(line)
			if flusher != nil {
				flusher.Flush()
			}
			// 收集用于缓存回放与 usage 提取
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
	// 流式缓存回放
	if cache.Enabled() && len(collected) > 0 {
		cache.PutStream(cacheKey, g.Name, collected)
	}
	recordHistory(reqID, g, req, hasImage, true, false, imgModelUsed, g.MainText.DisplayName(), pt, ct, time.Since(start), nil, "")
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

// writeErr 写 OpenAI 风格错误
func writeErr(c *gin.Context, status int, msg string) {
	c.JSON(status, gin.H{
		"error": gin.H{
			"message": msg,
			"type":    "fuck_chat_img_error",
			"code":    status,
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

// recordHistory 异步记录历史
func recordHistory(reqID string, g *modelGroupRuntime, req *responsesRequest, hasImage, success, cacheHit bool, imgModelUsed, mainModelUsed string, pt, ct int, dur time.Duration, respBytes []byte, errMsg string) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				// 防止异步 goroutine panic 导致进程崩溃
			}
		}()
		h := model.History{
			RequestID:        reqID,
			ModelGroup:       g.Name,
			Endpoint:         "responses",
			HasImage:         hasImage,
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
