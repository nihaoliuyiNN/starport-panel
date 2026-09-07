//go:build !linux

package installer

import "os/exec"

// setProcGroup 非 Linux 空实现（内置装机只在 Linux 节点运行，此处仅为可交叉编译/静态检查）。
func setProcGroup(cmd *exec.Cmd) {}
