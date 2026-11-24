package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Readers snapshot the ring and its wake-up channel under the same lock. A slow reader never blocks a publisher.
type notificationBroker struct {
	mu      sync.Mutex
	ring    []TurnNotification
	last    map[string]time.Time
	seq     int64
	changed chan struct{}
	done    chan struct{}
}

func newNotificationBroker() *notificationBroker {
	return &notificationBroker{seq: time.Now().UnixMilli() * 1000, last: make(map[string]time.Time), changed: make(chan struct{}), done: make(chan struct{})}
}
func (b *notificationBroker) wakeLocked() { close(b.changed); b.changed = make(chan struct{}) }
func (b *notificationBroker) signal()     { b.mu.Lock(); defer b.mu.Unlock(); b.wakeLocked() }
func (s NotificationSettings) Allows(kind string) bool {
	if !s.Enabled {
		return false
	}
	switch {
	case strings.HasPrefix(kind, "pool."):
		return s.TaskPoolEnabled
	case kind == "bot.group_decision":
		return s.BotEventsEnabled && s.GroupDecisionsEnabled
	case strings.HasPrefix(kind, "bot."):
		return s.BotEventsEnabled
	case strings.HasPrefix(kind, "turn."):
		return s.TurnEventsEnabled
	}
	return false
}
func (h *Hub) publishNotification(n TurnNotification, cooldown time.Duration) {
	if h.notifications == nil {
		return
	}
	s, err := h.notificationSettings()
	if err != nil || !s.Allows(n.Kind) {
		return
	}
	b := h.notifications
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	for k, expiry := range b.last {
		if !now.Before(expiry) {
			delete(b.last, k)
		}
	}
	if _, seen := b.last[n.DedupKey]; seen {
		return
	}
	if n.SessionID != "" && (n.Kind == "turn.input_required" || n.Kind == "bot.approval" || n.Kind == "bot.action_required") {
		k := "input:" + n.DeviceID + ":" + n.SessionID
		if _, seen := b.last[k]; seen {
			return
		}
		b.last[k] = now.Add(30 * time.Second)
	}
	b.last[n.DedupKey] = now.Add(cooldown)
	b.seq++
	n.ID = b.seq
	// Rate-limit by conversation on the Hub. Distinct accepted messages need distinct client replay keys.
	if n.Kind == "bot.chief_dm" || n.Kind == "bot.chief_mention" || n.Kind == "bot.group_decision" {
		n.DedupKey += ":" + strconv.FormatInt(n.ID, 10)
	}
	if n.At == "" {
		n.At = nowISO(now)
	}
	if len(b.ring) == 64 {
		copy(b.ring, b.ring[1:])
		b.ring = b.ring[:63]
	}
	b.ring = append(b.ring, n)
	b.wakeLocked()
}
func (h *Hub) handleNotificationStream(w http.ResponseWriter, r *http.Request) {
	flush, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "stream unavailable", 500)
		return
	}
	b := h.notifications
	b.mu.Lock()
	cursor := b.seq
	b.mu.Unlock()
	if last, err := strconv.ParseInt(r.Header.Get("Last-Event-ID"), 10, 64); err == nil && last > 0 && last <= cursor {
		cursor = last
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()
	var previous *NotificationSettings
	for {
		b.mu.Lock()
		changed := b.changed
		events := append([]TurnNotification(nil), b.ring...)
		b.mu.Unlock()
		if _, ok := h.requireStreamAccess(r); !ok {
			return
		}
		settings, err := h.notificationSettings()
		if err != nil {
			return
		}
		if previous == nil || *previous != settings {
			data, _ := json.Marshal(settings)
			if _, err := fmt.Fprintf(w, "id: %d\nevent: settings\ndata: %s\n\n", cursor, data); err != nil {
				return
			}
			previous = &settings
		}
		for _, n := range events {
			if n.ID <= cursor {
				continue
			}
			cursor = n.ID
			if !settings.Allows(n.Kind) {
				continue
			}
			data, _ := json.Marshal(n)
			if _, err := fmt.Fprintf(w, "id: %d\nevent: notification\ndata: %s\n\n", n.ID, data); err != nil {
				return
			}
		}
		flush.Flush()
		select {
		case <-r.Context().Done():
			return
		case <-b.done:
			return
		case <-changed:
		case <-ticker.C:
			if _, ok := h.requireStreamAccess(r); !ok {
				return
			}
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
		}
	}
}
func (h *Hub) requireStreamAccess(r *http.Request) (*AuthInfo, bool) {
	a := h.authenticate(r)
	return a, a != nil && (a.Via != "mobile" || a.HasScope(MobileScopeDevices))
}
func (h *Hub) signalNotificationDevices() {
	select {
	case h.notifySync <- struct{}{}:
	default:
	}
}
func (h *Hub) runNotificationDevices(ctx context.Context) {
	defer close(h.notifyDone)
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		rows, err := h.db.QueryContext(ctx, `SELECT d.id FROM devices d JOIN device_harness x ON x.device_id=d.id WHERE d.revoked_at IS NULL AND x.url<>''`)
		if err == nil {
			var ids []string
			for rows.Next() {
				var id string
				if err = rows.Scan(&id); err != nil {
					break
				}
				ids = append(ids, id)
			}
			rows.Close()
			if err == nil && rows.Err() == nil {
				h.StartTurnWatcher(ids)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-h.notifySync:
		}
	}
}
func (h *Hub) notifyPool(kind, id, title, key string) {
	h.publishNotification(TurnNotification{Kind: "pool." + kind, ItemID: id, SessionTitle: title, DedupKey: "pool:" + kind + ":" + key}, 10*time.Minute)
}
