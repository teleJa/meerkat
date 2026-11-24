package hub

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	errPoolInvalid    = errors.New("invalid task pool input")
	errPoolConflict   = errors.New("task pool revision conflict")
	errPoolReassigned = errors.New("task pool assignment changed")
	errPoolForbidden  = errors.New("task pool access denied")
)

const poolFields = `id, title, request, status, assignee_kind, assignee_id, assignee_actor_id, assignment_rev, revision, created_by, attachment_ids, created_at, updated_at`

const poolDeliveryTimeout = 30 * time.Minute

type poolItem struct {
	ID              string   `json:"id"`
	Title           string   `json:"title"`
	Request         string   `json:"request"`
	Status          string   `json:"status"`
	AssigneeKind    string   `json:"assignee_kind,omitempty"`
	AssigneeID      string   `json:"assignee_id,omitempty"`
	AssigneeActorID string   `json:"assignee_actor_id,omitempty"`
	AssignmentRev   int      `json:"assignment_rev"`
	Revision        int      `json:"revision"`
	CreatedBy       string   `json:"created_by"`
	AttachmentIDs   []string `json:"attachment_ids"`
	CreatedAt       string   `json:"created_at"`
	UpdatedAt       string   `json:"updated_at"`
}

type poolTarget struct {
	Kind    string
	ID      string
	ActorID string
}

func (t poolTarget) valid() bool {
	return t.ID != "" && t.ActorID != "" && (t.Kind == "group" || t.Kind == "bot" && t.ID == t.ActorID)
}

type poolCreate struct {
	Request       string   `json:"request"`
	AttachmentIDs []string `json:"attachment_ids"`
}

type poolEventBody struct {
	Note     string   `json:"note,omitempty"`
	Evidence []string `json:"evidence,omitempty"`
}

func scanPoolItem(row interface{ Scan(...any) error }) (poolItem, error) {
	var item poolItem
	var attachments string
	err := row.Scan(&item.ID, &item.Title, &item.Request, &item.Status, &item.AssigneeKind, &item.AssigneeID,
		&item.AssigneeActorID, &item.AssignmentRev, &item.Revision, &item.CreatedBy, &attachments, &item.CreatedAt, &item.UpdatedAt)
	if err == nil {
		err = json.Unmarshal([]byte(attachments), &item.AttachmentIDs)
	}
	return item, err
}

func poolItemIn(ctx context.Context, tx *sql.Tx, id string) (poolItem, error) {
	return scanPoolItem(tx.QueryRowContext(ctx, "SELECT "+poolFields+" FROM task_pool_items WHERE id = ?", id))
}

func insertPoolEvent(ctx context.Context, tx *sql.Tx, item poolItem, id, actor, kind string, payload []byte, now time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO task_pool_events (item_id,event_id,actor_id,assignment_rev,type,payload,at) VALUES (?,?,?,?,?,?,?)`,
		item.ID, id, actor, item.AssignmentRev, kind, string(payload), nowISO(now))
	return err
}

func enqueuePoolDelivery(ctx context.Context, tx *sql.Tx, item poolItem, purpose, eventID, target string, now time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO task_pool_deliveries (id,item_id,assignment_rev,purpose,source_event_id,target_id,status,created_at)
		VALUES (?,?,?,?,?,?,'pending',?)`, "tpd_"+randomHex(16), item.ID, item.AssignmentRev, purpose, eventID, target, nowISO(now))
	return err
}

