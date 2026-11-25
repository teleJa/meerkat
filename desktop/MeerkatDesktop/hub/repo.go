package hub

import (
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type registeredDevice struct {
	Device DeviceRow
	Secret string
}

type webSession struct {
	ExpiresAt  string
	LastSeenAt string
}

func nullString(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	value := v.String
	return &value
}

func nullInt(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	value := v.Int64
	return &value
}

func nullFloat(v sql.NullFloat64) *float64 {
	if !v.Valid {
		return nil
	}
	value := v.Float64
	return &value
}

func scanDevice(scanner interface{ Scan(dest ...any) error }) (DeviceRow, error) {
	var d DeviceRow
	var lastSeen, lastMetrics, revoked sql.NullString
	err := scanner.Scan(
		&d.ID, &d.Name, &d.Hostname, &d.OS, &d.Arch, &d.Role, &d.BootID,
		&d.Capabilities, &d.EdgeVersion, &d.ProtocolVersion, &d.SecretHash,
		&d.CreatedAt, &lastSeen, &lastMetrics, &revoked,
	)
	d.LastSeenAt = nullString(lastSeen)
	d.LastMetricsAt = nullString(lastMetrics)
	d.RevokedAt = nullString(revoked)
	return d, err
}

func (h *Hub) createPairingToken(label *string, ttlSeconds int, now time.Time) (token string, expiresAt string, err error) {
	token = "mkp_" + randomHex(24)
	expiresAt = nowISO(now.Add(time.Duration(ttlSeconds) * time.Second))
	_, err = h.db.Exec(
		"INSERT INTO pairing_tokens (token_hash, label, created_at, expires_at) VALUES (?, ?, ?, ?)",
		tokenHash(token), label, nowISO(now), expiresAt,
	)
	return token, expiresAt, err
}

func (h *Hub) consumePairingToken(token string, now time.Time) (bool, error) {
	hash := tokenHash(token)
	tx, err := h.db.Begin()
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	var expiresAt string
	var usedAt sql.NullString
	err = tx.QueryRow("SELECT expires_at, used_at FROM pairing_tokens WHERE token_hash = ?", hash).Scan(&expiresAt, &usedAt)
	if err == sql.ErrNoRows || usedAt.Valid {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	expiry, err := time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil || !expiry.After(now) {
		return false, nil
	}
	if _, err := tx.Exec("UPDATE pairing_tokens SET used_at = ? WHERE token_hash = ?", nowISO(now), hash); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (h *Hub) findReusableDevice(input DeviceInfo) (*DeviceRow, error) {
	row := h.db.QueryRow(
		`SELECT * FROM devices
		 WHERE hostname = ? AND os = ? AND arch = ? AND revoked_at IS NULL
		 ORDER BY COALESCE(last_seen_at, created_at) DESC LIMIT 1`,
		input.Hostname, input.OS, input.Arch,
	)
	device, err := scanDevice(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &device, err
}

func (h *Hub) registerDevice(input DeviceInfo, edgeVersion, protocolVersion string, now time.Time) (*registeredDevice, error) {
	h.connMu.Lock()
	defer h.connMu.Unlock()
	capabilities := input.Capabilities
	if capabilities == nil {
		capabilities = []string{}
	}
	capJSON, _ := json.Marshal(capabilities)
	secret := randomHex(32)
	// 同一台机器重新配对时复用已有设备记录，避免设备面板出现多个同名实例。
	existing, err := h.findReusableDevice(input)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		_, err := h.db.Exec(
			`UPDATE devices SET
			  name = ?, role = ?, boot_id = ?, capabilities = ?, edge_version = ?,
			  protocol_version = ?, secret_hash = ?, last_seen_at = ?, revoked_at = NULL
			 WHERE id = ?`,
			input.Name, input.Role, input.BootID, string(capJSON), edgeVersion,
			protocolVersion, tokenHash(secret), nowISO(now), existing.ID,
		)
		if err != nil {
			return nil, err
		}
		device, err := h.getDevice(existing.ID)
		if err != nil || device == nil {
			return nil, fmt.Errorf("device_update_failed")
		}
		detail, _ := json.Marshal(map[string]any{"name": device.Name, "hostname": device.Hostname, "reused": true})
		detailStr := string(detail)
		h.logEvent("device.registered", &device.ID, &detailStr)
		return &registeredDevice{Device: *device, Secret: secret}, nil
	}
	id := "dev_" + randomHex(4)
	_, err = h.db.Exec(
		`INSERT INTO devices
		 (id, name, hostname, os, arch, role, boot_id, capabilities, edge_version, protocol_version, secret_hash, created_at, last_seen_at, last_metrics_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL)`,
		id, input.Name, input.Hostname, input.OS, input.Arch, input.Role, input.BootID,
		string(capJSON), edgeVersion, protocolVersion, tokenHash(secret), nowISO(now),
	)
	if err != nil {
		return nil, err
	}
	device, err := h.getDevice(id)
	if err != nil || device == nil {
		return nil, fmt.Errorf("device_insert_failed")
	}
	detail, _ := json.Marshal(map[string]any{"name": device.Name, "hostname": device.Hostname})
	detailStr := string(detail)
	h.logEvent("device.registered", &id, &detailStr)
	return &registeredDevice{Device: *device, Secret: secret}, nil
}

func (h *Hub) getDevice(id string) (*DeviceRow, error) {
	row := h.db.QueryRow("SELECT * FROM devices WHERE id = ?", id)
	device, err := scanDevice(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &device, err
}

var errDeviceRevoked = errors.New("device_revoked")

func (h *Hub) getDeviceBySecret(id, secret string) (*DeviceRow, error) {
	device, err := h.getDevice(id)
	if err != nil || device == nil {
		return nil, err
	}
	if subtle.ConstantTimeCompare([]byte(tokenHash(secret)), []byte(device.SecretHash)) != 1 {
		return nil, nil
	}
	if device.RevokedAt != nil {
		return nil, errDeviceRevoked
	}
	return device, nil
}

func (h *Hub) listDevices() ([]DeviceRow, error) {
	rows, err := h.db.Query("SELECT * FROM devices ORDER BY created_at ASC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var devices []DeviceRow
	for rows.Next() {
		device, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		devices = append(devices, device)
	}
	return devices, rows.Err()
}

func (h *Hub) touchDevice(id string, lastSeen, lastMetrics *string, bootID, edgeVersion *string, capabilities *string) {
	_, _ = h.db.Exec(
		`UPDATE devices SET
		  last_seen_at = COALESCE(?, last_seen_at),
		  last_metrics_at = COALESCE(?, last_metrics_at),
		  boot_id = COALESCE(?, boot_id),
		  edge_version = COALESCE(?, edge_version),
		  capabilities = COALESCE(?, capabilities)
		 WHERE id = ?`,
		lastSeen, lastMetrics, bootID, edgeVersion, capabilities, id,
	)
}

func (h *Hub) revokeDevice(id string, now time.Time) error {
	_, err := h.db.Exec("UPDATE devices SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL", nowISO(now), id)
	if err != nil {
		return err
	}
	h.logEvent("device.revoked", &id, nil)
	return nil
}

func (h *Hub) insertMetric(deviceID, sampledAt string, metrics MetricsPayload, receivedAt time.Time) error {
	_, err := h.db.Exec(
		`INSERT INTO metrics
		 (device_id, sampled_at, received_at, cpu_pct, mem_used_bytes, mem_total_bytes, disk_used_bytes, disk_total_bytes, gpu_util_pct, gpu_mem_used_bytes, gpu_mem_total_bytes, gpu_temp_c)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		deviceID, sampledAt, nowISO(receivedAt), metrics.CPUPct, metrics.MemUsedBytes, metrics.MemTotalBytes,
		metrics.DiskUsedBytes, metrics.DiskTotalBytes, metrics.GPUUtilPct, metrics.GPUMemUsedBytes,
		metrics.GPUMemTotalBytes, metrics.GPUTempC,
	)
	return err
}

func scanMetric(scanner interface{ Scan(dest ...any) error }) (MetricRow, error) {
	var m MetricRow
	var cpu, gpuUtil, gpuTemp sql.NullFloat64
	var memUsed, memTotal, diskUsed, diskTotal, gpuMemUsed, gpuMemTotal sql.NullInt64
	err := scanner.Scan(&m.ID, &m.DeviceID, &m.SampledAt, &m.ReceivedAt, &cpu, &memUsed, &memTotal, &diskUsed, &diskTotal, &gpuUtil, &gpuMemUsed, &gpuMemTotal, &gpuTemp)
	m.CPUPct = nullFloat(cpu)
	m.MemUsedBytes = nullInt(memUsed)
	m.MemTotalBytes = nullInt(memTotal)
	m.DiskUsedBytes = nullInt(diskUsed)
	m.DiskTotalBytes = nullInt(diskTotal)
	m.GPUUtilPct = nullFloat(gpuUtil)
	m.GPUMemUsedBytes = nullInt(gpuMemUsed)
	m.GPUMemTotalBytes = nullInt(gpuMemTotal)
	m.GPUTempC = nullFloat(gpuTemp)
	return m, err
}

func (h *Hub) latestMetric(deviceID string) (*MetricRow, error) {
	row := h.db.QueryRow("SELECT * FROM metrics WHERE device_id = ? ORDER BY id DESC LIMIT 1", deviceID)
	metric, err := scanMetric(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &metric, err
}

func (h *Hub) listMetrics(deviceID string, limit int) ([]MetricRow, error) {
	rows, err := h.db.Query("SELECT * FROM metrics WHERE device_id = ? ORDER BY id DESC LIMIT ?", deviceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var metrics []MetricRow
	for rows.Next() {
		metric, err := scanMetric(rows)
		if err != nil {
			return nil, err
		}
		metrics = append(metrics, metric)
	}
	for i, j := 0, len(metrics)-1; i < j; i, j = i+1, j-1 {
		metrics[i], metrics[j] = metrics[j], metrics[i]
	}
	return metrics, rows.Err()
}

func (h *Hub) listEvents(limit int) ([]EventRow, error) {
	rows, err := h.db.Query("SELECT id, at, device_id, type, detail FROM events ORDER BY id DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []EventRow
	for rows.Next() {
		var event EventRow
		var deviceID, detail sql.NullString
		if err := rows.Scan(&event.ID, &event.At, &deviceID, &event.Type, &detail); err != nil {
			return nil, err
		}
		event.DeviceID = nullString(deviceID)
		event.Detail = nullString(detail)
		events = append(events, event)
	}
	return events, rows.Err()
}

func (h *Hub) createWebSession(ttlSeconds int, now time.Time) (string, string, error) {
	id := randomHex(32)
	expiresAt := nowISO(now.Add(time.Duration(ttlSeconds) * time.Second))
	_, err := h.db.Exec(
		"INSERT INTO web_sessions (id_hash, created_at, expires_at, last_seen_at) VALUES (?, ?, ?, ?)",
		tokenHash(id), nowISO(now), expiresAt, nowISO(now),
	)
	return id, expiresAt, err
}

func (h *Hub) getWebSession(id string, now time.Time) (*webSession, error) {
	hash := tokenHash(id)
	var expiresAt, lastSeenAt string
	err := h.db.QueryRow("SELECT expires_at, last_seen_at FROM web_sessions WHERE id_hash = ?", hash).Scan(&expiresAt, &lastSeenAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	expiry, err := time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil || !expiry.After(now) {
		_, _ = h.db.Exec("DELETE FROM web_sessions WHERE id_hash = ?", hash)
		return nil, nil
	}
	_, _ = h.db.Exec("UPDATE web_sessions SET last_seen_at = ? WHERE id_hash = ?", nowISO(now), hash)
	return &webSession{ExpiresAt: expiresAt, LastSeenAt: lastSeenAt}, nil
}

func (h *Hub) deleteWebSession(id string) {
	_, _ = h.db.Exec("DELETE FROM web_sessions WHERE id_hash = ?", tokenHash(id))
}

func (h *Hub) cleanupExpiredSessions(now time.Time) {
	_, _ = h.db.Exec("DELETE FROM web_sessions WHERE expires_at <= ?", nowISO(now))
}

func (h *Hub) cleanupOldMetrics(retentionHours int, now time.Time) {
	cutoff := nowISO(now.Add(-time.Duration(retentionHours) * time.Hour))
	_, _ = h.db.Exec("DELETE FROM metrics WHERE sampled_at < ?", cutoff)
}
