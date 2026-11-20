//go:build windows

package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"meerkat/desktop/internal/update"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// The build injects the release version and public key. Local builds without a
// key can run normally, but cannot download or execute an update.
var (
	updateMu         sync.Mutex
	automaticUpdates atomic.Bool
	updateText       atomic.Value
	updateReadyOnce  sync.Once
)

func initializeUpdate() {
	if strings.TrimSpace(appCfg.UpdateManifestURL) == "" {
		appCfg.UpdateManifestURL = "https://github.com/teleJa/meerkat/releases/latest/download/latest.json"
	}
}

func checkForUpdate() map[string]any {
	status := map[string]any{"current_version": appVersion, "configured": appCfg.UpdateManifestURL != "", "available": false, "downloaded": false}
	_, manifest, err := update.Fetch(appCfg.UpdateManifestURL, updateKey())
	if err != nil {
		status["error"] = err.Error()
		return status
	}
	status["latest_version"] = manifest.Version
	status["notes"] = manifest.Notes
	status["available"] = update.Newer(manifest.Version, appVersion)
	return status
}

func updateKey() ed25519.PublicKey {
	key, _ := hex.DecodeString(updatePublicKey)
	return ed25519.PublicKey(key)
}

func updateDir() string { return filepath.Join(configDir(), "updates") }

func automaticUpdateSetting() string { return filepath.Join(updateDir(), "automatic-disabled") }

func setupUpdateMenu(app *application.App, menu *application.Menu) {
	_, err := os.Stat(automaticUpdateSetting())
	automaticUpdates.Store(errors.Is(err, os.ErrNotExist))
	menu.Add("检查更新").OnClick(func(*application.Context) {
		go showWindowsUpdateCheck(app)
	})
	menu.Add("更新状态").OnClick(func(*application.Context) {
		message, _ := updateText.Load().(string)
		if message == "" {
			message = "等待首次检查"
			if record, err := os.ReadFile(filepath.Join(updateDir(), "last-result.txt")); err == nil {
				message = string(record)
			}
		}
		app.Dialog.Info().SetTitle("Meerkat 更新").SetMessage("当前版本：" + appVersion + "\n" + message).Show()
	})
	menu.AddCheckbox("自动更新", automaticUpdates.Load()).OnClick(func(ctx *application.Context) {
		enabled := !automaticUpdates.Load()
		err := os.MkdirAll(updateDir(), 0o700)
		if err == nil {
			if enabled {
				err = os.Remove(automaticUpdateSetting())
				if errors.Is(err, os.ErrNotExist) {
					err = nil
				}
			} else {
				err = os.WriteFile(automaticUpdateSetting(), nil, 0o600)
			}
		}
		if err != nil {
			app.Dialog.Info().SetTitle("无法保存自动更新设置").SetMessage(err.Error()).Show()
			ctx.ClickedMenuItem().SetChecked(automaticUpdates.Load())
			return
		}
		automaticUpdates.Store(enabled)
		ctx.ClickedMenuItem().SetChecked(enabled)
	})
}

func showWindowsUpdateCheck(app *application.App) {
	if !updateMu.TryLock() {
		message, _ := updateText.Load().(string)
		app.Dialog.Info().SetTitle("检查更新").SetMessage(message).Show()
		return
	}
	status := checkForUpdate()
	updateMu.Unlock()
	if status["available"] != true {
		message, _ := status["error"].(string)
		if message == "" {
			message = "当前已是最新版本 " + appVersion
		}
		app.Dialog.Info().SetTitle("检查更新").SetMessage(message).Show()
		return
	}
	dialog := app.Dialog.Question().SetTitle("发现新版 " + status["latest_version"].(string))
	dialog.SetMessage("下载安装包后将退出 Meerkat，安装完成后会自动重新打开。\n\n" + status["notes"].(string))
	install := dialog.AddButton("下载并安装")
	install.OnClick(func() { go runDesktopUpdate(app, true) })
	later := dialog.AddButton("稍后")
	dialog.SetCancelButton(later)
	dialog.Show()
}

