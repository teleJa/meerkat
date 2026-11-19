package hub

import (
	"database/sql"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func issueTestMobile(t *testing.T, h *Hub, now time.Time) (MobileCredential, string) {
	t.Helper()
	code, _, err := h.createMobilePairingCode(nil, MobilePairingTTL, now)
	if err != nil {
		t.Fatal(err)
	}
	credential, token, err := h.registerMobileCredential(mobileRegistration{
		Code: code, Label: "test phone", Platform: "android", Model: "iQOO 10", AppVersion: "0.1.0",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	return credential, token
}

func TestMobilePairingAndCredentialAuth(t *testing.T) {
	h := testHub(t)
	now := time.Now()
	code, _, err := h.createMobilePairingCode(nil, MobilePairingTTL, now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(code, "MEERKAT-") || len(code) != len("MEERKAT-XXXX-XXXX") {
		t.Fatalf("unexpected pairing code %q", code)
	}
	// 注册输入允许小写、空格和不同分隔符。
	normalized := strings.ToLower(strings.ReplaceAll(code, "-", " "))
	credential, token, err := h.registerMobileCredential(mobileRegistration{
		Code: normalized, Label: "phone", Platform: "android", Model: "iQOO 10", AppVersion: "0.1.0",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if token == "" || credential.ID == "" || len(credential.Scopes) != 3 {
		t.Fatalf("unexpected registration: %+v token=%q", credential, token)
	}
	if _, _, err := h.registerMobileCredential(mobileRegistration{Code: code}, now); err != ErrMobilePairingInvalid {
		t.Fatalf("pairing code should be one-time, got %v", err)
	}

	auth, err := h.authenticateMobile(token, now)
	if err != nil || auth.Via != "mobile" || auth.CredentialID != credential.ID || !auth.HasScope(MobileScopeDevices) {
		t.Fatalf("authenticate: %+v %v", auth, err)
	}
	row, err := h.getMobileCredential(credential.ID)
	if err != nil || row.LastUsedAt == nil {
		t.Fatalf("last used not recorded: %+v %v", row, err)
	}

	mux := http.NewServeMux()
	h.registerHTTP(mux)
	server := httptest.NewServer(mux)
	defer server.Close()

	request := func(method, path, bearer string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(method, server.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+bearer)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	resp := request(http.MethodGet, "/api/devices", token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("devices with mobile token = %d", resp.StatusCode)
	}
	resp.Body.Close()
	resp = request(http.MethodPost, "/api/devices/not-real/revoke", token)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("mobile mutation = %d", resp.StatusCode)
	}
	resp.Body.Close()
	resp = request(http.MethodGet, "/api/mobile/credentials", token)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("mobile list credentials = %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 轮换后旧 token 在宽限期内仍可用，宽限期后失效。
	newToken, err := h.rotateMobileCredential(credential.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.authenticateMobile(token, now.Add(time.Minute)); err != nil {
		t.Fatalf("old token within grace: %v", err)
	}
	if _, err := h.authenticateMobile(token, now.Add(MobileRotationGrace+time.Minute)); err != ErrMobileCredentialInvalid {
		t.Fatalf("old token after grace = %v", err)
	}
	if _, err := h.authenticateMobile(newToken, now.Add(MobileRotationGrace+time.Minute)); err != nil {
		t.Fatalf("new token: %v", err)
	}

	if _, err := h.revokeMobileCredential(credential.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.authenticateMobile(newToken, now); err != ErrMobileCredentialInvalid {
		t.Fatalf("revoked token = %v", err)
	}
}

func TestMobileCredentialExpiry(t *testing.T) {
	h := testHub(t)
	now := time.Now()
	_, token := issueTestMobile(t, h, now)
	if _, err := h.authenticateMobile(token, now.Add(MobileCredentialTTL+time.Minute)); err != ErrMobileCredentialExpired {
		t.Fatalf("expired token = %v", err)
	}
}

func TestListMobileCredentialsKeepsExpiredHiddenNothing(t *testing.T) {
	h := testHub(t)
	// 用一个 90 天前的时刻签发，让 expires_at 落在真实 now 之前——列表内部用的是
	// time.Now()，所以必须让过期时间真的过去，改不了时钟。
	past := time.Now().Add(-MobileCredentialTTL - time.Hour)
	credential, _ := issueTestMobile(t, h, past)

	list, err := h.listMobileCredentials()
	if err != nil {
		t.Fatal(err)
	}
	// 过期但没撤销的凭证仍要出现在列表里：用户需要看到它过期了，才知道该重新配对。
	if len(list) != 1 || list[0].ID != credential.ID {
		t.Fatalf("expired-but-not-revoked credential should be listed, got %+v", list)
	}
	if list[0].Status != "expired" {
		t.Fatalf("expected expired status, got %+v", list[0])
	}
}

func TestRevokeDeletesRowTombstoneFree(t *testing.T) {
	h := testHub(t)
	now := time.Now()
	kept, _ := issueTestMobile(t, h, now)
	gone, _ := issueTestMobile(t, h, now)

	if revoked, err := h.revokeMobileCredential(gone.ID); err != nil || !revoked {
		t.Fatalf("revoke = %v %v", revoked, err)
	}

	// 行必须真的从表里消失，不留墓碑。
	var rows int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM mobile_credentials WHERE id = ?`, gone.ID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("revoked credential should be deleted, found %d row(s)", rows)
	}

	// 未撤销的那台不受影响。
	list, err := h.listMobileCredentials()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != kept.ID || list[0].Status != "active" {
		t.Fatalf("list = %+v", list)
	}
}

func TestRevokeMissingCredentialReportsNotDeleted(t *testing.T) {
	h := testHub(t)
	now := time.Now()
	credential, _ := issueTestMobile(t, h, now)

	if revoked, err := h.revokeMobileCredential(credential.ID); err != nil || !revoked {
		t.Fatalf("first revoke = %v %v", revoked, err)
	}
	// 第二次撤销：行已经不在了，返回 false 让 handler 报 404。
	if revoked, err := h.revokeMobileCredential(credential.ID); err != nil || revoked {
		t.Fatalf("second revoke = %v %v", revoked, err)
	}
}

func TestListMobileCredentialsEmptyAfterRevokingAll(t *testing.T) {
	h := testHub(t)
	now := time.Now()
	first, _ := issueTestMobile(t, h, now)
	second, _ := issueTestMobile(t, h, now)
	for _, id := range []string{first.ID, second.ID} {
		if _, err := h.revokeMobileCredential(id); err != nil {
			t.Fatal(err)
		}
	}
	list, err := h.listMobileCredentials()
	if err != nil {
		t.Fatal(err)
	}
	// 前端靠 credentials.length === 0 显示空状态，所以这里必须是空数组而不是 nil。
	if list == nil {
		t.Fatal("list should be an empty slice, not nil")
	}
	if len(list) != 0 {
		t.Fatalf("expected no credentials, got %+v", list)
	}
}

func TestOpenDBPurgesLegacyRevokedCredentials(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.db")

	// 造一个旧库：一条有效的 + 一条当年软撤销的。
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE TABLE mobile_credentials (
			id TEXT PRIMARY KEY, secret_hash TEXT NOT NULL, previous_secret_hash TEXT,
			previous_valid_until TEXT, label TEXT NOT NULL, platform TEXT NOT NULL,
			model TEXT NOT NULL, app_version TEXT NOT NULL, scopes TEXT NOT NULL,
			created_at TEXT NOT NULL, last_used_at TEXT, expires_at TEXT NOT NULL,
			revoked_at TEXT)`,
		`INSERT INTO mobile_credentials VALUES
			('mc_keep','h1',NULL,NULL,'keep','android','m','1','[]','2026-01-01T00:00:00Z',NULL,'2099-01-01T00:00:00Z',NULL)`,
		`INSERT INTO mobile_credentials VALUES
			('mc_gone','h2',NULL,NULL,'gone','android','m','1','[]','2026-01-01T00:00:00Z',NULL,'2099-01-01T00:00:00Z','2026-02-02T00:00:00Z')`,
	} {
		if _, err := legacy.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	_ = legacy.Close()

	db, err := openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var remaining int
	if err := db.QueryRow(`SELECT COUNT(*) FROM mobile_credentials WHERE revoked_at IS NOT NULL`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("legacy revoked rows should be purged, found %d", remaining)
	}
	var kept int
	if err := db.QueryRow(`SELECT COUNT(*) FROM mobile_credentials WHERE id = 'mc_keep'`).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if kept != 1 {
		t.Fatal("active credential must survive the purge")
	}
}

func TestHarnessPathProxyRequiresAuthAndStripsCredentials(t *testing.T) {
	h := testHub(t)
	now := time.Now()
	credential, token := issueTestMobile(t, h, now)
	var mu sync.Mutex
	var seenAuth, seenCookie []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seenAuth = append(seenAuth, r.Header.Get("Authorization"))
		seenCookie = append(seenCookie, r.Header.Get("Cookie"))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><body>dsh</body></html>"))
	}))
	defer upstream.Close()

	registered, err := h.registerDevice(DeviceInfo{
		Name: "proxy-device", Hostname: "proxy-host", OS: "darwin", Arch: "arm64",
		Role: "test", BootID: "boot-proxy", Capabilities: []string{"exec"},
	}, "0.1.0", "1", now)
	if err != nil {
		t.Fatal(err)
	}
	// Run against the HTTP fixture without competing with an installed Hub for a fixed proxy port.
	h.harnessHTTP[registered.Device.ID] = newHarnessProxy(upstream.Listener.Addr().(*net.TCPAddr).Port, upstream.URL+"/")
	if err := h.setHarnessURL(registered.Device.ID, upstream.URL+"/", now); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	h.registerHTTP(mux)
	server := httptest.NewServer(mux)
	defer server.Close()

	path := "/harness-proxy/" + registered.Device.ID + "/"
	resp, err := http.Get(server.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated harness proxy = %d", resp.StatusCode)
	}
	resp.Body.Close()

	req, _ := http.NewRequest(http.MethodGet, server.URL+path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("mobile harness proxy = %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Desktop Web UI 走 session cookie，迁移后仍必须可用。
	sessionID, _, err := h.createWebSession(3600, now)
	if err != nil {
		t.Fatal(err)
	}
	req, _ = http.NewRequest(http.MethodGet, server.URL+path, nil)
	req.AddCookie(&http.Cookie{Name: "meerkat_session", Value: sessionID})
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("session harness proxy = %d", resp.StatusCode)
	}
	resp.Body.Close()

	mu.Lock()
	defer mu.Unlock()
	for _, value := range append(seenAuth, seenCookie...) {
		if strings.Contains(value, "Bearer") || strings.Contains(value, credential.ID) || strings.Contains(value, "meerkat_session") {
			t.Fatalf("hub credential leaked to DSH: %q", value)
		}
	}
}

func TestMobileCredentialDTOJSONShape(t *testing.T) {
	h := testHub(t)
	credential, token := issueTestMobile(t, h, time.Now())
	payload, err := json.Marshal(mobileCredentialToDTO(MobileCredentialRow{
		ID: credential.ID, Label: "x", Scopes: `["devices:read","tasks:read","harness:proxy"]`,
		CreatedAt: nowISO(time.Now()), ExpiresAt: nowISO(time.Now().Add(time.Hour)),
	}, time.Now()))
	if err != nil || !strings.Contains(string(payload), `"scopes"`) || strings.Contains(string(payload), token) {
		t.Fatalf("dto = %s err=%v", payload, err)
	}
}

func TestMobileCredentialEndpointsRevokeImmediately(t *testing.T) {
	h := testHub(t)
	mux := http.NewServeMux()
	h.registerHTTP(mux)
	server := httptest.NewServer(mux)
	defer server.Close()

	adminRequest := func(method, path string, body string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(method, server.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+h.AdminToken())
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	resp := adminRequest(http.MethodPost, "/api/mobile/pairing-tokens", `{"label":"phone"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("generate code = %d", resp.StatusCode)
	}
	var generated struct {
		Code string `json:"code"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&generated)
	resp.Body.Close()
	if generated.Code == "" {
		t.Fatal("empty pairing code")
	}

	req, _ := http.NewRequest(http.MethodPost, server.URL+"/api/mobile/register",
		strings.NewReader(`{"code":"`+generated.Code+`","label":"HTTP phone","platform":"android"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register = %d", resp.StatusCode)
	}
	var registered struct {
		CredentialID string   `json:"credential_id"`
		Token        string   `json:"token"`
		Scopes       []string `json:"scopes"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&registered)
	resp.Body.Close()
	if registered.Token == "" || registered.CredentialID == "" {
		t.Fatalf("register response = %+v", registered)
	}

	resp = adminRequest(http.MethodGet, "/api/mobile/credentials", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list = %d", resp.StatusCode)
	}
	var listed struct {
		Credentials []MobileCredential `json:"credentials"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&listed)
	resp.Body.Close()
	if len(listed.Credentials) != 1 || listed.Credentials[0].Status != "active" {
		t.Fatalf("list = %+v", listed)
	}

	resp = adminRequest(http.MethodPost, "/api/mobile/credentials/"+registered.CredentialID+"/revoke", "{}")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("revoke = %d", resp.StatusCode)
	}
	resp.Body.Close()

	req, _ = http.NewRequest(http.MethodGet, server.URL+"/api/devices", nil)
	req.Header.Set("Authorization", "Bearer "+registered.Token)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked request = %d", resp.StatusCode)
	}
	var failure struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&failure)
	resp.Body.Close()
	// 行已删除，Hub 只能回答「凭证无效」——这是硬删除带来的预期降级。
	if failure.Error.Code != "mobile_credential_invalid" {
		t.Fatalf("error code = %q", failure.Error.Code)
	}

	// 撤销后列表为空（行真的没了，不是被过滤掉）。
	resp = adminRequest(http.MethodGet, "/api/mobile/credentials", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list after revoke = %d", resp.StatusCode)
	}
	var afterRevoke struct {
		Credentials []MobileCredential `json:"credentials"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&afterRevoke)
	resp.Body.Close()
	if len(afterRevoke.Credentials) != 0 {
		t.Fatalf("credentials after revoke = %+v", afterRevoke.Credentials)
	}

	// 再撤销一次：行不存在，返回 404 而不是 409。
	resp = adminRequest(http.MethodPost, "/api/mobile/credentials/"+registered.CredentialID+"/revoke", "{}")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("second revoke = %d", resp.StatusCode)
	}
	var secondFailure struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&secondFailure)
	resp.Body.Close()
	if secondFailure.Error.Code != "mobile_credential_not_found" {
		t.Fatalf("second revoke code = %q", secondFailure.Error.Code)
	}
}

func TestMobileCredentialScopeDenied(t *testing.T) {
	h := testHub(t)
	credential, token := issueTestMobile(t, h, time.Now())
	// Remove tasks scope to prove endpoint-level enforcement, not just UI hiding.
	if _, err := h.db.Exec("UPDATE mobile_credentials SET scopes = ? WHERE id = ?",
		`["devices:read"]`, credential.ID); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	h.registerHTTP(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	req, _ := http.NewRequest(http.MethodGet, server.URL+"/api/tasks", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("tasks without scope = %d", resp.StatusCode)
	}
}
