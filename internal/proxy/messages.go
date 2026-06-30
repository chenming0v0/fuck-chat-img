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

// messagesRequest Claude /v1/messages 请求体(仅提取需要的字段, 其余原样透传)
type messagesRequest struct {
	Model    string          `json:"model"`
	Messages json.RawMessage `json:"messages"`
	System   json.RawMessage `json:"system"`
	Stream   bool            `json:"stream"`
	raw      json.RawMessage
}

// HandleMessages 处理 /v1/messages (Anthropic Claude 兼容)
func HandleMessages(c *gin.Context) {
	bodyBytes, err := io.ReadAll(c.Request.Body)
	if err != nil {
		writeAnthropicErr(c, http.StatusBadRequest, "read body: "+err.Error(), "invalid_request_error")
		return
	}
	var req messagesRequest
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		writeAnthropicErr(c, http.StatusBadRequest, "invalid json: "+err.Error(), "invalid_request_error")
		return
	}
	req.raw = bodyBytes

	if req.Model == "" {
		writeAnthropicErr(c, http.StatusBadRequest, "missing model", "invalid_request_error")
		return
	}

	grp, err := lookupGroup(req.Model)
	if err != nil {
		writeAnthropicErr(c, http.StatusNotFound, err.Error(), "not_found_error")
		return
	}

	// 缓存键: 输出影响参数指纹(stream/max_tokens/temperature/tools...) + 内容规范化(messages+system)
	// 必须纳入 stream, 否则流式与非流式会跨模式命中返回错误响应体
	contentCanonical := normalizeMessagesForCache(req.Messages, req.System)
	canonical := composeCacheCanonical(paramsFingerprint(req.raw), contentCanonical)
	cacheKey := cache.Key(grp.Name, canonical)

	start := time.Now()
	reqID := "msg_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:24]
	userID := extractUserID(c)

	// 1. 缓存命中直接回放(从 entry 回填真实元数据, 不再硬编码)
	if cache.Enabled() {
		if e, ok := cache.Get(cacheKey); ok {
			cache.RecordHit()
			replayCacheEntry(c, e)
			outSummary := cacheHitOutputSummary(e)
			// 从缓存条目提取真实 usage, 避免历史 token 统计恒为 0
			pt, ct := usageFromCacheEntry(e)
			recordMessagesHistory(reqID, userID, grp, &req, e.HasImage, true, true, e.ImageModelUsed, grp.MainText.DisplayName(), e.ImageCount, pt, ct, time.Since(start), outSummary, "cache hit")
			return
		}
		cache.RecordMiss()
	}

	// 2. 递归识别图片(Claude 格式 / OpenAI 兼容 / Codex 嵌套 base64 全部支持)
	hasImage, imgCount, imgModelUsed, modifiedMessages, imgErr := processImagesForMessagesValue(grp, req.Messages)
	if imgErr != nil {
		recordMessagesHistory(reqID, userID, grp, &req, hasImage, false, false, imgModelUsed, "", imgCount, 0, 0, time.Since(start), nil, imgErr.Error())
		writeAnthropicErr(c, http.StatusBadGateway, imgErr.Error(), "api_error")
		return
	}

	// 3. 重建请求体(替换 messages, 保持其它字段)
	newBody := rebuildMessagesBody(req.raw, modifiedMessages, grp)

	if req.Stream {
		handleMessagesStream(c, grp, newBody, reqID, userID, &req, hasImage, imgCount, imgModelUsed, cacheKey, start)
		return
	}
	handleMessagesNonStream(c, grp, newBody, reqID, userID, &req, hasImage, imgCount, imgModelUsed, cacheKey, start)
}

// normalizeMessagesForCache 把 messages + system 一起规范化用于缓存键
func normalizeMessagesForCache(messages json.RawMessage, system json.RawMessage) []byte {
	out := bytes.NewBuffer(nil)
	if len(system) > 0 {
		out.Write([]byte("sys:"))
		out.Write(normalizeForCache(system))
		out.Write([]byte{0})
	}
	if len(messages) > 0 {
		out.Write([]byte("msg:"))
		out.Write(normalizeForCache(messages))
	}
	return out.Bytes()
}

