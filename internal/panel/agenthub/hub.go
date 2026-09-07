// Package agenthub 是面板侧的 agent 连接中枢：接受 starport-agent 的 gRPC 呼出长连，
// 维护 nodeId → 连接表，把面板的「在某节点执行脚本 / 装机 / 开终端」翻成流上的帧，
// 再把回传的 log / result / session_* 按 requestId 关联回调用方。
//
// 契约见 proto/agent/v1/agent.proto；帧语义与 internal/agent 侧一一对应。
package agenthub

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"starport-panel/internal/agent"
	pb "starport-panel/internal/pb/agentv1"
)

// 错误。
var (
	ErrNodeOffline = errors.New("agenthub: node offline")
	ErrNoSession   = errors.New("agenthub: session not found")
)

// Result exec / install 的终态。
type Result struct {
	OK       bool
	ExitCode int
	Err      *agent.Error
	Install  *pb.InstallResult // 仅装机成功时有
}

// SessionSink 一条长会话（终端 / logs -f）的输出消费者。
type SessionSink interface {
	OnData(data []byte)
	OnExit(exitCode int, err *agent.Error)
}

// Options hub 参数。
type Options struct {
	// BootstrapToken 引导注册令牌；空则拒绝一切注册。
	BootstrapToken string
	// GrpcEndpoints 注册应答里下发给 agent 的 gRPC 入口（host:port）。
	// 空则由注册请求的 Host 推导（同主机 + GrpcPort）。
	GrpcEndpoints []string
	// GrpcPort 推导入口时用的端口。
	GrpcPort string
	// GrpcTLS 入口是否 TLS。
	GrpcTLS bool
	// HeartbeatInterval 下发给 agent 的心跳节奏；0 用 15s。
	HeartbeatInterval time.Duration
}

// Hub 见包注释。实现 pb.NodeAgentServiceServer。
type Hub struct {
	pb.UnimplementedNodeAgentServiceServer

	store Store
	opts  Options

	mu       sync.RWMutex
	conns    map[uint64]*conn
	pending  map[string]*request // exec / install，requestId → 等待方
	sessions map[string]*sessionEntry
}

// New 建 hub。
func New(store Store, opts Options) *Hub {
	if opts.HeartbeatInterval <= 0 {
		opts.HeartbeatInterval = 15 * time.Second
	}
	if opts.GrpcPort == "" {
		opts.GrpcPort = agent.DefaultGrpcPort
	}
	return &Hub{
		store:    store,
		opts:     opts,
		conns:    make(map[uint64]*conn),
		pending:  make(map[string]*request),
		sessions: make(map[string]*sessionEntry),
	}
}

// ── gRPC 服务端 ─────────────────────────────────────────────────────────────

// ServerOptions 面板 gRPC 服务端推荐参数：与 agent 的 keepalive 配置对齐，放开单帧上限。
func ServerOptions() []grpc.ServerOption {
	return []grpc.ServerOption{
		grpc.MaxRecvMsgSize(4 << 20),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			Time:    30 * time.Second,
			Timeout: 10 * time.Second,
		}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             10 * time.Second, // agent 端 20s，留余量
			PermitWithoutStream: true,
		}),
	}
}

// Connect 一条 agent 呼出长连的整个生命周期。
func (h *Hub) Connect(stream pb.NodeAgentService_ConnectServer) error {
	nodeID, err := h.authenticate(stream.Context())
	if err != nil {
		return err
	}
	c := newConn(nodeID, stream)
	h.attach(c)
	defer h.detach(c)

	log.Printf("[agenthub] 连接建立 nodeId=%d", nodeID)
	// 出站唯一写者
	go c.writeLoop()

	for {
		f, err := stream.Recv()
		if err != nil {
			if status.Code(err) != codes.Canceled {
				log.Printf("[agenthub] 连接结束 nodeId=%d: %v", nodeID, err)
			}
			return nil
		}
		h.dispatch(c, f)
	}
}

