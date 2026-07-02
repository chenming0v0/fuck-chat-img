package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/fuck-chat-img/fci/internal/cache"
	"github.com/fuck-chat-img/fci/internal/config"
	"github.com/fuck-chat-img/fci/internal/model"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type responsesRequest struct {
	Model        string          `json:"model"`
	Input        json.RawMessage `json:"input"`
	Instructions string          `json:"instructions"`
	Stream       bool            `json:"stream"`
	StreamOpts   struct {
		Include []string `json:"include"`
	} `json:"stream_options"`
	raw json.RawMessage
}

func HandleResponses(c *gin.Context) {
	bodyBytes, err := io.ReadAll(io.LimitReader(c.Request.Body, maxRequestBodySize))
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

	contentCanonical := normalizeResponsesInput(req.Input, req.Instructions)
	canonical := composeCacheCanonical(paramsFingerprint(req.raw), contentCanonical)
	cacheKey := cache.Key("responses", grp.Name, canonical)

	start := time.Now()
	reqID := "resp_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:24]
	userID := extractUserID(c)

	if cache.Enabled() {
		if e, ok := cache.Get(cacheKey); ok {
			replayCacheEntry(c, e)
			outSummary := cacheHitOutputSummary(e)
			pt, ct := usageFromCacheEntry(e)
			recordHistory(reqID, userID, grp, &req, e.HasImage, true, true, e.ImageModelUsed, grp.MainText.DisplayName(), e.ImageCount, pt, ct, time.Since(start), outSummary, "cache hit")
			return
		}
	}

	hasImage, imgCount, imgModelUsed, modifiedInput, imgErr := processImagesForInput(grp, req.Input, c.Request.Context())
	if imgErr != nil {
		recordHistory(reqID, userID, grp, &req, hasImage, false, false, imgModelUsed, "", imgCount, 0, 0, time.Since(start), nil, imgErr.Error())
		var jsonErr *json.SyntaxError
		if errors.As(imgErr, &jsonErr) || strings.HasPrefix(imgErr.Error(), "invalid input json:") {
			writeErr(c, http.StatusBadRequest, imgErr.Error())
		} else {
			writeErr(c, http.StatusBadGateway, imgErr.Error())
		}
		return
	}

	newBody := rebuildResponsesBody(req.raw, modifiedInput, grp)

	if req.Stream {
		handleResponsesStream(c, grp, newBody, reqID, userID, &req, hasImage, imgCount, imgModelUsed, cacheKey, start)
		return
	}
	handleResponsesNonStream(c, grp, newBody, reqID, userID, &req, hasImage, imgCount, imgModelUsed, cacheKey, start)
}

func normalizeResponsesInput(input json.RawMessage, instructions string) []byte {
	out := bytes.NewBuffer(nil)
	if instructions != "" {
		out.Write([]byte("inst:"))
		instBytes, _ := json.Marshal(instructions)
		out.Write(normalizeForCache(instBytes))
		out.Write([]byte{0})
	}
	if len(input) > 0 {
		out.Write([]byte("in:"))
		out.Write(normalizeForCache(input))
	}
	return out.Bytes()
}

// responsesImgConfig Responses 协议的图片处理配置: 文本项类型为 input_text
// replaceImage=true 时图片项被替换为文本描述; false 时图片项保留, 文本描述追加到后面
func responsesImgConfig(g *modelGroupRuntime) imgProcessConfig {
	return imgProcessConfig{
		replaceImage: g.ReplaceImage,
		textType:     "input_text",
	}
}

