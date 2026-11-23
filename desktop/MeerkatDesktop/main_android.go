//go:build android

package main

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"meerkat/desktop/androidclient"

	"github.com/wailsapp/wails/v3/pkg/application"
)

//go:embed client_page.html
var clientPage string

// Android client entrypoint. The desktop entry (main.go) is excluded by
// //go:build !android so the phone never starts the Hub or Edge sidecars.
func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	client := &androidClient{}
	mux := http.NewServeMux()
	client.register(mux)
	app := application.New(application.Options{
		Name: "Meerkat",
		Assets: application.AssetOptions{
			Handler: mux,
		},
	})
	client.start()
	window := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:  "Meerkat",
		URL:    "/",
		Width:  420,
		Height: 800,
	})
	// 回前台后补一次连接信号；必须在窗口建好之后挂，否则没有执行目标。
	attachResumeReconnect(client, window)
	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

type androidClient struct {
	client *androidclient.Client
	err    error
	// window 是外壳页所在的 Webview；回前台补连要往里提交 JS（见
	// main_android_resume.go）。DSH 本身跑在它的跨源 iframe 里，而
	// WebviewWindow.ExecJS 只作用于顶层 frame，所以信号必须先到外壳页。
	window *application.WebviewWindow
}

func (c *androidClient) start() {
	dataDir := strings.TrimSpace(application.Mobile.StoragePath())
	if dataDir == "" {
		c.err = fmt.Errorf("Android StoragePath() is empty")
		return
	}
	log.Printf("android client storage: %s", dataDir)
	client, err := androidclient.New(dataDir, wailsSecureStore{}, writeAndroidClipboard)
	if err != nil {
		c.err = err
		return
	}
	c.client = client
	client.Start()
	go c.keepAlive()
}

// wailsSecureStore adapts Wails' Keystore-backed Android secure storage to the
// small interface the client uses in tests.
type wailsSecureStore struct{}

func (wailsSecureStore) Set(key, value string) error { return application.Mobile.SecureSet(key, value) }
func (wailsSecureStore) Get(key string) (string, bool, error) {
	return application.Mobile.SecureGet(key)
}
func (wailsSecureStore) Delete(key string) error { return application.Mobile.SecureDelete(key) }

// keepAlive maintains the foreground notification and performs a health probe
// with exponential backoff. tsnet itself owns transport reconnects; this loop
// only decides when to refresh the notification and surfaces Hub state.
func (c *androidClient) keepAlive() {
	if c.client != nil {
		c.client.Logf("foreground: keepalive started")
	}
	delay := 5 * time.Second
	warnedExpiry := ""
	lastText := ""
	ticks := 0
	startForeground := func(text string) {
		payload, _ := json.Marshal(map[string]string{"title": "Meerkat 已连接", "text": text})
		application.Android.StartForegroundService(string(payload))
		if c.client != nil {
			c.client.Logf("foreground: start text=%q", text)
		}
	}
	if c.client != nil {
		snapshot := c.client.Snapshot()
		lastText = foregroundText(snapshot)
		startForeground(lastText)
	}
	for {
		time.Sleep(delay)
		ticks++
		if c.client == nil {
			continue
		}
		if err := c.client.CheckHealth(); err != nil {
			if delay < 30*time.Second {
				delay *= 2
				if delay > 30*time.Second {
					delay = 30 * time.Second
				}
			}
		} else {
			delay = 5 * time.Second
		}
		snapshot := c.client.Snapshot()
		text := foregroundText(snapshot)
		// The first call can land before the Wails activity is ready. Retry
		// every 30s even when the text is unchanged so the service starts.
		if text != lastText || ticks%6 == 0 {
			lastText = text
			startForeground(text)
		}
		if snapshot.AuthState == "expiring" && snapshot.CredentialExpiresAt != "" && warnedExpiry != snapshot.CredentialExpiresAt {
			warnedExpiry = snapshot.CredentialExpiresAt
			days := 0
			if snapshot.CredentialDays != nil {
				days = *snapshot.CredentialDays
			}
			body := fmt.Sprintf("移动设备凭证将在 %d 天后过期，请打开 Meerkat 续期。", days)
			notice, _ := json.Marshal(map[string]string{"title": "Meerkat 凭证即将过期", "body": body})
			application.Android.Notify(string(notice))
			if c.client != nil {
				c.client.Logf("credential: expiry warning days=%d", days)
			}
		}
	}
}

