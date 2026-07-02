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

type messagesRequest struct {
	Model    string          `json:"model"`
	Messages json.RawMessage `json:"messages"`
	System   json.RawMessage `json:"system"`
	Stream   bool            `json:"stream"`
	raw      json.RawMessage
}

func HandleMessages(c *gin.Context) {
	bodyBytes, err := io.ReadAll(io.LimitReader(c.Request.Body, maxRequestBodySize))
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

	contentCanonical := normalizeMessagesForCache(req.Messages, req.System)
	canonical := composeCacheCanonical(paramsFingerprint(req.raw), contentCanonical)
	cacheKey := cache.Key("messages", grp.Name, canonical)

	start := time.Now()
	reqID := "msg_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:24]
	userID := extractUserID(c)

	if cache.Enabled() {
		if e, ok := cache.Get(cacheKey); ok {
			replayCacheEntry(c, e)
			outSummary := cacheHitOutputSummary(e)
			pt, ct := usageFromCacheEntry(e)
			recordMessagesHistory(reqID, userID, grp, &req, e.HasImage, true, true, e.ImageModelUsed, grp.MainText.DisplayName(), e.ImageCount, pt, ct, time.Since(start), outSummary, "cache hit")
			return
		}
	}

	hasImage, imgCount, imgModelUsed, modifiedMessages, modifiedSystem, imgErr := processImagesForMessagesValue(grp, req.Messages, req.System, c.Request.Context())
	if imgErr != nil {
		recordMessagesHistory(reqID, userID, grp, &req, hasImage, false, false, imgModelUsed, "", imgCount, 0, 0, time.Since(start), nil, imgErr.Error())
		var jsonErr *json.SyntaxError
		if errors.As(imgErr, &jsonErr) || strings.HasPrefix(imgErr.Error(), "invalid messages json:") || strings.HasPrefix(imgErr.Error(), "invalid system json:") {
			writeAnthropicErr(c, http.StatusBadRequest, imgErr.Error(), "invalid_request_error")
		} else {
			writeAnthropicErr(c, http.StatusBadGateway, imgErr.Error(), "api_error")
		}
		return
	}

	newBody := rebuildMessagesBody(req.raw, modifiedMessages, modifiedSystem, grp)

	if req.Stream {
		handleMessagesStream(c, grp, newBody, reqID, userID, &req, hasImage, imgCount, imgModelUsed, cacheKey, start)
		return
	}
	handleMessagesNonStream(c, grp, newBody, reqID, userID, &req, hasImage, imgCount, imgModelUsed, cacheKey, start)
}

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

func processImagesForMessagesValue(g *modelGroupRuntime, messages json.RawMessage, system json.RawMessage, ctx context.Context) (hasImage bool, imgCount int, imgModelUsed string, modifiedMessages []byte, modifiedSystem []byte, err error) {
	modifiedMessages = messages
	modifiedSystem = system

	if len(messages) > 0 {
		var v interface{}
		if err := json.Unmarshal(messages, &v); err != nil {
			return false, 0, "", messages, system, fmt.Errorf("invalid messages json: %w", err)
		}
		cfg := defaultImgConfig(g)
		newV, r := processImagesInValueCfg(g, v, cfg, ctx)
		if r.err != nil {
			return r.hasImage, r.imgCount, pickImgModel(imgModelUsed, r.imgModel), messages, system, r.err
		}
		hasImage = r.hasImage
		imgCount = r.imgCount
		imgModelUsed = r.imgModel
		if r.modified {
			newBytes, mErr := json.Marshal(newV)
			if mErr != nil {
				return hasImage, imgCount, imgModelUsed, messages, system, mErr
			}
			modifiedMessages = newBytes
		}
	}

	if len(system) > 0 {
		var v interface{}
		if err := json.Unmarshal(system, &v); err != nil {
			return hasImage, imgCount, imgModelUsed, modifiedMessages, system, fmt.Errorf("invalid system json: %w", err)
		}
		cfg := defaultImgConfig(g)
		newV, r := processImagesInValueCfg(g, v, cfg, ctx)
		if r.err != nil {
			return hasImage || r.hasImage, imgCount + r.imgCount, pickImgModel(imgModelUsed, r.imgModel), modifiedMessages, system, r.err
		}
		if r.hasImage {
			hasImage = true
			imgCount += r.imgCount
			imgModelUsed = pickImgModel(imgModelUsed, r.imgModel)
		}
		if r.modified {
			newBytes, mErr := json.Marshal(newV)
			if mErr != nil {
				return hasImage, imgCount, imgModelUsed, modifiedMessages, system, mErr
			}
			modifiedSystem = newBytes
		}
	}

	return hasImage, imgCount, imgModelUsed, modifiedMessages, modifiedSystem, nil
}

func rebuildMessagesBody(raw json.RawMessage, newMessages []byte, newSystem []byte, g *modelGroupRuntime) []byte {
	var obj map[string]interface{}
	if err := json.Unmarshal(raw, &obj); err != nil {
		log.Printf("rebuildMessagesBody: unmarshal raw failed: %v", err)
		return raw
	}
	var m interface{}
	if err := json.Unmarshal(newMessages, &m); err != nil {
		log.Printf("rebuildMessagesBody: unmarshal newMessages failed: %v", err)
		return raw
	}
	obj["messages"] = m
	if len(newSystem) > 0 {
		var s interface{}
		if err := json.Unmarshal(newSystem, &s); err != nil {
			log.Printf("rebuildMessagesBody: unmarshal newSystem failed: %v", err)
		} else {
			obj["system"] = s
		}
	}
	obj["model"] = g.MainText.Model
	b, err := json.Marshal(obj)
	if err != nil {
		log.Printf("rebuildMessagesBody: marshal failed: %v", err)
		return raw
	}
	return b
}

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

