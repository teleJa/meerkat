//go:build android

package androidclient

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// ErrNoToken means the client has no paired mobile credential.
var ErrNoToken = fmt.Errorf("尚未配对，请先配对设备")

// ErrCredentialUnreadable means Android Keystore could not decrypt the saved
// credential. The client never falls back to the legacy plaintext token.
var ErrCredentialUnreadable = fmt.Errorf("凭证不可读，请重新配对")

// ErrHubUnreachable wraps a network failure through the embedded tsnet proxy.
var ErrHubUnreachable = fmt.Errorf("无法连接 Hub，请检查内嵌网络")

// HubError is a structured error returned by the Hub API.
type HubError struct {
	Status  int    `json:"status"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *HubError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return "Hub 返回 HTTP " + fmt.Sprintf("%d", e.Status)
}

// UserMessage turns Hub error codes into distinct UI copy. Keeping this in one
// place prevents 401/expired/invalid from collapsing into "网络错误".
func (e *HubError) UserMessage() string {
	switch e.Code {
	case "mobile_credential_expired":
		return "凭证已过期，请重新配对"
	case "mobile_credential_invalid":
		return "凭证无效，请重新配对"
	case "mobile_scope_denied":
		return "当前凭证没有该操作权限"
	case "mobile_read_only":
		return "移动凭证只有只读权限"
	case "mobile_pairing_invalid":
		return "配对码无效、已使用或已过期"
	case "mobile_admin_required":
		return "该操作需要 Hub 管理员权限"
	case "unauthorized":
		return "Hub 拒绝了请求，请检查凭证"
	}
	if strings.TrimSpace(e.Message) != "" {
		return e.Message
	}
	return fmt.Sprintf("Hub 返回 HTTP %d", e.Status)
}

func parseHubError(status int, body []byte) *HubError {
	hubErr := &HubError{Status: status, Message: shortBody(body)}
	var payload struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err == nil {
		hubErr.Code = payload.Error.Code
		hubErr.Message = payload.Error.Message
	}
	return hubErr
}

// Device is the subset of the Hub device DTO the mobile client renders.
type Device struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Hostname         string `json:"hostname"`
	Status           string `json:"status"`
	LastSeenAt       string `json:"last_seen_at"`
	HarnessAvailable bool   `json:"harness_available"`
	DSH              struct {
		Status    string `json:"status"`
		Mode      string `json:"mode"`
		Profile   string `json:"profile"`
		Error     string `json:"error"`
		Installed bool   `json:"installed"`
		HasURL    bool   `json:"has_url"`
	} `json:"dsh"`
}

// Task is the subset of the Hub task DTO used by the list and detail pages.
type Task struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Source       string `json:"source"`
	DeviceID     string `json:"device_id"`
	Status       string `json:"status"`
	Stage        string `json:"stage"`
	Cwd          string `json:"cwd"`
	Request      string `json:"request"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
	EvidenceKind string `json:"evidence_kind"`
	EvidenceNote string `json:"evidence_note"`
}

// CredentialInfo mirrors the Hub list DTO; it never contains a token.
type CredentialInfo struct {
	ID           string   `json:"id"`
	Label        string   `json:"label"`
	Platform     string   `json:"platform"`
	Model        string   `json:"model"`
	AppVersion   string   `json:"app_version"`
	Scopes       []string `json:"scopes"`
	CreatedAt    string   `json:"created_at"`
	LastUsedAt   *string  `json:"last_used_at"`
	ExpiresAt    string   `json:"expires_at"`
	Status       string   `json:"status"`
	DaysToExpiry *int     `json:"days_to_expiry,omitempty"`
}

func (c *Client) hubConnection() (string, Auth, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	auth, err := c.Auth()
	return c.state.ProxyURL, auth, err
}

