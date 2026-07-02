.PHONY: all web build run clean dev test vet fmt

# 前端构建工具(优先 bun, 回退 npm)
WEB_BUILD := $(shell command -v bun >/dev/null 2>&1 && echo "bun run build" || echo "npm run build")

# CGO: sqlite 驱动(mattn/go-sqlite3)需要 C 编译器, 测试/构建必须开启
export CGO_ENABLED := 1

all: web build

# 构建前端
web:
	cd web && $(if $(shell command -v bun >/dev/null 2>&1),bun install --frozen-lockfile,npm ci)
	cd web && $(WEB_BUILD)

# ensure-dist: 保证 web/dist 存在(//go:embed dist 要求至少一个文件),
# 干净 checkout 下未构建前端时提供占位, 避免 go build/test 编译失败.
# 已构建的真实 dist 不会被覆盖.
ensure-dist:
	@if [ ! -d web/dist ] || [ -z "$$(ls -A web/dist 2>/dev/null)" ]; then \
		echo "[ensure-dist] web/dist 不存在, 生成占位产物以支持编译"; \
		mkdir -p web/dist; \
		printf '<!doctype html><html><head><meta charset="utf-8"><title>fuck-chat-img</title></head><body>Web UI not built. Run <code>make web</code>.</body></html>' > web/dist/index.html; \
	fi

# 构建后端(前端产物已嵌入). 依赖 ensure-dist, 保证干净环境也能编译.
# -ldflags="-s -w" 去除调试符号与 DWARF 信息, 与 Dockerfile 构建保持一致.
build: ensure-dist
	go build -ldflags="-s -w" -o bin/fuck-chat-img .

# 运行
run: build
	./bin/fuck-chat-img

# 开发模式(前后端分别启动): 后端 :8080, 前端 dev server 自带代理
dev:
	@echo "终端1: go run ."
	@echo "终端2: cd web && bun run dev (代理 /api 到 :8080)"

# 测试: 依赖 ensure-dist(embed 编译) + CGO(sqlite)
test: ensure-dist
	go test ./...

# 静态检查
vet: ensure-dist
	go vet ./...

# 格式化
fmt:
	go fmt ./...

# 注意: clean 不删除 data/ (用户 SQLite 数据库), 误删会导致全部账户/模型组/历史丢失.
# 如需清空数据重新走 /setup 流程, 请显式 `rm -rf data`.
clean:
	rm -rf bin web/dist web/node_modules
