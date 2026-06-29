.PHONY: all web build run clean dev test

# 前端构建工具(优先 bun, 回退 npm)
WEB_BUILD := $(shell command -v bun >/dev/null 2>&1 && echo "bun run build" || echo "npm run build")

all: web build

# 构建前端
web:
	cd web && $(if $(shell command -v bun >/dev/null 2>&1),bun install,npm install)
	cd web && $(WEB_BUILD)

# 构建后端(前端产物已嵌入)
build:
	go build -o bin/fuck-chat-img .

# 运行
run: build
	./bin/fuck-chat-img

# 开发模式(前后端分别启动): 后端 :8080, 前端 dev server 自带代理
dev:
	@echo "终端1: go run ."
	@echo "终端2: cd web && bun run dev (代理 /api 到 :8080)"

test:
	go test ./...

clean:
	rm -rf bin web/dist web/node_modules data
