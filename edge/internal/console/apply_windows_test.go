//go:build windows

package console

import (
	"os/exec"
	"syscall"
	"testing"
)

func TestApplySetsNoWindowFlagsOnWindows(t *testing.T) {
	Enable(true)
	defer Enable(false)

	cmd := exec.Command("cmd.exe", "/c", "echo", "hi")
	Apply(cmd)
	if cmd.SysProcAttr == nil {
		t.Fatal("Windows 上 Apply 应设置 SysProcAttr")
	}
	if !cmd.SysProcAttr.HideWindow {
		t.Fatal("HideWindow 应为 true")
	}
	if cmd.SysProcAttr.CreationFlags&createNoWindow == 0 {
		t.Fatalf("CreationFlags 应包含 CREATE_NO_WINDOW，实际 %#x", cmd.SysProcAttr.CreationFlags)
	}
}

func TestApplyKeepsExistingSysProcAttrOnWindows(t *testing.T) {
	Enable(true)
	defer Enable(false)

	cmd := exec.Command("cmd.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: false} // 已有属性应被合并而不是丢弃
	Apply(cmd)
	if !cmd.SysProcAttr.HideWindow || cmd.SysProcAttr.CreationFlags&createNoWindow == 0 {
		t.Fatalf("已有 SysProcAttr 未被正确合并: %#v", cmd.SysProcAttr)
	}
}