func (h *Hub) authenticate(ctx context.Context) (uint64, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	token := first(md, agent.MetaAgentToken)
	if token == "" {
		return 0, status.Error(codes.Unauthenticated, "缺少 agent token")
	}
	nodeID, ok := h.store.Authenticate(token)
	if !ok {
		return 0, status.Error(codes.Unauthenticated, "agent token 无效")
	}
	return nodeID, nil
}

func (h *Hub) attach(c *conn) {
	h.mu.Lock()
	old := h.conns[c.nodeID]
	h.conns[c.nodeID] = c
	h.mu.Unlock()
	if old != nil {
		log.Printf("[agenthub] nodeId=%d 重连，踢掉旧连接", c.nodeID)
		old.close()
	}
}

func (h *Hub) detach(c *conn) {
	c.close()
	h.mu.Lock()
	current := h.conns[c.nodeID] == c
	if current {
		delete(h.conns, c.nodeID)
	}
	// 该连接上未完成的请求与会话全部以离线收尾
	var reqs []*request
	var sess []*sessionEntry
	for id, r := range h.pending {
		if r.nodeID == c.nodeID && r.conn == c {
			reqs = append(reqs, r)
			delete(h.pending, id)
		}
	}
	for id, s := range h.sessions {
		if s.nodeID == c.nodeID && s.conn == c {
			sess = append(sess, s)
			delete(h.sessions, id)
		}
	}
	h.mu.Unlock()

	offline := &agent.Error{Code: "NODE_AGENT_OFFLINE", Message: "节点连接已断开", Retryable: true}
	for _, r := range reqs {
		r.finish(Result{OK: false, ExitCode: -1, Err: offline})
	}
	for _, s := range sess {
		s.sink.OnExit(-1, offline)
	}
	if current {
		h.store.NodeOffline(c.nodeID)
		log.Printf("[agenthub] 连接关闭 nodeId=%d", c.nodeID)
	}
}

// dispatch 入站帧分派。
func (h *Hub) dispatch(c *conn, f *pb.AgentFrame) {
	reqID := f.GetRequestId()
	switch b := f.GetBody().(type) {
	case *pb.AgentFrame_Ready:
		h.store.NodeOnline(c.nodeID, b.Ready.GetAgentVersion(), factsFromPB(b.Ready.GetFacts()))
	case *pb.AgentFrame_Heartbeat:
		h.store.NodeHeartbeat(c.nodeID, factsFromPB(b.Heartbeat.GetFacts()))
	case *pb.AgentFrame_Log:
		if r := h.lookupRequest(reqID); r != nil && r.onLog != nil {
			r.onLog(b.Log.GetLine())
		}
	case *pb.AgentFrame_Result:
		if r := h.takeRequest(reqID); r != nil {
			r.finish(Result{
				OK:       b.Result.GetOk(),
				ExitCode: int(b.Result.GetExitCode()),
				Err:      errFromPB(b.Result.GetError()),
				Install:  b.Result.GetInstall(),
			})
		}
	case *pb.AgentFrame_SessionData:
		if s := h.lookupSession(reqID); s != nil {
			s.sink.OnData(b.SessionData.GetPayload())
		}
	case *pb.AgentFrame_SessionExit:
		if s := h.takeSession(reqID); s != nil {
			s.sink.OnExit(int(b.SessionExit.GetExitCode()), errFromPB(b.SessionExit.GetError()))
		}
	case *pb.AgentFrame_Pong:
	default:
		log.Printf("[agenthub] nodeId=%d 忽略未知帧 %T", c.nodeID, f.GetBody())
	}
}

// ── 面板侧 API ───────────────────────────────────────────────────────────────

// Online 节点是否有活连接。
func (h *Hub) Online(nodeID uint64) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.conns[nodeID] != nil
}

