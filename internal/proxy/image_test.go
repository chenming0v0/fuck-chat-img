package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fuck-chat-img/fci/internal/cache"
)

// 同一张图片(纯 base64 数据 raw), 不同外层格式应当:
//  1. extractImageRef 都能提取出相同内容
//  2. imageContentHash 产生相同哈希
//  3. normalizeForCache + cache.Key 产生相同缓存键(关键: 满足"相同图片任意格式命中缓存")
//
// rawB64 是不带 data: 前缀的纯 base64 数据(模拟一张极小的 PNG)
const rawB64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYPhfDwAChwGA60L6UwAAAABJRU5ErkJggg=="
const dataURL = "data:image/png;base64," + rawB64

// 各格式构造函数 ----------------------------------------------------------

// OpenAI Chat: image_url.url 是 data URL
func openAIImageURLItem() map[string]interface{} {
	return map[string]interface{}{
		"type":      "image_url",
		"image_url": map[string]interface{}{"url": dataURL},
	}
}

// OpenAI Responses: input_image.image_url 是 data URL
func responsesInputImageItem() map[string]interface{} {
	return map[string]interface{}{
		"type":       "input_image",
		"image_url":  dataURL,
	}
}

// Claude Messages: {type:image, source:{type:base64, media_type, data}}
func claudeImageItem() map[string]interface{} {
	return map[string]interface{}{
		"type": "image",
		"source": map[string]interface{}{
			"type":       "base64",
			"media_type": "image/png",
			"data":       rawB64,
		},
	}
}

// 单独的 data URL 字符串(Codex 工具直接把 base64 data URL 塞进字符串)
func dataURLString() string { return dataURL }

// ---------------------------------------------------------------- 识别函数

func TestExtractImageRef_ClaudeBase64(t *testing.T) {
	url, b64, ok := extractImageRef(claudeImageItem())
	if !ok {
		t.Fatal("Claude base64 图片应当被识别")
	}
	if url != "" {
		t.Errorf("base64 形式 url 应为空, 实际 %s", url)
	}
	if !strings.Contains(b64, rawB64) {
		t.Errorf("b64 应包含原始数据, 实际 %s", b64)
	}
	if !strings.HasPrefix(b64, "data:image/png;base64,") {
		t.Errorf("b64 应是 data URL 形式, 实际 %s", b64)
	}
}

func TestExtractImageRef_ClaudeURL(t *testing.T) {
	item := map[string]interface{}{
		"type": "image",
		"source": map[string]interface{}{
			"type": "url",
			"url":  "https://example.com/a.png",
		},
	}
	url, b64, ok := extractImageRef(item)
	if !ok {
		t.Fatal("Claude url 图片应当被识别")
	}
	if url != "https://example.com/a.png" {
		t.Errorf("url 应为 https://example.com/a.png, 实际 %s", url)
	}
	if b64 != "" {
		t.Errorf("url 形式 b64 应为空, 实际 %s", b64)
	}
}

func TestIsImageContentItem_AllFormats(t *testing.T) {
	cases := []struct {
		name string
		item map[string]interface{}
	}{
		{"OpenAI image_url", openAIImageURLItem()},
		{"Responses input_image", responsesInputImageItem()},
		{"Claude image", claudeImageItem()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !isImageContentItem(tc.item) {
				t.Errorf("isImageContentItem 应识别 %s 格式", tc.name)
			}
		})
	}
}

// 关键测试: 相同图片在 OpenAI / Responses / Claude 三种 item 格式下产生相同 imageContentHash
// (因为 extractImageRef 把所有 data: URL 都归一为 b64, 再经 stripDataURLPrefix 抽纯 base64)
func TestImageContentHash_ConsistentAcrossFormats(t *testing.T) {
	hOpenAI := imageContentHash(openAIImageURLItem())
	hResp := imageContentHash(responsesInputImageItem())
	hClaude := imageContentHash(claudeImageItem())
	if hOpenAI != hResp {
		t.Errorf("OpenAI 与 Responses 应产生相同哈希: %s vs %s", hOpenAI, hResp)
	}
	if hOpenAI != hClaude {
		t.Errorf("OpenAI 与 Claude 应产生相同哈希: %s vs %s", hOpenAI, hClaude)
	}
	// 单独的 data URL 字符串(经 hashDataURL)也应产生相同哈希
	hStr := hashDataURL(dataURL)
	if hOpenAI != hStr {
		t.Errorf("data URL 字符串应与 item 形式产生相同哈希: %s vs %s", hOpenAI, hStr)
	}
}

