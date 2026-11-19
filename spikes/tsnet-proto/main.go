// Command tsnet-proto 是 Meerkat 的 tsnet 原型：
// 不安装 Tailscale 客户端，直接把一个 Tailscale 节点嵌进进程。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"tailscale.com/tsnet"
	"tailscale.com/types/logger"
)

type statusPayload struct {
	Hostname     string   `json:"hostname"`
	State        string   `json:"state"`
	AuthURL      string   `json:"auth_url,omitempty"`
	TailscaleIPs []string `json:"tailscale_ips,omitempty"`
	HubURL       string   `json:"hub_url,omitempty"`
	Listen       string   `json:"listen,omitempty"`
	Error        string   `json:"error,omitempty"`
}

type nodeStatus struct {
	mu    sync.RWMutex
	value statusPayload
}

func (s *nodeStatus) set(value statusPayload) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.value = value
}

func (s *nodeStatus) get() statusPayload {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.value
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	mode := "hub"
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	switch mode {
	case "hub":
		runHub(ctx)
	case "edge":
		runEdge(ctx)
	default:
		fmt.Println("usage: tsnet-proto [hub|edge] [flags]")
		os.Exit(2)
	}
}

func runHub(ctx context.Context) {
	fs := flag.NewFlagSet("hub", flag.ExitOnError)
	stateDir := fs.String("state", filepath.Join(homeDir(), ".meerkat", "tsnet-proto", "hub"), "tsnet state dir")
	hostname := fs.String("hostname", "meerkat-hub-proto", "tailnet hostname")
	listenAddr := fs.String("listen", ":7443", "tailnet listen address")
	localAddr := fs.String("local", "127.0.0.1:17443", "local status page")
	_ = fs.Parse(os.Args[2:])
	if err := os.MkdirAll(*stateDir, 0o700); err != nil {
		log.Fatalf("创建状态目录失败: %v", err)
	}
	status := &nodeStatus{}
	status.set(statusPayload{Hostname: *hostname, State: "starting", Listen: *listenAddr})
	server := &tsnet.Server{
		Dir:      *stateDir,
		Hostname: *hostname,
		UserLogf: logger.Logf(func(format string, args ...any) { log.Printf("tsnet: "+format, args...) }),
		Logf:     logger.Logf(func(format string, args ...any) { log.Printf("tsnet-debug: "+format, args...) }),
	}
	defer server.Close()

	go serveLocalStatus(ctx, *localAddr, status)
	go pollAuthURL(ctx, server, status)
	go waitAndServeHub(ctx, server, status, *listenAddr)
	<-ctx.Done()
	log.Printf("退出")
}

func waitAndServeHub(ctx context.Context, server *tsnet.Server, status *nodeStatus, listenAddr string) {
	st, err := server.Up(ctx)
	if err != nil {
		status.set(statusPayload{State: "error", Error: err.Error()})
		log.Printf("tsnet Up 失败: %v", err)
		return
	}
	ip4, _ := server.TailscaleIPs()
	payload := statusPayload{
		Hostname:     st.Self.HostName,
		State:        "running",
		TailscaleIPs: []string{ip4.String()},
		Listen:       listenAddr,
	}
	if st.Self.DNSName != "" {
		payload.HubURL = "http://" + strings.TrimSuffix(st.Self.DNSName, ".") + listenAddr
	}
	status.set(payload)
	ln, err := server.Listen("tcp", listenAddr)
	if err != nil {
		status.set(statusPayload{State: "error", Error: err.Error()})
		log.Printf("tsnet Listen 失败: %v", err)
		return
	}
	log.Printf("HUB 已上线：http://%s%s/", ip4.String(), listenAddr)
	log.Printf("MagicDNS：%s", payload.HubURL)
	httpServer := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload := status.get()
		if r.URL.Path == "/api/status" {
			writeJSON(w, payload)
			return
		}
		w.Header().Set("content-type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, "<!doctype html><html><body style='font-family:system-ui;padding:32px'><h1>Meerkat tsnet prototype</h1><pre>"+htmlEscape(mustJSON(payload))+"</pre></body></html>")
	})}
	_ = httpServer.Serve(ln)
}

func runEdge(ctx context.Context) {
	fs := flag.NewFlagSet("edge", flag.ExitOnError)
	stateDir := fs.String("state", filepath.Join(homeDir(), ".meerkat", "tsnet-proto", "edge"), "tsnet state dir")
	hostname := fs.String("hostname", "meerkat-edge-proto", "tailnet hostname")
	hub := fs.String("hub", "", "hub tailnet address (100.x.x.x:7443)")
	once := fs.Bool("once", false, "probe once and exit")
	_ = fs.Parse(os.Args[2:])
	if err := os.MkdirAll(*stateDir, 0o700); err != nil {
		log.Fatalf("创建状态目录失败: %v", err)
	}
	server := &tsnet.Server{
		Dir:      *stateDir,
		Hostname: *hostname,
		UserLogf: logger.Logf(func(format string, args ...any) { log.Printf("tsnet: "+format, args...) }),
	}
	defer server.Close()
	st, err := server.Up(ctx)
	if err != nil {
		log.Fatalf("tsnet Up 失败: %v", err)
	}
	ip4, _ := server.TailscaleIPs()
	log.Printf("EDGE 已上线：%s (%s)", st.Self.HostName, ip4.String())
	if *hub == "" {
		log.Printf("未指定 --hub，仅验证节点上线")
		return
	}
	client := server.HTTPClient()
	resp, err := client.Get("http://" + *hub + "/api/status")
	if err != nil {
		log.Fatalf("访问 Hub 失败: %v", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	log.Printf("HUB 响应 %d: %s", resp.StatusCode, string(data))
	if *once {
		return
	}
}

func serveLocalStatus(ctx context.Context, addr string, status *nodeStatus) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		payload := status.get()
		w.Header().Set("content-type", "text/html; charset=utf-8")
		auth := ""
		if payload.AuthURL != "" {
			auth = "<p><a href='" + htmlEscape(payload.AuthURL) + "' target='_blank'>打开 Tailscale 登录</a></p>"
		}
		_, _ = io.WriteString(w, "<!doctype html><html><body style='font-family:system-ui;padding:32px;max-width:900px'><h1>Meerkat tsnet hub</h1>"+auth+"<pre>"+htmlEscape(mustJSON(payload))+"</pre></body></html>")
	})
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, status.get()) })
	server := &http.Server{Addr: addr, Handler: mux}
	go func() {
		<-ctx.Done()
		_ = server.Close()
	}()
	log.Printf("本地状态页：http://%s/", addr)
	_ = server.ListenAndServe()
}

func pollAuthURL(ctx context.Context, server *tsnet.Server, status *nodeStatus) {
	client, err := server.LocalClient()
	if err != nil {
		status.set(statusPayload{State: "error", Error: err.Error()})
		return
	}
	for {
		st, err := client.Status(ctx)
		if err == nil {
			current := status.get()
			current.State = st.BackendState
			current.AuthURL = st.AuthURL
			status.set(current)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("content-type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(value)
}

func mustJSON(value any) string {
	data, _ := json.MarshalIndent(value, "", "  ")
	return string(data)
}

func htmlEscape(value string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;", "'", "&#39;")
	return replacer.Replace(value)
}

func homeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return home
}
