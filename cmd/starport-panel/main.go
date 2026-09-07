// Command starport-panel 是 Starport 的控制面：一个可用区 / 一套集群部署一个，
// 纳管节点（经 starport-agent）、装 Kubernetes、管理集群与应用，并对外提供 API。
//
// 用法：
//
//	starport-panel serve --http :8080 --grpc :9192 --bootstrap-token <token>
//	starport-panel version
//
// 参数亦可用环境变量：STARPORT_HTTP_ADDR / STARPORT_GRPC_ADDR / STARPORT_BOOTSTRAP_TOKEN /
// STARPORT_GRPC_ENDPOINTS（逗号分隔，下发给 agent 的 gRPC 入口；空则按注册请求的主机推导）。
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"starport-panel/internal/panel"
)

// version 编译期可用 -ldflags "-X main.version=x.y.z" 注入。
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "serve":
		serve(os.Args[2:])
	case "version", "--version", "-v":
		fmt.Printf("starport-panel %s\n", version)
	default:
		usage()
		os.Exit(2)
	}
}

func serve(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	var cfg panel.Config
	var endpoints string
	fs.StringVar(&cfg.HTTPAddr, "http", env("STARPORT_HTTP_ADDR", ":8080"), "HTTP 监听地址（API / UI / agent 注册）")
	fs.StringVar(&cfg.GrpcAddr, "grpc", env("STARPORT_GRPC_ADDR", ":9192"), "gRPC 监听地址（agent 呼出长连）")
	fs.StringVar(&cfg.BootstrapToken, "bootstrap-token", env("STARPORT_BOOTSTRAP_TOKEN", ""), "agent 引导注册令牌（必填）")
	fs.StringVar(&endpoints, "grpc-endpoints", env("STARPORT_GRPC_ENDPOINTS", ""), "下发给 agent 的 gRPC 入口（逗号分隔 host:port）；空则按注册请求的主机推导")
	_ = fs.Parse(args)

	if cfg.BootstrapToken == "" {
		log.Fatal("缺少 --bootstrap-token（或 STARPORT_BOOTSTRAP_TOKEN）")
	}
	for _, e := range strings.Split(endpoints, ",") {
		if e = strings.TrimSpace(e); e != "" {
			cfg.GrpcEndpoints = append(cfg.GrpcEndpoints, e)
		}
	}
	cfg.Version = version

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Printf("starport-panel %s 启动", version)
	if err := panel.New(cfg).Run(ctx); err != nil && err != context.Canceled {
		log.Fatalf("starport-panel 退出: %v", err)
	}
	log.Printf("starport-panel 已停止")
}

func usage() {
	fmt.Fprintln(os.Stderr, "用法: starport-panel <serve|version> [flags]")
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
