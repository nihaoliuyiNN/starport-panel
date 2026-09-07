package installer

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// stream 执行系统命令，把合并后的 stdout/stderr 逐行送 log；返回退出码与启动/等待错误。
// ctx 取消时经 setProcGroup 杀掉整个进程组。
func stream(ctx context.Context, log LogFunc, name string, args ...string) (int, error) {
	emit(log, "$ "+name+" "+strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
	setProcGroup(cmd)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return -1, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return -1, err
	}
	if err := cmd.Start(); err != nil {
		return -1, err
	}

	var mu sync.Mutex
	var wg sync.WaitGroup
	line := func(s string) { mu.Lock(); emit(log, s); mu.Unlock() }
	wg.Add(2)
	go scanLines(stdout, line, &wg)
	go scanLines(stderr, line, &wg)
	wg.Wait()

	if werr := cmd.Wait(); werr != nil {
		if ee, ok := werr.(*exec.ExitError); ok {
			return ee.ExitCode(), werr
		}
		return -1, werr
	}
	return 0, nil
}

// mustRun 流式执行命令；非零退出或启动失败返回 *Fail(ErrStep)。
func mustRun(ctx context.Context, log LogFunc, name string, args ...string) *Fail {
	code, err := stream(ctx, log, name, args...)
	if ctx.Err() != nil {
		return &Fail{Code: ErrCancelled, Message: "装机已取消", Retryable: true}
	}
	if err != nil || code != 0 {
		reason := ""
		if err != nil {
			reason = err.Error()
		}
		return failf(ErrStep, "%s %s 失败(exit=%d) %s", name, strings.Join(args, " "), code, reason)
	}
	return nil
}

// output 捕获命令的合并输出（用于解析 join 命令 / 证书 key 等），不流式。
func output(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}

func scanLines(r io.Reader, onLine func(string), wg *sync.WaitGroup) {
	defer wg.Done()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		onLine(sc.Text())
	}
}

func emit(log LogFunc, s string) {
	if log != nil {
		log(s)
	}
}

// writeFile 落文件（0644，父目录自动建），失败返回 *Fail(ErrStep)。
func writeFile(path, content string, log LogFunc) *Fail {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return failf(ErrStep, "建目录 %s 失败: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return failf(ErrStep, "写文件 %s 失败: %v", path, err)
	}
	emit(log, "写入 "+path)
	return nil
}

// httpDownload 下载 url 到 dest（Go 原生，避免依赖 curl；bundle / apt key 用）。
func httpDownload(ctx context.Context, url, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	cli := &http.Client{Timeout: 10 * time.Minute}
	resp, err := cli.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载 %s 返回 %d", url, resp.StatusCode)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

// ── 系统探测（用于阶段 Skip 幂等判断）───────────────────────────────

func binExists(name string) bool { _, err := exec.LookPath(name); return err == nil }

func fileExists(path string) bool { _, err := os.Stat(path); return err == nil }

func fileContains(path, substr string) bool {
	b, err := os.ReadFile(path)
	return err == nil && strings.Contains(string(b), substr)
}

// swapDisabled true 表示当前无启用中的 swap（/proc/swaps 只剩表头）。
func swapDisabled() bool {
	b, err := os.ReadFile("/proc/swaps")
	if err != nil {
		return true
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	return len(lines) <= 1
}

// sysctlIs 读 /proc/sys 判断内核参数值。
func sysctlIs(key, want string) bool {
	p := "/proc/sys/" + strings.ReplaceAll(key, ".", "/")
	b, err := os.ReadFile(p)
	return err == nil && strings.TrimSpace(string(b)) == want
}

// moduleLoaded 读 /proc/modules 判断内核模块是否已加载。
func moduleLoaded(name string) bool {
	b, err := os.ReadFile("/proc/modules")
	if err != nil {
		return false
	}
	for _, ln := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(ln, name+" ") {
			return true
		}
	}
	return false
}

// serviceActive systemctl is-active 判断服务是否运行。
func serviceActive(name string) bool {
	out, _ := output(context.Background(), "systemctl", "is-active", name)
	return strings.TrimSpace(out) == "active"
}
