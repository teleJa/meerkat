package harnessweb

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeDSH 生成一个最小 dsh 脚本：--from-default-profile 时创建 profile，
// 否则打印 dsh web URL 并保持运行；launch 时把启动次数写到 marker。
func fakeDSH(t *testing.T, marker string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "dsh")
	script := `#!/bin/sh
profile=""
port=""
template=""
prev=""
for arg in "$@"; do
  case "$prev" in
    --profile) profile="$arg" ;;
    --port) port="$arg" ;;
    --from-default-profile) template="$arg" ;;
  esac
  prev="$arg"
done
if [ -n "$template" ]; then
  mkdir -p "$DSH_HOME/profiles/$profile"
  printf '{"dsh":{"profile":{"bundles":[]}}}' > "$DSH_HOME/profiles/$profile/package.json"
  exit 0
fi
echo "$profile" >> "` + marker + `"
printf 'dsh web: http://127.0.0.1:%s/?token=test\n' "$port"
while true; do sleep 1; done
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake dsh: %v", err)
	}
	return path
}

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	return port
}

func newTestManager(t *testing.T, home, stateDir string) (*Manager, string) {
	t.Helper()
	t.Setenv("DSH_HOME", home)
	marker := filepath.Join(t.TempDir(), "launched.txt")
	command := fakeDSH(t, marker)
	manager, err := NewManager(ManagerConfig{
		Enabled:  true,
		Command:  command,
		Port:     freePort(t),
		Home:     home,
		StateDir: stateDir,
	})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	return manager, marker
}

func waitStatus(t *testing.T, manager *Manager, status string) State {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		state := manager.Snapshot()
		if state.Status == status {
			return state
		}
		time.Sleep(20 * time.Millisecond)
	}
	state := manager.Snapshot()
	t.Fatalf("status = %q, want %q (error=%q)", state.Status, status, state.Error)
	return state
}

func TestConnectUsesDiscoveryWithoutSpawning(t *testing.T) {
	home := t.TempDir()
	stateDir := t.TempDir()
	manager, marker := newTestManager(t, home, stateDir)
	defer manager.Close()
	port := freePort(t)
	if err := WriteDiscovery(home, Discovery{
		Profile: "bot", URL: "http://127.0.0.1:" + strconv.Itoa(port) + "/?token=test",
		PID: os.Getpid(), Port: port, StartedAt: "2026-11-13T00:00:00Z",
	}); err != nil {
		t.Fatalf("write discovery: %v", err)
	}
	if err := manager.Apply("connect", "bot", "connect"); err != nil {
		t.Fatalf("connect: %v", err)
	}
	state := waitStatus(t, manager, StatusRunning)
	if state.Owner != OwnerExternal {
		t.Fatalf("owner = %q, want %q", state.Owner, OwnerExternal)
	}
	if state.Profile != "bot" || state.Mode != ModeConnect {
		t.Fatalf("profile/mode = %s/%s", state.Profile, state.Mode)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatalf("connect must not spawn dsh")
	}
}

func TestLaunchSpawnsAndStops(t *testing.T) {
	home := t.TempDir()
	stateDir := t.TempDir()
	manager, marker := newTestManager(t, home, stateDir)
	manager.Start(context.Background()) // 没有保存选择时按旧行为启动 web
	waitStatus(t, manager, StatusRunning)
	manager.Close()

	// 换成 probe：launch 必须真的 spawn，并写发现文件。
	manager, marker = newTestManager(t, home, stateDir)
	if err := manager.Apply("launch", "probe", "launch"); err != nil {
		t.Fatalf("launch: %v", err)
	}
	state := waitStatus(t, manager, StatusRunning)
	if state.Owner != OwnerEdge {
		t.Fatalf("owner = %q, want %q", state.Owner, OwnerEdge)
	}
	data, err := os.ReadFile(marker)
	if err != nil || string(data) == "" {
		t.Fatalf("launch did not spawn dsh: %v (%q)", err, string(data))
	}
	discovery, err := ReadDiscovery(home, "probe")
	if err != nil {
		t.Fatalf("discovery: %v", err)
	}
	if discovery.PID == 0 || discovery.URL == "" {
		t.Fatalf("discovery incomplete: %+v", discovery)
	}
	manager.Close()
	if _, err := os.Stat(DiscoveryPath(home, "probe")); !os.IsNotExist(err) {
		t.Fatalf("discovery should be removed on stop")
	}
}

// 找不到 dsh 时要给出可操作错误，而不是把 exec 的原始错误抛给 UI。
func TestLaunchWithoutDSHReportsActionableError(t *testing.T) {
	home := t.TempDir()
	manager, err := NewManager(ManagerConfig{
		Enabled:  true,
		Command:  filepath.Join(t.TempDir(), "missing", "dsh"),
		Port:     freePort(t),
		Home:     home,
		StateDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	defer manager.Close()
	err = manager.Apply("launch", "web", "launch")
	if err == nil {
		t.Fatalf("launch 应该失败")
	}
	if !errors.Is(err, ErrDSHNotFound) {
		t.Fatalf("err = %v, want ErrDSHNotFound", err)
	}
	state := manager.Snapshot()
	if state.Status != StatusError {
		t.Fatalf("status = %q, want %q", state.Status, StatusError)
	}
	for _, want := range []string{"未找到 dsh", "dsh_web_command"} {
		if !strings.Contains(state.Error, want) {
			t.Fatalf("state.Error 缺少 %q：%q", want, state.Error)
		}
	}
	if state.Installed {
		t.Fatalf("找不到 dsh 时 installed 应为 false")
	}
}

func TestLaunchAdoptsExistingInstanceInsteadOfDuplicating(t *testing.T) {
	home := t.TempDir()
	manager, marker := newTestManager(t, home, t.TempDir())
	defer manager.Close()
	port := freePort(t)
	if err := WriteDiscovery(home, Discovery{Profile: "bot", URL: "http://127.0.0.1:" + strconv.Itoa(port) + "/?token=test", PID: os.Getpid(), Port: port}); err != nil {
		t.Fatalf("write discovery: %v", err)
	}
	if err := manager.Apply("launch", "bot", "launch"); err != nil {
		t.Fatalf("launch: %v", err)
	}
	state := waitStatus(t, manager, StatusRunning)
	if state.Owner != OwnerExternal {
		t.Fatalf("owner = %q, want external", state.Owner)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatalf("launch must not spawn a second instance")
	}
}

func TestConnectFallsBackWhenDiscoveryIsStale(t *testing.T) {
	home := t.TempDir()
	manager, marker := newTestManager(t, home, t.TempDir())
	defer manager.Close()
	port := freePort(t)
	if err := WriteDiscovery(home, Discovery{Profile: "bot", URL: "http://127.0.0.1:" + strconv.Itoa(port) + "/?token=test", PID: 999999, Port: port}); err != nil {
		t.Fatalf("write discovery: %v", err)
	}
	if err := manager.Apply("connect", "bot", "connect"); err == nil {
		t.Fatalf("connect should report the stale instance")
	}
	state := manager.Snapshot()
	if state.Status != StatusStopped || state.Error == "" {
		t.Fatalf("state = %+v, want stopped with error", state)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatalf("stale discovery must not spawn dsh")
	}
}

func TestRefreshMarksExternalStopped(t *testing.T) {
	home := t.TempDir()
	manager, _ := newTestManager(t, home, t.TempDir())
	defer manager.Close()
	port := freePort(t)
	if err := WriteDiscovery(home, Discovery{Profile: "bot", URL: "http://127.0.0.1:" + strconv.Itoa(port) + "/?token=test", PID: os.Getpid(), Port: port}); err != nil {
		t.Fatalf("write discovery: %v", err)
	}
	if err := manager.Apply("connect", "bot", "connect"); err != nil {
		t.Fatalf("connect: %v", err)
	}
	waitStatus(t, manager, StatusRunning)
	if err := RemoveDiscovery(home, "bot"); err != nil {
		t.Fatalf("remove discovery: %v", err)
	}
	manager.Refresh()
	state := manager.Snapshot()
	if state.Status != StatusStopped || state.URL != "" || state.Error == "" {
		t.Fatalf("state = %+v, want stopped fallback", state)
	}
}

func TestCreateProfileListsIt(t *testing.T) {
	home := t.TempDir()
	manager, _ := newTestManager(t, home, t.TempDir())
	defer manager.Close()
	if err := manager.Apply("create", "probe", ""); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ProfilesDir(home), "probe", "package.json")); err != nil {
		t.Fatalf("profile not created: %v", err)
	}
	found := false
	for _, profile := range manager.Snapshot().Profiles {
		if profile.Name == "probe" {
			found = true
		}
	}
	if !found {
		t.Fatalf("new profile missing from state")
	}
}

func TestListProfilesMarksRunning(t *testing.T) {
	home := t.TempDir()
	for _, name := range []string{"web", "bot", "empty"} {
		dir := filepath.Join(ProfilesDir(home), name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if name != "empty" {
			if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte("{}"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	port := freePort(t)
	if err := WriteDiscovery(home, Discovery{Profile: "web", URL: "http://127.0.0.1:" + strconv.Itoa(port) + "/?token=test", PID: os.Getpid(), Port: port}); err != nil {
		t.Fatal(err)
	}
	profiles := ListProfiles(home)
	if len(profiles) != 3 {
		t.Fatalf("profiles = %+v", profiles)
	}
	byName := map[string]Profile{}
	for _, profile := range profiles {
		byName[profile.Name] = profile
	}
	if !byName["web"].Running || !byName["web"].Available {
		t.Fatalf("web = %+v", byName["web"])
	}
	if !byName["bot"].Available || byName["bot"].Running {
		t.Fatalf("bot = %+v", byName["bot"])
	}
	if byName["empty"].Available {
		t.Fatalf("empty should be unavailable")
	}
}
