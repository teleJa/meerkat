// Package localport 在本地回环地址上查找可用端口。
//
// 为什么需要它：Windows 上 Hyper-V / WSL / Docker 会动态保留大量高位 TCP 端口段
// （`netsh interface ipv4 show excludedportrange protocol=tcp` 可见），落在保留段里的
// 端口即使没有进程占用也绑不上，报 `listen EACCES`。Meerkat 的本地端口（DSH、
// 本机 Hub API 反代）默认用固定端口，一旦撞上保留段就只能靠用户手改配置，
// 表现为「顺利路径能跑、撞墙静默失败」。这里统一成「首选端口绑不上就往后找」。
package localport

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

const (
	// DefaultScanLimit 是从首选端口开始最多向后探测的端口数量。
	DefaultScanLimit = 100
	// MaxPort 是 TCP 端口上限。
	MaxPort = 65535
	// DefaultHost 探测和监听默认使用的回环地址。
	DefaultHost = "127.0.0.1"
)

// Available 报告 host:port 当前能否被绑定。
//
// 绑定后立刻关闭。对于「交给子进程监听」的端口（例如 dsh web）只能这样探测：
// 探测与子进程真正监听之间存在竞态，但比直接用一个已被占用/被保留的端口失败要好。
func Available(host string, port int) bool {
	host = normalizeHost(host)
	if port < 1 || port > MaxPort {
		return false
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return false
	}
	_ = listener.Close()
	return true
}

// FindAvailable 从 start 开始向后找第一个可绑定的端口，最多探测 limit 个。
//
// 全部不可用时返回明确错误（带区间和数量），不静默回落到一个必然失败的端口。
func FindAvailable(host string, start, limit int) (int, error) {
	if start < 1 || start > MaxPort {
		return 0, fmt.Errorf("端口 %d 超出 1-%d 范围", start, MaxPort)
	}
	if limit <= 0 {
		limit = DefaultScanLimit
	}
	end := start + limit - 1
	if end > MaxPort {
		end = MaxPort
	}
	for port := start; port <= end; port++ {
		if Available(host, port) {
			return port, nil
		}
	}
	return 0, fmt.Errorf("%d-%d 共 %d 个端口全部被占用或被系统保留", start, end, end-start+1)
}

// Listen 从 start 开始向后找端口并返回已经绑定的 listener 与它实际使用的端口。
func Listen(host string, start, limit int) (net.Listener, int, error) {
	host = normalizeHost(host)
	if start < 1 || start > MaxPort {
		return nil, 0, fmt.Errorf("端口 %d 超出 1-%d 范围", start, MaxPort)
	}
	if limit <= 0 {
		limit = DefaultScanLimit
	}
	end := start + limit - 1
	if end > MaxPort {
		end = MaxPort
	}
	var lastErr error
	for port := start; port <= end; port++ {
		listener, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
		if err == nil {
			return listener, port, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("没有可尝试的端口")
	}
	return nil, 0, fmt.Errorf("%d-%d 共 %d 个端口全部被占用或被系统保留（最后一次错误：%v）",
		start, end, end-start+1, lastErr)
}

// ParseAddr 把 "host:port" 拆成 host 与端口；host 为空时用 127.0.0.1。
func ParseAddr(addr string) (string, int, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return DefaultHost, 0, fmt.Errorf("地址为空")
	}
	host, portText, err := net.SplitHostPort(addr)
	if err != nil {
		return "", 0, fmt.Errorf("地址 %q 无效：%w", addr, err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > MaxPort {
		return "", 0, fmt.Errorf("地址 %q 的端口无效", addr)
	}
	return normalizeHost(host), port, nil
}

func normalizeHost(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return DefaultHost
	}
	return host
}
