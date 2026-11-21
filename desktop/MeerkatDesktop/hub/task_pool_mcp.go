package hub

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

const poolBotTokenPrefix = "mk_pool_bot_"

func (h *Hub) handlePoolBotCredential(w http.ResponseWriter, r *http.Request) {
	botID := r.PathValue("botID")
	ctx := r.Context()
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodDelete {
		if _, err := h.db.ExecContext(ctx, `DELETE FROM task_pool_bot_credentials WHERE bot_id=?`, botID); err != nil {
			writePoolError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if _, err := h.resolvePoolTarget(r, poolAssignmentRequest{Kind: "bot", ID: botID}); err != nil {
		writePoolError(w, err)
		return
	}
	credential := poolBotTokenPrefix + randomHex(32)
	_, err := h.db.ExecContext(ctx, `INSERT INTO task_pool_bot_credentials (bot_id,token_hash,created_at) VALUES (?,?,?)
		ON CONFLICT(bot_id) DO UPDATE SET token_hash=excluded.token_hash,created_at=excluded.created_at`, botID, tokenHash(credential), nowISO(time.Now()))
	if err != nil {
		writePoolError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"credential": credential, "bot_id": botID})
}

func (h *Hub) poolBotIdentity(r *http.Request) (string, string, error) {
	token, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "+poolBotTokenPrefix)
	if !found || len(token) != 64 {
		return "", "", errPoolForbidden
	}
	hash := tokenHash(poolBotTokenPrefix + token)
	var actor string
	err := h.db.QueryRowContext(r.Context(), `SELECT bot_id FROM task_pool_bot_credentials WHERE token_hash=?`, hash).Scan(&actor)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", errPoolForbidden
	}
	return actor, hash, err
}

type poolMCPRequest struct {
	Version string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

var poolMCPTools = []map[string]any{
	{"name": "read_task", "description": "Read the task currently assigned to you. Use its assignment_rev in reports.",
		"inputSchema": map[string]any{"type": "object", "required": []string{"task_id"}, "properties": map[string]any{
			"task_id": map[string]string{"type": "string"},
		}}},
	{"name": "report_progress", "description": "Report accepted, progress, blocked or delivered on a currently assigned task. Reuse event_id for the same report when retrying. Delivery requires evidence.",
		"inputSchema": map[string]any{"type": "object", "required": []string{"task_id", "assignment_rev", "event_id", "kind", "note"}, "properties": map[string]any{
			"task_id": map[string]string{"type": "string"}, "assignment_rev": map[string]string{"type": "integer"},
			"event_id": map[string]string{"type": "string"}, "kind": map[string]any{"type": "string", "enum": []string{"accepted", "progress", "blocked", "delivered"}},
			"note": map[string]string{"type": "string"}, "evidence": map[string]any{"type": "array", "items": map[string]string{"type": "string"}},
		}}},
}

func poolMCPResult(w http.ResponseWriter, id json.RawMessage, result any) {
	writeJSON(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func poolMCPError(w http.ResponseWriter, id json.RawMessage, code int, message string) {
	writeJSON(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": message}})
}

func (h *Hub) handlePoolMCP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	// This endpoint is for host-side clients. Browser-origin requests have no role here.
	if r.Header.Get("Origin") != "" {
		writePoolError(w, errPoolForbidden)
		return
	}
	actor, hash, err := h.poolBotIdentity(r)
	if errors.Is(err, errPoolForbidden) {
		writeError(w, http.StatusUnauthorized, "task_pool_credential_required", "需要任务池 Bot 凭据")
		return
	}
	if err != nil {
		writePoolError(w, err)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var request poolMCPRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&request); err != nil || request.Version != "2.0" {
		poolMCPError(w, nil, -32600, "Invalid request")
		return
	}
	if request.ID == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	switch request.Method {
	case "initialize":
		var input struct {
			Version string `json:"protocolVersion"`
		}
		if json.Unmarshal(request.Params, &input) != nil {
			poolMCPError(w, request.ID, -32602, "Invalid initialization")
			return
		}
		version := input.Version
		switch version {
		case "2025-03-26", "2025-06-18", "2025-11-25":
		default:
			version = "2025-11-25"
		}
		poolMCPResult(w, request.ID, map[string]any{"protocolVersion": version,
			"serverInfo":   map[string]string{"name": "meerkat-task-pool", "version": "1"},
			"capabilities": map[string]any{"tools": struct{}{}}})
	case "ping":
		poolMCPResult(w, request.ID, struct{}{})
	case "tools/list":
		poolMCPResult(w, request.ID, map[string]any{"tools": poolMCPTools})
	case "tools/call":
		h.handlePoolMCPCall(w, r, request, actor, hash)
	default:
		poolMCPError(w, request.ID, -32601, "Method not found")
	}
}

func (h *Hub) readPoolAssignment(r *http.Request, id, actor, hash string) (any, error) {
	ctx := r.Context()
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	item, err := scanPoolItem(tx.QueryRowContext(ctx, `SELECT `+poolFields+` FROM task_pool_items
		WHERE id=? AND assignee_actor_id=? AND status NOT IN ('completed','cancelled')
		AND EXISTS (SELECT 1 FROM task_pool_bot_credentials WHERE token_hash=? AND bot_id=?)`, id, actor, hash, actor))
	if err != nil {
		return nil, err
	}
	assignment := struct {
		poolItem
		ReturnReason string `json:"return_reason,omitempty"`
	}{poolItem: item}
	var payload string
	err = tx.QueryRowContext(ctx, `SELECT payload FROM task_pool_events WHERE item_id=? AND assignment_rev=? AND type='returned' ORDER BY seq DESC LIMIT 1`, item.ID, item.AssignmentRev).Scan(&payload)
	if err == nil {
		var body poolEventBody
		if err := json.Unmarshal([]byte(payload), &body); err != nil {
			return nil, err
		}
		assignment.ReturnReason = body.Note
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	return assignment, nil
}

func (h *Hub) handlePoolMCPCall(w http.ResponseWriter, r *http.Request, request poolMCPRequest, actor, hash string) {
	var call struct {
		Name string `json:"name"`
		Args struct {
			TaskID  string `json:"task_id"`
			Rev     int    `json:"assignment_rev"`
			EventID string `json:"event_id"`
			Kind    string `json:"kind"`
			poolEventBody
		} `json:"arguments"`
	}
	if json.Unmarshal(request.Params, &call) != nil || call.Args.TaskID == "" || (call.Name != "read_task" && call.Name != "report_progress") {
		poolMCPError(w, request.ID, -32602, "Unknown tool or invalid arguments")
		return
	}
	ctx := r.Context()
	now := time.Now()
	var item any
	var err error
	if call.Name == "read_task" {
		item, err = h.readPoolAssignment(r, call.Args.TaskID, actor, hash)
	} else {
		grant := poolReportGrant{ItemID: call.Args.TaskID, ActorID: actor, AssignmentRev: call.Args.Rev, ExpiresAt: now.Add(time.Minute), CredentialHash: hash}
		item, err = h.reportPoolEvent(ctx, grant, call.Args.EventID, call.Args.Kind, call.Args.poolEventBody, now)
	}
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = errPoolForbidden
		}
		writePoolError(w, err)
		return
	}
	data, err := json.Marshal(item)
	if err != nil {
		writePoolError(w, err)
		return
	}
	poolMCPResult(w, request.ID, map[string]any{"content": []map[string]string{{"type": "text", "text": string(data)}}})
}
