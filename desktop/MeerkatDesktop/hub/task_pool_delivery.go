package hub

import (
	"context"
	"database/sql"
	"time"
)

// expirePoolDeliveries moves only current, unacknowledged assignments to blocked.
// An old delivery cannot block a reassigned card or one that already made progress.
func (h *Hub) expirePoolDeliveries(ctx context.Context, now time.Time) (int64, error) {
	result, err := h.db.ExecContext(ctx, `UPDATE task_pool_items AS item
		SET status='blocked', revision=revision+1, updated_at=?
		WHERE item.status='processing' AND EXISTS (
			SELECT 1 FROM task_pool_deliveries AS delivery
			WHERE delivery.item_id=item.id AND delivery.assignment_rev=item.assignment_rev
			AND delivery.status IN ('queued','uncertain')
			AND julianday(COALESCE(delivery.queued_at,delivery.started_at,delivery.created_at)) <= julianday(?)
			AND delivery.rowid=(SELECT MAX(latest.rowid) FROM task_pool_deliveries AS latest
				WHERE latest.item_id=item.id AND latest.assignment_rev=item.assignment_rev)
		)`, nowISO(now), nowISO(now.Add(-poolDeliveryTimeout)))
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (h *Hub) retryPoolDelivery(ctx context.Context, id string, revision int, actor string, now time.Time) (poolItem, error) {
	if actor == "" {
		return poolItem{}, errPoolForbidden
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
	if item.Revision != revision || (item.Status != "blocked" && item.Status != "unassigned") {
		return poolItem{}, errPoolConflict
	}
	var target, purpose, status, started string
	err = tx.QueryRowContext(ctx, `SELECT target_id,purpose,status,COALESCE(queued_at,started_at,created_at)
		FROM task_pool_deliveries WHERE item_id=? AND assignment_rev=? ORDER BY rowid DESC LIMIT 1`, id, item.AssignmentRev).
		Scan(&target, &purpose, &status, &started)
	if err == sql.ErrNoRows {
		return poolItem{}, errPoolConflict
	}
	if err != nil {
		return poolItem{}, err
	}
	if status != "failed" {
		at, err := time.Parse(time.RFC3339Nano, started)
		if err != nil {
			return poolItem{}, err
		}
		if (status != "queued" && status != "uncertain") || now.Sub(at) < poolDeliveryTimeout {
			return poolItem{}, errPoolConflict
		}
	}
	eventID := "retry:" + randomHex(16)
	if item.AssignmentRev > 0 {
		item.Status = "processing"
	}
	if err := updatePoolStatus(ctx, tx, &item, now); err != nil {
		return poolItem{}, err
	}
	if err := insertPoolEvent(ctx, tx, item, eventID, actor, "delivery_retried", []byte(`{}`), now); err != nil {
		return poolItem{}, err
	}
	if err := enqueuePoolDelivery(ctx, tx, item, purpose, eventID, target, now); err != nil {
		return poolItem{}, err
	}
	return item, tx.Commit()
}