// Exec 在节点上执行脚本，阻塞到终态；onLog 逐行收输出（可为 nil）。
// ctx 取消 → 向 agent 发 cancel 并返回 ctx.Err()。timeout<=0 用 agent 默认（30 分钟）。
func (h *Hub) Exec(ctx context.Context, nodeID uint64, script string, timeout time.Duration, onLog func(string)) (Result, error) {
	f := &pb.ServerFrame{Body: &pb.ServerFrame_Exec{Exec: &pb.ExecRequest{Script: script, TimeoutMs: timeout.Milliseconds()}}}
	return h.request(ctx, nodeID, f, onLog)
}

// Install 下发内置 K8s 装机，阻塞到终态。装机无外部超时，只能经 ctx 取消。
func (h *Hub) Install(ctx context.Context, nodeID uint64, spec *pb.InstallSpec, onLog func(string)) (Result, error) {
	f := &pb.ServerFrame{Body: &pb.ServerFrame_Install{Install: &pb.InstallRequest{Spec: spec}}}
	return h.request(ctx, nodeID, f, onLog)
}

// request 下发一帧（RequestId 由此处分配）并等待终态。
func (h *Hub) request(ctx context.Context, nodeID uint64, f *pb.ServerFrame, onLog func(string)) (Result, error) {
	c := h.conn(nodeID)
	if c == nil {
		return Result{}, ErrNodeOffline
	}
	r := &request{nodeID: nodeID, conn: c, onLog: onLog, done: make(chan Result, 1)}
	id := newID()
	f.RequestId = id
	// 先登记再下发：agent 可能在下一毫秒就吐出第一行日志，晚登记就会丢开头
	h.mu.Lock()
	h.pending[id] = r
	h.mu.Unlock()

	if !c.send(f) {
		h.takeRequest(id)
		return Result{}, ErrNodeOffline
	}
	select {
	case res := <-r.done:
		return res, nil
	case <-ctx.Done():
		if h.takeRequest(id) != nil {
			c.send(&pb.ServerFrame{RequestId: id, Body: &pb.ServerFrame_Cancel{Cancel: &pb.CancelRequest{}}})
		}
		return Result{}, ctx.Err()
	}
}

// OpenSession 开一条长会话（终端 / logs -f），返回会话 ID。
func (h *Hub) OpenSession(nodeID uint64, script string, tty bool, cols, rows int, sink SessionSink) (string, error) {
	c := h.conn(nodeID)
	if c == nil {
		return "", ErrNodeOffline
	}
	id := newID()
	h.mu.Lock()
	h.sessions[id] = &sessionEntry{nodeID: nodeID, conn: c, sink: sink}
	h.mu.Unlock()
	ok := c.send(&pb.ServerFrame{RequestId: id, Body: &pb.ServerFrame_SessionOpen{SessionOpen: &pb.SessionOpen{
		Script: script, Tty: tty, Cols: int32(cols), Rows: int32(rows),
	}}})
	if !ok {
		h.takeSession(id)
		return "", ErrNodeOffline
	}
	return id, nil
}

// SessionStdin 向会话写入。
func (h *Hub) SessionStdin(sessionID string, data []byte) error {
	s := h.lookupSession(sessionID)
	if s == nil {
		return ErrNoSession
	}
	if len(data) == 0 {
		return nil
	}
	s.conn.send(&pb.ServerFrame{RequestId: sessionID, Body: &pb.ServerFrame_SessionStdin{SessionStdin: &pb.SessionStdin{Payload: data}}})
	return nil
}

// SessionResize 调整伪终端窗口。
func (h *Hub) SessionResize(sessionID string, cols, rows int) error {
	s := h.lookupSession(sessionID)
	if s == nil {
		return ErrNoSession
	}
	s.conn.send(&pb.ServerFrame{RequestId: sessionID, Body: &pb.ServerFrame_SessionResize{SessionResize: &pb.SessionResize{
		Cols: int32(cols), Rows: int32(rows),
	}}})
	return nil
}

