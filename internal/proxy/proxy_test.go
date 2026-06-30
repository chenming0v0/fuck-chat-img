package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fuck-chat-img/fci/internal/cache"
	"github.com/fuck-chat-img/fci/internal/config"
	"github.com/fuck-chat-img/fci/internal/model"
	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupTestEnv 构建一个带模拟上游的测试环境
func setupTestEnv(t *testing.T) (*gin.Engine, *int, *int, *[]byte) {
	t.Helper()
	// 内存数据库
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared&_busy_timeout=5000"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	db.AutoMigrate(&model.User{}, &model.ModelGroup{}, &model.History{})
	// 清空共享内存库的残留
	db.Exec("DELETE FROM model_groups")
	db.Exec("DELETE FROM histories")
	model.DB = db

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
	mainCalls := 0
	lastInput := []byte{}
	mainSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mainCalls++
		lastInput, _ = io.ReadAll(r.Body)
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
	r.POST("/v1/responses", HandleResponses)
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
	r, imgCalls, mainCalls, _ := setupTestEnv(t)

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
	_ = imgCalls
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
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared&_busy_timeout=5000"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	db.AutoMigrate(&model.User{}, &model.ModelGroup{}, &model.History{})
	db.Exec("DELETE FROM model_groups")
	db.Exec("DELETE FROM histories")
	model.DB = db

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
