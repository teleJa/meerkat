package hub

import (
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

const (
	maxAttachmentBytes  = 4 << 20
	maxAttachmentsTask  = 4
	maxAttachmentsTotal = 8 << 20
)

func (h *Hub) insertAttachment(name, mime string, content []byte, now time.Time) (AttachmentRow, error) {
	sum := sha256.Sum256(content)
	row := AttachmentRow{
		ID: newID("att_"), Name: name, Mime: mime, Size: int64(len(content)),
		SHA256: hex.EncodeToString(sum[:]), CreatedAt: nowISO(now),
	}
	_, err := h.db.Exec(
		`INSERT INTO attachments (id, task_id, turn_id, device_id, name, mime, size, sha256, content, created_at)
		 VALUES (?, NULL, NULL, NULL, ?, ?, ?, ?, ?, ?)`,
		row.ID, row.Name, row.Mime, row.Size, row.SHA256, content, row.CreatedAt,
	)
	return row, err
}

func (h *Hub) getAttachment(id string) (*AttachmentRow, []byte, error) {
	var row AttachmentRow
	var content []byte
	err := h.db.QueryRow(`SELECT id, task_id, turn_id, device_id, name, mime, size, sha256, created_at, content
		FROM attachments WHERE id = ?`, id).Scan(
		&row.ID, &row.TaskID, &row.TurnID, &row.DeviceID, &row.Name, &row.Mime,
		&row.Size, &row.SHA256, &row.CreatedAt, &content,
	)
	if err == sql.ErrNoRows {
		return nil, nil, nil
	}
	return &row, content, err
}

func (h *Hub) listAttachmentsForTask(taskID string) ([]AttachmentRow, error) {
	rows, err := h.db.Query(`SELECT id, task_id, turn_id, device_id, name, mime, size, sha256, created_at
		FROM attachments WHERE task_id = ? ORDER BY created_at ASC`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AttachmentRow
	for rows.Next() {
		var row AttachmentRow
		if err := rows.Scan(&row.ID, &row.TaskID, &row.TurnID, &row.DeviceID, &row.Name, &row.Mime, &row.Size, &row.SHA256, &row.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func attachmentToDTO(row AttachmentRow) map[string]any {
	return map[string]any{
		"id": row.ID, "task_id": row.TaskID, "turn_id": row.TurnID,
		"name": row.Name, "mime": row.Mime, "size": row.Size,
		"sha256": row.SHA256, "created_at": row.CreatedAt,
	}
}

func (h *Hub) LoadAttachments(ids []string) ([]AttachmentRow, []byte, error) {
	seen := map[string]bool{}
	var rows []AttachmentRow
	var total int64
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		row, content, err := h.getAttachment(id)
		if err != nil {
			return nil, nil, err
		}
		if row == nil {
			return nil, nil, fmt.Errorf("附件不存在: %s", id)
		}
		if len(rows) >= maxAttachmentsTask {
			return nil, nil, fmt.Errorf("最多 %d 个附件", maxAttachmentsTask)
		}
		total += int64(len(content))
		if total > maxAttachmentsTotal {
			return nil, nil, fmt.Errorf("附件总大小超过 8MB")
		}
		rows = append(rows, *row)
	}
	return rows, nil, nil
}

// BuildAttachmentPayloads 把附件转成 command payload 的内联结构。
func (h *Hub) BuildAttachmentPayloads(rows []AttachmentRow) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		_, content, err := h.getAttachment(row.ID)
		if err != nil {
			return nil, err
		}
		if content == nil {
			return nil, fmt.Errorf("附件不存在: %s", row.ID)
		}
		out = append(out, map[string]any{
			"id": row.ID, "name": row.Name, "mime": row.Mime, "size": row.Size,
			"sha256": row.SHA256, "content_base64": base64.StdEncoding.EncodeToString(content),
		})
	}
	return out, nil
}

func (h *Hub) bindAttachments(taskID, turnID, deviceID string, ids []string, now time.Time) error {
	for _, id := range ids {
		_, err := h.db.Exec(`UPDATE attachments SET task_id = ?, turn_id = ?, device_id = ? WHERE id = ? AND (task_id IS NULL OR task_id = ?)`,
			taskID, turnID, deviceID, id, taskID)
		if err != nil {
			return err
		}
	}
	return nil
}

func (h *Hub) handleUploadAttachment(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxAttachmentBytes+1024)
	if err := r.ParseMultipartForm(maxAttachmentBytes); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_attachment", "文件过大或格式无效")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_attachment", "缺少 file 字段")
		return
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxAttachmentBytes+1))
	if err != nil || len(content) == 0 || len(content) > maxAttachmentBytes {
		writeError(w, http.StatusBadRequest, "invalid_attachment", "文件不能为空且不能超过 4MB")
		return
	}
	name := filepath.Base(strings.TrimSpace(header.Filename))
	if name == "." || name == "/" || name == "" {
		name = "attachment"
	}
	mimeType := strings.TrimSpace(header.Header.Get("Content-Type"))
	if mimeType == "" {
		mimeType = http.DetectContentType(content)
	}
	row, err := h.insertAttachment(name, mimeType, content, time.Now())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "保存附件失败")
		return
	}
	writeJSON(w, http.StatusCreated, attachmentToDTO(row))
}

func (h *Hub) handleAttachmentContent(w http.ResponseWriter, r *http.Request) {
	row, content, err := h.getAttachment(pathID(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "读取附件失败")
		return
	}
	if row == nil {
		writeError(w, http.StatusNotFound, "attachment_not_found", "附件不存在")
		return
	}
	w.Header().Set("Content-Type", row.Mime)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", filepath.Base(row.Name)))
	_, _ = w.Write(content)
}
