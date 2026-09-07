//go:build linux

package agent

import (
	"os/exec"
	"syscall"
)

// setProcGroup 让脚本进程独立成组，并在 ctx 取消时杀掉整个进程组，
// 避免 kubeadm/apt 等派生的子进程在超时/取消后残留。
func setProcGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// 负 PID = 向整个进程组发信号
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
