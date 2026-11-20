package main

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"log"
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

var (
	// updateWork serializes checks, downloads, and handoff. It is released
	// while waiting for idle time, so the settings page can install a staged update.
	updateWork       sync.Mutex
	updateViewMu     sync.RWMutex
	updateView       updateSettingsState
	updatePreference sync.Mutex
	automaticUpdates atomic.Bool
	pendingDownload  *windowsUpdateCandidate
	preparedUpdate   *windowsUpdateCandidate
)

var errUpdateTaskBusy = errors.New("有任务正在运行，或暂时无法确认任务状态。请在任务结束后重试。")
var errUpdateAutomaticDisabled = errors.New("自动更新已关闭，可以在这里手动安装。")
var errUpdateNotIdle = errors.New("已下载，等待电脑至少两分钟无输入后自动安装。")

type windowsUpdateCandidate struct {
	manifest  update.Manifest
	data      []byte
	platform  update.Platform
	staging   string
	automatic bool
}

type updateCheckRecord struct {
	At     time.Time `json:"at"`
	Result string    `json:"result"`
}

func loadWindowsUpdateSettings() {
	_, err := os.Stat(automaticUpdateSetting())
	automaticUpdates.Store(errors.Is(err, os.ErrNotExist))
	updateView = updateSettingsState{Phase: "idle"}
	var record updateCheckRecord
	if data, err := os.ReadFile(filepath.Join(updateDir(), "last-check.json")); err == nil && json.Unmarshal(data, &record) == nil {
		updateView.LastCheck = &record.At
		updateView.CheckResult = record.Result
	}
}

func currentUpdateSettings() updateSettingsState {
	updateViewMu.RLock()
	state := updateView
	updateViewMu.RUnlock()
	state.Version = appVersion
	state.Mode = "windows"
	state.Automatic = automaticUpdates.Load()
	if len(updateKey()) != ed25519.PublicKeySize {
		state.Mode = "unconfigured"
		state.Message = "此构建未配置更新签名公钥。"
	}
	if record, err := os.ReadFile(filepath.Join(updateDir(), "last-result.txt")); err == nil {
		state.LastInstall = string(record)
	}
	version := failedUpdateVersion()
	if version == "" && strings.HasPrefix(state.LastInstall, "更新 ") {
		version, _, _ = strings.Cut(strings.TrimPrefix(state.LastInstall, "更新 "), " 失败：")
	}
	if update.Newer(version, appVersion) {
		state.FailedVersion = version
	}
	return state
}

func setUpdatePhase(phase, message string) {
	updateViewMu.Lock()
	updateView.Phase, updateView.Message = phase, message
	if phase != "downloading" {
		updateView.Bytes, updateView.Total = 0, 0
	}
	if phase != "blocked" {
		updateView.Blocker = nil
	}
	updateViewMu.Unlock()
}

func setCheckResult(message, version string, installable bool) {
	now := time.Now()
	updateViewMu.Lock()
	updateView.LastCheck, updateView.CheckResult = &now, message
	updateView.Available, updateView.Installable = version, installable
	updateViewMu.Unlock()
	if err := saveUpdateCheck(updateCheckRecord{At: now, Result: message}); err != nil {
		log.Printf("保存最近一次更新检查失败：%v", err)
	}
}

func saveUpdateCheck(record updateCheckRecord) error {
	if err := os.MkdirAll(updateDir(), 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(updateDir(), "last-check-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err := errors.Join(json.NewEncoder(file).Encode(record), file.Close()); err != nil {
		return err
	}
	return os.Rename(file.Name(), filepath.Join(updateDir(), "last-check.json"))
}

func displayUpdateError(err error) string {
	// Go's URL error can contain a temporary redirect with signed query parameters.
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err.Error()
	}
	return err.Error()
}

func checkWindowsUpdate() *windowsUpdateCandidate {
	setUpdatePhase("checking", "正在检查更新…")
	data, manifest, err := update.Fetch(appCfg.UpdateManifestURL, updateKey())
	if err != nil {
		message := "检查失败：" + displayUpdateError(err)
		setCheckResult(message, "", false)
		setUpdatePhase("error", message)
		return nil
	}
	if !update.Newer(manifest.Version, appVersion) {
		setCheckResult("当前已是最新版本", "", false)
		setUpdatePhase("current", "当前已是最新版本")
		return nil
	}
	platform, err := manifest.Package(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		message := "当前平台暂无安装包：" + err.Error()
		setCheckResult(message, manifest.Version, false)
		setUpdatePhase("available", message)
		return nil
	}
	if _, err := windowsInstalledDirectory(); err != nil {
		message := "发现新版 " + manifest.Version + "，请使用正式安装包手动升级。"
		setCheckResult(message, manifest.Version, false)
		setUpdatePhase("available", message)
		return nil
	}
	message := "发现新版本 " + manifest.Version
	setCheckResult(message, manifest.Version, true)
	setUpdatePhase("available", message)
	return &windowsUpdateCandidate{manifest: manifest, data: data, platform: platform}
}

