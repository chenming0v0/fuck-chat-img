# ===== 构建前端 =====
FROM node:20-alpine AS web-builder
WORKDIR /app/web
RUN npm install -g bun
COPY web/package.json web/bun.lock* web/package-lock.json* ./
RUN bun install --frozen-lockfile 2>/dev/null || bun install || npm install
COPY web/ ./
RUN bun run build

# ===== 构建后端 =====
FROM golang:1.25-alpine AS go-builder
WORKDIR /app
RUN apk add --no-cache gcc musl-dev
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web-builder /app/web/dist ./web/dist
RUN CGO_ENABLED=1 go build -o /out/fuck-chat-img .

# ===== 运行 =====
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=go-builder /out/fuck-chat-img /app/fuck-chat-img
RUN mkdir -p /app/data
ENV FCI_LISTEN=:8080
ENV FCI_DB_PATH=/app/data/fci.db
ENV FCI_WEB_DIR=
EXPOSE 8080
ENTRYPOINT ["/app/fuck-chat-img"]
