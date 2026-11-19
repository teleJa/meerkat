package hub

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestInQuietHours(t *testing.T) {
	settings := defaultNotificationSettings()
	cases := []struct {
		name string
		at   time.Time
		want bool
	}{
		{"深夜", time.Date(2026, 11, 15, 23, 30, 0, 0, time.Local), true},
		{"凌晨", time.Date(2026, 11, 15, 3, 0, 0, 0, time.Local), true},
		{"早上边界内", time.Date(2026, 11, 15, 7, 59, 0, 0, time.Local), true},
		{"早上边界外", time.Date(2026, 11, 15, 8, 0, 0, 0, time.Local), false},
		{"中午", time.Date(2026, 11, 15, 12, 0, 0, 0, time.Local), false},
		{"入夜前", time.Date(2026, 11, 15, 21, 59, 0, 0, time.Local), false},
		{"入夜边界", time.Date(2026, 11, 15, 22, 0, 0, 0, time.Local), true},
	}
	for _, tc := range cases {
		if got := InQuietHours(tc.at, settings); got != tc.want {
			t.Errorf("%s: InQuietHours=%v want %v", tc.name, got, tc.want)
		}
	}

	off := settings
	off.QuietHoursEnabled = false
	if InQuietHours(time.Date(2026, 11, 15, 23, 0, 0, 0, time.Local), off) {
		t.Error("免打扰关闭时不应静音")
	}

	same := settings
	same.QuietStart = "09:00"
	same.QuietEnd = "09:00"
	if InQuietHours(time.Date(2026, 11, 15, 9, 0, 0, 0, time.Local), same) {
		t.Error("起止相同视为不静音")
	}

	invalid := settings
	invalid.QuietStart = "25:00"
	if InQuietHours(time.Date(2026, 11, 15, 23, 0, 0, 0, time.Local), invalid) {
		t.Error("非法配置不应静音")
	}

	day := settings
	day.QuietStart = "13:00"
	day.QuietEnd = "14:00"
	if !InQuietHours(time.Date(2026, 11, 15, 13, 30, 0, 0, time.Local), day) {
		t.Error("同日窗口应静音")
	}
}

func TestNotificationSettingsDefaultsAndSave(t *testing.T) {
	h := testHub(t)
	settings, err := h.notificationSettings()
	if err != nil {
		t.Fatal(err)
	}
	if !settings.Enabled || !settings.QuietHoursEnabled || settings.QuietStart != "22:00" || settings.QuietEnd != "08:00" {
		t.Fatalf("默认设置不符: %+v", settings)
	}

	updated := NotificationSettings{Enabled: false, QuietHoursEnabled: true, QuietStart: "23:30", QuietEnd: "07:15"}
	if err := h.saveNotificationSettings(updated); err != nil {
		t.Fatal(err)
	}
	loaded, err := h.notificationSettings()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Enabled || loaded.QuietStart != "23:30" || loaded.QuietEnd != "07:15" {
		t.Fatalf("保存后读取不一致: %+v", loaded)
	}
}

// 回合通知不再落库：用户要求不保留历史记录，所以这张表在 openDB 里已经被 DROP。
func TestTurnNotificationNotPersisted(t *testing.T) {
	h := testHub(t)
	var count int
	// 表应已被删除——查 sqlite_master 而不是 sqlite_schema，兼容旧版本。
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='turn_notifications'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("turn_notifications 表应已删除")
	}
}

