package hub

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"
)

type poolDeliveryReceipt struct {
	ID        string `json:"delivery_id"`
	Status    string `json:"status"`
	ErrorCode string `json:"error_code,omitempty"`
	QueuedAt  string `json:"queued_at,omitempty"`
	CanRetry  bool   `json:"can_retry"`
}

type poolSnapshot struct {
	poolItem
	Delivery       *poolDeliveryReceipt `json:"delivery,omitempty"`
	LatestProgress *poolEventBody       `json:"latest_progress,omitempty"`
}

type poolHistoryEvent struct {
	ID            string        `json:"event_id"`
	ActorID       string        `json:"actor_id"`
	AssignmentRev int           `json:"assignment_rev"`
	Type          string        `json:"type"`
	Payload       poolEventBody `json:"payload"`
	At            string        `json:"at"`
}

func writePoolError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		writeError(w, http.StatusNotFound, "task_pool_not_found", "任务不存在")
	case errors.Is(err, errPoolInvalid):
		writeError(w, http.StatusBadRequest, "invalid_task_pool_request", "任务信息或操作无效")
	case errors.Is(err, errPoolReassigned):
		writeError(w, http.StatusConflict, "assignment_changed", "任务已改派，请停止回写")
	case errors.Is(err, errPoolConflict):
		writeError(w, http.StatusConflict, "task_pool_conflict", "任务状态或版本已变化，请刷新后重试")
	case errors.Is(err, errPoolBotUnavailable):
		writeError(w, http.StatusServiceUnavailable, "task_pool_bot_unavailable", "DS Bot 暂不可用，请稍后重试")
	case errors.Is(err, errPoolForbidden):
		writeError(w, http.StatusForbidden, "task_pool_forbidden", "当前凭据无权操作这张任务卡")
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", "中心内部错误")
	}
}

type poolAssignmentRequest struct {
	Kind     string `json:"assignee_kind"`
	ID       string `json:"assignee_id"`
	Revision int    `json:"expected_revision"`
}

func readPoolRequest(w http.ResponseWriter, r *http.Request, input any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(input); err != nil || decoder.Decode(new(any)) != io.EOF {
		writePoolError(w, errPoolInvalid)
		return false
	}
	return true
}

func (h *Hub) handlePoolTargets(w http.ResponseWriter, r *http.Request) {
	client, err := h.taskPoolBotClient(h.cfg.TaskPoolBotDeviceID)
	if err != nil {
		writePoolError(w, err)
		return
	}
	directory, err := client.directory(r.Context())
	if err != nil {
		writePoolError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"targets": directory.options()})
}

type poolTargetOption struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (directory poolBotDirectory) options() []poolTargetOption {
	targets := make([]poolTargetOption, 0, len(directory.Bots)+len(directory.Rooms))
	for _, bot := range directory.Bots {
		targets = append(targets, poolTargetOption{"bot", bot.ID, bot.Name})
	}
	for _, room := range directory.Rooms {
		if _, enabled := directory.target("group", room.ID); enabled {
			targets = append(targets, poolTargetOption{"group", room.ID, room.Name})
		}
	}
	return targets
}

func (h *Hub) resolvePoolTarget(r *http.Request, request poolAssignmentRequest) (poolTarget, error) {
	if request.Kind != "group" && request.Kind != "bot" || request.ID == "" {
		return poolTarget{}, errPoolInvalid
	}
	client, err := h.taskPoolBotClient(h.cfg.TaskPoolBotDeviceID)
	if err != nil {
		return poolTarget{}, err
	}
	directory, err := client.directory(r.Context())
	if err != nil {
		return poolTarget{}, err
	}
	target, ok := directory.target(request.Kind, request.ID)
	if !ok {
		return poolTarget{}, errPoolInvalid
	}
	return target, nil
}

