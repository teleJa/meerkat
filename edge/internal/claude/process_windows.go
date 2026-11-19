//go:build windows

package claude

import (
	"os/exec"
	"strconv"
	"syscall"

	"meerkat/edge/internal/console"
)

// sysProcAttr 在 Windows 上不使用 Unix 进程组。
func sysProcAttr() *syscall.SysProcAttr {
	return nil
}

// killTree 用 taskkill 结束进程树，失败时回退到只杀主进程。
func killTree(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	killCmd := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
	console.Apply(killCmd)
	if err := killCmd.Run(); err == nil {
		return nil
	}
	return cmd.Process.Kill()
}
