package hub

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"hash/fnv"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

func harnessProxyPort(deviceID string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(deviceID))
	return 8801 + int(h.Sum32()%100)
}

var errHarnessProxyClosed = errors.New("harness proxy closed")

func newHarnessProxy(port int, target string) *harnessProxy {
	entry := &harnessProxy{port: port, target: target}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.MaxIdleConnsPerHost = 16
	dial := transport.DialContext
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if entry.closed.Load() {
			return nil, errHarnessProxyClosed
		}
		conn, err := dial(ctx, network, address)
		if err == nil && entry.closed.Load() {
			_ = conn.Close()
			return nil, errHarnessProxyClosed
		}
		return conn, err
	}
	entry.pathTransport = transport
	return entry
}

func (entry *harnessProxy) RoundTrip(req *http.Request) (*http.Response, error) {
	if entry.closed.Load() {
		return nil, errHarnessProxyClosed
	}
	return entry.pathTransport.RoundTrip(req)
}

func (entry *harnessProxy) close() {
	entry.closed.Store(true)
	if entry.server != nil {
		_ = entry.server.Close()
	}
	if entry.listener != nil {
		_ = entry.listener.Close()
	}
	entry.pathTransport.CloseIdleConnections()
	if entry.upstreamTransport != nil {
		entry.upstreamTransport.CloseIdleConnections()
	}
}

func (h *Hub) ensureHarnessProxy(deviceID string) (string, error) {
	localURL, _, err := h.ensureHarnessProxyEntry(deviceID)
	return localURL, err
}

func (h *Hub) ensureHarnessProxyEntry(deviceID string) (string, *harnessProxy, error) {
	h.harnessMu.Lock()
	defer h.harnessMu.Unlock()
	if h.harnessClosed {
		return "", nil, errHarnessProxyClosed
	}
	rawURL, err := h.getHarnessURL(deviceID)
	if err != nil || strings.TrimSpace(rawURL) == "" {
		return "", nil, fmt.Errorf("未配置 Harness URL")
	}
	target, err := url.Parse(rawURL)
	if err != nil || target.Host == "" {
		return "", nil, fmt.Errorf("Harness URL 无效")
	}
	proxyTarget := *target
	proxyTarget.RawQuery = ""
	proxyTarget.ForceQuery = false
	targetKey := proxyTarget.String()
	if existing := h.harnessHTTP[deviceID]; existing != nil {
		if existing.target == targetKey {
			return harnessLocalURL(target, existing.port), existing, nil
		}
		existing.close()
		delete(h.harnessHTTP, deviceID)
	}
	port := harnessProxyPort(deviceID)
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return "", nil, fmt.Errorf("Harness 代理端口 %d 不可用: %w", port, err)
	}
	proxy := httputil.NewSingleHostReverseProxy(&proxyTarget)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	// DSH 的 WebSocket 与流式 API 走 HTTP/1.1；禁用上游 HTTP/2，避免代理升级失败。
	transport.ForceAttemptHTTP2 = false
	transport.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{}
	}
	transport.TLSClientConfig.NextProtos = []string{"http/1.1"}
	if ts := h.currentTSNet(); ts != nil {
		// Hub 的 DSH 反向代理也走内嵌 tsnet；这样本机系统 Tailscale 退出后仍可访问 tailnet 上的 Edge。
		transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			return ts.Dial(ctx, network, address)
		}
	}
	proxy.Transport = transport
	original := proxy.Director
	proxy.Director = func(req *http.Request) {
		original(req)
		req.Host = target.Host
		req.Header.Set("Host", target.Host)
		if req.Header.Get("Origin") != "" {
			req.Header.Set("Origin", target.Scheme+"://"+target.Host)
		}
	}
	proxy.FlushInterval = -1
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		http.Error(w, "Harness 代理错误: "+err.Error(), http.StatusBadGateway)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.harnessLog.Printf("%s %s upgrade=%q origin=%q", r.Method, r.URL.Path, r.Header.Get("Upgrade"), r.Header.Get("Origin"))
		proxy.ServeHTTP(w, r)
	}), ReadHeaderTimeout: 10 * time.Second}
	entry := newHarnessProxy(port, targetKey)
	entry.listener = listener
	entry.server = server
	entry.upstreamTransport = transport
	h.harnessHTTP[deviceID] = entry
	go func() {
		_ = server.Serve(listener)
	}()
	return harnessLocalURL(target, port), entry, nil
}

func (h *Hub) resetHarnessProxies() {
	h.harnessMu.Lock()
	defer h.harnessMu.Unlock()
	for id, entry := range h.harnessHTTP {
		entry.close()
		delete(h.harnessHTTP, id)
	}
}

func harnessLocalURL(target *url.URL, port int) string {
	query := ""
	if target.RawQuery != "" {
		query = "?" + target.RawQuery
	}
	return fmt.Sprintf("http://127.0.0.1:%d/%s", port, query)
}

type jarTransport struct {
	base http.RoundTripper
	jar  http.CookieJar
}

func newJar() http.CookieJar {
	jar, _ := cookiejar.New(nil)
	return jar
}

func (t *jarTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	cloned := req.Clone(req.Context())
	if cookies := t.jar.Cookies(req.URL); len(cookies) > 0 {
		for _, cookie := range cookies {
			cloned.AddCookie(cookie)
		}
	}
	resp, err := t.base.RoundTrip(cloned)
	if resp != nil {
		t.jar.SetCookies(req.URL, resp.Cookies())
	}
	return resp, err
}

// dropHarnessProxy 丢弃某设备的本地反代与其登录 cookie jar。
// watcher 连续失败时调用：Edge 重启 DSH 后 Harness URL 或会话 cookie 会变，
// 继续复用旧代理只会一直打到失效地址。
func (h *Hub) dropHarnessProxy(deviceID string) {
	h.harnessMu.Lock()
	defer h.harnessMu.Unlock()
	if entry := h.harnessHTTP[deviceID]; entry != nil {
		entry.close()
		delete(h.harnessHTTP, deviceID)
	}
}