func (h *Hub) handleCreatePoolItem(w http.ResponseWriter, r *http.Request) {
	var input struct {
		poolCreate
		poolAssignmentRequest
	}
	if !readPoolRequest(w, r, &input) {
		return
	}
	var target poolTarget
	if input.Kind != "" || input.ID != "" {
		resolved, err := h.resolvePoolTarget(r, input.poolAssignmentRequest)
		if err != nil {
			writePoolError(w, err)
			return
		}
		target = resolved
	}
	item, created, err := h.createPoolItem(r.Context(), input.poolCreate, r.Header.Get("Idempotency-Key"), target, h.cfg.TaskPoolChiefBotID, time.Now())
	if err != nil {
		writePoolError(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, map[string]any{"item": item})
}

func (h *Hub) handleAssignPoolItem(w http.ResponseWriter, r *http.Request) {
	var input poolAssignmentRequest
	if !readPoolRequest(w, r, &input) {
		return
	}
	target, err := h.resolvePoolTarget(r, input)
	if err != nil {
		writePoolError(w, err)
		return
	}
	item, err := h.assignPoolItem(r.Context(), r.PathValue("id"), input.Revision, "user", "", target, time.Now())
	if err != nil {
		writePoolError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": item})
}

func (h *Hub) handleRetryPoolDelivery(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Revision int `json:"expected_revision"`
	}
	if !readPoolRequest(w, r, &input) {
		return
	}
	item, err := h.retryPoolDelivery(r.Context(), r.PathValue("id"), input.Revision, "user", time.Now())
	if err != nil {
		writePoolError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": item})
}

func readPoolSnapshot(r *http.Request, tx *sql.Tx, item poolItem, now time.Time) (poolSnapshot, error) {
	ctx := r.Context()
	snapshot := poolSnapshot{poolItem: item}
	var receipt poolDeliveryReceipt
	var sent string
	err := tx.QueryRowContext(ctx, `SELECT id,status,error_code,COALESCE(queued_at,''),COALESCE(queued_at,started_at,created_at)
		FROM task_pool_deliveries WHERE item_id=? AND assignment_rev=? ORDER BY rowid DESC LIMIT 1`, item.ID, item.AssignmentRev).
		Scan(&receipt.ID, &receipt.Status, &receipt.ErrorCode, &receipt.QueuedAt, &sent)
	if err == nil {
		at, err := time.Parse(time.RFC3339Nano, sent)
		if err != nil {
			return poolSnapshot{}, err
		}
		receipt.CanRetry = (item.Status == "blocked" || item.Status == "unassigned") &&
			(receipt.Status == "failed" || ((receipt.Status == "uncertain" || receipt.Status == "queued") && now.Sub(at) >= poolDeliveryTimeout))
		snapshot.Delivery = &receipt
	} else if !errors.Is(err, sql.ErrNoRows) {
		return poolSnapshot{}, err
	}
	var payload string
	err = tx.QueryRowContext(ctx, `SELECT payload FROM task_pool_events WHERE item_id=? AND assignment_rev=?
		AND type IN ('accepted','progress','blocked','delivered') ORDER BY seq DESC LIMIT 1`, item.ID, item.AssignmentRev).Scan(&payload)
	if err == nil {
		if err := json.Unmarshal([]byte(payload), &snapshot.LatestProgress); err != nil {
			return poolSnapshot{}, err
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return poolSnapshot{}, err
	}
	return snapshot, nil
}

func (h *Hub) handleListPool(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	now := time.Now()
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		writePoolError(w, err)
		return
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT `+poolFields+` FROM task_pool_items ORDER BY rowid DESC`)
	if err != nil {
		writePoolError(w, err)
		return
	}
	var items []poolItem
	for rows.Next() {
		item, err := scanPoolItem(rows)
		if err != nil {
			_ = rows.Close()
			writePoolError(w, err)
			return
		}
		items = append(items, item)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		writePoolError(w, err)
		return
	}
	snapshots := make([]poolSnapshot, 0, len(items))
	for _, item := range items {
		snapshot, err := readPoolSnapshot(r, tx, item, now)
		if err != nil {
			writePoolError(w, err)
			return
		}
		snapshots = append(snapshots, snapshot)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": snapshots})
}

func (h *Hub) handlePoolDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		writePoolError(w, err)
		return
	}
	defer tx.Rollback()
	item, err := poolItemIn(ctx, tx, r.PathValue("id"))
	if err != nil {
		writePoolError(w, err)
		return
	}
	snapshot, err := readPoolSnapshot(r, tx, item, time.Now())
	if err != nil {
		writePoolError(w, err)
		return
	}
	events, err := tx.QueryContext(ctx, `SELECT event_id,actor_id,assignment_rev,type,payload,at FROM task_pool_events WHERE item_id=? ORDER BY seq`, item.ID)
	if err != nil {
		writePoolError(w, err)
		return
	}
	defer events.Close()
	history := []poolHistoryEvent{}
	for events.Next() {
		var event poolHistoryEvent
		var payload string
		if err := events.Scan(&event.ID, &event.ActorID, &event.AssignmentRev, &event.Type, &payload, &event.At); err != nil {
			writePoolError(w, err)
			return
		}
		if err := json.Unmarshal([]byte(payload), &event.Payload); err != nil {
			writePoolError(w, err)
			return
		}
		history = append(history, event)
	}
	if err := events.Err(); err != nil {
		writePoolError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": snapshot, "events": history})
}