func TestOpenDBDropsLegacyTurnNotifications(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.db")

	// 先手工造一个带历史记录的旧库，模拟升级前的用户数据。
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`CREATE TABLE turn_notifications (
		id INTEGER PRIMARY KEY AUTOINCREMENT, at TEXT NOT NULL, device_id TEXT NOT NULL,
		device_name TEXT NOT NULL, session_id TEXT NOT NULL, session_title TEXT NOT NULL,
		duration_ms INTEGER NOT NULL, reason TEXT NOT NULL, excerpt TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`INSERT INTO turn_notifications
		(at, device_id, device_name, session_id, session_title, duration_ms, reason, excerpt)
		VALUES ('2026-11-16T10:00:00Z','dev_old','Mac','session-old','旧记录',1500,'idle','旧摘要')`); err != nil {
		t.Fatal(err)
	}
	_ = legacy.Close()

	db, err := openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='turn_notifications'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("旧库里的 turn_notifications 应被 DROP")
	}
}

func TestAssistantExcerptAndTruncate(t *testing.T) {
	value := dshWaitValue{}
	value.Output.Records = []dshWaitRecord{
		{Type: "event", Event: struct {
			Type string `json:"type"`
			Data struct {
				Message struct {
					Content []struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"content"`
				} `json:"message"`
			} `json:"data"`
		}{Type: "user/message"}},
	}
	// 直接构造 assistant/message 记录，验证取最后一条文本并合并换行。
	var record dshWaitRecord
	record.Type = "event"
	record.Event.Type = "assistant/message"
	record.Event.Data.Message.Content = []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}{
		{Type: "text", Text: "第一段\n\n  第二段"},
		{Type: "image", Text: ""},
	}
	value.Output.Records = append(value.Output.Records, record)
	if got := assistantExcerpt(value); got != "第一段 第二段" {
		t.Fatalf("excerpt=%q", got)
	}

	long := strings.Repeat("深", turnExcerptRunes+5)
	if got := truncateRunes(long, turnExcerptRunes); !strings.HasSuffix(got, "…") || len([]rune(got)) != turnExcerptRunes+1 {
		t.Fatalf("truncate 结果不符: %d", len([]rune(got)))
	}
	if got := truncateRunes("短文本", turnExcerptRunes); got != "短文本" {
		t.Fatalf("短文本不应截断: %q", got)
	}
}

// fakeDSH 模拟 DSH 的 /api/session/list 与 /api/session/wait。
type fakeDSH struct {
	mu        sync.Mutex
	lists     int
	waits     int
	reason    string
	excerpt   string
	lastArgs  map[string]any
	onWait    func()
	listDelay time.Duration
}

func (f *fakeDSH) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/session/list", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.lists++
		delay := f.listDelay
		f.mu.Unlock()
		if delay > 0 {
			time.Sleep(delay)
		}
		writeRPCValue(w, map[string]any{"items": []map[string]any{{
			"sessionId": "session-test01", "running": true, "blank": false,
			"projections": map[string]any{"values": map[string]any{"title": "研究"}},
		}}})
	})
	mux.HandleFunc("POST /api/session/wait", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Payload struct {
				Args struct {
					Request struct {
						SessionID string `json:"sessionId"`
					} `json:"request"`
				} `json:"args"`
			} `json:"payload"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.waits++
		f.lastArgs = map[string]any{"sessionId": body.Payload.Args.Request.SessionID}
		reason := f.reason
		excerpt := f.excerpt
		hook := f.onWait
		f.mu.Unlock()
		if hook != nil {
			hook()
		}
		records := []any{}
		if excerpt != "" {
			records = append(records, map[string]any{
				"type": "event",
				"event": map[string]any{
					"type": "assistant/message",
					"data": map[string]any{"message": map[string]any{"content": []map[string]any{
						{"type": "text", "text": excerpt},
					}}},
				},
			})
		}
		writeRPCValue(w, map[string]any{
			"reason": reason,
			"state":  map[string]any{"running": reason != "idle", "cursor": 12},
			"output": map[string]any{"records": records},
		})
	})
	return mux
}

func writeRPCValue(w http.ResponseWriter, value any) {
	w.Header().Set("content-type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type":   "server-response",
		"rpcId":  "test",
		"result": map[string]any{"ok": true, "value": value},
	})
}

func TestTurnWatcherEmitsNotificationOnIdle(t *testing.T) {
	h := testHub(t)
	fake := &fakeDSH{reason: "idle", excerpt: "收到。第一段很长的说明"}
	server := httptest.NewServer(fake.handler())
	defer server.Close()

	watcher := newTurnWatcher(h)
	watcher.listInterval = 10 * time.Millisecond
	watcher.waitTimeout = time.Second
	events := make(chan TurnNotification, 4)
	watcher.sink = func(record TurnNotification) { events <- record }

	watch := &deviceWatch{
		hub: h, deviceID: "dev_test", base: server.URL,
		client: &http.Client{Timeout: 5 * time.Second},
		active: map[string]*activeTurn{}, waiting: map[string]bool{},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := watch.poll(ctx, watcher); err != nil {
		t.Fatal(err)
	}

	select {
	case record := <-events:
		if record.SessionTitle != "研究" {
			t.Fatalf("标题不符: %+v", record)
		}
		if record.DeviceID != "dev_test" || record.Reason != "idle" {
			t.Fatalf("记录不符: %+v", record)
		}
		if !strings.Contains(record.Excerpt, "收到") {
			t.Fatalf("摘要不符: %q", record.Excerpt)
		}
		if record.DurationMS < 0 {
			t.Fatalf("时长异常: %d", record.DurationMS)
		}
		if record.ID == 0 {
			t.Fatalf("通知应带一个进程内 ID（点击回调用），实际 %+v", record)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("等待通知超时")
	}

	// 通知仍然投递，但不再写库。
	var tableCount int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='turn_notifications'`).Scan(&tableCount); err != nil {
		t.Fatal(err)
	}
	if tableCount != 0 {
		t.Fatal("回合结束后不应再写 turn_notifications")
	}
}