func foregroundText(state androidclient.State) string {
	switch state.State {
	case "needs_login":
		return "等待 Tailscale 登录"
	case "error":
		return "连接中断，正在重试"
	case "running":
		if state.HubState == "retrying" {
			return "连接中断，正在重试"
		}
		switch state.AuthState {
		case "none":
			return "尚未配对，打开 Meerkat 完成设置"
		case "expired", "invalid", "unreadable":
			if state.CredentialError != "" {
				return state.CredentialError
			}
			return "凭证不可用，请重新配对"
		}
		text := fmt.Sprintf("已连接 · %d 台设备", state.DeviceCount)
		if state.AuthState == "expiring" && state.CredentialDays != nil {
			text += fmt.Sprintf(" · 凭证 %d 天后过期", *state.CredentialDays)
		}
		return text
	default:
		return "正在连接内嵌网络"
	}
}

func (c *androidClient) register(mux *http.ServeMux) {
	mux.HandleFunc("/client/state", c.handleState)
	mux.HandleFunc("/client/config", c.handleConfigStatus)
	mux.HandleFunc("/client/config/clear", c.handleConfigClear)
	mux.HandleFunc("/client/config/", c.handleConfigSet)
	mux.HandleFunc("/client/pair/", c.handlePair)
	mux.HandleFunc("/client/credential/rotate", c.handleCredentialRotate)
	mux.HandleFunc("/client/health", c.handleHealth)
	mux.HandleFunc("/client/devices", c.handleDevices)
	mux.HandleFunc("/client/tasks", c.handleTasks)
	mux.HandleFunc("/client/tasks/", c.handleTaskDetail)
	mux.HandleFunc("/client/harness/", c.handleHarness)
	mux.HandleFunc("/client/wsprobe/", c.handleWSProbe)
	mux.HandleFunc("/client/open/", c.handleOpen)
	mux.HandleFunc("/client/paste", c.handlePaste)
	mux.HandleFunc("/client/copy-auth", c.handleCopyAuth)
	mux.HandleFunc("/client/statusbar/", c.handleStatusBar)
	mux.HandleFunc("/client/probe", c.handleProbe)
	mux.HandleFunc("/client/probe/", c.handleProbe)
	mux.HandleFunc("/client/hub-draft/", c.handleHubDraft)
	mux.HandleFunc("/client/dsh-log/", c.handleDSHLog)
	mux.HandleFunc("/", c.handlePage)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (c *androidClient) unavailable(w http.ResponseWriter) bool {
	if c.client != nil {
		return false
	}
	message := "tsnet client not started"
	if c.err != nil {
		message = c.err.Error()
	}
	writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": message})
	return true
}

func (c *androidClient) handleState(w http.ResponseWriter, _ *http.Request) {
	if c.unavailable(w) {
		return
	}
	writeJSON(w, http.StatusOK, c.client.Snapshot())
}

func (c *androidClient) handleConfigStatus(w http.ResponseWriter, _ *http.Request) {
	if c.unavailable(w) {
		return
	}
	cfg := c.client.Config()
	state := c.client.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                    true,
		"configured":            state.Configured,
		"hub_url":               cfg.HubURL,
		"auth_kind":             state.AuthKind,
		"auth_state":            state.AuthState,
		"credential_id":         state.CredentialID,
		"credential_label":      state.CredentialLabel,
		"credential_expires_at": state.CredentialExpiresAt,
		"credential_days":       state.CredentialDays,
		"credential_scopes":     state.CredentialScopes,
		"credential_error":      state.CredentialError,
	})
}