// processImagesForMessagesValue 处理 Claude messages 数组中的图片
// 复用递归处理器 processImagesInValue(尊重 ReplaceImage 配置), 支持:
//   - 直接图片 item(Claude {type:image, source:{...}} / OpenAI {type:image_url, ...})
//   - role=tool 字符串 content(解析后递归)
//   - tool_result item.content 字符串(解析后递归)
//   - 任意嵌套深度
func processImagesForMessagesValue(g *modelGroupRuntime, messages json.RawMessage) (hasImage bool, imgCount int, imgModelUsed string, modified []byte, err error) {
	if len(messages) == 0 {
		return false, 0, "", messages, nil
	}
	var v interface{}
	if err := json.Unmarshal(messages, &v); err != nil {
		return false, 0, "", messages, nil
	}
	newV, r := processImagesInValue(g, v)
	if r.err != nil {
		return r.hasImage, r.imgCount, r.imgModel, messages, r.err
	}
	if !r.modified {
		return r.hasImage, r.imgCount, r.imgModel, messages, nil
	}
	newBytes, mErr := json.Marshal(newV)
	if mErr != nil {
		return r.hasImage, r.imgCount, r.imgModel, messages, nil
	}
	return r.hasImage, r.imgCount, r.imgModel, newBytes, nil
}

// rebuildMessagesBody 用修改后的 messages 重建请求体
func rebuildMessagesBody(raw json.RawMessage, newMessages []byte, g *modelGroupRuntime) []byte {
	var obj map[string]interface{}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return raw
	}
	var m interface{}
	_ = json.Unmarshal(newMessages, &m)
	obj["messages"] = m
	obj["model"] = g.MainText.Model
	b, err := json.Marshal(obj)
	if err != nil {
		return raw
	}
	return b
}

// applyMessagesAuthHeaders 设置 MES 上游鉴权头.
// 同时设置 x-api-key(官方 Anthropic API)与 Authorization: Bearer(Bearer 兼容网关),
// 由上游择一识别, 兼容两种部署形态. anthropic-version 缺省 2023-06-01.
func applyMessagesAuthHeaders(httpReq *http.Request, g *modelGroupRuntime, c *gin.Context) {
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", g.MainText.APIKey)
	httpReq.Header.Set("Authorization", "Bearer "+g.MainText.APIKey)
	if v := c.GetHeader("anthropic-version"); v != "" {
		httpReq.Header.Set("anthropic-version", v)
	} else {
		httpReq.Header.Set("anthropic-version", "2023-06-01")
	}
	if v := c.GetHeader("anthropic-beta"); v != "" {
		httpReq.Header.Set("anthropic-beta", v)
	}
}

func handleMessagesNonStream(c *gin.Context, g *modelGroupRuntime, body []byte, reqID string, userID uint, req *messagesRequest, hasImage bool, imgCount int, imgModelUsed string, cacheKey string, start time.Time) {
	// 传播客户端 context: 客户端断连时取消上游请求, 避免 token 浪费
	httpReq, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, g.MainText.MessagesURL(), bytes.NewReader(body))
	if err != nil {
		recordMessagesHistory(reqID, userID, g, req, hasImage, false, false, imgModelUsed, "", imgCount, 0, 0, time.Since(start), nil, err.Error())
		writeAnthropicErr(c, http.StatusInternalServerError, err.Error(), "api_error")
		return
	}
	applyMessagesAuthHeaders(httpReq, g, c)

	resp, err := sharedHTTPClient.Do(httpReq)
	if err != nil {
		recordMessagesHistory(reqID, userID, g, req, hasImage, false, false, imgModelUsed, "", imgCount, 0, 0, time.Since(start), nil, err.Error())
		writeAnthropicErr(c, http.StatusBadGateway, "上游请求失败: "+err.Error(), "api_error")
		return
	}
	defer resp.Body.Close()
	respBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		recordMessagesHistory(reqID, userID, g, req, hasImage, false, false, imgModelUsed, "", imgCount, 0, 0, time.Since(start), respBytes, fmt.Sprintf("上游 %d", resp.StatusCode))
		writeAnthropicErr(c, resp.StatusCode, fmt.Sprintf("上游返回 %d: %s", resp.StatusCode, truncate(string(respBytes), 800)), "api_error")
		return
	}
	if cache.Enabled() {
		cache.PutWithMeta(cacheKey, g.Name, respBytes, hasImage, imgCount, imgModelUsed)
	}
	pt, ct := extractUsageMessages(respBytes)
	recordMessagesHistory(reqID, userID, g, req, hasImage, true, false, imgModelUsed, g.MainText.DisplayName(), imgCount, pt, ct, time.Since(start), respBytes, "")
	c.Data(http.StatusOK, "application/json", respBytes)
}

