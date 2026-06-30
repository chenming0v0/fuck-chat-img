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
	"github.com/fuck-chat-img/fci/internal/config"
	"github.com/fuck-chat-img/fci/internal/model"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// chatRequest chat completions 请求
type chatRequest struct {
	Model    string          `json:"model"`
	Messages json.RawMessage `json:"messages"`
	Stream   bool            `json:"stream"`
	raw      json.RawMessage
}

// HandleChat 处理 /v1/chat/completions (将 messages 视作 input, 走同样的图片混合逻辑)
func HandleChat(c *gin.Context) {
	bodyBytes, err := io.ReadAll(c.Request.Body)
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

	// messages 数组规范化缓存键(同样区分 stream 避免污染)
	canonical := normalizeResponsesInput(req.Messages)
	canonical = withStreamFlag(canonical, req.Stream)
	cacheKey := cache.Key(grp.Name, canonical)

	start := time.Now()
	reqID := "chat_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:24]

	if cache.Enabled() {
		if e, ok := cache.Get(cacheKey); ok {
			cache.RecordHit()
			replayCacheEntry(c, e)
			hitHasImage := messagesHasImage(req.Messages)
			pt, ct := 0, 0
			if !e.IsStream {
				pt, ct = extractUsage(e.Value)
			}
			recordChatHistory(reqID, grp, &req, hitHasImage, 0, true, "", grp.MainText.DisplayName(), pt, ct, time.Since(start), e.Value, "cache hit")
			return
		}
		cache.RecordMiss()
	}

	hasImage, imgCount, imgModelUsed, modified, imgErr := processImagesForMessages(grp, req.Messages)
	if imgErr != nil {
		recordChatHistory(reqID, grp, &req, hasImage, imgCount, false, imgModelUsed, "", 0, 0, time.Since(start), nil, imgErr.Error())
		writeErr(c, http.StatusBadGateway, imgErr.Error())
		return
	}
	newBody := rebuildChatBody(req.raw, modified, grp)

	if req.Stream {
		handleChatStream(c, grp, newBody, reqID, &req, hasImage, imgCount, imgModelUsed, cacheKey, start)
		return
	}
	handleChatNonStream(c, grp, newBody, reqID, &req, hasImage, imgCount, imgModelUsed, cacheKey, start)
}

// messagesHasImage 扫描 messages 数组, 判断是否含图片类型(不调用上游)
func messagesHasImage(messages json.RawMessage) bool {
	if len(messages) == 0 {
		return false
	}
	var arr []map[string]interface{}
	if err := json.Unmarshal(messages, &arr); err != nil {
		return false
	}
	for _, item := range arr {
		cont, ok := item["content"].([]interface{})
		if !ok {
			continue
		}
		for _, c := range cont {
			cm, ok := c.(map[string]interface{})
			if !ok {
				continue
			}
			typ, _ := cm["type"].(string)
			if typ == "image_url" || typ == "input_image" || typ == "image" {
				return true
			}
		}
	}
	return false
}

// processImagesForMessages 处理 chat messages 中的图片
func processImagesForMessages(g *modelGroupRuntime, messages json.RawMessage) (hasImage bool, imgCount int, imgModelUsed string, modified []byte, err error) {
	var arr []map[string]interface{}
	if err := json.Unmarshal(messages, &arr); err != nil {
		return false, 0, "", messages, nil
	}
	client := &http.Client{Timeout: time.Duration(config.Get().RequestTimeout) * time.Second}
	for i := range arr {
		cont, ok := arr[i]["content"].([]interface{})
		if !ok {
			continue
		}
		for j := range cont {
			cm, ok := cont[j].(map[string]interface{})
			if !ok {
				continue
			}
			typ, _ := cm["type"].(string)
			if typ != "image_url" && typ != "input_image" && typ != "image" {
				continue
			}
			url, b64, ok := extractImageRef(cm)
			if !ok {
				continue
			}
			hasImage = true
			imgCount++
			imgs := nextImageModels(g)
			desc, used, e := recognizeImage(imgs, g.ImagePrompt, url, b64, client)
			if e != nil {
				return hasImage, imgCount, used, messages, e
			}
			imgModelUsed = used
			textItem := map[string]interface{}{
				"type": "text",
				"text": "[图片识别结果]\n" + desc,
			}
			if g.ReplaceImage {
				cont[j] = textItem
			} else {
				cont = append(cont[:j+1], append([]interface{}{textItem}, cont[j+1:]...)...)
				arr[i]["content"] = cont
			}
		}
	}
	modified, err = json.Marshal(arr)
	if err != nil {
		return hasImage, imgCount, imgModelUsed, messages, err
	}
	return hasImage, imgCount, imgModelUsed, modified, nil
}

