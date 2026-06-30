# Fuck Chat Img

> 文本模型 + 图片模型混合代理 · OpenAI Responses API 兼容 · 带缓存与历史记录
>
> 把「一个主对话模型 + 多个图片识别模型」混合成**一个对外暴露的模型**的小工具。

---

## 这是什么

`fuck-chat-img` 是一个轻量网关，让你可以把 **一个文本对话模型** 和 **多个图片识别模型** 组合成一个「模型组」，对外只暴露一个模型名。客户端像调用普通 OpenAI 模型一样调用它，网关会自动：

1. 拦截请求中所有图片（`input_image` / `image_url`）
2. 用配置的图片模型**轮询**识别每张图片，得到文字描述
3. 把图片替换（或追加）为识别文本，再转发给主对话模型
4. 返回主对话模型的应答

这样你就能让**不支持图片的文本模型**也能「看懂图片」，或者让**便宜的小模型**干活、**贵的大模型**只负责最终回答，组合出最优成本。

UI 复刻自 [newapi](https://github.com/Calcium-Ion/new-api)（React 19 + Semi UI），登录页、控制台布局、模型广场、历史记录页全部沿用其设计语言；**只有「模型组管理」是本项目自研核心**。

---

## 核心特性

| 特性 | 说明 |
| --- | --- |
| 模型组 | 创建模型组后，客户端从 `/v1/models` 拿到的就是这个组名，请求时 `model` 填它即可 |
| 主对话模型 + 图片模型轮询 | 主对话模型只能指定 1 个；图片模型可指定多个，按 `round_robin` / `failover` 轮询 |
| 图片识别失败直接报错 | 任一图片识别失败即返回错误，不会偷偷降级调用主模型 |
| 三协议兼容 + 真流式 | 完整支持 `/v1/responses`、`/v1/chat/completions`、`/v1/messages`（Anthropic 兼容），三协议均支持真流式 SSE（逐行 Flush，非缓冲假流式）|
| 绝对缓存 | LRU 缓存，对 input/messages 做**确定性规范化**后计算缓存键；缓存键同时纳入 `stream`、`max_tokens`、`temperature`、`tools` 等**影响输出的参数**，避免不同参数命中同一缓存返回错误响应 |
| 历史记录 | 完整记录每次请求（模型组、端点、用户、图片数、缓存命中、Token、延迟、输入输出摘要），Web UI 可查，按用户隔离 |
| Web UI 鉴权 | JWT 登录后才能打开控制台与配置；管理员才能改模型组；代理接口通过 `FCI_PROXY_KEY` 或管理员 JWT 鉴权 |
| 单二进制部署 | 前端 `go:embed` 嵌入 Go 二进制，一个文件即可运行 |

---

## 快速开始

### 方式一：直接运行预编译二进制（含前端）

```bash
# 拉取
git clone https://github.com/chenming0v0/fuck-chat-img.git
cd fuck-chat-img

# 运行（前端已嵌入）
./bin/fuck-chat-img
```

打开 http://localhost:8080 ，首次访问会跳转到 **设置页**，由你输入管理员账号与密码（**不再有默认密码**）。设置完成后用该账户登录即可。

> 生产部署推荐通过 `FCI_ADMIN_USER` + `FCI_ADMIN_PASS` 环境变量预置管理员，完全绕过首次设置页（详见 [AGENTS.md](AGENTS.md)）。

### 方式二：从源码构建

需要 Go 1.23+ 和 Node 20+（推荐用 [bun](https://bun.sh)），以及 C 编译器（gcc/musl-dev，因为 SQLite 驱动需要 CGO）。

```bash
# 一键构建（前端 + 后端）
make all

# 运行
./bin/fuck-chat-img
```

或分步：

```bash
# 前端
cd web && bun install && bun run build && cd ..

# 后端（会把 web/dist 嵌入二进制；CGO 供 SQLite 驱动编译）
CGO_ENABLED=1 go build -o bin/fuck-chat-img .
```

### 方式三：Docker

```bash
docker build -t fuck-chat-img .
docker run -d -p 8080:8080 -v $(pwd)/data:/app/data --name fci fuck-chat-img
```

### 方式四：开发模式（前后端热替换）

```bash
# 终端 1：后端
go run .

# 终端 2：前端（dev server 自动代理 /api 到 :8080）
cd web && bun run dev
```

---

## 配置

所有配置通过环境变量注入，见 [.env.example](.env.example)：

| 环境变量 | 默认值 | 说明 |
| --- | --- | --- |
| `FCI_LISTEN` | `:8080` | 监听地址 |
| `FCI_DB_PATH` | `./data/fci.db` | SQLite 数据库路径 |
| `FCI_WEB_DIR` | (空) | 前端静态目录；留空则用嵌入的前端，开发时指向 `web/dist` |
| `FCI_JWT_SECRET` | (随机生成) | JWT 签名密钥；留空则每次启动用 `crypto/rand` 随机生成（旧登录会失效） |
| `FCI_PROXY_KEY` | (空) | 代理接口访问密钥；留空时仅管理员 JWT 可调 `/v1/*` |
| `FCI_ADMIN_USER` | `root` | 初始管理员用户名（仅首次启动且无任何用户时生效） |
| `FCI_ADMIN_PASS` | (空) | 初始管理员密码（明文传入，服务端 bcrypt 哈希；仅首次启动生效；至少 6 位） |
| `FCI_CACHE_ENABLED` | `true` | 是否启用缓存 |
| `FCI_CACHE_MAX` | `10000` | 缓存最大条目数 |
| `FCI_REQUEST_TIMEOUT` | `300` | 上游请求超时（秒）；流式请求使用 2 倍超时 |

---

## 使用

### 1. 登录 Web UI

打开 http://localhost:8080 。若数据库无任何用户，会跳转到设置页让你创建管理员；否则用已设置的管理员账户登录。

### 2. 创建模型组

进入「控制台 → 模型组管理 → 新建」：

- **名称**：对外暴露的模型名（如 `mixed-vision`），客户端调用时 `model` 填这个
- **主对话模型**：1 个，填 `base_url` / `api_key` / `model`（如 `gpt-4o`）
- **图片模型**：1 个或多个，每个填 `base_url` / `api_key` / `model`（如 `gpt-4o-mini`），可增删
- **图片策略**：`round_robin`（轮询）/ `failover`（逐个尝试直到成功）
- **图片识别提示词**：给图片模型的额外指令（如「请重点识别图中的文字」）
- **替换图片**：开 = 用识别文本替换图片项；关 = 在图片后追加识别文本
- **启用**：关闭后该组不会出现在 `/v1/models`

### 3. 调用

像调用 OpenAI 一样调用，`model` 填模型组名：

```bash
curl http://localhost:8080/v1/responses \
  -H "Content-Type: application/json" \
  -d '{
    "model": "mixed-vision",
    "input": [
      {
        "role": "user",
        "content": [
          {"type": "input_text", "text": "这张图里有什么?"},
          {"type": "input_image", "image_url": "https://example.com/cat.png"}
        ]
      }
    ]
  }'
```

也支持流式（`"stream": true`）和 `/v1/chat/completions` 兼容端点。

### 4. 查看历史与缓存

- 「控制台 → 历史记录」：查看每次请求的模型组、图片数、缓存命中、Token、延迟、输入输出摘要
- 「控制台 → 仪表盘」：总览统计（成功率、缓存命中率、平均延迟、Token 总量）
- 缓存可在仪表盘/历史页查看统计，管理员可一键清空

---

## API 一览

### 代理端点（OpenAI 兼容，无需 Web UI 鉴权）

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/v1/models` | 列出所有启用的模型组 |
| POST | `/v1/responses` | Responses API（含流式） |
| POST | `/v1/chat/completions` | Chat Completions 兼容（含流式） |

### 管理端点（需 `Authorization: Bearer <token>`）

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/api/login` | 登录获取 token |
| GET | `/api/user` | 当前用户 |
| POST | `/api/user/password` | 修改密码 |
| GET \| POST | `/api/groups` | 列出 / 创建模型组 |
| GET \| PUT \| DELETE | `/api/groups/:id` | 查 / 改 / 删单个模型组 |
| POST | `/api/groups/:id/toggle` | 启用/禁用 |
| GET | `/api/history` | 历史列表（支持筛选） |
| GET | `/api/history/:id` | 历史详情 |
| DELETE | `/api/history` | 清空历史（admin） |
| GET | `/api/history/stats` | 统计 |
| GET | `/api/cache/stats` | 缓存统计 |
| DELETE | `/api/cache` | 清空缓存（admin） |

完整字段定义见源码 [internal/model/model.go](internal/model/model.go)。

---

## 缓存机制（重点）

缓存是本项目的核心要求，设计上**绝不破坏缓存命中率**：

1. **规范化缓存键**：请求到达后，对 `input` 数组做确定性规范化（[internal/proxy/normalize.go](internal/proxy/normalize.go)）：
   - 对 `input` 数组与每个 `content` 数组按字段排序
   - **图片内容用稳定哈希占位**——同一张图片无论以 `url` 还是 `base64` 传入，都产生相同缓存键
   - 剔除易变字段（`id` / `created_at` / `timestamp`）
2. **稳定键**：用 `模型组名 + 规范化后的 input` 做 SHA-256，作为缓存键
3. **流式可回放**：流式响应的 SSE 事件序列会被完整缓存，命中时原样回放
4. **乱序同语义命中**：字段顺序不同但语义相同的请求会命中同一缓存条目

效果：相同请求只打一次上游，后续全部走缓存；图片识别结果也会被缓存，避免重复识别。

---

## 项目结构

```
fuck-chat-img/
├── main.go                     # 入口
├── internal/
│   ├── config/                 # 环境变量配置
│   ├── model/                  # 数据模型 + SQLite (gorm)
│   ├── auth/                   # JWT 鉴权中间件
│   ├── cache/                  # LRU 缓存
│   ├── proxy/                  # 代理核心
│   │   ├── responses.go        #   Responses API 处理
│   │   ├── chat.go             #   Chat Completions 处理
│   │   ├── image.go            #   图片识别(轮询)
│   │   ├── normalize.go        #   输入规范化(缓存键)
│   │   └── types.go            #   运行时类型
│   └── api/                    # 管理 API + 路由 + 静态服务
├── web/                        # 前端
│   ├── embed.go                # go:embed 嵌入 dist
│   ├── src/
│   │   ├── pages/              #   Login/Dashboard/ModelGroup/ModelPlaza/History/Settings
│   │   ├── components/layout/  #   PageLayout/HeaderBar/SiderBar
│   │   ├── helpers/            #   api.js / auth.jsx
│   │   └── context/Theme.jsx   #   主题
│   └── dist/                   # 构建产物(嵌入二进制)
├── Dockerfile
├── Makefile
└── .env.example
```

---

## 技术栈

- **后端**：Go 1.25 · Gin · GORM + SQLite · JWT
- **前端**：React 19 · @douyinfe/semi-ui · Tailwind CSS · rsbuild · lucide-react
- **UI 设计**：复刻 [newapi](https://github.com/Calcium-Ion/new-api) 设计语言
- **部署**：单二进制（go:embed）/ Docker

---

## 开发

```bash
# 跑测试
go test ./...

# 构建前端(开发时)
cd web && bun run build

# 重新构建后端(嵌入最新前端)
go build -o bin/fuck-chat-img .
```

---

## 致谢

- [newapi](https://github.com/Calcium-Ion/new-api) —— Web UI 设计语言参考
- [Semi UI](https://semi.design/) —— 组件库
- [OpenAI](https://platform.openai.com/docs/api-reference/responses) —— Responses API 规范

---

## ⚠️ AI 生成声明

> **本项目完全由 AI（GLM-5.2）生成，请谨慎辨别使用。**
>
> 代码、文档、配置均未经人工逐行审查，可能存在逻辑缺陷、安全隐患或与最新上游 API 不兼容的情况。生产环境使用前请务必自行充分测试与审计。
>
> 如发现问题或有改进建议，**欢迎提交 PR**：https://github.com/chenming0v0/fuck-chat-img/pulls

---

## License

MIT
