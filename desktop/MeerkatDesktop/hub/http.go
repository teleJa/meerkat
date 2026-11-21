package hub

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("content-type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func (h *Hub) registerHTTP(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/health", h.handleHealth)
	mux.HandleFunc("POST /api/session", h.handleLogin)
	mux.HandleFunc("GET /api/session", h.handleSessionInfo)
	mux.HandleFunc("DELETE /api/session", h.handleLogout)
	mux.HandleFunc("POST /api/pairing-tokens", h.auth(h.handleCreatePairingToken, true))
	mux.HandleFunc("POST /api/mobile/pairing-tokens", h.authAdmin(h.handleCreateMobilePairingToken, true))
	mux.HandleFunc("POST /api/mobile/register", h.handleRegisterMobile)
	mux.HandleFunc("GET /api/mobile/credentials", h.authAdmin(h.handleListMobileCredentials, false))
	mux.HandleFunc("POST /api/mobile/credentials/{id}/revoke", h.authAdmin(h.handleRevokeMobileCredential, true))
	mux.HandleFunc("POST /api/mobile/credentials/rotate", h.handleRotateMobileCredential)
	mux.HandleFunc("POST /api/edges/register", h.handleRegister)
	mux.HandleFunc("GET /api/edges/ws", h.handleWS)
	mux.HandleFunc("GET /api/devices", h.authWithScope(h.handleDevices, MobileScopeDevices, false))
	mux.HandleFunc("GET /api/devices/{id}", h.authWithScope(h.handleDevice, MobileScopeDevices, false))
	mux.HandleFunc("GET /api/devices/{id}/metrics", h.authWithScope(h.handleDeviceMetrics, MobileScopeDevices, false))
	mux.HandleFunc("POST /api/devices/{id}/revoke", h.auth(h.handleRevoke, true))
	mux.HandleFunc("POST /api/devices/{id}/harness", h.auth(h.handleSetHarnessURL, true))
	mux.HandleFunc("GET /api/devices/{id}/harness", h.authWithScope(h.handleHarnessRedirect, MobileScopeHarness, false))
	mux.HandleFunc("GET /api/devices/{id}/harness-url", h.authWithScope(h.handleHarnessURL, MobileScopeHarness, false))
	mux.HandleFunc("GET /api/devices/{id}/harness-proxy", h.authWithScope(h.handleHarnessProxyURL, MobileScopeHarness, false))
	mux.HandleFunc("/harness-proxy/{id}", h.authWithScope(h.handleHarnessPathProxy, MobileScopeHarness, false))
	mux.HandleFunc("/harness-proxy/{id}/{rest...}", h.authWithScope(h.handleHarnessPathProxy, MobileScopeHarness, false))
	mux.HandleFunc("POST /api/devices/{id}/dsh/install", h.auth(h.handleDSHInstall, true))
	mux.HandleFunc("GET /api/devices/{id}/dsh", h.authWithScope(h.handleDSHStatus, MobileScopeDevices, false))
	mux.HandleFunc("POST /api/devices/{id}/dsh/profile", h.auth(h.handleSetDSHProfile, true))
	mux.HandleFunc("POST /api/devices/{id}/dsh/action", h.auth(h.handleDSHAction, true))
	mux.HandleFunc("POST /api/devices/{id}/dsh/profiles", h.auth(h.handleCreateDSHProfile, true))
	mux.HandleFunc("GET /api/network/tsnet", h.authAdmin(h.handleTSNetStatus, false))
	mux.HandleFunc("POST /api/network/tsnet/start", h.auth(h.handleTSNetStart, true))
	mux.HandleFunc("POST /api/network/tsnet/stop", h.auth(h.handleTSNetStop, true))
	mux.HandleFunc("GET /api/events", h.authWithScope(h.handleEvents, MobileScopeDevices, false))
	mux.HandleFunc("GET /api/notifications/settings", h.authAdmin(h.handleNotificationSettings, false))
	mux.HandleFunc("POST /api/notifications/settings", h.authAdmin(h.handleUpdateNotificationSettings, true))
	mux.HandleFunc("GET /api/task-pool", h.authAdmin(h.handleListPool, false))
	mux.HandleFunc("GET /api/task-pool/{id}", h.authAdmin(h.handlePoolDetail, false))
	mux.HandleFunc("GET /api/task-pool/targets", h.authAdmin(h.handlePoolTargets, false))
	mux.HandleFunc("POST /api/task-pool", h.authAdmin(h.handleCreatePoolItem, true))
	mux.HandleFunc("POST /api/task-pool/{id}/assign", h.authAdmin(h.handleAssignPoolItem, true))
	mux.HandleFunc("POST /api/task-pool/{id}/retry", h.authAdmin(h.handleRetryPoolDelivery, true))
	mux.HandleFunc("POST /api/task-pool/config/bot-credentials/{botID}", h.authAdmin(h.handlePoolBotCredential, true))
	mux.HandleFunc("DELETE /api/task-pool/config/bot-credentials/{botID}", h.authAdmin(h.handlePoolBotCredential, true))
	mux.HandleFunc("POST /api/task-pool/mcp", h.handlePoolMCP)
	mux.HandleFunc("GET /api/task-pool/mcp", h.handlePoolMCP)
	mux.HandleFunc("DELETE /api/task-pool/mcp", h.handlePoolMCP)
	mux.HandleFunc("POST /api/tasks", h.auth(h.handleCreateTask, true))
	mux.HandleFunc("GET /api/tasks", h.authWithScope(h.handleListTasks, MobileScopeTasks, false))
	mux.HandleFunc("GET /api/tasks/{id}", h.authWithScope(h.handleTaskDetail, MobileScopeTasks, false))
	mux.HandleFunc("GET /api/artifacts/{id}", h.authWithScope(h.handleArtifactContent, MobileScopeTasks, false))
	mux.HandleFunc("POST /api/attachments", h.auth(h.handleUploadAttachment, true))
	mux.HandleFunc("GET /api/attachments/{id}", h.authWithScope(h.handleAttachmentContent, MobileScopeTasks, false))
	mux.HandleFunc("POST /api/tasks/{id}/turns", h.auth(h.handleAddTurn, true))
	mux.HandleFunc("POST /api/tasks/{id}/cancel", h.auth(h.handleCancelTask, true))
	mux.HandleFunc("GET /api/sessions", h.authWithScope(h.handleListSessions, MobileScopeTasks, false))
	mux.HandleFunc("GET /api/sessions/{id}", h.authWithScope(h.handleSessionDetail, MobileScopeTasks, false))
	mux.HandleFunc("POST /api/_internal/task-events", h.auth(h.handleInternalEvents, true))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "接口不存在: "+r.Method+" "+r.URL.Path)
	})
}