func (c *Client) apiGET(path string) (int, []byte, error) {
	proxy, auth, authErr := c.hubConnection()
	if proxy == "" {
		return 0, nil, fmt.Errorf("loopback proxy not ready")
	}
	if authErr != nil {
		return 0, nil, ErrCredentialUnreadable
	}
	if !auth.Configured() {
		return 0, nil, ErrNoToken
	}
	request, err := http.NewRequest(http.MethodGet, proxy+path, nil)
	if err != nil {
		return 0, nil, err
	}
	client := loopbackHTTPClient(25 * time.Second)
	response, err := client.Do(request)
	if err != nil {
		c.setHubState(proxy, "retrying")
		c.Logf("api: GET %s network error: %v", path, err)
		return 0, nil, ErrHubUnreachable
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	c.Logf("api: GET %s status=%d bytes=%d", path, response.StatusCode, len(body))
	c.observeResponse(auth, response.StatusCode, body)
	return response.StatusCode, body, nil
}

// observeResponse updates Hub/auth state without logging credentials.
func (c *Client) observeResponse(auth Auth, status int, body []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	current, _ := c.Auth()
	if current.HubURL != auth.HubURL || current.Token != auth.Token {
		return
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		c.markAuthFailureLocked(parseHubError(status, body))
		return
	}
	if status >= 200 && status < 400 {
		c.state.HubState = "connected"
		if auth.IsMobile() {
			c.cfgMu.Lock()
			c.auth.LastUsedAt = nowISO(time.Now())
			c.cfgMu.Unlock()
		}
	}
}

func (c *Client) markAuthFailureLocked(hubErr *HubError) {
	switch hubErr.Code {
	case "mobile_credential_expired":
		c.authFailure = "expired"
		c.state.AuthState = "expired"
		c.state.CredentialError = "凭证已过期，请重新配对"
	case "mobile_credential_invalid":
		c.authFailure = "invalid"
		c.state.AuthState = "invalid"
		c.state.CredentialError = "凭证无效，请重新配对"
	case "mobile_scope_denied":
		c.state.CredentialError = "当前凭证没有该操作权限"
	case "mobile_read_only":
		c.state.CredentialError = "移动凭证只有只读权限"
	default:
		c.state.CredentialError = hubErr.UserMessage()
	}
	c.Logf("auth: %d %s", hubErr.Status, hubErr.Code)
}

func (c *Client) setHubState(proxy, state string) {
	c.mu.Lock()
	if c.state.ProxyURL == proxy {
		c.state.HubState = state
	}
	c.mu.Unlock()
}

func shortBody(body []byte) string {
	text := strings.TrimSpace(string(body))
	if len(text) > 240 {
		text = text[:240] + "…"
	}
	return text
}

// FetchDevices returns the Hub device list using the current credential.
func (c *Client) FetchDevices() ([]Device, int, error) {
	status, body, err := c.apiGET("/api/devices")
	if err != nil {
		return nil, 0, err
	}
	if status != http.StatusOK {
		return nil, status, parseHubError(status, body)
	}
	var payload struct {
		Devices []Device `json:"devices"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, status, fmt.Errorf("invalid device response: %w", err)
	}
	c.SetDeviceCount(len(payload.Devices))
	return payload.Devices, status, nil
}

// FetchTasks returns the Hub task list.
func (c *Client) FetchTasks() ([]Task, int, error) {
	status, body, err := c.apiGET("/api/tasks?limit=100")
	if err != nil {
		return nil, 0, err
	}
	if status != http.StatusOK {
		return nil, status, parseHubError(status, body)
	}
	var payload struct {
		Tasks []Task `json:"tasks"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, status, fmt.Errorf("invalid task response: %w", err)
	}
	return payload.Tasks, status, nil
}

// FetchTaskDetail returns the full Hub task detail envelope.
func (c *Client) FetchTaskDetail(id string) (map[string]any, int, error) {
	status, body, err := c.apiGET("/api/tasks/" + url.PathEscape(id))
	if err != nil {
		return nil, 0, err
	}
	if status != http.StatusOK {
		return nil, status, parseHubError(status, body)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, status, fmt.Errorf("invalid task detail: %w", err)
	}
	return payload, status, nil
}

// PairDevice exchanges a one-time pairing code for a long-lived mobile
// credential and stores it in Android Keystore-backed storage.
func (c *Client) PairDevice(hubAddress, code, label, platform, model, appVersion string) error {
	code = strings.TrimSpace(code)
	if code == "" {
		return fmt.Errorf("请输入配对码")
	}
	c.credentialMu.Lock()
	defer c.credentialMu.Unlock()

	currentURL := c.Config().HubURL
	hubURL := currentURL
	if hubAddress != "" {
		var err error
		hubURL, err = ParseHubAddress(hubAddress)
		if err != nil {
			return err
		}
		// Preserve a previously provisioned HTTPS URL when re-pairing
		// with the same address displayed in the host:port input.
		if strings.TrimPrefix(hubURL, "http://") == hostFromURL(currentURL) {
			hubURL = currentURL
		}
	}
	proxy := c.ProxyURL()
	if proxy == "" {
		return fmt.Errorf("内嵌网络尚未就绪")
	}
	previous, _ := c.Auth()
	var replacement *hubProxy
	if hubURL != currentURL {
		var err error
		replacement, err = c.prepareProxy(hubURL, 0, true)
		if err != nil {
			return err
		}
		defer func() {
			if replacement != nil {
				replacement.close()
			}
		}()
	}
	payload, _ := json.Marshal(map[string]string{
		"code": code, "label": label, "platform": platform, "model": model, "app_version": appVersion,
	})
	request, err := http.NewRequest(http.MethodPost, hubURL+"/api/mobile/register", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	transport := &http.Transport{DialContext: c.dialHub}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport, Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	response, err := client.Do(request)
	if err != nil {
		if replacement == nil {
			c.setHubState(proxy, "retrying")
		}
		c.Logf("pair: network error")
		return ErrHubUnreachable
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		return fmt.Errorf("Hub 返回了跳转，请检查 Hub 地址")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("无法读取配对响应：%w", err)
	}
	if response.StatusCode != http.StatusCreated {
		c.Logf("pair: register status=%d", response.StatusCode)
		return parseHubError(response.StatusCode, body)
	}
	var result struct {
		CredentialID string         `json:"credential_id"`
		Token        string         `json:"token"`
		Scopes       []string       `json:"scopes"`
		ExpiresAt    string         `json:"expires_at"`
		Credential   CredentialInfo `json:"credential"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("配对响应无法解析")
	}
	if result.Token == "" || result.CredentialID == "" {
		return fmt.Errorf("配对响应缺少凭证")
	}
	expiresAt := result.ExpiresAt
	if expiresAt == "" {
		expiresAt = result.Credential.ExpiresAt
	}
	auth := Auth{
		Kind: AuthKindMobile, Token: result.Token, CredentialID: result.CredentialID,
		HubURL: hubURL, ProxyPort: previous.ProxyPort,
		Label: result.Credential.Label, Scopes: result.Scopes, CreatedAt: result.Credential.CreatedAt,
		ExpiresAt: expiresAt, LastUsedAt: nowISO(time.Now()),
	}
	if replacement != nil {
		auth.ProxyPort = replacement.port
	}
	if err := SaveAuth(c.store, auth); err != nil {
		return fmt.Errorf("凭证无法保存：%w", err)
	}
	if err := c.store.Delete(draftHubKey); err != nil {
		c.Logf("pair: could not clear saved address draft")
	}
	c.mu.Lock()
	c.state.DraftHubAddr = ""
	var retired *hubProxy
	if replacement != nil {
		retired = c.proxy
		c.activateProxyLocked(replacement)
		replacement = nil
		c.dshVersion, c.dshCommit = "", ""
		c.state.DSHVersion, c.state.DSHCommit = "", ""
		c.state.DeviceCount = 0
	}
	c.setAuthLocked(auth, nil)
	c.state.HubState = "connected"
	c.mu.Unlock()
	if retired != nil {
		retired.close()
	}
	c.Logf("pair: registered credential=%s", auth.CredentialID)
	return nil
}

// RotateCredential rotates the current mobile credential. The Hub keeps the old
// token valid for five minutes; the new token is saved immediately.
func (c *Client) RotateCredential() error {
	c.credentialMu.Lock()
	defer c.credentialMu.Unlock()
	auth, authErr := c.Auth()
	if authErr != nil {
		return ErrCredentialUnreadable
	}
	if !auth.IsMobile() {
		return fmt.Errorf("当前凭证不支持轮换")
	}
	proxy := c.ProxyURL()
	if proxy == "" {
		return fmt.Errorf("内嵌网络尚未就绪")
	}
	request, err := http.NewRequest(http.MethodPost, proxy+"/api/mobile/credentials/rotate", bytes.NewReader([]byte("{}")))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := loopbackHTTPClient(30 * time.Second).Do(request)
	if err != nil {
		c.setHubState(proxy, "retrying")
		return ErrHubUnreachable
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	c.observeResponse(auth, response.StatusCode, body)
	if response.StatusCode != http.StatusOK {
		return parseHubError(response.StatusCode, body)
	}
	var result struct {
		Token      string         `json:"token"`
		ExpiresAt  string         `json:"expires_at"`
		Credential CredentialInfo `json:"credential"`
	}
	if err := json.Unmarshal(body, &result); err != nil || result.Token == "" {
		return fmt.Errorf("轮换响应无法解析")
	}
	next := auth
	next.Token = result.Token
	next.LastUsedAt = nowISO(time.Now())
	if result.ExpiresAt != "" {
		next.ExpiresAt = result.ExpiresAt
	}
	if len(result.Credential.Scopes) > 0 {
		next.Scopes = result.Credential.Scopes
	}
	saveErr := SaveAuth(c.store, next)
	c.setAuth(next, nil)
	if saveErr != nil {
		return fmt.Errorf("新凭证已生效但未能保存：%w", saveErr)
	}
	c.Logf("credential: rotated credential=%s", next.CredentialID)
	return nil
}

// CheckHealth probes the public Hub health endpoint. It is used by the
// foreground keepalive loop; it never touches credentials.
func (c *Client) CheckHealth() error {
	proxy := c.ProxyURL()
	if proxy == "" {
		return fmt.Errorf("loopback proxy not ready")
	}
	response, err := loopbackHTTPClient(8 * time.Second).Get(proxy + "/api/health")
	if err != nil {
		c.setHubState(proxy, "retrying")
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode != http.StatusOK {
		c.setHubState(proxy, "retrying")
		return fmt.Errorf("health status %d", response.StatusCode)
	}
	c.setHubState(proxy, "connected")
	return nil
}

func nowISO(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

// HarnessURL returns the local proxy URL that the DSH WebView should load.
func (c *Client) HarnessURL(deviceID string) string {
	return c.ProxyURL() + "/harness-proxy/" + url.PathEscape(deviceID) + "/"
}

// CheckHarness probes the Hub harness proxy root. It also creates the Hub-side
// DSH cookie jar that the subsequent WebSocket/iframe requests use.
func (c *Client) CheckHarness(deviceID string) (int, error) {
	proxy, auth, authErr := c.hubConnection()
	if proxy == "" {
		return 0, fmt.Errorf("loopback proxy not ready")
	}
	request, err := http.NewRequest(http.MethodGet, proxy+"/harness-proxy/"+url.PathEscape(deviceID)+"/", nil)
	if err != nil {
		return 0, err
	}
	client := loopbackHTTPClient(30 * time.Second)
	response, err := client.Do(request)
	if err != nil {
		c.setHubState(proxy, "retrying")
		return 0, err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if authErr == nil {
		c.observeResponse(auth, response.StatusCode, body)
	}
	c.Logf("harness: check device=%s status=%d", deviceID, response.StatusCode)
	if response.StatusCode < 200 || response.StatusCode >= 400 {
		return response.StatusCode, parseHubError(response.StatusCode, body)
	}
	return response.StatusCode, nil
}

// ProbeHarnessWS checks the DSH WebSocket mux path through the local proxy and
// the Hub harness proxy. A 101 handshake is enough for the UI badge.
func (c *Client) ProbeHarnessWS(deviceID string) (string, error) {
	base := c.ProxyURL()
	if base == "" {
		return "", fmt.Errorf("loopback proxy not ready")
	}
	wsURL := "ws" + strings.TrimPrefix(base, "http") + "/harness-proxy/" + url.PathEscape(deviceID) + "/api/remote.mux"
	dialer := websocket.Dialer{HandshakeTimeout: 20 * time.Second, NetDialContext: dialLoopbackProxy}
	conn, response, err := dialer.Dial(wsURL, nil)
	if err != nil {
		status := 0
		if response != nil {
			status = response.StatusCode
		}
		c.Logf("harness: ws device=%s status=%d err=%v", deviceID, status, err)
		return "", fmt.Errorf("websocket status=%d: %w", status, err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, _, readErr := conn.ReadMessage(); readErr != nil {
		c.Logf("harness: ws device=%s upgrade=101 read=%v", deviceID, readErr)
		return "101 Switching Protocols（未收到首帧）", nil
	}
	c.Logf("harness: ws device=%s upgrade=101 frame=ok", deviceID)
	return "101 Switching Protocols + frame", nil
}