func startUpdateLoop(app *application.App) {
	if len(updateKey()) != ed25519.PublicKeySize {
		return
	}
	go func() {
		time.Sleep(30 * time.Second)
		for {
			if automaticUpdates.Load() {
				runDesktopUpdate(app, false)
			}
			time.Sleep(6 * time.Hour)
		}
	}()
}

func setUpdateText(message string) { updateText.Store(message) }

func runDesktopUpdate(app *application.App, manual bool) {
	if !updateMu.TryLock() {
		return
	}
	defer updateMu.Unlock()
	status := func(message string) {
		setUpdateText(message)
		log.Printf("更新 %s", message)
		if manual {
			app.Dialog.Info().SetTitle("检查更新").SetMessage(message).Show()
		}
	}
	setUpdateText("正在检查更新…")
	manifestData, manifest, err := update.Fetch(appCfg.UpdateManifestURL, updateKey())
	if err != nil {
		status("检查失败：" + err.Error())
		return
	}
	if !update.Newer(manifest.Version, appVersion) {
		status("当前已是最新版本 " + appVersion)
		return
	}
	platform, err := manifest.Package(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		status(err.Error())
		return
	}
	if _, err := windowsInstalledDirectory(); err != nil {
		status(fmt.Sprintf("发现新版 %s。当前安装无法自动替换，请使用正式安装包。", manifest.Version))
		return
	}
	if !manual && failedUpdateVersion() == manifest.Version {
		status("上次升级失败并回退，等待手动检查后重试。")
		return
	}
	if err := os.MkdirAll(updateDir(), 0o700); err != nil {
		status(err.Error())
		return
	}
	staging, err := os.MkdirTemp(updateDir(), "download-")
	if err != nil {
		status(err.Error())
		return
	}
	handoff := false
	defer func() {
		if !handoff {
			_ = os.RemoveAll(staging)
		}
	}()
	setUpdateText("正在下载 " + manifest.Version)
	err = update.Download(platform, staging, func(done, size int64) {
		if size > 0 {
			setUpdateText(fmt.Sprintf("正在下载 %s · %d%%", manifest.Version, done*100/size))
		} else {
			setUpdateText(fmt.Sprintf("正在下载 %s · %.1f MiB", manifest.Version, float64(done)/(1<<20)))
		}
	})
	if err != nil {
		status("下载失败：" + err.Error())
		return
	}
	if err := os.WriteFile(filepath.Join(staging, "latest.json"), manifestData, 0o600); err != nil {
		status(err.Error())
		return
	}
	if !manual {
		setUpdateText("已下载 " + manifest.Version + "，等待空闲后更新")
		for automaticUpdates.Load() && !automaticUpdateReady() {
			time.Sleep(30 * time.Second)
		}
		if !automaticUpdates.Load() {
			setUpdateText("自动更新已关闭")
			return
		}
	} else if !noTrackedUpdateWork() {
		status("任务仍在运行或状态未知，请完成手头任务后再检查更新。")
		return
	}
	if err := startWindowsUpdate(staging); err != nil {
		status("无法启动安装：" + err.Error())
		return
	}
	handoff = true
	setUpdateText("正在退出并安装 " + manifest.Version)
	// Use the same shutdown sequence as an in-app restart. Waiting for the
	// Wails WebView teardown can strand an accepted Windows update.
	cleanupChildren()
	os.Exit(0)
}

func failedUpdateVersion() string {
	data, _ := os.ReadFile(filepath.Join(updateDir(), "failed-version"))
	return string(data)
}

