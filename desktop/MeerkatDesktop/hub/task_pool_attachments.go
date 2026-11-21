package hub

import (
	"database/sql"
	"errors"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
)

func (h *Hub) handlePoolAssignmentAttachment(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var actor, hash string
	isBot := strings.HasPrefix(r.Header.Get("Authorization"), "Bearer "+poolBotTokenPrefix)
	if isBot {
		var err error
		actor, hash, err = h.poolBotIdentity(r)
		if errors.Is(err, errPoolForbidden) {
			writeError(w, http.StatusUnauthorized, "task_pool_credential_required", "需要任务池 Bot 凭据")
			return
		}
		if err != nil {
			writePoolError(w, err)
			return
		}
	} else {
		auth, ok := h.requireAuth(w, r, false)
		if !ok {
			return
		}
		if auth.Via == "mobile" {
			writePoolError(w, errPoolForbidden)
			return
		}
	}
	var name string
	var content []byte
	err := h.db.QueryRowContext(ctx, `SELECT a.name,a.content FROM task_pool_items AS i
		JOIN json_each(i.attachment_ids) AS ids JOIN attachments AS a ON a.id=ids.value
		WHERE i.id=? AND a.id=? AND (NOT ? OR (
			i.assignee_actor_id=? AND i.status NOT IN ('completed','cancelled') AND EXISTS (
			SELECT 1 FROM task_pool_bot_credentials WHERE token_hash=? AND bot_id=?)))`,
		r.PathValue("id"), r.PathValue("attachmentID"), isBot, actor, hash, actor).Scan(&name, &content)
	if err != nil {
		if isBot && errors.Is(err, sql.ErrNoRows) {
			err = errPoolForbidden
		}
		writePoolError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(name)}))
	_, _ = w.Write(content)
}
