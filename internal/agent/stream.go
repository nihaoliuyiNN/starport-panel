package agent

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"sync"
	"time"

	pb "starport-panel/internal/pb/agentv1"
)

// 会话层参数。
const (
	// 单次读缓冲：终端输出是流式的，不按行切
	streamReadBuf = 32 * 1024
	// 会话硬上限：防止浏览器异常离开后会话在节点上长期挂着
	streamMaxLifetime = 4 * time.Hour
)

// streamSession 一条长连接会话（网页终端 / kubectl logs -f）。
//
// 与 exec 的本质区别：**不排队、不持全局锁**。exec 的串行化是为了避免 apt/kubeadm 互相抢锁，
// 而会话可能持续几十分钟，一旦混进 exec 队列，一个终端窗口就会把这台机器上的部署、
// 状态查询、监控采样全部堵死。因此每条会话独占一组 goroutine，彼此之间也不互相阻塞。
type streamSession struct {
	requestID string
	cmd       *exec.Cmd
	pty       *ptyPair // tty 模式下非空
	stdin     io.WriteCloser
	out       io.ReadCloser

	cancel context.CancelFunc
	closed chan struct{}
	once   sync.Once
}

// startStreamSession 起一条会话：tty 模式挂伪终端（网页终端），否则用普通管道（logs -f）。
func startStreamSession(s *session, reqID string, m *pb.SessionOpen) {
	sess := &streamSession{requestID: reqID, closed: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), streamMaxLifetime)
	sess.cancel = cancel

	shell := s.exec.shell
	if shell == "" {
		shell = "/bin/bash"
	}
	cmd := exec.CommandContext(ctx, shell, "-c", m.GetScript())
	sess.cmd = cmd

	if m.GetTty() {
		if !ptySupported() {
			cancel()
			s.sendSessionExit(reqID, 1, &Error{Code: ErrExecFailed, Message: "节点不支持伪终端"})
			return
		}
		p, err := openPty()
		if err != nil {
			cancel()
			s.sendSessionExit(reqID, 1, &Error{Code: ErrExecFailed, Message: err.Error()})
			return
		}
		sess.pty = p
		p.attach(cmd)
		_ = p.resize(int(m.GetCols()), int(m.GetRows()))
		sess.out = p.file()
		sess.stdin = p.file()
	} else {
		// 非 tty：合并 stderr 到 stdout，日志流不需要区分两条流
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			cancel()
			s.sendSessionExit(reqID, 1, &Error{Code: ErrExecFailed, Message: err.Error()})
			return
		}
		cmd.Stderr = cmd.Stdout
		in, err := cmd.StdinPipe()
		if err != nil {
			cancel()
			s.sendSessionExit(reqID, 1, &Error{Code: ErrExecFailed, Message: err.Error()})
			return
		}
		setProcGroup(cmd)
		sess.out = stdout
		sess.stdin = in
	}

	if err := cmd.Start(); err != nil {
		if sess.pty != nil {
			sess.pty.close()
		}
		cancel()
		s.sendSessionExit(reqID, 1, &Error{Code: ErrExecFailed, Message: err.Error()})
		return
	}
	// 子进程已接管 slave，父进程必须放手，否则读 master 拿不到 EOF
	if sess.pty != nil {
		sess.pty.closeSlave()
	}

	s.sessions.Store(reqID, sess)
	go sess.pump(s)
}

// pump 把会话输出源源不断发回控制面，进程退出后发 session_exit 并清理登记。
//
// 读到即发，不额外攒帧：出站队列满时 send 会阻塞，等于天然背压——输出快过网络时
// 自动降速，比自己攒缓冲更简单也更不容易出错。
func (ss *streamSession) pump(s *session) {
	defer func() {
		s.sessions.Delete(ss.requestID)
		ss.stop()
	}()

	buf := make([]byte, streamReadBuf)
	for {
		n, err := ss.out.Read(buf)
		if n > 0 {
			// 必须拷贝：buf 会被下一轮 Read 复用，而帧在 sendCh 里可能还没发出去
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			s.sendSessionData(ss.requestID, chunk)
		}
		if err != nil {
			break
		}
	}

	code := 0
	var e *Error
	if waitErr := ss.cmd.Wait(); waitErr != nil {
		var ee *exec.ExitError
		if errors.As(waitErr, &ee) {
			code = ee.ExitCode()
		} else {
			code = -1
			e = &Error{Code: ErrExecFailed, Message: waitErr.Error()}
		}
	}
	s.sendSessionExit(ss.requestID, code, e)
}

// write 往会话标准输入写数据（终端按键）。
func (ss *streamSession) write(b []byte) {
	if ss.stdin == nil || len(b) == 0 {
		return
	}
	_, _ = ss.stdin.Write(b)
}

func (ss *streamSession) resize(cols, rows int) {
	if ss.pty != nil {
		_ = ss.pty.resize(cols, rows)
	}
}

// stop 结束会话：杀进程 + 关闭伪终端/管道，幂等。
func (ss *streamSession) stop() {
	ss.once.Do(func() {
		close(ss.closed)
		ss.cancel()
		if ss.pty != nil {
			ss.pty.close()
		} else {
			if ss.stdin != nil {
				_ = ss.stdin.Close()
			}
			if c, ok := ss.out.(io.Closer); ok {
				_ = c.Close()
			}
		}
		if ss.cmd != nil && ss.cmd.Process != nil {
			_ = ss.cmd.Process.Kill()
		}
	})
}