func rebuildChatBody(raw json.RawMessage, newMessages []byte, g *modelGroupRuntime) []byte {
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

func handleChatNonStream(c *gin.Context, g *modelGroupRuntime, body []byte, reqID string, req *chatRequest, hasImage bool, imgCount int, imgModelUsed string, cacheKey string, start time.Time) {
	client := &http.Client{Timeout: time.Duration(config.Get().RequestTimeout) * time.Second}
	httpReq, _ := http.NewRequest(http.MethodPost, g.MainText.ChatURL(), bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+g.MainText.APIKey)
	resp, err := client.Do(httpReq)
	if err != nil {
		recordChatHistory(reqID, g, req, hasImage, imgCount, false, imgModelUsed, "", 0, 0, time.Since(start), nil, err.Error())
		writeErr(c, http.StatusBadGateway, "上游请求失败: "+err.Error())
		return
	}
	defer resp.Body.Close()
	respBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		recordChatHistory(reqID, g, req, hasImage, imgCount, false, imgModelUsed, "", 0, 0, time.Since(start), respBytes, fmt.Sprintf("上游 %d", resp.StatusCode))
		writeErr(c, resp.StatusCode, fmt.Sprintf("上游返回 %d: %s", resp.StatusCode, truncate(string(respBytes), 800)))
		return
	}
	if cache.Enabled() {
		cache.Put(cacheKey, g.Name, respBytes)
	}
	pt, ct := extractUsage(respBytes)
	recordChatHistory(reqID, g, req, hasImage, imgCount, true, imgModelUsed, g.MainText.DisplayName(), pt, ct, time.Since(start), respBytes, "")
	c.Data(http.StatusOK, "application/json", respBytes)
}

func handleChatStream(c *gin.Context, g *modelGroupRuntime, body []byte, reqID string, req *chatRequest, hasImage bool, imgCount int, imgModelUsed string, cacheKey string, start time.Time) {
	client := &http.Client{Timeout: time.Duration(config.Get().RequestTimeout*2) * time.Second}
	httpReq, _ := http.NewRequest(http.MethodPost, g.MainText.ChatURL(), bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Authorization", "Bearer "+g.MainText.APIKey)
	resp, err := client.Do(httpReq)
	if err != nil {
		recordChatHistory(reqID, g, req, hasImage, imgCount, false, imgModelUsed, "", 0, 0, time.Since(start), nil, err.Error())
		writeErr(c, http.StatusBadGateway, "上游请求失败: "+err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBytes, _ := io.ReadAll(resp.Body)
		recordChatHistory(reqID, g, req, hasImage, imgCount, false, imgModelUsed, "", 0, 0, time.Since(start), respBytes, fmt.Sprintf("上游 %d", resp.StatusCode))
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
	reader := bufio.NewReaderSize(resp.Body, 64*1024)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			c.Writer.Write(line)
			if flusher != nil {
				flusher.Flush()
			}
			collected = append(collected, line)
			pt, ct = updateUsageFromSSE(line, pt, ct)
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
	summary := summarizeCollectedSSE(collected)
	recordChatHistory(reqID, g, req, hasImage, imgCount, true, imgModelUsed, g.MainText.DisplayName(), pt, ct, time.Since(start), summary, "")
}

// HandleModels /v1/models 返回所有启用的模型组(以及可选直通)
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

func recordChatHistory(reqID string, g *modelGroupRuntime, req *chatRequest, hasImage bool, imgCount int, success bool, imgModelUsed, mainModelUsed string, pt, ct int, dur time.Duration, respBytes []byte, errMsg string) {
	go func() {
		h := model.History{
			RequestID:        reqID,
			ModelGroup:       g.Name,
			Endpoint:         "chat",
			HasImage:         hasImage,
			ImageCount:       imgCount,
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
		model.DB.Create(&h)
	}()
}
