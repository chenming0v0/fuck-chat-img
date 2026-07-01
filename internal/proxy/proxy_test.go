package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fuck-chat-img/fci/internal/cache"
	"github.com/fuck-chat-img/fci/internal/config"
	"github.com/fuck-chat-img/fci/internal/model"
	"github.com/gin-gonic/gin"
)

// setupTestEnv 构建一个带模拟上游的测试环境
func setupTestEnv(t *testing.T) (*gin.Engine, *int, *int, *[]byte) {
	t.Helper()
	if err := model.InitTestDB("file::memory:?cache=shared"); err != nil {
		t.Fatal(err)
	}
	db := model.DB
	db.Exec("DELETE FROM model_groups")
	db.Exec("DELETE FROM histories")
	db.Exec("DELETE FROM users")

	cfg := config.Get()
	cfg.CacheEnabled = true
	cfg.CacheMaxItems = 1000
	cfg.RequestTimeout = 10
	cache.Init()

	// 模拟图片模型: 返回固定描述
	imgCalls := 0
	imgSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		imgCalls++
		body, _ := io.ReadAll(r.Body)
		var obj map[string]interface{}
		json.Unmarshal(body, &obj)
		// 校验请求体确实包含 image_url
		js, _ := json.Marshal(obj)
		if !bytes.Contains(js, []byte("image_url")) {
			t.Errorf("图片模型未收到 image_url, body=%s", string(js))
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"图片里是一只猫"}}]}`))
	}))

	// 模拟主对话模型: 记录收到的 input, 返回 responses 格式
	// 流式请求(Accept: text/event-stream)返回 SSE, 非流式返回 JSON
	mainCalls := 0
	lastInput := []byte{}
	mainSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mainCalls++
		lastInput, _ = io.ReadAll(r.Body)
		if r.Header.Get("Accept") == "text/event-stream" {
			// 真流式 SSE: 逐事件写出
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			fmt.Fprintf(w, "data: {\"type\":\"message_start\",\"usage\":{\"input_tokens\":10}}\n\n")
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			fmt.Fprintf(w, "data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":5}}\n\n")
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			fmt.Fprintf(w, "data: [DONE]\n\n")
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"resp_test","object":"response","model":"gpt-4o","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"这是一只猫"}]}],"usage":{"input_tokens":10,"output_tokens":5}}`))
	}))

	// 写入一个模型组
	mainConf, _ := json.Marshal(model.UpstreamModel{BaseURL: mainSrv.URL, APIKey: "sk-main", Model: "gpt-4o"})
	imgConf, _ := json.Marshal([]model.UpstreamModel{{BaseURL: imgSrv.URL, APIKey: "sk-img", Model: "vision-1"}})
	mg := model.ModelGroup{
		Name:          "mixed-vision",
		MainTextModel: string(mainConf),
		ImageModels:   string(imgConf),
		ImageStrategy: "round_robin",
		ImagePrompt:   "描述图片",
		ReplaceImage:  true,
		Enabled:       true,
	}
	if err := db.Create(&mg).Error; err != nil {
		t.Fatal(err)
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	// 注册三协议路由, 便于各协议测试复用同一环境
	r.POST("/v1/responses", HandleResponses)
	r.POST("/v1/messages", HandleMessages)
	r.POST("/v1/chat/completions", HandleChat)
	r.GET("/v1/models", HandleModels)

	t.Cleanup(func() {
		imgSrv.Close()
		mainSrv.Close()
	})
	return r, &imgCalls, &mainCalls, &lastInput
}

