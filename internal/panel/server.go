// Package panel 是 starport-panel 控制面的装配层：SQLite 存储、agent gRPC 中枢、任务执行器、
// 集群编排与 HTTP API 在此拼装；业务逻辑在各子包。
package panel

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"path/filepath"
	"time"

	"google.golang.org/grpc"

	"starport-panel/internal/panel/agenthub"
	"starport-panel/internal/panel/cluster"
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
}

// Server 面板进程。
type Server struct {
	cfg      Config
	store    *store.Store
	hub      *agenthub.Hub
	tasks    *task.Runner
	clusters *cluster.Service
	kube     *kube.Client
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
	})
	tasks := task.New(st)
	s := &Server{
		cfg:      cfg,
		store:    st,
		hub:      hub,
		tasks:    tasks,
		clusters: cluster.New(st, hub, tasks),
		kube:     kube.New(),
	}

	s.http = &http.Server{Addr: cfg.HTTPAddr, Handler: s.routes(), ReadHeaderTimeout: 10 * time.Second}
	s.grpc = grpc.NewServer(agenthub.ServerOptions()...)
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
		log.Printf("[panel] HTTP 监听 %s，数据目录 %s", s.cfg.HTTPAddr, s.cfg.DataDir)
		if err := s.http.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
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
