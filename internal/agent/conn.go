package agent

import (
	"context"
	"crypto/tls"
	"errors"
	"log"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"starport-panel/internal/installer"
	pb "starport-panel/internal/pb/agentv1"
)

// 会话层参数。
const (
	dialTimeout     = 15 * time.Second // 建连（含 TLS 握手）上限
	keepaliveTime   = 20 * time.Second // HTTP/2 PING 间隔（须 ≥ 服务端 permitKeepAliveTime）
	keepaliveTO     = 10 * time.Second // PING 应答超时，超时判定断链
	execQueueDepth  = 32               // 待执行脚本队列深度
	idempotentTTL   = 10 * time.Minute // requestId 去重窗口
	maxMessageBytes = 4 << 20          // 单条入站帧上限（脚本可能较大）
)

// session 一次 gRPC 呼出长连的生命周期。
type session struct {
	stream   pb.NodeAgentService_ConnectClient
	exec     *Executor
	idem     *idempotentStore
	id       identity
	agentVer string
	dataDir  string // 内置装机的临时/缓存目录（installer.Options）

	sendCh   chan *pb.AgentFrame  // 所有出站帧汇聚到唯一 writer：grpc 流 Send 不允许并发
	execCh   chan *pb.ServerFrame // 串行执行队列（exec / install）
	cancels  sync.Map             // requestId -> context.CancelFunc
	sessions sync.Map             // requestId -> *streamSession（长连接会话，不走 execCh）
	done     chan struct{}        // 关闭信号
	once     sync.Once            // done 只关一次
	closeFn  func()               // 取消流的 ctx，立刻解阻塞 Recv
}

// runSession 建立并运行一次会话，阻塞到连接结束；返回结束原因（errUnauthorized 触发重注册）。
// attempt 用于在多个 gRPC 入口间轮转。
func runSession(ctx context.Context, cfg Config, id identity, exectr *Executor, idem *idempotentStore, attempt int) error {
	endpoints := grpcEndpoints(cfg, id)
	target := endpoints[attempt%len(endpoints)]

	creds := insecure.NewCredentials()
	if grpcTLS(cfg, id) {
		creds = credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12})
	}
	conn, err := grpc.NewClient(target,
		grpc.WithTransportCredentials(creds),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                keepaliveTime,
			Timeout:             keepaliveTO,
			PermitWithoutStream: true,
		}),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(maxMessageBytes)),
	)
	if err != nil {
		return err
	}
	defer conn.Close()

	// 流的 ctx 独立于外部 ctx：close() 取消它即可立刻解阻塞 Recv/Send，
	// 不必等 keepalive 超时；外部 ctx 取消也会级联进来。
	streamCtx, cancelStream := context.WithCancel(ctx)
	defer cancelStream()

	md := metadata.Pairs(
		MetaAgentToken, id.AgentToken,
		MetaNodeID, strconv.FormatUint(id.NodeID, 10),
		MetaAgentVersion, cfg.AgentVersion,
	)
	client := pb.NewNodeAgentServiceClient(conn)
	stream, err := client.Connect(metadata.NewOutgoingContext(streamCtx, md), grpc.WaitForReady(false))
	if err != nil {
		return mapAuthErr(err)
	}
	log.Printf("[agent] 已连接控制面 gRPC %s", target)

	s := &session{
		stream:   stream,
		exec:     exectr,
		idem:     idem,
		id:       id,
		agentVer: cfg.AgentVersion,
		dataDir:  cfg.DataDir,
		sendCh:   make(chan *pb.AgentFrame, 64),
		execCh:   make(chan *pb.ServerFrame, execQueueDepth),
		done:     make(chan struct{}),
		closeFn:  cancelStream,
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); s.writeLoop(cfg) }()
	go func() { defer wg.Done(); s.execLoop() }()

	// ready 首帧：告诉控制面本节点已上线及当前现状
	s.send(&pb.AgentFrame{Body: &pb.AgentFrame_Ready{Ready: &pb.Ready{
		NodeId: id.NodeID, AgentVersion: cfg.AgentVersion, Facts: collectFacts().toPB(),
	}}})

	// readLoop 在当前 goroutine 阻塞运行，返回即连接结束
	readErr := s.readLoop()
	s.close()
	wg.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return mapAuthErr(readErr)
}

