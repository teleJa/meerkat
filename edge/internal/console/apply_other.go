//go:build !windows

package console

import "os/exec"

// apply 在非 Windows 平台上无操作：Unix 没有控制台窗口概念。
func apply(*exec.Cmd) {}
