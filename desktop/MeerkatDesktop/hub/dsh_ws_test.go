package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func readWSMessage(t *testing.T, conn *websocket.Conn) map[string]any {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read ws: %v", err)
	}
	var message map[string]any
	if err := json.Unmarshal(raw, &message); err != nil {
		t.Fatalf("decode ws: %v", err)
	}
	return message
}

func TestDSHProfileOverWebSocket(t *testing.T) {
	h := testHub(t)
	now := time.Now()
	token, _, err := h.createPairingToken(nil, 600, now)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := h.consumePairingToken(token, now); err != nil || !ok {
		t.Fatalf("pairing token: %v %v", ok, err)
	}
	registered, err := h.registerDevice(DeviceInfo{
		Name: "ws-device", Hostname: "ws-host", OS: "darwin", Arch: "arm64",
		Role: "test", BootID: "boot-ws", Capabilities: []string{"exec"},
	}, "0.1.0", "1", now)
	if err != nil {
		t.Fatal(err)
	}
	deviceID := registered.Device.ID
	// 先保存选择，Edge 连接后应收到 select，恢复上次选择。
	if err := h.setDSHSelection(deviceID, "bot", "connect", now); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	h.registerHTTP(mux)
	server := httptest.NewServer(mux)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/edges/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, http.Header{})
	if err != nil {
		t.Fatalf("dial ws: %v", err)
	}
	defer conn.Close()
	hello := map[string]any{
		"type": "hello", "protocol_version": "1", "device_id": deviceID,
		"device_secret": registered.Secret, "edge_version": "0.1.0", "boot_id": "boot-ws",
		"capabilities": []string{"exec"},
	}
	if err := conn.WriteJSON(hello); err != nil {
		t.Fatal(err)
	}
	if ack := readWSMessage(t, conn); ack["type"] != "hello.ack" {
		t.Fatalf("first message = %+v", ack)
	}
	selectMsg := readWSMessage(t, conn)
	if selectMsg["type"] != "harness.profile" || selectMsg["action"] != "select" || selectMsg["profile"] != "bot" || selectMsg["mode"] != "connect" {
		t.Fatalf("sync message = %+v", selectMsg)
	}

	// Edge 上报状态后，Hub 应保存 URL 与 profile 状态。
	update := map[string]any{
		"type": "harness.update",
		"url":  "http://127.0.0.1:51901/?token=test",
		"dsh": map[string]any{
			"profile": "bot", "mode": "connect", "status": "running", "owner": "external",
			"installed": true, "profiles": []map[string]any{{"name": "bot", "available": true, "running": true}},
		},
	}
	if err := conn.WriteJSON(update); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		state, found, err := h.getDSHState(deviceID)
		if err == nil && found && state.Status == "running" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("state not stored: %+v %v", state, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if raw, err := h.getHarnessURL(deviceID); err != nil || !strings.Contains(raw, "token=test") {
		t.Fatalf("harness url = %q, %v", raw, err)
	}

	// 在线设备的动作应下发到这条 WS 连接。
	resp := dshRequest(t, server, h, http.MethodPost, "/api/devices/"+deviceID+"/dsh/action", map[string]any{"action": "launch", "profile": "probe"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("action status = %d", resp.StatusCode)
	}
	actionMsg := readWSMessage(t, conn)
	if actionMsg["type"] != "harness.profile" || actionMsg["action"] != "launch" || actionMsg["profile"] != "probe" {
		t.Fatalf("action message = %+v", actionMsg)
	}
}
