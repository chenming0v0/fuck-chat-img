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
//
// Claude 消息格式:
//
//	{
//	  "model": "...",
//	  "system": "...",              // 可选, 字符串或 content 数组
//	  "messages": [
//	    {"role":"user","content":[
//	      {"type":"text","text":"这是什么?"},
//	      {"type":"image","source":{"type":"base64","media_type":"image/png","data":"..."}}
//	    ]},
//	    {"role":"assistant","content":[...]},
//	    {"role":"user","content":"字符串也可"}
//	  ],
//	  "stream": false,
//	  "max_tokens": 1024
//	}
//
// 也兼容 Codex agent 工具:
//   - role=tool 的消息 content 是 JSON 字符串, 内含 base64 图片
//   - role=user 的 content 数组中 type=tool_result 的 item, content 是 JSON 字符串
type messagesRequest struct {
	Model    string          `json:"model"`
	Messages json.RawMessage `json:"messages"`
	System   json.RawMessage `json:"system"`
	Stream   bool            `json:"stream"`
	raw      json.RawMessage
}

// HandleMessages 处理 /v1/messages (Anthropic Claude 兼容)
//
// 流程:
//  1. 解析请求, 用规范化的 messages 计算缓存键
//  2. 命中缓存直接回放(区分流式/非流式)
//  3. 未命中: 递归识别所有图片(Claude image+source / OpenAI 兼容 / Codex 嵌套 base64),
//     把图片替换为 {type:text, text:"[图片识别结果]\n..."} 后转发到上游 /messages
//  4. 写回缓存(非流式存响应体, 流式存 SSE 事件序列)
//  5. 异步记录历史
func HandleMessages(c *gin.Context) {
	bodyBytes, err := io.ReadAll(c.Request.Body)
	if err != nil {
		writeErr(c, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	var req messagesRequest
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

	// 用规范化后的 messages 计算缓存键(含 system 与 messages, 保证语义相同即命中)
	canonical := normalizeMessagesForCache(req.Messages, req.System)
	cacheKey := cache.Key(grp.Name, canonical)

	start := time.Now()
	reqID := "msg_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:24]

	// 1. 缓存命中直接回放
	if cache.Enabled() {
		if e, ok := cache.Get(cacheKey); ok {
			cache.RecordHit()
			replayCacheEntry(c, e)
			recordMessagesHistory(reqID, grp, &req, true, true, true, "", "", 0, 0, time.Since(start), e.Value, "cache hit")
			return
		}
		cache.RecordMiss()
	}

	// 2. 递归识别图片(Claude 格式 / OpenAI 兼容 / Codex 嵌套 base64 全部支持)
	hasImage, imgCount, imgModelUsed, modifiedMessages, imgErr := processImagesForMessagesValue(grp, req.Messages)
	if imgErr != nil {
		recordMessagesHistory(reqID, grp, &req, hasImage, false, false, imgModelUsed, "", 0, 0, time.Since(start), nil, imgErr.Error())
		writeErr(c, http.StatusBadGateway, imgErr.Error())
		return
	}

	// 3. 重建请求体(替换 messages, 保持其它字段)
	newBody := rebuildMessagesBody(req.raw, modifiedMessages, grp)

	if req.Stream {
		handleMessagesStream(c, grp, newBody, reqID, &req, hasImage, imgCount, imgModelUsed, cacheKey, start)
		return
	}
	handleMessagesNonStream(c, grp, newBody, reqID, &req, hasImage, imgCount, imgModelUsed, cacheKey, start)
}

// normalizeMessagesForCache 把 messages + system 一起规范化用于缓存键
// system 不影响消息顺序语义, 单独哈希后拼接到规范化结果前
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
// 复用递归处理器 processImagesInValue, 支持:
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

func handleMessagesNonStream(c *gin.Context, g *modelGroupRuntime, body []byte, reqID string, req *messagesRequest, hasImage bool, imgCount int, imgModelUsed string, cacheKey string, start time.Time) {
	httpReq, err := http.NewRequest(http.MethodPost, g.MainText.MessagesURL(), bytes.NewReader(body))
	if err != nil {
		recordMessagesHistory(reqID, g, req, hasImage, false, false, imgModelUsed, "", 0, 0, time.Since(start), nil, err.Error())
		writeErr(c, http.StatusInternalServerError, err.Error())
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	// Claude API 需要 anthropic-version 头, 由客户端透传; 也支持 Authorization Bearer
	httpReq.Header.Set("Authorization", "Bearer "+g.MainText.APIKey)
	// 透传 anthropic-version / anthropic-beta 等头(若客户端传入)
	if v := c.GetHeader("anthropic-version"); v != "" {
		httpReq.Header.Set("anthropic-version", v)
	} else {
		httpReq.Header.Set("anthropic-version", "2023-06-01")
	}
	if v := c.GetHeader("anthropic-beta"); v != "" {
		httpReq.Header.Set("anthropic-beta", v)
	}

	resp, err := sharedHTTPClient.Do(httpReq)
	if err != nil {
		recordMessagesHistory(reqID, g, req, hasImage, false, false, imgModelUsed, "", 0, 0, time.Since(start), nil, err.Error())
		writeErr(c, http.StatusBadGateway, "上游请求失败: "+err.Error())
		return
	}
	defer resp.Body.Close()
	respBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		recordMessagesHistory(reqID, g, req, hasImage, false, false, imgModelUsed, "", 0, 0, time.Since(start), respBytes, fmt.Sprintf("上游 %d", resp.StatusCode))
		writeErr(c, resp.StatusCode, fmt.Sprintf("上游返回 %d: %s", resp.StatusCode, truncate(string(respBytes), 800)))
		return
	}
	if cache.Enabled() {
		cache.Put(cacheKey, g.Name, respBytes)
	}
	pt, ct := extractUsageMessages(respBytes)
	recordMessagesHistory(reqID, g, req, hasImage, true, false, imgModelUsed, g.MainText.DisplayName(), pt, ct, time.Since(start), respBytes, "")
	c.Data(http.StatusOK, "application/json", respBytes)
}

func handleMessagesStream(c *gin.Context, g *modelGroupRuntime, body []byte, reqID string, req *messagesRequest, hasImage bool, imgCount int, imgModelUsed string, cacheKey string, start time.Time) {
	httpReq, err := http.NewRequest(http.MethodPost, g.MainText.MessagesURL(), bytes.NewReader(body))
	if err != nil {
		recordMessagesHistory(reqID, g, req, hasImage, false, false, imgModelUsed, "", 0, 0, time.Since(start), nil, err.Error())
		writeErr(c, http.StatusInternalServerError, err.Error())
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Authorization", "Bearer "+g.MainText.APIKey)
	if v := c.GetHeader("anthropic-version"); v != "" {
		httpReq.Header.Set("anthropic-version", v)
	} else {
		httpReq.Header.Set("anthropic-version", "2023-06-01")
	}
	if v := c.GetHeader("anthropic-beta"); v != "" {
		httpReq.Header.Set("anthropic-beta", v)
	}

	resp, err := sharedStreamHTTPClient.Do(httpReq)
	if err != nil {
		recordMessagesHistory(reqID, g, req, hasImage, false, false, imgModelUsed, "", 0, 0, time.Since(start), nil, err.Error())
		writeErr(c, http.StatusBadGateway, "上游请求失败: "+err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBytes, _ := io.ReadAll(resp.Body)
		recordMessagesHistory(reqID, g, req, hasImage, false, false, imgModelUsed, "", 0, 0, time.Since(start), respBytes, fmt.Sprintf("上游 %d", resp.StatusCode))
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
		cache.PutStream(cacheKey, g.Name, collected)
	}
	recordMessagesHistory(reqID, g, req, hasImage, true, false, imgModelUsed, g.MainText.DisplayName(), pt, ct, time.Since(start), nil, "")
}

// extractUsageMessages 从 Claude 非流式响应中提取 token 用量
// Claude 格式: {"usage":{"input_tokens":10,"output_tokens":5}}
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
	return pt, ct
}

// recordMessagesHistory 异步记录历史(messages 端点)
func recordMessagesHistory(reqID string, g *modelGroupRuntime, req *messagesRequest, hasImage, success, cacheHit bool, imgModelUsed, mainModelUsed string, pt, ct int, dur time.Duration, respBytes []byte, errMsg string) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				// 防止异步 goroutine panic 导致进程崩溃
			}
		}()
		// messages 请求体可能很大, 截取前 2000 字符作为输入摘要
		inputSummary := truncate(string(req.Messages), 2000)
		h := model.History{
			RequestID:        reqID,
			ModelGroup:       g.Name,
			Endpoint:         "messages",
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
			InputSummary:     inputSummary,
			OutputSummary:    truncate(string(respBytes), 2000),
		}
		model.DB.Create(&h)
	}()
}