func (h *Hub) createPoolItem(ctx context.Context, input poolCreate, key string, target poolTarget, chiefID string, now time.Time) (poolItem, bool, error) {
	input.Request = strings.TrimSpace(input.Request)
	if input.Request == "" || len(input.Request) > 8192 || !utf8.ValidString(input.Request) || len(key) > 200 || len(input.AttachmentIDs) > maxAttachmentsTask {
		return poolItem{}, false, errPoolInvalid
	}
	if target != (poolTarget{}) && !target.valid() {
		return poolItem{}, false, errPoolInvalid
	}
	if input.AttachmentIDs == nil {
		input.AttachmentIDs = []string{}
	}
	fingerprint, err := json.Marshal(struct {
		Input poolCreate
		Kind  string
		ID    string
	}{input, target.Kind, target.ID})
	if err != nil {
		return poolItem{}, false, err
	}
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return poolItem{}, false, err
	}
	defer tx.Rollback()
	if key != "" {
		var id, hash string
		err := tx.QueryRowContext(ctx, `SELECT id,request_hash FROM task_pool_items WHERE idempotency_key = ?`, key).Scan(&id, &hash)
		switch {
		case err == nil:
			if hash != tokenHash(string(fingerprint)) {
				return poolItem{}, false, errPoolConflict
			}
			item, err := poolItemIn(ctx, tx, id)
			return item, false, err
		case !errors.Is(err, sql.ErrNoRows):
			return poolItem{}, false, err
		}
	}
	seen := make(map[string]bool, len(input.AttachmentIDs))
	var total int64
	for _, id := range input.AttachmentIDs {
		if seen[id] {
			return poolItem{}, false, errPoolInvalid
		}
		seen[id] = true
		var size int64
		err := tx.QueryRowContext(ctx, `SELECT size FROM attachments WHERE id = ? AND task_id IS NULL`, id).Scan(&size)
		if errors.Is(err, sql.ErrNoRows) {
			return poolItem{}, false, errPoolInvalid
		}
		if err != nil {
			return poolItem{}, false, err
		}
		total += size
	}
	if total > maxAttachmentsTotal {
		return poolItem{}, false, errPoolInvalid
	}
	title := []rune(strings.TrimSpace(strings.SplitN(input.Request, "\n", 2)[0]))
	if len(title) > 100 {
		title = title[:100]
	}
	item := poolItem{ID: "tp_" + randomHex(16), Title: string(title), Request: input.Request, Status: "unassigned", Revision: 1,
		CreatedBy: "user", AttachmentIDs: input.AttachmentIDs, CreatedAt: nowISO(now), UpdatedAt: nowISO(now)}
	purpose, recipient := "assign_requested", chiefID
	if target.valid() {
		item.Status, item.AssignmentRev = "processing", 1
		item.AssigneeKind, item.AssigneeID, item.AssigneeActorID = target.Kind, target.ID, target.ActorID
		purpose, recipient = "assigned", target.ID
	}
	attachments, err := json.Marshal(item.AttachmentIDs)
	if err != nil {
		return poolItem{}, false, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO task_pool_items (`+poolFields+`,idempotency_key,request_hash)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,NULLIF(?,''),?)`, item.ID, item.Title, item.Request, item.Status, item.AssigneeKind,
		item.AssigneeID, item.AssigneeActorID, item.AssignmentRev, item.Revision, item.CreatedBy, string(attachments), item.CreatedAt,
		item.UpdatedAt, key, tokenHash(string(fingerprint)))
	if err != nil {
		return poolItem{}, false, err
	}
	if err := insertPoolEvent(ctx, tx, item, "created", "user", "created", []byte(`{}`), now); err != nil {
		return poolItem{}, false, err
	}
	if recipient != "" {
		if err := enqueuePoolDelivery(ctx, tx, item, purpose, "created", recipient, now); err != nil {
			return poolItem{}, false, err
		}
	}
	return item, true, tx.Commit()
}

func (h *Hub) assignPoolItem(ctx context.Context, id string, revision int, actor, credentialHash string, target poolTarget, now time.Time) (poolItem, error) {
	if !target.valid() || actor == "" {
		return poolItem{}, errPoolInvalid
	}
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return poolItem{}, err
	}
	defer tx.Rollback()
	item, err := poolItemIn(ctx, tx, id)
	if err != nil {
		return poolItem{}, err
	}
	if credentialHash != "" {
		if actor != h.cfg.TaskPoolChiefBotID {
			return poolItem{}, errPoolForbidden
		}
		if err := verifyPoolCredential(ctx, tx, actor, credentialHash); err != nil {
			return poolItem{}, err
		}
		if item.Status != "unassigned" {
			return poolItem{}, errPoolConflict
		}
	}
	if item.Revision != revision || item.Status == "completed" || item.Status == "cancelled" || item.Status == "review" {
		return poolItem{}, errPoolConflict
	}
	item.AssignmentRev++
	item.Revision++
	item.AssigneeKind, item.AssigneeID, item.AssigneeActorID = target.Kind, target.ID, target.ActorID
	item.Status, item.UpdatedAt = "processing", nowISO(now)
	if _, err := tx.ExecContext(ctx, `UPDATE task_pool_items SET status=?, assignee_kind=?, assignee_id=?, assignee_actor_id=?,
		assignment_rev=?,revision=?,updated_at=? WHERE id=?`, item.Status, item.AssigneeKind, item.AssigneeID, item.AssigneeActorID,
		item.AssignmentRev, item.Revision, item.UpdatedAt, item.ID); err != nil {
		return poolItem{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE task_pool_deliveries SET status='superseded'
		WHERE item_id=? AND assignment_rev < ? AND status IN ('pending','sending','queued','uncertain')`, item.ID, item.AssignmentRev); err != nil {
		return poolItem{}, err
	}
	if err := insertPoolEvent(ctx, tx, item, "assigned", actor, "assigned", []byte(`{}`), now); err != nil {
		return poolItem{}, err
	}
	if err := enqueuePoolDelivery(ctx, tx, item, "assigned", "assigned", target.ID, now); err != nil {
		return poolItem{}, err
	}
	return item, tx.Commit()
}

// poolReportGrant is produced by the authenticated transport for a single assignment.
type poolReportGrant struct {
	ItemID         string
	ActorID        string
	AssignmentRev  int
	ExpiresAt      time.Time
	CredentialHash string
}

