package hub

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// 桌面通知开关与免打扰配置。默认开启通知、22:00–08:00 静音。
type NotificationSettings struct {
	TaskPoolEnabled       bool `json:"task_pool_enabled"`
	BotEventsEnabled      bool `json:"bot_events_enabled"`
	TurnEventsEnabled     bool `json:"turn_events_enabled"`
	GroupDecisionsEnabled bool `json:"group_decisions_enabled"`
	ApprovalBypass        bool `json:"approval_bypass"`

	Enabled           bool   `json:"enabled"`
	QuietHoursEnabled bool   `json:"quiet_hours_enabled"`
	QuietStart        string `json:"quiet_start"`
	QuietEnd          string `json:"quiet_end"`
}

func defaultNotificationSettings() NotificationSettings {
	return NotificationSettings{
		Enabled:               true,
		TaskPoolEnabled:       true,
		BotEventsEnabled:      true,
		TurnEventsEnabled:     true,
		GroupDecisionsEnabled: true,

		QuietHoursEnabled: true,
		QuietStart:        "22:00",
		QuietEnd:          "08:00",
	}
}

// TurnNotification 是一次「DSH 回合结束」的通知，只用于当场投递系统通知。
// 不落库：历史记录没有使用价值，用户明确要求不保留。
// ID 是进程内自增序号，仅用来区分同一次运行里的多条系统通知（并作为点击回调的键）。
type TurnNotification struct {
	Kind     string `json:"kind"`
	DedupKey string `json:"dedup_key"`
	ItemID   string `json:"item_id,omitempty"`

	ID           int64  `json:"id"`
	At           string `json:"at"`
	DeviceID     string `json:"device_id"`
	DeviceName   string `json:"device_name"`
	SessionID    string `json:"session_id"`
	SessionTitle string `json:"session_title"`
	DurationMS   int64  `json:"duration_ms"`
	Reason       string `json:"reason"`
	Excerpt      string `json:"excerpt"`
}

const (
	// 探测运行中会话的间隔；session/wait 才是主要事件源，轮询只负责发现新回合。
	turnWatcherListInterval = 3 * time.Second
	// 单次 session/wait 的上限，超时视为「还在跑」继续等。
	turnWatcherWaitTimeout = 120 * time.Second
	// 同一会话等待审批/提问的提醒冷却，避免长轮询刷屏。
	turnWatcherInputCooldown = 5 * time.Minute
	// 等待审批时空转的间隔。
	turnWatcherInputPause = 15 * time.Second
	// assistant 正文摘要长度（按 rune 计）。
	turnExcerptRunes = 110
	// 设备暂时不可用（Harness 未就绪）或单次轮询失败后的重试间隔。
	// 保持较短，避免 15 秒退避直接错过一个十秒级回合。
	turnWatcherDeviceBackoff = 2 * time.Second
	// 连续失败多少次后才重建设备代理（tsnet 刚启动时首个请求可能失败）。
	turnWatcherMaxFailures = 5
)

// InQuietHours 判断 now 是否落在免打扰窗口内。窗口跨午夜时按两段处理；
// 起止相同视为不静音，配置非法时也不静音（宁可多通知，不少通知）。
func InQuietHours(now time.Time, settings NotificationSettings) bool {
	if !settings.QuietHoursEnabled {
		return false
	}
	start, okStart := parseClockMinutes(settings.QuietStart)
	end, okEnd := parseClockMinutes(settings.QuietEnd)
	if !okStart || !okEnd || start == end {
		return false
	}
	current := now.Hour()*60 + now.Minute()
	if start < end {
		return current >= start && current < end
	}
	return current >= start || current < end
}

func parseClockMinutes(value string) (int, bool) {
	parts := strings.Split(strings.TrimSpace(value), ":")
	if len(parts) != 2 {
		return 0, false
	}
	hour, errHour := parseIntStrict(parts[0])
	minute, errMinute := parseIntStrict(parts[1])
	if errHour != nil || errMinute != nil || hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return 0, false
	}
	return hour*60 + minute, true
}