// readLoop 读取并分派控制面下发的帧；返回即代表连接结束。
func (s *session) readLoop() error {
	for {
		f, err := s.stream.Recv()
		if err != nil {
			return err
		}
		reqID := f.GetRequestId()
		switch b := f.GetBody().(type) {
		case *pb.ServerFrame_Exec, *pb.ServerFrame_Install:
			select {
			case s.execCh <- f:
			case <-s.done:
				return nil
			}
		case *pb.ServerFrame_Cancel:
			if c, ok := s.cancels.Load(reqID); ok {
				c.(context.CancelFunc)()
			}
		// 会话帧一律就地处理：绝不入 execCh——那是串行队列且持全局锁，
		// 长会话进去会把这台机器上的 exec 全堵死。
		case *pb.ServerFrame_SessionOpen:
			startStreamSession(s, reqID, b.SessionOpen)
		case *pb.ServerFrame_SessionStdin:
			if ss, ok := s.lookupSession(reqID); ok {
				ss.write(b.SessionStdin.GetPayload())
			}
		case *pb.ServerFrame_SessionResize:
			if ss, ok := s.lookupSession(reqID); ok {
				ss.resize(int(b.SessionResize.GetCols()), int(b.SessionResize.GetRows()))
			}
		case *pb.ServerFrame_SessionClose:
			if ss, ok := s.lookupSession(reqID); ok {
				ss.stop()
			}
		case *pb.ServerFrame_Ping:
			s.send(&pb.AgentFrame{Body: &pb.AgentFrame_Pong{Pong: &pb.Pong{}}})
		default:
			log.Printf("[agent] 忽略未知帧: %T", f.GetBody())
		}
	}
}

// writeLoop 唯一写者：串行发送出站帧 + 周期心跳。传输层保活由 gRPC keepalive 负责。
func (s *session) writeLoop(cfg Config) {
	hb := time.NewTicker(cfg.heartbeatInterval())
	defer hb.Stop()

	for {
		select {
		case <-s.done:
			return
		case f := <-s.sendCh:
			if err := s.stream.Send(f); err != nil {
				s.close()
				return
			}
		case <-hb.C:
			s.send(&pb.AgentFrame{Body: &pb.AgentFrame_Heartbeat{Heartbeat: &pb.Heartbeat{
				NodeId: s.id.NodeID, Facts: collectFacts().toPB(),
			}}})
		}
	}
}

// execLoop 串行消费执行队列：一次一个，边跑边把日志回传，跑完发 result。
func (s *session) execLoop() {
	for {
		select {
		case <-s.done:
			return
		case f := <-s.execCh:
			switch b := f.GetBody().(type) {
			case *pb.ServerFrame_Install:
				s.handleInstall(f.GetRequestId(), b.Install)
			case *pb.ServerFrame_Exec:
				s.handleExec(f.GetRequestId(), b.Exec)
			}
		}
	}
}

// handleExec 执行单个 exec：先查幂等缓存，再带超时/可取消地跑脚本。
func (s *session) handleExec(reqID string, req *pb.ExecRequest) {
	if r, ok := s.idem.get(reqID); ok {
		s.sendResult(reqID, r.ok, r.exitCode, r.err, nil)
		return
	}

	timeout := time.Duration(req.GetTimeoutMs()) * time.Millisecond
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	s.cancels.Store(reqID, cancel)
	defer func() {
		cancel()
		s.cancels.Delete(reqID)
	}()

	code, execErr := s.exec.Run(ctx, req.GetScript(), func(line string) { s.sendLog(reqID, line) })
	ok := execErr == nil && code == 0
	s.idem.put(reqID, cachedResult{ok: ok, exitCode: code, err: execErr})
	s.sendResult(reqID, ok, code, execErr, nil)
}

