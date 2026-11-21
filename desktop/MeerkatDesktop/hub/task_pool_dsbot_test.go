package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTaskPoolBotDirectoryAndReceipts(t *testing.T) {
	var sends int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Endpoint string            `json:"endpoint"`
			Payload  map[string]string `json:"payload"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || r.Method != "POST" || r.URL.Path != "/api/bot" {
			t.Errorf("unexpected bot request: %s %s %v", r.Method, r.URL.Path, err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if input.Endpoint == "state" {
			fmt.Fprint(w, `{"ok":true,"value":{"mainBotId":"chief-id","bots":[{"id":"chief-id","name":"Chief"}],"rooms":[{"id":"admin-room","mode":"admin","admin":"chief-id","members":["chief-id"]},{"id":"all-room","mode":"everyone","admin":"chief-id","members":["chief-id"]},{"id":"missing-admin","mode":"admin","admin":"unknown","members":["unknown"]}]}}`)
			return
		}
		if input.Endpoint != "send" || input.Payload["sessionId"] != "admin-room" || strings.Contains(input.Payload["text"], "@") ||
			!strings.Contains(input.Payload["text"], "任务 ID tp_1 · Meerkat 看板 #/live/task-pool/tp_1") {
			t.Errorf("unexpected delivery payload: %+v", input)
		}
		sends++
		switch sends {
		case 1:
			fmt.Fprint(w, `{"ok":true,"value":true}`)
		case 2:
			fmt.Fprint(w, `{"ok":false,"error":{"code":"bot/invalid","message":"details"}}`)
		case 3:
			w.WriteHeader(http.StatusBadGateway)
		default:
			fmt.Fprint(w, `{"delivery":"not confirmed"}`)
		}
	}))
	t.Cleanup(server.Close)
	client := &poolBotClient{http: server.Client(), endpoint: server.URL + "/api/bot"}
	ctx := context.Background()
	directory, err := client.directory(ctx)
	if err != nil || directory.MainBotID != "chief-id" {
		t.Fatalf("directory: %+v %v", directory, err)
	}
	if target, ok := directory.target("group", "admin-room"); !ok || target.ActorID != "chief-id" {
		t.Fatalf("admin room: %+v %v", target, ok)
	}
	for _, room := range []string{"all-room", "missing-admin"} {
		if target, ok := directory.target("group", room); ok {
			t.Fatalf("unsupported room accepted: %+v", target)
		}
	}
	for _, want := range [][2]string{{"queued", ""}, {"failed", "bot/invalid"}, {"uncertain", "delivery_unconfirmed"}, {"uncertain", "delivery_unconfirmed"}} {
		status, code := client.send(ctx, "admin-room", "assigned", "tp_1")
		if status != want[0] || code != want[1] {
			t.Fatalf("send returned (%s, %s), want %v", status, code, want)
		}
	}
}

func TestTaskPoolChiefToGroupDelivery(t *testing.T) {
	const directory = `{"ok":true,"value":{"bots":[{"id":"chief","name":"Chief"},{"id":"builder","name":"Builder"}],"rooms":[{"id":"team","name":"Team","mode":"admin","admin":"builder","members":["builder"]}]}}`
	var mu sync.Mutex
	var targets []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Query().Get("token") == "fixture" {
			http.SetCookie(w, &http.Cookie{Name: "dsh", Value: "session", Path: "/"})
			w.WriteHeader(http.StatusOK)
			return
		}
		if cookie, err := r.Cookie("dsh"); err != nil || cookie.Value != "session" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var request struct {
			Endpoint string            `json:"endpoint"`
			Payload  map[string]string `json:"payload"`
		}
		if r.Method != http.MethodPost || r.URL.Path != "/api/bot" || json.NewDecoder(r.Body).Decode(&request) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch request.Endpoint {
		case "state":
			fmt.Fprint(w, directory)
		case "send":
			if text := request.Payload["text"]; strings.Contains(text, "@") || strings.Contains(text, "任务原文") || !strings.Contains(text, "#/live/task-pool/tp_") {
				t.Errorf("invalid group notice: %s", text)
			}
			mu.Lock()
			targets = append(targets, request.Payload["sessionId"])
			mu.Unlock()
			fmt.Fprint(w, `{"ok":true,"value":true}`)
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(upstream.Close)
	h := testHub(t)
	now := time.Now()
	device, err := h.registerDevice(DeviceInfo{Name: "fixture", Hostname: "fixture", OS: "windows", Arch: "amd64", Role: "test", BootID: "pool-fixture"}, "0.2.2", "1", now)
	if err != nil {
		t.Fatal(err)
	}
	h.cfg.TaskPoolBotDeviceID, h.cfg.TaskPoolChiefBotID = device.Device.ID, "chief"
	h.harnessHTTP[device.Device.ID] = newHarnessProxy(upstream.Listener.Addr().(*net.TCPAddr).Port, upstream.URL+"/")
	if err := h.setHarnessURL(device.Device.ID, upstream.URL+"/?token=fixture", now); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	h.registerHTTP(mux)
	call := func(method, path, token, body string, expected int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != expected {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body)
		}
		return w
	}
	created := call("POST", "/api/task-pool", h.adminToken, `{"request":"任务原文 @Builder"}`, http.StatusCreated)
	var result struct{ Item poolItem }
	if err := json.Unmarshal(created.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	var credential struct{ Credential string }
	provision := call("POST", "/api/task-pool/config/bot-credentials/chief", h.adminToken, "", http.StatusCreated)
	if err := json.Unmarshal(provision.Body.Bytes(), &credential); err != nil || credential.Credential == "" {
		t.Fatalf("provision: %v", err)
	}
	ctx := context.Background()
	flush := func(target, purpose string) {
		t.Helper()
		delivery, err := h.claimPoolDelivery(ctx, now)
		if err != nil || delivery.Target != target || delivery.Purpose != purpose {
			t.Fatalf("claim: %+v %v", delivery, err)
		}
		status, code := h.sendPoolDelivery(ctx, delivery)
		if status != "queued" || code != "" {
			t.Fatalf("result: %s %s", status, code)
		}
		if err := h.settlePoolDelivery(ctx, delivery.ID, status, code, now); err != nil {
			t.Fatal(err)
		}
	}
	flush("chief", "assign_requested")
	list := call("POST", "/api/task-pool/mcp", credential.Credential, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_unassigned"}}`, http.StatusOK)
	if !strings.Contains(list.Body.String(), result.Item.ID) {
		t.Fatalf("missing task in chief list: %s", list.Body)
	}
	assign := fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"assign_task","arguments":{"task_id":%q,"expected_revision":1,"assignee_kind":"group","assignee_id":"team"}}}`, result.Item.ID)
	call("POST", "/api/task-pool/mcp", credential.Credential, assign, http.StatusOK)
	flush("team", "assigned")
	call("POST", "/api/task-pool/mcp", credential.Credential, assign, http.StatusConflict)
	mu.Lock()
	defer mu.Unlock()
	if fmt.Sprint(targets) != "[chief team]" {
		t.Fatalf("targets: %v", targets)
	}
}
