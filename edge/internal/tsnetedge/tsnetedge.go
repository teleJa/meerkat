// Package tsnetedge 把 Edge 变成一个嵌入式 Tailscale 节点，
// 并把本机 DSH 端口暴露到 tailnet。
package tsnetedge

import (
	"context"
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
	"strconv"
	"strings"
	"sync"
	"time"

	"tailscale.com/tsnet"

	"meerkat/edge/internal/localport"
)

const DSHListenPort = 51858

type Config struct {
	Enabled      bool
	Hostname     string
	HubURL       string
	AuthKey      string
	StateDir     string
	StatusPath   string
	LocalAPIAddr string
}

type Service struct {
	server       *tsnet.Server
	state        string
	status       string
	mu           sync.Mutex
	ip           string
	onIPChange   func(string)
	listen       net.Listener
	dshPort      int
	apiOnce      sync.Once
	apiURL       string
	apiErr       string
	hubURL       string
	localAPIAddr string
	log          *log.Logger
}

func (s *Service) IP() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ip
}

// OnIPChange subscribes to address updates, including a delayed login.
func (s *Service) OnIPChange(fn func(string)) {
	s.mu.Lock()
	s.onIPChange = fn
	ip := s.ip
	s.mu.Unlock()
	if fn != nil && ip != "" {
		fn(ip)
	}
}
func (s *Service) updateIP(ip string) {
	s.mu.Lock()
	if s.ip == ip {
		s.mu.Unlock()
		return
	}
	s.ip = ip
	fn := s.onIPChange
	s.mu.Unlock()
	if fn != nil {
		fn(ip)
	}
}

func (s *Service) Dial(ctx context.Context, network, address string) (net.Conn, error) {
	if s == nil || s.server == nil {
		return nil, fmt.Errorf("tsnet edge 未启动")
	}
	return s.server.Dial(ctx, network, address)
}

// Start 启动 tsnet，但不等登录完成；Wait 等待节点 Running。
func Start(ctx context.Context, cfg Config, logger *log.Logger) (*Service, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	if logger == nil {
		logger = log.Default()
	}
	if strings.TrimSpace(cfg.StateDir) == "" {
		return nil, fmt.Errorf("tsnet edge 状态目录为空")
	}
	if cfg.Hostname == "" {
		cfg.Hostname = "meerkat-edge"
	}
	if cfg.StatusPath == "" {
		cfg.StatusPath = filepath.Join(cfg.StateDir, "tsnet-status.json")
	}
	if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
		return nil, fmt.Errorf("创建 tsnet 状态目录失败: %w", err)
	}
	// 等待登录时 tsnet 会每 5 秒重复同一条授权地址提示；去重后再写日志，避免刷满 app-debug.log。
	userLogf := dedupeLogf(func(format string, args ...any) { logger.Printf("tsnet-edge: "+format, args...) })
	server := &tsnet.Server{
		Dir:      cfg.StateDir,
		Hostname: cfg.Hostname,
		AuthKey:  cfg.AuthKey,
		UserLogf: userLogf,
	}
	if err := server.Start(); err != nil {
		return nil, fmt.Errorf("启动 tsnet edge 失败: %w", err)
	}
	service := &Service{server: server, state: "starting", status: cfg.StatusPath, log: logger, hubURL: cfg.HubURL, localAPIAddr: cfg.LocalAPIAddr}
	go service.monitorStatus(ctx)
	return service, nil
}

func (s *Service) Wait(ctx context.Context) error {
	if s == nil {
		return nil
	}
	st, err := s.server.Up(ctx)
	if err != nil {
		s.setState("error", err.Error())
		return err
	}
	ip4, _ := s.server.TailscaleIPs()
	if ip4.IsValid() {
		s.updateIP(ip4.String())
	}
	s.setState("running", "")
	s.log.Printf("tsnet-edge 已上线：%s (%s)", st.Self.HostName, ip4.String())
	return nil
}

// A paired Edge can finish its interactive login after the initial Wait times out.
func (s *Service) startLocalAPIWhenReady(ctx context.Context, state string, hasIP bool) {
	if state == "Running" && hasIP {
		s.apiOnce.Do(func() {
			if err := s.StartLocalAPI(ctx); err != nil {
				s.log.Printf("启动 Edge 本地 Hub API 代理失败：%v", err)
			}
		})
	}
}

func (s *Service) monitorStatus(ctx context.Context) {
	client, err := s.server.LocalClient()
	if err != nil {
		s.setState("error", err.Error())
		return
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		st, err := client.Status(ctx)
		if err != nil {
			s.setState("error", err.Error())
			continue
		}
		if st.Self == nil {
			continue
		}
		s.startLocalAPIWhenReady(ctx, st.BackendState, len(st.Self.TailscaleIPs) > 0)
		ips := make([]string, 0, len(st.Self.TailscaleIPs))
		for _, ip := range st.Self.TailscaleIPs {
			ips = append(ips, ip.String())
		}
		if len(ips) > 0 {
			s.updateIP(ips[0])
		}
		s.mu.Lock()
		localAPI := s.apiURL
		localAPIErr := s.apiErr
		s.mu.Unlock()
		payload := map[string]any{
			"enabled": true, "state": stateName(st.BackendState), "hostname": st.Self.HostName,
			"auth_url": st.AuthURL, "tailscale_ips": ips, "listen_port": DSHListenPort,
			"local_api_url": localAPI,
		}
		// 代理没起来时把具体原因写进状态，UI 才能显示「端口被系统保留」而不是笼统的「尚未就绪」。
		if localAPIErr != "" {
			payload["local_api_error"] = localAPIErr
		}
		s.writeStatus(payload)
	}
}

