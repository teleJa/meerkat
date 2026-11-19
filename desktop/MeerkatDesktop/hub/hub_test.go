package hub

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func testHub(t *testing.T) *Hub {
	t.Helper()
	h, err := New(Config{DataDir: t.TempDir(), Host: "127.0.0.1", Port: 8877, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close() })
	return h
}

func TestPairingRegisterAndTaskLifecycle(t *testing.T) {
	h := testHub(t)
	now := time.Now()
	token, _, err := h.createPairingToken(nil, 600, now)
	if err != nil {
		t.Fatal(err)
	}
	ok, err := h.consumePairingToken(token, now)
	if err != nil || !ok {
		t.Fatalf("consume pairing token: %v %v", ok, err)
	}
	registered, err := h.registerDevice(DeviceInfo{
		Name: "test-device", Hostname: "test-host", OS: "darwin", Arch: "arm64",
		Role: "test", BootID: "boot-1", Capabilities: []string{"exec"},
	}, "0.1.0", "1", now)
	if err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	h.registerHTTP(mux)
	server := httptest.NewServer(mux)
	defer server.Close()

	body := map[string]any{
		"request": "Reply TEST_OK", "device_id": registered.Device.ID,
		"cwd": "/tmp", "title": "test task",
	}
	data, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, server.URL+"/api/tasks", bytes.NewReader(data))
	req.Header.Set("authorization", "Bearer "+h.AdminToken())
	req.Header.Set("content-type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create task status %d", resp.StatusCode)
	}
	var created struct {
		Task struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"task"`
		CommandID string `json:"command_id"`
		TurnID    string `json:"turn_id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	if created.Task.ID == "" || created.Task.Status != "queued" || created.CommandID == "" {
		t.Fatalf("unexpected create response: %+v", created)
	}
	turns, _ := h.listTurns(*h.mustTask(t, created.Task.ID).SessionID)
	if len(turns) != 1 {
		t.Fatalf("expected one turn, got %d", len(turns))
	}
	h.handleCommandAck(created.CommandID, "accepted", "", "", time.Now())
	h.handleTurnCompleted(TurnCompletedInput{
		TaskID: created.Task.ID, SessionID: *h.mustTask(t, created.Task.ID).SessionID,
		TurnID: turns[0].ID, Status: "succeeded", Result: stringPtr("TEST_OK"),
	}, time.Now())
	task := h.mustTask(t, created.Task.ID)
	if task.Status != "succeeded" {
		t.Fatalf("task status = %s", task.Status)
	}
	turns, _ = h.listTurns(*task.SessionID)
	if turns[0].Result == nil || *turns[0].Result != "TEST_OK" {
		t.Fatalf("turn result missing: %+v", turns[0])
	}
}

func (h *Hub) mustTask(t *testing.T, id string) *TaskRow {
	t.Helper()
	task, err := h.getTask(id)
	if err != nil || task == nil {
		t.Fatalf("task %s missing: %v", id, err)
	}
	return task
}

func TestPairingTokenCanOnlyBeUsedOnce(t *testing.T) {
	h := testHub(t)
	token, _, err := h.createPairingToken(nil, 600, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := h.consumePairingToken(token, time.Now()); !ok {
		t.Fatal("first consume should succeed")
	}
	if ok, _ := h.consumePairingToken(token, time.Now()); ok {
		t.Fatal("second consume should fail")
	}
}

func stringPtr(value string) *string { return &value }
