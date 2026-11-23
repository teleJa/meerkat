//go:build android

// Package androidclient is the Android-only mobile client: it joins the
// tailnet with an embedded tsnet node and exposes a loopback HTTP proxy to the
// Hub. No Hub or Edge services run on the phone.
package androidclient

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"tailscale.com/net/netmon"
	"tailscale.com/tsnet"
)

// Hostname is the node name this client registers in the tailnet.
const Hostname = "meerkat-android"

// State is the JSON snapshot shown in the client WebView.
type State struct {
	State                string   `json:"state"`
	Backend              string   `json:"backend_state"`
	AuthURL              string   `json:"auth_url"`
	IPs                  []string `json:"tailscale_ips"`
	Error                string   `json:"error"`
	ProxyURL             string   `json:"proxy_url"`
	HubAddr              string   `json:"hub_addr"`
	Configured           bool     `json:"configured"`
	AuthKind             string   `json:"auth_kind,omitempty"`
	AuthState            string   `json:"auth_state,omitempty"`
	CredentialID         string   `json:"credential_id,omitempty"`
	CredentialLabel      string   `json:"credential_label,omitempty"`
	CredentialExpiresAt  string   `json:"credential_expires_at,omitempty"`
	CredentialLastUsedAt string   `json:"credential_last_used_at,omitempty"`
	CredentialDays       *int     `json:"credential_days,omitempty"`
	CredentialScopes     []string `json:"credential_scopes,omitempty"`
	CredentialError      string   `json:"credential_error,omitempty"`
	DeviceCount          int      `json:"device_count,omitempty"`
	HubState             string   `json:"hub_state,omitempty"`
	DSHVersion           string   `json:"dsh_version,omitempty"`
	DSHCommit            string   `json:"dsh_commit,omitempty"`
	LogPath              string   `json:"log_path"`
	LogTail              string   `json:"log_tail"`
}

// Client owns the process-wide tsnet node and the loopback proxy.
type Client struct {
	dataDir        string
	mu             sync.Mutex
	credentialMu   sync.Mutex // serializes persistent pairing, rotation and token changes
	cfgMu          sync.RWMutex
	cfg            Config
	auth           Auth
	authErr        error
	authFailure    string
	store          SecureStore
	server         *tsnet.Server
	dialHub        func(context.Context, string, string) (net.Conn, error)
	proxy          *hubProxy
	state          State
	dshVersion     string
	dshCommit      string
	logPath        string
	logFile        *os.File
	writeClipboard func(string) bool
}

// New creates the client. tsnet identity is persisted under
// <dataDir>/tsnet, so a second launch reuses the same node.
func New(dataDir string, store SecureStore, writeClipboard func(string) bool) (*Client, error) {
	if strings.TrimSpace(dataDir) == "" {
		return nil, fmt.Errorf("app private storage path is empty")
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "tsnet"), 0o700); err != nil {
		return nil, fmt.Errorf("create tsnet state dir: %w", err)
	}
	logPath := filepath.Join(dataDir, "android-tsnet.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open tsnet log: %w", err)
	}
	cfg, cfgErr := LoadConfig(dataDir)
	auth, cfg, authErr := loadOrMigrateAuth(dataDir, cfg, store)
	c := &Client{
		dataDir:        dataDir,
		cfg:            cfg,
		auth:           auth,
		authErr:        authErr,
		store:          store,
		logPath:        logPath,
		logFile:        logFile,
		writeClipboard: writeClipboard,
		state: State{
			State:      "starting",
			HubAddr:    hostFromURL(cfg.HubURL),
			Configured: auth.Configured() && authErr == nil,
			LogPath:    logPath,
			HubState:   "starting",
		},
	}
	if cfgErr != nil {
		_ = c.logf("config load: %v (using defaults)", cfgErr)
	}
	if authErr != nil {
		_ = c.logf("credential: %v", authErr)
	}
	c.refreshAuthStateLocked()
	return c, nil
}

func hostFromURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" {
		return HubAddr
	}
	return parsed.Host
}

// Config returns a snapshot of the persisted client configuration.
func (c *Client) Config() Config {
	c.cfgMu.RLock()
	defer c.cfgMu.RUnlock()
	return c.cfg
}

