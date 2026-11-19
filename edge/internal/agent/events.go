package agent

import (
	"encoding/json"
	"sync"
	"time"

	"meerkat/edge/internal/claude"
	"meerkat/edge/internal/protocol"
)

// eventsFor 把一条 stream-json 事件映射为 0..n 条协议事件。
func (m *Manager) eventsFor(entry claude.Entry, cmd *protocol.Command) []protocol.Event {
	base := func(eventType string, payload map[string]any) protocol.Event {
		return protocol.Event{
			EventID:   m.newEventID(),
			TaskID:    cmd.TaskID,
			SessionID: cmd.SessionID,
			TurnID:    cmd.TurnID,
			Type:      eventType,
			At:        protocol.FormatTime(time.Now()),
			Payload:   payload,
		}
	}

	var out []protocol.Event
	switch entry.Type {
	case "system":
		if !entry.IsInit() {
			return nil
		}
		out = append(out, base(protocol.EventSystemInit, map[string]any{
			"session_id":      entry.SessionID,
			"model":           entry.Model,
			"cwd":             entry.Cwd,
			"harness_version": entry.HarnessVersion,
		}))
	case "stream_event":
		if entry.DeltaType == "text_delta" && entry.DeltaText != "" {
			out = append(out, base(protocol.EventAssistantDelta, map[string]any{
				"text":  entry.DeltaText,
				"index": entry.BlockIndex,
			}))
		}
	case "assistant":
		if text := entry.Text; text != "" {
			out = append(out, base(protocol.EventAssistantMessage, map[string]any{
				"text": truncate(text, maxAssistantChars),
			}))
		}
		for _, use := range entry.ToolUses {
			payload := map[string]any{
				"tool":        use.Name,
				"tool_use_id": use.ID,
			}
			if len(use.Input) > 0 {
				payload["input"] = truncateRaw(use.Input, maxToolInputChars)
			}
			out = append(out, base(protocol.EventToolUse, payload))
		}
	case "terminal.snapshot":
		out = append(out, base(protocol.EventTerminalSnapshot, map[string]any{
			"text": truncate(entry.Text, maxTerminalChars),
		}))
	case "user":
		for _, result := range entry.ToolResults {
			out = append(out, base(protocol.EventToolResult, map[string]any{
				"tool_use_id": result.ToolUseID,
				"is_error":    result.IsError,
				"content":     truncate(result.Content, maxToolResultChars),
			}))
		}
	case "result":
		out = append(out, base(protocol.EventResult, map[string]any{
			"subtype":     entry.Subtype,
			"is_error":    entry.IsError,
			"result":      truncate(entry.ResultText, maxResultChars),
			"duration_ms": entry.DurationMS,
			"turns":       entry.Turns,
			"session_id":  entry.SessionID,
		}))
	}
	return out
}

// truncateRaw 截断过大的工具入参，保留可读前缀。
func truncateRaw(raw json.RawMessage, limit int) any {
	if len(raw) <= limit {
		return raw
	}
	preview := string(raw)
	if len(preview) > limit {
		preview = preview[:limit]
	}
	return map[string]any{
		"truncated":     true,
		"original_size": len(raw),
		"preview":       preview,
	}
}

// flushEvents 把事件切分为不超过 200 条的批次写入 outbox。
func (m *Manager) flushEvents(events []protocol.Event) {
	for len(events) > 0 {
		n := len(events)
		if n > MaxBatchEvents {
			n = MaxBatchEvents
		}
		if _, err := m.outbox.AppendEventBatch(events[:n]); err != nil {
			m.log.Printf("写入事件 outbox 失败（丢失 %d 条事件）：%v", n, err)
			return
		}
		events = events[n:]
	}
	m.signalFlush()
}

// eventBatcher 按数量/时间攒批，避免每个 assistant 消息都单独发包。
type eventBatcher struct {
	m        *Manager
	size     int
	interval time.Duration

	mu   sync.Mutex
	buf  []protocol.Event
	stop chan struct{}
	done chan struct{}
	once sync.Once
}

func newEventBatcher(m *Manager) *eventBatcher {
	b := &eventBatcher{
		m:        m,
		size:     m.batchSize,
		interval: m.batchInterval,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	go b.loop()
	return b
}

func (b *eventBatcher) loop() {
	defer close(b.done)
	ticker := time.NewTicker(b.interval)
	defer ticker.Stop()
	for {
		select {
		case <-b.stop:
			return
		case <-ticker.C:
			b.Flush()
		}
	}
}

// Add 追加事件；达到批大小时立即发送。
func (b *eventBatcher) Add(events []protocol.Event) {
	if len(events) == 0 {
		return
	}
	b.mu.Lock()
	b.buf = append(b.buf, events...)
	var ready []protocol.Event
	if len(b.buf) >= b.size {
		ready = b.buf
		b.buf = nil
	}
	b.mu.Unlock()
	if len(ready) > 0 {
		b.m.flushEvents(ready)
	}
}

// Flush 立即发送当前缓冲。
func (b *eventBatcher) Flush() {
	b.mu.Lock()
	ready := b.buf
	b.buf = nil
	b.mu.Unlock()
	if len(ready) > 0 {
		b.m.flushEvents(ready)
	}
}

// Close 停止定时器并做最后一次 flush（保证 turn.completed 之前的事件先入 outbox）。
func (b *eventBatcher) Close() {
	b.once.Do(func() {
		close(b.stop)
		<-b.done
	})
	b.Flush()
}