func TestResponsesImageMixingAndCache(t *testing.T) {
	r, imgCalls, mainCalls, lastInput := setupTestEnv(t)

	reqBody := `{
		"model": "mixed-vision",
		"input": [
			{"role":"user","content":[
				{"type":"input_text","text":"这是什么?"},
				{"type":"input_image","image_url":"https://example.com/cat.png"}
			]}
		],
		"stream": false
	}`

	// 第一次请求: 应识别图片并转发主模型
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(reqBody))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("第一次请求失败 status=%d body=%s", w.Code, w.Body.String())
	}
	if *imgCalls != 1 {
		t.Errorf("图片模型应被调用1次, 实际 %d", *imgCalls)
	}
	if *mainCalls != 1 {
		t.Errorf("主模型应被调用1次, 实际 %d", *mainCalls)
	}
	// 校验主模型收到的 input 中图片已被替换为文本(不含 input_image, 含识别结果)
	if bytes.Contains(*lastInput, []byte("input_image")) {
		t.Errorf("主模型收到的 input 不应再含 input_image, body=%s", string(*lastInput))
	}
	if !bytes.Contains(*lastInput, []byte("图片识别结果")) {
		t.Errorf("主模型收到的 input 应含图片识别结果文本, body=%s", string(*lastInput))
	}

	// 校验传给主模型的请求体不含 input_image 且含图片识别结果文本
	// (lastInput 在闭包外不可直接取, 这里通过缓存命中验证)
	if !bytes.Contains(w.Body.Bytes(), []byte("一只猫")) {
		t.Errorf("响应应包含主模型输出, body=%s", w.Body.String())
	}

	// 第二次相同请求: 应命中缓存, 不再调用上游
	req2 := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(reqBody))
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("第二次请求失败 status=%d body=%s", w2.Code, w2.Body.String())
	}
	if *imgCalls != 1 {
		t.Errorf("缓存命中后图片模型不应再被调用, 实际 %d", *imgCalls)
	}
	if *mainCalls != 1 {
		t.Errorf("缓存命中后主模型不应再被调用, 实际 %d", *mainCalls)
	}

	// 第三次: content 数组顺序不同 -> 语义不同, 不应命中缓存
	reqBody3 := `{
		"model": "mixed-vision",
		"input": [
			{"role":"user","content":[
				{"type":"input_image","image_url":"https://example.com/cat.png"},
				{"type":"input_text","text":"这是什么?"}
			]}
		],
		"stream": false
	}`
	req3 := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(reqBody3))
	w3 := httptest.NewRecorder()
	r.ServeHTTP(w3, req3)
	if w3.Code != http.StatusOK {
		t.Fatalf("第三次请求失败 status=%d body=%s", w3.Code, w3.Body.String())
	}
	// content 数组顺序变化 -> 语义不同 -> 应调用上游
	if *imgCalls != 2 {
		t.Errorf("content 顺序变化语义不同, 图片模型应被调用, 期望 2, 实际 %d", *imgCalls)
	}
	if *mainCalls != 2 {
		t.Errorf("content 顺序变化语义不同, 主模型应被调用, 期望 2, 实际 %d", *mainCalls)
	}

	// 第四次: 对象字段顺序不同但数组顺序相同 -> 语义相同, 应命中缓存
	reqBody4 := `{
		"stream": false,
		"model": "mixed-vision",
		"input": [
			{"content":[
				{"type":"input_text","text":"这是什么?"},
				{"type":"input_image","image_url":"https://example.com/cat.png"}
			],"role":"user"}
		]
	}`
	req4 := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(reqBody4))
	w4 := httptest.NewRecorder()
	r.ServeHTTP(w4, req4)
	if w4.Code != http.StatusOK {
		t.Fatalf("字段顺序不同的请求应命中缓存, status=%d", w4.Code)
	}
	// 字段顺序不同但数组顺序相同 -> 语义相同 -> 命中缓存, 不调用上游
	if *imgCalls != 2 || *mainCalls != 2 {
		t.Errorf("字段顺序不同但语义相同应命中缓存, img=%d main=%d", *imgCalls, *mainCalls)
	}
}

func TestImageRecognitionFailureReturnsError(t *testing.T) {
	r, _, mainCalls, _ := setupTestEnv(t)

	// 让图片模型返回错误: 直接通过 DB 改成指向一个会 500 的上游
	errSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"model down"}`))
	}))
	t.Cleanup(errSrv.Close)
	errImg, _ := json.Marshal([]model.UpstreamModel{{BaseURL: errSrv.URL, APIKey: "sk", Model: "bad-vision"}})
	model.DB.Model(&model.ModelGroup{}).Where("name = ?", "mixed-vision").Update("image_models", string(errImg))

	reqBody := `{"model":"mixed-vision","input":[{"role":"user","content":[{"type":"input_text","text":"x"},{"type":"input_image","image_url":"https://example.com/cat.png"}]}],"stream":false}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(reqBody))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code == http.StatusOK {
		t.Fatalf("图片识别失败时应返回错误, 但得到了 200: %s", w.Body.String())
	}
	if *mainCalls != 0 {
		t.Errorf("图片失败时不应调用主模型, 实际 %d", *mainCalls)
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("图片识别失败")) {
		t.Errorf("错误信息应包含'图片识别失败', body=%s", w.Body.String())
	}
}

func TestModelsEndpoint(t *testing.T) {
	r, _, _, _ := setupTestEnv(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("models 接口 status=%d", w.Code)
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("mixed-vision")) {
		t.Errorf("models 应包含模型组名, body=%s", w.Body.String())
	}
}