func fetchMessagesNonStream(g *modelGroupRuntime, body []byte, ctx context.Context, c *gin.Context, hasImage bool, imgCount int, imgModelUsed string, cacheKey string) (*cache.Entry, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, g.MainText.MessagesURL(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	applyMessagesAuthHeaders(httpReq, g, c)
	httpReq.Header.Set("Accept", "application/json")
	resp, err := sharedHTTPClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var respBytes []byte
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBytes, err = io.ReadAll(io.LimitReader(resp.Body, maxErrorBodySize))
	} else {
		respBytes, err = io.ReadAll(resp.Body)
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

func handleMessagesNonStream(c *gin.Context, g *modelGroupRuntime, body []byte, reqID string, userID uint, req *messagesRequest, hasImage bool, imgCount int, imgModelUsed string, cacheKey string, start time.Time) {
	var entry *cache.Entry
	var fetchErr error
	timeout := time.Duration(config.Get().RequestTimeout) * time.Second
	if cache.Enabled() {
		entry, fetchErr = cache.Do(cacheKey, func() (*cache.Entry, error) {
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			e, err := fetchMessagesNonStream(g, body, ctx, c, hasImage, imgCount, imgModelUsed, cacheKey)
			if err != nil {
				return nil, err
			}
			cache.PutWithMeta(cacheKey, g.Name, e.Value, hasImage, imgCount, imgModelUsed)
			return e, nil
		})
	} else {
		ctx, cancel := context.WithTimeout(c.Request.Context(), timeout)
		defer cancel()
		entry, fetchErr = fetchMessagesNonStream(g, body, ctx, c, hasImage, imgCount, imgModelUsed, cacheKey)
	}
	if fetchErr != nil {
		if ue, ok := fetchErr.(*upstreamError); ok {
			recordMessagesHistory(reqID, userID, g, req, hasImage, false, false, imgModelUsed, "", imgCount, 0, 0, time.Since(start), ue.body, ue.msg)
			writeAnthropicErr(c, ue.statusCode, fmt.Sprintf("上游返回 %d: %s", ue.statusCode, truncate(string(ue.body), 800)), "api_error")
		} else {
			recordMessagesHistory(reqID, userID, g, req, hasImage, false, false, imgModelUsed, "", imgCount, 0, 0, time.Since(start), nil, fetchErr.Error())
			writeAnthropicErr(c, http.StatusBadGateway, "上游请求失败: "+fetchErr.Error(), "api_error")
		}
		return
	}
	respBytes := entry.Value
	pt, ct := extractUsageMessages(respBytes)
	if pt == 0 && ct == 0 {
		pt, ct = extractUsage(respBytes)
	}
	recordMessagesHistory(reqID, userID, g, req, hasImage, true, false, imgModelUsed, g.MainText.DisplayName(), imgCount, pt, ct, time.Since(start), respBytes, "")
	c.Data(http.StatusOK, "application/json", respBytes)
}

func handleMessagesStream(c *gin.Context, g *modelGroupRuntime, body []byte, reqID string, userID uint, req *messagesRequest, hasImage bool, imgCount int, imgModelUsed string, cacheKey string, start time.Time) {
	httpReq, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, g.MainText.MessagesURL(), bytes.NewReader(body))
	if err != nil {
		recordMessagesHistory(reqID, userID, g, req, hasImage, false, false, imgModelUsed, "", imgCount, 0, 0, time.Since(start), nil, err.Error())
		writeAnthropicErr(c, http.StatusInternalServerError, err.Error(), "api_error")
		return
	}
	applyMessagesAuthHeaders(httpReq, g, c)
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := sharedStreamHTTPClient.Do(httpReq)
	if err != nil {
		recordMessagesHistory(reqID, userID, g, req, hasImage, false, false, imgModelUsed, "", imgCount, 0, 0, time.Since(start), nil, err.Error())
		writeAnthropicErr(c, http.StatusBadGateway, "上游请求失败: "+err.Error(), "api_error")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBytes, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodySize))
		if err != nil {
			log.Printf("read error response body error: %v", err)
		}
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
			collected = append(collected, line)
			if bytes.HasPrefix(bytes.TrimSpace(line), []byte("data:")) {
				pt, ct = updateUsageFromSSE(line, pt, ct)
			}
		}
		if err != nil {
			if err != io.EOF {
				streamErr = true
				log.Printf("messages stream read error: %v", err)
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
	recordMessagesHistory(reqID, userID, g, req, hasImage, success, false, imgModelUsed, g.MainText.DisplayName(), imgCount, pt, ct, time.Since(start), respBytes, errMsg)
}

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

func recordMessagesHistory(reqID string, userID uint, g *modelGroupRuntime, req *messagesRequest, hasImage, success, cacheHit bool, imgModelUsed, mainModelUsed string, imgCount, pt, ct int, dur time.Duration, respBytes []byte, errMsg string) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("recordMessagesHistory panic: %v", r)
			}
		}()
		inputSummary := truncate(string(req.Messages), 2000)
		if len(req.System) > 0 {
			inputSummary = "system: " + truncate(string(req.System), 500) + "\n" + inputSummary
		}
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
		if err := model.DB.Create(&h).Error; err != nil {
			log.Printf("recordMessagesHistory create error: %v", err)
		}
	}()
}
