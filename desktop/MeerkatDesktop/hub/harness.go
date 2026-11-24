package hub

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func (h *Hub) setHarnessURL(deviceID, rawURL string, now time.Time) error {
	_, err := h.db.Exec(
		`INSERT INTO device_harness (device_id, url, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT(device_id) DO UPDATE SET url = excluded.url, updated_at = excluded.updated_at`,
		deviceID, rawURL, nowISO(now),
	)
	if err == nil {
		h.signalNotificationDevices()
	}
	return err
}

func (h *Hub) getHarnessURL(deviceID string) (string, error) {
	var rawURL string
	err := h.db.QueryRow("SELECT url FROM device_harness WHERE device_id = ?", deviceID).Scan(&rawURL)
	return rawURL, err
}

func (h *Hub) handleSetHarnessURL(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid json")
		return
	}
	rawURL := strings.TrimSpace(body.URL)
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		writeError(w, http.StatusBadRequest, "invalid_url", "必须提供 http/https Harness URL")
		return
	}
	device, err := h.getDevice(pathID(r))
	if err != nil || device == nil {
		writeError(w, http.StatusNotFound, "device_not_found", "设备不存在")
		return
	}
	if err := h.setHarnessURL(device.ID, rawURL, time.Now()); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "保存 Harness URL 失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "device_id": device.ID})
}

func (h *Hub) handleHarnessRedirect(w http.ResponseWriter, r *http.Request) {
	device, err := h.getDevice(pathID(r))
	if err != nil || device == nil {
		writeError(w, http.StatusNotFound, "device_not_found", "设备不存在")
		return
	}
	rawURL, err := h.getHarnessURL(device.ID)
	if err != nil || strings.TrimSpace(rawURL) == "" {
		writeError(w, http.StatusNotFound, "harness_not_configured", "该设备还没有配置远程 Harness")
		return
	}
	http.Redirect(w, r, rawURL, http.StatusFound)
}
func (h *Hub) handleHarnessURL(w http.ResponseWriter, r *http.Request) {
	device, err := h.getDevice(pathID(r))
	if err != nil || device == nil {
		writeError(w, http.StatusNotFound, "device_not_found", "设备不存在")
		return
	}
	rawURL, err := h.getHarnessURL(device.ID)
	if err != nil || strings.TrimSpace(rawURL) == "" {
		writeError(w, http.StatusNotFound, "harness_not_configured", "该设备还没有配置远程 Harness")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"url": rawURL})
}
func (h *Hub) handleHarnessProxyURL(w http.ResponseWriter, r *http.Request) {
	deviceID := pathID(r)
	localURL, err := h.ensureHarnessProxy(deviceID)
	if err != nil {
		writeError(w, http.StatusBadGateway, "harness_proxy_unavailable", err.Error())
		return
	}
	parsed, parseErr := url.Parse(localURL)
	if parseErr != nil {
		writeError(w, http.StatusBadGateway, "harness_proxy_invalid", parseErr.Error())
		return
	}
	path := "/harness-proxy/" + deviceID + "/"
	if parsed.RawQuery != "" {
		path += "?" + parsed.RawQuery
	}
	writeJSON(w, http.StatusOK, map[string]any{"url": localURL, "path": path})
}
func (h *Hub) hasHarness(deviceID string) bool {
	value, err := h.getHarnessURL(deviceID)
	return err == nil && strings.TrimSpace(value) != ""
}

// DSHProfileItem 是 Edge 上报的单个 profile。
type DSHProfileItem struct {
	Name      string `json:"name"`
	Available bool   `json:"available"`
	Running   bool   `json:"running"`
}

// DSHState 是 Edge 上报的 DSH 状态快照；不含带 token 的 URL。
type DSHState struct {
	Profile   string           `json:"profile"`
	Mode      string           `json:"mode"`
	Status    string           `json:"status"`
	Owner     string           `json:"owner,omitempty"`
	Error     string           `json:"error,omitempty"`
	Installed bool             `json:"installed"`
	Profiles  []DSHProfileItem `json:"profiles"`
	UpdatedAt string           `json:"updated_at,omitempty"`
}

