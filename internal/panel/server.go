// Package panel 是 starport-panel 控制面的装配层：HTTP API + agent gRPC 中枢。
//
// Phase 0 只有节点纳管所需的最小面：注册、节点列表、在节点上执行脚本（联调/排障用）。
// 集群生命周期、应用管理、Web UI 在后续阶段进入本包。
package panel

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"google.golang.org/grpc"

	"starport-panel/internal/panel/agenthub"
	pb "starport-panel/internal/pb/agentv1"
)

// Config 面板启动参数。
type Config struct {
	HTTPAddr       string // 如 :8080
	GrpcAddr       string // 如 :9192
	BootstrapToken string
	GrpcEndpoints  []string // 下发给 agent 的入口；空则按注册请求 Host 推导
	Version        string
}

// Server 面板进程。
type Server struct {
	cfg   Config
	store *agenthub.MemoryStore
	hub   *agenthub.Hub
	http  *http.Server
	grpc  *grpc.Server
}

// New 装配。
func New(cfg Config) *Server {
	store := agenthub.NewMemoryStore()
	_, grpcPort, _ := net.SplitHostPort(cfg.GrpcAddr)
	hub := agenthub.New(store, agenthub.Options{
		BootstrapToken: cfg.BootstrapToken,
		GrpcEndpoints:  cfg.GrpcEndpoints,
		GrpcPort:       grpcPort,
	})
	s := &Server{cfg: cfg, store: store, hub: hub}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") })
	mux.HandleFunc("GET /api/v1/version", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]string{"version": cfg.Version})
	})
	mux.Handle("POST "+agenthub.RegisterPath, hub.RegisterHandler())
	mux.HandleFunc("GET /api/v1/nodes", s.listNodes)
	mux.HandleFunc("POST /api/v1/nodes/{id}/exec", s.execNode)

	s.http = &http.Server{Addr: cfg.HTTPAddr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	s.grpc = grpc.NewServer(agenthub.ServerOptions()...)
	pb.RegisterNodeAgentServiceServer(s.grpc, hub)
	return s
}

// Run 启动 HTTP 与 gRPC，阻塞到 ctx 取消后优雅停机。
func (s *Server) Run(ctx context.Context) error {
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
		log.Printf("[panel] HTTP 监听 %s", s.cfg.HTTPAddr)
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

// Hub 暴露给上层业务（装机编排等）。
func (s *Server) Hub() *agenthub.Hub { return s.hub }

func (s *Server) listNodes(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, s.store.List())
}

// execNode 在节点上跑脚本，日志按行流式回写（text/plain），最后一行是终态 JSON。
// 联调/排障用；正式的任务接口在后续阶段做成异步任务 + 日志拉取。
func (s *Server) execNode(w http.ResponseWriter, r *http.Request) {
	nodeID, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "非法节点 ID", http.StatusBadRequest)
		return
	}
	var req struct {
		Script    string `json:"script"`
		TimeoutMs int64  `json:"timeoutMs"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil || strings.TrimSpace(req.Script) == "" {
		http.Error(w, "缺少 script", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	flusher, _ := w.(http.Flusher)
	onLog := func(line string) {
		_, _ = io.WriteString(w, line+"\n")
		if flusher != nil {
			flusher.Flush()
		}
	}
	res, err := s.hub.Exec(r.Context(), nodeID, req.Script, time.Duration(req.TimeoutMs)*time.Millisecond, onLog)
	if err != nil {
		if errors.Is(err, agenthub.ErrNodeOffline) {
			// 头已可能发出，只能在正文里报
			onLog(`{"error":"节点离线"}`)
			return
		}
		onLog(`{"error":"` + err.Error() + `"}`)
		return
	}
	b, _ := json.Marshal(map[string]any{"ok": res.OK, "exitCode": res.ExitCode, "error": res.Err})
	onLog(string(b))
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
