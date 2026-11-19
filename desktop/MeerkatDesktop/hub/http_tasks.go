package hub

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func taskToDTO(task TaskRow) map[string]any {
	return map[string]any{
		"id": task.ID, "title": task.Title, "source": task.Source, "device_id": task.DeviceID,
		"session_id": task.SessionID, "status": task.Status, "stage": task.Stage, "cwd": task.Cwd,
		"request": task.Request, "created_at": task.CreatedAt, "updated_at": task.UpdatedAt,
		"evidence_kind": task.EvidenceKind, "evidence_note": task.EvidenceNote,
	}
}

func sessionToDTO(session SessionRow) map[string]any {
	return map[string]any{
		"id": session.ID, "task_id": session.TaskID, "device_id": session.DeviceID,
		"mode": session.Mode, "status": session.Status, "cwd": session.Cwd,
		"harness": session.Harness, "harness_version": session.HarnessVersion,
		"harness_session_id": session.HarnessSessionID, "created_at": session.CreatedAt,
		"last_activity_at": session.LastActivityAt,
	}
}

func turnToDTO(turn TurnRow) map[string]any {
	return map[string]any{
		"id": turn.ID, "seq": turn.Seq, "request": turn.Request, "status": turn.Status,
		"started_at": turn.StartedAt, "ended_at": turn.EndedAt, "exit_code": turn.ExitCode,
		"result": turn.Result, "error": turn.Error, "created_at": turn.CreatedAt,
	}
}

func commandToDTO(command CommandRow) map[string]any {
	return map[string]any{
		"id": command.ID, "kind": command.Kind, "status": command.Status,
		"created_at": command.CreatedAt, "delivered_at": command.DeliveredAt,
		"acked_at": command.AckedAt, "error": command.Error, "expires_at": command.ExpiresAt,
	}
}

func taskEventToDTO(event TaskEventRow) map[string]any {
	var payload any
	if err := json.Unmarshal([]byte(event.Payload), &payload); err != nil {
		payload = map[string]any{}
	}
	return map[string]any{
		"event_id": event.EventID, "type": event.Type, "at": event.At,
		"turn_id": event.TurnID, "payload": payload,
	}
}

func (h *Hub) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	h.expireCommands(time.Now())
	var body struct {
		Request        string   `json:"request"`
		DeviceID       string   `json:"device_id"`
		Cwd            string   `json:"cwd"`
		Title          string   `json:"title"`
		RunProfile     *string  `json:"run_profile"`
		IdempotencyKey *string  `json:"idempotency_key"`
		TimeoutSeconds *int     `json:"timeout_seconds"`
		AttachmentIDs  []string `json:"attachment_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid json")
		return
	}
	if strings.TrimSpace(body.Request) == "" || strings.TrimSpace(body.DeviceID) == "" || strings.TrimSpace(body.Cwd) == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "request/device_id/cwd 必填")
		return
	}
	if len(body.Request) > 20000 || len(body.Cwd) > 1024 {
		writeError(w, http.StatusBadRequest, "invalid_request", "字段过长")
		return
	}
	device, err := h.getDevice(body.DeviceID)
	if err != nil || device == nil {
		writeError(w, http.StatusNotFound, "device_not_found", "设备不存在")
		return
	}
	if device.RevokedAt != nil {
		writeError(w, http.StatusConflict, "device_revoked", "设备凭据已撤销，不能派发任务")
		return
	}
	attachmentRows, _, err := h.LoadAttachments(body.AttachmentIDs)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_attachment", err.Error())
		return
	}
	created, err := h.createTask(CreateTaskInput{
		Request: body.Request, Title: body.Title, DeviceID: body.DeviceID, Cwd: body.Cwd,
		RunProfile: body.RunProfile, IdempotencyKey: body.IdempotencyKey, TimeoutSeconds: body.TimeoutSeconds,
		AttachmentIDs: body.AttachmentIDs, AttachmentRows: attachmentRows,
	}, time.Now())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "中心内部错误")
		return
	}
	h.deliverCommand(created.Command, time.Now())
	writeJSON(w, http.StatusCreated, map[string]any{
		"task": taskToDTO(created.Task), "session_id": created.Session.ID, "command_id": created.Command.ID,
	})
}

func (h *Hub) handleListTasks(w http.ResponseWriter, r *http.Request) {
	h.expireCommands(time.Now())
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			limit = parsed
		}
	}
	if limit < 1 {
		limit = 1
	}
	if limit > 500 {
		limit = 500
	}
	tasks, err := h.listTasks(500)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "中心内部错误")
		return
	}
	status := r.URL.Query().Get("status")
	deviceID := r.URL.Query().Get("device_id")
	out := make([]map[string]any, 0)
	for _, task := range tasks {
		if status != "" && task.Status != status {
			continue
		}
		if deviceID != "" && task.DeviceID != deviceID {
			continue
		}
		if len(out) >= limit {
			break
		}
		out = append(out, taskToDTO(task))
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": out})
}

func (h *Hub) handleTaskDetail(w http.ResponseWriter, r *http.Request) {
	h.expireCommands(time.Now())
	task, err := h.getTask(pathID(r))
	if err != nil || task == nil {
		writeError(w, http.StatusNotFound, "task_not_found", "任务不存在")
		return
	}
	var session *SessionRow
	if task.SessionID != nil {
		session, _ = h.getSession(*task.SessionID)
	}
	var turns []map[string]any
	var events []map[string]any
	if session != nil {
		rows, _ := h.listTurns(session.ID)
		for _, turn := range rows {
			turns = append(turns, turnToDTO(turn))
		}
		eventRows, _ := h.listTaskEvents(task.ID, 300)
		for _, event := range eventRows {
			events = append(events, taskEventToDTO(event))
		}
	}
	if turns == nil {
		turns = []map[string]any{}
	}
	if events == nil {
		events = []map[string]any{}
	}
	commands, _ := h.listCommandsForTask(task.ID)
	commandOut := make([]map[string]any, 0, len(commands))
	for _, command := range commands {
		commandOut = append(commandOut, commandToDTO(command))
	}
	var sessionOut any
	if session != nil {
		sessionOut = sessionToDTO(*session)
	}
	artifactRows, _ := h.listArtifactsForTask(task.ID)
	artifactOut := make([]map[string]any, 0, len(artifactRows))
	for _, artifact := range artifactRows {
		artifactOut = append(artifactOut, artifactToDTO(artifact))
	}
	attachmentRows, _ := h.listAttachmentsForTask(task.ID)
	attachmentOut := make([]map[string]any, 0, len(attachmentRows))
	for _, attachment := range attachmentRows {
		attachmentOut = append(attachmentOut, attachmentToDTO(attachment))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"task": taskToDTO(*task), "session": sessionOut, "turns": turns,
		"commands": commandOut, "events": events, "artifacts": artifactOut,
		"attachments": attachmentOut,
	})
}

func (h *Hub) handleAddTurn(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Request        string   `json:"request"`
		IdempotencyKey *string  `json:"idempotency_key"`
		AttachmentIDs  []string `json:"attachment_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Request) == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "request 必填")
		return
	}
	attachmentRows, _, err := h.LoadAttachments(body.AttachmentIDs)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_attachment", err.Error())
		return
	}
	created, code := h.createTurn(pathID(r), body.Request, body.IdempotencyKey, body.AttachmentIDs, attachmentRows, time.Now())
	if code != "" {
		status := http.StatusConflict
		if code == "task_not_found" {
			status = http.StatusNotFound
		}
		writeError(w, status, code, "当前状态不允许追加输入")
		return
	}
	h.deliverCommand(created.Command, time.Now())
	writeJSON(w, http.StatusOK, map[string]any{
		"task": taskToDTO(created.Task), "session_id": created.Session.ID,
		"turn_id": created.Turn.ID, "command_id": created.Command.ID,
	})
}

