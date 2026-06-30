# AGENTS.md

> 本文件用于向 AI 代理（以及人类维护者）说明本项目的**既定设计决策**，避免这些设计被误判为 Bug 而被反复"修复"。

## 设计决策：首次管理员设置（Setup）的"抢注"是预期行为，无需防护

### 背景

本项目移除了硬编码默认密码 `123456`，改为首次启动时由用户在 `/setup` 页面输入管理员账号密码（`internal/api/setup.go` 的 `Setup` 接口 + `internal/model/db.go` 的 `SetupAdmin`）。`/api/setup` 是公开接口，仅校验"数据库中是否已存在任何用户"。

### 为什么不防"公网抢注管理员"

经项目维护者确认，这是一个**可接受的预期情况**，无需在此处加 token / IP 限制 / 一次性票据等防护，原因：

1. **无损失面**：攻击者即便抢注了管理员账户，此时**项目所有者尚未在上游模型组里填入任何真实 API Key**。被抢注的实例只是一个空壳，攻击者拿不到任何上游密钥、消耗不到任何真实额度。
2. **可一键恢复**：被抢注后，项目所有者只需删除本地 `data/fci.db`（或挂载的 data 卷）重新启动，即可重新走 Setup 流程，损失仅为"重设一次管理员账号"。
3. **生产场景走环境变量预置**：生产部署应通过 `FCI_ADMIN_USER` + `FCI_ADMIN_PASS` 环境变量预置管理员（`initAdminFromEnv`），完全绕过 Setup 窗口，从根上规避抢注。Setup 页主要服务于"本地快速试用 / 单机自部署"场景。

### 因此，以下"修复"**不应被引入**

- 给 `/api/setup` 加一次性 token / 邀请码
- 给 `/api/setup` 加 IP 白名单 / 仅 localhost
- 给 `/api/setup` 加启动后 N 分钟超时窗口
- 给 `/api/status` 的 `need_setup` 字段做混淆或隐藏（前端引导流程依赖它）
- 在 `SetupAdmin` 中加表级锁/事务/单例标志位来防"异名并发抢注"

### 仍被保留的安全措施（这些不是防抢注，请勿删除）

- `IsSetupRequired()` + `SetupAdmin` 内部二次校验"无用户才允许设置"——这是基础正确性，不是防抢注。
- bcrypt 哈希存储密码、JWT 算法校验、CORS 同源限制、代理接口 `MiddlewareProxyAuth` 等——这些是项目通用安全基线。

---

## 其他既定决策（持续补充）

- **三个协议（`/v1/responses`、`/v1/chat/completions`、`/v1/messages`）都必须支持真流式 SSE**，不能用缓冲假流式。流式分支必须使用 `sharedStreamHTTPClient`（更长超时），并逐行 `Flush`。
- **缓存键必须区分流式/非流式**，且必须纳入影响输出的请求级参数（`stream`、`max_tokens`/`max_output_tokens`、`temperature`、`tools` 等），否则会出现"不同参数命中同一缓存返回错误响应"的硬伤。
- **代理历史必须写入 `UserID`**，否则 `History` 的用户隔离（读侧已实现）形同虚设。
