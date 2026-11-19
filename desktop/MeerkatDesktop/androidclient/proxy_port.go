package androidclient

import (
	"fmt"
	"net"
	"time"
)

// 回环反代的监听端口必须**跨冷启保持不变**。
//
// 之前是 net.Listen("tcp", "127.0.0.1:0")，端口交给系统随机分配，于是每次进程
// 重启 origin 都变（scheme + host + port）。WebView 的磁盘缓存和 Local Storage
// 都按 origin 分桶，换端口等于换了一套全新的空桶：
//
//	http://127.0.0.1:40997/harness-proxy/dev_1/assets/index-BgUyXZcc.js
//	http://127.0.0.1:40031/harness-proxy/dev_1/assets/index-BgUyXZcc.js  ← 两个资源
//
// 真机实测（iQOO 11 Pro，三次 force-stop 冷启三个端口 40997 / 40031 / 43117）：
// 磁盘上因此躺着三套完全相同的条目，同一个 index-BgUyXZcc.js 存了三份（各
// 333,062 B，合计 9.4 MB 冗余），而每次冷启的 fromDiskCache 全是 false——缓存
// 没坏，是新 origin 里没有这个 URL 的条目。Local Storage 里已经有 13 个不同
// 端口的 origin，说明 DSH 的界面设置每次冷启也一起丢了。
//
// 端口固定后，缓存 key 稳定，第二次冷启才能真正命中磁盘缓存。
const (
	// fixedProxyPort 是首选端口。它落在 Linux/Android 的临时端口范围
	// （/proc/sys/net/ipv4/ip_local_port_range = 37000 50000）之外，不会被
	// 系统的对外连接随机抢走。
	fixedProxyPort = 18923
	// proxyPortFallbacks 是固定端口候选个数（18923..18932）。只在首选端口
	// 反复抢不到时才往后挪。
	proxyPortFallbacks = 10
	// proxyListenAttempts 是**同一个端口**上的重试次数：上一个进程可能还没退
	// 干净，立刻跳到下一个端口会让 origin 又变一次，那正是要避免的事。
	proxyListenAttempts = 3
	// proxyListenRetryDelay 是同一端口两次尝试之间的间隔。
	proxyListenRetryDelay = 200 * time.Millisecond
)

// proxyPortCandidates 返回固定端口候选，按优先级排列。
func proxyPortCandidates() []int {
	ports := make([]int, 0, proxyPortFallbacks)
	for i := 0; i < proxyPortFallbacks; i++ {
		ports = append(ports, fixedProxyPort+i)
	}
	return ports
}

// proxyListenFunc 与 net.Listen 同签名，测试里替换成假实现。
type proxyListenFunc func(network, address string) (net.Listener, error)

// listenProxyPort 按候选顺序抢一个监听端口。
//
// 返回值第三项 fixed 表示是否拿到了固定端口：false 意味着退回了系统随机端口，
// 本次 origin 会变、缓存又不会命中，调用方必须把这件事写进日志，否则真机上
// 「怎么还是没命中」根本查不出来。
//
// sleep 可注入（nil 时用 time.Sleep），测试里用假时钟，不真的等待。
func listenProxyPort(
	listen proxyListenFunc,
	candidates []int,
	attempts int,
	delay time.Duration,
	sleep func(time.Duration),
) (net.Listener, int, bool, error) {
	if attempts < 1 {
		attempts = 1
	}
	if sleep == nil {
		sleep = time.Sleep
	}
	var lastErr error
	for _, port := range candidates {
		address := fmt.Sprintf("127.0.0.1:%d", port)
		for attempt := 1; attempt <= attempts; attempt++ {
			listener, err := listen("tcp", address)
			if err == nil {
				return listener, port, true, nil
			}
			lastErr = err
			if attempt < attempts {
				sleep(delay)
			}
		}
	}
	// 固定端口全被占：宁可 origin 不稳定，也不能起不来。
	listener, err := listen("tcp", "127.0.0.1:0")
	if err != nil {
		if lastErr != nil {
			return nil, 0, false, fmt.Errorf("固定端口全部占用（最后一个错误 %v），随机端口也失败: %w", lastErr, err)
		}
		return nil, 0, false, err
	}
	port := 0
	if tcp, ok := listener.Addr().(*net.TCPAddr); ok {
		port = tcp.Port
	}
	return listener, port, false, nil
}
