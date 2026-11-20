//go:build !windows && !android

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

func initializeUpdate()                       { loadDownloadedUpdate() }
func runUpdateHelper() (bool, error)          { return false, nil }
func recoverInterruptedUpdate() (bool, error) { return false, nil }
func checkUpdateGuard() error                 { return nil }
func writeUpdateReady()                       {}

func currentUpdateSettings() updateSettingsState {
	state := updateSettingsState{Version: appVersion, Mode: "manual", Phase: "idle"}
	if strings.TrimSpace(appCfg.UpdateManifestURL) == "" {
		state.Mode = "unconfigured"
		state.Message = "此平台当前没有配置更新地址。"
	}
	if info := currentDownloadedUpdate(); info != nil {
		state.Available, state.Installable, state.Prepared = info.Version, true, true
		state.Phase, state.Message = "ready", "安装包已下载。请按系统提示安装。"
	}
	return state
}

func applyUpdateSettingsRequest(request updateSettingsRequest) error {
	if request.Action == "status" {
		return nil
	}
	if desktopApp == nil || mainWindow == nil {
		return fmt.Errorf("Meerkat 主窗口尚未就绪")
	}
	switch request.Action {
	case "check":
		go showUpdateCheck(desktopApp)
	case "install":
		info := currentDownloadedUpdate()
		if info == nil || info.Version != request.Version {
			return fmt.Errorf("请先检查更新")
		}
		go promptInstallUpdate(desktopApp, true)
	default:
		return fmt.Errorf("未知的更新操作")
	}
	return nil
}

type updatePlatform struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256,omitempty"`
}

type updateManifest struct {
	Version   string                    `json:"version"`
	Notes     string                    `json:"notes,omitempty"`
	Platforms map[string]updatePlatform `json:"platforms"`
}

type updateStatus struct {
	Configured  bool   `json:"configured"`
	Current     string `json:"current_version"`
	Available   bool   `json:"available"`
	Latest      string `json:"latest_version,omitempty"`
	Notes       string `json:"notes,omitempty"`
	DownloadURL string `json:"download_url,omitempty"`
	SHA256      string `json:"sha256,omitempty"`
	Downloaded  bool   `json:"downloaded"`
	LocalPath   string `json:"local_path,omitempty"`
	Error       string `json:"error,omitempty"`
}

type downloadedUpdateInfo struct {
	Version string `json:"version"`
	Notes   string `json:"notes,omitempty"`
	Path    string `json:"path"`
	URL     string `json:"url,omitempty"`
}

var (
	updateMu              sync.Mutex
	downloadedUpdate      *downloadedUpdateInfo
	updateDownloading     bool
	updatePromptedVersion string
)

func updatesRoot() string {
	base := configDir()
	if base == "" {
		return ""
	}
	return filepath.Join(base, "updates")
}

func updateStatePath() string {
	root := updatesRoot()
	if root == "" {
		return ""
	}
	return filepath.Join(root, "state.json")
}

func loadDownloadedUpdate() {
	path := updateStatePath()
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var info downloadedUpdateInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return
	}
	if !isNewerVersion(info.Version, appVersion) {
		return
	}
	if _, err := os.Stat(info.Path); err != nil {
		return
	}
	updateMu.Lock()
	downloadedUpdate = &info
	updateMu.Unlock()
}

