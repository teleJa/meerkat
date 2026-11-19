//go:build windows

package console

import (
	"os/exec"
	"syscall"
)

// createNoWindow 对应 CREATE_NO_WINDOW（0x08000000）。
const createNoWindow = 0x08000000

// apply 在保留已有 SysProcAttr 的前提下加上隐藏窗口标记。
func apply(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= createNoWindow
}