func handleMessagesStream(c *gin.Context, g *modelGroupRuntime, body []byte, reqID string, userID uint, req *messagesRequest, hasImage bool, imgCount int, imgModelUsed string, cacheKey string, start time.Time) {
	// 传播客户端 context: 客户端断连时取消上游请求, 避免上游继续生成造成 token 浪费
	httpReq, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, g.MainText.MessagesURL(), bytes.NewReader(body))
	if err != nil {
		recordMessagesHistory(reqID, userID, g, req, hasImage, false, false, imgModelUsed, "", imgCount, 0, 0, time.Since(start), nil, err.Error())
		writeAnthropicErr(c, http.StatusInternalServerError, err.Error(), "api_error")
		return
	}
	applyMessagesAuthHeaders(httpReq, g, c)
	httpReq.Header.Set("Accept", "text/event-stream")

	// 流式必须使用 sharedStreamHTTPClient(不设整体 Timeout, 避免长 SSE 被切断)
	resp, err := sharedStreamHTTPClient.Do(httpReq)
	if err != nil {
		recordMessagesHistory(reqID, userID, g, req, hasImage, false, false, imgModelUsed, "", imgCount, 0, 0, time.Since(start), nil, err.Error())
		writeAnthropicErr(c, http.StatusBadGateway, "上游请求失败: "+err.Error(), "api_error")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBytes, _ := io.ReadAll(resp.Body)
		recordMessagesHistory(reqID, userID, g, req, hasImage, false, false, imgModelUsed, "", imgCount, 0, 0, time.Since(start), respBytes, fmt.Sprintf("上游 %d", resp.StatusCode))
		writeAnthropicErr(c, resp.StatusCode, fmt.Sprintf("上游返回 %d: %s", resp.StatusCode, truncate(string(respBytes), 800)), "api_error")
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
			// 真流式: 收到一行立即写出并 Flush, 不缓冲
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
	// 客户端断连时标记 success=false, 避免历史记录误报成功
	recordMessagesHistory(reqID, userID, g, req, hasImage, !clientDisconnected, false, imgModelUsed, g.MainText.DisplayName(), imgCount, pt, ct, time.Since(start), nil, clientDisconnectedMsg(clientDisconnected))
}

// extractUsageMessages 从 Claude 非流式响应中提取 token 用量
// Claude 格式: {"usage":{"input_tokens":10,"output_tokens":5,"cache_creation_input_tokens":..,"cache_read_input_tokens":..}}
func extractUsageMessages(b []byte) (int, int) {
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
	// Anthropic prompt caching 字段也计入, 便于额度统计
	if v, ok := u["cache_creation_input_tokens"].(float64); ok {
		pt += int(v)
	}
	if v, ok := u["cache_read_input_tokens"].(float64); ok {
		pt += int(v)
	}
	return pt, ct
}

// recordMessagesHistory 异步记录历史(messages 端点)
// userID 由代理 handler 从 gin.Context 提取并透传, 实现 History 用户隔离.
// imgCount 透传到 History.ImageCount, 避免恒为 0.
func recordMessagesHistory(reqID string, userID uint, g *modelGroupRuntime, req *messagesRequest, hasImage, success, cacheHit bool, imgModelUsed, mainModelUsed string, imgCount, pt, ct int, dur time.Duration, respBytes []byte, errMsg string) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				// 防止异步 goroutine panic 导致进程崩溃
			}
		}()
		inputSummary := truncate(string(req.Messages), 2000)
		h := model.History{
			UserID:           userID,
			RequestID:        reqID,
			ModelGroup:       g.Name,
			Endpoint:         "messages",
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
			InputSummary:     inputSummary,
			OutputSummary:    truncate(string(respBytes), 2000),
		}
		model.DB.Create(&h)
	}()
}
