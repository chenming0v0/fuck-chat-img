# 代码 Review 报告

> 审查范围: `fuck-chat-img` (fci) 全部后端 Go 代码
> 审查依据: `README.md` 中描述的预期行为(缓存 LRU、图片识别失败直接报错、round_robin/failover 策略、流式回放、历史记录准确性等)
> 构建验证: `go build ./internal/...` 通过; `go test -race ./internal/proxy/...` 全部通过

---

## 一、Review 重点 (审查点)

| # | 模块 | 审查点 |
|---|------|--------|
| 1 | `internal/cache` | LRU 是否真正按"最近使用"淘汰; hits/misses 计数是否线程安全 |
| 2 | `internal/proxy/types.go` | round_robin 与 failover 两种图片策略语义是否区分 |
| 3 | `internal/proxy/image.go` | MaxRetries 字段是否生效; 死代码; 错误信息是否可区分"空结果"与"调用失败" |
| 4 | `internal/proxy/responses.go` | 流式/非流式请求是否会命中同一缓存(污染); 历史记录中 hasImage / imgCount 是否准确; 流式响应是否记录输出摘要 |
| 5 | `internal/proxy/chat.go` | 与 responses.go 同样的缓存/历史问题 |
| 6 | `internal/api/modelgroup.go` | 删除模型组是否会泄漏内存; ImageModels 空数组是否被校验 |
| 7 | `internal/config/config.go` | 是否存在未使用的配置字段(死配置) |

---

## 二、发现的 Bug 及修复

### Bug 1 — 缓存计数器存在数据竞争(严重)

