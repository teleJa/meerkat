package hub

import (
	"database/sql"
	"encoding/base64"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

const maxArtifactBytes = 2 << 20

func (h *Hub) insertArtifact(taskID, deviceID, turnID, name, path, mime, sha string, size int64, content []byte, now time.Time) (ArtifactRow, error) {
	row := ArtifactRow{
		ID: newID("art_"), TaskID: taskID, DeviceID: deviceID, Name: name,
		Path: path, Mime: mime, Size: size, SHA256: sha, CreatedAt: nowISO(now),
	}
	if strings.TrimSpace(turnID) != "" {
		row.TurnID = &turnID
	}
	_, err := h.db.Exec(
		`INSERT OR IGNORE INTO artifacts (id, task_id, device_id, turn_id, name, path, mime, size, sha256, content, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.ID, row.TaskID, row.DeviceID, row.TurnID, row.Name, row.Path, row.Mime, row.Size, row.SHA256, content, row.CreatedAt,
	)
	if err != nil {
		return ArtifactRow{}, err
	}
	if existing, err := h.getArtifactByNameSHA(taskID, name, sha); err == nil && existing != nil {
		return *existing, nil
	}
	return row, nil
}

func (h *Hub) getArtifactByNameSHA(taskID, name, sha string) (*ArtifactRow, error) {
	row := h.db.QueryRow(`SELECT id, task_id, device_id, turn_id, name, path, mime, size, sha256, created_at
		FROM artifacts WHERE task_id = ? AND name = ? AND sha256 = ?`, taskID, name, sha)
	return scanArtifact(row)
}

func (h *Hub) getArtifact(id string) (*ArtifactRow, []byte, error) {
	var row ArtifactRow
	var content []byte
	err := h.db.QueryRow(`SELECT id, task_id, device_id, turn_id, name, path, mime, size, sha256, created_at, content
		FROM artifacts WHERE id = ?`, id).Scan(
		&row.ID, &row.TaskID, &row.DeviceID, &row.TurnID, &row.Name, &row.Path,
		&row.Mime, &row.Size, &row.SHA256, &row.CreatedAt, &content,
	)
	if err == sql.ErrNoRows {
		return nil, nil, nil
	}
	return &row, content, err
}

func (h *Hub) listArtifactsForTask(taskID string) ([]ArtifactRow, error) {
	rows, err := h.db.Query(`SELECT id, task_id, device_id, turn_id, name, path, mime, size, sha256, created_at
		FROM artifacts WHERE task_id = ? ORDER BY created_at ASC`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ArtifactRow
	for rows.Next() {
		row, err := scanArtifact(rows)
		if err != nil {
			return nil, err
		}
		if row != nil {
			out = append(out, *row)
		}
	}
	return out, rows.Err()
}

type artifactScanner interface {
	Scan(dest ...any) error
}

func scanArtifact(scanner artifactScanner) (*ArtifactRow, error) {
	var row ArtifactRow
	err := scanner.Scan(&row.ID, &row.TaskID, &row.DeviceID, &row.TurnID, &row.Name, &row.Path, &row.Mime, &row.Size, &row.SHA256, &row.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func artifactToDTO(row ArtifactRow) map[string]any {
	return map[string]any{
		"id": row.ID, "task_id": row.TaskID, "device_id": row.DeviceID, "turn_id": row.TurnID,
		"name": row.Name, "path": row.Path, "mime": row.Mime, "size": row.Size,
		"sha256": row.SHA256, "created_at": row.CreatedAt,
		"url": "/api/artifacts/" + row.ID,
	}
}

func (h *Hub) handleArtifactContent(w http.ResponseWriter, r *http.Request) {
	row, content, err := h.getArtifact(pathID(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "读取工件失败")
		return
	}
	if row == nil {
		writeError(w, http.StatusNotFound, "artifact_not_found", "工件不存在")
		return
	}
	filename := filepath.Base(row.Name)
	if filename == "." || filename == "/" || filename == "" {
		filename = "artifact"
	}
	w.Header().Set("Content-Type", row.Mime)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", filename))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(content)
}

func (h *Hub) handleArtifactRegister(deviceID string, artifact *wsArtifact, now time.Time) (map[string]any, error) {
	if artifact == nil {
		return nil, fmt.Errorf("artifact 缺失")
	}
	if strings.TrimSpace(artifact.TaskID) == "" || strings.TrimSpace(artifact.Name) == "" || strings.TrimSpace(artifact.ContentBase64) == "" {
		return nil, fmt.Errorf("task_id/name/content 必填")
	}
	task, err := h.getTask(artifact.TaskID)
	if err != nil || task == nil {
		return nil, fmt.Errorf("任务不存在")
	}
	if task.DeviceID != deviceID {
		return nil, fmt.Errorf("任务不属于当前设备")
	}
	content, err := base64.StdEncoding.DecodeString(artifact.ContentBase64)
	if err != nil {
		return nil, fmt.Errorf("content base64 无效")
	}
	if len(content) == 0 || len(content) > maxArtifactBytes {
		return nil, fmt.Errorf("工件大小超出限制")
	}
	size := artifact.Size
	if size <= 0 {
		size = int64(len(content))
	}
	name := filepath.Base(strings.TrimSpace(artifact.Name))
	if name == "." || name == "/" || name == "" {
		return nil, fmt.Errorf("工件名无效")
	}
	mime := strings.TrimSpace(artifact.Mime)
	if mime == "" {
		mime = "application/octet-stream"
	}
	row, err := h.insertArtifact(artifact.TaskID, deviceID, artifact.TurnID, name, artifact.Path, mime, artifact.SHA256, size, content, now)
	if err != nil {
		return nil, err
	}
	return map[string]any{"type": "artifact.ack", "artifact_id": row.ID, "task_id": row.TaskID, "name": row.Name}, nil
}
