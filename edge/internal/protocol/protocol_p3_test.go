package protocol

import (
	"encoding/json"
	"testing"
	"time"
)

func TestDecodeCommandStartTurn(t *testing.T) {
	data := []byte(`{"type":"command","command_id":"cmd_5c4b3a2f","kind":"start_turn","task_id":"tsk_1a2b3c4d","session_id":"sess_9f8e7d6c","turn_id":"turn_1","payload":{"request":"请回复 OK","cwd":"/Users/example/projects/meerkat","run_profile":"default","harness_session_id":null,"timeout_seconds":1800},"created_at":"2026-11-06T07:00:00.000Z","expires_at":"2026-11-06T07:10:00.000Z"}`)
	msg, err := DecodeServerMessage(data)
	if err != nil {
		t.Fatalf("DecodeServerMessage: %v", err)
	}
	cmd, ok := msg.(*Command)
	if !ok {
		t.Fatalf("类型 = %T, want *Command", msg)
	}
	if cmd.Kind != CommandStartTurn || cmd.CommandID != "cmd_5c4b3a2f" || cmd.TurnID != "turn_1" || cmd.SessionID != "sess_9f8e7d6c" {
		t.Fatalf("command 字段错误: %+v", cmd)
	}
	if cmd.Payload.Request != "请回复 OK" || cmd.Payload.Cwd != "/Users/example/projects/meerkat" ||
		cmd.Payload.RunProfile != "default" || cmd.Payload.TimeoutSeconds != 1800 {
		t.Fatalf("payload 错误: %+v", cmd.Payload)
	}
	if cmd.Payload.HarnessSessionID != nil {
		t.Fatalf("harness_session_id 应为 nil，实际 %v", *cmd.Payload.HarnessSessionID)
	}
}

func TestDecodeCommandCancelWithoutPayload(t *testing.T) {
	data := []byte(`{"type":"command","command_id":"cmd_cancel","kind":"cancel","task_id":"tsk_1","session_id":"sess_1","created_at":"2026-11-06T07:00:00.000Z","expires_at":"2026-11-06T07:10:00.000Z"}`)
	msg, err := DecodeServerMessage(data)
	if err != nil {
		t.Fatalf("DecodeServerMessage: %v", err)
	}
	cmd, ok := msg.(*Command)
	if !ok || cmd.Kind != CommandCancel || cmd.TurnID != "" {
		t.Fatalf("cancel command 解析错误: %+v", msg)
	}
	if cmd.Payload.Request != "" || cmd.Payload.Cwd != "" {
		t.Fatalf("cancel payload 应为零值: %+v", cmd.Payload)
	}
}

func TestDecodeEventAckIgnoresExtraFields(t *testing.T) {
	data := []byte(`{"type":"event.ack","stream_id":"str_ab12","seq":7,"inserted":2,"received_at":"2026-11-06T07:00:00.100Z"}`)
	msg, err := DecodeServerMessage(data)
	if err != nil {
		t.Fatalf("DecodeServerMessage: %v", err)
	}
	ack, ok := msg.(*EventAck)
	if !ok || ack.StreamID != "str_ab12" || ack.Seq != 7 {
		t.Fatalf("event.ack 解析错误: %+v", msg)
	}
}

func TestCommandExpired(t *testing.T) {
	now := time.Date(2026, 11, 6, 7, 5, 0, 0, time.UTC)
	future := &Command{ExpiresAt: "2026-11-06T07:10:00.000Z"}
	past := &Command{ExpiresAt: "2026-11-06T07:00:00.000Z"}
	missing := &Command{}
	invalid := &Command{ExpiresAt: "not-a-time"}
	if future.Expired(now) {
		t.Fatal("未过期指令不应判定为过期")
	}
	if !past.Expired(now) {
		t.Fatal("过期指令应判定为过期")
	}
	if missing.Expired(now) || invalid.Expired(now) {
		t.Fatal("缺失/非法 expires_at 应按未过期处理，由中心兜底")
	}
}