func TestTurnWatcherSkipsWhenDisabled(t *testing.T) {
	h := testHub(t)
	if err := h.saveNotificationSettings(NotificationSettings{
		Enabled: false, QuietHoursEnabled: false, QuietStart: "22:00", QuietEnd: "08:00",
	}); err != nil {
		t.Fatal(err)
	}
	fake := &fakeDSH{reason: "idle", excerpt: "不应出现"}
	server := httptest.NewServer(fake.handler())
	defer server.Close()

	watcher := newTurnWatcher(h)
	watcher.sink = func(TurnNotification) { t.Error("关闭通知后不应投递") }
	watch := &deviceWatch{
		hub: h, deviceID: "dev_test", base: server.URL,
		client: &http.Client{Timeout: 5 * time.Second},
		active: map[string]*activeTurn{}, waiting: map[string]bool{},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := watch.poll(ctx, watcher); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	fake.mu.Lock()
	waits := fake.waits
	fake.mu.Unlock()
	if waits != 0 {
		t.Fatalf("关闭通知后不应发起 session/wait，实际 %d 次", waits)
	}
}

func TestTurnWatcherInputRequiredNotifiesOncePerCooldown(t *testing.T) {
	h := testHub(t)
	fake := &fakeDSH{reason: "input-required", excerpt: ""}
	server := httptest.NewServer(fake.handler())
	defer server.Close()

	watcher := newTurnWatcher(h)
	watcher.waitTimeout = 500 * time.Millisecond
	watcher.inputPause = 10 * time.Millisecond
	watcher.inputCooldown = time.Hour
	events := make(chan TurnNotification, 8)
	watcher.sink = func(record TurnNotification) { events <- record }

	watch := &deviceWatch{
		hub: h, deviceID: "dev_test", base: server.URL,
		client: &http.Client{Timeout: 5 * time.Second},
		active: map[string]*activeTurn{}, waiting: map[string]bool{},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := watch.poll(ctx, watcher); err != nil {
		t.Fatal(err)
	}
	select {
	case record := <-events:
		if record.Reason != "input-required" {
			t.Fatalf("reason=%q", record.Reason)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("等待 input-required 通知超时")
	}
	time.Sleep(150 * time.Millisecond)
	select {
	case extra := <-events:
		t.Fatalf("冷却期内不应重复通知: %+v", extra)
	default:
	}
}
