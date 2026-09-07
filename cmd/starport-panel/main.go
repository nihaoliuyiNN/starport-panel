// Command starport-panel 是 Starport 的控制面：一个可用区 / 一套集群部署一个，
// 纳管节点（经 starport-agent）、装 Kubernetes、管理集群与应用，并对外提供 API。
//
// 用法：
//
//	starport-panel serve --http :8080 --grpc :9192 --bootstrap-token <token>
//	starport-panel token create --name ci      # 签发 API 令牌（明文只打印一次）
//	starport-panel token list | revoke <id>
//	starport-panel backup --out /backup/panel.db   # 数据库一致性快照
//	starport-panel version
//
// 参数亦可用环境变量：STARPORT_HTTP_ADDR / STARPORT_GRPC_ADDR / STARPORT_BOOTSTRAP_TOKEN /
// STARPORT_API_TOKEN（静态 API 令牌，可选）/ STARPORT_DATA_DIR / STARPORT_TLS_CERT / STARPORT_TLS_KEY /
// STARPORT_GRPC_ENDPOINTS（逗号分隔，下发给 agent 的 gRPC 入口；空则按注册请求的主机推导）。
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"starport-panel/internal/panel"
	"starport-panel/internal/panel/store"
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
	case "token":
		tokenCmd(os.Args[2:])
	case "backup":
		backupCmd(os.Args[2:])
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
	fs.StringVar(&cfg.DataDir, "data-dir", env("STARPORT_DATA_DIR", defaultDataDir()), "状态目录（SQLite 数据库等）")
	fs.StringVar(&cfg.BootstrapToken, "bootstrap-token", env("STARPORT_BOOTSTRAP_TOKEN", ""), "agent 引导注册令牌（必填）")
	fs.StringVar(&cfg.APIToken, "api-token", env("STARPORT_API_TOKEN", ""), "静态 API 令牌（可选，与 `token create` 签发的令牌并行有效）")
	fs.BoolVar(&cfg.InsecureNoAuth, "insecure-no-auth", false, "关闭 API 鉴权（仅本机开发）")
	fs.StringVar(&endpoints, "grpc-endpoints", env("STARPORT_GRPC_ENDPOINTS", ""), "下发给 agent 的 gRPC 入口（逗号分隔 host:port）；空则按注册请求的主机推导")
	fs.StringVar(&cfg.TLSCert, "tls-cert", env("STARPORT_TLS_CERT", ""), "TLS 证书（PEM，全链）；与 --tls-key 同时给出则 HTTP 与 gRPC 都启用 TLS")
	fs.StringVar(&cfg.TLSKey, "tls-key", env("STARPORT_TLS_KEY", ""), "TLS 私钥（PEM）")
	_ = fs.Parse(args)

	if cfg.BootstrapToken == "" {
		log.Fatal("缺少 --bootstrap-token（或 STARPORT_BOOTSTRAP_TOKEN）")
	}
	if (cfg.TLSCert == "") != (cfg.TLSKey == "") {
		log.Fatal("--tls-cert 与 --tls-key 须同时给出")
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
	srv, err := panel.New(cfg)
	if err != nil {
		log.Fatalf("starport-panel 初始化失败: %v", err)
	}
	if err := srv.Run(ctx); err != nil && err != context.Canceled {
		log.Fatalf("starport-panel 退出: %v", err)
	}
	log.Printf("starport-panel 已停止")
}

