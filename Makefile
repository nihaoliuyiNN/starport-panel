# starport-panel 构建入口。Windows 请用 Git Bash / WSL，或直接执行对应的 go / pnpm 命令。
VERSION ?= dev
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all build panel agent agent-linux ui ui-dev proto lint test vet clean

all: build

build: ui panel agent

## 面板二进制（内嵌 internal/panel/ui/dist 里已构建的 Web UI；未执行 ui 则内嵌占位）
panel:
	go build -ldflags "$(LDFLAGS)" -o dist/starport-panel ./cmd/starport-panel

agent:
	go build -ldflags "$(LDFLAGS)" -o dist/starport-agent ./cmd/starport-agent

## agent 生产只跑在 Linux 节点：amd64 / arm64 静态二进制
agent-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/starport-agent-linux-amd64 ./cmd/starport-agent
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/starport-agent-linux-arm64 ./cmd/starport-agent

## Web UI：web/ → internal/panel/ui/dist（需 node ≥ 20 + pnpm）
ui:
	cd web && pnpm install --frozen-lockfile && pnpm build

## UI 开发服务器（:5173，/api 代理到 :8080 的面板）
ui-dev:
	cd web && pnpm install && pnpm dev

## 从 proto/ 重新生成 internal/pb（需 buf + protoc-gen-go + protoc-gen-go-grpc）
proto:
	cd proto && buf lint && buf generate

lint:
	cd proto && buf lint
	go vet ./...
	cd web && pnpm typecheck

vet:
	go vet ./...

test:
	go test ./...

clean:
	rm -rf dist
	find internal/panel/ui/dist -mindepth 1 ! -name .gitkeep -delete