func persistDownloadedUpdate(info *downloadedUpdateInfo) error {
	path := updateStatePath()
	if path == "" {
		return fmt.Errorf("配置目录不可用")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func currentDownloadedUpdate() *downloadedUpdateInfo {
	updateMu.Lock()
	defer updateMu.Unlock()
	if downloadedUpdate == nil {
		return nil
	}
	if _, err := os.Stat(downloadedUpdate.Path); err != nil {
		return nil
	}
	copy := *downloadedUpdate
	return &copy
}

func isNewerVersion(latest, current string) bool {
	parse := func(value string) []int {
		value = strings.TrimPrefix(strings.TrimSpace(value), "v")
		parts := strings.Split(value, ".")
		out := make([]int, 0, len(parts))
		for _, part := range parts {
			digits := ""
			for _, r := range part {
				if r < '0' || r > '9' {
					break
				}
				digits += string(r)
			}
			number, _ := strconv.Atoi(digits)
			out = append(out, number)
		}
		return out
	}
	a, b := parse(latest), parse(current)
	for i := 0; i < len(a) || i < len(b); i++ {
		av, bv := 0, 0
		if i < len(a) {
			av = a[i]
		}
		if i < len(b) {
			bv = b[i]
		}
		if av != bv {
			return av > bv
		}
	}
	return false
}

func checkForUpdate() updateStatus {
	status := updateStatus{Current: appVersion}
	manifestURL := strings.TrimSpace(appCfg.UpdateManifestURL)
	if manifestURL == "" {
		status.Error = "未配置更新地址（update_manifest_url）"
		return status
	}
	status.Configured = true
	client := http.Client{Timeout: 8 * time.Second}
	resp, err := client.Get(manifestURL)
	if err != nil {
		status.Error = "检查更新失败：" + err.Error()
		return status
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		status.Error = fmt.Sprintf("更新清单返回 HTTP %d", resp.StatusCode)
		return status
	}
	var manifest updateManifest
	if err := json.NewDecoder(resp.Body).Decode(&manifest); err != nil {
		status.Error = "更新清单格式无效"
		return status
	}
	status.Latest = manifest.Version
	status.Notes = manifest.Notes
	platform := manifest.Platforms[runtime.GOOS]
	if platform.URL == "" {
		status.Error = "更新清单没有当前平台的下载地址"
		return status
	}
	status.DownloadURL = platform.URL
	status.SHA256 = platform.SHA256
	status.Available = isNewerVersion(manifest.Version, appVersion)
	if downloaded := currentDownloadedUpdate(); downloaded != nil && downloaded.Version == manifest.Version {
		status.Downloaded = true
		status.LocalPath = downloaded.Path
	}
	return status
}

func downloadUpdateFile(status updateStatus) (string, error) {
	parsed, err := url.Parse(status.DownloadURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", fmt.Errorf("下载地址无效")
	}
	name := filepath.Base(parsed.Path)
	if name == "" || name == "." || name == "/" {
		name = "Meerkat-Setup-" + status.Latest
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
	}
	root := updatesRoot()
	if root == "" {
		return "", fmt.Errorf("配置目录不可用")
	}
	dir := filepath.Join(root, "v"+status.Latest)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	target := filepath.Join(dir, name)
	partial := target + ".part"
	_ = os.Remove(partial)
	client := http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Get(status.DownloadURL)
	if err != nil {
		return "", fmt.Errorf("下载失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("下载返回 HTTP %d", resp.StatusCode)
	}
	out, err := os.OpenFile(partial, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	hasher := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(out, hasher), io.LimitReader(resp.Body, 512<<20))
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(partial)
		return "", fmt.Errorf("下载写入失败：%w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(partial)
		return "", closeErr
	}
	if status.SHA256 != "" {
		sum := hex.EncodeToString(hasher.Sum(nil))
		if !strings.EqualFold(sum, status.SHA256) {
			_ = os.Remove(partial)
			return "", fmt.Errorf("SHA256 校验失败")
		}
	}
	if err := os.Rename(partial, target); err != nil {
		_ = os.Remove(partial)
		return "", err
	}
	return target, nil
}

func downloadUpdate(app *application.App) {
	status := checkForUpdate()
	if status.Error != "" || !status.Available {
		return
	}
	updateMu.Lock()
	if updateDownloading {
		updateMu.Unlock()
		return
	}
	if downloadedUpdate != nil && downloadedUpdate.Version == status.Latest {
		updateMu.Unlock()
		promptInstallUpdate(app, false)
		return
	}
	updateDownloading = true
	updateMu.Unlock()

	path, err := downloadUpdateFile(status)
	updateMu.Lock()
	updateDownloading = false
	if err != nil {
		updateMu.Unlock()
		log.Printf("后台更新下载失败: %v", err)
		return
	}
	info := &downloadedUpdateInfo{Version: status.Latest, Notes: status.Notes, Path: path, URL: status.DownloadURL}
	downloadedUpdate = info
	updateMu.Unlock()
	if err := persistDownloadedUpdate(info); err != nil {
		log.Printf("保存更新状态失败: %v", err)
	}
	log.Printf("更新已下载: v%s -> %s", info.Version, path)
	if app != nil {
		promptInstallUpdate(app, false)
	}
}

func promptInstallUpdate(app *application.App, force bool) {
	if app == nil {
		return
	}
	info := currentDownloadedUpdate()
	if info == nil {
		return
	}
	updateMu.Lock()
	if !force && updatePromptedVersion == info.Version {
		updateMu.Unlock()
		return
	}
	updatePromptedVersion = info.Version
	updateMu.Unlock()

	dialog := app.Dialog.Question().SetTitle("更新已下载 v" + info.Version)
	message := "新版本已下载完成。\n\n版本：" + info.Version + "\n当前版本：" + appVersion + "\n\n什么时候安装由你决定。"
	if info.Notes != "" {
		message += "\n\n" + info.Notes
	}
	dialog.SetMessage(message)
	install := dialog.AddButton("立即安装")
	install.OnClick(func() { installDownloadedUpdate(app) })
	later := dialog.AddButton("稍后")
	dialog.SetCancelButton(later)
	dialog.Show()
}

func installDownloadedUpdate(app *application.App) {
	info := currentDownloadedUpdate()
	if info == nil {
		app.Dialog.Info().SetTitle("安装更新").SetMessage("没有已下载的更新").Show()
		return
	}
	openCmd := exec.Command("open", info.Path)
	hideConsole(openCmd)
	if err := openCmd.Start(); err != nil {
		app.Dialog.Info().SetTitle("打开更新失败").SetMessage(err.Error()).Show()
		return
	}
	app.Dialog.Info().SetTitle("更新已下载").SetMessage("已打开安装包，请按提示完成安装。").Show()
}

func startUpdateLoop(app *application.App) {
	go func() {
		time.Sleep(12 * time.Second)
		for {
			if strings.TrimSpace(appCfg.UpdateManifestURL) != "" {
				status := checkForUpdate()
				if status.Available && !status.Downloaded {
					downloadUpdate(app)
				}
			}
			time.Sleep(6 * time.Hour)
		}
	}()
}

func showUpdateCheck(app *application.App) {
	status := checkForUpdate()
	if status.Error != "" {
		app.Dialog.Info().SetTitle("检查更新").SetMessage(status.Error).Show()
		return
	}
	if !status.Available {
		app.Dialog.Info().SetTitle("检查更新").SetMessage(fmt.Sprintf("当前已是最新版本 %s", status.Current)).Show()
		return
	}
	if status.Downloaded {
		promptInstallUpdate(app, true)
		return
	}
	dialog := app.Dialog.Question().SetTitle("发现新版本 " + status.Latest)
	message := "当前版本：" + status.Current + "\n最新版本：" + status.Latest
	if status.Notes != "" {
		message += "\n\n" + status.Notes
	}
	message += "\n\n将在后台下载安装包，下载完成后由你决定什么时候安装。"
	dialog.SetMessage(message)
	download := dialog.AddButton("后台下载")
	download.OnClick(func() { go downloadUpdate(app) })
	later := dialog.AddButton("稍后")
	dialog.SetCancelButton(later)
	dialog.Show()
}
