//go:build !android

package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDecideAuthOpenSameURLOnlyOnce(t *testing.T) {
	now := time.Now()
	url := "https://login.tailscale.com/a/aaa"
	state, open := decideAuthOpen("needs_login", url, authOpenState{}, now)
	if !open {
		t.Fatal("首次 auth_url 应该自动打开")
	}
	if _, open := decideAuthOpen("needs_login", url, state, now.Add(2*time.Second)); open {
		t.Fatal("相同 auth_url 不应重复打开（2 秒轮询会反复弹浏览器）")
	}
	if _, open := decideAuthOpen("needs_login", "  "+url+"  ", state, now.Add(4*time.Second)); open {
		t.Fatal("auth_url 前后空白不应影响去重")
	}
}

func TestDecideAuthOpenURLChangeHonorsCooldown(t *testing.T) {
	now := time.Now()
	first := "https://login.tailscale.com/a/aaa"
	second := "https://login.tailscale.com/a/bbb"
	state, _ := decideAuthOpen("needs_login", first, authOpenState{}, now)

	if _, open := decideAuthOpen("needs_login", second, state, now.Add(2*time.Second)); open {
		t.Fatal("auth_url 变化但仍在冷却期，不应重复弹窗")
	}
	next, open := decideAuthOpen("needs_login", second, state, now.Add(authOpenCooldown+time.Second))
	if !open {
		t.Fatal("冷却期后新的 auth_url 应允许打开")
	}
	if next.openedURL != second {
		t.Fatalf("去重键应更新为新 auth_url，实际 %q", next.openedURL)
	}
}

func TestDecideAuthOpenResetsAfterLeavingNeedsLogin(t *testing.T) {
	now := time.Now()
	url := "https://login.tailscale.com/a/aaa"
	state, _ := decideAuthOpen("needs_login", url, authOpenState{}, now)

	reset, open := decideAuthOpen("running", "", state, now.Add(time.Second))
	if open {
		t.Fatal("running 状态不应打开授权页")
	}
	if reset.openedURL != "" || !reset.openedAt.IsZero() {
		t.Fatal("离开 needs_login 后应重置去重状态")
	}
	if _, open := decideAuthOpen("needs_login", url, reset, now.Add(2*time.Second)); !open {
		t.Fatal("重新进入 needs_login 后应可再次自动打开")
	}
}

func TestDecideAuthOpenIgnoresEmptyURLAndOtherStates(t *testing.T) {
	now := time.Now()
	for _, state := range []string{"", "starting", "error", "running"} {
		if _, open := decideAuthOpen(state, "https://login.tailscale.com/a/aaa", authOpenState{}, now); open {
			t.Fatalf("状态 %q 不应自动打开授权页", state)
		}
	}
	if _, open := decideAuthOpen("needs_login", "   ", authOpenState{}, now); open {
		t.Fatal("auth_url 为空时不应打开")
	}
}

func TestBindHostDefaultsToLoopback(t *testing.T) {
	old := appCfg
	defer func() { appCfg = old }()
	appCfg = appConfig{}
	if got := bindHost(); got != "127.0.0.1" {
		t.Fatalf("default bind host = %q, want 127.0.0.1", got)
	}
}

func TestBindHostRequiresAllowLANForWildcard(t *testing.T) {
	old := appCfg
	defer func() { appCfg = old }()
	appCfg = appConfig{BindHost: "0.0.0.0"}
	if got := bindHost(); got != "127.0.0.1" {
		t.Fatalf("persisted wildcard without allow_lan = %q, want 127.0.0.1", got)
	}
}

func TestBindHostAllowsLANWhenOptedIn(t *testing.T) {
	old := appCfg
	defer func() { appCfg = old }()
	appCfg = appConfig{AllowLAN: true, BindHost: "0.0.0.0"}
	if got := bindHost(); got != "0.0.0.0" {
		t.Fatalf("allow_lan bind host = %q, want 0.0.0.0", got)
	}
	appCfg = appConfig{AllowLAN: true}
	if got := bindHost(); got != "0.0.0.0" {
		t.Fatalf("allow_lan default bind host = %q, want 0.0.0.0", got)
	}
}

func TestDesktopJSONErrorUsesUTF8Charset(t *testing.T) {
	recorder := httptest.NewRecorder()
	message := "Edge 正在加入内嵌网络并自动配对，请稍候"
	writeDesktopJSON(recorder, http.StatusServiceUnavailable, map[string]any{
		"error": map[string]string{"code": "edge_network_starting", "message": message},
	})
	if got := recorder.Header().Get("content-type"); got != desktopJSONContentType {
		t.Fatalf("content-type = %q, want %q", got, desktopJSONContentType)
	}
	if body := recorder.Body.String(); !strings.Contains(body, message) {
		t.Fatalf("response body lost Chinese text: %q", body)
	}
}
