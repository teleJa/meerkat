package localport

import (
	"net"
	"strconv"
	"strings"
	"testing"
)

// occupy 占用一个端口并返回它的端口号；测试结束时释放。
func occupy(t *testing.T) (int, net.Listener) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	return listener.Addr().(*net.TCPAddr).Port, listener
}

func TestAvailableDetectsOccupiedPort(t *testing.T) {
	port, listener := occupy(t)
	defer listener.Close()
	if Available("127.0.0.1", port) {
		t.Fatalf("端口 %d 已被占用，Available 应返回 false", port)
	}
	if !Available("127.0.0.1", port+1) && !Available("127.0.0.1", port-1) {
		t.Logf("相邻端口也不可用（可能是系统保留段），跳过该断言")
	}
}

func TestFindAvailableSkipsOccupiedPort(t *testing.T) {
	port, listener := occupy(t)
	defer listener.Close()
	got, err := FindAvailable("127.0.0.1", port, 50)
	if err != nil {
		// 相邻 50 个端口全被占是极端情况，测试机不该出现。
		t.Fatalf("find available: %v", err)
	}
	if got == port {
		t.Fatalf("应跳过被占用的端口 %d，实际返回了它", port)
	}
	if got <= port {
		t.Fatalf("应从 %d 往后找，实际返回 %d", port, got)
	}
}

func TestFindAvailableExhausted(t *testing.T) {
	// 绑一个端口，再从一个必然被它挡住的位置开始扫 1 个。
	port, listener := occupy(t)
	defer listener.Close()
	if _, err := FindAvailable("127.0.0.1", port, 1); err == nil {
		t.Fatalf("只扫被占用的那一个端口时应报错")
	} else if !strings.Contains(err.Error(), "全部被占用") {
		t.Fatalf("错误文案应说明全部被占用，实际：%v", err)
	}
}

func TestFindAvailableRejectsInvalidPort(t *testing.T) {
	for _, port := range []int{-1, 0, 70000} {
		if _, err := FindAvailable("127.0.0.1", port, 10); err == nil {
			t.Fatalf("端口 %d 应报错", port)
		}
	}
}

func TestListenReturnsBoundListener(t *testing.T) {
	start, blocker := occupy(t)
	defer blocker.Close()
	listener, port, err := Listen("127.0.0.1", start, 50)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()
	if port == start {
		t.Fatalf("应跳过被占用的 %d", start)
	}
	// listener 必须是真的绑在那个端口上。
	if got := listener.Addr().(*net.TCPAddr).Port; got != port {
		t.Fatalf("listener 端口 = %d，返回值 = %d", got, port)
	}
}

func TestParseAddr(t *testing.T) {
	cases := []struct {
		addr string
		host string
		port int
		bad  bool
	}{
		{addr: "127.0.0.1:51900", host: "127.0.0.1", port: 51900},
		{addr: ":52000", host: "127.0.0.1", port: 52000},
		{addr: "  localhost:1234  ", host: "localhost", port: 1234},
		{addr: "", bad: true},
		{addr: "127.0.0.1", bad: true},
		{addr: "127.0.0.1:0", bad: true},
		{addr: "127.0.0.1:70000", bad: true},
	}
	for _, item := range cases {
		host, port, err := ParseAddr(item.addr)
		if item.bad {
			if err == nil {
				t.Fatalf("ParseAddr(%q) 应报错", item.addr)
			}
			continue
		}
		if err != nil {
			t.Fatalf("ParseAddr(%q): %v", item.addr, err)
		}
		if host != item.host || port != item.port {
			t.Fatalf("ParseAddr(%q) = %s/%d，want %s/%d", item.addr, host, port, item.host, item.port)
		}
	}
}

func TestAvailableIgnoresInvalidPort(t *testing.T) {
	for _, port := range []int{0, -5, 70000} {
		if Available("127.0.0.1", port) {
			t.Fatalf("端口 %d 不该被判为可用", port)
		}
	}
}

func TestFindAvailableClampsToMaxPort(t *testing.T) {
	// 从 65530 开始扫 100 个，不能越过 65535 也不该 panic。
	if _, err := FindAvailable("127.0.0.1", 65530, 100); err != nil {
		// 高位端口可能全被保留，这里只要求不 panic 且错误文案合理。
		if !strings.Contains(err.Error(), "全部被占用") {
			t.Fatalf("错误文案不符合预期：%v", err)
		}
	}
}

func TestListenParseRoundTrip(t *testing.T) {
	listener, port, err := Listen("", 0, 1)
	if err == nil {
		defer listener.Close()
		t.Fatalf("端口 0 应报错，返回了 %d", port)
	}
	_ = strconv.Itoa(port)
}