func saveAutomaticUpdatePreference(enabled bool) error {
	updatePreference.Lock()
	defer updatePreference.Unlock()
	if err := os.MkdirAll(updateDir(), 0o700); err != nil {
		return err
	}
	var err error
	if enabled {
		err = os.Remove(automaticUpdateSetting())
		if errors.Is(err, os.ErrNotExist) {
			err = nil
		}
	} else {
		err = os.WriteFile(automaticUpdateSetting(), nil, 0o600)
	}
	if err == nil {
		automaticUpdates.Store(enabled)
	}
	return err
}

func applyUpdateSettingsRequest(request updateSettingsRequest) error {
	if request.Action == "status" {
		return nil
	}
	if len(updateKey()) != ed25519.PublicKeySize {
		return errors.New("此构建未配置更新签名公钥")
	}
	if request.Action == "automatic" {
		if request.Enabled == nil {
			return errors.New("缺少自动更新设置")
		}
		if err := saveAutomaticUpdatePreference(*request.Enabled); err != nil {
			return fmt.Errorf("无法保存自动更新设置：%w", err)
		}
		return nil
	}
	if !updateWork.TryLock() {
		return errors.New("更新操作正在进行，请稍候")
	}
	started := false
	defer func() {
		if !started {
			updateWork.Unlock()
		}
	}()
	switch request.Action {
	case "check":
		if preparedUpdate != nil {
			return errors.New("安装包已下载，可以立即更新")
		}
		setUpdatePhase("checking", "正在检查更新…")
		started = true
		go func() {
			defer updateWork.Unlock()
			pendingDownload = checkWindowsUpdate()
		}()
	case "install", "end-process":
		state := currentUpdateSettings()
		if !update.Newer(request.Version, appVersion) || !((state.Installable && request.Version == state.Available) || request.Version == state.FailedVersion) {
			return errors.New("请先检查更新并确认要安装的版本")
		}
		var blocker *updateBlockedProcess
		if request.Action == "end-process" {
			if state.Blocker == nil || state.Blocker.PID != request.PID {
				return errors.New("占用进程已经变化，请重新检查")
			}
			blocker = state.Blocker
		}
		if desktopApp == nil || mainWindow == nil {
			return errors.New("Meerkat 主窗口尚未就绪")
		}
		setUpdatePhase("confirming", "等待确认安装 "+request.Version)
		started = true
		go func() {
			defer updateWork.Unlock()
			if !confirmWindowsUpdate(request.Version, blocker) {
				if state.Blocker != nil {
					setUpdateBlocker(state.Blocker, state.Message)
				} else {
					setUpdatePhase(state.Phase, state.Message)
				}
				return
			}
			runManualWindowsUpdate(request.Version, blocker)
		}()
	default:
		return errors.New("未知的更新操作")
	}
	return nil
}

func startUpdateLoop(_ *application.App) {
	if len(updateKey()) != ed25519.PublicKeySize {
		return
	}
	go func() {
		time.Sleep(30 * time.Second)
		for {
			if automaticUpdates.Load() && updateWork.TryLock() {
				if preparedUpdate == nil {
					pendingDownload = checkWindowsUpdate()
					if candidate := pendingDownload; candidate != nil && candidate.manifest.Version != failedUpdateVersion() && automaticUpdates.Load() {
						candidate.automatic = true
						prepareWindowsUpdate(candidate)
					}
				}
				updateWork.Unlock()
			}
			time.Sleep(6 * time.Hour)
		}
	}()
	go func() {
		tick := time.NewTicker(15 * time.Second)
		defer tick.Stop()
		for range tick.C {
			if !updateWork.TryLock() {
				continue
			}
			if candidate := preparedUpdate; candidate != nil && candidate.automatic {
				advanceAutomaticUpdate(candidate)
			}
			updateWork.Unlock()
		}
	}()
}

func advanceAutomaticUpdate(candidate *windowsUpdateCandidate) {
	if !automaticUpdates.Load() {
		candidate.automatic = false
		setUpdatePhase("ready", errUpdateAutomaticDisabled.Error())
		return
	}
	if !windowsInputIdle() {
		if currentUpdateSettings().Blocker == nil {
			setUpdatePhase("waiting", "已下载，等待电脑至少两分钟无输入后自动安装。")
		}
		return
	}
	installWindowsCandidate(candidate, false)
}