func noTrackedUpdateWork() bool {
	if firstRunPending() {
		return true
	}
	deviceID := localDeviceID()
	if localUIURL == "" || deviceID == "" {
		return false
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Timeout: 3 * time.Second, Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	taskDevice := ""
	if appCfg.Mode == "node" {
		taskDevice = deviceID
	}
	return hubTasksIdle(client, localUIURL, taskDevice) && localDSHIdle(client, localUIURL, deviceID)
}

func hubTasksIdle(client *http.Client, base, deviceID string) bool {
	var result struct {
		Tasks []struct {
			Status   string `json:"status"`
			DeviceID string `json:"device_id"`
		} `json:"tasks"`
		ActiveFilterApplied bool `json:"active_filter_applied"`
	}
	if !readUpdateStatus(client, base+"api/tasks?limit=500&active=1", "", &result) || result.Tasks == nil {
		return false
	}
	// A full page can hide an older active task on this device, including on
	// older Hubs which return the newest 500 tasks without the active filter.
	if len(result.Tasks) >= 500 {
		return false
	}
	for _, task := range result.Tasks {
		if deviceID != "" && task.DeviceID != "" && task.DeviceID != deviceID {
			continue
		}
		if result.ActiveFilterApplied || task.Status == "running" || task.Status == "queued" || task.Status == "cancel_requested" || task.Status == "" {
			return false
		}
	}
	return true
}

func localDSHIdle(client *http.Client, base, deviceID string) bool {
	id := url.PathEscape(deviceID)
	var status struct {
		Online bool `json:"online"`
		DSH    struct {
			Status string `json:"status"`
			HasURL bool   `json:"has_url"`
		} `json:"dsh"`
	}
	if !readUpdateStatus(client, base+"api/devices/"+id+"/dsh", "", &status) || !status.Online {
		return false
	}
	if status.DSH.Status == "stopped" && !status.DSH.HasURL {
		return true
	}
	if status.DSH.Status != "running" || !status.DSH.HasURL {
		return false
	}
	var sessions struct {
		Result struct {
			OK    bool `json:"ok"`
			Value struct {
				Items []struct{ Running bool } `json:"items"`
			} `json:"value"`
		} `json:"result"`
	}
	body := `{"type":"client-request","rpcId":"meerkat-update-idle","method":"session/list","payload":{"args":{"_request":{}}}}`
	if !readUpdateStatus(client, base+"harness-proxy/"+id+"/api/session/list", body, &sessions) || !sessions.Result.OK || sessions.Result.Value.Items == nil {
		return false
	}
	for _, session := range sessions.Result.Value.Items {
		if session.Running {
			return false
		}
	}
	return true
}

func readUpdateStatus(client *http.Client, target, body string, out any) bool {
	var response *http.Response
	var err error
	if body == "" {
		response, err = client.Get(target)
	} else {
		response, err = client.Post(target, "application/json", strings.NewReader(body))
	}
	if err != nil {
		return false
	}
	defer response.Body.Close()
	return response.StatusCode == http.StatusOK && json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(out) == nil
}

func writeUpdateReady() {
	if len(os.Args) != 3 || os.Args[1] != "--update-ready" {
		return
	}
	staging, err := filepath.Abs(os.Args[2])
	if err != nil || !validUpdateStaging(staging) {
		return
	}
	updateReadyOnce.Do(func() {
		data, err := json.Marshal(readyUpdate{PID: os.Getpid(), Version: appVersion})
		if err != nil {
			return
		}
		client := &http.Client{Timeout: 2 * time.Second}
		deadline := time.Now().Add(50 * time.Second)
		for time.Now().Before(deadline) {
			if updateServicesReady(client) {
				if err := os.WriteFile(filepath.Join(staging, "ready.json"), data, 0o600); err == nil {
					return
				}
			}
			time.Sleep(time.Second)
		}
		log.Print("更新启动后，本机界面或服务未就绪")
	})
}

func updateServicesReady(client *http.Client) bool {
	route := localUIURL
	if firstRunPending() {
		route += "welcome"
	} else {
		if edgeDone == nil {
			return false
		}
		select {
		case <-edgeDone:
			return false
		default:
		}
		var health struct{ OK bool }
		if !readUpdateStatus(client, localUIURL+"api/health", "", &health) || !health.OK {
			return false
		}
	}
	response, err := client.Get(route)
	if err != nil {
		return false
	}
	_ = response.Body.Close()
	return response.StatusCode == http.StatusOK
}