// setupMessagesTestEnv 构建一个用于 /v1/messages (Claude) 端到端测试的环境
// 模拟图片模型返回固定描述, 模拟上游 Claude API 返回固定 message
func setupMessagesTestEnv(t *testing.T) (*gin.Engine, *int, *int) {
	t.Helper()
	if err := model.InitTestDB("file::memory:?cache=shared"); err != nil {
		t.Fatal(err)
	}
	db := model.DB
	db.Exec("DELETE FROM model_groups")
	db.Exec("DELETE FROM histories")
	db.Exec("DELETE FROM users")

	cfg := config.Get()
	cfg.CacheEnabled = true
	cfg.CacheMaxItems = 1000
	cfg.RequestTimeout = 10
	cache.Init()

	imgCalls := 0
	imgSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		imgCalls++
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"图片里是一只猫"}}]}`))
	}))

	mainCalls := 0
	mainSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mainCalls++
		// 校验: 转发到上游的 messages 中不应再含原始 base64 图片
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte("iVBORw0KGgo")) {
			t.Errorf("转发到上游的 messages 不应包含原始 base64 图片数据, body=%s", truncate(string(body), 500))
		}
		// 上游应当透传 anthropic-version 头
		if v := r.Header.Get("anthropic-version"); v == "" {
			t.Errorf("上游应透传 anthropic-version 头")
		}
		// 流式请求返回 SSE, 非流式返回 JSON
		if r.Header.Get("Accept") == "text/event-stream" {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			fmt.Fprintf(w, "data: {\"type\":\"message_start\",\"usage\":{\"input_tokens\":10}}\n\n")
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			fmt.Fprintf(w, "data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":5}}\n\n")
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			fmt.Fprintf(w, "data: [DONE]\n\n")
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"msg_test","type":"message","role":"assistant","content":[{"type":"text","text":"这是一只猫"}],"usage":{"input_tokens":10,"output_tokens":5}}`))
	}))

	mainConf, _ := json.Marshal(model.UpstreamModel{BaseURL: mainSrv.URL, APIKey: "sk-main", Model: "claude-3"})
	imgConf, _ := json.Marshal([]model.UpstreamModel{{BaseURL: imgSrv.URL, APIKey: "sk-img", Model: "vision-1"}})
	mg := model.ModelGroup{
		Name:          "claude-vision",
		MainTextModel: string(mainConf),
		ImageModels:   string(imgConf),
		ImageStrategy: "round_robin",
		ImagePrompt:   "描述图片",
		ReplaceImage:  true,
		Enabled:       true,
	}
	if err := db.Create(&mg).Error; err != nil {
		t.Fatal(err)
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/v1/messages", HandleMessages)

	t.Cleanup(func() {
		imgSrv.Close()
		mainSrv.Close()
	})
	return r, &imgCalls, &mainCalls
}

