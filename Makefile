# starport-panel 构建入口。Windows 请用 Git Bash / WSL，或直接执行对应的 go 命令。
VERSION ?= dev
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all build panel agent agent-linux proto lint test vet clean

all: build

build: panel agent

panel:
	go build -ldflags "$(LDFLAGS)" -o dist/starport-panel ./cmd/starport-panel

agent:
	go build -ldflags "$(LDFLAGS)" -o dist/starport-agent ./cmd/starport-agent

## agent 生产只跑在 Linux 节点：amd64 / arm64 静态二进制
agent-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/starport-agent-linux-amd64 ./cmd/starport-agent
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/starport-agent-linux-arm64 ./cmd/starport-agent

## 从 proto/ 重新生成 internal/pb（需 buf + protoc-gen-go + protoc-gen-go-grpc）
proto:
	cd proto && buf lint && buf generate

lint:
	cd proto && buf lint
	go vet ./...

vet:
	go vet ./...

test:
	go test ./...

clean:
	rm -rf dist
