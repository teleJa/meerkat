package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"meerkat/edge/internal/agent"
	"meerkat/edge/internal/center"
)

// registerStub 返回一个最小的 Hub 注册接口。
func registerStub(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/edges/register" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method %q", r.Method)
		}
		var req struct {
			PairingToken string `json:"pairing_token"`
			Device       struct {
				Name string `json:"name"`
			} `json:"device"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode register request: %v", err)
		}
		if req.PairingToken == "" {
			t.Errorf("missing pairing token")
		}
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}

func TestPairDeviceWritesIdentity(t *testing.T) {
	server := registerStub(t, http.StatusCreated, `{
		"device_id": "dev_test",
		"device_secret": "secret_test",
		"protocol_version": "1",
		"heartbeat_interval_ms": 10000,
		"metrics_interval_ms": 5000
	}`)
	dir := t.TempDir()

	id, err := pairDevice(context.Background(), pairRequest{
		StateDir: dir,
		HubURL:   server.URL,
		Token:    "pair-token",
		Name:     "test-node",
		Role:     "Edge · Desktop",
	})
	if err != nil {
		t.Fatalf("pairDevice: %v", err)
	}
	if id.DeviceID != "dev_test" || id.DeviceSecret != "secret_test" {
		t.Fatalf("unexpected identity: %#v", id)
	}
	if id.CenterURL != server.URL {
		t.Fatalf("center url = %q, want %q", id.CenterURL, server.URL)
	}
	if id.Device.Name != "test-node" || id.Device.Role != "Edge · Desktop" {
		t.Fatalf("device overrides not applied: %#v", id.Device)
	}

	loaded, err := loadIdentityForTest(dir)
	if err != nil {
		t.Fatalf("load saved identity: %v", err)
	}
	if loaded.DeviceID != "dev_test" {
		t.Fatalf("saved device id = %q", loaded.DeviceID)
	}
	info, err := os.Stat(filepath.Join(dir, "identity.json"))
	if err != nil {
		t.Fatalf("stat identity: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("identity mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestPairDeviceUsesDialer(t *testing.T) {
	server := registerStub(t, http.StatusOK, `{"device_id":"dev","device_secret":"secret","protocol_version":"1"}`)
	var dials atomic.Int32
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		dials.Add(1)
		return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, network, address)
	}
	if _, err := pairDevice(context.Background(), pairRequest{
		StateDir: t.TempDir(),
		HubURL:   server.URL,
		Token:    "pair-token",
		Dial:     dial,
	}); err != nil {
		t.Fatalf("pairDevice with dialer: %v", err)
	}
	if dials.Load() == 0 {
		t.Fatal("custom dialer was not used")
	}
}

func TestPairDeviceMissingToken(t *testing.T) {
	_, err := pairDevice(context.Background(), pairRequest{
		StateDir: t.TempDir(),
		HubURL:   "http://127.0.0.1:1",
	})
	if err == nil || !strings.Contains(err.Error(), "配对令牌") {
		t.Fatalf("expected missing token error, got %v", err)
	}
}

func TestPairDeviceHubError(t *testing.T) {
	server := registerStub(t, http.StatusUnauthorized, `{"code":"pairing_token_invalid","message":"token 已使用"}`)
	_, err := pairDevice(context.Background(), pairRequest{
		StateDir: t.TempDir(),
		HubURL:   server.URL,
		Token:    "used-token",
	})
	if err == nil || !strings.Contains(err.Error(), "配对令牌无效") {
		t.Fatalf("expected pairing token hint, got %v", err)
	}
}

func TestWritePairingStatus(t *testing.T) {
	dir := t.TempDir()
	writePairingStatus(dir, pairingStatus{Phase: pairPhasePairing, HubURL: "http://100.64.0.1:7443"}, nil)
	writePairingStatus(dir, pairingStatus{Phase: pairPhaseError, Error: "boom", DeviceID: ""}, nil)

	data, err := os.ReadFile(filepath.Join(dir, pairingStatusFileName))
	if err != nil {
		t.Fatalf("read pairing status: %v", err)
	}
	var status pairingStatus
	if err := json.Unmarshal(data, &status); err != nil {
		t.Fatalf("parse pairing status: %v", err)
	}
	if status.Phase != pairPhaseError || status.Error != "boom" {
		t.Fatalf("unexpected status: %#v", status)
	}
}

func TestAutoPairWithoutPairingConfig(t *testing.T) {
	dir := t.TempDir()
	if _, err := autoPair(context.Background(), dir, testFileConfig(), nil, nil); err == nil {
		t.Fatal("expected error when pairing_token/tsnet_hub_url missing")
	}
	data, err := os.ReadFile(filepath.Join(dir, pairingStatusFileName))
	if err != nil {
		t.Fatalf("read pairing status: %v", err)
	}
	var status pairingStatus
	if err := json.Unmarshal(data, &status); err != nil {
		t.Fatalf("parse pairing status: %v", err)
	}
	if status.Phase != pairPhaseError || status.Error == "" {
		t.Fatalf("expected error status, got %#v", status)
	}
}

// TestAutoPairRequiresNetworkWhenTsnetEnabled 验证启用 tsnet 但未入网时不会尝试注册。
func TestAutoPairRequiresNetworkWhenTsnetEnabled(t *testing.T) {
	cfg := testFileConfig()
	cfg.TSNetEnabled = true
	cfg.TSNetHubURL = "http://100.64.0.1:7443"
	cfg.PairingToken = "token"
	if _, err := autoPair(context.Background(), t.TempDir(), cfg, nil, nil); err == nil {
		t.Fatal("expected error when tsnet is not up")
	}
}

// loadIdentityForTest 直接读取身份文件，避免测试依赖 identity 包的实现细节。
func loadIdentityForTest(dir string) (*identityFile, error) {
	data, err := os.ReadFile(filepath.Join(dir, "identity.json"))
	if err != nil {
		return nil, err
	}
	var id identityFile
	if err := json.Unmarshal(data, &id); err != nil {
		return nil, err
	}
	return &id, nil
}

type identityFile struct {
	CenterURL    string `json:"center_url"`
	DeviceID     string `json:"device_id"`
	DeviceSecret string `json:"device_secret"`
}

// testFileConfig 返回空的可变配置副本，供配对用例按需覆盖字段。
func testFileConfig() agent.FileConfig { return agent.FileConfig{} }

func TestRetryablePairError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "network", err: errors.New("connect: connection refused"), want: true},
		{name: "hub 500", err: &center.APIError{StatusCode: 500}, want: true},
		{name: "hub 503", err: &center.APIError{StatusCode: 503}, want: true},
		{name: "token invalid", err: &center.APIError{StatusCode: 401}, want: false},
		{name: "protocol", err: &center.APIError{StatusCode: 400}, want: false},
	}
	for _, tc := range cases {
		if got := retryablePairError(tc.err); got != tc.want {
			t.Errorf("%s: retryablePairError = %v, want %v", tc.name, got, tc.want)
		}
	}
}
