//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// createNoWindow 对应 CREATE_NO_WINDOW（0x08000000）。
const createNoWindow = 0x08000000

// hideConsole 让 App 拉起的子进程在 Windows 上不弹出控制台窗口。
// App 本身是 GUI 进程，Edge、herdr、tailscale、安装器等子进程都应隐藏窗口；
// 仍通过 cmd.Stdout/cmd.Stderr 或管道读取输出，不影响日志。
func hideConsole(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= createNoWindow
}
