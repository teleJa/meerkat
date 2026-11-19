package wsclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"meerkat/edge/internal/agent"
	"meerkat/edge/internal/claude"
	"meerkat/edge/internal/identity"
	"meerkat/edge/internal/protocol"
)

func testIdentity(centerURL string) identity.Identity {
	return identity.Identity{
		CenterURL:       centerURL,
		DeviceID:        "dev_test",
		DeviceSecret:    "test-device-secret",
		ProtocolVersion: protocol.Version,
		EdgeVersion:     "0.1.0",
		Device: protocol.Device{
			Name:         "test",
			Hostname:     "test",
			OS:           "macOS 15.4",
			Arch:         "arm64",
			Role:         "Edge",
			Capabilities: []string{"metrics"},
			BootID:       "boot_00000001",
		},
		CreatedAt:           "2026-11-06T05:40:00.000Z",
		HeartbeatIntervalMS: 100,
		MetricsIntervalMS:   100,
	}
}

func discardLogger() *log.Logger {
	return log.New(io.Discard, "", 0)
}

func helloAckJSON() []byte {
	return []byte(`{"type":"hello.ack","protocol_version":"1","server_time":"2026-11-06T05:40:00.000Z","device_id":"dev_test","heartbeat_interval_ms":100,"metrics_interval_ms":100}`)
}

func newUpgrader() websocket.Upgrader {
	return websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
}

