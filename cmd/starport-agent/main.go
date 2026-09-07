// Command starport-agent 驻在每台被纳管的节点（裸机 / 物理机 / ECS）上，主动呼出连 starport-panel：
// 面板经 gRPC 双向流下发脚本 / 结构化装机指令 / 终端会话，agent 在本机执行并流式回传。
//
// 用法：
//
//	starport-agent --server http://panel.example.com:8080 --token <bootstrap-token>
//
// 各参数也可用环境变量：STARPORT_SERVER_URL / STARPORT_BOOTSTRAP_TOKEN /
// STARPORT_DATA_DIR / STARPORT_SHELL / STARPORT_GRPC_ENDPOINTS / STARPORT_GRPC_TLS。
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"starport-panel/internal/agent"
)

// version 编译期可用 -ldflags "-X main.version=x.y.z" 注入。
var version = "dev"

func main() {
	var cfg agent.Config
	flag.StringVar(&cfg.ServerURL, "server", env("STARPORT_SERVER_URL", ""), "面板 HTTP 地址，如 http://panel.example.com:8080")
	flag.StringVar(&cfg.BootstrapToken, "token", env("STARPORT_BOOTSTRAP_TOKEN", ""), "一次性引导令牌（仅首次注册用）")
	flag.StringVar(&cfg.DataDir, "data-dir", env("STARPORT_DATA_DIR", "/var/lib/starport-agent"), "身份持久化与临时脚本目录")
	flag.StringVar(&cfg.Shell, "shell", env("STARPORT_SHELL", "/bin/bash"), "执行脚本的 shell")
	flag.StringVar(&cfg.GrpcEndpoints, "grpc", env("STARPORT_GRPC_ENDPOINTS", ""), "可选：面板 gRPC 入口（逗号分隔 host:port），覆盖注册应答下发的值")
	flag.BoolVar(&cfg.GrpcTLS, "grpc-tls", env("STARPORT_GRPC_TLS", "") == "true", "可选：强制 gRPC 走 TLS")
	showVersion := flag.Bool("version", false, "打印版本后退出")
	flag.Parse()

	if *showVersion {
		log.Printf("starport-agent %s", version)
		return
	}
	cfg.AgentVersion = version

	if cfg.ServerURL == "" {
		log.Fatal("缺少 --server（或 STARPORT_SERVER_URL）")
	}

	// SIGINT/SIGTERM 优雅退出：ctx 取消后 Run 收链返回
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Printf("starport-agent %s 启动，面板=%s", version, cfg.ServerURL)
	if err := agent.Run(ctx, cfg); err != nil && err != context.Canceled {
		log.Fatalf("starport-agent 退出: %v", err)
	}
	log.Printf("starport-agent 已停止")
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