func parseIntStrict(value string) (int, error) {
	if value == "" || len(value) > 2 {
		return 0, errors.New("invalid number")
	}
	result := 0
	for _, r := range value {
		if r < '0' || r > '9' {
			return 0, errors.New("invalid number")
		}
		result = result*10 + int(r-'0')
	}
	return result, nil
}

func (h *Hub) notificationSettings() (NotificationSettings, error) {
	settings := defaultNotificationSettings()
	var enabled, quiet int
	err := h.db.QueryRow(
		`SELECT enabled, quiet_hours_enabled, quiet_start, quiet_end, task_pool_enabled, bot_events_enabled, turn_events_enabled, group_decisions_enabled, approval_bypass FROM notification_settings WHERE id = 1`,
	).Scan(&enabled, &quiet, &settings.QuietStart, &settings.QuietEnd, &settings.TaskPoolEnabled, &settings.BotEventsEnabled, &settings.TurnEventsEnabled, &settings.GroupDecisionsEnabled, &settings.ApprovalBypass)
	if errors.Is(err, sql.ErrNoRows) {
		return settings, nil
	}
	if err != nil {
		return settings, err
	}
	settings.Enabled = enabled == 1
	settings.QuietHoursEnabled = quiet == 1
	return settings, nil
}

func (h *Hub) saveNotificationSettings(settings NotificationSettings) error {
	_, err := h.db.Exec(
		`INSERT INTO notification_settings (id, enabled, quiet_hours_enabled, quiet_start, quiet_end, task_pool_enabled, bot_events_enabled, turn_events_enabled, group_decisions_enabled, approval_bypass, updated_at)
		 VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   enabled = excluded.enabled,
		   quiet_hours_enabled = excluded.quiet_hours_enabled,
		   quiet_start = excluded.quiet_start,
		   quiet_end = excluded.quiet_end,
		   task_pool_enabled = excluded.task_pool_enabled,
		   bot_events_enabled = excluded.bot_events_enabled,
		   turn_events_enabled = excluded.turn_events_enabled,
		   group_decisions_enabled = excluded.group_decisions_enabled,
		   approval_bypass = excluded.approval_bypass,
		   updated_at = excluded.updated_at`,
		boolToInt(settings.Enabled), boolToInt(settings.QuietHoursEnabled),
		settings.QuietStart, settings.QuietEnd, settings.TaskPoolEnabled, settings.BotEventsEnabled, settings.TurnEventsEnabled, settings.GroupDecisionsEnabled, settings.ApprovalBypass, nowISO(time.Now()),
	)
	return err
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// ---------------------------------------------------------------------------
// 回合 watcher：按设备管理，纯 HTTP 轮询 DSH 的 session/list + session/wait。
// 不订阅 $events，因此不会参与审批/提问的 waterfall 仲裁。
// ---------------------------------------------------------------------------

type turnWatcher struct {
	hub     *Hub
	mu      sync.Mutex
	sink    func(TurnNotification)
	devices map[string]context.CancelFunc
	wg      sync.WaitGroup

	client        *http.Client
	listInterval  time.Duration
	waitTimeout   time.Duration
	inputCooldown time.Duration
	inputPause    time.Duration
	now           func() time.Time

	// 通知不再落库，ID 只在进程内递增，用来标识一次系统通知并作为点击回调的键。
	notifySeq atomic.Int64
}

func newTurnWatcher(hub *Hub) *turnWatcher {
	w := &turnWatcher{
		hub:           hub,
		devices:       map[string]context.CancelFunc{},
		client:        &http.Client{Timeout: turnWatcherWaitTimeout + 30*time.Second},
		listInterval:  turnWatcherListInterval,
		waitTimeout:   turnWatcherWaitTimeout,
		inputCooldown: turnWatcherInputCooldown,
		inputPause:    turnWatcherInputPause,
		now:           time.Now,
	}
	// 用启动时刻做起点，这样重启后新通知的 ID 不会和上一次运行撞号
	// （系统通知按 ID 去重，撞号会把旧通知覆盖掉）。
	w.notifySeq.Store(time.Now().Unix())
	return w
}

// SetTurnNotificationSink 注册通知出口（桌面 App 用它接 Wails 通知）。
func (h *Hub) SetTurnNotificationSink(sink func(TurnNotification)) {
	if h.turnWatcher == nil {
		return
	}
	h.turnWatcher.mu.Lock()
	h.turnWatcher.sink = sink
	h.turnWatcher.mu.Unlock()
}

// StartTurnWatcher 为给定设备启动回合 watcher；重复调用会按新列表增删设备。
func (h *Hub) StartTurnWatcher(deviceIDs []string) {
	if h.turnWatcher == nil {
		return
	}
	wanted := map[string]struct{}{}
	for _, id := range deviceIDs {
		id = strings.TrimSpace(id)
		if id != "" {
			wanted[id] = struct{}{}
		}
	}
	h.turnWatcher.mu.Lock()
	defer h.turnWatcher.mu.Unlock()
	for id, cancel := range h.turnWatcher.devices {
		if _, keep := wanted[id]; keep {
			continue
		}
		cancel()
		delete(h.turnWatcher.devices, id)
	}
	for id := range wanted {
		if _, running := h.turnWatcher.devices[id]; running {
			continue
		}
		ctx, cancel := context.WithCancel(context.Background())
		h.turnWatcher.devices[id] = cancel
		h.turnWatcher.wg.Add(1)
		go func(deviceID string) {
			defer h.turnWatcher.wg.Done()
			h.turnWatcher.runDevice(ctx, deviceID)
		}(id)
	}
}

// StopTurnWatcher 停掉所有设备的 watcher 并等待退出。
func (h *Hub) StopTurnWatcher() {
	if h.turnWatcher == nil {
		return
	}
	h.turnWatcher.mu.Lock()
	for id, cancel := range h.turnWatcher.devices {
		cancel()
		delete(h.turnWatcher.devices, id)
	}
	h.turnWatcher.mu.Unlock()
	h.turnWatcher.wg.Wait()
}

func (w *turnWatcher) runDevice(ctx context.Context, deviceID string) {
	var watch *deviceWatch
	var lastPollError time.Time
	failures := 0
	for {
		if ctx.Err() != nil {
			return
		}
		if watch == nil {
			created, err := w.hub.newDeviceWatch(deviceID)
			if err != nil {
				// Harness 还没注册 URL / 端口未就绪时静默重试，由下一次循环再试。
				if !sleepContext(ctx, turnWatcherDeviceBackoff*2) {
					return
				}
				continue
			}
			watch = created
			failures = 0
			log.Printf("通知 watcher 已连接设备 %s（harness %s）", deviceID, watch.base)
		}
		watch.pollNotices(ctx)
		if err := watch.poll(ctx, w); err != nil {
			failures++
			// DSH 短暂不可用时只记一次，避免刷日志。
			if w.now().Sub(lastPollError) > 5*time.Minute {
				log.Printf("通知 watcher 轮询失败（%s）: %v", deviceID, err)
				lastPollError = w.now()
			}
			// 先短间隔重试；连续失败才重建代理，避免频繁重建端口和登录 cookie。
			if failures >= turnWatcherMaxFailures {
				watch.close()
				watch = nil
				failures = 0
				// Edge 重启 DSH 会换端口或换会话 cookie：丢掉旧代理，下一轮按最新 Harness URL 重建。
				w.hub.dropHarnessProxy(deviceID)
			}
			if !sleepContext(ctx, turnWatcherDeviceBackoff) {
				return
			}
			continue
		}
		failures = 0
		if !sleepContext(ctx, w.listInterval) {
			return
		}
	}
}

func sleepContext(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// deviceWatch 持有的是一台设备 DSH 的本地反代地址与登录 cookie。
type deviceWatch struct {
	tracker  noticeTracker
	hub      *Hub
	deviceID string
	base     string
	jar      http.CookieJar
	client   *http.Client

	mu      sync.Mutex
	active  map[string]*activeTurn
	waiting map[string]bool
	closed  bool
}

type activeTurn struct {
	startedAt       time.Time
	lastInputNotice time.Time
}

func (h *Hub) newDeviceWatch(deviceID string) (*deviceWatch, error) {
	localURL, entry, err := h.ensureHarnessProxyEntry(deviceID)
	if err != nil {
		return nil, err
	}
	parsed, err := url.Parse(localURL)
	if err != nil || parsed.Host == "" {
		return nil, fmt.Errorf("harness 本地代理地址无效")
	}
	jar, err := entry.jar(localURL, nil)
	if err != nil {
		return nil, err
	}
	return &deviceWatch{
		hub:      h,
		deviceID: deviceID,
		base:     parsed.Scheme + "://" + parsed.Host,
		jar:      jar,
		client:   &http.Client{Timeout: turnWatcherWaitTimeout + 30*time.Second},
		active:   map[string]*activeTurn{},
		waiting:  map[string]bool{},
	}, nil
}

func (d *deviceWatch) close() {
	d.mu.Lock()
	d.closed = true
	d.mu.Unlock()
}

type dshSessionItem struct {
	SessionID   string `json:"sessionId"`
	UpdatedAt   *int64 `json:"updatedAt"`
	Running     bool   `json:"running"`
	Blank       bool   `json:"blank"`
	CWD         string `json:"cwd"`
	Projections struct {
		Values struct {
			Title string `json:"title"`
		} `json:"values"`
	} `json:"projections"`
}

func (d *deviceWatch) listSessions(ctx context.Context) ([]dshSessionItem, error) {
	var value struct {
		Items []dshSessionItem `json:"items"`
	}
	if err := d.call(ctx, "session/list", map[string]any{"_request": map[string]any{}}, &value); err != nil {
		return nil, err
	}
	return value.Items, nil
}

type dshWaitValue struct {
	Reason string `json:"reason"`
	State  struct {
		Running bool `json:"running"`
		Cursor  int  `json:"cursor"`
	} `json:"state"`
	Output struct {
		Records []dshWaitRecord `json:"records"`
	} `json:"output"`
}

type dshWaitRecord struct {
	Type  string `json:"type"`
	Event struct {
		Type string `json:"type"`
		Data struct {
			Message struct {
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"message"`
		} `json:"data"`
	} `json:"event"`
}

func (d *deviceWatch) waitSession(ctx context.Context, sessionID string, timeoutMs int) (dshWaitValue, error) {
	var value dshWaitValue
	args := map[string]any{"request": map[string]any{
		"sessionId": sessionID,
		"timeoutMs": timeoutMs,
	}}
	if err := d.call(ctx, "session/wait", args, &value); err != nil {
		return value, err
	}
	return value, nil
}

func (d *deviceWatch) call(ctx context.Context, endpoint string, args any, out any) error {
	body, err := json.Marshal(map[string]any{
		"type":    "client-request",
		"rpcId":   "meerkat-notify-" + randomHex(8),
		"method":  endpoint,
		"payload": map[string]any{"args": args},
	})
	if err != nil {
		return err
	}
	target := d.base + "/api/" + endpoint
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	request.Header.Set("content-type", "application/json")
	if d.jar != nil {
		for _, cookie := range d.jar.Cookies(request.URL) {
			request.AddCookie(cookie)
		}
	}
	response, err := d.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	var envelope struct {
		Result struct {
			OK    bool            `json:"ok"`
			Value json.RawMessage `json:"value"`
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		} `json:"result"`
	}
	raw, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if readErr != nil {
		return fmt.Errorf("读取 %s 响应失败: %w", endpoint, readErr)
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		snippet := strings.TrimSpace(string(raw))
		if len(snippet) > 160 {
			snippet = snippet[:160]
		}
		return fmt.Errorf("解码 %s 响应失败（HTTP %d）: %w: %s", endpoint, response.StatusCode, err, snippet)
	}
	if !envelope.Result.OK {
		if envelope.Result.Error.Message != "" {
			return fmt.Errorf("%s 失败: %s", endpoint, envelope.Result.Error.Message)
		}
		return fmt.Errorf("%s 失败 (HTTP %d)", endpoint, response.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(envelope.Result.Value, out)
}

// poll 发现运行中的会话并为每个会话启动一次长轮询。
func (d *deviceWatch) poll(ctx context.Context, w *turnWatcher) error {
	settings, err := d.hub.notificationSettings()
	if err != nil {
		return err
	}
	if !settings.Enabled {
		return nil
	}
	sessions, err := d.listSessions(ctx)
	if err != nil {
		return err
	}
	for _, item := range sessions {
		if !item.Running || item.Blank || d.tracker.hidden[item.SessionID] {
			continue
		}
		d.mu.Lock()
		if d.closed {
			d.mu.Unlock()
			return nil
		}
		turn, seen := d.active[item.SessionID]
		if !seen {
			turn = &activeTurn{startedAt: w.now()}
			d.active[item.SessionID] = turn
		}
		if d.waiting[item.SessionID] {
			d.mu.Unlock()
			continue
		}
		d.waiting[item.SessionID] = true
		d.mu.Unlock()
		go d.watchSession(ctx, w, item, turn)
	}
	return nil
}

func (d *deviceWatch) watchSession(ctx context.Context, w *turnWatcher, item dshSessionItem, turn *activeTurn) {
	defer func() {
		d.mu.Lock()
		d.waiting[item.SessionID] = false
		d.mu.Unlock()
	}()
	failures := 0
	for {
		if ctx.Err() != nil {
			return
		}
		settings, err := d.hub.notificationSettings()
		if err != nil || !settings.Enabled {
			d.mu.Lock()
			delete(d.active, item.SessionID)
			d.mu.Unlock()
			return
		}
		value, err := d.waitSession(ctx, item.SessionID, int(w.waitTimeout/time.Millisecond))
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			failures++
			if failures >= 3 {
				// 会话被删除/归档或 DSH 抖动：放弃这一轮，让下一次 poll 重新发现。
				d.mu.Lock()
				delete(d.active, item.SessionID)
				d.mu.Unlock()
				return
			}
			if !sleepContext(ctx, 5*time.Second) {
				return
			}
			continue
		}
		failures = 0
		switch value.Reason {
		case "idle":
			d.finish(ctx, w, item, turn, "idle", value)
			return
		case "input-required":
			d.mu.Lock()
			shouldNotify := w.now().Sub(turn.lastInputNotice) >= w.inputCooldown
			if shouldNotify {
				turn.lastInputNotice = w.now()
			}
			d.mu.Unlock()
			if shouldNotify {
				d.finish(ctx, w, item, turn, "input-required", value)
			}
			if !sleepContext(ctx, w.inputPause) {
				return
			}
		default: // timeout：回合还在跑，继续等
			continue
		}
	}
}

func (d *deviceWatch) finish(ctx context.Context, w *turnWatcher, item dshSessionItem, turn *activeTurn, reason string, value dshWaitValue) {
	d.mu.Lock()
	delete(d.active, item.SessionID)
	d.mu.Unlock()

	settings, err := d.hub.notificationSettings()
	if err != nil || !settings.Enabled {
		return
	}
	title := strings.TrimSpace(item.Projections.Values.Title)
	if title == "" {
		title = "会话 " + shortSessionID(item.SessionID)
	}
	deviceName := d.hub.deviceDisplayName(d.deviceID)
	record := TurnNotification{
		At:           nowISO(w.now()),
		DeviceID:     d.deviceID,
		DeviceName:   deviceName,
		SessionID:    item.SessionID,
		SessionTitle: title,
		DurationMS:   w.now().Sub(turn.startedAt).Milliseconds(),
		Reason:       reason,
		Excerpt:      assistantExcerpt(value),
	}
	// 不落库：直接把通知投递出去。ID 只是进程内序号，供系统通知与点击回调配对。
	record.ID = w.nextNotificationID()
	record.Kind = "turn." + strings.ReplaceAll(reason, "-", "_")
	record.DedupKey = fmt.Sprintf("dsh:turn:%s:%s:%d", d.deviceID, item.SessionID, value.State.Cursor)
	w.hub.publishNotification(record, turnWatcherInputCooldown)
	w.deliver(record)
}

func (w *turnWatcher) nextNotificationID() int64 {
	return w.notifySeq.Add(1)
}

func (w *turnWatcher) deliver(record TurnNotification) {
	w.mu.Lock()
	sink := w.sink
	w.mu.Unlock()
	if sink != nil {
		sink(record)
	}
}

func (h *Hub) deviceDisplayName(deviceID string) string {
	device, err := h.getDevice(deviceID)
	if err != nil || device == nil || strings.TrimSpace(device.Name) == "" {
		return deviceID
	}
	return device.Name
}

func shortSessionID(sessionID string) string {
	trimmed := strings.TrimPrefix(sessionID, "session-")
	if len(trimmed) > 8 {
		trimmed = trimmed[:8]
	}
	if trimmed == "" {
		return "未知"
	}
	return trimmed
}

// assistantExcerpt 取出本回合最后一条 assistant 文本并截断到 turnExcerptRunes。
func assistantExcerpt(value dshWaitValue) string {
	var parts []string
	for index := len(value.Output.Records) - 1; index >= 0; index-- {
		record := value.Output.Records[index]
		if record.Type != "event" || record.Event.Type != "assistant/message" {
			continue
		}
		for _, part := range record.Event.Data.Message.Content {
			if part.Type == "text" && strings.TrimSpace(part.Text) != "" {
				parts = append(parts, part.Text)
			}
		}
		if len(parts) > 0 {
			break
		}
	}
	text := collapseWhitespace(strings.Join(parts, " "))
	return truncateRunes(text, turnExcerptRunes)
}

func collapseWhitespace(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 || utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit]) + "…"
}

// ---------------------------------------------------------------------------
// HTTP
// ---------------------------------------------------------------------------

func (h *Hub) handleNotificationSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := h.notificationSettings()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "读取通知设置失败")
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func (h *Hub) handleUpdateNotificationSettings(w http.ResponseWriter, r *http.Request) {
	body, err := h.notificationSettings()
	if err != nil {
		writeError(w, 500, "internal_error", "读取通知设置失败")
		return
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid json")
		return
	}
	body.QuietStart = strings.TrimSpace(body.QuietStart)
	body.QuietEnd = strings.TrimSpace(body.QuietEnd)
	if _, ok := parseClockMinutes(body.QuietStart); !ok {
		writeError(w, http.StatusBadRequest, "invalid_quiet_hours", "免打扰开始时间需为 HH:MM")
		return
	}
	if _, ok := parseClockMinutes(body.QuietEnd); !ok {
		writeError(w, http.StatusBadRequest, "invalid_quiet_hours", "免打扰结束时间需为 HH:MM")
		return
	}
	if err := h.saveNotificationSettings(body); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "保存通知设置失败")
		return
	}
	settings, err := h.notificationSettings()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "读取通知设置失败")
		return
	}
	h.notifications.signal()
	writeJSON(w, http.StatusOK, settings)
}

// NotificationSettings 供桌面端在投递前复用同一份配置。
func (h *Hub) NotificationSettings() (NotificationSettings, error) {
	return h.notificationSettings()
}