// TestMessagesImageAndCache 端到端验证 Claude /v1/messages 流程:
// 1. 第一次请求: Claude image+source 格式 -> 图片识别 + 转发上游 + 写缓存
// 2. 第二次相同请求: 命中缓存, 图片模型与上游都不再调用
// 3. 第三次: 同图片但外层 id/created_at 不同 -> 仍命中缓存(规范化剥离易变字段)
func TestMessagesImageAndCache(t *testing.T) {
	r, imgCalls, mainCalls := setupMessagesTestEnv(t)

	// 1. Claude image+source 格式
	reqBody := `{
		"model": "claude-vision",
		"messages": [{"role":"user","content":[
			{"type":"text","text":"这是什么?"},
			{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYPhfDwAChwGA60L6UwAAAABJRU5ErkJggg=="}}
		]}],
		"stream": false,
		"max_tokens": 100
	}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewBufferString(reqBody))
	req.Header.Set("anthropic-version", "2023-06-01")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("第一次请求失败 status=%d body=%s", w.Code, w.Body.String())
	}
	if *imgCalls != 1 {
		t.Errorf("图片模型应被调用1次, 实际 %d", *imgCalls)
	}
	if *mainCalls != 1 {
		t.Errorf("上游应被调用1次, 实际 %d", *mainCalls)
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("一只猫")) {
		t.Errorf("响应应包含主模型输出, body=%s", w.Body.String())
	}

	// 2. 第二次相同请求: 应命中缓存
	req2 := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewBufferString(reqBody))
	req2.Header.Set("anthropic-version", "2023-06-01")
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("第二次请求失败 status=%d body=%s", w2.Code, w2.Body.String())
	}
	if *imgCalls != 1 || *mainCalls != 1 {
		t.Errorf("缓存命中后不应再调用上游/图片模型, img=%d main=%d", *imgCalls, *mainCalls)
	}
	if !bytes.Contains(w2.Body.Bytes(), []byte("一只猫")) {
		t.Errorf("缓存回放应返回相同内容, body=%s", w2.Body.String())
	}

	// 3. 第三次: 同图片, 外层 id 不同 -> 仍应命中缓存
	reqBody3 := `{
		"id": "req-different-id",
		"created_at": 99999,
		"model": "claude-vision",
		"messages": [{"role":"user","content":[
			{"type":"text","text":"这是什么?"},
			{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYPhfDwAChwGA60L6UwAAAABJRU5ErkJggg=="}}
		]}],
		"stream": false,
		"max_tokens": 100
	}`
	req3 := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewBufferString(reqBody3))
	req3.Header.Set("anthropic-version", "2023-06-01")
	w3 := httptest.NewRecorder()
	r.ServeHTTP(w3, req3)
	if w3.Code != http.StatusOK {
		t.Fatalf("第三次请求失败 status=%d body=%s", w3.Code, w3.Body.String())
	}
	if *imgCalls != 1 || *mainCalls != 1 {
		t.Errorf("同图片+易变字段差异应命中缓存, img=%d main=%d", *imgCalls, *mainCalls)
	}
}

// TestMessagesCodexToolForm 端到端验证 Codex 工具形态:
// role=tool 消息 content 是 JSON 字符串, 内含 base64 图片 -> 应识别并替换
func TestMessagesCodexToolForm(t *testing.T) {
	r, imgCalls, mainCalls := setupMessagesTestEnv(t)

	// Codex 形态: role=tool 的 content 是 JSON 字符串, 字符串内含 base64 data URL
	inner := `{"image":"data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYPhfDwAChwGA60L6UwAAAABJRU5ErkJggg==","tool":"view_image"}`
	innerEscaped := strings.ReplaceAll(strings.ReplaceAll(inner, `\`, `\\`), `"`, `\"`)
	reqBody := `{
		"model": "claude-vision",
		"messages": [
			{"role":"user","content":"请看截图"},
			{"role":"assistant","content":"我来查看"},
			{"role":"tool","content":"` + innerEscaped + `"}
		],
		"stream": false
	}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewBufferString(reqBody))
	req.Header.Set("anthropic-version", "2023-06-01")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("Codex tool 形态请求失败 status=%d body=%s", w.Code, w.Body.String())
	}
	if *imgCalls != 1 {
		t.Errorf("图片模型应被调用1次, 实际 %d", *imgCalls)
	}
	if *mainCalls != 1 {
		t.Errorf("上游应被调用1次, 实际 %d", *mainCalls)
	}
}

// TestMessagesStreamVsNonStreamCacheIsolation 回归: 流式与非流式必须使用不同缓存键.
// 历史缺陷: cacheKey 未纳入 stream, 导致先非流式后流式(同 messages)命中同一缓存,
// 流式客户端拿到一个 JSON blob 而非 SSE, 解析失败.
func TestMessagesStreamVsNonStreamCacheIsolation(t *testing.T) {
	r, _, mainCalls := setupMessagesTestEnv(t)

	body := `{"model":"claude-vision","messages":[{"role":"user","content":"hi"}],"max_tokens":100,"stream":false}`

	// 1. 非流式请求 -> 上游调用 1 次, 响应应是 JSON
	req1 := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewBufferString(body))
	req1.Header.Set("anthropic-version", "2023-06-01")
	w1 := httptest.NewRecorder()
	r.ServeHTTP(w1, req1)
	if w1.Code != http.StatusOK {
		t.Fatalf("非流式请求失败 status=%d body=%s", w1.Code, w1.Body.String())
	}
	if ct := w1.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("非流式响应 Content-Type 应为 application/json, 实际 %s", ct)
	}
	if *mainCalls != 1 {
		t.Fatalf("非流式应调用上游1次, 实际 %d", *mainCalls)
	}

	// 2. 同 messages 但 stream=true -> 必须不命中缓存, 上游再调用1次, 响应应是 SSE
	bodyStream := `{"model":"claude-vision","messages":[{"role":"user","content":"hi"}],"max_tokens":100,"stream":true}`
	req2 := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewBufferString(bodyStream))
	req2.Header.Set("anthropic-version", "2023-06-01")
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("流式请求失败 status=%d body=%s", w2.Code, w2.Body.String())
	}
	if *mainCalls != 2 {
		t.Errorf("流式不应命中非流式缓存, 上游应被调用第2次, 实际 %d", *mainCalls)
	}
	if ct := w2.Header().Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Errorf("流式响应 Content-Type 应为 text/event-stream, 实际 %s", ct)
	}
	if !bytes.Contains(w2.Body.Bytes(), []byte("data:")) {
		t.Errorf("流式响应体应包含 SSE data: 行, body=%s", w2.Body.String())
	}

	// 3. 再来一次同 stream=true -> 这次应命中流式缓存, 上游不再调用
	req3 := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewBufferString(bodyStream))
	req3.Header.Set("anthropic-version", "2023-06-01")
	w3 := httptest.NewRecorder()
	r.ServeHTTP(w3, req3)
	if w3.Code != http.StatusOK {
		t.Fatalf("流式缓存命中请求失败 status=%d", w3.Code)
	}
	if *mainCalls != 2 {
		t.Errorf("流式缓存命中后上游不应再调用, 实际 %d", *mainCalls)
	}
	if ct := w3.Header().Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Errorf("流式缓存回放 Content-Type 应仍为 text/event-stream, 实际 %s", ct)
	}
}

// TestMessagesMaxTokensCacheIsolation 回归: 不同 max_tokens 必须不命中同一缓存.
// 历史缺陷: cacheKey 仅规范化 messages, 遗漏 max_tokens, 导致 max_tokens=100 与 =4096
// 命中同一条缓存返回截断内容(Claude max_tokens 必填, 碰撞概率高).
func TestMessagesMaxTokensCacheIsolation(t *testing.T) {
	r, _, mainCalls := setupMessagesTestEnv(t)

	// 1. max_tokens=100
	b1 := `{"model":"claude-vision","messages":[{"role":"user","content":"写一首诗"}],"max_tokens":100,"stream":false}`
	req1 := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewBufferString(b1))
	req1.Header.Set("anthropic-version", "2023-06-01")
	w1 := httptest.NewRecorder()
	r.ServeHTTP(w1, req1)
	if w1.Code != http.StatusOK {
		t.Fatalf("请求1失败 status=%d", w1.Code)
	}
	if *mainCalls != 1 {
		t.Fatalf("请求1应调用上游1次, 实际 %d", *mainCalls)
	}

	// 2. 同 messages 但 max_tokens=4096 -> 不应命中缓存
	b2 := `{"model":"claude-vision","messages":[{"role":"user","content":"写一首诗"}],"max_tokens":4096,"stream":false}`
	req2 := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewBufferString(b2))
	req2.Header.Set("anthropic-version", "2023-06-01")
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("请求2失败 status=%d", w2.Code)
	}
	if *mainCalls != 2 {
		t.Errorf("不同 max_tokens 不应命中同一缓存, 上游应调用第2次, 实际 %d", *mainCalls)
	}

	// 3. 再发 max_tokens=100 -> 命中缓存, 上游不再调用
	req3 := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewBufferString(b1))
	req3.Header.Set("anthropic-version", "2023-06-01")
	w3 := httptest.NewRecorder()
	r.ServeHTTP(w3, req3)
	if *mainCalls != 2 {
		t.Errorf("相同 max_tokens 应命中缓存, 上游不应再调用, 实际 %d", *mainCalls)
	}
}

// TestMessagesAnthropicErrorFormat 回归: MES 错误响应必须是 Anthropic 格式,
// 不能用 OpenAI 的 {"error":{"message":...}}. Claude SDK 依赖 {"type":"error","error":{...}}.
func TestMessagesAnthropicErrorFormat(t *testing.T) {
	r, _, _ := setupMessagesTestEnv(t)

	// 不存在的模型组 -> 应返回 Anthropic 格式错误
	bad := `{"model":"not-exist","messages":[{"role":"user","content":"hi"}],"max_tokens":100}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewBufferString(bad))
	req.Header.Set("anthropic-version", "2023-06-01")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code == http.StatusOK {
		t.Fatalf("不存在的模型组应返回非200")
	}
	var obj map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &obj); err != nil {
		t.Fatalf("错误响应不是合法 JSON: %v, body=%s", err, w.Body.String())
	}
	if obj["type"] != "error" {
		t.Errorf("Anthropic 错误响应顶层 type 应为 'error', 实际 %v, body=%s", obj["type"], w.Body.String())
	}
	errObj, ok := obj["error"].(map[string]interface{})
	if !ok {
		t.Fatalf("Anthropic 错误响应应含 error 对象, body=%s", w.Body.String())
	}
	if _, ok := errObj["message"].(string); !ok {
		t.Errorf("error.message 应为字符串, body=%s", w.Body.String())
	}
}