// SaveToken stores a legacy admin token in Android Keystore-backed storage.
// It remains available for compatibility, but the mobile pairing flow is the
// recommended path.
func (c *Client) SaveToken(token string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return fmt.Errorf("token is empty")
	}
	if len(token) > 4096 {
		return fmt.Errorf("token is too long")
	}
	c.credentialMu.Lock()
	defer c.credentialMu.Unlock()
	previous, _ := c.Auth()
	auth := Auth{
		Kind: AuthKindAdmin, Token: token, Label: "admin token",
		HubURL: c.Config().HubURL, ProxyPort: previous.ProxyPort,
	}
	if err := SaveAuth(c.store, auth); err != nil {
		return fmt.Errorf("凭证不可读：%w", err)
	}
	c.setAuth(auth, nil)
	_ = c.logf("credential: saved admin token len=%d", len(token))
	return nil
}

// ClearToken removes the credential and keeps the active Hub address.
func (c *Client) ClearToken() error {
	c.credentialMu.Lock()
	defer c.credentialMu.Unlock()
	previous, _ := c.Auth()
	auth := Auth{HubURL: c.Config().HubURL, ProxyPort: previous.ProxyPort}
	if err := SaveAuth(c.store, auth); err != nil {
		return err
	}
	c.setAuth(auth, nil)
	_ = c.logf("credential: cleared")
	return nil
}

// Auth returns the in-memory credential snapshot. The token is intentionally
// available inside the process for request injection but never serialized in
// State.
func (c *Client) Auth() (Auth, error) {
	c.cfgMu.RLock()
	defer c.cfgMu.RUnlock()
	return c.auth, c.authErr
}

func (c *Client) setAuth(auth Auth, authErr error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.setAuthLocked(auth, authErr)
}

func (c *Client) setAuthLocked(auth Auth, authErr error) {
	c.cfgMu.Lock()
	c.auth = auth
	c.authErr = authErr
	if auth.HubURL != "" {
		c.cfg.HubURL = auth.HubURL
		c.state.HubAddr = hostFromURL(auth.HubURL)
	}
	c.cfg.AdminToken = ""
	c.cfgMu.Unlock()
	c.authFailure = ""
	c.state.Configured = auth.Configured() && authErr == nil
	c.refreshAuthStateLocked()
}

// refreshAuthStateLocked must be called with c.mu held.
func (c *Client) refreshAuthStateLocked() {
	c.cfgMu.RLock()
	auth := c.auth
	authErr := c.authErr
	c.cfgMu.RUnlock()
	c.state.AuthKind = auth.Kind
	c.state.CredentialID = auth.CredentialID
	c.state.CredentialLabel = auth.Label
	c.state.CredentialExpiresAt = auth.ExpiresAt
	c.state.CredentialLastUsedAt = auth.LastUsedAt
	c.state.CredentialScopes = append([]string(nil), auth.Scopes...)
	c.state.CredentialDays = nil
	c.state.CredentialError = ""
	if authErr != nil {
		c.state.AuthState = "unreadable"
		c.state.CredentialError = "凭证不可读，请重新配对"
		return
	}
	if !auth.Configured() {
		c.state.AuthState = "none"
		return
	}
	c.state.AuthState = auth.AuthState(time.Now())
	if c.state.AuthState == "expired" {
		c.state.CredentialError = "凭证已过期，请重新配对"
	}
	if days, ok := auth.DayCountdown(time.Now()); ok {
		c.state.CredentialDays = &days
	}
	// A Hub-side rejection (expired/invalid) is sticky until the user pairs
	// again; otherwise the next /client/state poll would erase it.
	// 撤销是硬删除：被撤销的设备再从 Hub 拿到的就是 mobile_credential_invalid
	// （服务端已经没有这行了），所以这里只需要处理 expired / invalid。
	if c.authFailure != "" {
		c.state.AuthState = c.authFailure
	}
}

// SetDeviceCount updates the count shown in the foreground notification.
func (c *Client) SetDeviceCount(count int) {
	c.mu.Lock()
	c.state.DeviceCount = count
	c.mu.Unlock()
}

// ProxyURL returns the loopback proxy base URL, if it has started.
func (c *Client) ProxyURL() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state.ProxyURL
}

