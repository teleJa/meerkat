package hub

import (
	"net/http"
	"time"
)

// handleDSHInstall 向指定 Edge 下发安装 DSH 的指令。
// DSH 安装完成后 Edge 会重新启动 dsh web 并通过 harness.update 上报地址。
func (h *Hub) handleDSHInstall(w http.ResponseWriter, r *http.Request) {
	device, err := h.getDevice(pathID(r))
	if err != nil || device == nil {
		writeError(w, http.StatusNotFound, "device_not_found", "设备不存在")
		return
	}
	if h.hasHarness(device.ID) {
		writeJSON(w, http.StatusOK, map[string]any{"already": true})
		return
	}
	h.connMu.Lock()
	online := h.connections[device.ID] != nil
	h.connMu.Unlock()
	if !online {
		writeError(w, http.StatusConflict, "device_offline", "设备不在线，无法下发安装指令")
		return
	}
	now := time.Now()
	commandID := newID("cmd_")
	taskID := newID("tsk_dsh_")
	if _, err := h.db.Exec(
		`INSERT INTO commands (id, task_id, session_id, turn_id, device_id, kind, status, payload, error, created_at, delivered_at, acked_at, expires_at)
		 VALUES (?, ?, NULL, NULL, ?, 'install_dsh', 'pending', '{}', NULL, ?, NULL, NULL, ?)`,
		commandID, taskID, device.ID, nowISO(now), nowISO(now.Add(30*time.Minute)),
	); err != nil {
		writeError(w, http.StatusInternalServerError, "command_failed", err.Error())
		return
	}
	h.deliverPendingCommands(device.ID, now)
	writeJSON(w, http.StatusAccepted, map[string]any{"command_id": commandID, "already": false})
}
