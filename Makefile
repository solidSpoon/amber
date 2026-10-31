# rigd 的构建入口。约定：make 只做「本地能复现」的事，不隐式联网下载（web 依赖需先 npm install）。
SHELL := /bin/bash
BIN := bin/rigd
PKG := ./...
GOFLAGS := -trimpath

.PHONY: help dev build web test lint tidy clean run mcp icons

help:
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-8s\033[0m %s\n", $$1, $$2}'

build: web ## 编译二进制（含内嵌前端）
	go build $(GOFLAGS) -o $(BIN) ./cmd/rigd

web: ## 构建 React 管理台到 web/dist（会被 go:embed 打进二进制）
	cd web && npm run build

dev: ## 本地开发：后端 + 前端热更新（两个进程，前端 /api 代理到后端）
	@echo "后端：RIGD_ADDR=127.0.0.1:8088 go run ./cmd/rigd"
	@echo "前端：cd web && npm run dev"

test: ## 跑全部测试
	go test ./...

lint: ## 静态检查（需要 golangci-lint）
	golangci-lint run

tidy: ## 整理依赖
	go mod tidy

run: build ## 本地跑起来（数据目录可覆盖）
	RIGD_ADDR=$${RIGD_ADDR:-127.0.0.1:8088} $(BIN)

mcp: build ## 以 MCP stdio 模式运行（给外部 AI 客户端用）
	$(BIN) --mcp

clean:
	rm -rf bin web/dist

icons: ## 重新生成品牌图标（SVG + 1024 位图，源码见 cmd/ambericon）
	go run ./cmd/ambericon