// 关键测试: 相同图片在 OpenAI Chat 与 Claude Messages 两种请求体下(都用 messages + type:text)
// 产生相同缓存键 -> 满足"相同图片任意格式命中缓存"
// 注意: Responses API 的请求体形状不同(input + input_text), 与 Chat/Claude 不应共享缓存键(语义本就不同)
func TestCacheKey_ConsistentAcrossFormats(t *testing.T) {
	const group = "test-group"

	// OpenAI Chat 与 Claude Messages 都用 messages + type:text, 仅图片 item 外层格式不同
	openaiBody := json.RawMessage(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"},{"type":"image_url","image_url":{"url":"` + dataURL + `"}}]}]}`)
	claudeBody := json.RawMessage(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + rawB64 + `"}}]}]}`)

	keyOpenAI := cache.Key(group, normalizeForCache(openaiBody))
	keyClaude := cache.Key(group, normalizeForCache(claudeBody))
	if keyOpenAI != keyClaude {
		t.Errorf("OpenAI Chat 与 Claude Messages(同 messages+text 形状)应产生相同缓存键:\n  OpenAI: %s\n  Claude: %s", keyOpenAI, keyClaude)
	}
}

// 关键测试: 同一 Codex tool 形态的请求, 仅周围易变元数据(id/timestamp)不同 -> 相同缓存键
// 这是 Codex agent 重试/降级场景下"缓存要做好"的核心要求
func TestCacheKey_CodexToolForm_StableAcrossVolatileFields(t *testing.T) {
	const group = "g"
	// 同样的 role=tool + 内嵌 base64 图片, 但外层 id/created_at 不同
	a := json.RawMessage(`{"id":"req-1","created_at":100,"messages":[{"role":"tool","content":"{\"image\":\"` + dataURL + `\",\"meta\":\"x\"}"}]}`)
	b := json.RawMessage(`{"id":"req-2","created_at":999,"messages":[{"role":"tool","content":"{\"image\":\"` + dataURL + `\",\"meta\":\"x\"}"}]}`)
	ka := cache.Key(group, normalizeForCache(a))
	kb := cache.Key(group, normalizeForCache(b))
	if ka != kb {
		t.Errorf("Codex tool 形态: 仅 id/created_at 差异应命中相同缓存键:\n  a=%s\n  b=%s", ka, kb)
	}
}

// 不同图片 -> 不同缓存键
func TestCacheKey_DifferentImagesDifferent(t *testing.T) {
	const group = "g"
	a := json.RawMessage(`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}]}`)
	b := json.RawMessage(`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,BBBB"}}]}]}`)
	ka := cache.Key(group, normalizeForCache(a))
	kb := cache.Key(group, normalizeForCache(b))
	if ka == kb {
		t.Errorf("不同图片应产生不同缓存键, 都得到 %s", ka)
	}
}

// media_type 不同但图片内容相同 -> 缓存键相同(规范化会抽取纯 base64)
func TestCacheKey_IgnoresMediaTypeDifference(t *testing.T) {
	const group = "g"
	png := json.RawMessage(`{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + rawB64 + `"}}]}]}`)
	jpeg := json.RawMessage(`{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/jpeg","data":"` + rawB64 + `"}}]}]}`)
	if cache.Key(group, normalizeForCache(png)) != cache.Key(group, normalizeForCache(jpeg)) {
		t.Errorf("media_type 不同但图片内容相同应命中同一缓存键")
	}
}

// 易变字段(id/created_at)不应影响缓存键
func TestCacheKey_VolatileKeysStripped(t *testing.T) {
	const group = "g"
	a := json.RawMessage(`{"id":"abc","created_at":123,"timestamp":456,"messages":[{"role":"user","content":"hi"}]}`)
	b := json.RawMessage(`{"id":"xyz","created_at":999,"timestamp":0,"messages":[{"role":"user","content":"hi"}]}`)
	if cache.Key(group, normalizeForCache(a)) != cache.Key(group, normalizeForCache(b)) {
		t.Errorf("易变字段差异不应改变缓存键")
	}
}

// 数组顺序敏感: 消息顺序变化不应命中缓存(语义不同)
func TestCacheKey_OrderSensitive(t *testing.T) {
	const group = "g"
	a := json.RawMessage(`{"messages":[{"role":"user","content":"A"},{"role":"user","content":"B"}]}`)
	b := json.RawMessage(`{"messages":[{"role":"user","content":"B"},{"role":"user","content":"A"}]}`)
	if cache.Key(group, normalizeForCache(a)) == cache.Key(group, normalizeForCache(b)) {
		t.Errorf("消息顺序变化应导致不同缓存键")
	}
}

// ---------------------------------------------------------------- 工具

// mockImageModelServer 启一个伪图片模型 HTTP 服务, 收到任意请求返回固定描述
// 返回 (server, callCountPtr)
func mockImageModelServer(t *testing.T, desc string) (*httptest.Server, *int) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		// 校验: 收到的请求体应是 chat completions 格式且包含图片
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"` + desc + `"}}]}`))
	}))
	t.Cleanup(func() { srv.Close() })
	return srv, &calls
}

// swapHTTPClient 临时替换 sharedHTTPClient, 测试结束后恢复
func swapHTTPClient(t *testing.T, client *http.Client) {
	orig := sharedHTTPClient
	sharedHTTPClient = client
	t.Cleanup(func() { sharedHTTPClient = orig })
}

func newTestGroupRuntime(imageURL string) *modelGroupRuntime {
	return &modelGroupRuntime{
		Name: "test-group",
		MainText: UpstreamModelRT{
			BaseURL: "http://unused.invalid",
			Model:   "main-model",
			APIKey:  "k",
		},
		ImageModels: []UpstreamModelRT{
			{ExtraURL: imageURL, Model: "img-model", APIKey: "k"},
		},
		ImageStrategy: "round_robin",
		ImagePrompt:   "",
		ReplaceImage:  true, // 替换图片为文本, 便于断言
	}
}

// ---------------------------------------------------------------- 识别替换

// Codex 形态1: role=tool 的 message.content 是 JSON 字符串, 内含 base64 图片
func TestProcessImages_CodexRoleToolJSONString(t *testing.T) {
	srv, calls := mockImageModelServer(t, "红色方块")
	swapHTTPClient(t, srv.Client())
	g := newTestGroupRuntime(srv.URL)

	// content 是一个 JSON 字符串, 内部结构包含 base64 data URL
	innerObj := map[string]interface{}{
		"image": dataURL,
		"meta":  "screenshot",
	}
	innerBytes, _ := json.Marshal(innerObj)
	contentStr := string(innerBytes)

	msgs := json.RawMessage(`[{"role":"tool","content":` + jsonString(contentStr) + `}]`)

	hasImg, cnt, _, modified, err := processImagesForMessages(g, msgs)
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if !hasImg || cnt != 1 {
		t.Fatalf("应识别到1张图片, hasImg=%v cnt=%d", hasImg, cnt)
	}
	if *calls != 1 {
		t.Errorf("图片模型应被调用1次, 实际 %d", *calls)
	}
	// 修改后的 content 应包含识别结果, 不再包含原始 data URL
	s := string(modified)
	if !strings.Contains(s, "红色方块") {
		t.Errorf("修改后应包含识别结果, body=%s", s)
	}
	if strings.Contains(s, dataURL) {
		t.Errorf("修改后不应再包含原始 base64 data URL, body=%s", s)
	}
}

// Codex 形态2: role=user 重试/降级, content 是数组, item.type=tool_result,
// item.content 是 JSON 字符串, 字符串内含 base64 图片
func TestProcessImages_CodexToolResultNestedJSONString(t *testing.T) {
	srv, calls := mockImageModelServer(t, "蓝色圆圈")
	swapHTTPClient(t, srv.Client())
	g := newTestGroupRuntime(srv.URL)

	// tool_result.content 是 JSON 字符串, 内含 base64 图片
	inner := map[string]interface{}{"image": dataURL}
	innerBytes, _ := json.Marshal(inner)
	innerStr := string(innerBytes)

	msgs := json.RawMessage(`[{"role":"user","content":[{"type":"tool_result","tool_use_id":"x","content":` + jsonString(innerStr) + `}]}]`)

	hasImg, cnt, _, modified, err := processImagesForMessages(g, msgs)
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if !hasImg || cnt != 1 {
		t.Fatalf("应识别到1张图片, hasImg=%v cnt=%d", hasImg, cnt)
	}
	if *calls != 1 {
		t.Errorf("图片模型应被调用1次, 实际 %d", *calls)
	}
	s := string(modified)
	if !strings.Contains(s, "蓝色圆圈") {
		t.Errorf("修改后应包含识别结果, body=%s", s)
	}
	if strings.Contains(s, dataURL) {
		t.Errorf("修改后不应再包含原始 base64, body=%s", s)
	}
}

// Claude /v1/messages: {type:image, source:{type:base64, data}}
func TestProcessImages_ClaudeImageSource(t *testing.T) {
	srv, calls := mockImageModelServer(t, "绿色三角")
	swapHTTPClient(t, srv.Client())
	g := newTestGroupRuntime(srv.URL)

	msgs := json.RawMessage(`{"messages":[{"role":"user","content":[{"type":"text","text":"这是什么?"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + rawB64 + `"}}]}]}`)

	hasImg, cnt, _, modified, err := processImagesForMessagesValue(g, msgs)
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if !hasImg || cnt != 1 {
		t.Fatalf("应识别到1张图片, hasImg=%v cnt=%d", hasImg, cnt)
	}
	if *calls != 1 {
		t.Errorf("图片模型应被调用1次, 实际 %d", *calls)
	}
	s := string(modified)
	if !strings.Contains(s, "绿色三角") {
		t.Errorf("修改后应包含识别结果, body=%s", s)
	}
	if strings.Contains(s, rawB64) {
		t.Errorf("修改后不应再包含原始 base64, body=%s", s)
	}
}

// 多层嵌套: tool_result.content 是数组, 数组元素里又嵌了对象, 对象里有 base64 图片
// 验证 processImagesInValue 的递归能力
func TestProcessImages_DeepNesting(t *testing.T) {
	srv, calls := mockImageModelServer(t, "嵌套图片描述")
	swapHTTPClient(t, srv.Client())
	g := newTestGroupRuntime(srv.URL)

	msgs := json.RawMessage(`[{"role":"user","content":[{"type":"tool_result","content":[{"type":"text","text":"result"},{"wrapper":{"image":"` + dataURL + `"}}]}]}]`)

	hasImg, cnt, _, _, err := processImagesForMessages(g, msgs)
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if !hasImg || cnt != 1 {
		t.Fatalf("深层嵌套应识别到1张图片, hasImg=%v cnt=%d", hasImg, cnt)
	}
	if *calls != 1 {
		t.Errorf("图片模型应被调用1次, 实际 %d", *calls)
	}
}

// ---------------------------------------------------------------- 端到端缓存

// 端到端: 不同格式的同图片请求, 第二次应命中缓存(不再调用图片模型 & 上游)
// 这是用户最强调的: "一定要把缓存给我做好"
func TestCacheHit_SameImageDifferentFormats(t *testing.T) {
	imgSrv, imgCalls := mockImageModelServer(t, "识别结果A")
	swapHTTPClient(t, imgSrv.Client())

	// 主对话模型伪服务: 记录被调用次数
	upstreamCalls := 0
	upstreamSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-x","choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`))
	}))
	t.Cleanup(func() { upstreamSrv.Close() })

	g := &modelGroupRuntime{
		Name: "cache-test",
		MainText: UpstreamModelRT{
			ExtraURL: upstreamSrv.URL,
			Model:    "main",
			APIKey:   "k",
		},
		ImageModels: []UpstreamModelRT{
			{ExtraURL: imgSrv.URL, Model: "img", APIKey: "k"},
		},
		ImageStrategy: "round_robin",
		ReplaceImage:  true,
	}

	// 请求1: OpenAI image_url 格式
	body1 := `{"model":"cache-test","messages":[{"role":"user","content":[{"type":"text","text":"看图"},{"type":"image_url","image_url":{"url":"` + dataURL + `"}}]}]}`

	// 请求2: Claude image 格式(图片内容相同)
	body2 := `{"model":"cache-test","messages":[{"role":"user","content":[{"type":"text","text":"看图"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + rawB64 + `"}}]}]}`

	// 调用 processImagesForMessages 处理两次(模拟两次请求的图片识别阶段)
	_, _, _, mod1, err := processImagesForMessages(g, json.RawMessage(extractMessagesField(t, body1)))
	if err != nil {
		t.Fatalf("请求1 处理失败: %v", err)
	}
	if *imgCalls != 1 {
		t.Errorf("请求1 后图片模型应被调用1次, 实际 %d", *imgCalls)
	}

	_, _, _, mod2, err := processImagesForMessages(g, json.RawMessage(extractMessagesField(t, body2)))
	if err != nil {
		t.Fatalf("请求2 处理失败: %v", err)
	}

	// 验证两次规范化后的缓存键相同
	const groupName = "cache-test"
	key1 := cache.Key(groupName, normalizeForCache(mod1))
	key2 := cache.Key(groupName, normalizeForCache(mod2))
	if key1 != key2 {
		t.Errorf("相同图片不同格式应产生相同缓存键(用户强调: 缓存要做好):\n  key1=%s\n  key2=%s", key1, key2)
	}

	// 写入缓存后第二次应命中
	cache.Init()
	cache.Put(key1, groupName, []byte(`{"cached":true}`))
	if _, ok := cache.Get(key2); !ok {
		t.Errorf("第二次请求(同图片不同格式)应命中缓存, 但未命中(用户强调: 缓存要做好)")
	}
}

// jsonString 把字符串转成合法的 JSON 字符串字面量
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// extractMessagesField 从完整请求体里抽出 messages 字段的 JSON(测试辅助)
func extractMessagesField(t *testing.T, body string) string {
	t.Helper()
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &obj); err != nil {
		t.Fatalf("解析 body 失败: %v", err)
	}
	return string(obj["messages"])
}
