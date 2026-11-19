package main

import (
	"os/exec"
	"runtime"
	"testing"
)

func TestHideConsoleNonWindowsIsNoop(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 上由 console_windows_test.go 覆盖")
	}
	cmd := exec.Command("echo", "hi")
	hideConsole(cmd)
	if cmd.SysProcAttr != nil {
		t.Fatalf("非 Windows 平台不应设置 SysProcAttr: %#v", cmd.SysProcAttr)
	}
	hideConsole(nil) // 不应 panic
}