// processImagesForInput 遍历 input, 识别所有图片并用文本描述替换/追加
// 兼容三种 input 形态:
//   - 数组(标准 content array)
//   - 单个对象
//   - 纯字符串(Codex 等场景, 可能包含 data URL 或 JSON 字符串内的图片)
func processImagesForInput(g *modelGroupRuntime, input json.RawMessage, ctx context.Context) (hasImage bool, imgCount int, imgModelUsed string, modified []byte, err error) {
	if len(input) == 0 {
		return false, 0, "", input, nil
	}
	cfg := responsesImgConfig(g)

	var v interface{}
	if err := json.Unmarshal(input, &v); err != nil {
		return false, 0, "", input, fmt.Errorf("invalid input json: %w", err)
	}

	newV, r := processImagesInValueCfg(g, v, cfg, ctx)
	if r.err != nil {
		return r.hasImage, r.imgCount, pickImgModel(imgModelUsed, r.imgModel), input, r.err
	}
	if !r.modified {
		return r.hasImage, r.imgCount, r.imgModel, input, nil
	}
	newBytes, mErr := json.Marshal(newV)
	if mErr != nil {
		return r.hasImage, r.imgCount, r.imgModel, input, mErr
	}
	return r.hasImage, r.imgCount, r.imgModel, newBytes, nil
}

func rebuildResponsesBody(raw json.RawMessage, newInput []byte, g *modelGroupRuntime) []byte {
	var obj map[string]interface{}
	if err := json.Unmarshal(raw, &obj); err != nil {
		log.Printf("rebuildResponsesBody: unmarshal raw failed: %v", err)
		return raw
	}
	var in interface{}
	if err := json.Unmarshal(newInput, &in); err != nil {
		log.Printf("rebuildResponsesBody: unmarshal newInput failed: %v", err)
		return raw
	}
	obj["input"] = in
	obj["model"] = g.MainText.Model
	b, err := json.Marshal(obj)
	if err != nil {
		log.Printf("rebuildResponsesBody: marshal failed: %v", err)
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
	var respBytes []byte
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBytes, err = io.ReadAll(io.LimitReader(resp.Body, maxErrorBodySize))
	} else {
		respBytes, err = io.ReadAll(io.LimitReader(resp.Body, maxResponseBodySize))
	}
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
	timeout := time.Duration(config.Get().RequestTimeout) * time.Second
	if cache.Enabled() {
		entry, fetchErr = cache.Do(cacheKey, func() (*cache.Entry, error) {
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			e, err := fetchResponsesNonStream(g, body, ctx, hasImage, imgCount, imgModelUsed, cacheKey)
			if err != nil {
				return nil, err
			}
			cache.PutWithMeta(cacheKey, g.Name, e.Value, hasImage, imgCount, imgModelUsed)
			return e, nil
		})
	} else {
		ctx, cancel := context.WithTimeout(c.Request.Context(), timeout)
		defer cancel()
		entry, fetchErr = fetchResponsesNonStream(g, body, ctx, hasImage, imgCount, imgModelUsed, cacheKey)
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
	pt, ct := extractUsageMessages(respBytes)
	if pt == 0 && ct == 0 {
		pt, ct = extractUsage(respBytes)
	}
	recordHistory(reqID, userID, g, req, hasImage, true, false, imgModelUsed, g.MainText.DisplayName(), imgCount, pt, ct, time.Since(start), respBytes, "")
	c.Data(http.StatusOK, "application/json", respBytes)
}

func handleResponsesStream(c *gin.Context, g *modelGroupRuntime, body []byte, reqID string, userID uint, req *responsesRequest, hasImage bool, imgCount int, imgModelUsed string, cacheKey string, start time.Time) {
	httpReq, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, g.MainText.ResponsesURL(), bytes.NewReader(body))
	if err != nil {
		recordHistory(reqID, userID, g, req, hasImage, false, false, imgModelUsed, "", imgCount, 0, 0, time.Since(start), nil, err.Error())
		writeErr(c, http.StatusInternalServerError, err.Error())
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Authorization", "Bearer "+g.MainText.APIKey)
	resp, err := sharedStreamHTTPClient.Do(httpReq)
	if err != nil {
		recordHistory(reqID, userID, g, req, hasImage, false, false, imgModelUsed, "", imgCount, 0, 0, time.Since(start), nil, err.Error())
		writeErr(c, http.StatusBadGateway, "上游请求失败: "+err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBytes, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodySize))
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
	var collectedBytes int
	var pt, ct int
	clientDisconnected := false
	streamErr := false
	reader := bufio.NewReaderSize(resp.Body, 64*1024)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			_, werr := c.Writer.Write(line)
			if flusher != nil {
				flusher.Flush()
			}
			if clientGone(c, werr) {
				clientDisconnected = true
				break
			}
			if collectedBytes+len(line) <= maxResponseBodySize {
				collected = append(collected, line)
				collectedBytes += len(line)
			}
			if bytes.HasPrefix(bytes.TrimSpace(line), []byte("data:")) {
				pt, ct = updateUsageFromSSE(line, pt, ct)
			}
		}
		if err != nil {
			if err != io.EOF {
				streamErr = true
				log.Printf("responses stream read error: %v", err)
			}
			break
		}
	}
	if flusher != nil && !clientDisconnected {
		flusher.Flush()
	}
	success := !clientDisconnected && !streamErr
	if cache.Enabled() && len(collected) > 0 && success {
		cache.PutStreamWithMeta(cacheKey, g.Name, collected, hasImage, imgCount, imgModelUsed)
	}
	var respBytes []byte
	var errMsg string
	if success {
		respBytes = bytes.Join(collected, nil)
	} else if clientDisconnected {
		errMsg = clientDisconnectedMsg(clientDisconnected)
	} else if streamErr {
		errMsg = "stream read error"
	}
	recordHistory(reqID, userID, g, req, hasImage, success, false, imgModelUsed, g.MainText.DisplayName(), imgCount, pt, ct, time.Since(start), respBytes, errMsg)
}

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
	if v, ok := u["prompt_tokens"].(float64); ok {
		pt = int(v)
	}
	if v, ok := u["completion_tokens"].(float64); ok {
		ct = int(v)
	}
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
	if v, ok := u["prompt_tokens"].(float64); ok {
		pt = int(v)
	}
	if v, ok := u["completion_tokens"].(float64); ok {
		ct = int(v)
	}
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

