//go:build linux

package installer

import (
	"os/exec"
	"syscall"
)

// setProcGroup 让命令独立成进程组，ctx 取消时杀掉整组，避免 kubeadm/apt 派生的子进程残留。
func setProcGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
