package hub

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestStreamReplayAndSettings(t *testing.T) {
	h := testHub(t)
	server := httptest.NewServer(h.server.Handler)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	open := func(last string) *http.Response {
		req, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/api/notifications/stream", nil)
		req.Header.Set("Authorization", "Bearer "+h.AdminToken())
		req.Header.Set("Last-Event-ID", last)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != 200 {
			t.Fatal(res.Status)
		}
		return res
	}
	frame := func(reader *bufio.Reader) (id, kind, data string) {
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			field, v, _ := strings.Cut(strings.TrimSpace(line), ": ")
			switch field {
			case "id":
				id = v
			case "event":
				kind = v
			case "data":
				data = v
			case "":
				return
			}
		}
	}
	first := open("")
	last, kind, _ := frame(bufio.NewReader(first.Body))
	first.Body.Close()
	if last == "" || kind != "settings" {
		t.Fatal(last, kind)
	}
	for range 2 {
		h.notifyPool("review_ready", "item", "验收", "item:2")
	}
	second := open(last)
	defer second.Body.Close()
	reader := bufio.NewReader(second.Body)
	frame(reader)
	_, kind, data := frame(reader)
	var n TurnNotification
	if json.Unmarshal([]byte(data), &n) != nil || kind != "notification" || n.ItemID != "item" {
		t.Fatalf("replay: %s %s", kind, data)
	}
	s := defaultNotificationSettings()
	s.TaskPoolEnabled = false
	if err := h.saveNotificationSettings(s); err != nil {
		t.Fatal(err)
	}
	h.notifications.signal()
	_, kind, data = frame(reader)
	if kind != "settings" || !strings.Contains(data, `"task_pool_enabled":false`) {
		t.Fatal(kind, data)
	}
	h.notifyPool("blocked", "item", "受阻", "item:3")
	if len(h.notifications.ring) != 1 {
		t.Fatal("duplicate or disabled notification")
	}
}
func TestNotificationDecisionFallback(t *testing.T) {
	t.Setenv("MEERKAT_NOTIFY_LLM_URL", "")
	for _, tc := range []struct {
		text               string
		explicit, decision bool
	}{{"```@老板``` 我先查一下", false, false}, {"@老板", true, false}, {"请确认是否开工", false, true}, {"已归档", false, false}, {"[待验收]", true, false}} {
		if explicitUser(tc.text) != tc.explicit {
			t.Errorf("mention: %q", tc.text)
		}
		got, source := classifyDecision(context.Background(), tc.text, nil)
		if got != tc.decision || source != "rule_fallback" {
			t.Errorf("decision: %q", tc.text)
		}
	}
}
