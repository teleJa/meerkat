package center

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"meerkat/edge/internal/protocol"
)

func TestRegisterSuccess(t *testing.T) {
	var got protocol.RegisterRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/edges/register" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &got); err != nil {
			t.Errorf("请求体不是合法 JSON: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"device_id":"dev_a1b2c3d4","device_secret":"deadbeef","protocol_version":"1","center_time":"2026-11-06T05:40:00.000Z","heartbeat_interval_ms":10000,"metrics_interval_ms":5000}`))
	}))
	defer srv.Close()

	c := New(5 * time.Second)
	resp, err := c.Register(context.Background(), srv.URL, protocol.RegisterRequest{
		PairingToken:    "pairtoken",
		ProtocolVersion: protocol.Version,
		EdgeVersion:     "0.1.0",
		Device: protocol.Device{
			Name:         "MacBook Pro",
			Hostname:     "MacBookPro",
			OS:           "macOS 15.4",
			Arch:         "arm64",
			Role:         "Edge",
			Capabilities: []string{"metrics"},
			BootID:       "boot_1e2d3c4b",
		},
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if resp.DeviceID != "dev_a1b2c3d4" || resp.DeviceSecret != "deadbeef" || resp.HeartbeatIntervalMS != 10000 {
		t.Fatalf("响应字段错误: %+v", resp)
	}
	if got.PairingToken != "pairtoken" || got.Device.BootID != "boot_1e2d3c4b" || got.ProtocolVersion != "1" {
		t.Fatalf("中心收到的请求错误: %+v", got)
	}
}

func TestRegisterErrorsAreReadable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"code":"pairing_token_invalid","message":"pairing token expired"}`))
	}))
	defer srv.Close()

	_, err := New(5*time.Second).Register(context.Background(), srv.URL, protocol.RegisterRequest{PairingToken: "bad"})
	if err == nil {
		t.Fatal("401 应返回错误")
	}
	msg := err.Error()
	if !strings.Contains(msg, "401") || !strings.Contains(msg, "pairing_token_invalid") || !strings.Contains(msg, "配对令牌无效") {
		t.Fatalf("错误信息不可读: %s", msg)
	}
}

func TestRegisterRejectsIncompleteResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"device_id":"dev_x"}`))
	}))
	defer srv.Close()

	_, err := New(5*time.Second).Register(context.Background(), srv.URL, protocol.RegisterRequest{})
	if err == nil || !strings.Contains(err.Error(), "缺少 device_id 或 device_secret") {
		t.Fatalf("不完整响应应报错，实际: %v", err)
	}
}

func TestHealth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/health" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true,"version":"0.1.0","time":"2026-11-06T05:41:00.000Z","uptime_seconds":42}`))
	}))
	defer srv.Close()

	h, err := New(5*time.Second).Health(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if !h.OK || h.Version != "0.1.0" || h.UptimeSeconds != 42 {
		t.Fatalf("健康检查字段错误: %+v", h)
	}
}

func TestHealthUnreachable(t *testing.T) {
	c := New(2 * time.Second)
	_, err := c.Health(context.Background(), "http://127.0.0.1:1")
	if err == nil {
		t.Fatal("不可达地址应返回错误")
	}
	if !strings.Contains(err.Error(), "请求中心") {
		t.Fatalf("错误信息不可读: %v", err)
	}
}
