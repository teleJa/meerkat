package hub

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTaskPoolMCPReportScopeAndRevocation(t *testing.T) {
	db, err := openDB(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	h := &Hub{db: db, adminToken: "fixture-admin", cfg: Config{TaskPoolChiefBotID: "actor"}}
	mux := http.NewServeMux()
	h.registerHTTP(mux)
	actorToken := poolBotTokenPrefix + strings.Repeat("a", 64)
	foreignToken := poolBotTokenPrefix + strings.Repeat("b", 64)
	now := time.Now()
	for actor, token := range map[string]string{"actor": actorToken, "other": foreignToken} {
		if _, err := db.Exec(`INSERT INTO task_pool_bot_credentials VALUES (?,?,?)`, actor, tokenHash(token), nowISO(now)); err != nil {
			t.Fatal(err)
		}
	}
	attachment, err := h.insertAttachment("report.html", "text/html", []byte("synthetic attachment"), now)
	if err != nil {
		t.Fatal(err)
	}
	item, _, err := h.createPoolItem(context.Background(), poolCreate{Request: "synthetic task data", AttachmentIDs: []string{attachment.ID}}, "", poolTarget{Kind: "bot", ID: "actor", ActorID: "actor"}, "", now)
	if err != nil {
		t.Fatal(err)
	}
	check := func(method, path, token, input string, status int, want string) {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(input))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != status || !strings.Contains(w.Body.String(), want) || strings.Contains(w.Body.String(), token) {
			t.Fatalf("response: %d %s, want %d with %q", w.Code, w.Body, status, want)
		}
	}
	const mcp = "/api/task-pool/mcp"
	const tools = `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	read := fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"read_task","arguments":{"task_id":%q}}}`, item.ID)
	report := fmt.Sprintf(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"report_progress","arguments":{"task_id":%q,"assignment_rev":1,"event_id":"evt-1","kind":"accepted","note":"已接手"}}}`, item.ID)
	check("POST", mcp, h.adminToken, tools, http.StatusUnauthorized, "task_pool_credential_required")
	check("POST", mcp, actorToken, tools, http.StatusOK, "assign_task")
	check("POST", mcp, foreignToken, tools, http.StatusOK, "report_progress")
	check("POST", mcp, foreignToken, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"list_unassigned","arguments":{}}}`, http.StatusForbidden, "task_pool_forbidden")
	check("POST", mcp, foreignToken, read, http.StatusForbidden, "task_pool_forbidden")
	check("POST", mcp, actorToken, read, http.StatusOK, "synthetic task data")
	check("POST", mcp, foreignToken, report, http.StatusForbidden, "task_pool_forbidden")
	check("POST", mcp, actorToken, report, http.StatusOK, "assignment_rev")
	check("POST", mcp, actorToken, report, http.StatusOK, "assignment_rev")
	attachmentPath := "/api/task-pool/" + item.ID + "/attachments/" + attachment.ID
	check("GET", attachmentPath, actorToken, "", http.StatusOK, "synthetic attachment")
	if _, err := h.assignPoolItem(context.Background(), item.ID, item.Revision+1, "actor", tokenHash(actorToken), poolTarget{Kind: "bot", ID: "other", ActorID: "other"}, now); err != errPoolConflict {
		t.Fatalf("Chief must not reassign an assigned card: %v", err)
	}
	check("GET", attachmentPath, foreignToken, "", http.StatusForbidden, "task_pool_forbidden")
	check("GET", "/api/task-pool", actorToken, "", http.StatusUnauthorized, "unauthorized")
	check("DELETE", "/api/task-pool/config/bot-credentials/actor", actorToken, "", http.StatusUnauthorized, "unauthorized")
	check("DELETE", "/api/task-pool/config/bot-credentials/actor", h.adminToken, "", http.StatusNoContent, "")
	check("POST", mcp, actorToken, report, http.StatusUnauthorized, "task_pool_credential_required")
	check("GET", attachmentPath, actorToken, "", http.StatusUnauthorized, "task_pool_credential_required")
	assertTaskPoolCounts(t, h, 1, 2, 1)
}
