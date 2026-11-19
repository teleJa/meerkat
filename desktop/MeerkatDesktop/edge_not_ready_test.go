//go:build !android

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupEdgeStatus 把 Edge 状态目录指向临时目录，避免读到开发机自己的状态。
func setupEdgeStatus(t *testing.T) string {
	t.Helper()
	old := appCfg
	dir := t.TempDir()
	appCfg.EdgeStateDir = dir
	t.Cleanup(func() { appCfg = old })
	return dir
}

// writeEdgeStatus 把 tsnet-status.json 写进当前测试的 Edge 状态目录。
func writeEdgeStatus(t *testing.T, payload string) {
	t.Helper()
	dir := edgeStateDirectory()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tsnet-status.json"), []byte(payload), 0o600); err != nil {
		t.Fatalf("write status: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "identity.json"), []byte(`{"device_id":"dev_test"}`), 0o600); err != nil {
		t.Fatalf("write identity: %v", err)
	}
}

// 本地代理端口全被占用时，UI 应该看到「端口不可用」的具体原因，
// 而不是笼统的「尚未就绪」——那会让用户等一个永远不会来的状态。
func TestEdgeNotReadyReportsLocalAPIPortReason(t *testing.T) {
	setupEdgeStatus(t)
	writeEdgeStatus(t, `{"enabled":true,"state":"running","local_api_url":"","local_api_error":"51900-51999 共 100 个端口全部被占用或被系统保留"}`)

	code, message := edgeNotReady()
	if code != "edge_proxy_port_unavailable" {
		t.Fatalf("code = %q，want edge_proxy_port_unavailable", code)
	}
	if !strings.Contains(message, "全部被占用") {
		t.Fatalf("message 应带上具体原因：%q", message)
	}
	if strings.Contains(message, "尚未就绪") {
		t.Fatalf("有具体原因时不该退化成笼统文案：%q", message)
	}
}

// 代理正常绑定后不再报错。
func TestEdgeNotReadyWhenProxyReady(t *testing.T) {
	setupEdgeStatus(t)
	writeEdgeStatus(t, `{"enabled":true,"state":"running","local_api_url":"http://127.0.0.1:51901"}`)

	code, message := edgeNotReady()
	if message != "" {
		t.Fatalf("代理就绪时不该有错误：%s %s", code, message)
	}
}

// 还没写 local_api_error、也没 local_api_url 时，保持原来的「尚未就绪」。
func TestEdgeNotReadyFallsBackToStarting(t *testing.T) {
	setupEdgeStatus(t)
	writeEdgeStatus(t, `{"enabled":true,"state":"running","local_api_url":""}`)

	code, message := edgeNotReady()
	if code != "edge_proxy_starting" {
		t.Fatalf("code = %q，want edge_proxy_starting", code)
	}
	if !strings.Contains(message, "尚未就绪") {
		t.Fatalf("message = %q", message)
	}
}