// Start launches tsnet and the loopback proxy. It returns immediately; state
// is updated by a monitor goroutine.
func (c *Client) Start() {
	logger := log.New(io.MultiWriter(c.logFile, os.Stderr), "android-tsnet ", log.LstdFlags|log.Lmicroseconds)
	// Tailscale's log policy picks a state dir from the runtime; on Android
	// UserCacheDir/Getwd are unavailable, and without TMPDIR it panics while
	// looking for a safe place. Keep both inside the app sandbox.
	logsDir := filepath.Join(c.dataDir, "tailscale-logs")
	tmpDir := filepath.Join(c.dataDir, "tmp")
	_ = os.MkdirAll(logsDir, 0o700)
	_ = os.MkdirAll(tmpDir, 0o700)
	_ = os.Setenv("TS_LOGS_DIR", logsDir)
	_ = os.Setenv("TMPDIR", tmpDir)
	_ = os.Setenv("TMP", tmpDir)
	registerAndroidInterfaces(logger)
	server := &tsnet.Server{
		Dir:      filepath.Join(c.dataDir, "tsnet"),
		Hostname: Hostname,
		Logf:     func(format string, args ...any) { logger.Printf("tsnet: "+format, args...) },
		UserLogf: func(format string, args ...any) { logger.Printf("tsnet-user: "+format, args...) },
	}
	c.mu.Lock()
	c.server = server
	c.dialHub = server.Dial
	c.mu.Unlock()
	if err := server.Start(); err != nil {
		c.setError(fmt.Errorf("tsnet start: %w", err))
		return
	}
	c.startProxy()
	go c.monitor(logger)
}

func (c *Client) monitor(logger *log.Logger) {
	client, err := c.server.LocalClient()
	if err != nil {
		c.setError(fmt.Errorf("tsnet local client: %w", err))
		return
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		status, err := client.Status(context.Background())
		if err != nil {
			c.setError(fmt.Errorf("tsnet status: %w", err))
			continue
		}
		ips := make([]string, 0, len(status.TailscaleIPs))
		for _, ip := range status.TailscaleIPs {
			ips = append(ips, ip.String())
		}
		stateName := "starting"
		switch status.BackendState {
		case "Running":
			stateName = "running"
		case "NeedsLogin", "NeedsMachineAuth":
			stateName = "needs_login"
		}
		logger.Printf("status: backend=%s auth_url=%q", status.BackendState, status.AuthURL)
		c.mu.Lock()
		c.state.State = stateName
		c.state.Backend = status.BackendState
		c.state.AuthURL = status.AuthURL
		c.state.IPs = ips
		if stateName == "running" {
			c.state.Error = ""
			logger.Printf("tsnet running: %s ips=%v", status.Self.HostName, ips)
		} else if status.AuthURL != "" {
			logger.Printf("tsnet needs login: %s", status.AuthURL)
		}
		c.mu.Unlock()
	}
}

type hubProxy struct {
	listener  net.Listener
	server    *http.Server
	transport *http.Transport
	cancel    context.CancelFunc
	host      string
	port      int
}

func (p *hubProxy) close() {
	p.cancel()
	_ = p.server.Close()
	_ = p.listener.Close()
	p.transport.CloseIdleConnections()
}

func (c *Client) startProxy() {
	c.credentialMu.Lock()
	defer c.credentialMu.Unlock()
	auth, _ := c.Auth()
	instance, err := c.prepareProxy(c.Config().HubURL, auth.ProxyPort, auth.ProxyPort > 0)
	if err != nil {
		c.setError(err)
		return
	}
	c.mu.Lock()
	c.activateProxyLocked(instance)
	c.mu.Unlock()
}