**文件**: [internal/cache/cache.go](file:///workspace/internal/cache/cache.go)

**问题**: `hits` / `misses` 为普通 `int64` 全局变量, 由 `RecordHit()` / `RecordMiss()` 在多个 HTTP 请求 goroutine 中并发 `++` 自增, 在 `-race` 下必触发数据竞争, 计数值也会丢失。

**修复**: 改为 `atomic.AddInt64` 写入、`atomic.LoadInt64` 读取。

```go
var (
    hits   int64
    misses int64
)
func RecordHit()  { atomic.AddInt64(&hits, 1) }
func RecordMiss() { atomic.AddInt64(&misses, 1) }
// GetStats() 中: Hits: atomic.LoadInt64(&hits), Misses: atomic.LoadInt64(&misses)
```

---

### Bug 2 — LRU 实际上不是 LRU, 命中不更新顺序(严重)

**文件**: [internal/cache/cache.go](file:///workspace/internal/cache.go#L60-L94)

**问题**: 原实现 `Get()` 命中缓存时只增加 `HitCount`, 并未把该 key 在 `order` 列表中移到队尾。结果: 频繁访问的热点 key 仍可能被当作"最久未使用"被 `evictLocked` 淘汰, 违反 README 中"LRU 缓存"的承诺。

**修复**: 新增 `touchLocked(key)`, 在 `Get` 命中时调用, 将 key 从当前位置移除并追加到队尾。

```go
func (s *Store) touchLocked(key string) {
    if len(s.order) <= 1 { return }
    idx := -1
    for i, k := range s.order {
        if k == key { idx = i; break }
    }
    if idx < 0 || idx == len(s.order)-1 { return }
    s.order = append(s.order[:idx], s.order[idx+1:]...)
    s.order = append(s.order, key)
}
```

---

### Bug 3 — round_robin 与 failover 行为完全相同(严重)

**文件**: [internal/proxy/types.go](file:///workspace/internal/proxy/types.go#L100-L116)

**问题**: 原 `nextImageModels` 对两种策略都返回"全部模型, 原顺序", 仅有注释不同, 语义上等价。结果是:
- `round_robin` 每次都尝试所有模型, 没有轮转, 负载无法在图片模型间分担;
- `failover` 与 `round_robin` 无法区分。

**修复**: 严格区分语义
- `failover`: 返回全部模型, 由调用方逐个尝试直到成功(故障转移);
- `round_robin`: 每次只挑 1 个模型(按 `rrIndex` 轮转), 失败即报错, 不再尝试下一个(纯轮询负载均衡)。

```go
if g.ImageStrategy == "failover" {
    out := make([]UpstreamModelRT, len(g.ImageModels))
    copy(out, g.ImageModels)
    return out
}
// round_robin: 只挑 1 个, 轮转
rrMu.Lock()
start := rrIndex[g.Name] % len(g.ImageModels)
rrIndex[g.Name] = (start + 1) % len(g.ImageModels)
rrMu.Unlock()
return []UpstreamModelRT{g.ImageModels[start]}
```

---

### Bug 4 — MaxRetries 字段被完全忽略(严重)

**文件**: [internal/proxy/image.go](file:///workspace/internal/proxy/image.go#L73-L108)

**问题**: 配置中 `max_retries` 字段反序列化到 `UpstreamModelRT.MaxRetries`, 但 `recognizeImage` 原实现对每个模型只调用 1 次 `callImageModel`, 完全没有重试逻辑。该字段形同虚设。

**修复**: 为每个模型按 `MaxRetries`(默认 1) 进行重试循环。

```go
retries := m.MaxRetries
if retries < 1 { retries = 1 }
for attempt := 0; attempt < retries; attempt++ {
    desc, err = callImageModel(m, prompt, imageURL, imageB64, client)
    if err == nil && strings.TrimSpace(desc) != "" {
        return desc, m.DisplayName(), nil
    }
}
```

---

### Bug 5 — 流式与非流式请求共享同一缓存键(严重)

**文件**: [internal/proxy/responses.go](file:///workspace/internal/proxy/responses.go#L100-L106) 、[internal/proxy/chat.go](file:///workspace/internal/proxy/chat.go#L51-L54)

**问题**: 缓存键仅由 `模型组名 + 规范化 input` 构成, 不包含 `stream` 标记。于是:
1. 同一 prompt 先以 `stream=false` 请求, 缓存写入非流式 JSON 体积;
2. 之后以 `stream=true` 请求, 命中缓存, 却把非流式 JSON 当作 SSE 事件回放 → 客户端解析失败;
3. 反之亦然, 流式事件列表被当作非流式 JSON 返回。

**修复**: 在 canonical 前追加 `S\x00`(流式) 或 `N\x00`(非流式) 前缀, 两种请求落不同缓存槽。

```go
func withStreamFlag(canonical []byte, stream bool) []byte {
    if stream { return append([]byte("S\x00"), canonical...) }
    return append([]byte("N\x00"), canonical...)
}
```

---

### Bug 6 — 历史记录 imgCount 始终为 0(中等)

**文件**: [internal/proxy/responses.go](file:///workspace/internal/proxy/responses.go#L458) 、[internal/proxy/chat.go](file:///workspace/internal/proxy/chat.go#L278)

**问题**: `recordHistory` / `recordChatHistory` 的签名中无 `imgCount` 形参, 所有调用点传入的图片数量都没写入 `model.History.ImageCount`, 该字段恒为 0, 历史/统计页面无法体现图片数量。

**修复**: 给两个函数增加 `imgCount int` 参数, 并在所有调用点(缓存命中、图片识别失败、上游失败、上游成功、流式成功)传入实际计数。

---

### Bug 7 — 缓存命中时 hasImage 恒为 false(中等)

**文件**: [internal/proxy/responses.go](file:///workspace/internal/proxy/responses.go#L70) 、[internal/proxy/chat.go](file:///workspace/internal/proxy/chat.go#L63)

**问题**: 缓存命中分支只调用 `recordHistory(..., false /*hasImage*/, ...)`, 把命中请求的图片标记记成"无图"。真实情况是: 该请求 input 里就有图片, 只是图片识别在首次请求时已完成并被缓存。

**修复**: 新增 `inputHasImage` / `messagesHasImage` 仅做类型扫描(不调上游), 在缓存命中分支用它计算 `hitHasImage` 后再记录历史。

```go
hitHasImage := inputHasImage(req.Input)
recordHistory(reqID, grp, &req, hitHasImage, 0, true, ...)
```

---

### Bug 8 — 流式响应历史 OutputSummary 为空(中等)

**文件**: [internal/proxy/responses.go](file:///workspace/internal/proxy/responses.go#L320-L340) 、[internal/proxy/chat.go](file:///workspace/internal/proxy/chat.go#L258)

**问题**: 流式分支把收集到的 SSE 行写入 `collected` 用于缓存回放与 usage 提取, 但调用 `recordHistory` 时把 `respBytes` 传成 `nil`, 导致流式请求的历史 `output_summary` 永远为空, 用户在历史页无法看到流式输出内容。

**修复**: 新增 `summarizeCollectedSSE`, 从收集到的 SSE 行里取最后一段非空 `data:` 行作为摘要, 传给 `recordHistory`。

```go
func summarizeCollectedSSE(collected [][]byte) []byte {
    var last []byte
    for _, line := range collected {
        s := strings.TrimSpace(string(line))
        if strings.HasPrefix(s, "data:") {
            s = strings.TrimSpace(strings.TrimPrefix(s, "data:"))
            if s != "" && s != "[DONE]" { last = []byte(s) }
        }
    }
    return last
}
```

---

### Bug 9 — 删除模型组导致 rrIndex 内存泄漏(中等)

**文件**: [internal/api/modelgroup.go](file:///workspace/internal/api/modelgroup.go#L125-L139)

**问题**: `round_robin` 策略使用全局 `rrIndex = map[string]int{}`, 以模型组 `Name` 为 key。删除模型组时只删了 DB 行, 没有清理对应 key, 长期运行下删除过的模型组名会一直残留在 map 里, 形成内存泄漏; 且若同名模型组被重建, 会复用旧游标导致起点错乱。

**修复**: 新增 `proxy.ForgetGroupRR(name)`, 在 `DeleteGroup` 中先取出模型组名、删除 DB 行后调用。

```go
func ForgetGroupRR(name string) {
    rrMu.Lock()
    delete(rrIndex, name)
    rrMu.Unlock()
}
```

---

### Bug 10 — 创建/更新模型组未校验 ImageModels 非空(中等)

**文件**: [internal/api/modelgroup.go](file:///workspace/internal/api/modelgroup.go#L176-L205)

**问题**: `validateGroupReq` 只校验了 `Name`、`MainTextModel` 三个字段以及每个图片模型的字段, 但没有校验 `ImageModels` 数组长度。允许创建 `ImageModels = []` 的模型组, 之后所有请求在 `nextImageModels` 返回 nil, 触发"未配置图片模型"错误, 用户难以排查。

**修复**: 在校验开始处增加长度判断。

```go
if len(r.ImageModels) == 0 {
    return errStr("至少需要 1 个图片模型")
}
```

---

### Bug 11 — 未使用的死代码与未使用配置(轻度)

**文件**: [internal/proxy/image.go](file:///workspace/internal/proxy/image.go) 、[internal/config/config.go](file:///workspace/internal/config/config.go) 、[.env.example](file:///workspace/.env.example)

**问题**:
1. `image.go` 中存在未被引用的 `ImageModelsConfig` 结构体, 与已使用的 `UpstreamModelRT` 重复定义, 易误导维护者;
2. `responses.go` 中 `import "errors"` 但实际未使用 `errors` 包;
3. `config.go` 中 `SessionSecret` 字段无任何读取方, `.env.example` 中也保留对应 `FCI_SESSION_SECRET`, 给用户造成"必须配置"的错觉。

**修复**:
1. 删除 `ImageModelsConfig` 结构体;
2. 删除 `responses.go` 中未使用的 `errors` 导入;
3. 删除 `config.go` 的 `SessionSecret` 字段及其环境变量加载; 同步从 `.env.example` 中删除 `FCI_SESSION_SECRET`。

---

## 三、验证

- `go build ./internal/...` — 通过
- `go test -race ./internal/proxy/...` — 全部通过, 无数据竞争

> 说明: `web/embed.go` 使用 `//go:embed dist/*` 嵌入前端产物, 仓库当前未构建前端时 `dist/` 目录不存在, 因此根包 `go build .` 会报 `pattern dist/*: no matching files found`。此为构建环境缺前端产物所致, 非代码缺陷; 在仓库根执行 `make web` 后即可正常构建。

---

## 四、建议后续改进(本次未实施)

1. `recognizeImage` 对 `round_robin` 单模型失败应允许"降级到 failover"作为可选项, 兼顾负载均衡与可用性;
2. `cache.touchLocked` 当前为 O(n) 线性查找, 缓存量大时可引入 `container/list` 做到 O(1) 移动;
3. `processImagesForInput` / `processImagesForMessages` 内对每张图都调用 `nextImageModels`, round_robin 模式下多图请求会跨多模型识别, 若需"同一请求的图都用同一模型"应再讨论;
4. 历史记录写入当前用 `go func()` 异步 fire-and-forget, 失败无重试与日志, 建议加错误日志或改为带缓冲通道。
