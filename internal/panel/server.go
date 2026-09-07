// Package panel 是 starport-panel 控制面的装配层：SQLite 存储、agent gRPC 中枢、任务执行器、
// 集群编排与 HTTP API 在此拼装；业务逻辑在各子包。
package panel

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"path/filepath"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"starport-panel/internal/panel/agenthub"
	"starport-panel/internal/panel/cluster"
	"starport-panel/internal/panel/helm"
	"starport-panel/internal/panel/kube"
	"starport-panel/internal/panel/store"
	"starport-panel/internal/panel/task"
	pb "starport-panel/internal/pb/agentv1"
)

// Config 面板启动参数。
type Config struct {
	HTTPAddr       string // 如 :8080
	GrpcAddr       string // 如 :9192
	DataDir        string // SQLite 等状态目录
	BootstrapToken string
	GrpcEndpoints  []string // 下发给 agent 的入口；空则按注册请求 Host 推导
	Version        string
	// APIToken 静态 API 令牌（可选）：适合自动化 / 上层系统按配置注入；与库内令牌并行有效。
	APIToken string
	// InsecureNoAuth 关闭 API 鉴权（仅本机开发）。
	InsecureNoAuth bool
	// TLSCert / TLSKey 同时用于 HTTP 与 gRPC；都非空即启用 TLS，注册应答会告知 agent 走 TLS。
	// agent 用系统信任库校验证书，因此须是公网可信证书（如 Let's Encrypt），自签名证书需要另行分发到节点。
	TLSCert string
	TLSKey  string
}

// TLS 是否启用 TLS。
func (c Config) TLS() bool { return c.TLSCert != "" && c.TLSKey != "" }

// Server 面板进程。
type Server struct {
	cfg      Config
	store    *store.Store
	hub      *agenthub.Hub
	tasks    *task.Runner
	clusters *cluster.Service
	kube     *kube.Client
	helm     *helm.Client
	http     *http.Server
	grpc     *grpc.Server
}

// New 装配（打开数据库、建各子系统、注册路由）。
func New(cfg Config) (*Server, error) {
	st, err := store.Open(filepath.Join(cfg.DataDir, "panel.db"))
	if err != nil {
		return nil, err
	}
	// 新进程还没有任何连接：上次留下的在线标记与 running 任务全部作废
	if err := st.MarkAllOffline(); err != nil {
		return nil, err
	}
	if n, err := st.FailRunningTasks(context.Background()); err != nil {
		return nil, err
	} else if n > 0 {
		log.Printf("[panel] 上次进程遗留 %d 个运行中任务已标记失败", n)
	}

	_, grpcPort, _ := net.SplitHostPort(cfg.GrpcAddr)
	hub := agenthub.New(st, agenthub.Options{
		BootstrapToken: cfg.BootstrapToken,
		GrpcEndpoints:  cfg.GrpcEndpoints,
		GrpcPort:       grpcPort,
		GrpcTLS:        cfg.TLS(),
	})
	tasks := task.New(st)
	s := &Server{
		cfg:      cfg,
		store:    st,
		hub:      hub,
		tasks:    tasks,
		clusters: cluster.New(st, hub, tasks),
		kube:     kube.New(),
		helm:     helm.New(),
	}
	s.clusters.OnDeleted(s.kube.Forget)

	// WebSocket 长连接（终端 / 日志 / exec）不能有写超时；HTTP 服务器只限制请求头读取
	if cfg.InsecureNoAuth {
		log.Printf("[panel] 警告：API 鉴权已关闭（--insecure-no-auth），仅限本机开发")
	} else if n, _ := st.CountActiveTokens(context.Background()); n == 0 && cfg.APIToken == "" {
		log.Printf("[panel] 尚无 API 令牌：除 agent 注册外所有 API 将拒绝访问；请执行 `starport-panel token create --name <名称>`")
	}
	s.http = &http.Server{Addr: cfg.HTTPAddr, Handler: s.auth(s.audit(s.routes())), ReadHeaderTimeout: 10 * time.Second}
	grpcOpts := agenthub.ServerOptions()
	if cfg.TLS() {
		creds, err := credentials.NewServerTLSFromFile(cfg.TLSCert, cfg.TLSKey)
		if err != nil {
			_ = st.Close()
			return nil, fmt.Errorf("加载 TLS 证书: %w", err)
		}
		grpcOpts = append(grpcOpts, grpc.Creds(creds))
	}
	s.grpc = grpc.NewServer(grpcOpts...)
	pb.RegisterNodeAgentServiceServer(s.grpc, hub)
	return s, nil
}

// Run 启动 HTTP 与 gRPC，阻塞到 ctx 取消后优雅停机。
func (s *Server) Run(ctx context.Context) error {
	defer s.store.Close()
	lis, err := net.Listen("tcp", s.cfg.GrpcAddr)
	if err != nil {
		return err
	}
	errCh := make(chan error, 2)
	go func() {
		log.Printf("[panel] gRPC 监听 %s（agent 呼出入口）", s.cfg.GrpcAddr)
		errCh <- s.grpc.Serve(lis)
	}()
	go func() {
		var err error
		if s.cfg.TLS() {
			log.Printf("[panel] HTTPS 监听 %s，数据目录 %s", s.cfg.HTTPAddr, s.cfg.DataDir)
			err = s.http.ListenAndServeTLS(s.cfg.TLSCert, s.cfg.TLSKey)
		} else {
			log.Printf("[panel] HTTP 监听 %s，数据目录 %s", s.cfg.HTTPAddr, s.cfg.DataDir)
			err = s.http.ListenAndServe()
		}
		if !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = s.http.Shutdown(shutdownCtx)
	s.grpc.GracefulStop()
	return ctx.Err()
}
