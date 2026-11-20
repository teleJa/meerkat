//go:build !android

package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

const updateSettingsRoute = "/desktop/update-settings"

// This capability is delivered by the native window, not by the loopback page.
var updateSettingsToken = newUpdateSettingsToken()

func newUpdateSettingsToken() string {
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(token[:])
}

func updateSettingsScript(base string) string {
	return fmt.Sprintf(`if (window.top === window && location.origin === %q && location.pathname === "/" && !window.__meerkatUpdateToken) {
		Object.defineProperty(window, "__meerkatUpdateToken", {value: %q});
		window.dispatchEvent(new Event("meerkat-update-ready"));
	}`, strings.TrimRight(base, "/"), updateSettingsToken)
}

func setupUpdateMenu(app *application.App, menu *application.Menu, window *application.WebviewWindow) {
	menu.Add("打开更新设置").OnClick(func(*application.Context) {
		window.Show()
		window.Focus()
		if firstRunPending() {
			app.Dialog.Info().AttachToWindow(window).SetTitle("关于 Meerkat").SetMessage("当前版本 " + appVersion + "。完成工作区设置后可管理更新。").Show()
			return
		}
		window.ExecJS(`window.location.hash = "#/live/settings?section=about&open=" + Date.now()`)
	})
}

type updateSettingsState struct {
	Version       string                `json:"version"`
	Mode          string                `json:"mode"`
	Automatic     bool                  `json:"automatic"`
	Phase         string                `json:"phase"`
	Message       string                `json:"message,omitempty"`
	Available     string                `json:"available,omitempty"`
	Installable   bool                  `json:"installable"`
	LastCheck     *time.Time            `json:"last_check,omitempty"`
	CheckResult   string                `json:"check_result,omitempty"`
	LastInstall   string                `json:"last_install,omitempty"`
	FailedVersion string                `json:"failed_version,omitempty"`
	Bytes         int64                 `json:"bytes,omitempty"`
	Total         int64                 `json:"total,omitempty"`
	Prepared      bool                  `json:"prepared,omitempty"`
	Blocker       *updateBlockedProcess `json:"blocker,omitempty"`
}

type updateBlockedProcess struct {
	PID          uint32 `json:"pid"`
	Name         string `json:"name"`
	path         string
	creationTime int64
}

type updateSettingsRequest struct {
	Action  string `json:"action"`
	Enabled *bool  `json:"enabled,omitempty"`
	Version string `json:"version,omitempty"`
	PID     uint32 `json:"pid,omitempty"`
}

func authorizedUpdateSettingsRequest(r *http.Request, base, token string) bool {
	origin, err := url.Parse(base)
	if err != nil || origin.Scheme != "http" || origin.Hostname() != "127.0.0.1" || origin.Host == "" || token == "" {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && mediaType == "application/json" &&
		r.Method == http.MethodPost && r.Host == origin.Host &&
		r.Header.Get("Origin") == origin.Scheme+"://"+origin.Host &&
		r.Header.Get("Sec-Fetch-Site") == "same-origin" && r.Header.Get("Sec-Fetch-Mode") == "cors" &&
		subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Meerkat-Update-Token")), []byte(token)) == 1
}

func handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !authorizedUpdateSettingsRequest(r, localUIURL, updateSettingsToken) {
		http.Error(w, "桌面更新仅在 Meerkat 主窗口可用", http.StatusForbidden)
		return
	}
	var request updateSettingsRequest
	body := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	body.DisallowUnknownFields()
	if err := body.Decode(&request); err != nil || !errors.Is(body.Decode(new(any)), io.EOF) {
		http.Error(w, "更新请求无效", http.StatusBadRequest)
		return
	}
	setDesktopJSONHeader(w)
	if err := applyUpdateSettingsRequest(request); err != nil {
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": err.Error()})
		return
	}
	_ = json.NewEncoder(w).Encode(currentUpdateSettings())
}
