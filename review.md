# Code Review: fuck-chat-img 项目

> 审查范围：初始提交 5d8e0d8（47个文件，6729行代码）
> 审查日期：2026-06-30

## 问题总览

| 严重程度 | 数量 | 说明 |
|---------|------|------|
| 🔴 严重 (Critical) | 6 | 安全漏洞、数据泄露、服务崩溃风险 |
| 🟠 高 (High) | 7 | 逻辑错误、功能缺陷、缓存语义错误 |
| 🟡 中 (Medium) | 8 | 并发问题、错误处理、代码规范 |
| 🟢 低 (Low) | 6 | 优化建议、代码清理、体验问题 |

---

## 🔴 严重问题 (Critical)

### 1. API Key 泄露 - GetGroup 接口未脱敏且无权限控制

**文件**: [modelgroup.go](file:///workspace/internal/api/modelgroup.go#L49-L57)

```go
// GetGroup 获取单个
func GetGroup(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var g model.ModelGroup
	if err := model.DB.First(&g, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "模型组不存在"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": groupToDTO(g, false)}) // ❌ maskKey=false
}
```

**问题**:
- `groupToDTO(g, false)` 第二个参数为 `false`，API Key **未脱敏**直接返回明文
- 该接口在 `authed` 路由组下但**没有 `MiddlewareAdmin()` 保护**，任何登录用户（包括普通用户）都可以调用
- 攻击者只需遍历 ID 即可获取所有上游模型的 API Key

**修复**:
1. 将 `groupToDTO(g, false)` 改为 `groupToDTO(g, true)`
2. 给 GET /groups/:id 添加管理员中间件，或采用"编辑时单独返回"的方案

---

### 2. OpenAI 兼容接口完全无认证

**文件**: [server.go](file:///workspace/internal/api/server.go#L38-L43)

```go
// ===== OpenAI 兼容代理接口 (不需要 Web UI 鉴权, 用模型组名作为访问凭证) =====
v1 := r.Group("/v1")
v1.GET("/models", proxy.HandleModels)
v1.POST("/responses", proxy.HandleResponses)
v1.POST("/chat/completions", proxy.HandleChat)
```

**问题**:
- 代码注释说"用模型组名作为访问凭证"，但 `/v1/models` 接口**公开返回所有启用的模型组名**
- 任何人都能先 GET `/v1/models` 获取模型名，然后直接调用代理接口消耗你的 API 额度
- 没有任何 API Key 或 IP 限制机制

**修复**: 增加 API Key 认证机制，为每个模型组生成独立访问令牌。

---

### 3. JWT 未验证签名算法 - 算法混淆攻击风险

**文件**: [auth.go](file:///workspace/internal/auth/auth.go#L48-L58)

```go
func ParseToken(tokenStr string) (*Claims, error) {
	cfg := config.Get()
	claims := &Claims{}
	t, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (interface{}, error) {
		return []byte(cfg.JWTSecret), nil // ❌ 未验证 t.Method 是 HS256
	})
```

**问题**: 攻击者可以使用 `none` 算法或 RS256（公钥）伪造任意用户 token，绕过认证。

**修复**:
```go
t, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (interface{}, error) {
	if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
		return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
	}
	return []byte(cfg.JWTSecret), nil
})
```

---

### 4. CORS 配置违规 - AllowCredentials + 任意 Origin

**文件**: [server.go](file:///workspace/internal/api/server.go#L24-L30)

```go
r.Use(cors.New(cors.Config{
	AllowOriginFunc:  func(string) bool { return true }, // ❌ 允许所有来源
	AllowCredentials: true,                              // ❌ 同时允许凭证
}))
```

**问题**: 根据 CORS 规范，当 `AllowCredentials: true` 时，`Access-Control-Allow-Origin` 不能是 `*` 或返回任意来源，浏览器会直接拒绝响应。这实际上会导致跨域请求失效。

**修复**: 配置具体的允许来源列表，或开发环境特殊处理。

---

### 5. 默认硬编码密码 + 弱默认密码

**文件**: [db.go](file:///workspace/internal/model/db.go#L51-L54), [config.go](file:///workspace/internal/config/config.go#L28-L30)

```go
// db.go
if plain == "" {
	plain = "123456" // ❌ 默认密码 123456
}

// config.go
JWTSecret:     "fuck-chat-img-default-secret-change-me", // ❌ 硬编码默认密钥
SessionSecret: "fuck-chat-img-session-secret",
```

**问题**:
- 首次启动默认密码 `123456` 是极弱密码
- JWT/Session 有硬编码默认密钥，如果用户忘记配置环境变量，所有实例使用相同密钥
- 没有启动警告提醒用户修改默认密钥

---

### 6. 历史记录无用户隔离

**文件**: [model.go](file:///workspace/internal/model/model.go#L48-L68), [history.go](file:///workspace/internal/api/history.go#L15-L57)

**问题**:
- `History` 模型**没有 `UserID` 字段**，所有用户共享同一份历史记录
- 任何登录用户都能看到所有通过代理的请求（包括可能包含敏感信息的输入输出）
- 删除历史需要管理员权限，但查看不需要，信息泄露风险大

---

## 🟠 高优先级问题 (High)

### 7. 缓存键对消息数组排序 - 严重破坏对话语义

**文件**: [normalize.go](file:///workspace/internal/proxy/normalize.go#L30-L38)

```go
keys := make([]string, 0, len(arr))
encoded := make(map[string][]byte, len(arr))
for _, item := range arr {
	b := normalizeMessageItem(item)
	keys = append(keys, string(b))
	encoded[string(b)] = b
}
sort.Strings(keys) // ❌ 对 messages/input 数组排序！
```

**问题**: 为了让"相同语义不同顺序"命中缓存，代码对整个消息数组和 content 数组都做了排序。这是完全错误的：

- LLM 对话中消息顺序决定上下文：`[user问A, assistant答A, user问B]` 和 `[user问B, assistant答A, user问A]` 语义完全不同
- 单条消息内 content 顺序也重要：`"先描述[图1]再描述[图2]"` 调换图片顺序会得到错误结果
- 测试用例 `TestResponsesImageMixingAndCache` 甚至**验证了这个错误行为**（把 input_text 和 input_image 调换后期待命中缓存）

**修复**: 绝不能对消息数组排序。只应对 JSON 对象的 key 做排序（保证字段顺序不影响），数组顺序必须保留。

---

### 8. CacheHit 字段从未被正确设置

**文件**: [responses.go](file:///workspace/internal/proxy/responses.go#L65-L72), [responses.go](file:///workspace/internal/proxy/responses.go#L392-L412), [chat.go](file:///workspace/internal/proxy/chat.go#L243-L263)

**问题**: 
- `recordHistory` 和 `recordChatHistory` 函数都**没有 `cacheHit` 参数**
- 创建 `History` 时 `CacheHit` 字段永远是默认值 `false`
- 缓存命中统计、`cache_hit=true/false` 过滤功能完全无效

---

### 9. go:embed 路径错误，嵌入前端资源无法访问

**文件**: [embed.go](file:///workspace/web/embed.go#L8-L9), [server.go](file:///workspace/internal/api/server.go#L91)

```go
//go:embed dist/*  // ❌ 这个模式不会保留 dist/ 前缀
var DistFS embed.FS

// server.go
emb, err := fs.Sub(web.DistFS, "dist") // ❌ 找不到 dist 子目录
```

**问题**: `//go:embed dist/*` 只嵌入 dist 目录下的**文件**，文件路径不带 `dist/` 前缀。`fs.Sub(..., "dist")` 会失败，`rootFS` 为 nil，最终返回"Web UI not built"提示。

**修复**: 将 `//go:embed dist/*` 改为 `//go:embed dist`。

---

### 10. 轮询索引 rrIndex 永远不会清理

**文件**: [types.go](file:///workspace/internal/proxy/types.go#L92-L95)

```go
var (
	rrMu    sync.Mutex
	rrIndex = map[string]int{} // ❌ 模型组删除后游标残留
)
```

**问题**: 模型组被删除后，rrIndex 中对应的条目永远不会被移除，长期运行内存泄漏。另外 failover 策略根本没有实现（见第11点）。

---

### 11. round_robin 与 failover 策略无区别

**文件**: [image.go](file:///workspace/internal/proxy/image.go#L83-L92)

```go
for _, m := range imgModels {
	desc, err := callImageModel(m, prompt, imageURL, imageB64, client)
	if err == nil && strings.TrimSpace(desc) != "" {
		return desc, m.DisplayName(), nil
	}
	lastErr = fmt.Errorf("[%s] %v", m.DisplayName(), err)
	// round_robin 与 failover 在此表现一致: 逐个尝试直到成功
	_ = strategy // ❌ strategy 被丢弃
}
```

**问题**: `_ = strategy` 直接丢弃策略参数。两种策略目前行为完全相同，都是从轮询起始位置开始依次尝试所有模型直到成功。failover 应该只在主模型失败时尝试备用模型，而不是轮询。

---

### 12. 追加文本时循环修改切片可能导致跳过或重复处理

**文件**: [chat.go](file:///workspace/internal/proxy/chat.go#L122-L124), [responses.go](file:///workspace/internal/proxy/responses.go#L147-L149)

```go
cont = append(cont[:j+1], append([]interface{}{textItem}, cont[j+1:]...)...)
arr[i]["content"] = cont
// ❌ 还在 for j := range cont 循环中，range 是基于原始长度的
```

**问题**: 在 range 循环中向切片插入元素，range 的迭代次数是循环开始时确定的，新插入的元素不会被遍历，但后续索引指向的是新切片的位置，可能导致跳过原始元素或出现逻辑混乱。

---

### 13. ChangePassword 类型断言无检查，可能 panic

**文件**: [auth.go](file:///workspace/internal/api/auth.go#L70-L80)

```go
uid, _ := c.Get(auth.ContextKeyUserID)
username, _ := c.Get(auth.ContextKeyUsername)
if _, ok := model.VerifyPassword(username.(string), req.OldPassword); !ok {
```

**问题**: `username.(string)` 和 `uid.(uint)` 都使用单返回值类型断言，如果中间件没有正确设置 context 值（比如接口被错误配置），会直接 panic，导致 500 错误。

**修复**: 使用双返回值断言：`username, ok := c.Get(...); if !ok { ... }`。

---

## 🟡 中优先级问题 (Medium)

### 14. 缓存 hits/misses 计数器存在并发竞态

**文件**: [cache.go](file:///workspace/internal/cache/cache.go#L134-L143)

```go
var (
	hits   int64
	misses int64
)

func RecordHit()  { hits++ }   // ❌ 并发不安全
func RecordMiss() { misses++ }
```

**问题**: `int64++` 不是原子操作，并发下会有数据竞态，统计不准确。

**修复**: 使用 `sync/atomic` 包：`atomic.AddInt64(&hits, 1)`。

---

### 15. LRU 缓存实际上是 FIFO，且更新逻辑错误

**文件**: [cache.go](file:///workspace/internal/cache/cache.go#L77-L94)

```go
func Put(key, modelName string, value []byte) {
	// ...
	if _, exists := store.items[key]; !exists {
		store.order = append(store.order, key) // ❌ 已存在的key不会更新顺序
		store.evictLocked()
	}
	store.items[key] = &Entry{...} // 覆盖entry但order位置不变
}
```

**问题**:
- 访问命中（Get）时没有将条目移到"最近使用"位置，淘汰策略实际是 FIFO 不是 LRU
- Put 更新已有 key 时，顺序列表中位置不变，也不更新 CreatedAt
- 应该在 Get 命中时将 key 移到 order 末尾

---

### 16. HTTP Client 每次请求新建，连接不复用

**文件多处**:
- [chat.go:89](file:///workspace/internal/proxy/chat.go#L89), [chat.go:152](file:///workspace/internal/proxy/chat.go#L152), [chat.go:178](file:///workspace/internal/proxy/chat.go#L178)
- [responses.go:111](file:///workspace/internal/proxy/responses.go#L111), [responses.go:183](file:///workspace/internal/proxy/responses.go#L183), [responses.go:215](file:///workspace/internal/proxy/responses.go#L215)
- [image.go:79](file:///workspace/internal/proxy/image.go#L79)

**问题**: 每次请求都 `&http.Client{Timeout: ...}` 创建新客户端，无法复用 TCP 连接，性能差。

**修复**: 全局复用一个配置好 Transport 的 http.Client。

---

### 17. 异步记录历史无 panic 恢复

**文件**: [responses.go](file:///workspace/internal/proxy/responses.go#L393), [chat.go](file:///workspace/internal/proxy/chat.go#L244)

```go
go func() {
	h := model.History{...}
	model.DB.Create(&h) // ❌ 如果 panic 会导致整个进程崩溃
}()
```

**问题**: goroutine 中如果发生 panic（比如 DB 连接问题、nil pointer），会直接导致整个服务崩溃。

**修复**: 在 goroutine 开头加 `defer func() { recover() }()`。

---

### 18. 大量错误被忽略

代码中多处使用 `_` 忽略错误，可能导致静默失败：

| 文件 | 位置 | 问题 |
|-----|------|------|
| modelgroup.go | L70, L71, L106, L107 | `json.Marshal` 错误被忽略 |
| modelgroup.go | L205, L207 | `json.Unmarshal` 错误被忽略 |
| server.go | L100-L102 | `io.ReadAll` 和 `f.Close()` 错误被忽略 |
| history.go | L73, L79 | `db.Delete` 错误被忽略 |
| modelgroup.go | L142 | `db.Save` (ToggleGroup) 错误被忽略 |
| normalize.go | L75, L100, L110, L198 | 多处 json 错误被忽略 |

---

### 19. SessionSecret 配置项从未被使用

**文件**: [config.go](file:///workspace/internal/config/config.go#L18), [config.go](file:///workspace/internal/config/config.go#L53-L55)

`SessionSecret` 字段和 `FCI_SESSION_SECRET` 环境变量在代码中从未被引用，属于死代码。

---

### 20. 静态文件服务缺少 Cache-Control 和安全头

**文件**: [server.go](file:///workspace/internal/api/server.go#L148-L166)

- 静态资源（JS/CSS/字体）没有设置 `Cache-Control` 头，无法利用浏览器缓存
- 没有设置 `X-Content-Type-Options: nosniff`、`X-Frame-Options` 等安全头

---

### 21. 部分 http.NewRequest 错误未检查

对比：
- [responses.go:184](file:///workspace/internal/proxy/responses.go#L184) - 检查了 err
- [chat.go:153](file:///workspace/internal/proxy/chat.go#L153) - `httpReq, _ := http.NewRequest(...)` 忽略了 err

虽然 http.NewRequest 在方法/URL 合法时基本不会出错，但一致性上应该都检查。

---

## 🟢 低优先级问题 (Low)

### 22. 前端导航菜单无权限控制

**文件**: [SiderBar.jsx](file:///workspace/web/src/components/layout/SiderBar.jsx)

侧边栏对所有登录用户显示"模型组管理"等管理菜单项，普通用户点击后会收到 403 错误。应根据 `isAdmin` 隐藏管理菜单。

---

### 23. 前端路由未使用 AdminRoute 保护

**文件**: [App.jsx](file:///workspace/web/src/App.jsx#L42-L55)

```jsx
<Route path="groups" element={<ModelGroup />} />
// ❌ 只用了 ProtectedRoute，没有用 AdminRoute
```

后端有 `MiddlewareAdmin` 保护写操作，但 GET 接口（如 GetGroup 泄露 key）没有保护。前端也应该配合使用 `AdminRoute`。

---

### 24. 前端混用两个 Toast 库

**文件**: [ModelGroup.jsx](file:///workspace/web/src/pages/ModelGroup.jsx#L14-L20)

```js
import { Toast } from '@douyinfe/semi-ui'
import { toast } from 'react-toastify'
```

同时使用 Semi UI 的 Toast 和 react-toastify，造成包体积冗余和样式不一致。应该统一使用一个（建议用 Semi UI 自带的）。

---

### 25. auth.jsx 有两个 logout 函数

**文件**: [auth.jsx](file:///workspace/web/src/helpers/auth.jsx#L28-L36), [auth.jsx](file:///workspace/web/src/helpers/auth.jsx#L111-L118)

一个是 AuthProvider 内部的 logout（清理 state），一个是独立导出的 logout（只清 localStorage 然后跳转），容易混淆。独立的那个不会更新 React 状态，可能导致 UI 不一致。

---

### 26. pickMessage 错误解析不正确

**文件**: [api.js](file:///workspace/web/src/helpers/api.js#L40-L47)

```js
error?.response?.data?.error ||  // error 是对象 {"message": "...", ...}，不是字符串
```

后端错误格式是 `{"error": {"message": "xxx", ...}}`，应该取 `error?.response?.data?.error?.message`。

---

### 27. proxy_test.go 中无意义的变量赋值和导入

- [proxy_test.go:67-68](file:///workspace/internal/proxy/proxy_test.go#L67-L68): `mainJSON, _ := json.Marshal(...); _ = mainJSON` 赋值后马上丢弃
- [proxy_test.go:216](file:///workspace/internal/proxy/proxy_test.go#L216): `_ = time.Now` 只是为了避免 unused 警告，应该直接删除 time 导入
- [responses.go:414](file:///workspace/internal/proxy/responses.go#L414): `var _ = errors.New` 无意义，应该正确使用 errors 包或删除导入

---

## 测试问题

当前测试仅覆盖了基本场景，缺少：
- 鉴权中间件测试
- 错误边界测试（如 JWT 算法混淆、越权访问）
- 并发缓存测试
- 流式响应测试

另外，**测试用例 `TestResponsesImageMixingAndCache` 验证了错误的缓存行为**（content 数组乱序命中缓存），需要修正。

---

## 建议修复优先级

1. **立即修复**（上线前必须）：#1 API Key泄露、#2 接口无认证、#3 JWT算法、#4 CORS、#6 历史无隔离、#9 embed路径错误、#7 消息排序缓存
2. **尽快修复**：#8 CacheHit统计、#11 策略未实现、#13 类型断言panic、#14 竞态条件、#17 goroutine panic
3. **后续优化**：LRU实现、HTTP连接复用、错误处理、前端权限控制、代码清理
