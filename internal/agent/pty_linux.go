//go:build linux

package agent

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"
)

// ptyPair 一对伪终端：master 供本进程读写，slave 交给子进程当控制终端。
type ptyPair struct {
	master *os.File
	slave  *os.File
}

// openPty 申请一对伪终端。
//
// 不引第三方 pty 库：所需系统调用 x/sys/unix 全都有，且该库在离线环境不可得。
// 步骤即 POSIX 那套：开 /dev/ptmx → 解锁 slave → 查 pts 编号 → 打开对应 /dev/pts/N。
func openPty() (*ptyPair, error) {
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		return nil, fmt.Errorf("打开 /dev/ptmx 失败: %w", err)
	}
	// TIOCSPTLCK 置 0 = 解锁 slave 端，不解锁后面打不开
	if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		_ = master.Close()
		return nil, fmt.Errorf("解锁伪终端失败: %w", err)
	}
	n, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		_ = master.Close()
		return nil, fmt.Errorf("获取伪终端编号失败: %w", err)
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		_ = master.Close()
		return nil, fmt.Errorf("打开伪终端从端失败: %w", err)
	}
	return &ptyPair{master: master, slave: slave}, nil
}

// attach 把子进程的三个标准流接到 slave，并让它成为新会话的控制终端。
// 有了控制终端，子进程里的 shell 才会认为自己在交互终端上（有提示符、行编辑、Ctrl-C 生效）。
func (p *ptyPair) attach(cmd *exec.Cmd) {
	cmd.Stdin = p.slave
	cmd.Stdout = p.slave
	cmd.Stderr = p.slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
}

// resize 调整窗口大小；浏览器端终端尺寸变化时调用，子进程会收到 SIGWINCH。
func (p *ptyPair) resize(cols, rows int) error {
	if cols <= 0 || rows <= 0 {
		return nil
	}
	return unix.IoctlSetWinsize(int(p.master.Fd()), unix.TIOCSWINSZ, &unix.Winsize{
		Row: uint16(rows), Col: uint16(cols),
	})
}

// closeSlave 父进程侧关掉 slave：子进程已持有它，父进程留着会导致读 master 时
// 即便子进程退出也拿不到 EOF，会话永远不结束。
func (p *ptyPair) closeSlave() { _ = p.slave.Close() }

func (p *ptyPair) close() {
	_ = p.slave.Close()
	_ = p.master.Close()
}

func (p *ptyPair) file() *os.File { return p.master }

func ptySupported() bool { return true }
