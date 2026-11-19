package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"tailscale.com/tsnet"
	"tailscale.com/types/logger"
)

const tsnetServicePort = 7443

type tsnetStatus struct {
	mu    sync.RWMutex
	value map[string]any
}

func (s *tsnetStatus) set(value map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.value = value
}

func (s *tsnetStatus) get() map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.value == nil {
		return map[string]any{"enabled": false, "state": "off"}
	}
	out := make(map[string]any, len(s.value))
	for k, v := range s.value {
		out[k] = v
	}
	return out
}

// TSNetURL 返回 Hub 在 tailnet 上的访问地址（仅 running 时有效）。
func (h *Hub) TSNetURL() string {
	state := h.tsnetState.get()
	if state["state"] != "running" {
		return ""
	}
	value, _ := state["hub_url"].(string)
	return strings.TrimSpace(value)
}

// TSNetMagicDNSURL 返回稳定的 MagicDNS 访问地址；
// 仅当 tsnet 已 running 时可用，避免目标设备拿到一个尚未监听的地址。
func (h *Hub) TSNetMagicDNSURL() string {
	if h.TSNetURL() == "" {
		return ""
	}
	return fmt.Sprintf("http://meerkat-hub:%d", tsnetServicePort)
}

func (h *Hub) currentTSNet() *tsnet.Server {
	h.tsnetMu.Lock()
	defer h.tsnetMu.Unlock()
	return h.tsnetServer
}

func (h *Hub) StartTSNet() error { return h.startTSNet() }

func (h *Hub) handleTSNetStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.tsnetState.get())
}

func (h *Hub) handleTSNetStart(w http.ResponseWriter, r *http.Request) {
	h.tsnetMu.Lock()
	if h.tsnetServer != nil {
		state := h.tsnetState.get()
		h.tsnetMu.Unlock()
		writeJSON(w, http.StatusOK, state)
		return
	}
	h.tsnetMu.Unlock()
	if err := h.startTSNet(); err != nil {
		writeError(w, http.StatusBadGateway, "tsnet_start_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, h.tsnetState.get())
}

func (h *Hub) handleTSNetStop(w http.ResponseWriter, r *http.Request) {
	h.stopTSNet()
	writeJSON(w, http.StatusOK, h.tsnetState.get())
}

func (h *Hub) startTSNet() error {
	h.tsnetMu.Lock()
	defer h.tsnetMu.Unlock()
	if h.tsnetServer != nil {
		return nil
	}
	dir := filepath.Join(h.cfg.DataDir, "tsnet", "hub")
	server := &tsnet.Server{
		Dir:      dir,
		Hostname: "meerkat-hub",
		UserLogf: logger.Logf(func(format string, args ...any) { log.Printf("tsnet: "+format, args...) }),
	}
	if err := server.Start(); err != nil {
		return fmt.Errorf("启动 tsnet 失败: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.tsnetServer = server
	h.tsnetCancel = cancel
	h.tsnetState.set(map[string]any{"enabled": true, "state": "starting", "hostname": "meerkat-hub", "port": tsnetServicePort})
	go h.monitorTSNet(ctx, server)
	// 旧 Harness 代理可能是在没有 tsnet 时创建的，切换网络后重建。
	go h.resetHarnessProxies()
	return nil
}

func (h *Hub) monitorTSNet(ctx context.Context, server *tsnet.Server) {
	client, err := server.LocalClient()
	if err != nil {
		h.tsnetState.set(map[string]any{"enabled": true, "state": "error", "error": err.Error()})
		return
	}
	serving := false
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
			state := h.tsnetState.get()
			state["enabled"] = true
			state["state"] = "error"
			state["error"] = err.Error()
			h.tsnetState.set(state)
			continue
		}
		ips := make([]string, 0, len(st.Self.TailscaleIPs))
		for _, ip := range st.Self.TailscaleIPs {
			ips = append(ips, ip.String())
		}
		state := map[string]any{
			"enabled":       true,
			"state":         tsnetStateName(st.BackendState),
			"hostname":      st.Self.HostName,
			"auth_url":      st.AuthURL,
			"tailscale_ips": ips,
			"port":          tsnetServicePort,
		}
		if len(ips) > 0 {
			state["hub_url"] = fmt.Sprintf("http://%s:%d", ips[0], tsnetServicePort)
		}
		h.tsnetState.set(state)
		if st.BackendState == "Running" && !serving {
			listener, err := server.Listen("tcp", fmt.Sprintf(":%d", tsnetServicePort))
			if err != nil {
				state["state"] = "error"
				state["error"] = "tsnet listen: " + err.Error()
				h.tsnetState.set(state)
				continue
			}
			serving = true
			go func() {
				log.Printf("tsnet Hub API 监听 :%d", tsnetServicePort)
				_ = http.Serve(listener, h.server.Handler)
			}()
		}
	}
}

func tsnetStateName(state string) string {
	switch state {
	case "Running":
		return "running"
	case "NeedsLogin", "NeedsMachineAuth":
		return "needs_login"
	case "Stopped":
		return "stopped"
	default:
		return "starting"
	}
}

func (h *Hub) stopTSNet() {
	h.tsnetMu.Lock()
	server := h.tsnetServer
	cancel := h.tsnetCancel
	h.tsnetServer = nil
	h.tsnetCancel = nil
	h.tsnetMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if server != nil {
		_ = server.Close()
	}
	h.tsnetState.set(map[string]any{"enabled": false, "state": "off"})
	go h.resetHarnessProxies()
}

// 让 Hub 的现有鉴权、API 和 WebSocket 复用同一个 handler。
var _ = websocket.TextMessage
var _ = net.IPv4len
var _ = json.Marshal
