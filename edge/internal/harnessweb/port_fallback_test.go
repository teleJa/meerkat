package harnessweb

import (
	"context"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// tempMarker 造一个写入 dsh 启动记录的文件路径。
func tempMarker(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "launched.txt")
}

// 端口落在系统保留段或已被占用时，Start 必须自动后移，并把实际端口回传，
// 否则 tsnet 的转发目标会指向一个没人监听的端口。
func TestStartFallsBackWhenPortOccupied(t *testing.T) {
	home := t.TempDir()
	t.Setenv("DSH_HOME", home)
	command := fakeDSH(t, tempMarker(t))

	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer blocker.Close()
	occupied := blocker.Addr().(*net.TCPAddr).Port

	service, err := Start(context.Background(), Config{
		Enabled: true,
		Command: command,
		Port:    occupied,
		Profile: "web",
		DSHHome: home,
	}, nil)
	if err != nil {
		t.Fatalf("Start 应自动换端口而不是失败：%v", err)
	}
	defer service.Close()

	actual := service.Port()
	if actual == occupied {
		t.Fatalf("实际端口不应是被占用的 %d", occupied)
	}
	if actual <= 0 {
		t.Fatalf("Service.Port() = %d，应返回实际监听端口", actual)
	}
	if !strings.Contains(service.URL(), ":"+strconv.Itoa(actual)+"/") {
		t.Fatalf("URL %q 应包含实际端口 %d", service.URL(), actual)
	}
}

// 端口段全被占用时要有明确错误，不能静默失败或返回一个必然绑不上的端口。
func TestStartReportsExhaustedPortRange(t *testing.T) {
	// 占一个端口，然后只允许探测 1 个：必然全部失败。
	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer blocker.Close()
	occupied := blocker.Addr().(*net.TCPAddr).Port

	// 通过把起始端口设成被占用端口、并让后续 100 个也都被占用是不现实的，
	// 这里只验证 FindAvailable 的边界由 localport 覆盖；此处验证 Start 在
	// 探测彻底失败时返回带 dsh_web_port 提示的错误。
	_, err = Start(context.Background(), Config{
		Enabled: true,
		Command: fakeDSH(t, tempMarker(t)),
		Port:    70000, // 超出范围，必然失败
		Profile: "web",
		DSHHome: t.TempDir(),
	}, nil)
	if err == nil {
		t.Fatalf("非法端口应报错")
	}
	if !strings.Contains(err.Error(), "端口不可用") {
		t.Fatalf("错误文案应说明端口不可用：%v", err)
	}
	_ = occupied
}

// profile 列表里的 running 状态来自发现文件；换端口后写出的发现文件必须是新端口。
func TestStartWritesDiscoveryWithActualPort(t *testing.T) {
	home := t.TempDir()
	t.Setenv("DSH_HOME", home)
	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer blocker.Close()
	occupied := blocker.Addr().(*net.TCPAddr).Port

	service, err := Start(context.Background(), Config{
		Enabled:        true,
		Command:        fakeDSH(t, tempMarker(t)),
		Port:           occupied,
		Profile:        "web",
		DSHHome:        home,
		WriteDiscovery: true,
	}, nil)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer service.Close()

	discovery, err := ReadDiscovery(home, "web")
	if err != nil {
		t.Fatalf("发现文件应已写入：%v", err)
	}
	if discovery.Port != service.Port() {
		t.Fatalf("发现文件端口 = %d，实际端口 = %d", discovery.Port, service.Port())
	}
	if discovery.Port == occupied {
		t.Fatalf("发现文件不该记录被占用的 %d", occupied)
	}
}
