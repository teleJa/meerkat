//go:build !android

package main

import (
	"testing"

	"github.com/wailsapp/wails/v3/pkg/services/notifications"
	"meerkat/desktop/hub"
)

func TestClickedNotificationUsesOriginalTask(t *testing.T) {
	for _, cached := range []string{"", "another-run"} {
		if cached != "" {
			rememberTurnNotification("mk-1", hub.TurnNotification{ItemID: cached})
		}
		got := clickedNotification(notifications.NotificationResponse{ID: "mk-1", UserInfo: map[string]any{"item_id": "selected-task"}})
		if got.ItemID != "selected-task" {
			t.Fatalf("cache %q routed to %q", cached, got.ItemID)
		}
	}
	rememberTurnNotification("mk-2", hub.TurnNotification{ItemID: "legacy-task"})
	if got := clickedNotification(notifications.NotificationResponse{ID: "mk-2"}); got.ItemID != "legacy-task" {
		t.Fatal(got)
	}
}
