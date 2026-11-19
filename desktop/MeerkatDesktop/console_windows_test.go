//go:build windows

package main

import (
	"os/exec"
	"syscall"
	"testing"
)

func TestHideConsoleSetsNoWindowFlags(t *testing.T) {
	cmd := exec.Command("cmd.exe", "/c", "echo", "hi")
	hideConsole(cmd)
	if cmd.SysProcAttr == nil {
		t.Fatal("Windows 上应设置 SysProcAttr")
	}
	if !cmd.SysProcAttr.HideWindow {
		t.Fatal("HideWindow 应为 true")
	}
	if cmd.SysProcAttr.CreationFlags&createNoWindow == 0 {
		t.Fatalf("CreationFlags 应包含 CREATE_NO_WINDOW，实际 %#x", cmd.SysProcAttr.CreationFlags)
	}
}

func TestHideConsoleMergesExistingAttr(t *testing.T) {
	cmd := exec.Command("cmd.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{}
	hideConsole(cmd)
	if !cmd.SysProcAttr.HideWindow || cmd.SysProcAttr.CreationFlags&createNoWindow == 0 {
		t.Fatalf("已有 SysProcAttr 未被正确合并: %#v", cmd.SysProcAttr)
	}
}