type authHandlerFunc func(http.ResponseWriter, *http.Request)

func (h *Hub) auth(next authHandlerFunc, mutation bool) http.HandlerFunc {
	return h.authWithScope(next, "", mutation)
}

func (h *Hub) authWithScope(next authHandlerFunc, scope string, mutation bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		auth, ok := h.requireAuth(w, r, mutation)
		if !ok {
			return
		}
		if auth.Via == "mobile" && scope != "" && !auth.HasScope(scope) {
			writeError(w, http.StatusForbidden, "mobile_scope_denied", "移动设备凭证缺少权限："+scope)
			return
		}
		next(w, r)
	}
}

// authAdmin is for Hub administration endpoints that mobile credentials must
// never reach, including pairing-code creation and credential management.
func (h *Hub) authAdmin(next authHandlerFunc, mutation bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		auth, ok := h.requireAuth(w, r, mutation)
		if !ok {
			return
		}
		if auth.Via == "mobile" {
			writeError(w, http.StatusForbidden, "mobile_admin_required", "该操作需要 Hub 管理员权限")
			return
		}
		next(w, r)
	}
}

func (h *Hub) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "version": h.cfg.Version, "time": nowISO(time.Now()),
		"uptime_seconds": int(time.Since(h.startedAt).Seconds()),
	})
}

func (h *Hub) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("x-meerkat-request") != "1" {
		writeError(w, http.StatusForbidden, "csrf_required", "缺少 x-meerkat-request 请求头")
		return
	}
	var body struct {
		Token string `json:"token"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if strings.TrimSpace(body.Token) == "" || tokenHash(strings.TrimSpace(body.Token)) != tokenHash(h.adminToken) {
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "访问令牌无效")
		return
	}
	h.cleanupExpiredSessions(time.Now())
	sessionID, expiresAt, err := h.createWebSession(12*60*60, time.Now())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "中心内部错误")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: "meerkat_session", Value: sessionID, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "expires_at": expiresAt})
}

func (h *Hub) handleSessionInfo(w http.ResponseWriter, r *http.Request) {
	auth := h.authenticate(r)
	if auth == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "未登录")
		return
	}
	switch auth.Via {
	case "session":
		writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "via": "session", "expires_at": auth.ExpiresAt})
	case "mobile":
		writeJSON(w, http.StatusOK, map[string]any{
			"authenticated": true, "via": "mobile", "credential_id": auth.CredentialID,
			"scopes": auth.Scopes, "expires_at": auth.ExpiresAt,
		})
	default:
		writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "via": "bearer"})
	}
}

func (h *Hub) handleLogout(w http.ResponseWriter, r *http.Request) {
	auth := h.authenticate(r)
	if auth == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "缺少或错误的管理凭据")
		return
	}
	if auth.Via == "session" {
		h.deleteWebSession(auth.SessionID)
	}
	http.SetCookie(w, &http.Cookie{Name: "meerkat_session", Value: "", Path: "/", HttpOnly: true, MaxAge: -1})
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": false})
}

func (h *Hub) handleCreatePairingToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Label      string `json:"label"`
		TTLSeconds int    `json:"ttl_seconds"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	ttl := body.TTLSeconds
	if ttl == 0 {
		ttl = 600
	}
	if ttl < 60 {
		ttl = 60
	}
	if ttl > 3600 {
		ttl = 3600
	}
	var label *string
	if strings.TrimSpace(body.Label) != "" {
		value := strings.TrimSpace(body.Label)
		if len(value) > 120 {
			value = value[:120]
		}
		label = &value
	}
	token, expiresAt, err := h.createPairingToken(label, ttl, time.Now())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "中心内部错误")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"token": token, "expires_at": expiresAt, "label": label})
}