func (c *Client) prepareProxy(hubURL string, preferredPort int, named bool) (*hubProxy, error) {
	target, err := url.Parse(hubURL)
	if err != nil || target.Host == "" || (target.Scheme != "http" && target.Scheme != "https") {
		return nil, fmt.Errorf("Hub 地址无效")
	}
	// Keep the preferred port stable across cold starts. A new Hub also
	// gets a distinct browser hostname so its cookies and storage are isolated.
	ports := proxyPortCandidates()
	if preferredPort > 0 && preferredPort <= 65535 {
		ports = append([]int{preferredPort}, ports...)
	}
	listener, port, fixed, err := listenProxyPort(net.Listen, ports, proxyListenAttempts, proxyListenRetryDelay, nil)
	if err != nil {
		return nil, fmt.Errorf("无法启动本机代理：%w", err)
	}
	_ = c.logf("loopback proxy: port=%d fixed=%t", port, fixed)
	host := listener.Addr().String()
	if named {
		sum := sha256.Sum256([]byte(hubURL))
		host = net.JoinHostPort(fmt.Sprintf("hub-%x.localhost", sum[:16]), strconv.Itoa(port))
	}
	ctx, cancel := context.WithCancel(context.Background())
	instance := &hubProxy{listener: listener, cancel: cancel, host: host, port: port}
	clipboard := &clipboardBridge{host: host, write: c.writeClipboard, log: c.Logf}
	proxy := httputil.NewSingleHostReverseProxy(target)
	transport := &http.Transport{
		DialContext:     c.dialHub,
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		// 关掉 Transport 的透明 gzip：否则 Go 会自己加 Accept-Encoding、自己解压，
		// 再把这个头摘掉，注入逻辑就分不清「上游没压缩」和「Go 已经解过了」。
		// 真正的压缩仍然发生——WebView 自己发的 Accept-Encoding 原样透传给 Hub，
		// 压缩后的字节直接回到手机上（见下面的 Director）。
		DisableCompression: true,
	}
	instance.transport = transport
	proxy.Transport = transport
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		// HTML 入口页要注入，必须先拿到 identity 字节，所以只对它摘掉
		// Accept-Encoding。其余请求（插件包、assets、字体，占了初始加载的
		// 12 MB 中的绝大部分）保留 WebView 自己发的 Accept-Encoding，让压缩
		// 发生在最贵的那一段：手机 ↔ Hub。
		//
		// 之前这里是无条件 Del，导致整条链路裸传——实测 12.8 MB 一个字节都
		// 没压缩，loadEvent 11.96 s；同样的资源走 gzip 是 3x 小、2.8x 快。
		injectEntry := isEntryNavigation(req)
		prepareClipboardNavigation(req)
		originalDirector(req)
		if injectEntry {
			req.Header.Del("Accept-Encoding")
		}
		// The Hub protects /harness-proxy and API routes; inject the current
		// mobile/admin credential here so WebView requests and WebSocket
		// upgrades never need to know it.
		req.Header.Del("Authorization")
		c.mu.Lock()
		if c.proxy == instance {
			if auth, err := c.Auth(); err == nil && auth.Configured() {
				req.Header.Set("Authorization", "Bearer "+auth.Token)
			}
		}
		c.mu.Unlock()
	}
	proxy.ModifyResponse = func(resp *http.Response) error {
		if resp == nil || !strings.Contains(resp.Request.URL.Path, "/harness-proxy/") {
			return nil
		}
		// /assets/ 下的文件名带内容哈希（index-ghOpC9uV.js），内容变了名字就变，
		// 可以永久缓存。DSH 自己发的是 Cache-Control: no-cache，于是每次打开
		// 都要重下 1.8 MB。这里改写在反代层，不动 DSH 安装。
		//
		// 只认带哈希的静态产物：路径里有 -<8位以上 base62> 才加 immutable，
		// 免得把会变的东西（比如 favicon）也永久钉住。
		if resp.StatusCode == http.StatusOK && isHashedAssetPath(resp.Request.URL.Path) {
			resp.Header.Set("Cache-Control", "public, max-age=31536000, immutable")
			resp.Header.Del("ETag")
			resp.Header.Del("Last-Modified")
			resp.Header.Del("Expires")
		}
		if !strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/html") {
			return nil
		}
		body, err := readDSHHTML(resp)
		if err != nil {
			c.Logf("dsh css: read html failed path=%s encoding=%q: %v", resp.Request.URL.Path, resp.Header.Get("Content-Encoding"), err)
			return nil
		}
		// 注入的东西是内联 <style> 和内联 <script>：一旦 DSH 哪天加了 CSP，
		// 它们会静默不执行，页面看着完全正常却没有任何效果。响应头里出现 CSP
		// 就在这里留一条，真机上至少能在 logcat 里看到原因，不用再猜。
		if policy := resp.Header.Get("Content-Security-Policy"); policy != "" {
			c.Logf("dsh inject: 响应带 CSP，内联脚本可能被拦 path=%s policy=%q", resp.Request.URL.Path, policy)
		}
		result := InjectDSHMobileHTML(body)
		if result.Injected && resp.StatusCode == http.StatusOK {
			var clipboardInjected bool
			result.Body, clipboardInjected = clipboard.inject(resp.Request, result.Body)
			if clipboardInjected {
				resp.Header.Set("Cache-Control", "no-store")
			}
		}
		if !result.Injected {
			resp.Body = io.NopCloser(bytes.NewReader(body))
			resp.ContentLength = int64(len(body))
			// 同样要还原成 identity：readDSHHTML 可能已经把 gzip 解掉了，
			// 留着 Content-Encoding 会让 WebView 拿明文再解一次。
			resp.Header.Del("Content-Encoding")
			resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
			c.Logf("dsh css: skip injection path=%s reason=%s", resp.Request.URL.Path, result.Reason)
			return nil
		}
		resp.Body = io.NopCloser(bytes.NewReader(result.Body))
		resp.ContentLength = int64(len(result.Body))
		// 注入后字节变了，编码已经还原成 identity（readDSHHTML 解掉了 gzip），
		// 长度头必须一起改，否则 WebView 会按压缩长度截断。
		resp.Header.Del("Content-Encoding")
		resp.Header.Set("Content-Length", strconv.Itoa(len(result.Body)))
		if resp.Header.Get("Cache-Control") != "no-store" {
			resp.Header.Set("Cache-Control", "no-cache")
		}
		resp.Header.Del("ETag")
		resp.Header.Del("Last-Modified")
		c.mu.Lock()
		if c.proxy == instance {
			c.dshVersion = result.Version
			c.dshCommit = result.Commit
			c.state.DSHVersion = result.Version
			c.state.DSHCommit = result.Commit
		}
		c.mu.Unlock()
		c.Logf("dsh css: injected path=%s bytes=%d version=%s commit=%s", resp.Request.URL.Path, len(result.Body), result.Version, result.Commit)
		return nil
	}
	// The Hub is plain HTTP on the tailnet. Force HTTP/1.1 so the reverse
	// proxy keeps DSH WebSocket upgrades working.
	handler := clipboard.proxy(proxy)
	instance.server = &http.Server{
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Host != instance.host {
				http.NotFound(w, r)
				return
			}
			// A retired Hub page must not issue requests to the new Hub via
			// its browser origin. Go callers do not send Fetch Metadata.
			if named {
				origin := r.Header.Get("Origin")
				site := r.Header.Get("Sec-Fetch-Site")
				if (origin != "" && origin != "http://"+instance.host) ||
					(site != "" && site != "same-origin" && !isClipboardNavigation(r)) {
					http.Error(w, "unexpected request origin", http.StatusForbidden)
					return
				}
			}
			handler.ServeHTTP(w, r)
		}),
	}
	return instance, nil
}

