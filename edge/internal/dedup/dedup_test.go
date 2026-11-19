package dedup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"meerkat/edge/internal/protocol"
)

func TestRecordAndReopen(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, ok := store.Get("cmd_missing"); ok {
		t.Fatal("未记录的 command_id 不应命中")
	}
	if err := store.Record(Entry{CommandID: "cmd_1", Status: StatusAccepted}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if err := store.Record(Entry{CommandID: "cmd_2", Status: StatusRejected, Detail: "cwd_not_allowed"}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	entry, ok := store.Get("cmd_1")
	if !ok || entry.Status != StatusAccepted || entry.At == "" {
		t.Fatalf("cmd_1 = %+v ok=%v", entry, ok)
	}

	reopened, err := Open(dir)
	if err != nil {
		t.Fatalf("重新 Open: %v", err)
	}
	if reopened.Count() != 2 {
		t.Fatalf("Count = %d, want 2", reopened.Count())
	}
	entry, ok = reopened.Get("cmd_2")
	if !ok || entry.Status != StatusRejected || entry.Detail != "cwd_not_allowed" {
		t.Fatalf("重启后 cmd_2 = %+v ok=%v", entry, ok)
	}
	if err := reopened.Record(Entry{CommandID: "cmd_1", Status: StatusAccepted, Detail: "重复"}); err != nil {
		t.Fatalf("重复 Record: %v", err)
	}
	if reopened.Count() != 2 {
		t.Fatalf("重复记录后 Count = %d, want 2", reopened.Count())
	}
}

func TestSetCompletionPersists(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := store.Record(Entry{CommandID: "cmd_1", Status: StatusAccepted}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	result := "OK"
	tc := protocol.TurnCompleted{
		Type:      protocol.TypeTurnCompleted,
		TaskID:    "tsk_1",
		SessionID: "sess_1",
		TurnID:    "turn_1",
		Status:    protocol.TurnSucceeded,
		Result:    &result,
		At:        "2026-11-06T07:00:01.000Z",
	}
	if err := store.SetCompletion("cmd_1", tc); err != nil {
		t.Fatalf("SetCompletion: %v", err)
	}

	reopened, err := Open(dir)
	if err != nil {
		t.Fatalf("重新 Open: %v", err)
	}
	entry, ok := reopened.Get("cmd_1")
	if !ok || len(entry.TurnCompleted) == 0 {
		t.Fatalf("重启后未保留 turn.completed: %+v", entry)
	}
	var got protocol.TurnCompleted
	if err := json.Unmarshal(entry.TurnCompleted, &got); err != nil {
		t.Fatalf("解析 turn.completed 失败: %v", err)
	}
	if got.TurnID != "turn_1" || got.Status != protocol.TurnSucceeded || got.Result == nil || *got.Result != "OK" {
		t.Fatalf("turn.completed = %+v", got)
	}
}

func TestCompactsLargeFile(t *testing.T) {
	dir := t.TempDir()
	var sb strings.Builder
	for i := 0; i < maxLines+5; i++ {
		id := "cmd_a"
		if i%2 == 1 {
			id = "cmd_b"
		}
		sb.WriteString(`{"command_id":"` + id + `","status":"accepted","at":"2026-11-06T07:00:00.000Z"}`)
		sb.WriteString("\n")
	}
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(sb.String()), 0o600); err != nil {
		t.Fatalf("写大文件失败: %v", err)
	}
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if store.Count() != 2 {
		t.Fatalf("压缩后 Count = %d, want 2", store.Count())
	}
	data, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatalf("读取压缩文件失败: %v", err)
	}
	if lines := strings.Count(string(data), "\n"); lines != 2 {
		t.Fatalf("压缩后行数 = %d, want 2", lines)
	}
}
