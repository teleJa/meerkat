package hub

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func registerTestDevice(t *testing.T, h *Hub) string {
	t.Helper()
	now := time.Now()
	token, _, err := h.createPairingToken(nil, 600, now)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := h.consumePairingToken(token, now); err != nil || !ok {
		t.Fatalf("consume pairing token: %v %v", ok, err)
	}
	registered, err := h.registerDevice(DeviceInfo{
		Name: "dsh-device", Hostname: "dsh-host", OS: "darwin", Arch: "arm64",
		Role: "test", BootID: "boot-dsh", Capabilities: []string{"exec"},
	}, "0.1.0", "1", now)
	if err != nil {
		t.Fatal(err)
	}
	return registered.Device.ID
}

func dshRequest(t *testing.T, server *httptest.Server, h *Hub, method, path string, body any) *http.Response {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		reader = bytes.NewReader(data)
	} else {
		reader = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, server.URL+path, reader)
	req.Header.Set("authorization", "Bearer "+h.AdminToken())
	req.Header.Set("content-type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestDSHSelectionAndStateSummary(t *testing.T) {
	h := testHub(t)
	deviceID := registerTestDevice(t, h)
	now := time.Now()

	if err := h.setDSHSelection(deviceID, "bot", "connect", now); err != nil {
		t.Fatal(err)
	}
	if profile, mode, found, err := h.getDSHSelection(deviceID); err != nil || !found || profile != "bot" || mode != "connect" {
		t.Fatalf("selection = %q/%q found=%v err=%v", profile, mode, found, err)
	}

	state := DSHState{
		Profile: "bot", Mode: "connect", Status: "running", Owner: "external", Installed: true,
		Profiles: []DSHProfileItem{{Name: "bot", Available: true, Running: true}},
	}
	if err := h.setDSHState(deviceID, state, now); err != nil {
		t.Fatal(err)
	}
	if err := h.setHarnessURL(deviceID, "http://127.0.0.1:51901/?token=secret", now); err != nil {
		t.Fatal(err)
	}
	summary := h.dshSummary(deviceID)
	if summary["profile"] != "bot" || summary["mode"] != "connect" || summary["status"] != "running" || summary["owner"] != "external" {
		t.Fatalf("summary = %+v", summary)
	}
	if summary["has_url"] != true {
		t.Fatalf("has_url = %+v", summary["has_url"])
	}
	if _, leaked := summary["url"]; leaked {
		t.Fatalf("summary must not expose the token URL: %+v", summary)
	}

	mux := http.NewServeMux()
	h.registerHTTP(mux)
	server := httptest.NewServer(mux)
	defer server.Close()

	resp := dshRequest(t, server, h, http.MethodGet, "/api/devices/"+deviceID+"/dsh", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status endpoint = %d", resp.StatusCode)
	}
	var payload struct {
		Online bool                   `json:"online"`
		DSH    map[string]interface{} `json:"dsh"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Online {
		t.Fatalf("device should be offline in this test")
	}
	if payload.DSH["profile"] != "bot" || payload.DSH["status"] != "running" {
		t.Fatalf("payload = %+v", payload)
	}

	listResp := dshRequest(t, server, h, http.MethodGet, "/api/devices", nil)
	defer listResp.Body.Close()
	var list struct {
		Devices []map[string]interface{} `json:"devices"`
	}
	if err := json.NewDecoder(listResp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list.Devices) != 1 {
		t.Fatalf("devices = %d", len(list.Devices))
	}
	dsh, ok := list.Devices[0]["dsh"].(map[string]interface{})
	if !ok || dsh["profile"] != "bot" {
		t.Fatalf("device dsh summary = %+v", list.Devices[0]["dsh"])
	}
}

func TestDSHActionsOfflineAndValidation(t *testing.T) {
	h := testHub(t)
	deviceID := registerTestDevice(t, h)

	mux := http.NewServeMux()
	h.registerHTTP(mux)
	server := httptest.NewServer(mux)
	defer server.Close()

	resp := dshRequest(t, server, h, http.MethodPost, "/api/devices/"+deviceID+"/dsh/action", map[string]any{"action": "connect", "profile": "bot"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("offline action status = %d, want 409", resp.StatusCode)
	}
	// 选择先保存，设备上线后可以重放。
	if profile, mode, found, _ := h.getDSHSelection(deviceID); !found || profile != "bot" || mode != "connect" {
		t.Fatalf("saved selection = %q/%q found=%v", profile, mode, found)
	}

	resp = dshRequest(t, server, h, http.MethodPost, "/api/devices/"+deviceID+"/dsh/profile", map[string]any{"profile": "../bad"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid profile status = %d, want 400", resp.StatusCode)
	}

	resp = dshRequest(t, server, h, http.MethodPost, "/api/devices/"+deviceID+"/dsh/profiles", map[string]any{"name": "probe"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("offline create status = %d, want 409", resp.StatusCode)
	}
}
