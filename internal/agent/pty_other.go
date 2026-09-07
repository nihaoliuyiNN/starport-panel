//go:build !linux

package agent

import (
	"errors"
	"os"
	"os/exec"
)

// 非 Linux 平台没有伪终端实现（starport-agent 只部署在 Linux 节点，此处仅为可编译）。
type ptyPair struct{}

func openPty() (*ptyPair, error) {
	return nil, errors.New("当前平台不支持伪终端")
}

func (p *ptyPair) attach(*exec.Cmd)      {}
func (p *ptyPair) resize(int, int) error { return nil }
func (p *ptyPair) closeSlave()           {}
func (p *ptyPair) close()                {}
func (p *ptyPair) file() *os.File        { return nil }

func ptySupported() bool { return false }