// tokenCmd 直接操作面板数据库签发 / 列出 / 吊销 API 令牌（面板运行中亦可，SQLite WAL 允许并发读写）。
func tokenCmd(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "用法: starport-panel token <create --name <名称>|list|revoke <id>> [--data-dir DIR]")
		os.Exit(2)
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("token "+sub, flag.ExitOnError)
	dataDir := fs.String("data-dir", env("STARPORT_DATA_DIR", defaultDataDir()), "状态目录（与 serve 一致）")
	name := fs.String("name", "", "令牌名称（create 必填，如 ci / admin-ui）")
	// 允许位置参数放在 flag 之前（revoke <id> --data-dir x）
	var positional []string
	for len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		positional, rest = append(positional, rest[0]), rest[1:]
	}
	_ = fs.Parse(rest)
	positional = append(positional, fs.Args()...)

	st, err := store.Open(filepath.Join(*dataDir, "panel.db"))
	if err != nil {
		log.Fatalf("打开数据库失败: %v", err)
	}
	defer st.Close()
	ctx := context.Background()

	switch sub {
	case "create":
		if *name == "" {
			log.Fatal("缺少 --name")
		}
		t, plain, err := st.CreateToken(ctx, *name)
		if err != nil {
			log.Fatalf("签发失败: %v", err)
		}
		fmt.Printf("已签发令牌 #%d（%s）。明文只显示这一次，请妥善保存：\n\n  %s\n\n", t.ID, t.Name, plain)
		fmt.Printf("用法: curl -H 'Authorization: Bearer %s' http://<panel>/api/v1/nodes\n", plain)
	case "list":
		ts, err := st.ListTokens(ctx)
		if err != nil {
			log.Fatalf("读取失败: %v", err)
		}
		fmt.Printf("%-5s %-20s %-14s %-20s %-20s %s\n", "ID", "NAME", "PREFIX", "CREATED", "LAST USED", "STATUS")
		for _, t := range ts {
			status := "active"
			if t.Revoked() {
				status = "revoked " + t.RevokedAt.Local().Format("2006-01-02 15:04")
			}
			fmt.Printf("%-5d %-20s %-14s %-20s %-20s %s\n", t.ID, t.Name, t.Prefix+"…",
				t.CreatedAt.Local().Format("2006-01-02 15:04:05"), fmtTime(t.LastUsedAt), status)
		}
	case "revoke":
		if len(positional) < 1 {
			log.Fatal("用法: starport-panel token revoke <id>")
		}
		id, err := strconv.ParseUint(positional[0], 10, 64)
		if err != nil {
			log.Fatalf("非法 id: %s", positional[0])
		}
		if err := st.RevokeToken(ctx, id); err != nil {
			log.Fatalf("吊销失败: %v", err)
		}
		fmt.Printf("令牌 #%d 已吊销\n", id)
	default:
		fmt.Fprintf(os.Stderr, "未知子命令 token %s\n", sub)
		os.Exit(2)
	}
}

// backupCmd 生成数据库一致性快照：starport-panel backup --out /backup/panel-20260907.db
// 恢复：停面板，把快照文件拷回 <data-dir>/panel.db（删掉旧的 -wal / -shm），再启动。
func backupCmd(args []string) {
	fs := flag.NewFlagSet("backup", flag.ExitOnError)
	dataDir := fs.String("data-dir", env("STARPORT_DATA_DIR", defaultDataDir()), "状态目录（与 serve 一致）")
	out := fs.String("out", "", "输出文件（默认 <data-dir>/backups/panel-<时间>.db）")
	_ = fs.Parse(args)
	if *out == "" {
		*out = filepath.Join(*dataDir, "backups", "panel-"+time.Now().Format("20060102-150405")+".db")
	}
	st, err := store.Open(filepath.Join(*dataDir, "panel.db"))
	if err != nil {
		log.Fatalf("打开数据库失败: %v", err)
	}
	defer st.Close()
	if err := st.Backup(context.Background(), *out); err != nil {
		log.Fatalf("备份失败: %v", err)
	}
	fmt.Printf("已备份到 %s\n", *out)
}

func fmtTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Local().Format("2006-01-02 15:04:05")
}

func usage() {
	fmt.Fprintln(os.Stderr, "用法: starport-panel <serve|token|backup|version> [flags]")
}

// defaultDataDir Linux 服务器用 /var/lib；其它平台（开发机）用当前目录下的 data/。
func defaultDataDir() string {
	if runtime.GOOS == "linux" {
		return "/var/lib/starport-panel"
	}
	return "data"
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
