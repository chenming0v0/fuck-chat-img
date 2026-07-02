# syntax=docker/dockerfile:1

# ===== Stage 1: 前端构建 =====
FROM node:20-alpine AS web-builder
WORKDIR /web
# 先拷贝依赖描述以利用层缓存
COPY web/package.json web/bun.lock* web/package-lock.json* ./
# 优先 bun(--frozen-lockfile 保证可复现); 回退 npm ci(基于 package-lock.json, 同样可复现).
# 不再吞掉 stderr, 锁文件不一致时显式失败而非悄悄回退, 保证构建可复现性.
RUN if command -v bun >/dev/null 2>&1; then \
        bun install --frozen-lockfile; \
    else \
        npm ci; \
    fi
COPY web/ ./
RUN if command -v bun >/dev/null 2>&1; then bun run build; else npm run build; fi

# ===== Stage 2: 后端构建 =====
# go.mod 声明 go 1.25, 这里使用匹配的 golang 镜像(>=1.25).
FROM golang:1.25-alpine AS go-builder
WORKDIR /app
# sqlite 驱动(mattn/go-sqlite3)需要 CGO + gcc
RUN apk add --no-cache gcc musl-dev
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# 前端产物就位
COPY --from=web-builder /web/dist ./web/dist
ENV CGO_ENABLED=1
RUN go build -ldflags="-s -w" -o /out/fuck-chat-img .

# ===== Stage 3: 运行时 =====
# 与 go-builder 的 golang:1.25-alpine(基于 alpine 3.21) 保持一致, 避免跨 alpine 大版本的 musl 版本错配
FROM alpine:3.21
# 非 root 运行(容器最小权限)
RUN addgroup -S app && adduser -S app -G app && \
    apk add --no-cache ca-certificates tzdata && \
    mkdir -p /app/data && chown -R app:app /app
WORKDIR /app
COPY --chown=app:app --from=go-builder /out/fuck-chat-img /app/fuck-chat-img
# 前端嵌入二进制, 无需单独拷贝 dist; 此处仅声明数据卷
ENV FCI_LISTEN=:8080 \
    FCI_DB_PATH=/app/data/fci.db \
    FCI_WEB_DIR=
# 数据持久化: SQLite 落盘到 /app/data, 宿主应挂载卷
VOLUME ["/app/data"]
USER app
EXPOSE 8080
# 健康检查: /api/status 是公开接口(返回 need_setup 等), 无需鉴权; 用 alpine 自带 busybox wget, 无需额外装包.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget --spider -q http://127.0.0.1:8080/api/status || exit 1
ENTRYPOINT ["/app/fuck-chat-img"]