func (h *Hub) handleCancelTask(w http.ResponseWriter, r *http.Request) {
	task, command, immediate, code := h.cancelTask(pathID(r), time.Now())
	if code != "" {
		status := http.StatusConflict
		if code == "task_not_found" {
			status = http.StatusNotFound
		}
		writeError(w, status, code, "任务已结束，不能取消")
		return
	}
	if command != nil {
		h.deliverCommand(*command, time.Now())
	}
	var commandID any
	if command != nil {
		commandID = command.ID
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"task": taskToDTO(*task), "command_id": commandID, "cancelled_immediately": immediate,
	})
}

func (h *Hub) handleListSessions(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			limit = parsed
		}
	}
	if limit < 1 {
		limit = 1
	}
	if limit > 500 {
		limit = 500
	}
	sessions, err := h.listSessions(500)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "中心内部错误")
		return
	}
	mode := r.URL.Query().Get("mode")
	out := make([]map[string]any, 0)
	for _, session := range sessions {
		if mode != "" && session.Mode != mode {
			continue
		}
		if len(out) >= limit {
			break
		}
		var count int
		_ = h.db.QueryRow("SELECT COUNT(*) FROM turns WHERE session_id = ?", session.ID).Scan(&count)
		item := sessionToDTO(session)
		item["turn_count"] = count
		out = append(out, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": out})
}

func (h *Hub) handleSessionDetail(w http.ResponseWriter, r *http.Request) {
	session, err := h.getSession(pathID(r))
	if err != nil || session == nil {
		writeError(w, http.StatusNotFound, "session_not_found", "会话不存在")
		return
	}
	task, _ := h.getTask(session.TaskID)
	turns, _ := h.listTurns(session.ID)
	events, _ := h.listSessionEvents(session.ID, 300)
	turnOut := make([]map[string]any, 0, len(turns))
	for _, turn := range turns {
		turnOut = append(turnOut, turnToDTO(turn))
	}
	eventOut := make([]map[string]any, 0, len(events))
	for _, event := range events {
		eventOut = append(eventOut, taskEventToDTO(event))
	}
	var taskOut any
	if task != nil {
		taskOut = taskToDTO(*task)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"session": sessionToDTO(*session), "task": taskOut, "turns": turnOut, "events": eventOut,
	})
}

func (h *Hub) handleInternalEvents(w http.ResponseWriter, r *http.Request) {
	var body struct {
		DeviceID string          `json:"device_id"`
		Events   []IncomingEvent `json:"events"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid json")
		return
	}
	if body.DeviceID == "" {
		body.DeviceID = "unknown"
	}
	inserted := h.handleEventBatch(body.DeviceID, body.Events, time.Now())
	writeJSON(w, http.StatusOK, map[string]any{"inserted": inserted})
}