func (c *Client) activateProxyLocked(instance *hubProxy) {
	c.proxy = instance
	c.state.ProxyURL = "http://" + instance.host
	go func() {
		if err := instance.server.Serve(instance.listener); err != nil && err != http.ErrServerClosed {
			c.Logf("proxy serve: %v", err)
		}
	}()
}

// Dial the proxy aliases directly for native probes. WebView resolves
// *.localhost to loopback itself.
func dialLoopbackProxy(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || (host != "127.0.0.1" && !strings.HasSuffix(host, ".localhost")) {
		return nil, fmt.Errorf("invalid loopback proxy address")
	}
	return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort("127.0.0.1", port))
}

var loopbackTransport = &http.Transport{DialContext: dialLoopbackProxy}

func loopbackHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Transport: loopbackTransport,
		Timeout:   timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// registerAndroidInterfaces gives tsnet a synthetic interface list. Android
// app UIDs cannot open NETLINK_ROUTE sockets, so the stdlib's net.Interfaces
// (and therefore netmon) fails with "netlinkrib: permission denied". A UDP
// dial performs no I/O but makes the kernel pick the outbound source address.
func registerAndroidInterfaces(logger *log.Logger) {
	netmon.RegisterInterfaceGetter(func() ([]netmon.Interface, error) {
		addrs := make([]net.Addr, 0, 2)
		if ip := outboundIP("udp4", "8.8.8.8:53"); ip != nil {
			addrs = append(addrs, &net.IPNet{IP: ip, Mask: net.CIDRMask(32, 32)})
		}
		if ip := outboundIP("udp6", "[2001:4860:4860::8888]:53"); ip != nil {
			addrs = append(addrs, &net.IPNet{IP: ip, Mask: net.CIDRMask(128, 128)})
		}
		if len(addrs) == 0 {
			return nil, fmt.Errorf("no outbound route found")
		}
		logger.Printf("netmon interface getter: %v", addrs)
		return []netmon.Interface{{
			Interface: &net.Interface{Name: "android", MTU: 1500, Flags: net.FlagUp | net.FlagRunning},
			AltAddrs:  addrs,
		}}, nil
	})
}

