package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
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
	cacheKey := cache.Key("responses", grp.Name, canonical)

	start := time.Now()
	reqID := "resp_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:24]
	userID := extractUserID(c)

	// 1. 命中缓存直接回放(从 entry 回填真实元数据)
	if cache.Enabled() {
		if e, ok := cache.Get(cacheKey); ok {
			cache.RecordHit()
			replayCacheEntry(c, e)
			outSummary := cacheHitOutputSummary(e)
			// 从缓存条目提取真实 usage, 避免历史 token 统计恒为 0
			pt, ct := usageFromCacheEntry(e)
			recordHistory(reqID, userID, grp, &req, e.HasImage, true, true, e.ImageModelUsed, grp.MainText.DisplayName(), e.ImageCount, pt, ct, time.Since(start), outSummary, "cache hit")
			return
		}
		cache.RecordMiss()
	}

	// 2. 解析 input, 提取并识别图片
	hasImage, imgCount, imgModelUsed, modifiedInput, imgErr := processImagesForInput(grp, req.Input, c.Request.Context())
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
func processImagesForInput(g *modelGroupRuntime, input json.RawMessage, ctx context.Context) (hasImage bool, imgCount int, imgModelUsed string, modified []byte, err error) {
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
		if s, ok := arr[i]["content"].(string); ok && s != "" {
			newStr, hasImg, cnt, used, perr := processImagesInStringContentCfg(g, s, cfg, ctx)
			if perr != nil {
				// 失败时优先保留前面已累计的 imgModelUsed, 避免被本次失败的空值覆盖
				return hasImage || hasImg, imgCount + cnt, pickImgModel(imgModelUsed, used), input, perr
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
		var extraItems []interface{}
		for j := 0; j < len(cont); j++ {
			cm, ok := cont[j].(map[string]interface{})
			if !ok {
				continue
			}
			typ, _ := cm["type"].(string)
			if typ == "tool_result" || typ == "tool_use" {
				if sub, ok := cm["content"].([]interface{}); ok {
					newV, r := processImagesInValueCfg(g, sub, cfg, ctx)
					if r.err != nil {
						return hasImage || r.hasImage, imgCount + r.imgCount, pickImgModel(imgModelUsed, r.imgModel), input, r.err
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
					newStr, hasImg, cnt, used, perr := processImagesInStringContentCfg(g, ss, cfg, ctx)
					if perr != nil {
						return hasImage || hasImg, imgCount + cnt, pickImgModel(imgModelUsed, used), input, perr
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
			desc, used, e := recognizeImage(ctx, imgs, g.ImageStrategy, g.ImagePrompt, url, b64, client)
			if e != nil {
				return hasImage, imgCount, pickImgModel(imgModelUsed, used), input, e
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

type upstreamError struct {
	statusCode int
	body       []byte
	msg        string
}

func (e *upstreamError) Error() string { return e.msg }

func fetchResponsesNonStream(g *modelGroupRuntime, body []byte, ctx context.Context, hasImage bool, imgCount int, imgModelUsed string, cacheKey string) (*cache.Entry, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, g.MainText.ResponsesURL(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+g.MainText.APIKey)
	resp, err := sharedHTTPClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("read response body error: %v", err)
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &upstreamError{
			statusCode: resp.StatusCode,
			body:       respBytes,
			msg:        fmt.Sprintf("上游 %d", resp.StatusCode),
		}
	}
	entry := &cache.Entry{
		Key:            cacheKey,
		Value:          respBytes,
		IsStream:       false,
		ModelName:      g.Name,
		HasImage:       hasImage,
		ImageCount:     imgCount,
		ImageModelUsed: imgModelUsed,
	}
	return entry, nil
}

func handleResponsesNonStream(c *gin.Context, g *modelGroupRuntime, body []byte, reqID string, userID uint, req *responsesRequest, hasImage bool, imgCount int, imgModelUsed string, cacheKey string, start time.Time) {
	var entry *cache.Entry
	var fetchErr error
	if cache.Enabled() {
		entry, fetchErr = cache.Do(cacheKey, func() (*cache.Entry, error) {
			e, err := fetchResponsesNonStream(g, body, c.Request.Context(), hasImage, imgCount, imgModelUsed, cacheKey)
			if err != nil {
				return nil, err
			}
			cache.PutWithMeta(cacheKey, g.Name, e.Value, hasImage, imgCount, imgModelUsed)
			return e, nil
		})
	} else {
		entry, fetchErr = fetchResponsesNonStream(g, body, c.Request.Context(), hasImage, imgCount, imgModelUsed, cacheKey)
	}
	if fetchErr != nil {
		if ue, ok := fetchErr.(*upstreamError); ok {
			recordHistory(reqID, userID, g, req, hasImage, false, false, imgModelUsed, "", imgCount, 0, 0, time.Since(start), ue.body, ue.msg)
			writeErr(c, ue.statusCode, fmt.Sprintf("上游返回 %d: %s", ue.statusCode, truncate(string(ue.body), 800)))
		} else {
			recordHistory(reqID, userID, g, req, hasImage, false, false, imgModelUsed, "", imgCount, 0, 0, time.Since(start), nil, fetchErr.Error())
			writeErr(c, http.StatusBadGateway, "上游请求失败: "+fetchErr.Error())
		}
		return
	}
	respBytes := entry.Value
	pt, ct := extractUsage(respBytes)
	recordHistory(reqID, userID, g, req, hasImage, true, false, imgModelUsed, g.MainText.DisplayName(), imgCount, pt, ct, time.Since(start), respBytes, "")
	c.Data(http.StatusOK, "application/json", respBytes)
}

func handleResponsesStream(c *gin.Context, g *modelGroupRuntime, body []byte, reqID string, userID uint, req *responsesRequest, hasImage bool, imgCount int, imgModelUsed string, cacheKey string, start time.Time) {
	// 传播客户端 context: 客户端断连时取消上游请求, 避免上游继续生成造成 token 浪费
	httpReq, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, g.MainText.ResponsesURL(), bytes.NewReader(body))
	if err != nil {
		recordHistory(reqID, userID, g, req, hasImage, false, false, imgModelUsed, "", imgCount, 0, 0, time.Since(start), nil, err.Error())
		writeErr(c, http.StatusInternalServerError, err.Error())
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Authorization", "Bearer "+g.MainText.APIKey)
	// 流式必须使用 sharedStreamHTTPClient(不设整体 Timeout, 避免长 SSE 被切断)
	resp, err := sharedStreamHTTPClient.Do(httpReq)
	if err != nil {
		recordHistory(reqID, userID, g, req, hasImage, false, false, imgModelUsed, "", imgCount, 0, 0, time.Since(start), nil, err.Error())
		writeErr(c, http.StatusBadGateway, "上游请求失败: "+err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBytes, err := io.ReadAll(resp.Body)
		if err != nil {
			log.Printf("read error response body error: %v", err)
		}
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
	clientDisconnected := false
	reader := bufio.NewReaderSize(resp.Body, 64*1024)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			// 真流式: 收到一行立即写出并 Flush
			_, werr := c.Writer.Write(line)
			if flusher != nil {
				flusher.Flush()
			}
			// 检测客户端断连: 写入错误或 context 取消都意味着客户端已离开,
			// 必须立即停止读上游, 避免上游空跑浪费 token + 把未送达响应写入缓存
			if clientGone(c, werr) {
				clientDisconnected = true
				break
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
	if flusher != nil && !clientDisconnected {
		flusher.Flush()
	}
	// 仅在客户端完整接收(未断连)时写入缓存, 防止"用户中途取消的响应"污染缓存
	if cache.Enabled() && len(collected) > 0 && !clientDisconnected {
		cache.PutStreamWithMeta(cacheKey, g.Name, collected, hasImage, imgCount, imgModelUsed)
	}
	var respBytes []byte
	if !clientDisconnected {
		respBytes = bytes.Join(collected, nil)
	}
	// 客户端断连时标记 success=false, 避免历史记录误报成功
	recordHistory(reqID, userID, g, req, hasImage, !clientDisconnected, false, imgModelUsed, g.MainText.DisplayName(), imgCount, pt, ct, time.Since(start), respBytes, clientDisconnectedMsg(clientDisconnected))
}

// updateUsageFromSSE 从 SSE data 行提取 usage
// 兼容 OpenAI(prompt_tokens/completion_tokens)与 Anthropic(input_tokens/output_tokens),
// 并把 Anthropic 的 prompt caching 字段(cache_creation_input_tokens/cache_read_input_tokens)
// 计入 prompt_tokens, 与非流式 extractUsageMessages 保持一致.
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
	u, ok := obj["usage"].(map[string]interface{})
	if !ok {
		if msg, ok := obj["message"].(map[string]interface{}); ok {
			u, _ = msg["usage"].(map[string]interface{})
		}
	}
	if u == nil {
		return pt, ct
	}
	// OpenAI 风格
	if v, ok := u["prompt_tokens"].(float64); ok {
		pt = int(v)
	}
	if v, ok := u["completion_tokens"].(float64); ok {
		ct = int(v)
	}
	// Anthropic 风格(若同时存在则覆盖 OpenAI 值, Anthropic SDK 不发 prompt_tokens)
	if v, ok := u["input_tokens"].(float64); ok {
		pt = int(v)
	}
	if v, ok := u["output_tokens"].(float64); ok {
		ct = int(v)
	}
	cacheCreate := 0
	cacheRead := 0
	if v, ok := u["cache_creation_input_tokens"].(float64); ok {
		cacheCreate = int(v)
	}
	if v, ok := u["cache_read_input_tokens"].(float64); ok {
		cacheRead = int(v)
	}
	if cacheCreate+cacheRead > pt {
		pt = cacheCreate + cacheRead
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
	// OpenAI 风格
	if v, ok := u["prompt_tokens"].(float64); ok {
		pt = int(v)
	}
	if v, ok := u["completion_tokens"].(float64); ok {
		ct = int(v)
	}
	// Anthropic 风格(若同时存在则覆盖 OpenAI 值, Anthropic SDK 不发 prompt_tokens)
	if v, ok := u["input_tokens"].(float64); ok {
		pt = int(v)
	}
	if v, ok := u["output_tokens"].(float64); ok {
		ct = int(v)
	}
	cacheCreate := 0
	cacheRead := 0
	if v, ok := u["cache_creation_input_tokens"].(float64); ok {
		cacheCreate = int(v)
	}
	if v, ok := u["cache_read_input_tokens"].(float64); ok {
		cacheRead = int(v)
	}
	if cacheCreate+cacheRead > pt {
		pt = cacheCreate + cacheRead
	}
	return pt, ct
}

// replayCacheEntry 回放缓存条目(区分流式/非流式)
// 流式命中时逐事件 Flush, 保持真流式语义; 非流式命中直接返回 JSON.
// 回放过程中检测客户端断连, 及时停止, 避免客户端已离开仍 CPU/IO 空跑.
func replayCacheEntry(c *gin.Context, e *cache.Entry) {
	if e.IsStream {
		c.Writer.Header().Set("Content-Type", "text/event-stream")
		c.Writer.Header().Set("Cache-Control", "no-cache")
		c.Writer.Header().Set("Connection", "keep-alive")
		c.Writer.Header().Set("X-Accel-Buffering", "no")
		flusher, _ := c.Writer.(http.Flusher)
		for _, ev := range e.StreamEvents {
			// 客户端已断连则停止回放, 节省 CPU/IO
			if clientGone(c, nil) {
				return
			}
			_, werr := c.Writer.Write(ev)
			if flusher != nil {
				flusher.Flush()
			}
			if werr != nil {
				return
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

// usageFromCacheEntry 从缓存条目提取 token 用量, 避免缓存命中时历史 token 统计恒为 0.
// 非流式: 直接从 Value 解析; 流式: 遍历 StreamEvents 用 updateUsageFromSSE 累计.
// 解析失败返回 (0, 0), 不影响缓存回放本身.
func usageFromCacheEntry(e *cache.Entry) (int, int) {
	if e == nil {
		return 0, 0
	}
	if !e.IsStream {
		// 非流式优先用 messages 协议的提取(兼容 cache_creation_input_tokens);
		// 若解析结果为 0, 回退到通用 extractUsage(兼容 OpenAI prompt_tokens)
		pt, ct := extractUsageMessages(e.Value)
		if pt == 0 && ct == 0 {
			return extractUsage(e.Value)
		}
		return pt, ct
	}
	// 流式: 遍历所有 SSE 事件累计 usage
	pt, ct := 0, 0
	for _, ev := range e.StreamEvents {
		pt, ct = updateUsageFromSSE(ev, pt, ct)
	}
	return pt, ct
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
				log.Printf("recordHistory panic: %v", r)
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
		if err := model.DB.Create(&h).Error; err != nil {
			log.Printf("recordHistory create error: %v", err)
		}
	}()
}
