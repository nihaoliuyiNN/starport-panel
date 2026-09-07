package agent

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
)

// Executor 在本机执行控制面下发的脚本，逐行回传合并后的 stdout/stderr。
// 等价于旧 SSH 版 clustermgmt.Runner.RunScript，区别只是「就在本机跑」而非 SSH 到远端。
type Executor struct {
	shell   string
	dataDir string
	mu      sync.Mutex // 串行化：同一时刻本机只跑一个脚本，避免 apt/dpkg/kubeadm 抢锁互相踩
}

// NewExecutor 构造执行器；shell 默认 /bin/bash，临时脚本落在 dataDir。
func NewExecutor(shell, dataDir string) *Executor {
	if shell == "" {
		shell = "/bin/bash"
	}
	return &Executor{shell: shell, dataDir: dataDir}
}

// Run 执行脚本：写临时文件后用 shell 运行，合并 stdout/stderr 逐行回调 onLine。
// ctx 取消（超时或主动 cancel）会杀掉整个进程组（Linux）。返回退出码与结构化错误（成功为 0,nil）。
func (e *Executor) Run(ctx context.Context, script string, onLine func(string)) (int, *Error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	// 大脚本走临时文件而非 -c 参数，避开 ARG_MAX，也让 shebang/多行更稳。
	f, err := os.CreateTemp(e.dataDir, "step-*.sh")
	if err != nil {
		return -1, &Error{Code: ErrScriptWriteFailed, Message: err.Error(), Retryable: true}
	}
	path := f.Name()
	defer os.Remove(path)
	if _, err := io.WriteString(f, script); err != nil {
		_ = f.Close()
		return -1, &Error{Code: ErrScriptWriteFailed, Message: err.Error(), Retryable: true}
	}
	_ = f.Close()

	cmd := exec.CommandContext(ctx, e.shell, path)
	setProcGroup(cmd) // 平台相关：Linux 上开新进程组并在取消时杀整组

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return -1, &Error{Code: ErrExecFailed, Message: err.Error(), Retryable: true}
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return -1, &Error{Code: ErrExecFailed, Message: err.Error(), Retryable: true}
	}
	if err := cmd.Start(); err != nil {
		return -1, &Error{Code: ErrExecFailed, Message: err.Error(), Retryable: true}
	}

	// stdout / stderr 两条流并发扫描，onLine 加锁保证整行不交错。
	var wg sync.WaitGroup
	var lineMu sync.Mutex
	emit := func(s string) {
		lineMu.Lock()
		onLine(s)
		lineMu.Unlock()
	}
	wg.Add(2)
	go scanLines(stdout, emit, &wg)
	go scanLines(stderr, emit, &wg)
	wg.Wait()

	waitErr := cmd.Wait()
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return -1, &Error{Code: ErrExecTimeout, Message: "脚本执行超时", Retryable: true}
	case errors.Is(ctx.Err(), context.Canceled):
		return -1, &Error{Code: ErrCancelled, Message: "已取消", Retryable: true}
	case waitErr != nil:
		var ee *exec.ExitError
		if errors.As(waitErr, &ee) {
			// 非零退出：脚本自身失败，重跑同样会失败，不建议自动重试
			return ee.ExitCode(), &Error{Code: ErrExecFailed, Message: waitErr.Error(), Retryable: false}
		}
		return -1, &Error{Code: ErrExecFailed, Message: waitErr.Error(), Retryable: true}
	default:
		return 0, nil
	}
}

// maxLineBytes 单行上限，容忍超长行（如 base64 证书、kubectl -o json 的单行回传）。
const maxLineBytes = 1024 * 1024

// scanLines 逐行读取并回调。
//
// 关键在超长行的处理：bufio.Scanner 撞上 ErrTooLong 会直接停扫，此时若就此 return，
// 管道没人读，子进程写满缓冲后永远阻塞在 write 上，cmd.Wait() 再也不返回——
// 表现是整条 exec 卡到超时，而不是「这一行丢了」。所以出错后必须继续把剩余输出排空，
// 并回一行截断说明，让调用方知道这里少了东西。
func scanLines(r io.Reader, onLine func(string), wg *sync.WaitGroup) {
	defer wg.Done()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
	for sc.Scan() {
		onLine(sc.Text())
	}
	if err := sc.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			onLine("[agent] 输出中有超过 1MB 的单行，已丢弃该行")
		}
		io.Copy(io.Discard, r)
	}
}
