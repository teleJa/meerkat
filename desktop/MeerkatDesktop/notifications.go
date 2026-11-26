//go:build !android

package main

import (
	"fmt"
	"log"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"meerkat/desktop/hub"

	"github.com/wailsapp/wails/v3/pkg/services/notifications"
)

var (
	notificationService *notifications.NotificationService

	notificationMu       sync.Mutex
	notificationPerm     string
	notificationSentByID = map[string]hub.TurnNotification{}
)

const notificationRecentLimit = 50

// notificationsAvailable 判断当前进程能否注册系统通知服务。
// macOS 上 Wails 的通知要求进程运行在带 bundle identifier 的 .app 内；
// 直接从命令行跑裸二进制（开发/调试）时只降级成「不可用」，不阻塞启动。
func notificationsAvailable() bool {
	if runtime.GOOS != "darwin" {
		return true
	}
	executable, err := os.Executable()
	if err != nil {
		return false
	}
	return strings.Contains(executable, ".app/Contents/MacOS/")
}

// setupNotificationService 注册 Wails 通知服务，并在用户点击通知时回到对应设备。
func setupNotificationService() {
	if !notificationsAvailable() {
		log.Printf("桌面通知不可用：当前进程不在 App Bundle 内（回合结束不会弹通知）")
		return
	}
	notificationService = notifications.New()
	notificationService.OnNotificationResponse(func(result notifications.NotificationResult) {
		if result.Error != nil {
			log.Printf("桌面通知回调失败: %v", result.Error)
			return
		}
		openTurnNotification(clickedNotification(result.Response))
	})
}

// startNotificationWatcher 等本机设备注册完成后启动 Hub 侧的回合 watcher。
// 多设备时这里只是把更多 deviceID 交给 Hub，watcher 本身按设备管理。
func startNotificationWatcher() {
	go func() {
		// 首次运行先确认通知权限；未授权时由系统弹一次授权框。
		time.Sleep(4 * time.Second)
		if notificationPermissionStatus(true) != "granted" {
			log.Printf("通知权限状态: %s（可在 设置 → 桌面通知 中重新申请）", requestNotificationPermission())
		}
	}()
	go runNotificationSubscriber()
}

func sendTurnNotification(record hub.TurnNotification) error {
	if notificationService == nil || notificationPermissionStatus(false) != "granted" {
		return fmt.Errorf("系统通知权限尚未授予")
	}
	notificationID := fmt.Sprintf("mk-%d", record.ID)
	rememberTurnNotification(notificationID, record)
	options := notifications.NotificationOptions{
		ID:    notificationID,
		Title: notificationTitle(record),
		Body:  notificationBody(record),
		Data: map[string]any{
			"item_id":      record.ItemID,
			"device_id":    record.DeviceID,
			"session_id":   record.SessionID,
			"session":      record.SessionTitle,
			"notification": record.ID,
		},
	}
	if record.Reason == "idle" {
		options.Subtitle = "回合结束 · " + formatDuration(record.DurationMS)
	} else if record.Reason == "input-required" {
		options.Subtitle = "回合暂停 · 等待你确认"
	}
	return notificationService.SendNotification(options)
}

func notificationTitle(record hub.TurnNotification) string {
	if title := map[string]string{"pool.review_ready": "任务待验收", "pool.blocked": "任务受阻", "pool.delivery_failed": "任务投递失败", "bot.group_decision": "群内待决策", "bot.chief_mention": "群内提醒", "bot.chief_dm": "主智能体私信", "bot.action_required": "等待你回答", "bot.approval": "等待你审批"}[record.Kind]; title != "" {
		return title
	}
	device := strings.TrimSpace(record.DeviceName)
	title := strings.TrimSpace(record.SessionTitle)
	switch {
	case device == "" && title == "":
		return "Meerkat"
	case title == "":
		return device
	case device == "":
		return title
	default:
		return device + " · " + title
	}
}

