package outbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"meerkat/edge/internal/protocol"
)

func testEvent(id string) protocol.Event {
	return protocol.Event{
		EventID:   id,
		TaskID:    "tsk_1",
		SessionID: "sess_1",
		TurnID:    "turn_1",
		Type:      protocol.EventAssistantMessage,
		At:        "2026-11-06T07:00:00.000Z",
		Payload:   map[string]any{"text": "hi"},
	}
}

func testCompleted(turnID string) protocol.TurnCompleted {
	result := "OK"
	return protocol.TurnCompleted{
		Type:      protocol.TypeTurnCompleted,
		TaskID:    "tsk_1",
		SessionID: "sess_1",
		TurnID:    turnID,
		Status:    protocol.TurnSucceeded,
		Result:    &result,
		At:        "2026-11-06T07:00:01.000Z",
	}
}

func TestAppendAndReopen(t *testing.T) {
	dir := t.TempDir()
	ob, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	batch1, err := ob.AppendEventBatch([]protocol.Event{testEvent("evt_1")})
	if err != nil {
		t.Fatalf("AppendEventBatch: %v", err)
	}
	batch2, err := ob.AppendEventBatch([]protocol.Event{testEvent("evt_2"), testEvent("evt_3")})
	if err != nil {
		t.Fatalf("AppendEventBatch: %v", err)
	}
	if batch1.Seq != 1 || batch2.Seq != 2 {
		t.Fatalf("seq = %d/%d, want 1/2", batch1.Seq, batch2.Seq)
	}
	if batch1.StreamID != batch2.StreamID || !strings.HasPrefix(batch1.StreamID, "str_") {
		t.Fatalf("stream_id = %q/%q", batch1.StreamID, batch2.StreamID)
	}
	if ob.Len() != 2 {
		t.Fatalf("Len = %d, want 2", ob.Len())
	}

	unsent := ob.Unsent()
	if len(unsent) != 2 {
		t.Fatalf("Unsent = %d, want 2", len(unsent))
	}
	ob.MarkSent(unsent[0].ID)
	if len(ob.Unsent()) != 1 {
		t.Fatalf("MarkSent 后 Unsent = %d, want 1", len(ob.Unsent()))
	}
	if err := ob.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// 重启后应沿用同一 stream_id 与 seq，并重发全部未确认记录。
	ob2, err := Open(dir)
	if err != nil {
		t.Fatalf("重新 Open: %v", err)
	}
	defer ob2.Close()
	if ob2.StreamID() != batch1.StreamID {
		t.Fatalf("重启后 stream_id = %q, want %q", ob2.StreamID(), batch1.StreamID)
	}
	reopened := ob2.Unsent()
	if len(reopened) != 2 {
		t.Fatalf("重启后 Unsent = %d, want 2", len(reopened))
	}
	if reopened[0].Seq != 1 || reopened[1].Seq != 2 {
		t.Fatalf("重启后 seq = %d/%d", reopened[0].Seq, reopened[1].Seq)
	}
	var batch protocol.EventBatch
	if err := json.Unmarshal(reopened[1].Message, &batch); err != nil {
		t.Fatalf("反序列化记录失败: %v", err)
	}
	if len(batch.Events) != 2 || batch.Events[0].EventID != "evt_2" {
		t.Fatalf("记录内容错误: %+v", batch)
	}

	batch3, err := ob2.AppendEventBatch([]protocol.Event{testEvent("evt_4")})
	if err != nil {
		t.Fatalf("重启后 Append: %v", err)
	}
	if batch3.Seq != 3 {
		t.Fatalf("重启后 seq = %d, want 3", batch3.Seq)
	}
}

func TestAckTruncatesCumulatively(t *testing.T) {
	ob, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer ob.Close()
	for i := 0; i < 3; i++ {
		if _, err := ob.AppendEventBatch([]protocol.Event{testEvent("evt_x")}); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	streamID := ob.StreamID()
	removed, err := ob.Ack(streamID, 2)
	if err != nil {
		t.Fatalf("Ack: %v", err)
	}
	if removed != 2 || ob.Len() != 1 {
		t.Fatalf("removed=%d Len=%d, want 2/1", removed, ob.Len())
	}
	if ob.Unsent()[0].Seq != 3 {
		t.Fatalf("剩余 seq = %d, want 3", ob.Unsent()[0].Seq)
	}
	// 重复 ack 更小的 seq 不应误删。
	if removed, _ := ob.Ack(streamID, 1); removed != 0 {
		t.Fatalf("重复 ack removed = %d, want 0", removed)
	}
	if removed, _ := ob.Ack("str_ffff", 3); removed != 0 {
		t.Fatalf("不同 stream ack removed = %d, want 0", removed)
	}
}

func TestAckRemovesEarlierTurnCompleted(t *testing.T) {
	dir := t.TempDir()
	ob, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer ob.Close()

	if _, err := ob.AppendEventBatch([]protocol.Event{testEvent("evt_1")}); err != nil {
		t.Fatalf("Append batch1: %v", err)
	}
	if err := ob.AppendTurnCompleted(testCompleted("turn_1")); err != nil {
		t.Fatalf("AppendTurnCompleted: %v", err)
	}
	if _, err := ob.AppendEventBatch([]protocol.Event{testEvent("evt_2")}); err != nil {
		t.Fatalf("Append batch2: %v", err)
	}
	if ob.Len() != 3 {
		t.Fatalf("Len = %d, want 3", ob.Len())
	}
	removed, err := ob.Ack(ob.StreamID(), 2)
	if err != nil {
		t.Fatalf("Ack: %v", err)
	}
	if removed != 3 || ob.Len() != 0 {
		t.Fatalf("removed=%d Len=%d, want 3/0", removed, ob.Len())
	}

	// 只确认第一批时，turn.completed 仍保留等待重发。
	if _, err := ob.AppendEventBatch([]protocol.Event{testEvent("evt_3")}); err != nil {
		t.Fatalf("Append batch3: %v", err)
	}
	if err := ob.AppendTurnCompleted(testCompleted("turn_2")); err != nil {
		t.Fatalf("AppendTurnCompleted: %v", err)
	}
	if removed, err := ob.Ack(ob.StreamID(), 3); err != nil || removed != 1 {
		t.Fatalf("Ack batch3 removed=%d err=%v", removed, err)
	}
	if ob.Len() != 1 {
		t.Fatalf("turn.completed 应保留，Len = %d", ob.Len())
	}
}

func TestCorruptedLineSkipped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, []byte("{not-json\n"), 0o600); err != nil {
		t.Fatalf("写坏文件失败: %v", err)
	}
	ob, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer ob.Close()
	if ob.CorruptedLines() != 1 {
		t.Fatalf("CorruptedLines = %d, want 1", ob.CorruptedLines())
	}
	if _, err := ob.AppendEventBatch([]protocol.Event{testEvent("evt_ok")}); err != nil {
		t.Fatalf("损坏行不应阻塞写入: %v", err)
	}
	if ob.Len() != 1 {
		t.Fatalf("Len = %d, want 1", ob.Len())
	}
}
