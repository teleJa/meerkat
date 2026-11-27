package androidclient

import (
	"context"
	"crypto/rand"
	"errors"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

var serviceRange = netip.MustParsePrefix("100.64.0.0/10")
var serviceRange6 = netip.MustParsePrefix("fd7a:115c:a1e0::/48")

func serviceTarget(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.User != nil || strings.Contains(raw, "\\") {
		return nil, errors.New("请输入完整的内网 HTTP 链接")
	}
	ip, err := netip.ParseAddr(u.Hostname())
	if err != nil || !(serviceRange.Contains(ip) || serviceRange6.Contains(ip)) {
		return nil, errors.New("地址必须使用 Tailscale IP")
	}
	port := 80
	if u.Port() != "" {
		port, err = strconv.Atoi(u.Port())
		if err != nil || port < 1 || port > 65535 {
			return nil, errors.New("端口无效")
		}
	}
	u.Host = net.JoinHostPort(ip.String(), strconv.Itoa(port))
	return u, nil
}

// IDs are assigned on the Android UI thread, before dispatch. Remember the latest
// intent even after closing so a delayed open cannot resurrect a dismissed page.
type serviceSessions struct {
	mu       sync.Mutex
	latest   uint64
	activeID uint64
	active   *serviceSession
}

func (v *serviceSessions) open(id uint64, raw string, dial func(context.Context, string, string) (net.Conn, error)) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if id <= v.latest {
		return "", errors.New("服务窗口已过期")
	}
	v.latest = id
	target, err := serviceTarget(raw)
	if err != nil {
		return "", err
	}
	if dial == nil {
		return "", errors.New("内嵌网络尚未启动")
	}
	next, err := startService(target, dial)
	if err != nil {
		return "", err
	}
	if v.active != nil {
		v.active.close()
	}
	v.active, v.activeID = next, id
	return next.entry(target), nil
}

func (v *serviceSessions) close(id uint64) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if id > v.latest {
		v.latest = id
	}
	if v.active != nil && v.activeID == id {
		v.active.close()
		v.active = nil
	}
}

type serviceSession struct {
	*http.Server
	transport *http.Transport
	origin    url.URL
}

func (s *serviceSession) close() { s.Close(); s.transport.CloseIdleConnections() }

// Each opening gets a fresh browser hostname. Neither a reused listener port nor
// a different target can inherit a previous document's cookies or service worker.
func startService(target *url.URL, dial func(context.Context, string, string) (net.Conn, error)) (*serviceSession, error) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	origin := url.URL{Scheme: "http", Host: net.JoinHostPort("service-"+strings.ToLower(rand.Text())+".localhost", strconv.Itoa(ln.Addr().(*net.TCPAddr).Port))}
	tr := &http.Transport{DisableCompression: true, ResponseHeaderTimeout: 30 * time.Second, IdleConnTimeout: time.Minute, DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr != target.Host {
			return nil, errors.New("服务地址已改变")
		}
		return dial(ctx, network, addr)
	}}
	proxy := &httputil.ReverseProxy{Transport: tr, FlushInterval: -1, Rewrite: func(p *httputil.ProxyRequest) {
		p.Out.URL.Scheme = "http"
		p.Out.URL.Host = target.Host
		p.Out.Host = target.Host
		// Only representation, request body and cache/range metadata cross this boundary.
		h := make(http.Header)
		for _, k := range []string{"Accept", "Accept-Encoding", "Accept-Language", "User-Agent", "Range", "If-Range", "If-None-Match", "If-Modified-Since", "Content-Type", "Rsc", "Next-Router-State-Tree", "Next-Router-Prefetch"} {
			if values := p.Out.Header.Values(k); len(values) > 0 {
				h[k] = values
			}
		}
		p.Out.Header = h
	}, ModifyResponse: func(r *http.Response) error {
		if location := r.Header.Get("Location"); location != "" {
			next, err := r.Request.URL.Parse(location)
			if err != nil {
				return err
			}
			canonical, err := serviceTarget(next.String())
			if err != nil || canonical.Host != target.Host {
				return errors.New("服务重定向到了其他地址")
			}
			canonical.Host = origin.Host
			r.Header.Set("Location", canonical.String())
		}
		for _, k := range []string{"Set-Cookie", "Refresh", "Link", "Alt-Svc", "Report-To", "Reporting-Endpoints", "Access-Control-Allow-Origin"} {
			r.Header.Del(k)
		}
		r.Header.Add("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline' 'unsafe-eval'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; media-src 'self' blob:; worker-src 'none'; frame-src 'none'; object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'")
		r.Header.Set("Referrer-Policy", "no-referrer")
		return nil
	}}
	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != origin.Host || r.URL.IsAbs() || r.Method == "CONNECT" || r.Header.Get("Upgrade") != "" {
			http.Error(w, "无效的服务请求", 403)
			return
		}
		proxy.ServeHTTP(w, r)
	})}
	go srv.Serve(ln)
	return &serviceSession{srv, tr, origin}, nil
}

func (s *serviceSession) entry(target *url.URL) string {
	u := *target
	u.Host = s.origin.Host
	return u.String()
}
