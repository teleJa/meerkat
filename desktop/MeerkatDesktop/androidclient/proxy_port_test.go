package androidclient

import (
	"errors"
	"net"
	"testing"
	"time"
)

// fakeListener 只用来让 listenProxyPort 有东西可返回，不真的监听。
type fakeListener struct{ addr net.Addr }

func (f *fakeListener) Accept() (net.Conn, error) { return nil, errors.New("not implemented") }
func (f *fakeListener) Close() error              { return nil }
func (f *fakeListener) Addr() net.Addr            { return f.addr }

// 首选端口一次就抢到：不该有任何重试，也不该 sleep。
func TestListenProxyPortFirstTry(t *testing.T) {
	var slept int
	var tried []string
	listen := func(network, address string) (net.Listener, error) {
		tried = append(tried, address)
		return &fakeListener{addr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: fixedProxyPort}}, nil
	}
	listener, port, fixed, err := listenProxyPort(listen, proxyPortCandidates(), 3, 200*time.Millisecond, func(time.Duration) { slept++ })
	if err != nil {
		t.Fatal(err)
	}
	if listener == nil {
		t.Fatal("listener 为空")
	}
	if port != fixedProxyPort || !fixed {
		t.Fatalf("port=%d fixed=%v，期望 %d/true", port, fixed, fixedProxyPort)
	}
	if len(tried) != 1 {
		t.Fatalf("应该只尝试一次，实际 %d 次：%v", len(tried), tried)
	}
	if slept != 0 {
		t.Fatalf("成功时不该 sleep，实际 %d 次", slept)
	}
}

// 上一轮进程还没退干净时，必须在同一个端口上重试，而不是立刻换端口——换端口
// 等于换 origin，缓存又不命中，这正是要修的问题。
func TestListenProxyPortRetriesSamePort(t *testing.T) {
	calls := map[string]int{}
	listen := func(network, address string) (net.Listener, error) {
		calls[address]++
		if calls[address] < 2 {
			return nil, errors.New("address already in use")
		}
		return &fakeListener{addr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: fixedProxyPort}}, nil
	}
	var delays []time.Duration
	listener, port, fixed, err := listenProxyPort(listen, proxyPortCandidates(), 3, 150*time.Millisecond, func(d time.Duration) {
		delays = append(delays, d)
	})
	if err != nil {
		t.Fatal(err)
	}
	if listener == nil || port != fixedProxyPort || !fixed {
		t.Fatalf("port=%d fixed=%v", port, fixed)
	}
	address := "127.0.0.1:18923"
	if calls[address] != 2 {
		t.Fatalf("首选端口应被尝试 2 次，实际 %d 次（全部：%v）", calls[address], calls)
	}
	if len(calls) != 1 {
		t.Fatalf("不该换端口（那会改变 origin），实际尝试了 %v", calls)
	}
	if len(delays) != 1 || delays[0] != 150*time.Millisecond {
		t.Fatalf("两次尝试之间应 sleep 一次 150ms，实际 %v", delays)
	}
}

// 首选端口连续 3 次都抢不到，才允许往下一个候选挪。
func TestListenProxyPortFallsBackToNextPort(t *testing.T) {
	var order []string
	listen := func(network, address string) (net.Listener, error) {
		order = append(order, address)
		if address == "127.0.0.1:18923" {
			return nil, errors.New("address already in use")
		}
		return &fakeListener{addr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 18924}}, nil
	}
	_, port, fixed, err := listenProxyPort(listen, proxyPortCandidates(), 3, time.Millisecond, func(time.Duration) {})
	if err != nil {
		t.Fatal(err)
	}
	if !fixed {
		t.Fatal("第二个候选仍是固定端口，fixed 应为 true")
	}
	if port != 18924 {
		t.Fatalf("port=%d 期望 18924", port)
	}
	if len(order) != 4 {
		t.Fatalf("期望 3 次 18923 + 1 次 18924，实际 %v", order)
	}
	for i := 0; i < 3; i++ {
		if order[i] != "127.0.0.1:18923" {
			t.Fatalf("前三次必须是同一个端口，实际 %v", order)
		}
	}
}

// 所有固定端口都被占时退到随机端口，并且必须报告 fixed=false——调用方据此写
// 一条「本次缓存不会命中」的日志，否则真机上根本查不出为什么还是没命中。
func TestListenProxyPortFallsBackToRandom(t *testing.T) {
	listen := func(network, address string) (net.Listener, error) {
		if address == "127.0.0.1:0" {
			return &fakeListener{addr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 43210}}, nil
		}
		return nil, errors.New("address already in use")
	}
	candidates := []int{18923, 18924}
	_, port, fixed, err := listenProxyPort(listen, candidates, 2, time.Millisecond, func(time.Duration) {})
	if err != nil {
		t.Fatal(err)
	}
	if fixed {
		t.Fatal("退出固定端口时 fixed 必须为 false")
	}
	if port != 43210 {
		t.Fatalf("port=%d 期望随机端口的 43210", port)
	}
}

// 连随机端口都起不来才返回错误，并把最后一个固定端口的错误带上。
func TestListenProxyPortAllFail(t *testing.T) {
	listen := func(network, address string) (net.Listener, error) {
		return nil, errors.New("nope " + address)
	}
	_, _, _, err := listenProxyPort(listen, []int{18923}, 1, time.Millisecond, func(time.Duration) {})
	if err == nil {
		t.Fatal("应该返回错误")
	}
}

// 候选端口必须连续、落在临时端口范围之外——否则会被系统给对外连接随机抢走。
func TestProxyPortCandidates(t *testing.T) {
	ports := proxyPortCandidates()
	if len(ports) != proxyPortFallbacks {
		t.Fatalf("候选数 %d，期望 %d", len(ports), proxyPortFallbacks)
	}
	if ports[0] != fixedProxyPort {
		t.Fatalf("首个候选 %d，期望 %d", ports[0], fixedProxyPort)
	}
	for i, port := range ports {
		if port != fixedProxyPort+i {
			t.Fatalf("第 %d 个候选 %d 不连续", i, port)
		}
		// Android 的临时端口范围实测是 37000-50000。
		if port >= 37000 && port <= 50000 {
			t.Fatalf("候选端口 %d 落在临时端口范围内，会被系统抢走", port)
		}
	}
}