func (s *Service) setState(state, errText string) {
	payload := map[string]any{"enabled": true, "state": state, "hostname": "meerkat-edge", "listen_port": DSHListenPort}
	if errText != "" {
		payload["error"] = errText
	}
	// 状态快照要带上本地代理的实际情况：否则一轮状态刷新会把「代理没起来的原因」覆盖掉。
	s.mu.Lock()
	apiURL := s.apiURL
	apiErr := s.apiErr
	s.mu.Unlock()
	if apiURL != "" {
		payload["local_api_url"] = apiURL
	}
	if apiErr != "" {
		payload["local_api_error"] = apiErr
	}
	s.writeStatus(payload)
}

func (s *Service) writeStatus(value map[string]any) {
	s.mu.Lock()
	path := s.status
	s.mu.Unlock()
	if path == "" {
		return
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	_ = os.WriteFile(path, append(data, 10), 0o600)
}

func stateName(state string) string {
	switch state {
	case "Running":
		return "running"
	case "NeedsLogin", "NeedsMachineAuth":
		return "needs_login"
	default:
		return "starting"
	}
}

// SetDSHLocalPort 更新 tailnet DSH 转发目标端口；切 profile 时由 Edge 调用。
func (s *Service) SetDSHLocalPort(port int) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.dshPort = port
	s.mu.Unlock()
}

// DSHLocalPort 返回当前转发目标端口。
func (s *Service) DSHLocalPort() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dshPort
}

// ListenDSH 在 tailnet 上监听 :51858，并把原始 TCP 转发到本机 DSH。
// 转发目标可以随后通过 SetDSHLocalPort 更新。
func (s *Service) ListenDSH(ctx context.Context, localPort int) error {
	if s == nil {
		return nil
	}
	if localPort > 0 {
		s.mu.Lock()
		s.dshPort = localPort
		s.mu.Unlock()
	}
	ln, err := s.server.Listen("tcp", fmt.Sprintf(":%d", DSHListenPort))
	if err != nil {
		return fmt.Errorf("tsnet 监听 DSH 失败: %w", err)
	}
	s.mu.Lock()
	s.listen = ln
	s.mu.Unlock()
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			return nil
		}
		port := s.DSHLocalPort()
		if port <= 0 {
			_ = conn.Close()
			continue
		}
		go proxyTCP(conn, fmt.Sprintf("127.0.0.1:%d", port))
	}
}

func proxyTCP(src net.Conn, target string) {
	defer src.Close()
	dst, err := net.DialTimeout("tcp", target, 5*time.Second)
	if err != nil {
		return
	}
	defer dst.Close()
	go func() {
		_, _ = io.Copy(dst, src)
		if tcp, ok := dst.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
	}()
	_, _ = io.Copy(src, dst)
	if tcp, ok := src.(*net.TCPConn); ok {
		_ = tcp.CloseWrite()
	}
}

func (s *Service) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	ln := s.listen
	s.listen = nil
	s.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
	}
	if s.server != nil {
		_ = s.server.Close()
	}
}

// StartLocalAPI 在 127.0.0.1 上给本机 Meerkat UI 提供 Hub API 代理，
// 这样 Windows 本地 UI 不需要系统 Tailscale 也能访问 Hub。
//
// 首选端口绑不上时自动往后探测：Windows 的 Hyper-V / WSL / Docker 会保留大量高位
// TCP 端口段（51900 就常在保留段边界上），固定端口会导致本机 UI 的 /api 永远 503。
func (s *Service) StartLocalAPI(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if strings.TrimSpace(s.hubURL) == "" {
		return s.failLocalAPI(fmt.Errorf("tsnet hub URL 为空"))
	}
	target, err := url.Parse(s.hubURL)
	if err != nil || target.Host == "" {
		return s.failLocalAPI(fmt.Errorf("tsnet hub URL 无效"))
	}
	addr := strings.TrimSpace(s.localAPIAddr)
	if addr == "" {
		addr = localport.DefaultHost + ":51900"
	}
	host, port, err := localport.ParseAddr(addr)
	if err != nil {
		return s.failLocalAPI(fmt.Errorf("本地 Hub API 代理地址无效: %w", err))
	}
	listener, actualPort, err := localport.Listen(host, port, localport.DefaultScanLimit)
	if err != nil {
		return s.failLocalAPI(fmt.Errorf("监听本地 Hub API 代理失败: %w", err))
	}
	apiURL := "http://" + net.JoinHostPort(host, strconv.Itoa(actualPort))
	if actualPort != port {
		s.log.Printf("本地 Hub API 代理端口 %d 被占用或被系统保留，改用 %d（%s）", port, actualPort, apiURL)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.ForceAttemptHTTP2 = false
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		return s.server.Dial(ctx, network, address)
	}
	proxy.Transport = transport
	proxy.FlushInterval = -1
	s.mu.Lock()
	s.apiURL = apiURL
	s.apiErr = ""
	s.mu.Unlock()
	s.writeStatus(map[string]any{
		"enabled": true, "state": "running", "hostname": "meerkat-edge",
		"local_api_url": apiURL, "listen_port": DSHListenPort,
	})
	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()
	s.log.Printf("Edge 本地 Hub API 代理：%s -> %s", apiURL, s.hubURL)
	go func() {
		_ = http.Serve(listener, proxy)
	}()
	return nil
}

// failLocalAPI 记录代理启动失败原因（写进 tsnet-status.json 的 local_api_error），
// 让 UI 能说明具体是端口被占用/被系统保留，而不是笼统的「尚未就绪」。
func (s *Service) failLocalAPI(err error) error {
	s.mu.Lock()
	s.apiURL = ""
	s.apiErr = err.Error()
	s.mu.Unlock()
	s.setState("running", "")
	return err
}
