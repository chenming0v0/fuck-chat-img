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

type chatRequest struct {
	Model    string          `json:"model"`
	Messages json.RawMessage `json:"messages"`
	Stream   bool            `json:"stream"`
	raw      json.RawMessage
}

func HandleChat(c *gin.Context) {
	bodyBytes, err := io.ReadAll(io.LimitReader(c.Request.Body, maxRequestBodySize))
	if err != nil {
		writeErr(c, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	var req chatRequest
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

	contentCanonical := normalizeForCache(req.Messages)
	canonical := composeCacheCanonical(paramsFingerprint(req.raw), contentCanonical)
	cacheKey := cache.Key("chat", grp.Name, canonical)

	start := time.Now()
	reqID := "chat_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:24]
	userID := extractUserID(c)

	if cache.Enabled() {
		if e, ok := cache.Get(cacheKey); ok {
			replayCacheEntry(c, e)
			outSummary := cacheHitOutputSummary(e)
			pt, ct := usageFromCacheEntry(e)
			recordChatHistory(reqID, userID, grp, &req, e.HasImage, true, true, e.ImageModelUsed, grp.MainText.DisplayName(), e.ImageCount, pt, ct, time.Since(start), outSummary, "cache hit")
			return
		}
	}

	hasImage, imgCount, imgModelUsed, modified, imgErr := processImagesForChatMessages(grp, req.Messages, c.Request.Context())
	if imgErr != nil {
		recordChatHistory(reqID, userID, grp, &req, hasImage, false, false, imgModelUsed, "", imgCount, 0, 0, time.Since(start), nil, imgErr.Error())
		var jsonErr *json.SyntaxError
		if errors.As(imgErr, &jsonErr) || strings.HasPrefix(imgErr.Error(), "invalid messages json:") {
			writeErr(c, http.StatusBadRequest, imgErr.Error())
		} else {
			writeErr(c, http.StatusBadGateway, imgErr.Error())
		}
		return
	}
	newBody := rebuildChatBody(req.raw, modified, grp)

	if req.Stream {
		handleChatStream(c, grp, newBody, reqID, userID, &req, hasImage, imgCount, imgModelUsed, cacheKey, start)
		return
	}
	handleChatNonStream(c, grp, newBody, reqID, userID, &req, hasImage, imgCount, imgModelUsed, cacheKey, start)
}

func processImagesForChatMessages(g *modelGroupRuntime, messages json.RawMessage, ctx context.Context) (hasImage bool, imgCount int, imgModelUsed string, modified []byte, err error) {
	if len(messages) == 0 {
		return false, 0, "", messages, nil
	}
	var v interface{}
	if err := json.Unmarshal(messages, &v); err != nil {
		return false, 0, "", messages, fmt.Errorf("invalid messages json: %w", err)
	}
	cfg := defaultImgConfig(g)
	newV, r := processImagesInValueCfg(g, v, cfg, ctx)
	if r.err != nil {
		return r.hasImage, r.imgCount, pickImgModel(imgModelUsed, r.imgModel), messages, r.err
	}
	hasImage = r.hasImage
	imgCount = r.imgCount
	imgModelUsed = r.imgModel
	if !r.modified {
		return hasImage, imgCount, imgModelUsed, messages, nil
	}
	newBytes, mErr := json.Marshal(newV)
	if mErr != nil {
		return hasImage, imgCount, imgModelUsed, messages, mErr
	}
	return hasImage, imgCount, imgModelUsed, newBytes, nil
}

func processImagesForMessages(g *modelGroupRuntime, messages json.RawMessage) (hasImage bool, imgCount int, imgModelUsed string, modified []byte, err error) {
	return processImagesForChatMessages(g, messages, context.Background())
}

func rebuildChatBody(raw json.RawMessage, newMessages []byte, g *modelGroupRuntime) []byte {
	var obj map[string]interface{}
	if err := json.Unmarshal(raw, &obj); err != nil {
		log.Printf("rebuildChatBody: unmarshal raw failed: %v", err)
		return raw
	}
	var m interface{}
	if err := json.Unmarshal(newMessages, &m); err != nil {
		log.Printf("rebuildChatBody: unmarshal newMessages failed: %v", err)
		return raw
	}
	obj["messages"] = m
	obj["model"] = g.MainText.Model
	b, err := json.Marshal(obj)
	if err != nil {
		log.Printf("rebuildChatBody: marshal failed: %v", err)
		return raw
	}
	return b
}

func fetchChatNonStream(g *modelGroupRuntime, body []byte, ctx context.Context, hasImage bool, imgCount int, imgModelUsed string, cacheKey string) (*cache.Entry, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, g.MainText.ChatURL(), bytes.NewReader(body))
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

func handleChatNonStream(c *gin.Context, g *modelGroupRuntime, body []byte, reqID string, userID uint, req *chatRequest, hasImage bool, imgCount int, imgModelUsed string, cacheKey string, start time.Time) {
	var entry *cache.Entry
	var fetchErr error
	timeout := time.Duration(config.Get().RequestTimeout) * time.Second
	if cache.Enabled() {
		entry, fetchErr = cache.Do(cacheKey, func() (*cache.Entry, error) {
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			e, err := fetchChatNonStream(g, body, ctx, hasImage, imgCount, imgModelUsed, cacheKey)
			if err != nil {
				return nil, err
			}
			cache.PutWithMeta(cacheKey, g.Name, e.Value, hasImage, imgCount, imgModelUsed)
			return e, nil
		})
	} else {
		ctx, cancel := context.WithTimeout(c.Request.Context(), timeout)
		defer cancel()
		entry, fetchErr = fetchChatNonStream(g, body, ctx, hasImage, imgCount, imgModelUsed, cacheKey)
	}
	if fetchErr != nil {
		if ue, ok := fetchErr.(*upstreamError); ok {
			recordChatHistory(reqID, userID, g, req, hasImage, false, false, imgModelUsed, "", imgCount, 0, 0, time.Since(start), ue.body, ue.msg)
			writeErr(c, ue.statusCode, fmt.Sprintf("上游返回 %d: %s", ue.statusCode, truncate(string(ue.body), 800)))
		} else {
			recordChatHistory(reqID, userID, g, req, hasImage, false, false, imgModelUsed, "", imgCount, 0, 0, time.Since(start), nil, fetchErr.Error())
			writeErr(c, http.StatusBadGateway, "上游请求失败: "+fetchErr.Error())
		}
		return
	}
	respBytes := entry.Value
	pt, ct := extractUsageMessages(respBytes)
	if pt == 0 && ct == 0 {
		pt, ct = extractUsage(respBytes)
	}
	recordChatHistory(reqID, userID, g, req, hasImage, true, false, imgModelUsed, g.MainText.DisplayName(), imgCount, pt, ct, time.Since(start), respBytes, "")
	c.Data(http.StatusOK, "application/json", respBytes)
}

func handleChatStream(c *gin.Context, g *modelGroupRuntime, body []byte, reqID string, userID uint, req *chatRequest, hasImage bool, imgCount int, imgModelUsed string, cacheKey string, start time.Time) {
	httpReq, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, g.MainText.ChatURL(), bytes.NewReader(body))
	if err != nil {
		recordChatHistory(reqID, userID, g, req, hasImage, false, false, imgModelUsed, "", imgCount, 0, 0, time.Since(start), nil, err.Error())
		writeErr(c, http.StatusInternalServerError, err.Error())
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Authorization", "Bearer "+g.MainText.APIKey)
	resp, err := sharedStreamHTTPClient.Do(httpReq)
	if err != nil {
		recordChatHistory(reqID, userID, g, req, hasImage, false, false, imgModelUsed, "", imgCount, 0, 0, time.Since(start), nil, err.Error())
		writeErr(c, http.StatusBadGateway, "上游请求失败: "+err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBytes, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodySize))
		if err != nil {
			log.Printf("read error response body error: %v", err)
		}
		recordChatHistory(reqID, userID, g, req, hasImage, false, false, imgModelUsed, "", imgCount, 0, 0, time.Since(start), respBytes, fmt.Sprintf("上游 %d", resp.StatusCode))
		writeErr(c, resp.StatusCode, fmt.Sprintf("上游返回 %d: %s", resp.StatusCode, truncate(string(respBytes), 800)))
		return
	}
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	flusher, _ := c.Writer.(http.Flusher)
	var collected [][]byte
	pt, ct := 0, 0
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
				log.Printf("chat stream read error: %v", err)
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
	recordChatHistory(reqID, userID, g, req, hasImage, success, false, imgModelUsed, g.MainText.DisplayName(), imgCount, pt, ct, time.Since(start), respBytes, errMsg)
}

func HandleModels(c *gin.Context) {
	var groups []model.ModelGroup
	model.DB.Where("enabled = ?", true).Find(&groups)
	data := make([]gin.H, 0, len(groups))
	for _, g := range groups {
		data = append(data, gin.H{
			"id":       g.Name,
			"object":   "model",
			"created":  g.CreatedAt.Unix(),
			"owned_by": "fuck-chat-img",
		})
	}
	c.JSON(http.StatusOK, gin.H{"object": "list", "data": data})
}

func recordChatHistory(reqID string, userID uint, g *modelGroupRuntime, req *chatRequest, hasImage, success, cacheHit bool, imgModelUsed, mainModelUsed string, imgCount, pt, ct int, dur time.Duration, respBytes []byte, errMsg string) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("recordChatHistory panic: %v", r)
			}
		}()
		h := model.History{
			UserID:           userID,
			RequestID:        reqID,
			ModelGroup:       g.Name,
			Endpoint:         "chat",
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
			InputSummary:     truncate(string(req.Messages), 2000),
			OutputSummary:    truncate(string(respBytes), 2000),
		}
		if err := model.DB.Create(&h).Error; err != nil {
			log.Printf("recordChatHistory create error: %v", err)
		}
	}()
}
