package hub

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestNoticeRestartAndGroupDecision(t *testing.T) {
	t.Setenv("MEERKAT_NOTIFY_LLM_URL", "")
	var mu sync.Mutex
	batch := noticeBatch{Boot: "first"}
	var latest botNotice
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			http.SetCookie(w, &http.Cookie{Name: "fixture", Value: "ok", Path: "/"})
			return
		}
		var req struct{ Endpoint string }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		var value any
		switch req.Endpoint {
		case "notices":
			value = batch
		case "state":
			value = noticeState{MainBotID: "chief", Rooms: []teamRoom{{ID: "room", Admin: "chief", SessionID: "session"}}}
		case "room-chat":
			value = map[string]any{"items": []botNotice{latest}}
		default:
			t.Errorf("unexpected operation %s", req.Endpoint)
		}
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "value": value})
	}))
	defer upstream.Close()
	h := testHub(t)
	now := time.Now()
	device, err := h.registerDevice(DeviceInfo{Name: "fixture", Hostname: "fixture", OS: "windows", Arch: "amd64", Role: "test", BootID: "device"}, "0.2.5", "1", now)
	if err != nil {
		t.Fatal(err)
	}
	id := device.Device.ID
	h.harnessHTTP[id] = newHarnessProxy(upstream.Listener.Addr().(*net.TCPAddr).Port, upstream.URL+"/")
	if err := h.setHarnessURL(id, upstream.URL+"/?token=fixture", now); err != nil {
		t.Fatal(err)
	}
	d := deviceWatch{hub: h, deviceID: id}
	poll := func() { d.pollNotices(context.Background()) }
	poll()
	deliver := func(boot string, seq int, text string) {
		mu.Lock()
		latest = botNotice{Seq: seq, Kind: "group", RoomID: "room", SessionID: "session", BotID: "chief", Text: text}
		batch = noticeBatch{Boot: boot, Seq: seq, Notices: []botNotice{latest}}
		mu.Unlock()
		poll()
	}
	flush := func() {
		t.Helper()
		p, ok := d.tracker.pending["room"]
		if !ok {
			t.Fatal("group was not scheduled")
		}
		p.due = time.Now()
		d.tracker.pending["room"] = p
		poll()
	}
	deliver("first", 1, "编译已经完成，正在跑回归。")
	flush()
	if len(h.notifications.ring) != 0 {
		t.Fatal("ordinary progress notified")
	}
	deliver("first", 2, "两种方案都可行，需要你决定使用哪种。")
	flush()
	if len(h.notifications.ring) != 1 || h.notifications.ring[0].Kind != "bot.group_decision" {
		t.Fatal("implicit decision not delivered")
	}
	deliver("first", 3, "请确认后续处理。")
	deliver("second", 1, "@老板 旧消息")
	poll()
	if d.tracker.boot != "second" || d.tracker.seq != 1 || len(d.tracker.pending) != 0 || len(h.notifications.ring) != 1 {
		t.Fatal("new boot replayed history or stale pending work")
	}
}
