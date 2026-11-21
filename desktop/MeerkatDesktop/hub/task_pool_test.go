package hub

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestTaskPoolAssignmentAndReport(t *testing.T) {
	db, err := openDB(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	h := &Hub{db: db}
	ctx := context.Background()
	now := time.Now()
	input := poolCreate{Request: "检查 @工程群 的构建"}
	item, created, err := h.createPoolItem(ctx, input, "form-1", poolTarget{}, "chief", now)
	if err != nil || !created || item.Status != "unassigned" || item.Revision != 1 {
		t.Fatalf("create: %+v %v %v", item, created, err)
	}
	assertTaskPoolCounts(t, h, 1, 1, 1)
	firstID := item.ID
	item, created, err = h.createPoolItem(ctx, input, "form-1", poolTarget{}, "chief", now)
	if err != nil || created || item.ID != firstID {
		t.Fatalf("replay create: %+v %v %v", item, created, err)
	}
	if _, _, err := h.createPoolItem(ctx, poolCreate{Request: "another"}, "form-1", poolTarget{}, "chief", now); !errors.Is(err, errPoolConflict) {
		t.Fatalf("create key with different payload: %v", err)
	}
	item, err = h.assignPoolItem(ctx, item.ID, item.Revision, "chief", poolTarget{Kind: "group", ID: "group", ActorID: "admin"}, now)
	if err != nil || item.AssignmentRev != 1 || item.Status != "processing" {
		t.Fatalf("assign: %+v %v", item, err)
	}
	if _, err := h.assignPoolItem(ctx, item.ID, 1, "chief", poolTarget{Kind: "bot", ID: "b", ActorID: "b"}, now); !errors.Is(err, errPoolConflict) {
		t.Fatalf("stale assignment: %v", err)
	}
	grant := poolReportGrant{ItemID: item.ID, ActorID: "admin", AssignmentRev: 1, ExpiresAt: now.Add(time.Minute)}
	foreign := grant
	foreign.ActorID = "member"
	body := poolEventBody{Note: "已接手"}
	if _, err := h.reportPoolEvent(ctx, foreign, "event-1", "accepted", body, now); !errors.Is(err, errPoolForbidden) {
		t.Fatalf("foreign group member: %v", err)
	}
	if _, err := h.reportPoolEvent(ctx, grant, "event-1", "accepted", body, grant.ExpiresAt); !errors.Is(err, errPoolForbidden) {
		t.Fatalf("expired report: %v", err)
	}
	item, err = h.reportPoolEvent(ctx, grant, "event-1", "accepted", body, now)
	if err != nil || item.Revision != 3 {
		t.Fatalf("accept: %+v %v", item, err)
	}
	item, err = h.reportPoolEvent(ctx, grant, "event-1", "accepted", body, now)
	if err != nil || item.Revision != 3 {
		t.Fatalf("replay report: %+v %v", item, err)
	}
	if _, err := h.reportPoolEvent(ctx, grant, "event-1", "progress", body, now); !errors.Is(err, errPoolConflict) {
		t.Fatalf("event key with different payload: %v", err)
	}
	var delivery string
	if err := db.QueryRow(`SELECT status FROM task_pool_deliveries WHERE item_id=? AND assignment_rev=1`, item.ID).Scan(&delivery); err != nil || delivery != "acknowledged" {
		t.Fatalf("delivery: %q %v", delivery, err)
	}
	item, err = h.assignPoolItem(ctx, item.ID, item.Revision, "user", poolTarget{Kind: "bot", ID: "b", ActorID: "b"}, now)
	if err != nil || item.AssignmentRev != 2 {
		t.Fatalf("reassign: %+v %v", item, err)
	}
	if _, err := h.reportPoolEvent(ctx, grant, "event-2", "progress", body, now); !errors.Is(err, errPoolReassigned) {
		t.Fatalf("old assignment: %v", err)
	}
	grant.ActorID, grant.AssignmentRev = "b", 2
	if _, err := h.reportPoolEvent(ctx, grant, "event-1", "delivered", body, now); !errors.Is(err, errPoolInvalid) {
		t.Fatalf("delivery without evidence: %v", err)
	}
	item, err = h.reportPoolEvent(ctx, grant, "event-1", "delivered", poolEventBody{Note: "通过", Evidence: []string{"go test ./..."}}, now)
	if err != nil || item.Status != "review" {
		t.Fatalf("deliver: %+v %v", item, err)
	}
	assertTaskPoolCounts(t, h, 1, 5, 3)
	var deviceTasks int
	if err := db.QueryRow("SELECT COUNT(*) FROM tasks").Scan(&deviceTasks); err != nil || deviceTasks != 0 {
		t.Fatalf("existing device tasks changed: %d %v", deviceTasks, err)
	}
}

func TestTaskPoolDeliveryTimeoutAndRetry(t *testing.T) {
	db, err := openDB(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	h := &Hub{db: db}
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)
	target := poolTarget{Kind: "bot", ID: "bot", ActorID: "bot"}
	item, _, err := h.createPoolItem(ctx, poolCreate{Request: "核查投递"}, "", target, "", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE task_pool_deliveries SET status='uncertain' WHERE item_id=?`, item.ID); err != nil {
		t.Fatal(err)
	}
	deadline := now.Add(poolDeliveryTimeout)
	if count, err := h.expirePoolDeliveries(ctx, deadline.Add(-time.Second)); err != nil || count != 0 {
		t.Fatalf("before deadline: %d %v", count, err)
	}
	if count, err := h.expirePoolDeliveries(ctx, deadline); err != nil || count != 1 {
		t.Fatalf("at deadline: %d %v", count, err)
	}
	if _, err := h.retryPoolDelivery(ctx, item.ID, item.Revision, "user", deadline); !errors.Is(err, errPoolConflict) {
		t.Fatalf("stale retry: %v", err)
	}
	updated, err := h.retryPoolDelivery(ctx, item.ID, item.Revision+1, "user", deadline)
	if err != nil || updated.AssignmentRev != item.AssignmentRev || updated.Status != "processing" {
		t.Fatalf("retry: %+v %v", updated, err)
	}
	if count, err := h.expirePoolDeliveries(ctx, deadline); err != nil || count != 0 {
		t.Fatalf("old uncertain delivery after retry: %d %v", count, err)
	}
	assertTaskPoolCounts(t, h, 1, 2, 2)
}

func assertTaskPoolCounts(t *testing.T, h *Hub, items, events, deliveries int) {
	t.Helper()
	var gotItems, gotEvents, gotDeliveries int
	err := h.db.QueryRow(`SELECT (SELECT COUNT(*) FROM task_pool_items), (SELECT COUNT(*) FROM task_pool_events),
		(SELECT COUNT(*) FROM task_pool_deliveries)`).Scan(&gotItems, &gotEvents, &gotDeliveries)
	if err != nil || gotItems != items || gotEvents != events || gotDeliveries != deliveries {
		t.Fatalf("counts: got %d items, %d events, %d deliveries (err %v), want %d, %d, %d", gotItems, gotEvents, gotDeliveries, err, items, events, deliveries)
	}
}