func TestRunHandshakeHeartbeatAndMetrics(t *testing.T) {
	upgrader := newUpgrader()
	hellos := make(chan map[string]any, 1)
	messages := make(chan map[string]any, 64)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != protocol.WSPath {
			http.NotFound(w, r)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var hello map[string]any
		if err := json.Unmarshal(data, &hello); err != nil {
			return
		}
		select {
		case hellos <- hello:
		default:
		}
		if err := conn.WriteMessage(websocket.TextMessage, helloAckJSON()); err != nil {
			return
		}
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var msg map[string]any
			if err := json.Unmarshal(data, &msg); err != nil {
				continue
			}
			select {
			case messages <- msg:
			default:
			}
			var ack map[string]any
			switch msg["type"] {
			case protocol.TypeHeartbeat:
				ack = map[string]any{"type": protocol.TypeHeartbeatAck, "seq": msg["seq"], "received_at": "2026-11-06T05:41:00.100Z"}
			case protocol.TypeMetricsBatch:
				ack = map[string]any{"type": protocol.TypeMetricsAck, "seq": msg["seq"], "received_at": "2026-11-06T05:41:00.100Z"}
			default:
				continue
			}
			ackData, err := json.Marshal(ack)
			if err != nil {
				return
			}
			if err := conn.WriteMessage(websocket.TextMessage, ackData); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	client, err := New(Config{
		Identity:          testIdentity(srv.URL),
		HeartbeatInterval: 100 * time.Millisecond,
		MetricsInterval:   100 * time.Millisecond,
		Logger:            discardLogger(),
		BackoffBase:       20 * time.Millisecond,
		BackoffMax:        100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx) }()

	var hello map[string]any
	select {
	case hello = <-hellos:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("5s 内未收到 hello")
	}
	if hello["type"] != "hello" || hello["device_id"] != "dev_test" || hello["device_secret"] != "test-device-secret" ||
		hello["protocol_version"] != "1" || hello["boot_id"] != "boot_00000001" {
		cancel()
		t.Fatalf("hello 内容错误: %v", hello)
	}
	caps, ok := hello["capabilities"].([]any)
	if !ok || len(caps) != 1 || caps[0] != "metrics" {
		cancel()
		t.Fatalf("hello capabilities 错误: %v", hello["capabilities"])
	}

	sawHeartbeat, sawMetrics := false, false
	deadline := time.After(8 * time.Second)
	for !sawHeartbeat || !sawMetrics {
		select {
		case msg := <-messages:
			switch msg["type"] {
			case protocol.TypeHeartbeat:
				sawHeartbeat = true
				if _, ok := msg["seq"].(float64); !ok {
					cancel()
					t.Fatalf("心跳缺少 seq: %v", msg)
				}
				if sentAt, _ := msg["sent_at"].(string); !strings.HasSuffix(sentAt, "Z") {
					cancel()
					t.Fatalf("心跳 sent_at 不是 UTC: %q", sentAt)
				}
			case protocol.TypeMetricsBatch:
				sawMetrics = true
				streamID, _ := msg["stream_id"].(string)
				if !strings.HasPrefix(streamID, "str_") || len(streamID) != len("str_")+4 {
					cancel()
					t.Fatalf("stream_id 格式错误: %q", streamID)
				}
				if sampledAt, _ := msg["sampled_at"].(string); !strings.HasSuffix(sampledAt, "Z") {
					cancel()
					t.Fatalf("sampled_at 不是 UTC: %q", sampledAt)
				}
				payload, _ := msg["metrics"].(map[string]any)
				if payload == nil {
					cancel()
					t.Fatalf("metrics.batch 缺少 metrics: %v", msg)
				}
				for _, key := range []string{"cpu_pct", "mem_used_bytes", "mem_total_bytes", "disk_used_bytes", "disk_total_bytes", "gpu_util_pct"} {
					if _, ok := payload[key]; !ok {
						cancel()
						t.Fatalf("metrics 缺少字段 %s: %v", key, payload)
					}
				}
			}
		case <-deadline:
			cancel()
			t.Fatalf("8s 内未收到心跳/指标: heartbeat=%v metrics=%v", sawHeartbeat, sawMetrics)
		}
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run 返回错误: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run 未在 5s 内退出")
	}
}

func TestRunStopsOnAuthFailure(t *testing.T) {
	upgrader := newUpgrader()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"error","code":"auth_failed","message":"device_secret 无效"}`))
		_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(4401, "auth failed"), time.Now().Add(time.Second))
	}))
	defer srv.Close()

	client, err := New(Config{
		Identity:          testIdentity(srv.URL),
		HeartbeatInterval: 100 * time.Millisecond,
		MetricsInterval:   100 * time.Millisecond,
		Logger:            discardLogger(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	runErr := client.Run(ctx)
	var fatal *FatalError
	if !errors.As(runErr, &fatal) {
		t.Fatalf("鉴权失败应返回 FatalError，实际: %v", runErr)
	}
	if !strings.Contains(runErr.Error(), "auth_failed") {
		t.Fatalf("错误信息不可读: %v", runErr)
	}
}

func TestRunReconnectsAfterServerClose(t *testing.T) {
	upgrader := newUpgrader()
	hellos := make(chan map[string]any, 4)
	var mu sync.Mutex
	connections := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		mu.Lock()
		connections++
		current := connections
		mu.Unlock()

		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var hello map[string]any
		if json.Unmarshal(data, &hello) == nil {
			select {
			case hellos <- hello:
			default:
			}
		}
		if err := conn.WriteMessage(websocket.TextMessage, helloAckJSON()); err != nil {
			return
		}
		if current == 1 {
			// 模拟中心/网络断开，Edge 应退避重连。
			_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(1011, "test disconnect"), time.Now().Add(time.Second))
			return
		}
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	client, err := New(Config{
		Identity:          testIdentity(srv.URL),
		HeartbeatInterval: 100 * time.Millisecond,
		MetricsInterval:   100 * time.Millisecond,
		Logger:            discardLogger(),
		BackoffBase:       20 * time.Millisecond,
		BackoffMax:        50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx) }()

	for i := 0; i < 2; i++ {
		select {
		case <-hellos:
		case <-time.After(5 * time.Second):
			cancel()
			t.Fatalf("第 %d 次连接未在 5s 内收到 hello", i+1)
		}
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run 返回错误: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run 未在 5s 内退出")
	}
}

type wsFakeRunner struct {
	entries []claude.Entry
	result  claude.Result
}

func (f *wsFakeRunner) Run(ctx context.Context, opts claude.RunOptions, onEntry func(claude.Entry)) (claude.Result, error) {
	for _, entry := range f.entries {
		onEntry(entry)
	}
	return f.result, nil
}

func exitCodePtr(v int) *int { return &v }

// TestRunHandlesCommandFlow 验证 WS 连接收到 command 后完整走通
// command.ack → turn.started → event.batch（event.ack 截断）→ turn.completed。
func TestRunHandlesCommandFlow(t *testing.T) {
	upgrader := newUpgrader()
	root := t.TempDir()
	messages := make(chan map[string]any, 64)
	completed := make(chan struct{})

	cmdData, err := json.Marshal(protocol.Command{
		Type:      protocol.TypeCommand,
		CommandID: "cmd_ws",
		Kind:      protocol.CommandStartTurn,
		TaskID:    "tsk_ws",
		SessionID: "sess_ws",
		TurnID:    "turn_ws",
		Payload: protocol.CommandPayload{
			Request:        "请回复 OK",
			Cwd:            root,
			RunProfile:     "default",
			TimeoutSeconds: 30,
		},
		CreatedAt: protocol.FormatTime(time.Now()),
		ExpiresAt: protocol.FormatTime(time.Now().Add(5 * time.Minute)),
	})
	if err != nil {
		t.Fatalf("marshal command: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != protocol.WSPath {
			http.NotFound(w, r)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
		if err := conn.WriteMessage(websocket.TextMessage, helloAckJSON()); err != nil {
			return
		}
		if err := conn.WriteMessage(websocket.TextMessage, cmdData); err != nil {
			return
		}
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var msg map[string]any
			if err := json.Unmarshal(data, &msg); err != nil {
				continue
			}
			select {
			case messages <- msg:
			default:
			}
			switch msg["type"] {
			case protocol.TypeEventBatch:
				ack := map[string]any{
					"type":        protocol.TypeEventAck,
					"stream_id":   msg["stream_id"],
					"seq":         msg["seq"],
					"inserted":    len(msg["events"].([]any)),
					"received_at": protocol.FormatTime(time.Now()),
				}
				ackData, err := json.Marshal(ack)
				if err != nil {
					return
				}
				if err := conn.WriteMessage(websocket.TextMessage, ackData); err != nil {
					return
				}
			case protocol.TypeTurnCompleted:
				select {
				case <-completed:
				default:
					close(completed)
				}
			}
		}
	}))
	defer srv.Close()

	manager, err := agent.New(agent.Options{
		StateDir:      t.TempDir(),
		AllowRoot:     root,
		Logger:        discardLogger(),
		Runner:        &wsFakeRunner{entries: []claude.Entry{{Type: "system", Subtype: "init", SessionID: "claude-ws", HarnessVersion: "2.1.304"}, {Type: "assistant", Text: "OK"}, {Type: "result", Subtype: "success", ResultText: "OK"}}, result: claude.Result{SessionID: "claude-ws", ExitCode: exitCodePtr(0), ResultText: "OK"}},
		BatchInterval: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	defer manager.Close()

	client, err := New(Config{
		Identity:          testIdentity(srv.URL),
		HeartbeatInterval: 100 * time.Millisecond,
		MetricsInterval:   100 * time.Millisecond,
		Logger:            discardLogger(),
		BackoffBase:       20 * time.Millisecond,
		BackoffMax:        100 * time.Millisecond,
		Manager:           manager,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx) }()

	sawAck, sawTurnStarted, sawBatch, sawCompleted := false, false, false, false
	timeout := time.After(10 * time.Second)
	for !(sawAck && sawTurnStarted && sawBatch && sawCompleted) {
		select {
		case msg := <-messages:
			switch msg["type"] {
			case protocol.TypeCommandAck:
				sawAck = true
				if msg["status"] != "accepted" || msg["command_id"] != "cmd_ws" {
					cancel()
					t.Fatalf("command.ack 内容错误: %v", msg)
				}
			case protocol.TypeSessionStarted:
				if msg["harness_session_id"] != "claude-ws" {
					cancel()
					t.Fatalf("session.started = %v", msg)
				}
			case protocol.TypeTurnStarted:
				sawTurnStarted = true
				if msg["turn_id"] != "turn_ws" {
					cancel()
					t.Fatalf("turn.started = %v", msg)
				}
			case protocol.TypeEventBatch:
				sawBatch = true
			case protocol.TypeTurnCompleted:
				sawCompleted = true
				if msg["status"] != "succeeded" || msg["result"] != "OK" {
					cancel()
					t.Fatalf("turn.completed = %v", msg)
				}
			}
		case <-completed:
			// 完成信号已到，继续消费其余消息。
		case <-timeout:
			cancel()
			t.Fatalf("10s 内未完成指令闭环：ack=%v turn=%v batch=%v completed=%v", sawAck, sawTurnStarted, sawBatch, sawCompleted)
		}
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run 返回错误: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run 未在 5s 内退出")
	}
}