type registerDeviceRequest struct {
	PairingToken    string     `json:"pairing_token"`
	ProtocolVersion string     `json:"protocol_version"`
	EdgeVersion     string     `json:"edge_version"`
	Device          DeviceInfo `json:"device"`
}

func (h *Hub) handleRegister(w http.ResponseWriter, r *http.Request) {
	var body registerDeviceRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_device", "invalid json")
		return
	}
	if body.ProtocolVersion != ProtocolVersion {
		writeError(w, http.StatusBadRequest, "unsupported_protocol", "仅支持协议版本 "+ProtocolVersion)
		return
	}
	if strings.TrimSpace(body.Device.Name) == "" || strings.TrimSpace(body.Device.Hostname) == "" ||
		strings.TrimSpace(body.Device.OS) == "" || strings.TrimSpace(body.Device.Arch) == "" ||
		strings.TrimSpace(body.Device.Role) == "" || strings.TrimSpace(body.Device.BootID) == "" {
		writeError(w, http.StatusBadRequest, "invalid_device", "设备信息不完整")
		return
	}
	consumed, err := h.consumePairingToken(body.PairingToken, time.Now())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "中心内部错误")
		return
	}
	if !consumed {
		writeError(w, http.StatusUnauthorized, "pairing_token_invalid", "配对令牌无效、已使用或已过期")
		return
	}
	registered, err := h.registerDevice(body.Device, body.EdgeVersion, body.ProtocolVersion, time.Now())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "中心内部错误")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"device_id": registered.Device.ID, "device_secret": registered.Secret,
		"protocol_version": ProtocolVersion, "center_time": nowISO(time.Now()),
		"heartbeat_interval_ms": 10000, "metrics_interval_ms": 5000,
	})
}

type metricsDTO struct {
	SampledAt        string   `json:"sampled_at"`
	ReceivedAt       string   `json:"received_at"`
	CPUPct           *float64 `json:"cpu_pct"`
	MemUsedBytes     *int64   `json:"mem_used_bytes"`
	MemTotalBytes    *int64   `json:"mem_total_bytes"`
	DiskUsedBytes    *int64   `json:"disk_used_bytes"`
	DiskTotalBytes   *int64   `json:"disk_total_bytes"`
	GPUUtilPct       *float64 `json:"gpu_util_pct"`
	GPUMemUsedBytes  *int64   `json:"gpu_mem_used_bytes"`
	GPUMemTotalBytes *int64   `json:"gpu_mem_total_bytes"`
	GPUTempC         *float64 `json:"gpu_temp_c"`
}

func metricToDTO(metric *MetricRow) *metricsDTO {
	if metric == nil {
		return nil
	}
	return &metricsDTO{
		SampledAt: metric.SampledAt, ReceivedAt: metric.ReceivedAt,
		CPUPct: metric.CPUPct, MemUsedBytes: metric.MemUsedBytes, MemTotalBytes: metric.MemTotalBytes,
		DiskUsedBytes: metric.DiskUsedBytes, DiskTotalBytes: metric.DiskTotalBytes,
		GPUUtilPct: metric.GPUUtilPct, GPUMemUsedBytes: metric.GPUMemUsedBytes,
		GPUMemTotalBytes: metric.GPUMemTotalBytes, GPUTempC: metric.GPUTempC,
	}
}

func deriveStatus(lastSeen *string, now time.Time) (string, *int64) {
	if lastSeen == nil {
		return "offline", nil
	}
	seen, err := time.Parse(time.RFC3339Nano, *lastSeen)
	if err != nil {
		return "offline", nil
	}
	age := int64(now.Sub(seen).Seconds())
	if age < 0 {
		age = 0
	}
	if age <= 30 {
		return "online", &age
	}
	if age <= 60 {
		return "stale", &age
	}
	return "offline", &age
}

