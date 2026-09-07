//go:build !linux

package agent

import "os/exec"

// setProcGroup 非 Linux 平台的空实现（starport-agent 生产只跑在 Linux 节点上，
// 此处仅为在开发机上可交叉编译/静态检查）。
func setProcGroup(cmd *exec.Cmd) {}