func (h *Hub) setDSHSelection(deviceID, profile, mode string, now time.Time) error {
	_, err := h.db.Exec(
		`INSERT INTO device_dsh (device_id, profile, mode, updated_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(device_id) DO UPDATE SET profile = excluded.profile, mode = excluded.mode, updated_at = excluded.updated_at`,
		deviceID, profile, mode, nowISO(now),
	)
	return err
}

func (h *Hub) getDSHSelection(deviceID string) (profile, mode string, found bool, err error) {
	row := h.db.QueryRow("SELECT profile, mode FROM device_dsh WHERE device_id = ?", deviceID)
	scanErr := row.Scan(&profile, &mode)
	if scanErr == nil {
		return profile, mode, true, nil
	}
	if errors.Is(scanErr, sql.ErrNoRows) {
		return "", "", false, nil
	}
	return "", "", false, scanErr
}

func (h *Hub) setDSHState(deviceID string, state DSHState, now time.Time) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	_, err = h.db.Exec(
		`INSERT INTO device_dsh_state (device_id, payload, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT(device_id) DO UPDATE SET payload = excluded.payload, updated_at = excluded.updated_at`,
		deviceID, string(data), nowISO(now),
	)
	return err
}

func (h *Hub) getDSHState(deviceID string) (DSHState, bool, error) {
	var payload string
	err := h.db.QueryRow("SELECT payload FROM device_dsh_state WHERE device_id = ?", deviceID).Scan(&payload)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return DSHState{}, false, nil
		}
		return DSHState{}, false, err
	}
	var state DSHState
	if err := json.Unmarshal([]byte(payload), &state); err != nil {
		return DSHState{}, false, err
	}
	return state, true, nil
}

// dshSummary 合并保存的选择与最近一次上报，供设备列表和 DSH 页面使用。
func (h *Hub) dshSummary(deviceID string) map[string]any {
	profile, mode, _, _ := h.getDSHSelection(deviceID)
	state, hasState, _ := h.getDSHState(deviceID)
	if hasState {
		if profile == "" {
			profile = state.Profile
		}
		if mode == "" {
			mode = state.Mode
		}
	}
	status := "stopped"
	owner, lastError := "", ""
	installed := false
	profiles := []DSHProfileItem{}
	if hasState {
		status = state.Status
		owner = state.Owner
		lastError = state.Error
		installed = state.Installed
		if state.Profiles != nil {
			profiles = state.Profiles
		}
	}
	if status == "" {
		status = "stopped"
	}
	payload := map[string]any{
		"profile": profile, "mode": mode, "status": status, "owner": owner,
		"error": lastError, "installed": installed, "profiles": profiles,
		"has_url": h.hasHarness(deviceID),
	}
	return payload
}

func (h *Hub) handleDSHStatus(w http.ResponseWriter, r *http.Request) {
	device, err := h.getDevice(pathID(r))
	if err != nil || device == nil {
		writeError(w, http.StatusNotFound, "device_not_found", "设备不存在")
		return
	}
	online := false
	h.connMu.Lock()
	online = h.connections[device.ID] != nil
	h.connMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"device_id": device.ID,
		"online":    online,
		"dsh":       h.dshSummary(device.ID),
	})
}

type dshCommandError string

func (e dshCommandError) Error() string { return string(e) }

const (
	errDSHOffline dshCommandError = "设备不在线"
	errDSHSend    dshCommandError = "下发 DSH 指令失败"
)

func (h *Hub) sendDSHCommand(deviceID, action, profile, mode string) error {
	h.connMu.Lock()
	client := h.connections[deviceID]
	h.connMu.Unlock()
	if client == nil {
		return errDSHOffline
	}
	message := map[string]any{"type": "harness.profile", "request_id": newID("dsh_"), "action": action}
	if strings.TrimSpace(profile) != "" {
		message["profile"] = strings.TrimSpace(profile)
	}
	if strings.TrimSpace(mode) != "" {
		message["mode"] = strings.TrimSpace(mode)
	}
	if !client.send(message) {
		return errDSHSend
	}
	return nil
}