func outboundIP(network, address string) net.IP {
	conn, err := net.DialTimeout(network, address, 2*time.Second)
	if err != nil {
		return nil
	}
	defer conn.Close()
	if udp, ok := conn.LocalAddr().(*net.UDPAddr); ok {
		return udp.IP
	}
	return nil
}

func (c *Client) setError(err error) {
	c.mu.Lock()
	c.state.State = "error"
	c.state.Error = err.Error()
	c.mu.Unlock()
	_ = c.logf("error: %v", err)
}

func (c *Client) logf(format string, args ...any) error {
	_, err := fmt.Fprintf(c.logFile, "%s android-tsnet %s\n", time.Now().Format(time.RFC3339), fmt.Sprintf(format, args...))
	return err
}

// Logf appends a line to the client log file. The page can then show it in the
// in-app log panel and the user can retrieve it with `adb shell run-as`.
func (c *Client) Logf(format string, args ...any) {
	if c == nil {
		return
	}
	_ = c.logf(format, args...)
}

// Snapshot returns the current state including the tail of the log.
func (c *Client) Snapshot() State {
	c.mu.Lock()
	c.refreshAuthStateLocked()
	state := c.state
	state.IPs = append([]string(nil), c.state.IPs...)
	state.CredentialScopes = append([]string(nil), c.state.CredentialScopes...)
	logPath := c.logPath
	c.mu.Unlock()
	state.LogTail = tailFile(logPath, 4096)
	if state.State == "needs_login" && state.AuthURL == "" {
		// The backend repeats the auth URL through UserLogf even when
		// LocalClient.Status leaves AuthURL empty. The verbose log is large,
		// so search a wider window than the tail shown in the page. Only do
		// this while login is still pending: after a successful login the old
		// URL stays in the log and would otherwise keep the login prompt on
		// screen next to a Running backend.
		if match := authURLPattern.FindString(tailFile(logPath, 256*1024)); match != "" {
			state.AuthURL = match
		}
	}
	return state
}

var authURLPattern = regexp.MustCompile(`https://login\.tailscale\.com/a/[A-Za-z0-9]+`)

func tailFile(path string, max int64) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Size() == 0 {
		return ""
	}
	offset := info.Size() - max
	if offset < 0 {
		offset = 0
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return ""
	}
	data, _ := io.ReadAll(file)
	return string(data)
}

// Devices asks the Hub for /api/devices through the loopback proxy and returns
// the response status and body (bounded). It uses the persisted admin token.
func (c *Client) Devices() (int, string) {
	status, body, err := c.apiGET("/api/devices")
	if err != nil {
		return 0, err.Error()
	}
	return status, string(body)
}

// Probe checks the Hub over tsnet: an HTTP /api/health request and a WebSocket
// upgrade to /api/edges/ws. The WS handshake is enough to prove long-lived
// connections traverse the tailnet; a real Edge hello needs a device secret.
func (c *Client) Probe() string {
	c.mu.Lock()
	server := c.server
	proxyURL := c.state.ProxyURL
	c.mu.Unlock()
	if server == nil || proxyURL == "" {
		return "tsnet not ready"
	}
	var out strings.Builder
	response, err := loopbackHTTPClient(15 * time.Second).Get(proxyURL + "/api/health")
	if err != nil {
		fmt.Fprintf(&out, "HTTP /api/health: ERROR %v\n", err)
	} else {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 512))
		response.Body.Close()
		fmt.Fprintf(&out, "HTTP /api/health: %d %s\n", response.StatusCode, strings.TrimSpace(string(body)))
	}
	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
		NetDialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return server.Dial(ctx, network, addr)
		},
	}
	hubWS := "ws" + strings.TrimPrefix(c.Config().HubURL, "http") + "/api/edges/ws"
	conn, resp, err := dialer.Dial(hubWS, nil)
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		fmt.Fprintf(&out, "WS /api/edges/ws: ERROR status=%d %v\n", status, err)
		return out.String()
	}
	fmt.Fprintf(&out, "WS /api/edges/ws: 101 Switching Protocols\n")
	_ = conn.Close()
	return out.String()
}

// OpenURLCheck validates the auth URL before handing it to Android.
func OpenURLCheck(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
		return "", fmt.Errorf("invalid auth url")
	}
	return parsed.String(), nil
}

// MarshalState is a helper for tests/debug pages.
func MarshalState(state State) string {
	data, _ := json.Marshal(state)
	return string(data)
}