func deviceToDTO(device DeviceRow, metric *MetricRow, now time.Time) map[string]any {
	status, age := deriveStatus(device.LastSeenAt, now)
	if device.RevokedAt != nil {
		status = "offline"
	}
	var capabilities []string
	_ = json.Unmarshal([]byte(device.Capabilities), &capabilities)
	if capabilities == nil {
		capabilities = []string{}
	}
	return map[string]any{
		"id": device.ID, "name": device.Name, "hostname": device.Hostname, "os": device.OS,
		"arch": device.Arch, "role": device.Role, "status": status, "status_age_seconds": age,
		"last_seen_at": device.LastSeenAt, "last_metrics_at": device.LastMetricsAt,
		"edge_version": device.EdgeVersion, "protocol_version": device.ProtocolVersion,
		"boot_id": device.BootID, "capabilities": capabilities, "created_at": device.CreatedAt,
		"revoked_at": device.RevokedAt, "metrics": metricToDTO(metric),
	}
}

func (h *Hub) handleDevices(w http.ResponseWriter, _ *http.Request) {
	all, err := h.listDevices()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "中心内部错误")
		return
	}
	// 同一台机器重新配对会留下历史记录；设备面板只展示每台机器最新的一条。
	now := time.Now()
	winners := map[string]DeviceRow{}
	for _, device := range all {
		key := device.Hostname + "|" + device.OS + "|" + device.Arch
		current, exists := winners[key]
		if !exists {
			winners[key] = device
			continue
		}
		currentStatus, _ := deriveStatus(current.LastSeenAt, now)
		deviceStatus, _ := deriveStatus(device.LastSeenAt, now)
		rank := func(d DeviceRow, status string) int {
			if d.RevokedAt != nil {
				return 0
			}
			if status == "online" {
				return 3
			}
			if status == "stale" {
				return 2
			}
			return 1
		}
		if rank(device, deviceStatus) > rank(current, currentStatus) {
			winners[key] = device
		}
	}
	devices := make([]DeviceRow, 0, len(winners))
	for _, device := range winners {
		devices = append(devices, device)
	}
	sort.Slice(devices, func(i, j int) bool { return devices[i].CreatedAt < devices[j].CreatedAt })
	out := make([]map[string]any, 0, len(devices))
	for _, device := range devices {
		metric, _ := h.latestMetric(device.ID)
		dto := deviceToDTO(device, metric, now)
		dto["harness_available"] = h.hasHarness(device.ID)
		dto["dsh"] = h.dshSummary(device.ID)
		out = append(out, dto)
	}
	writeJSON(w, http.StatusOK, map[string]any{"server_time": nowISO(now), "devices": out})
}

func pathID(r *http.Request) string { return r.PathValue("id") }

func (h *Hub) handleDevice(w http.ResponseWriter, r *http.Request) {
	device, err := h.getDevice(pathID(r))
	if err != nil || device == nil {
		writeError(w, http.StatusNotFound, "device_not_found", "设备不存在")
		return
	}
	metric, _ := h.latestMetric(device.ID)
	dto := deviceToDTO(*device, metric, time.Now())
	dto["harness_available"] = h.hasHarness(device.ID)
	dto["dsh"] = h.dshSummary(device.ID)
	writeJSON(w, http.StatusOK, map[string]any{"server_time": nowISO(time.Now()), "device": dto})
}

func (h *Hub) handleDeviceMetrics(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	device, err := h.getDevice(id)
	if err != nil || device == nil {
		writeError(w, http.StatusNotFound, "device_not_found", "设备不存在")
		return
	}
	limit := 60
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			limit = parsed
		}
	}
	if limit < 1 {
		limit = 1
	}
	if limit > 500 {
		limit = 500
	}
	metrics, err := h.listMetrics(id, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "中心内部错误")
		return
	}
	samples := make([]*metricsDTO, 0, len(metrics))
	for i := range metrics {
		samples = append(samples, metricToDTO(&metrics[i]))
	}
	_ = device
	writeJSON(w, http.StatusOK, map[string]any{"device_id": id, "samples": samples})
}

func (h *Hub) handleRevoke(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	device, err := h.getDevice(id)
	if err != nil || device == nil {
		writeError(w, http.StatusNotFound, "device_not_found", "设备不存在")
		return
	}
	h.revokeDevice(id, time.Now())
	h.connMu.Lock()
	client := h.connections[id]
	delete(h.connections, id)
	h.connMu.Unlock()
	if client != nil {
		_ = client.conn.Close()
	}
	writeJSON(w, http.StatusOK, map[string]any{"device_id": id, "revoked": true})
}

func (h *Hub) handleEvents(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			limit = parsed
		}
	}
	if limit < 1 {
		limit = 1
	}
	if limit > 500 {
		limit = 500
	}
	events, err := h.listEvents(limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "中心内部错误")
		return
	}
	if events == nil {
		events = []EventRow{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}