func replayCacheEntry(c *gin.Context, e *cache.Entry) {
	if e.IsStream {
		c.Writer.Header().Set("Content-Type", "text/event-stream")
		c.Writer.Header().Set("Cache-Control", "no-cache")
		c.Writer.Header().Set("Connection", "keep-alive")
		c.Writer.Header().Set("X-Accel-Buffering", "no")
		flusher, _ := c.Writer.(http.Flusher)
		for _, ev := range e.StreamEvents {
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

func cacheHitOutputSummary(e *cache.Entry) []byte {
	if e == nil {
		return nil
	}
	if !e.IsStream {
		return e.Value
	}
	var sb strings.Builder
	for _, ev := range e.StreamEvents {
		sb.Write(ev)
		if sb.Len() >= 2000 {
			break
		}
	}
	return []byte(truncate(sb.String(), 2000))
}

func usageFromCacheEntry(e *cache.Entry) (int, int) {
	if e == nil {
		return 0, 0
	}
	if !e.IsStream {
		pt, ct := extractUsageMessages(e.Value)
		if pt == 0 && ct == 0 {
			return extractUsage(e.Value)
		}
		return pt, ct
	}
	pt, ct := 0, 0
	for _, ev := range e.StreamEvents {
		pt, ct = updateUsageFromSSE(ev, pt, ct)
	}
	return pt, ct
}

func writeErr(c *gin.Context, status int, msg string) {
	c.JSON(status, gin.H{
		"error": gin.H{
			"message": msg,
			"type":    "fuck_chat_img_error",
			"code":    status,
		},
	})
}

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

func recordHistory(reqID string, userID uint, g *modelGroupRuntime, req *responsesRequest, hasImage, success, cacheHit bool, imgModelUsed, mainModelUsed string, imgCount, pt, ct int, dur time.Duration, respBytes []byte, errMsg string) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("recordHistory panic: %v", r)
			}
		}()
		inputSummary := truncate(string(req.Input), 2000)
		if req.Instructions != "" {
			inputSummary = "instructions: " + truncate(req.Instructions, 500) + "\n" + inputSummary
		}
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
			InputSummary:     inputSummary,
			OutputSummary:    truncate(string(respBytes), 2000),
		}
		if err := model.DB.Create(&h).Error; err != nil {
			log.Printf("recordHistory create error: %v", err)
		}
	}()
}