func (h *Hub) reportPoolEvent(ctx context.Context, grant poolReportGrant, eventID, kind string, body poolEventBody, now time.Time) (poolItem, error) {
	if !now.Before(grant.ExpiresAt) || grant.ActorID == "" || grant.AssignmentRev < 1 {
		return poolItem{}, errPoolForbidden
	}
	body.Note = strings.TrimSpace(body.Note)
	if eventID == "" || len(eventID) > 128 || body.Note == "" || len(body.Note) > 8192 || len(body.Evidence) > 8 {
		return poolItem{}, errPoolInvalid
	}
	for _, evidence := range body.Evidence {
		if strings.TrimSpace(evidence) == "" || len(evidence) > 2048 {
			return poolItem{}, errPoolInvalid
		}
	}
	if kind == "delivered" && len(body.Evidence) == 0 {
		return poolItem{}, errPoolInvalid
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return poolItem{}, err
	}
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return poolItem{}, err
	}
	defer tx.Rollback()
	if grant.CredentialHash != "" {
		if err := verifyPoolCredential(ctx, tx, grant.ActorID, grant.CredentialHash); err != nil {
			return poolItem{}, err
		}
	}
	item, err := poolItemIn(ctx, tx, grant.ItemID)
	if err != nil {
		return poolItem{}, err
	}
	if item.AssignmentRev != grant.AssignmentRev {
		return poolItem{}, errPoolReassigned
	}
	if item.AssigneeActorID != grant.ActorID {
		return poolItem{}, errPoolForbidden
	}
	var storedKind, storedBody string
	err = tx.QueryRowContext(ctx, `SELECT type,payload FROM task_pool_events WHERE item_id=? AND assignment_rev=? AND event_id=?`,
		item.ID, item.AssignmentRev, "report:"+eventID).Scan(&storedKind, &storedBody)
	if err == nil {
		if storedKind != kind || storedBody != string(encoded) {
			return poolItem{}, errPoolConflict
		}
		return item, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return poolItem{}, err
	}
	if item.Status != "processing" && item.Status != "blocked" {
		return poolItem{}, errPoolConflict
	}
	switch kind {
	case "accepted", "progress":
		item.Status = "processing"
	case "blocked":
		item.Status = "blocked"
	case "delivered":
		item.Status = "review"
	default:
		return poolItem{}, errPoolInvalid
	}
	if err := updatePoolStatus(ctx, tx, &item, now); err != nil {
		return poolItem{}, err
	}
	if err := insertPoolEvent(ctx, tx, item, "report:"+eventID, grant.ActorID, kind, encoded, now); err != nil {
		return poolItem{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE task_pool_deliveries SET status='acknowledged',error_code=''
		WHERE item_id=? AND assignment_rev=? AND status IN ('pending','sending','queued','uncertain','failed')`, item.ID, item.AssignmentRev); err != nil {
		return poolItem{}, err
	}
	if err := tx.Commit(); err != nil {
		return poolItem{}, err
	}
	if kind == "blocked" || kind == "delivered" {
		event := kind
		if kind == "delivered" {
			event = "review_ready"
		}
		h.notifyPool(event, item.ID, item.Title, item.ID+":"+strconv.Itoa(item.Revision))
	}
	return item, nil
}

// reviewPoolItem changes review state after the transport has verified the user.
func (h *Hub) reviewPoolItem(ctx context.Context, id string, revision int, decision, note string, now time.Time) (poolItem, error) {
	note = strings.TrimSpace(note)
	if len(note) > 8192 || (decision == "return" && note == "") {
		return poolItem{}, errPoolInvalid
	}
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return poolItem{}, err
	}
	defer tx.Rollback()
	item, err := poolItemIn(ctx, tx, id)
	if err != nil {
		return poolItem{}, err
	}
	if item.Revision != revision || item.Status == "cancelled" || item.Status == "completed" || (decision != "cancel" && item.Status != "review") {
		return poolItem{}, errPoolConflict
	}
	var event string
	switch decision {
	case "approve":
		item.Status, event = "completed", "approved"
	case "return":
		item.Status, event = "processing", "returned"
	case "cancel":
		item.Status, event = "cancelled", "cancelled"
	default:
		return poolItem{}, errPoolInvalid
	}
	payload, err := json.Marshal(poolEventBody{Note: note})
	if err != nil {
		return poolItem{}, err
	}
	if err := updatePoolStatus(ctx, tx, &item, now); err != nil {
		return poolItem{}, err
	}
	eventID := event + ":" + randomHex(16)
	if err := insertPoolEvent(ctx, tx, item, eventID, "user", event, payload, now); err != nil {
		return poolItem{}, err
	}
	if decision == "return" {
		if err := enqueuePoolDelivery(ctx, tx, item, "returned", eventID, item.AssigneeID, now); err != nil {
			return poolItem{}, err
		}
	}
	if decision == "cancel" {
		if _, err := tx.ExecContext(ctx, `UPDATE task_pool_deliveries SET status='superseded'
			WHERE item_id=? AND status IN ('pending','sending','queued','uncertain')`, item.ID); err != nil {
			return poolItem{}, err
		}
	}
	return item, tx.Commit()
}

func updatePoolStatus(ctx context.Context, tx *sql.Tx, item *poolItem, now time.Time) error {
	item.Revision++
	item.UpdatedAt = nowISO(now)
	_, err := tx.ExecContext(ctx, `UPDATE task_pool_items SET status=?,revision=?,updated_at=? WHERE id=?`, item.Status, item.Revision, item.UpdatedAt, item.ID)
	return err
}