func notificationBody(record hub.TurnNotification) string {
	if record.ItemID != "" {
		return record.SessionTitle
	}
	if record.Reason == "input-required" {
		return "回合需要审批或回答，打开 Meerkat 继续。"
	}
	if strings.TrimSpace(record.Excerpt) == "" {
		return "回合已完成（没有文本回复）。"
	}
	return record.Excerpt
}

func formatDuration(milliseconds int64) string {
	if milliseconds <= 0 {
		return "刚刚"
	}
	seconds := milliseconds / 1000
	if seconds < 60 {
		return fmt.Sprintf("%d 秒", seconds)
	}
	minutes := seconds / 60
	if minutes < 60 {
		return fmt.Sprintf("%d 分 %d 秒", minutes, seconds%60)
	}
	return fmt.Sprintf("%d 小时 %d 分", minutes/60, minutes%60)
}

func rememberTurnNotification(id string, record hub.TurnNotification) {
	notificationMu.Lock()
	defer notificationMu.Unlock()
	notificationSentByID[id] = record
	if len(notificationSentByID) > notificationRecentLimit {
		for key := range notificationSentByID {
			if key != id {
				delete(notificationSentByID, key)
			}
			if len(notificationSentByID) <= notificationRecentLimit/2 {
				break
			}
		}
	}
}

func takeTurnNotification(id string) (hub.TurnNotification, bool) {
	notificationMu.Lock()
	defer notificationMu.Unlock()
	record, ok := notificationSentByID[id]
	if ok {
		delete(notificationSentByID, id)
	}
	return record, ok
}

// The original payload survives process restarts, while stream IDs and the
// in-memory cache belong to the current run. Older pool toasts lack item_id.
func clickedNotification(response notifications.NotificationResponse) hub.TurnNotification {
	cached, _ := takeTurnNotification(response.ID)
	item, hasItem := response.UserInfo["item_id"].(string)
	device, _ := response.UserInfo["device_id"].(string)
	if hasItem || (cached.ItemID == "" && device != "") {
		return hub.TurnNotification{ItemID: item, DeviceID: device}
	}
	return cached
}

// openTurnNotification 把窗口带到前台并跳到对应设备的 DSH 页。
func openTurnNotification(record hub.TurnNotification) {
	if mainWindow != nil {
		mainWindow.Show()
		mainWindow.Focus()
		if record.ItemID != "" {
			mainWindow.ExecJS(fmt.Sprintf("window.location.hash = %q", "#/live/task-pool/"+url.PathEscape(record.ItemID)))
		} else if strings.TrimSpace(record.DeviceID) != "" {
			hash := "#/live/harness/" + url.PathEscape(record.DeviceID)
			mainWindow.ExecJS(fmt.Sprintf("window.location.hash = %q", hash))
		}
	}
}

// notificationPermissionStatus 返回 granted / denied / unavailable，结果缓存。
func notificationPermissionStatus(refresh bool) string {
	notificationMu.Lock()
	cached := notificationPerm
	notificationMu.Unlock()
	if cached != "" && !refresh {
		return cached
	}
	status := "unavailable"
	if notificationService != nil {
		granted, err := notificationService.CheckNotificationAuthorization()
		switch {
		case err != nil:
			log.Printf("读取通知权限失败: %v", err)
			status = "denied"
		case granted:
			status = "granted"
		default:
			status = "denied"
		}
	}
	notificationMu.Lock()
	notificationPerm = status
	notificationMu.Unlock()
	return status
}

// requestNotificationPermission 主动申请权限；macOS 上被拒后不会再次弹窗。
func requestNotificationPermission() string {
	if notificationService == nil {
		return "unavailable"
	}
	granted, err := notificationService.RequestNotificationAuthorization()
	status := "denied"
	if err != nil {
		log.Printf("申请通知权限失败: %v", err)
	} else if granted {
		status = "granted"
	}
	notificationMu.Lock()
	notificationPerm = status
	notificationMu.Unlock()
	return status
}