func TestP3OutgoingMessageJSON(t *testing.T) {
	ackData, err := json.Marshal(CommandAck{Type: TypeCommandAck, CommandID: "cmd_1", Status: CommandAckRejected, Detail: "cwd_not_allowed", At: "2026-11-06T07:00:00.000Z"})
	if err != nil {
		t.Fatalf("marshal command.ack: %v", err)
	}
	var ack map[string]any
	if err := json.Unmarshal(ackData, &ack); err != nil {
		t.Fatalf("unmarshal command.ack: %v", err)
	}
	if ack["type"] != "command.ack" || ack["status"] != "rejected" || ack["detail"] != "cwd_not_allowed" {
		t.Fatalf("command.ack JSON = %s", ackData)
	}

	startedData, err := json.Marshal(SessionStarted{
		Type: TypeSessionStarted, CommandID: "cmd_1", TaskID: "tsk_1", SessionID: "sess_1",
		HarnessSessionID: "claude-x", HarnessVersion: "2.1.304", Cwd: "/tmp", At: "2026-11-06T07:00:00.000Z",
	})
	if err != nil {
		t.Fatalf("marshal session.started: %v", err)
	}
	var started map[string]any
	if err := json.Unmarshal(startedData, &started); err != nil {
		t.Fatalf("unmarshal session.started: %v", err)
	}
	if started["type"] != "session.started" || started["harness_session_id"] != "claude-x" || started["harness_version"] != "2.1.304" {
		t.Fatalf("session.started JSON = %s", startedData)
	}

	eventData, err := json.Marshal(EventBatch{
		Type: TypeEventBatch, StreamID: "str_ab12", Seq: 7,
		Events: []Event{
			{EventID: "evt_1", TaskID: "tsk_1", SessionID: "sess_1", TurnID: "turn_1", Type: EventAssistantMessage, At: "2026-11-06T07:00:00.000Z", Payload: map[string]any{"text": "OK"}},
			{EventID: "evt_2", Type: EventToolUse, At: "2026-11-06T07:00:00.100Z", Payload: map[string]any{"tool": "Bash", "tool_use_id": "toolu_1", "input": json.RawMessage(`{"command":"ls"}`)}},
		},
	})
	if err != nil {
		t.Fatalf("marshal event.batch: %v", err)
	}
	var batch map[string]any
	if err := json.Unmarshal(eventData, &batch); err != nil {
		t.Fatalf("unmarshal event.batch: %v", err)
	}
	events, ok := batch["events"].([]any)
	if !ok || len(events) != 2 || batch["stream_id"] != "str_ab12" || batch["seq"].(float64) != 7 {
		t.Fatalf("event.batch JSON = %s", eventData)
	}
	second := events[1].(map[string]any)
	if second["type"] != "tool.use" || second["event_id"] != "evt_2" {
		t.Fatalf("事件字段错误: %v", second)
	}

	completedData, err := json.Marshal(TurnCompleted{
		Type: TypeTurnCompleted, TaskID: "tsk_1", SessionID: "sess_1", TurnID: "turn_1",
		Status: TurnSucceeded, At: "2026-11-06T07:00:01.000Z",
	})
	if err != nil {
		t.Fatalf("marshal turn.completed: %v", err)
	}
	var completed map[string]any
	if err := json.Unmarshal(completedData, &completed); err != nil {
		t.Fatalf("unmarshal turn.completed: %v", err)
	}
	for _, key := range []string{"exit_code", "duration_ms", "result", "error"} {
		if value, ok := completed[key]; !ok || value != nil {
			t.Fatalf("%s 应为 null，JSON = %s", key, completedData)
		}
	}
	if completed["status"] != "succeeded" {
		t.Fatalf("turn.completed JSON = %s", completedData)
	}
}