func validDSHProfile(profile string) bool {
	profile = strings.TrimSpace(profile)
	if profile == "" || len(profile) > 64 || strings.Contains(profile, "..") {
		return false
	}
	for _, char := range profile {
		switch {
		case char >= 'a' && char <= 'z', char >= 'A' && char <= 'Z', char >= '0' && char <= '9':
		case char == '.', char == '_', char == '-':
		default:
			return false
		}
	}
	return true
}

func (h *Hub) handleSetDSHProfile(w http.ResponseWriter, r *http.Request) {
	device, err := h.getDevice(pathID(r))
	if err != nil || device == nil {
		writeError(w, http.StatusNotFound, "device_not_found", "设备不存在")
		return
	}
	var body struct {
		Profile string `json:"profile"`
		Mode    string `json:"mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid json")
		return
	}
	if !validDSHProfile(body.Profile) {
		writeError(w, http.StatusBadRequest, "invalid_profile", "profile 名不合法")
		return
	}
	_, mode, _, _ := h.getDSHSelection(device.ID)
	profile := strings.TrimSpace(body.Profile)
	if body.Mode == "connect" || body.Mode == "launch" {
		mode = body.Mode
	}
	if err := h.setDSHSelection(device.ID, profile, mode, time.Now()); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "保存 DSH 选择失败")
		return
	}
	// 选择只保存，不触发启动/连接；设备卡片上的动作按钮才下发命令，
	// 避免切换下拉框就改变正在运行的实例。
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "profile": profile, "mode": mode})
}

func (h *Hub) handleDSHAction(w http.ResponseWriter, r *http.Request) {
	device, err := h.getDevice(pathID(r))
	if err != nil || device == nil {
		writeError(w, http.StatusNotFound, "device_not_found", "设备不存在")
		return
	}
	var body struct {
		Action  string `json:"action"`
		Profile string `json:"profile"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid json")
		return
	}
	action := strings.TrimSpace(body.Action)
	if action != "connect" && action != "launch" && action != "stop" {
		writeError(w, http.StatusBadRequest, "invalid_action", "action 只支持 connect、launch、stop")
		return
	}
	profile, mode, _, _ := h.getDSHSelection(device.ID)
	if strings.TrimSpace(body.Profile) != "" {
		if !validDSHProfile(body.Profile) {
			writeError(w, http.StatusBadRequest, "invalid_profile", "profile 名不合法")
			return
		}
		profile = strings.TrimSpace(body.Profile)
	}
	if action != "stop" {
		mode = action
	}
	if profile == "" {
		writeError(w, http.StatusBadRequest, "profile_required", "请先选择 profile")
		return
	}
	if err := h.setDSHSelection(device.ID, profile, mode, time.Now()); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "保存 DSH 选择失败")
		return
	}
	if err := h.sendDSHCommand(device.ID, action, profile, mode); err != nil {
		writeError(w, http.StatusConflict, "device_offline", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "action": action, "profile": profile, "mode": mode})
}

func (h *Hub) handleCreateDSHProfile(w http.ResponseWriter, r *http.Request) {
	device, err := h.getDevice(pathID(r))
	if err != nil || device == nil {
		writeError(w, http.StatusNotFound, "device_not_found", "设备不存在")
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid json")
		return
	}
	name := strings.TrimSpace(body.Name)
	if !validDSHProfile(name) {
		writeError(w, http.StatusBadRequest, "invalid_profile", "profile 名不合法")
		return
	}
	if err := h.sendDSHCommand(device.ID, "create", name, ""); err != nil {
		writeError(w, http.StatusConflict, "device_offline", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "name": name})
}

// syncDSHSelection 在 Edge 重连时下发 Hub 保存的选择。
func (h *Hub) syncDSHSelection(deviceID string) {
	profile, mode, found, _ := h.getDSHSelection(deviceID)
	if !found || profile == "" || mode == "" {
		return
	}
	if err := h.sendDSHCommand(deviceID, "select", profile, mode); err != nil {
		log.Printf("同步 DSH 选择失败（device=%s）：%v", deviceID, err)
	}
}
