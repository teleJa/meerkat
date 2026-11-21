package hub

import (
	"context"
	"database/sql"
	"log"
	"net/url"
	"strings"
	"time"
)

type poolDelivery struct {
	ID, ItemID, Target, Purpose string
	Kind, Actor                 string
}

func (h *Hub) claimPoolDelivery(ctx context.Context, now time.Time) (poolDelivery, error) {
	var delivery poolDelivery
	err := h.db.QueryRowContext(ctx, `UPDATE task_pool_deliveries SET status='sending',started_at=?
		WHERE rowid=(SELECT d.rowid FROM task_pool_deliveries d JOIN task_pool_items i ON i.id=d.item_id
			WHERE d.status='pending' AND i.assignment_rev=d.assignment_rev AND i.status IN ('unassigned','processing') ORDER BY d.rowid LIMIT 1)
		RETURNING id,item_id,target_id,purpose,
		(SELECT assignee_kind FROM task_pool_items WHERE id=item_id),
		(SELECT assignee_actor_id FROM task_pool_items WHERE id=item_id)`, nowISO(now)).
		Scan(&delivery.ID, &delivery.ItemID, &delivery.Target, &delivery.Purpose, &delivery.Kind, &delivery.Actor)
	return delivery, err
}

func (h *Hub) settlePoolDelivery(ctx context.Context, id, status, code string, now time.Time) error {
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE task_pool_deliveries SET status=?,error_code=?,queued_at=CASE WHEN ?='queued' THEN ? ELSE NULL END
		WHERE id=? AND status='sending'`, status, code, status, nowISO(now), id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 1 && status == "failed" {
		_, err := tx.ExecContext(ctx, `UPDATE task_pool_items SET status='blocked',revision=revision+1,updated_at=?
			WHERE status='processing' AND EXISTS (SELECT 1 FROM task_pool_deliveries d WHERE d.id=? AND d.item_id=task_pool_items.id AND d.assignment_rev=task_pool_items.assignment_rev)`, nowISO(now), id)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (h *Hub) sendPoolDelivery(ctx context.Context, delivery poolDelivery) (string, string) {
	board, err := url.Parse(h.cfg.TaskPoolBoardURL)
	if err != nil || (board.Scheme != "http" && board.Scheme != "https") || board.Host == "" || board.User != nil || board.RawQuery != "" || board.ForceQuery {
		return "failed", "task_pool_board_url_invalid"
	}
	board.Fragment = "/live/task-pool/" + delivery.ItemID
	board.RawFragment = ""
	client, err := h.taskPoolBotClient(h.cfg.TaskPoolBotDeviceID)
	if err != nil {
		return "failed", "ds_bot_unavailable"
	}
	if delivery.Purpose == "assign_requested" {
		delivery.Kind, delivery.Actor = "bot", delivery.Target
	}
	directory, err := client.directory(ctx)
	if err != nil {
		return "failed", "ds_bot_unavailable"
	}
	target, ok := directory.target(delivery.Kind, delivery.Target)
	if !ok || target.ActorID != delivery.Actor {
		return "failed", "delivery_target_changed"
	}
	return client.send(ctx, target.ID, delivery.Purpose, board.String())
}

func (h *Hub) runPoolDeliveries(ctx context.Context) {
	// An interrupted send may already be in DS Bot's queue. Preserve the ambiguity.
	if _, err := h.db.ExecContext(ctx, `UPDATE task_pool_deliveries SET status='uncertain',error_code='delivery_unconfirmed' WHERE status='sending'`); err != nil {
		log.Print("任务池投递恢复失败")
		return
	}
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		now := time.Now()
		if _, err := h.expirePoolDeliveries(ctx, now); err != nil && ctx.Err() == nil {
			log.Print("任务池投递超时检查失败")
		}
		if strings.TrimSpace(h.cfg.TaskPoolBotDeviceID) != "" {
			delivery, err := h.claimPoolDelivery(ctx, now)
			if err == nil {
				status, code := h.sendPoolDelivery(ctx, delivery)
				if err := h.settlePoolDelivery(ctx, delivery.ID, status, code, time.Now()); err != nil && ctx.Err() == nil {
					log.Print("任务池投递回执保存失败")
				}
			} else if err != sql.ErrNoRows && ctx.Err() == nil {
				log.Print("任务池读取投递队列失败")
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

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
