package hub

import (
	"database/sql"
	"encoding/json"
	"time"
)

func (h *Hub) getTask(id string) (*TaskRow, error) {
	row := h.db.QueryRow("SELECT * FROM tasks WHERE id = ?", id)
	var task TaskRow
	err := row.Scan(
		&task.ID, &task.Title, &task.Source, &task.DeviceID, &task.SessionID, &task.Status,
		&task.Stage, &task.Cwd, &task.Request, &task.IdempotencyKey, &task.EvidenceKind,
		&task.EvidenceNote, &task.CreatedAt, &task.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &task, err
}

func (h *Hub) getTaskByIdempotency(key string) (*TaskRow, error) {
	row := h.db.QueryRow("SELECT * FROM tasks WHERE idempotency_key = ?", key)
	var task TaskRow
	err := row.Scan(
		&task.ID, &task.Title, &task.Source, &task.DeviceID, &task.SessionID, &task.Status,
		&task.Stage, &task.Cwd, &task.Request, &task.IdempotencyKey, &task.EvidenceKind,
		&task.EvidenceNote, &task.CreatedAt, &task.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &task, err
}

func scanSession(scanner interface{ Scan(dest ...any) error }) (SessionRow, error) {
	var s SessionRow
	err := scanner.Scan(
		&s.ID, &s.TaskID, &s.DeviceID, &s.Mode, &s.Status, &s.Cwd, &s.Harness,
		&s.HarnessVersion, &s.HarnessSessionID, &s.CreatedAt, &s.LastActivityAt,
	)
	return s, err
}

func (h *Hub) getSession(id string) (*SessionRow, error) {
	row := h.db.QueryRow("SELECT * FROM sessions WHERE id = ?", id)
	session, err := scanSession(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &session, err
}

func (h *Hub) listSessions(limit int) ([]SessionRow, error) {
	rows, err := h.db.Query("SELECT * FROM sessions ORDER BY last_activity_at DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sessions []SessionRow
	for rows.Next() {
		session, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, session)
	}
	return sessions, rows.Err()
}

func (h *Hub) listTasks(limit int, status, deviceID string, activeOnly bool) ([]TaskRow, error) {
	rows, err := h.db.Query(`SELECT * FROM tasks
		WHERE (? = '' OR status = ?) AND (? = '' OR device_id = ?)
		AND (NOT ? OR status IN ('queued', 'running', 'cancel_requested'))
		ORDER BY created_at DESC LIMIT ?`, status, status, deviceID, deviceID, activeOnly, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tasks []TaskRow
	for rows.Next() {
		var task TaskRow
		if err := rows.Scan(
			&task.ID, &task.Title, &task.Source, &task.DeviceID, &task.SessionID, &task.Status,
			&task.Stage, &task.Cwd, &task.Request, &task.IdempotencyKey, &task.EvidenceKind,
			&task.EvidenceNote, &task.CreatedAt, &task.UpdatedAt,
		); err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

func scanTurn(scanner interface{ Scan(dest ...any) error }) (TurnRow, error) {
	var turn TurnRow
	err := scanner.Scan(
		&turn.ID, &turn.TaskID, &turn.SessionID, &turn.Seq, &turn.Request, &turn.Status,
		&turn.StartedAt, &turn.EndedAt, &turn.ExitCode, &turn.Result, &turn.Error, &turn.CreatedAt,
	)
	return turn, err
}

func (h *Hub) listTurns(sessionID string) ([]TurnRow, error) {
	rows, err := h.db.Query("SELECT * FROM turns WHERE session_id = ? ORDER BY seq ASC", sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var turns []TurnRow
	for rows.Next() {
		turn, err := scanTurn(rows)
		if err != nil {
			return nil, err
		}
		turns = append(turns, turn)
	}
	return turns, rows.Err()
}

func scanCommand(scanner interface{ Scan(dest ...any) error }) (CommandRow, error) {
	var command CommandRow
	err := scanner.Scan(
		&command.ID, &command.TaskID, &command.SessionID, &command.TurnID, &command.DeviceID,
		&command.Kind, &command.Status, &command.Payload, &command.Error, &command.CreatedAt,
		&command.DeliveredAt, &command.AckedAt, &command.ExpiresAt,
	)
	return command, err
}

func (h *Hub) listCommandsForTask(taskID string) ([]CommandRow, error) {
	rows, err := h.db.Query("SELECT * FROM commands WHERE task_id = ? ORDER BY created_at ASC", taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var commands []CommandRow
	for rows.Next() {
		command, err := scanCommand(rows)
		if err != nil {
			return nil, err
		}
		commands = append(commands, command)
	}
	return commands, rows.Err()
}

func (h *Hub) listDeliverableCommands(deviceID string) ([]CommandRow, error) {
	rows, err := h.db.Query(
		"SELECT * FROM commands WHERE device_id = ? AND status IN ('pending','delivered') ORDER BY created_at ASC",
		deviceID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var commands []CommandRow
	for rows.Next() {
		command, err := scanCommand(rows)
		if err != nil {
			return nil, err
		}
		commands = append(commands, command)
	}
	return commands, rows.Err()
}

func scanTaskEvent(scanner interface{ Scan(dest ...any) error }) (TaskEventRow, error) {
	var event TaskEventRow
	err := scanner.Scan(
		&event.EventID, &event.TaskID, &event.SessionID, &event.TurnID, &event.DeviceID,
		&event.Type, &event.Payload, &event.At, &event.ReceivedAt,
	)
	return event, err
}

func (h *Hub) listTaskEvents(taskID string, limit int) ([]TaskEventRow, error) {
	rows, err := h.db.Query("SELECT * FROM task_events WHERE task_id = ? ORDER BY at ASC LIMIT ?", taskID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []TaskEventRow
	for rows.Next() {
		event, err := scanTaskEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func (h *Hub) listSessionEvents(sessionID string, limit int) ([]TaskEventRow, error) {
	rows, err := h.db.Query("SELECT * FROM task_events WHERE session_id = ? ORDER BY at ASC LIMIT ?", sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []TaskEventRow
	for rows.Next() {
		event, err := scanTaskEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func (h *Hub) countActiveCommands(taskID string) int {
	var count int
	_ = h.db.QueryRow(
		"SELECT COUNT(*) FROM commands WHERE task_id = ? AND status IN ('pending','delivered','accepted')",
		taskID,
	).Scan(&count)
	return count
}

type createdTask struct {
	Task    TaskRow
	Session SessionRow
	Turn    TurnRow
	Command CommandRow
}

func (h *Hub) createTask(input CreateTaskInput, now time.Time) (*createdTask, error) {
	at := nowISO(now)
	if input.IdempotencyKey != nil && *input.IdempotencyKey != "" {
		existing, err := h.getTaskByIdempotency(*input.IdempotencyKey)
		if err != nil {
			return nil, err
		}
		if existing != nil && existing.SessionID != nil {
			session, err := h.getSession(*existing.SessionID)
			if err != nil {
				return nil, err
			}
			turns, err := h.listTurns(*existing.SessionID)
			if err != nil {
				return nil, err
			}
			commands, err := h.listCommandsForTask(existing.ID)
			if err != nil {
				return nil, err
			}
			if session != nil && len(turns) > 0 && len(commands) > 0 {
				return &createdTask{Task: *existing, Session: *session, Turn: turns[0], Command: commands[0]}, nil
			}
		}
	}
	taskID := newID("tsk_")
	sessionID := newID("sess_")
	turnID := newID("turn_")
	commandID := newID("cmd_")
	title := input.Title
	if title == "" {
		title = input.Request
		if len(title) > 40 {
			title = title[:40]
		}
	}
	if len(title) > 120 {
		title = title[:120]
	}
	timeout := 1800
	if input.TimeoutSeconds != nil {
		timeout = *input.TimeoutSeconds
	}
	runProfile := "default"
	if input.RunProfile != nil && *input.RunProfile != "" {
		runProfile = *input.RunProfile
	}
	attachmentPayloads, _ := h.BuildAttachmentPayloads(input.AttachmentRows)
	payload, _ := json.Marshal(map[string]any{
		"request": input.Request, "cwd": input.Cwd, "run_profile": runProfile,
		"harness_session_id": nil, "timeout_seconds": timeout,
		"attachments": attachmentPayloads,
	})
	expiresAt := nowISO(now.Add(10 * time.Minute))
	tx, err := h.db.Begin()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(
		`INSERT INTO tasks
		 (id, title, source, device_id, session_id, status, stage, cwd, request, idempotency_key, evidence_kind, evidence_note, created_at, updated_at)
		 VALUES (?, ?, 'managed', ?, ?, 'queued', ?, ?, ?, ?, NULL, NULL, ?, ?)`,
		taskID, title, input.DeviceID, sessionID, "指令已持久化，等待 Edge 执行", input.Cwd, input.Request,
		input.IdempotencyKey, at, at,
	); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(
		`INSERT INTO sessions
		 (id, task_id, device_id, mode, status, cwd, harness, harness_version, harness_session_id, created_at, last_activity_at)
		 VALUES (?, ?, ?, 'managed', 'idle', ?, 'claude-code', NULL, NULL, ?, ?)`,
		sessionID, taskID, input.DeviceID, input.Cwd, at, at,
	); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(
		`INSERT INTO turns (id, task_id, session_id, seq, request, status, started_at, ended_at, exit_code, result, error, created_at)
		 VALUES (?, ?, ?, 1, ?, 'queued', NULL, NULL, NULL, NULL, NULL, ?)`,
		turnID, taskID, sessionID, input.Request, at,
	); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(
		`INSERT INTO commands (id, task_id, session_id, turn_id, device_id, kind, status, payload, error, created_at, delivered_at, acked_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, 'start_turn', 'pending', ?, NULL, ?, NULL, NULL, ?)`,
		commandID, taskID, sessionID, turnID, input.DeviceID, string(payload), at, expiresAt,
	); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if len(input.AttachmentIDs) > 0 {
		if err := h.bindAttachments(taskID, turnID, input.DeviceID, input.AttachmentIDs, now); err != nil {
			return nil, err
		}
	}
	task, _ := h.getTask(taskID)
	session, _ := h.getSession(sessionID)
	turns, _ := h.listTurns(sessionID)
	commands, _ := h.listCommandsForTask(taskID)
	return &createdTask{Task: *task, Session: *session, Turn: turns[0], Command: commands[0]}, nil
}

func (h *Hub) createTurn(taskID, request string, idempotencyKey *string, attachmentIDs []string, attachmentRows []AttachmentRow, now time.Time) (*createdTask, string) {
	task, err := h.getTask(taskID)
	if err != nil || task == nil {
		return nil, "task_not_found"
	}
	if task.Source != "managed" {
		return nil, "task_not_managed"
	}
	if task.Status == "failed" || task.Status == "cancelled" {
		return nil, "task_finished"
	}
	if h.countActiveCommands(taskID) > 0 {
		return nil, "turn_in_progress"
	}
	if task.SessionID == nil {
		return nil, "session_missing"
	}
	session, err := h.getSession(*task.SessionID)
	if err != nil || session == nil {
		return nil, "session_missing"
	}
	at := nowISO(now)
	turnID := newID("turn_")
	commandID := newID("cmd_")
	var seq int64
	_ = h.db.QueryRow("SELECT COALESCE(MAX(seq), 0) FROM turns WHERE session_id = ?", session.ID).Scan(&seq)
	harnessSessionID := any(nil)
	if session.HarnessSessionID != nil {
		harnessSessionID = *session.HarnessSessionID
	}
	attachmentPayloads, _ := h.BuildAttachmentPayloads(attachmentRows)
	payload, _ := json.Marshal(map[string]any{
		"request": request, "cwd": task.Cwd, "run_profile": "default",
		"harness_session_id": harnessSessionID, "timeout_seconds": 1800,
		"attachments": attachmentPayloads,
	})
	expiresAt := nowISO(now.Add(10 * time.Minute))
	tx, err := h.db.Begin()
	if err != nil {
		return nil, "internal_error"
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(
		`INSERT INTO turns (id, task_id, session_id, seq, request, status, started_at, ended_at, exit_code, result, error, created_at)
		 VALUES (?, ?, ?, ?, ?, 'queued', NULL, NULL, NULL, NULL, NULL, ?)`,
		turnID, taskID, session.ID, seq+1, request, at,
	); err != nil {
		return nil, "internal_error"
	}
	if _, err := tx.Exec(
		`INSERT INTO commands (id, task_id, session_id, turn_id, device_id, kind, status, payload, error, created_at, delivered_at, acked_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, 'send_turn', 'pending', ?, NULL, ?, NULL, NULL, ?)`,
		commandID, taskID, session.ID, turnID, task.DeviceID, string(payload), at, expiresAt,
	); err != nil {
		return nil, "internal_error"
	}
	if _, err := tx.Exec("UPDATE tasks SET status = 'running', stage = '追加输入已排队', updated_at = ? WHERE id = ?", at, taskID); err != nil {
		return nil, "internal_error"
	}
	if _, err := tx.Exec("UPDATE sessions SET last_activity_at = ? WHERE id = ?", at, session.ID); err != nil {
		return nil, "internal_error"
	}
	if err := tx.Commit(); err != nil {
		return nil, "internal_error"
	}
	if len(attachmentIDs) > 0 {
		if err := h.bindAttachments(taskID, turnID, task.DeviceID, attachmentIDs, now); err != nil {
			return nil, "internal_error"
		}
	}
	updatedTask, _ := h.getTask(taskID)
	updatedSession, _ := h.getSession(session.ID)
	turns, _ := h.listTurns(session.ID)
	commands, _ := h.listCommandsForTask(taskID)
	_ = idempotencyKey
	return &createdTask{Task: *updatedTask, Session: *updatedSession, Turn: turns[len(turns)-1], Command: commands[len(commands)-1]}, ""
}

func (h *Hub) cancelTask(taskID string, now time.Time) (*TaskRow, *CommandRow, bool, string) {
	task, err := h.getTask(taskID)
	if err != nil || task == nil {
		return nil, nil, false, "task_not_found"
	}
	if task.Status == "succeeded" || task.Status == "failed" || task.Status == "cancelled" {
		return nil, nil, false, "task_finished"
	}
	at := nowISO(now)
	rows, err := h.db.Query("SELECT * FROM commands WHERE task_id = ? AND status IN ('pending','delivered','accepted')", taskID)
	if err != nil {
		return nil, nil, false, "internal_error"
	}
	var active []CommandRow
	for rows.Next() {
		command, err := scanCommand(rows)
		if err != nil {
			rows.Close()
			return nil, nil, false, "internal_error"
		}
		active = append(active, command)
	}
	rows.Close()
	deliveredActive := false
	for _, command := range active {
		if command.Status != "pending" {
			deliveredActive = true
			break
		}
	}
	if !deliveredActive {
		tx, _ := h.db.Begin()
		defer func() { _ = tx.Rollback() }()
		_, _ = tx.Exec("UPDATE commands SET status = 'cancelled', error = '未投递即取消' WHERE task_id = ? AND status = 'pending'", taskID)
		_, _ = tx.Exec("UPDATE tasks SET status = 'cancelled', stage = '指令未投递，已直接取消', updated_at = ? WHERE id = ?", at, taskID)
		if task.SessionID != nil {
			_, _ = tx.Exec("UPDATE sessions SET status = 'ended', last_activity_at = ? WHERE id = ?", at, *task.SessionID)
		}
		_ = tx.Commit()
		updated, _ := h.getTask(taskID)
		return updated, nil, true, ""
	}
	commandID := newID("cmd_")
	expiresAt := nowISO(now.Add(10 * time.Minute))
	var sessionID any = nil
	if task.SessionID != nil {
		sessionID = *task.SessionID
	}
	tx, _ := h.db.Begin()
	defer func() { _ = tx.Rollback() }()
	_, _ = tx.Exec(
		`INSERT INTO commands (id, task_id, session_id, turn_id, device_id, kind, status, payload, error, created_at, delivered_at, acked_at, expires_at)
		 VALUES (?, ?, ?, NULL, ?, 'cancel', 'pending', '{}', NULL, ?, NULL, NULL, ?)`,
		commandID, taskID, sessionID, task.DeviceID, at, expiresAt,
	)
	_, _ = tx.Exec("UPDATE tasks SET status = 'cancel_requested', stage = '取消已记录，等待 Edge 确认', updated_at = ? WHERE id = ?", at, taskID)
	if err := tx.Commit(); err != nil {
		return nil, nil, false, "internal_error"
	}
	updated, _ := h.getTask(taskID)
	row := h.db.QueryRow("SELECT * FROM commands WHERE id = ?", commandID)
	command, _ := scanCommand(row)
	return updated, &command, false, ""
}

func (h *Hub) markCommandDelivered(commandID string, now time.Time) {
	at := nowISO(now)
	var kind, taskID string
	err := h.db.QueryRow("SELECT kind, task_id FROM commands WHERE id = ?", commandID).Scan(&kind, &taskID)
	if err != nil {
		return
	}
	_, _ = h.db.Exec("UPDATE commands SET status = 'delivered', delivered_at = ? WHERE id = ? AND status = 'pending'", at, commandID)
	if kind == "start_turn" || kind == "send_turn" {
		_, _ = h.db.Exec("UPDATE tasks SET stage = '指令已投递，等待 Edge ACK', updated_at = ? WHERE id = ? AND status = 'queued'", at, taskID)
	}
}

func (h *Hub) handleCommandAck(commandID, status, detail string, at string, now time.Time) {
	if at == "" {
		at = nowISO(now)
	}
	var kind, taskID string
	if err := h.db.QueryRow("SELECT kind, task_id FROM commands WHERE id = ?", commandID).Scan(&kind, &taskID); err != nil {
		return
	}
	if status == "done" {
		_, _ = h.db.Exec("UPDATE commands SET status = 'done', acked_at = ? WHERE id = ?", at, commandID)
		return
	}
	if status == "failed" {
		_, _ = h.db.Exec("UPDATE commands SET status = 'rejected', acked_at = ?, error = ? WHERE id = ?", at, defaultString(detail, "安装失败"), commandID)
		return
	}
	if status == "accepted" {
		_, _ = h.db.Exec("UPDATE commands SET status = 'accepted', acked_at = ? WHERE id = ?", at, commandID)
		if kind != "cancel" {
			_, _ = h.db.Exec("UPDATE tasks SET status = 'running', stage = 'Edge 已接受指令', updated_at = ? WHERE id = ? AND status IN ('queued','running')", at, taskID)
		} else {
			_, _ = h.db.Exec("UPDATE tasks SET status = 'cancel_requested', stage = 'Edge 已接受取消请求，等待进程结束', updated_at = ? WHERE id = ?", at, taskID)
		}
		return
	}
	_, _ = h.db.Exec("UPDATE commands SET status = 'rejected', acked_at = ?, error = ? WHERE id = ?", at, detail, commandID)
	_, _ = h.db.Exec("UPDATE tasks SET status = 'failed', stage = ?, updated_at = ? WHERE id = ? AND status NOT IN ('succeeded','cancelled')",
		"Edge 拒绝执行："+defaultString(detail, "未说明原因"), at, taskID)
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func (h *Hub) handleSessionStarted(taskID, sessionID, harnessSessionID, harnessVersion, at string, now time.Time) {
	if at == "" {
		at = nowISO(now)
	}
	_, _ = h.db.Exec("UPDATE sessions SET status = 'running', harness_session_id = COALESCE(?, harness_session_id), harness_version = COALESCE(?, harness_version), last_activity_at = ? WHERE id = ?",
		nullableString(harnessSessionID), nullableString(harnessVersion), at, sessionID)
	_, _ = h.db.Exec("UPDATE tasks SET status = 'running', stage = '会话已启动', updated_at = ? WHERE id = ? AND status IN ('queued','running')", at, taskID)
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (h *Hub) handleTurnStarted(taskID, sessionID, turnID, at string, now time.Time) {
	if at == "" {
		at = nowISO(now)
	}
	_, _ = h.db.Exec("UPDATE turns SET status = 'running', started_at = COALESCE(started_at, ?) WHERE id = ?", at, turnID)
	_, _ = h.db.Exec("UPDATE sessions SET status = 'running', last_activity_at = ? WHERE id = ?", at, sessionID)
	_, _ = h.db.Exec("UPDATE tasks SET status = 'running', stage = '执行中', updated_at = ? WHERE id = ? AND status IN ('queued','running')", at, taskID)
}

type IncomingEvent struct {
	EventID   string  `json:"event_id"`
	TaskID    *string `json:"task_id"`
	SessionID *string `json:"session_id"`
	TurnID    *string `json:"turn_id"`
	Type      string  `json:"type"`
	At        string  `json:"at"`
	Payload   any     `json:"payload"`
}

func (h *Hub) handleEventBatch(deviceID string, events []IncomingEvent, now time.Time) int {
	receivedAt := nowISO(now)
	tx, err := h.db.Begin()
	if err != nil {
		return 0
	}
	defer func() { _ = tx.Rollback() }()
	inserted := 0
	for _, event := range events {
		payload, _ := json.Marshal(event.Payload)
		if event.Payload == nil {
			payload = []byte("{}")
		}
		result, err := tx.Exec(
			`INSERT OR IGNORE INTO task_events (event_id, task_id, session_id, turn_id, device_id, type, payload, at, received_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			event.EventID, event.TaskID, event.SessionID, event.TurnID, deviceID, event.Type, string(payload), event.At, receivedAt,
		)
		if err != nil {
			continue
		}
		if rows, _ := result.RowsAffected(); rows > 0 {
			inserted++
		}
		if event.SessionID != nil {
			_, _ = tx.Exec("UPDATE sessions SET last_activity_at = ? WHERE id = ?", event.At, *event.SessionID)
		}
		if event.TaskID != nil {
			_, _ = tx.Exec("UPDATE tasks SET updated_at = ? WHERE id = ?", event.At, *event.TaskID)
		}
	}
	_ = tx.Commit()
	return inserted
}

func (h *Hub) handleTurnCompleted(input TurnCompletedInput, now time.Time) {
	at := input.At
	if at == "" {
		at = nowISO(now)
	}
	result := truncate(input.Result, 8000)
	errorText := truncate(input.Error, 4000)
	tx, err := h.db.Begin()
	if err != nil {
		return
	}
	defer func() { _ = tx.Rollback() }()
	_, _ = tx.Exec("UPDATE turns SET status = ?, ended_at = ?, exit_code = ?, result = ?, error = ? WHERE id = ?",
		input.Status, at, input.ExitCode, result, errorText, input.TurnID)
	_, _ = tx.Exec("UPDATE commands SET status = 'done' WHERE turn_id = ? AND status IN ('accepted','delivered','pending')", input.TurnID)
	_, _ = tx.Exec("UPDATE sessions SET status = 'idle', last_activity_at = ? WHERE id = ?", at, input.SessionID)
	switch input.Status {
	case "succeeded":
		evidence := "Claude 进程正常结束"
		if result != nil && *result != "" {
			evidence = *truncate(result, 500)
		}
		_, _ = tx.Exec("UPDATE tasks SET status = 'succeeded', stage = '已完成', evidence_kind = 'explicit_agent_report', evidence_note = ?, updated_at = ? WHERE id = ?", evidence, at, input.TaskID)
	case "cancelled":
		_, _ = tx.Exec("UPDATE tasks SET status = 'cancelled', stage = '已取消', evidence_note = ?, updated_at = ? WHERE id = ?", errorText, at, input.TaskID)
	default:
		_, _ = tx.Exec("UPDATE tasks SET status = 'failed', stage = ?, evidence_note = ?, updated_at = ? WHERE id = ?",
			"执行失败："+defaultString(derefString(errorText), "未知错误"), errorText, at, input.TaskID)
	}
	_ = tx.Commit()
}

func (h *Hub) expireCommands(now time.Time) {
	at := nowISO(now)
	rows, err := h.db.Query("SELECT id, task_id FROM commands WHERE status IN ('pending','delivered') AND expires_at <= ?", at)
	if err != nil {
		return
	}
	type stale struct{ id, taskID string }
	var items []stale
	for rows.Next() {
		var item stale
		if err := rows.Scan(&item.id, &item.taskID); err == nil {
			items = append(items, item)
		}
	}
	rows.Close()
	for _, item := range items {
		_, _ = h.db.Exec("UPDATE commands SET status = 'expired', error = '指令过期未执行' WHERE id = ?", item.id)
		_, _ = h.db.Exec("UPDATE tasks SET status = 'failed', stage = '指令过期，Edge 未执行', updated_at = ? WHERE id = ? AND status IN ('queued','running','cancel_requested')", at, item.taskID)
	}
}

func (h *Hub) commandMessage(command CommandRow) map[string]any {
	message := map[string]any{
		"type": "command", "command_id": command.ID, "kind": command.Kind,
		"task_id": command.TaskID, "created_at": command.CreatedAt, "expires_at": command.ExpiresAt,
	}
	if command.SessionID != nil {
		message["session_id"] = *command.SessionID
	} else {
		message["session_id"] = nil
	}
	if command.TurnID != nil {
		message["turn_id"] = *command.TurnID
	}
	if command.Kind != "cancel" {
		var payload any
		_ = json.Unmarshal([]byte(command.Payload), &payload)
		message["payload"] = payload
	}
	return message
}

func (h *Hub) deliverCommand(command CommandRow, now time.Time) bool {
	if !h.sendToDevice(command.DeviceID, h.commandMessage(command)) {
		return false
	}
	h.markCommandDelivered(command.ID, now)
	return true
}

func (h *Hub) deliverPendingCommands(deviceID string, now time.Time) int {
	commands, err := h.listDeliverableCommands(deviceID)
	if err != nil {
		return 0
	}
	count := 0
	for _, command := range commands {
		if h.deliverCommand(command, now) {
			count++
		}
	}
	return count
}

func truncate(value *string, max int) *string {
	if value == nil {
		return nil
	}
	text := *value
	if len(text) > max {
		text = text[:max]
	}
	return &text
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
