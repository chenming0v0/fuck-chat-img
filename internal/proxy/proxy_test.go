package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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
	mainJSON, _ := json.Marshal(model.UpstreamModel{BaseURL: imgSrv.URL, APIKey: "sk-img", Model: "vision-1"})
	_ = mainJSON
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

	// 第三次: 字段顺序不同但语义相同, 也应命中缓存
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
		t.Fatalf("乱序请求应命中缓存, status=%d", w3.Code)
	}
	if *imgCalls != 1 || *mainCalls != 1 {
		t.Errorf("乱序同语义请求应命中缓存, img=%d main=%d", *imgCalls, *mainCalls)
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

func init() {
	// 确保时间引用(避免某些环境下 unused 警告)
	_ = time.Now
}