// CloseSession 结束会话（幂等）。
func (h *Hub) CloseSession(sessionID string) {
	s := h.takeSession(sessionID)
	if s == nil {
		return
	}
	s.conn.send(&pb.ServerFrame{RequestId: sessionID, Body: &pb.ServerFrame_SessionClose{SessionClose: &pb.SessionClose{}}})
}

// ── 内部 ─────────────────────────────────────────────────────────────────────

type request struct {
	nodeID uint64
	conn   *conn
	onLog  func(string)
	done   chan Result
	once   sync.Once
}

func (r *request) finish(res Result) {
	r.once.Do(func() { r.done <- res })
}

type sessionEntry struct {
	nodeID uint64
	conn   *conn
	sink   SessionSink
}

func (h *Hub) conn(nodeID uint64) *conn {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.conns[nodeID]
}

func (h *Hub) lookupRequest(id string) *request {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.pending[id]
}

func (h *Hub) takeRequest(id string) *request {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.pending[id]
	delete(h.pending, id)
	return r
}

func (h *Hub) lookupSession(id string) *sessionEntry {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.sessions[id]
}

func (h *Hub) takeSession(id string) *sessionEntry {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.sessions[id]
	delete(h.sessions, id)
	return s
}

// conn 一条入站流：出站帧经 sendCh 汇聚到唯一 writer（grpc 流 Send 不允许并发）。
type conn struct {
	nodeID uint64
	stream pb.NodeAgentService_ConnectServer
	sendCh chan *pb.ServerFrame
	done   chan struct{}
	once   sync.Once
}

func newConn(nodeID uint64, stream pb.NodeAgentService_ConnectServer) *conn {
	return &conn{nodeID: nodeID, stream: stream, sendCh: make(chan *pb.ServerFrame, 64), done: make(chan struct{})}
}

func (c *conn) writeLoop() {
	for {
		select {
		case <-c.done:
			return
		case f := <-c.sendCh:
			if err := c.stream.Send(f); err != nil {
				c.close()
				return
			}
		}
	}
}

// send 入队；连接已关返回 false。
func (c *conn) send(f *pb.ServerFrame) bool {
	select {
	case <-c.done:
		return false
	default:
	}
	select {
	case c.sendCh <- f:
		return true
	case <-c.done:
		return false
	case <-time.After(10 * time.Second):
		// 慢消费者：出站队列 10s 都排不进去，当断链处理
		c.close()
		return false
	}
}

func (c *conn) close() {
	c.once.Do(func() { close(c.done) })
}

func first(md metadata.MD, key string) string {
	if v := md.Get(key); len(v) > 0 {
		return v[0]
	}
	return ""
}

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func factsFromPB(p *pb.Facts) agent.Facts {
	if p == nil {
		return agent.Facts{}
	}
	return agent.Facts{
		Hostname:       p.GetHostname(),
		InternalIP:     p.GetInternalIp(),
		OS:             p.GetOs(),
		Arch:           p.GetArch(),
		Kernel:         p.GetKernel(),
		CPUCores:       int(p.GetCpuCores()),
		MemBytes:       p.GetMemBytes(),
		CPUUsedPercent: p.GetCpuUsedPercent(),
		MemUsedPercent: p.GetMemUsedPercent(),
	}
}

func errFromPB(e *pb.Error) *agent.Error {
	if e == nil {
		return nil
	}
	return &agent.Error{Code: e.GetCode(), Message: e.GetMessage(), Retryable: e.GetRetryable()}
}

// String 便于日志。
func (r Result) String() string {
	if r.Err != nil {
		return fmt.Sprintf("ok=%v exit=%d err=%s: %s", r.OK, r.ExitCode, r.Err.Code, r.Err.Message)
	}
	return fmt.Sprintf("ok=%v exit=%d", r.OK, r.ExitCode)
}
