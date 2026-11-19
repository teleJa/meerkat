package device

import "testing"

func TestFormatOS(t *testing.T) {
	tests := []struct {
		name     string
		goos     string
		platform string
		version  string
		want     string
	}{
		{name: "darwin", goos: "darwin", platform: "darwin", version: "15.4", want: "macOS 15.4"},
		{name: "darwin no version", goos: "darwin", platform: "darwin", version: "", want: "macOS"},
		{name: "windows", goos: "windows", platform: "Microsoft Windows 11 Pro", version: "10.0.26100", want: "Windows 11 Pro 10.0.26100"},
		{name: "windows no version", goos: "windows", platform: "Microsoft Windows 11 Pro", version: "", want: "Windows 11 Pro"},
		{name: "linux fallback", goos: "linux", platform: "ubuntu", version: "24.04", want: "linux"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FormatOS(tt.goos, tt.platform, tt.version); got != tt.want {
				t.Fatalf("FormatOS(%q, %q, %q) = %q, want %q", tt.goos, tt.platform, tt.version, got, tt.want)
			}
		})
	}
}

func TestBootIDFormat(t *testing.T) {
	id, err := BootID()
	if err != nil {
		t.Skipf("当前环境无法读取启动时间: %v", err)
	}
	if len(id) < len("boot_00000000") || id[:5] != "boot_" {
		t.Fatalf("boot_id 格式错误: %q", id)
	}
	second, err := BootID()
	if err != nil {
		t.Fatalf("第二次 BootID: %v", err)
	}
	if id != second {
		t.Fatalf("同一进程内 boot_id 不稳定: %q != %q", id, second)
	}
}