func (c *androidClient) handleConfigSet(w http.ResponseWriter, r *http.Request) {
	if c.unavailable(w) {
		return
	}
	token, err := androidclient.DecodePathParam("/client/config", r.URL.Path)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "缺少或无效的 token"})
		return
	}
	if err := c.client.SaveToken(token); err != nil {
		c.client.Logf("config: save token failed: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "configured": true})
}

func (c *androidClient) handleConfigClear(w http.ResponseWriter, _ *http.Request) {
	if c.unavailable(w) {
		return
	}
	if err := c.client.ClearToken(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "configured": false})
}

// writeClientError keeps network failures, missing credentials and Hub 401
// codes distinct for the mobile UI. It never includes a token.
func (c *androidClient) writeClientError(w http.ResponseWriter, prefix string, status int, err error) {
	payload := map[string]any{"ok": false, "status": status, "error": err.Error()}
	var hubErr *androidclient.HubError
	switch {
	case errors.Is(err, androidclient.ErrInvalidHubAddress):
		payload["error"] = "invalid_hub_address"
		payload["message"] = err.Error()
	case errors.Is(err, androidclient.ErrNoToken):
		payload["error"] = "not_configured"
		payload["message"] = "尚未配对或保存凭证"
	case errors.Is(err, androidclient.ErrCredentialUnreadable):
		payload["error"] = "credential_unreadable"
		payload["message"] = err.Error()
	case errors.As(err, &hubErr):
		payload["error"] = hubErr.Code
		payload["message"] = hubErr.UserMessage()
		payload["status"] = hubErr.Status
	default:
		payload["message"] = prefix + "：" + err.Error()
	}
	writeJSON(w, http.StatusOK, payload)
}

func (c *androidClient) handlePair(w http.ResponseWriter, r *http.Request) {
	if c.unavailable(w) {
		return
	}
	var body struct {
		Code       string `json:"code"`
		Label      string `json:"label"`
		HubAddress string `json:"hub_address"`
	}
	if r.Method == http.MethodGet {
		// Android's WebViewAssetLoader discards the body and query string.
		// The local page sends the code, label and address as path segments.
		// Keep accepting the earlier two-segment form for the current Hub.
		rest := strings.TrimPrefix(r.URL.Path, "/client/pair/")
		parts := strings.Split(rest, "/")
		if len(parts) == 3 {
			address, err := androidclient.DecodePathParam("/client/pair", "/client/pair/"+parts[2])
			if err != nil {
				c.writeClientError(w, "配对失败", 0, androidclient.ErrInvalidHubAddress)
				return
			}
			body.HubAddress = address
		} else if len(parts) > 3 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_pairing_request"})
			return
		}
		code, err := androidclient.DecodePathParam("/client/pair", "/client/pair/"+parts[0])
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_code"})
			return
		}
		body.Code = code
		if len(parts) > 1 {
			if label, labelErr := androidclient.DecodePathParam("/client/pair", "/client/pair/"+parts[1]); labelErr == nil {
				body.Label = label
			}
		}
	} else if r.Method == http.MethodPost {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_json"})
			return
		}
	} else {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method_not_allowed"})
		return
	}
	version := "0.1.0"
	if raw := application.Mobile.AppInfoJSON(); raw != "" {
		var info struct {
			Version string `json:"version"`
		}
		if json.Unmarshal([]byte(raw), &info) == nil && info.Version != "" {
			version = info.Version
		}
	}
	label := strings.TrimSpace(body.Label)
	if label == "" {
		label = "Android 手机"
	}
	before := c.client.Config().HubURL
	if err := c.client.PairDevice(body.HubAddress, body.Code, label, "android", "", version); err != nil {
		c.writeClientError(w, "配对失败", 0, err)
		return
	}
	state := c.client.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "configured": true, "auth_state": state.AuthState, "credential_id": state.CredentialID,
		"hub_addr": state.HubAddr, "hub_changed": before != c.client.Config().HubURL,
	})
}