func prepareWindowsUpdate(candidate *windowsUpdateCandidate) bool {
	fail := func(err error) bool {
		setUpdatePhase("error", "下载失败："+displayUpdateError(err))
		return false
	}
	if err := os.MkdirAll(updateDir(), 0o700); err != nil {
		return fail(err)
	}
	staging, err := os.MkdirTemp(updateDir(), "download-")
	if err != nil {
		return fail(err)
	}
	prepared := false
	defer func() {
		if !prepared {
			_ = os.RemoveAll(staging)
		}
	}()
	setUpdatePhase("downloading", "正在下载 "+candidate.manifest.Version)
	if err := update.Download(candidate.platform, staging, func(done, total int64) {
		updateViewMu.Lock()
		updateView.Bytes, updateView.Total = done, total
		updateViewMu.Unlock()
	}); err != nil {
		return fail(err)
	}
	if err := os.WriteFile(filepath.Join(staging, "latest.json"), candidate.data, 0o600); err != nil {
		return fail(err)
	}
	candidate.staging = staging
	preparedUpdate = candidate
	prepared = true
	updateViewMu.Lock()
	updateView.Prepared = true
	updateViewMu.Unlock()
	setUpdatePhase("ready", "安装包已下载并通过校验。")
	return true
}

func confirmWindowsUpdate(version string, blocker *updateBlockedProcess) bool {
	message := "Meerkat 将退出当前版本 " + appVersion + "，安装 " + version + "，然后自动重新打开。安装会中断正在使用本机的工作，请先保存并结束任务。"
	if blocker != nil {
		message = fmt.Sprintf("将结束占用安装目录的 %s（PID %d）。\n\n%s", blocker.Name, blocker.PID, message)
	}
	dialog := desktopApp.Dialog.Question().AttachToWindow(mainWindow).SetTitle("安装 Meerkat " + version).SetMessage(message)
	confirmed := false
	dialog.AddButton("继续安装").OnClick(func() { confirmed = true })
	cancel := dialog.AddButton("取消")
	dialog.SetCancelButton(cancel).SetDefaultButton(cancel).Show()
	return confirmed
}

func runManualWindowsUpdate(version string, blocker *updateBlockedProcess) {
	if !noTrackedUpdateWork() {
		setUpdatePhase("waiting", errUpdateTaskBusy.Error())
		return
	}
	if blocker != nil {
		if err := endUpdateBlockedProcess(*blocker); err != nil {
			if errors.Is(err, errUpdateProcessExited) {
				setUpdatePhase("blocked", err.Error())
			} else {
				setUpdateBlocker(blocker, "未能结束该进程，请在任务管理器结束后点重试。")
			}
			return
		}
	}
	if preparedUpdate != nil && preparedUpdate.manifest.Version == version {
		installWindowsCandidate(preparedUpdate, true)
		return
	}
	candidate := pendingDownload
	if candidate == nil || candidate.manifest.Version != version {
		candidate = checkWindowsUpdate()
		pendingDownload = candidate
	}
	if candidate == nil {
		return
	}
	if candidate.manifest.Version != version {
		setUpdatePhase("available", "版本已变化，请确认新的安装版本。")
		return
	}
	if prepareWindowsUpdate(candidate) {
		installWindowsCandidate(candidate, true)
	}
}

func setUpdateBlocker(blocker *updateBlockedProcess, message string) {
	updateViewMu.Lock()
	updateView.Phase, updateView.Message, updateView.Blocker = "blocked", message, blocker
	updateViewMu.Unlock()
}

func installWindowsCandidate(candidate *windowsUpdateCandidate, manual bool) {
	if !noTrackedUpdateWork() {
		setUpdatePhase("waiting", errUpdateTaskBusy.Error())
		return
	}
	blocker, err := findUpdateBlockedProcess()
	if err != nil {
		setUpdatePhase("error", "无法核对安装目录占用："+err.Error())
		candidate.automatic = false
		return
	}
	if blocker != nil {
		setUpdateBlocker(blocker, "安装目录被残留进程占用。可结束该进程后继续，或在任务管理器中处理后重试。")
		return
	}
	setUpdatePhase("installing", "正在准备安装 "+candidate.manifest.Version)
	err = startWindowsUpdate(candidate.staging, func() error {
		if !noTrackedUpdateWork() {
			return errUpdateTaskBusy
		}
		if !manual && !automaticUpdates.Load() {
			return errUpdateAutomaticDisabled
		}
		if !manual && !windowsInputIdle() {
			return errUpdateNotIdle
		}
		if manual && failedUpdateVersion() == candidate.manifest.Version {
			if err := os.Remove(filepath.Join(updateDir(), "failed-version")); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		return nil
	})
	if errors.Is(err, errUpdateTaskBusy) || errors.Is(err, errUpdateAutomaticDisabled) || errors.Is(err, errUpdateNotIdle) {
		setUpdatePhase("waiting", err.Error())
		return
	}
	if err != nil {
		setUpdatePhase("error", "无法启动安装："+err.Error())
		candidate.automatic = false
		return
	}
	// The helper owns staging after the final check and waits for both processes.
	cleanupChildren()
	os.Exit(0)
}
