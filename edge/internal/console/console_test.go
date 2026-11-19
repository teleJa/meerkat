package console

import (
	"os/exec"
	"runtime"
	"testing"
)

func TestEnableFromEnvOrFlag(t *testing.T) {
	t.Setenv("MEERKAT_NO_CONSOLE", "")
	if EnableFromEnvOrFlag(false) {
		t.Fatal("默认不应启用隐藏控制台")
	}
	if Enabled() {
		t.Fatal("默认 Enabled 应为 false")
	}
	if !EnableFromEnvOrFlag(true) {
		t.Fatal("显式参数应启用隐藏控制台")
	}
	if !Enabled() {
		t.Fatal("显式参数启用后 Enabled 应为 true")
	}

	Enable(false)
	t.Setenv("MEERKAT_NO_CONSOLE", "1")
	if !EnableFromEnvOrFlag(false) {
		t.Fatal("MEERKAT_NO_CONSOLE=1 应启用隐藏控制台")
	}
	Enable(false)
	t.Setenv("MEERKAT_NO_CONSOLE", "yes")
	if !EnableFromEnvOrFlag(false) {
		t.Fatal("MEERKAT_NO_CONSOLE=yes 应启用隐藏控制台")
	}
	Enable(false)
	t.Setenv("MEERKAT_NO_CONSOLE", "0")
	if EnableFromEnvOrFlag(false) {
		t.Fatal("MEERKAT_NO_CONSOLE=0 不应启用")
	}
}

func TestApplyWhenDisabledKeepsCmd(t *testing.T) {
	Enable(false)
	cmd := exec.Command("echo", "hi")
	Apply(cmd)
	if runtime.GOOS != "windows" && cmd.SysProcAttr != nil {
		t.Fatalf("非 Windows 上 Apply 不应修改 SysProcAttr: %#v", cmd.SysProcAttr)
	}
	Apply(nil) // 不应 panic
}

func TestApplyOnNonWindowsIsNoop(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 上的 SysProcAttr 由 apply_windows_test.go 覆盖")
	}
	Enable(true)
	cmd := exec.Command("echo", "hi")
	Apply(cmd)
	if cmd.SysProcAttr != nil {
		t.Fatalf("非 Windows 平台不应设置 SysProcAttr: %#v", cmd.SysProcAttr)
	}
	Enable(false)
}