// handleInstall 执行一次内置 K8s 装机：幂等去重 → installer.Run 流式回传日志 →
// 终态随 result 帧携带结构化 InstallResult。装机可能长达数十分钟，故不设外部超时，
// 只支持控制面 cancel。
func (s *session) handleInstall(reqID string, req *pb.InstallRequest) {
	if r, ok := s.idem.get(reqID); ok {
		s.sendResult(reqID, r.ok, r.exitCode, r.err, r.install)
		return
	}
	spec := installSpecFromPB(req.GetSpec())
	if spec == nil {
		s.sendResult(reqID, false, 1, &Error{Code: installer.ErrStep, Message: "缺少 install 指令"}, nil)
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	s.cancels.Store(reqID, cancel)
	defer func() {
		cancel()
		s.cancels.Delete(reqID)
	}()

	res, f := installer.Run(ctx, spec, installer.Options{DataDir: s.dataDir}, func(line string) { s.sendLog(reqID, line) })

	ok := f == nil
	code := 0
	var e *Error
	var out *pb.InstallResult
	if f != nil {
		code = 1
		e = failToError(f)
	} else {
		out = installResultToPB(res)
	}
	s.idem.put(reqID, cachedResult{ok: ok, exitCode: code, err: e, install: out})
	s.sendResult(reqID, ok, code, e, out)
}

// lookupSession 取一条在册的长连接会话。
func (s *session) lookupSession(reqID string) (*streamSession, bool) {
	v, ok := s.sessions.Load(reqID)
	if !ok {
		return nil, false
	}
	ss, ok := v.(*streamSession)
	return ss, ok
}

func (s *session) sendLog(reqID, line string) {
	s.send(&pb.AgentFrame{RequestId: reqID, Body: &pb.AgentFrame_Log{Log: &pb.LogLine{Line: line}}})
}

func (s *session) sendResult(reqID string, ok bool, code int, e *Error, install *pb.InstallResult) {
	s.send(&pb.AgentFrame{RequestId: reqID, Body: &pb.AgentFrame_Result{Result: &pb.ExecResult{
		Ok: ok, ExitCode: int32(code), Error: e.toPB(), Install: install,
	}}})
}

func (s *session) sendSessionData(reqID string, data []byte) {
	s.send(&pb.AgentFrame{RequestId: reqID, Body: &pb.AgentFrame_SessionData{SessionData: &pb.SessionData{Payload: data}}})
}

func (s *session) sendSessionExit(reqID string, code int, e *Error) {
	s.send(&pb.AgentFrame{RequestId: reqID, Body: &pb.AgentFrame_SessionExit{SessionExit: &pb.SessionExit{
		ExitCode: int32(code), Error: e.toPB(),
	}}})
}

// send 入队；连接已关时丢弃（避免执行 goroutine 卡死）。队列满时阻塞等于天然背压。
func (s *session) send(f *pb.AgentFrame) {
	select {
	case s.sendCh <- f:
	case <-s.done:
	}
}

func (s *session) close() {
	s.once.Do(func() {
		// 断链时把还挂着的会话一并收掉，否则终端进程会在节点上残留
		s.sessions.Range(func(_, v any) bool {
			if ss, ok := v.(*streamSession); ok {
				ss.stop()
			}
			return true
		})
		close(s.done)
		// 取消流 ctx 以立刻解阻塞 readLoop 的 Recv（否则要等到 keepalive 超时才返回，
		// 导致 SIGTERM 优雅退出/重连被拖长）。
		s.closeFn()
	})
}

// mapAuthErr 把 gRPC 的 UNAUTHENTICATED / PERMISSION_DENIED 映射为 errUnauthorized（触发重注册）。
func mapAuthErr(err error) error {
	if err == nil {
		return nil
	}
	switch status.Code(err) {
	case codes.Unauthenticated, codes.PermissionDenied:
		return errUnauthorized
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	return err
}

// grpcEndpoints 决定本次连接可用的 gRPC 入口：CLI 显式指定 > 注册应答下发 > server 主机 + 默认端口。
func grpcEndpoints(cfg Config, id identity) []string {
	if cfg.GrpcEndpoints != "" {
		return splitEndpoints(cfg.GrpcEndpoints)
	}
	if len(id.GrpcEndpoints) > 0 {
		return id.GrpcEndpoints
	}
	host := cfg.ServerURL
	if u, err := url.Parse(cfg.ServerURL); err == nil && u.Host != "" {
		host = u.Hostname()
	}
	return []string{net.JoinHostPort(host, DefaultGrpcPort)}
}

// grpcTLS：CLI 显式开启 > 注册应答。
func grpcTLS(cfg Config, id identity) bool {
	return cfg.GrpcTLS || id.GrpcTLS
}

func splitEndpoints(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
