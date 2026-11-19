package identity

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"meerkat/edge/internal/protocol"
)

func testIdentity(secret string) *Identity {
	return &Identity{
		CenterURL:       "http://100.64.0.1:8787",
		DeviceID:        "dev_a1b2c3d4",
		DeviceSecret:    secret,
		ProtocolVersion: "1",
		EdgeVersion:     "0.1.0",
		Device: protocol.Device{
			Name:         "MacBook Pro",
			Hostname:     "MacBookPro",
			OS:           "macOS 15.4",
			Arch:         "arm64",
			Role:         "Edge",
			Capabilities: []string{"metrics"},
			BootID:       "boot_1e2d3c4b",
		},
		CreatedAt:           "2026-11-06T05:40:00.000Z",
		HeartbeatIntervalMS: 10000,
		MetricsIntervalMS:   5000,
	}
}

func TestDefaultDirFor(t *testing.T) {
	darwinDir, err := DefaultDirFor("darwin", "/Users/example", "")
	if err != nil {
		t.Fatalf("darwin: %v", err)
	}
	if want := "/Users/example/Library/Application Support/Meerkat/edge"; darwinDir != want {
		t.Fatalf("darwin dir = %q, want %q", darwinDir, want)
	}

	winDir, err := DefaultDirFor("windows", "C:\\Users\\example", "C:\\Users\\example\\AppData\\Local")
	if err != nil {
		t.Fatalf("windows: %v", err)
	}
	if want := filepath.Join("C:\\Users\\example\\AppData\\Local", "Meerkat", "edge"); winDir != want {
		t.Fatalf("windows dir = %q, want %q", winDir, want)
	}

	linuxDir, err := DefaultDirFor("linux", "/home/example", "")
	if err != nil {
		t.Fatalf("linux: %v", err)
	}
	if want := "/home/example/.local/state/meerkat/edge"; linuxDir != want {
		t.Fatalf("linux dir = %q, want %q", linuxDir, want)
	}

	if _, err := DefaultDirFor("darwin", "", ""); err == nil {
		t.Fatal("缺少 home 时应报错")
	}
	if _, err := DefaultDirFor("windows", "", ""); err == nil {
		t.Fatal("缺少 LOCALAPPDATA 时应报错")
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state", "edge")
	id := testIdentity("deadbeefdeadbeef")

	if err := Save(dir, id); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if runtime.GOOS != "windows" {
		dirInfo, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("stat dir: %v", err)
		}
		if perm := dirInfo.Mode().Perm(); perm != 0o700 {
			t.Fatalf("目录权限 = %o, want 700", perm)
		}
		fileInfo, err := os.Stat(Path(dir))
		if err != nil {
			t.Fatalf("stat identity: %v", err)
		}
		if perm := fileInfo.Mode().Perm(); perm != 0o600 {
			t.Fatalf("文件权限 = %o, want 600", perm)
		}
	}

	loaded, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.DeviceID != id.DeviceID || loaded.DeviceSecret != id.DeviceSecret {
		t.Fatalf("roundtrip 丢失关键字段: %+v", loaded)
	}
	if loaded.Device.BootID != "boot_1e2d3c4b" || loaded.HeartbeatIntervalMS != 10000 {
		t.Fatalf("roundtrip 丢失设备/间隔字段: %+v", loaded)
	}
}

func TestSaveOverwritesExisting(t *testing.T) {
	dir := t.TempDir()
	if err := Save(dir, testIdentity("first")); err != nil {
		t.Fatalf("Save first: %v", err)
	}
	second := testIdentity("second")
	second.DeviceID = "dev_second"
	if err := Save(dir, second); err != nil {
		t.Fatalf("Save second: %v", err)
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.DeviceID != "dev_second" || loaded.DeviceSecret != "second" {
		t.Fatalf("覆盖失败: %+v", loaded)
	}
}

func TestLoadMissing(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "nope"))
	if err == nil {
		t.Fatal("缺少身份文件应报错")
	}
	if !strings.Contains(err.Error(), "未找到身份文件") || !strings.Contains(err.Error(), "加入工作区") {
		t.Fatalf("错误信息不可读: %v", err)
	}
}

func TestLoadInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(Path(dir), []byte("{oops"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(dir)
	if err == nil || !strings.Contains(err.Error(), "不是合法 JSON") {
		t.Fatalf("错误信息不可读: %v", err)
	}
}

func TestDescribeRedactsSecret(t *testing.T) {
	id := testIdentity("supersecretvalue")
	desc := id.Describe()
	if strings.Contains(desc, "supersecretvalue") {
		t.Fatalf("Describe 泄露 secret: %s", desc)
	}
	if !strings.Contains(desc, "dev_a1b2c3d4") || !strings.Contains(desc, "http://100.64.0.1:8787") {
		t.Fatalf("Describe 缺少关键信息: %s", desc)
	}
}