func (c *androidClient) handleCredentialRotate(w http.ResponseWriter, r *http.Request) {
	if c.unavailable(w) {
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method_not_allowed"})
		return
	}
	if err := c.client.RotateCredential(); err != nil {
		c.writeClientError(w, "轮换失败", 0, err)
		return
	}
	state := c.client.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "auth_state": state.AuthState, "expires_at": state.CredentialExpiresAt})
}

func (c *androidClient) handleHealth(w http.ResponseWriter, _ *http.Request) {
	if c.unavailable(w) {
		return
	}
	if err := c.client.CheckHealth(); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (c *androidClient) handleDevices(w http.ResponseWriter, _ *http.Request) {
	if c.unavailable(w) {
		return
	}
	devices, status, err := c.client.FetchDevices()
	if err != nil {
		c.writeClientError(w, "设备加载失败", status, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "devices": devices})
}

func (c *androidClient) handleTasks(w http.ResponseWriter, _ *http.Request) {
	if c.unavailable(w) {
		return
	}
	tasks, status, err := c.client.FetchTasks()
	if err != nil {
		c.writeClientError(w, "任务加载失败", status, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "tasks": tasks})
}

func (c *androidClient) handleTaskDetail(w http.ResponseWriter, r *http.Request) {
	if c.unavailable(w) {
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/client/tasks/")
	if id == "" || strings.Contains(id, "/") {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "无效的任务 id"})
		return
	}
	detail, status, err := c.client.FetchTaskDetail(id)
	if err != nil {
		c.writeClientError(w, "任务详情加载失败", status, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "detail": detail})
}

func (c *androidClient) handleHarness(w http.ResponseWriter, r *http.Request) {
	if c.unavailable(w) {
		return
	}
	deviceID := strings.TrimPrefix(r.URL.Path, "/client/harness/")
	if deviceID == "" || strings.Contains(deviceID, "/") {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "无效的设备 id"})
		return
	}
	status, err := c.client.CheckHarness(deviceID)
	if err != nil {
		c.writeClientError(w, "DSH 打不开", status, err)
		return
	}
	if status < 200 || status >= 400 {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "status": status, "error": fmt.Sprintf("DSH 页面返回 HTTP %d", status)})
		return
	}
	wsMessage, wsErr := c.client.ProbeHarnessWS(deviceID)
	payload := map[string]any{
		"ok":    true,
		"url":   c.client.HarnessURL(deviceID),
		"ws":    wsMessage,
		"ws_ok": wsErr == nil,
	}
	if wsErr != nil {
		payload["ws_error"] = wsErr.Error()
	}
	writeJSON(w, http.StatusOK, payload)
}

func (c *androidClient) handleWSProbe(w http.ResponseWriter, r *http.Request) {
	if c.unavailable(w) {
		return
	}
	deviceID := strings.TrimPrefix(r.URL.Path, "/client/wsprobe/")
	if deviceID == "" || strings.Contains(deviceID, "/") {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "无效的设备 id"})
		return
	}
	message, err := c.client.ProbeHarnessWS(deviceID)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": message})
}

func (c *androidClient) handleOpen(w http.ResponseWriter, r *http.Request) {
	raw, err := androidclient.DecodePathParam("/client/open", r.URL.Path)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "缺少登录链接"})
		return
	}
	raw, err = androidclient.OpenURLCheck(raw)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if c.client != nil {
		c.client.Logf("open: requesting system browser url_len=%d", len(raw))
	}
	application.Android.OpenURL(raw)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handlePaste reads the native Android clipboard. WebView on Android 16
