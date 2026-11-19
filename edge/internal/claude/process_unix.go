//go:build !windows

package claude

import (
	"os/exec"
	"syscall"
)

// sysProcAttr 让 harness 与子进程进入独立进程组，取消时整组 kill。
func sysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

// killTree 杀掉整个进程组；失败时回退到只杀主进程。
func killTree(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		return cmd.Process.Kill()
	}
	return nil
}
