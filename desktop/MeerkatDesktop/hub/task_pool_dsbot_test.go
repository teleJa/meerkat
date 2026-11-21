package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
