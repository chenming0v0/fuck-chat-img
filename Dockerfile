# syntax=docker/dockerfile:1

# ===== Stage 1: 前端构建 =====
FROM node:20-alpine AS web-builder
WORKDIR /web
# 先拷贝依赖描述以利用层缓存
COPY web/package.json web/bun.lock* web/package-lock.json* ./
# 优先 bun(--frozen-lockfile 保证可复现); 失败则回退 npm. 不再吞掉 stderr,
# 锁文件不一致时显式失败而非悄悄回退, 保证构建可复现性.
RUN if command -v bun >/dev/null 2>&1; then \
        bun install --frozen-lockfile; \
    else \
        npm install; \
    fi
COPY web/ ./
RUN if command -v bun >/dev/null 2>&1; then bun run build; else npm run build; fi

# ===== Stage 2: 后端构建 =====
FROM golang:1.23-alpine AS go-builder
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
FROM alpine:3.20
# 非 root 运行(容器最小权限)
RUN addgroup -S app && adduser -S app -G app && \
    apk add --no-cache ca-certificates tzdata && \
    mkdir -p /app/data && chown -R app:app /app
WORKDIR /app
COPY --from=go-builder /out/fuck-chat-img /app/fuck-chat-img
# 前端嵌入二进制, 无需单独拷贝 dist; 此处仅声明数据卷
ENV FCI_LISTEN=:8080 \
    FCI_DB_PATH=/app/data/fci.db \
    FCI_WEB_DIR=
# 数据持久化: SQLite 落盘到 /app/data, 宿主应挂载卷
VOLUME ["/app/data"]
USER app
EXPOSE 8080
ENTRYPOINT ["/app/fuck-chat-img"]