// denies navigator.clipboard.readText even after a user gesture, so the M1
// "粘贴" button goes through the activity's ClipboardManager instead.
func (c *androidClient) handlePaste(w http.ResponseWriter, _ *http.Request) {
	app := application.Get()
	if app == nil || app.Clipboard == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "系统剪贴板不可用"})
		return
	}
	text, ok := app.Clipboard.Text()
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "系统不允许读取剪贴板"})
		return
	}
	text = strings.TrimSpace(text)
	if text == "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "剪贴板是空的"})
		return
	}
	if len(text) > 4096 {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "剪贴板内容过长"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "text": text})
}

// handleCopyAuth copies the current Tailscale auth URL using the native
// clipboard, so it works even when navigator.clipboard.writeText is denied.
func (c *androidClient) handleCopyAuth(w http.ResponseWriter, _ *http.Request) {
	if c.unavailable(w) {
		return
	}
	authURL := strings.TrimSpace(c.client.Snapshot().AuthURL)
	if authURL == "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "暂时没有登录链接"})
		return
	}
	app := application.Get()
	if app == nil || app.Clipboard == nil || !app.Clipboard.SetText(authURL) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "写入剪贴板失败"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleStatusBar keeps the Android status bar readable when the system
// switches between light and dark themes. "style":"dark" means dark icons on
// a light background in the Wails Android bridge.
func (c *androidClient) handleStatusBar(w http.ResponseWriter, r *http.Request) {
	theme := strings.TrimPrefix(r.URL.Path, "/client/statusbar/")
	if theme != "light" && theme != "dark" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid theme"})
		return
	}
	style := "light"
	color := "#0b0c0f"
	if theme == "light" {
		style = "dark"
		color = "#f4f5f7"
	}
	payload := fmt.Sprintf(`{"hidden":false,"style":%q,"color":%q}`, style, color)
	application.Android.SetStatusBar(payload)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (c *androidClient) handleHubDraft(w http.ResponseWriter, r *http.Request) {
	if c.unavailable(w) {
		return
	}
	address, err := androidclient.DecodePathParam("/client/hub-draft", r.URL.Path)
	if err == nil {
		address, err = c.client.SaveHubDraft(address)
	}
	if err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "message": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "address": address})
}

func (c *androidClient) handleProbe(w http.ResponseWriter, r *http.Request) {
	if c.unavailable(w) {
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path != "/client/probe" {
		address, err := androidclient.DecodePathParam("/client/probe", r.URL.Path)
		if err != nil {
			http.Error(w, "无效的 Hub 地址", 400)
			return
		}
		_, _ = w.Write([]byte(c.client.ProbeAddress(address)))
		return
	}
	_, _ = w.Write([]byte(c.client.Probe()))
}

// handleDSHLog 接收 DSH 页内 shim 的触发结果，写进客户端日志文件。
//
// 为什么绕这一圈：真机上 vivo 把全局 log.tag 设成 I，Android 会丢弃所有 Log.d，
// WebChromeClient 的 onConsoleMessage → Log.d 那条路整个被系统吃掉（实测
// `log -t ProbeD -p d` 写不进去，`setprop log.tag.WailsActivity D` 后才出现），
// 于是 logcat 里 `grep -c dsh-resume` 恒为 0，主路径和兜底路径无法区分。
//
// 走「shim postMessage → 外壳页 → 这里」就不依赖日志级别：结果落在
// files/android-tsnet.log，真机上 `run-as com.meerkat.app cat files/android-tsnet.log`
// 直接可读。
//
// 结果用 base64url 放在路径里：WebView 的资产加载器会丢掉非 /wails/ 路径的
// query 和 body（见 pathparam.go），所以参数只能走路径段。
func (c *androidClient) handleDSHLog(w http.ResponseWriter, r *http.Request) {
	if c.unavailable(w) {
		return
	}
	result, err := androidclient.DecodePathParam("/client/dsh-log", r.URL.Path)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "无效的日志内容"})
		return
	}
	// 页内脚本已经截到 200 字符，这里再兜一次，避免异常页面把日志刷爆。
	if len(result) > 400 {
		result = result[:400]
	}
	c.client.Logf("dsh-resume: %s", result)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (c *androidClient) handlePage(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(clientPage))
}
