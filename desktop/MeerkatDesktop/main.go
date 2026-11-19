//go:build !android

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"meerkat/desktop/hub"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"github.com/wailsapp/wails/v3/pkg/icons"
)

//go:embed all:frontend/dist setup.html welcome.html welcome-icon.png
var assets embed.FS

type appConfig struct {
	Mode              string `json:"mode"`
	CenterURL         string `json:"center_url"`
	EdgeStateDir      string `json:"edge_state_dir"`
	EdgeAllowRoot     string `json:"edge_allow_root"`
	HarnessModel      string `json:"harness_model"`
	BindHost          string `json:"bind_host"`
	AllowLAN          bool   `json:"allow_lan"`
	AdminToken        string `json:"admin_token"`
	PairingToken      string `json:"pairing_token"`
	UpdateManifestURL string `json:"update_manifest_url"`
}

const appVersion = "0.1.0"

const desktopJSONContentType = "application/json; charset=utf-8"

func setDesktopJSONHeader(w http.ResponseWriter) {
	w.Header().Set("content-type", desktopJSONContentType)
}

func writeDesktopJSON(w http.ResponseWriter, status int, value any) {
	setDesktopJSONHeader(w)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

var (
	harnessOriginMu sync.Mutex
	harnessOrigins  = map[string]string{}
	desktopApp      *application.App
	mainWindow      *application.WebviewWindow
	edgeProcess     *exec.Cmd
	edgeParentPipe  io.WriteCloser
	edgeDone        chan struct{}
	edgeLockMessage string // Published before edgeDone is closed.
	cleanupOnce     sync.Once
	appCfg          appConfig
	localUIURL      string
	embeddedHub     *hub.Hub
)

func localDeviceID() string {
	data, err := os.ReadFile(filepath.Join(edgeStateDirectory(), "identity.json"))
	if err != nil {
		return ""
	}
	var identity map[string]any
	if err := json.Unmarshal(data, &identity); err != nil {
		return ""
	}
	value, _ := identity["device_id"].(string)
	return strings.TrimSpace(value)
}

func edgeTSNetState() string {
	data, err := os.ReadFile(filepath.Join(edgeStateDirectory(), "tsnet-status.json"))
	if err != nil {
		return ""
	}
	var status struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(data, &status); err != nil {
		return ""
	}
	return strings.TrimSpace(status.State)
}

func edgeLocalAPIHealthy(ctx context.Context, rawURL string) bool {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(rawURL, "/")+"/api/health", nil)
	if err != nil {
		return false
	}
	client := &http.Client{Timeout: 800 * time.Millisecond}
	response, err := client.Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	return response.StatusCode == http.StatusOK
}

func waitEdgeLocalAPIURL(parent context.Context) (string, error) {
	// 本地代理通常几秒内就绪；限时等待，避免 UI 请求长时间挂住。
	// needs_login 由 edgeNotReady 提供阶段说明，这里不再当成致命错误。
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	for {
		if value := edgeLocalAPIURL(); value != "" && edgeLocalAPIHealthy(ctx, value) {
			return value, nil
		}
		if edgeProcess == nil {
			return "", fmt.Errorf("Edge 未启动")
		}
		select {
		case <-edgeDone:
			return "", fmt.Errorf("Edge 进程已退出")
		default:
		}
		if edgeTSNetState() == "error" {
			return "", fmt.Errorf("Edge 内嵌网络启动失败")
		}
		select {
		case <-ctx.Done():
			if parent.Err() != nil {
				return "", parent.Err()
			}
			return "", fmt.Errorf("等待 Edge 本地代理超时，请稍候重试")
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func edgeLocalAPIURL() string {
	data, err := os.ReadFile(filepath.Join(edgeStateDirectory(), "tsnet-status.json"))
	if err != nil {
		return ""
	}
	var status struct {
		LocalAPIURL string `json:"local_api_url"`
	}
	if err := json.Unmarshal(data, &status); err != nil {
		return ""
	}
	value := strings.TrimSpace(status.LocalAPIURL)
	if strings.HasPrefix(value, "http://127.0.0.1:") {
		return value
	}
	return ""
}

func edgeStateDirectory() string {
	if strings.TrimSpace(appCfg.EdgeStateDir) != "" {
		return appCfg.EdgeStateDir
	}
	return filepath.Join(configDir(), "edge")
}

func configDir() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "Meerkat")
}

func runtimeRoot() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	base := filepath.Dir(exe)
	candidates := []string{
		filepath.Join(base, "..", "Resources"),
		filepath.Join(base, "runtime"),
	}
	for _, candidate := range candidates {
		if _, err := os.Stat(filepath.Join(candidate, "center", "server.cjs")); err == nil {
			return candidate
		}
	}
	return ""
}

func bundledPath(parts ...string) string {
	root := runtimeRoot()
	if root == "" {
		return ""
	}
	return filepath.Join(append([]string{root}, parts...)...)
}

func bundledNode() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	name := "node"
	if runtime.GOOS == "windows" {
		name = "node.exe"
	}
	candidates := []string{
		filepath.Join(filepath.Dir(exe), name),
		filepath.Join(runtimeRoot(), name),
	}
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return ""
}

func bundledCenterScript() string { return bundledPath("center", "server.cjs") }

func bundledEdge() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	name := "meerkat-edge"
	if runtime.GOOS == "windows" {
		name = "meerkat-edge.exe"
	}
	candidates := []string{
		filepath.Join(filepath.Dir(exe), name),
		filepath.Join(runtimeRoot(), name),
	}
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return ""
}

func adminTokenPath() string {
	if appCfg.Mode == "bundled" {
		return filepath.Join(configDir(), "center", "admin-token")
	}
	return ""
}

func loadConfig() {
	appCfg = appConfig{CenterURL: "http://127.0.0.1:8787"}
	path := desktopConfigPath()
	configFound := false
	if data, err := os.ReadFile(path); err == nil {
		configFound = true
		if err := json.Unmarshal(data, &appCfg); err != nil {
			log.Printf("读取 %s 失败: %v", path, err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		// 文件存在但读不出来（权限等）不能当成首次启动，否则会把用户配置覆盖掉。
		configFound = true
		log.Printf("读取 %s 失败: %v", path, err)
	}
	if value := strings.TrimSpace(os.Getenv("MEERKAT_CENTER_URL")); value != "" {
		appCfg.CenterURL = value
	}
	if value := strings.TrimSpace(os.Getenv("MEERKAT_EDGE_STATE")); value != "" {
		appCfg.EdgeStateDir = value
	}
	if value := strings.TrimSpace(os.Getenv("MEERKAT_EDGE_ALLOW_ROOT")); value != "" {
		appCfg.EdgeAllowRoot = value
	}
	if value := strings.TrimSpace(os.Getenv("MEERKAT_BIND_HOST")); value != "" {
		appCfg.BindHost = value
	}
	if value := strings.TrimSpace(os.Getenv("MEERKAT_ADMIN_TOKEN")); value != "" {
		appCfg.AdminToken = value
	}
	if value := strings.TrimSpace(os.Getenv("MEERKAT_PAIRING_TOKEN")); value != "" {
		appCfg.PairingToken = value
	}
	if value := strings.TrimSpace(os.Getenv("MEERKAT_UPDATE_MANIFEST")); value != "" {
		appCfg.UpdateManifestURL = value
	}
	switch appCfg.Mode {
	case "bundled", "node":
	case "external":
		// external 已移除；旧配置按 node 处理，避免再启动一个本地 Hub。
		log.Printf("external 模式已移除，按 node 处理；请重新加入工作区")
		appCfg.Mode = "node"
	default:
		if !configFound {
			// 真正的首次启动：不默认 bundled。之前这里直接落 bundled，等于
			// 「什么都没问就把这台机器当成 Hub」，用户会进到一个空的设备列表里，
			// 也永远看不到「加入别人的工作区」的入口（那个卡片只在 node 模式显示）。
			// 现在先把 mode 留空，由 main() 判断是否走欢迎页；
			// 用户选完才写 config.json 并重启。
			appCfg.Mode = ""
			break
		}
		if strings.TrimSpace(appCfg.Mode) != "" {
			log.Printf("未知 mode=%q，按 bundled 处理", appCfg.Mode)
		}
		appCfg.Mode = "bundled"
	}
	appCfg.CenterURL = strings.TrimRight(strings.TrimSpace(appCfg.CenterURL), "/")
	if appCfg.Mode == "bundled" {
		if appCfg.CenterURL == "" {
			appCfg.CenterURL = "http://127.0.0.1:8787"
		}
		if appCfg.EdgeStateDir == "" {
			appCfg.EdgeStateDir = filepath.Join(configDir(), "edge")
		}
		if appCfg.EdgeAllowRoot == "" {
			if home, err := os.UserHomeDir(); err == nil {
				appCfg.EdgeAllowRoot = home
			}
		}
	}
	if appCfg.CenterURL == "" {
		appCfg.CenterURL = "http://127.0.0.1:8787"
	}
	if appCfg.HarnessModel == "" {
		appCfg.HarnessModel = defaultHarnessModel()
	}
	if appCfg.EdgeStateDir == "" {
		appCfg.EdgeStateDir = filepath.Join(configDir(), "edge")
	}
	log.Printf("配置: mode=%s center=%s config=%s", appCfg.Mode, appCfg.CenterURL, path)
}

// firstRunPending 表示这是真正的第一次启动：没有可用的 mode，需要用户在欢迎页
// 二选一（加入现有工作区 / 创建新工作区）。此时不启动 Hub 与 Edge。
func firstRunPending() bool {
	return strings.TrimSpace(appCfg.Mode) == ""
}

const (
	// 主窗口的默认尺寸；屏幕放不下时按 clampWindowToWorkArea 收缩。
	defaultWindowWidth  = 1440
	defaultWindowHeight = 900
	// 收缩时给自己留的呼吸空间：菜单栏、任务栏、Dock 之后还能露出标题栏和边缘。
	windowEdgeMargin = 40
	// 收缩下限。再小的屏幕上宁可让窗口略微超出可用区（用户还能拖），
	// 也不要把界面压到放不下内容。
	windowMinWidth  = 720
	windowMinHeight = 520
)

// clampWindowToWorkArea 把期望的窗口尺寸收进屏幕可用区。
//
// 默认 1440×900 在 1366×768 的笔记本（或 1080p + 150% 缩放，可用区只有
// 1280×672）上会超出屏幕，欢迎页底部那两个按钮直接被顶到屏幕外，用户
// 既看不到也点不到——这是纯尺寸问题，不需要改布局，把窗口收进可用区即可。
func clampWindowToWorkArea(width, height int, workArea application.Rect) (int, int) {
	if workArea.Width <= 0 || workArea.Height <= 0 {
		return width, height
	}
	availWidth := workArea.Width - windowEdgeMargin
	availHeight := workArea.Height - windowEdgeMargin
	if availWidth < windowMinWidth {
		availWidth = workArea.Width
	}
	if availHeight < windowMinHeight {
		availHeight = workArea.Height
	}
	if width > availWidth {
		width = availWidth
	}
	if height > availHeight {
		height = availHeight
	}
	return width, height
}

// setupWindowName 是「加入工作区」子窗口的名字，用来复用它而不是每点一次托盘
// 就再开一个（重复点击会叠出好几个一样的窗口）。
const setupWindowName = "join-workspace"

// openSetupWindow 打开（或聚焦已有的）加入工作区窗口。
// 尺寸按屏幕可用区收一下：620 高的窗口在 1280×672 这类缩放下会把
// 底部「加入工作区」按钮挤出可视区。
func openSetupWindow(app *application.App) {
	if app == nil {
		return
	}
	// 首启时主窗口本身就是欢迎页，再开一个 720 宽的加入窗口会让人分不清
	// 哪个是「真正在走的流程」（而且那个窗口里的「返回」会把欢迎页装进小窗口）。
	// 此时只把主窗口叫到前面，用户在主窗口里点「用邀请码加入工作区」即可。
	if firstRunPending() {
		if mainWindow != nil {
			mainWindow.Show()
			mainWindow.Focus()
		}
		return
	}
	if existing, ok := app.Window.GetByName(setupWindowName); ok && existing != nil {
		existing.Show()
		existing.Focus()
		return
	}
	width, height := clampWindowToWorkArea(720, 620, primaryWorkArea(app))
	setupWindow := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:             setupWindowName,
		Title:            "加入 Meerkat 工作区",
		Width:            width,
		Height:           height,
		URL:              localUIURL + "setup",
		BackgroundColour: application.NewRGB(17, 18, 20),
	})
	setupWindow.Show()
	setupWindow.Focus()
}

// primaryWorkArea 取主屏可用区；拿不到时返回零值（clamp 会原样返回）。
func primaryWorkArea(app *application.App) application.Rect {
	if app == nil || app.Screen == nil {
		return application.Rect{}
	}
	screen := app.Screen.GetPrimary()
	if screen == nil {
		return application.Rect{}
	}
	return screen.WorkArea
}

// fitMainWindowToScreen 按当前屏幕可用区调整主窗口尺寸并居中。
func fitMainWindowToScreen(app *application.App, win *application.WebviewWindow) {
	if app == nil || win == nil {
		return
	}
	area := primaryWorkArea(app)
	width, height := clampWindowToWorkArea(defaultWindowWidth, defaultWindowHeight, area)
	if width == defaultWindowWidth && height == defaultWindowHeight && win.IsVisible() {
		// 大屏：尺寸不用动，只居中，省掉一次无意义的 resize。
		win.Center()
		log.Printf("主窗口尺寸: %dx%d（可用区 %v，无需收缩）", width, height, screenWorkAreaForLog(app))
		return
	}
	win.SetSize(width, height)
	win.Center()
	log.Printf("主窗口尺寸: %dx%d（可用区 %v）", width, height, screenWorkAreaForLog(app))
}

func screenWorkAreaForLog(app *application.App) string {
	if app == nil || app.Screen == nil {
		return "unknown"
	}
	screen := app.Screen.GetPrimary()
	if screen == nil {
		return "unknown"
	}
	return fmt.Sprintf("%dx%d @%.2fx", screen.WorkArea.Width, screen.WorkArea.Height, screen.ScaleFactor)
}

// createWorkspace 把本机落成 Hub（bundled）。只在欢迎页点「创建新工作区」时调用。
func createWorkspace() error {
	cfg := appConfig{
		Mode:          "bundled",
		CenterURL:     "http://127.0.0.1:8787",
		BindHost:      "127.0.0.1",
		HarnessModel:  defaultHarnessModel(),
		EdgeStateDir:  filepath.Join(configDir(), "edge"),
		EdgeAllowRoot: "",
	}
	if home, err := os.UserHomeDir(); err == nil {
		cfg.EdgeAllowRoot = home
	}
	return writeDesktopConfig(cfg)
}

func centerURL() string { return appCfg.CenterURL }

func centerPort() string {
	parsed, err := url.Parse(centerURL())
	if err == nil && parsed.Port() != "" {
		return parsed.Port()
	}
	return "8787"
}

func bindHost() string {
	host := strings.TrimSpace(appCfg.BindHost)
	if !appCfg.AllowLAN {
		if host != "" && host != "127.0.0.1" && host != "localhost" && host != "::1" {
			log.Printf("bind_host=%q 但 allow_lan=false，强制只监听 127.0.0.1", host)
		}
		return "127.0.0.1"
	}
	if host == "" {
		return "0.0.0.0"
	}
	return host
}

func centerHealthy() bool {
	// Hub 可能位于 tailnet 内；系统代理不应拦截 100.x/局域网地址。
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	client := http.Client{Timeout: 2 * time.Second, Transport: transport}
	resp, err := client.Get(centerURL() + "/api/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == 200
}

func centerStatus() string {
	if embeddedHub != nil {
		return "embedded go hub"
	}
	if centerHealthy() {
		return "running (remote hub)"
	}
	return "unreachable"
}

func waitCenterReady(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if centerHealthy() {
			return true
		}
		time.Sleep(250 * time.Millisecond)
	}
	return false
}

func startEmbeddedHub() {
	port, err := strconv.Atoi(centerPort())
	if err != nil || port <= 0 || port > 65535 {
		log.Printf("Center 端口无效: %s", centerPort())
		return
	}
	instance, err := hub.New(hub.Config{
		Host: bindHost(), Port: port, DataDir: filepath.Join(configDir(), "center"), Version: "0.1.0",
	})
	if err != nil {
		log.Printf("启动 Go Hub 失败: %v", err)
		return
	}
	if err := instance.Start(); err != nil {
		log.Printf("Go Hub 监听失败: %v", err)
		return
	}
	embeddedHub = instance
	instance.SetTurnNotificationSink(deliverTurnNotification)
	if err := instance.StartTSNet(); err != nil {
		log.Printf("Hub 内嵌 tsnet 启动失败: %v", err)
	}
	if waitCenterReady(10 * time.Second) {
		log.Printf("Go Hub 已就绪: %s", centerURL())
	} else {
		log.Printf("Go Hub 启动超时")
	}
}

func startCenterIfNeeded() {
	if appCfg.Mode != "bundled" {
		return
	}
	if centerHealthy() {
		log.Printf("Center 已在运行: %s", centerURL())
		return
	}
	startEmbeddedHub()
}

func herdrStatus() string {
	statusCmd := exec.Command("herdr", "status")
	hideConsole(statusCmd)
	out, err := statusCmd.CombinedOutput()
	if err != nil {
		return "Herdr 未安装或不可用：" + err.Error()
	}
	status := strings.TrimSpace(string(out))
	agentsCmd := exec.Command("herdr", "agent", "list")
	hideConsole(agentsCmd)
	agents, _ := agentsCmd.CombinedOutput()
	return status + "\n\n" + strings.TrimSpace(string(agents))
}

func pairingToken() (string, error) {
	if token := strings.TrimSpace(appCfg.PairingToken); token != "" {
		return token, nil
	}
	token, err := os.ReadFile(adminTokenPath())
	if err != nil {
		return "", err
	}
	body, _ := json.Marshal(map[string]string{"label": "desktop-app"})
	req, _ := http.NewRequest(http.MethodPost, centerURL()+"/api/pairing-tokens", bytes.NewReader(body))
	req.Header.Set("authorization", "Bearer "+strings.TrimSpace(string(token)))
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-meerkat-request", "1")
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("pairing token status %d", resp.StatusCode)
	}
	var payload struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", err
	}
	if payload.Token == "" {
		return "", fmt.Errorf("empty pairing token")
	}
	return payload.Token, nil
}

// edgeConfigPath 返回 Edge 状态目录下的 config.json。
func edgeConfigPath(stateDir string) string {
	return filepath.Join(stateDir, "config.json")
}

// writeEdgeConfig 合并写入 Edge 配置，保留用户已有的 harness 等字段。
func writeEdgeConfig(stateDir string, updates map[string]any) error {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	cfg := map[string]any{}
	if data, err := os.ReadFile(edgeConfigPath(stateDir)); err == nil {
		if err := json.Unmarshal(data, &cfg); err != nil {
			return fmt.Errorf("Edge 配置不是合法 JSON: %w", err)
		}
	}
	for key, value := range updates {
		cfg[key] = value
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(edgeConfigPath(stateDir), append(data, '\n'), 0o600)
}

// ensureBundledEdgeConfig 为 Hub 本机的 Edge 准备自配对配置。
// bundled 模式不启用 Edge 内嵌 tsnet：Edge run 用同一个 pairing_token 经本地 HTTP Hub 注册。
func ensureBundledEdgeConfig(stateDir string) error {
	if _, err := os.Stat(filepath.Join(stateDir, "identity.json")); err == nil {
		return nil
	}
	token, err := pairingToken()
	if err != nil {
		return fmt.Errorf("生成配对令牌: %w", err)
	}
	return writeEdgeConfig(stateDir, map[string]any{
		"dsh_web_enabled": true,
		"pairing_token":   token,
		"tsnet_hub_url":   strings.TrimRight(centerURL(), "/"),
	})
}

// pairingProgress 是 Edge 自动配对的进度（配对阶段与错误）。
type pairingProgress struct {
	Phase string `json:"phase,omitempty"`
	Error string `json:"error,omitempty"`
}

// edgeNetworkStatus 汇总 Edge 的 tsnet 状态、自动配对进度与身份，供 UI 展示加入流程。
type edgeNetworkStatus struct {
	Enabled      bool            `json:"enabled"`
	State        string          `json:"state"`
	AuthURL      string          `json:"auth_url,omitempty"`
	LocalAPIURL  string          `json:"local_api_url,omitempty"`
	LocalAPIErr  string          `json:"local_api_error,omitempty"`
	TailscaleIPs []string        `json:"tailscale_ips,omitempty"`
	Paired       bool            `json:"paired"`
	DeviceID     string          `json:"device_id,omitempty"`
	Pairing      pairingProgress `json:"pairing"`
}

// readEdgeNetworkStatus 合并 tsnet-status.json、pairing-status.json 与 identity.json。
// 三个文件都不存在时返回未启用状态，不报错。
func readEdgeNetworkStatus() edgeNetworkStatus {
	status := edgeNetworkStatus{}
	if data, err := os.ReadFile(filepath.Join(edgeStateDirectory(), "tsnet-status.json")); err == nil {
		_ = json.Unmarshal(data, &status)
	}
	if data, err := os.ReadFile(filepath.Join(edgeStateDirectory(), "pairing-status.json")); err == nil {
		_ = json.Unmarshal(data, &status.Pairing)
	}
	if deviceID := localDeviceID(); deviceID != "" {
		status.Paired = true
		status.DeviceID = deviceID
	}
	return status
}

// edgeNotReady 返回 node 模式下 /api 尚未可代理的原因（code, message）。
// message 为空表示本地 Edge 已配对且代理就绪；needs_login 属于等待状态而不是错误。
func edgeNotReady() (string, string) {
	select {
	case <-edgeDone:
		if edgeLockMessage != "" {
			return "edge_instance_conflict", edgeLockMessage
		}
	default:
	}
	status := readEdgeNetworkStatus()
	if status.State == "error" {
		return "edge_network_error", "Edge 内嵌网络启动失败，请打开网络设置重试"
	}
	if !status.Paired {
		if status.Pairing.Phase == "error" {
			message := "Edge 自动配对失败"
			if strings.TrimSpace(status.Pairing.Error) != "" {
				message += "：" + strings.TrimSpace(status.Pairing.Error)
			}
			return "edge_pairing_failed", message
		}
		if status.State == "needs_login" {
			return "edge_network_needs_login", "Edge 内嵌网络等待 Tailscale 登录，登录页会自动打开，完成后自动继续配对"
		}
		return "edge_network_starting", "Edge 正在加入内嵌网络并自动配对，请稍候"
	}
	if status.State != "" && status.State != "running" {
		return "edge_network_starting", "Edge 内嵌网络正在启动，请稍候"
	}
	if status.LocalAPIURL == "" {
		// Edge 报上来的具体失败原因优先，让用户看到「端口被占用/被系统保留」而不是笼统的「尚未就绪」。
		if detail := strings.TrimSpace(status.LocalAPIErr); detail != "" {
			return "edge_proxy_port_unavailable", "Edge 本地代理端口不可用：" + detail
		}
		return "edge_proxy_starting", "Edge 本地代理尚未就绪，请稍候"
	}
	return "", ""
}

// authOpenState 记录当前“等待 Tailscale 登录”期间自动打开授权页的去重状态。
type authOpenState struct {
	openedURL string
	openedAt  time.Time
}

// authOpenCooldown 是两次自动打开授权页的最小间隔。auth_url 变化会重置去重键，
// 但冷却期内不重复弹窗，避免 tsnet 轮换地址时浏览器反复弹出。
const authOpenCooldown = 30 * time.Second

// decideAuthOpen 判断是否要自动打开授权页，并返回更新后的去重状态：
//   - 只在 needs_login 且 auth_url 非空时打开；
//   - 同一 auth_url 只打开一次；
//   - auth_url 变化视为新的打开机会，但距上次自动打开不足 authOpenCooldown 时跳过；
//   - 调用方在状态离开 needs_login 时清空状态，下次登录可再次自动打开。
func decideAuthOpen(state, authURL string, prev authOpenState, now time.Time) (authOpenState, bool) {
	if state != "needs_login" {
		return authOpenState{}, false
	}
	authURL = strings.TrimSpace(authURL)
	if authURL == "" || authURL == prev.openedURL {
		return prev, false
	}
	if !prev.openedAt.IsZero() && now.Sub(prev.openedAt) < authOpenCooldown {
		return prev, false
	}
	return authOpenState{openedURL: authURL, openedAt: now}, true
}

// watchEdgeNetworkLogin 在 Edge 内嵌网络等待登录时自动打开 Tailscale 授权页。
// 同一 auth_url 只自动打开一次；离开 needs_login 后重置。用户仍可点界面按钮主动重开。
func watchEdgeNetworkLogin(app *application.App) {
	var state authOpenState
	lastSeenURL := ""
	coolingDownURL := ""
	for {
		time.Sleep(2 * time.Second)
		status := readEdgeNetworkStatus()
		authURL := strings.TrimSpace(status.AuthURL)
		if status.State != "needs_login" {
			state = authOpenState{}
			lastSeenURL = ""
			coolingDownURL = ""
			continue
		}
		if authURL != lastSeenURL {
			lastSeenURL = authURL
			if authURL != "" && state.openedURL != "" && authURL != state.openedURL {
				log.Printf("Edge 授权地址已更新：%s", authURL)
			}
		}
		next, open := decideAuthOpen(status.State, authURL, state, time.Now())
		if !open {
			// 地址变化但仍在冷却期：每个新地址只提示一次，避免每个轮询周期刷日志。
			if authURL != "" && authURL != state.openedURL && authURL != coolingDownURL && !state.openedAt.IsZero() {
				coolingDownURL = authURL
				log.Printf("Edge 授权地址变化，距上次自动打开不足 %s，不重复打开（可点界面按钮手动打开）", authOpenCooldown)
			}
			state = next
			continue
		}
		state = next
		if app == nil {
			continue
		}
		log.Printf("Edge 内嵌网络需要 Tailscale 登录，已自动打开授权页：%s", authURL)
		if err := app.Browser.OpenURL(authURL); err != nil {
			log.Printf("打开 Tailscale 授权页失败: %v", err)
		}
	}
}

func startEdge() {
	bin := bundledEdge()
	if bin == "" {
		if value := strings.TrimSpace(os.Getenv("MEERKAT_EDGE_BIN")); value != "" {
			bin = value
		}
	}
	if bin == "" {
		return
	}
	stateDir := strings.TrimSpace(appCfg.EdgeStateDir)
	if stateDir == "" {
		stateDir = filepath.Join(configDir(), "edge")
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		log.Printf("创建 Edge 状态目录失败: %v", err)
		return
	}
	if appCfg.Mode == "bundled" {
		if err := ensureBundledEdgeConfig(stateDir); err != nil {
			log.Printf("准备本地 Edge 自配对配置失败: %v", err)
			return
		}
	}
	if _, err := os.Stat(filepath.Join(stateDir, "identity.json")); err != nil {
		// 不再由 App 先跑 pair 子进程：Edge run 先入网、再用 pairing_token 自配对。
		if _, cfgErr := os.Stat(edgeConfigPath(stateDir)); cfgErr != nil {
			log.Printf("本地 Edge 未配置（%s），跳过启动", stateDir)
			return
		}
		log.Printf("本地 Edge 将在启动后自动配对（%s）", stateDir)
	}
	// --no-console：App 以无窗口方式拉起 Edge，Edge 再隐藏自己拉起的 DSH/harness 等子进程。
	args := []string{"run", "--state-dir", stateDir, "--no-console"}
	if root := strings.TrimSpace(appCfg.EdgeAllowRoot); root != "" {
		args = append(args, "--allow-root", root)
	}
	if model := strings.TrimSpace(appCfg.HarnessModel); model != "" {
		args = append(args, "--harness-model", model)
	}
	cmd := exec.Command(bin, args...)
	hideConsole(cmd)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), "MEERKAT_EDGE_PARENT_STDIN=1")
	parentPipe, err := cmd.StdinPipe()
	if err != nil {
		log.Printf("创建 Edge 生命周期管道失败: %v", err)
		return
	}
	if err := cmd.Start(); err != nil {
		_ = parentPipe.Close()
		log.Printf("启动本地 Edge 失败: %v", err)
		return
	}
	done := make(chan struct{})
	edgeProcess, edgeParentPipe, edgeDone = cmd, parentPipe, done
	go func() {
		err := cmd.Wait()
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == edgeRunLockConflictExitCode {
			edgeLockMessage = edgeRunConflictMessage(stateDir)
		}
		close(done)
		log.Printf("本地 Edge 退出: %v", err)
	}()
}

func bundledAuthToken() string {
	if token := strings.TrimSpace(appCfg.AdminToken); token != "" {
		return token
	}
	if adminTokenPath() == "" {
		return ""
	}
	token, err := os.ReadFile(adminTokenPath())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(token))
}

func ensureLocalSession(w http.ResponseWriter, r *http.Request) {
	// Kept for compatibility with webviews that do not persist the cookie set on
	// the initial navigation. The proxy also injects Bearer auth in bundled mode.
	_ = w
	_ = r
}

type joinCodePayload struct {
	V            int    `json:"v"`
	HubURL       string `json:"hub_url"`
	TSNetHubURL  string `json:"tsnet_hub_url,omitempty"`
	PairingToken string `json:"pairing_token"`
	AdminToken   string `json:"admin_token"`
}

func tailscaleIPv4() string {
	tailscaleCmd := exec.Command("tailscale", "ip", "-4")
	hideConsole(tailscaleCmd)
	out, err := tailscaleCmd.Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "100.") {
			return line
		}
	}
	return ""
}

func advertisedHubURL() string {
	if embeddedHub != nil {
		if value := embeddedHub.TSNetMagicDNSURL(); value != "" {
			return value
		}
	}
	parsed, err := url.Parse(centerURL())
	if err == nil {
		host := parsed.Hostname()
		if host != "" && host != "127.0.0.1" && host != "localhost" && host != "0.0.0.0" && host != "::1" {
			return "http://" + net.JoinHostPort(host, centerPort())
		}
	}
	ip := tailscaleIPv4()
	if ip == "" {
		ip = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(ip, centerPort())
}

func createJoinCode() (string, error) {
	if appCfg.Mode != "bundled" {
		return "", fmt.Errorf("只有 Hub 模式可以生成邀请码")
	}
	pairing, err := pairingToken()
	if err != nil {
		return "", fmt.Errorf("生成配对 token: %w", err)
	}
	admin := bundledAuthToken()
	if admin == "" {
		return "", fmt.Errorf("读取 Hub admin token 失败")
	}
	payload := joinCodePayload{
		V:            1,
		HubURL:       advertisedHubURL(),
		PairingToken: pairing,
		AdminToken:   admin,
	}
	if embeddedHub != nil {
		payload.TSNetHubURL = embeddedHub.TSNetURL()
	}
	// 目标设备要通过内嵌网络访问 Hub：Hub 的 tsnet 未就绪时邀请码无法完成配对。
	if strings.TrimSpace(payload.TSNetHubURL) == "" {
		return "", fmt.Errorf("Hub 的内嵌网络尚未就绪：请先在「设置 → 网络」启动并登录内嵌 Tailscale 网络，再生成邀请码")
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return "MEERKAT1:" + base64.RawURLEncoding.EncodeToString(data), nil
}

func parseJoinCode(code string) (joinCodePayload, error) {
	code = strings.TrimSpace(code)
	code = strings.TrimPrefix(code, "MEERKAT1:")
	raw, err := base64.RawURLEncoding.DecodeString(code)
	if err != nil {
		return joinCodePayload{}, fmt.Errorf("邀请码格式无效")
	}
	var payload joinCodePayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return joinCodePayload{}, fmt.Errorf("邀请码内容无效")
	}
	if payload.HubURL == "" || payload.PairingToken == "" || payload.AdminToken == "" {
		return joinCodePayload{}, fmt.Errorf("邀请码缺少字段")
	}
	if strings.TrimSpace(payload.TSNetHubURL) == "" {
		return joinCodePayload{}, fmt.Errorf("邀请码缺少内嵌网络地址，请在 Hub 上重新生成邀请码")
	}
	parsed, err := url.Parse(payload.HubURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return joinCodePayload{}, fmt.Errorf("Hub 地址无效")
	}
	return payload, nil
}

func writeNodeConfig(payload joinCodePayload) error {
	stateDir := filepath.Join(configDir(), "edge-node")
	_ = os.RemoveAll(stateDir)
	home, _ := os.UserHomeDir()
	cfg := appConfig{
		Mode:          "node",
		CenterURL:     strings.TrimRight(payload.HubURL, "/"),
		AdminToken:    payload.AdminToken,
		PairingToken:  payload.PairingToken,
		EdgeStateDir:  stateDir,
		EdgeAllowRoot: home,
		HarnessModel:  defaultHarnessModel(),
		BindHost:      "127.0.0.1",
	}
	if err := writeDesktopConfig(cfg); err != nil {
		return err
	}

	// Node 的 Edge 启用内嵌 tsnet：Edge run 先入网，再用邀请码里的一次性令牌自配对。
	// 地址复用 tsnet_hub_url，不依赖系统 Tailscale 与 MagicDNS。
	tsnetHubURL := strings.TrimSpace(payload.TSNetHubURL)
	hostname, _ := os.Hostname()
	var cleaned strings.Builder
	for _, r := range strings.ToLower(hostname) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			cleaned.WriteRune(r)
		}
	}
	suffix := strings.Trim(cleaned.String(), "-")
	if len(suffix) > 12 {
		suffix = suffix[:12]
	}
	if suffix == "" {
		suffix = "node"
	}
	return writeEdgeConfig(stateDir, map[string]any{
		"dsh_web_enabled": true,
		"tsnet_enabled":   true,
		"tsnet_hostname":  "meerkat-edge-" + suffix,
		"tsnet_hub_url":   tsnetHubURL,
		"pairing_token":   strings.TrimSpace(payload.PairingToken),
	})
}

func defaultHarnessModel() string {
	if runtime.GOOS == "windows" {
		return "group/deepseek-flash"
	}
	return "group/coding"
}

func restartSelf() {
	cleanupChildren()
	time.Sleep(500 * time.Millisecond)
	exe, err := os.Executable()
	if err != nil {
		log.Printf("重启失败: %v", err)
		return
	}
	if desktopInstanceLock != nil {
		_ = desktopInstanceLock.Close()
		desktopInstanceLock = nil
	}
	cmd := exec.Command(exe)
	hideConsole(cmd)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		log.Printf("重启失败: %v", err)
		return
	}
	os.Exit(0)
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
	switch runtime.GOOS {
	case "windows":
		cmd := exec.Command(info.Path, "/S")
		hideConsole(cmd)
		cmd.Dir = filepath.Dir(info.Path)
		if err := cmd.Start(); err != nil {
			app.Dialog.Info().SetTitle("安装更新失败").SetMessage(err.Error()).Show()
			return
		}
		log.Printf("启动更新安装器: %s", info.Path)
		go func() {
			time.Sleep(1500 * time.Millisecond)
			cleanupChildren()
			app.Quit()
		}()
	default:
		openCmd := exec.Command("open", info.Path)
		hideConsole(openCmd)
		if err := openCmd.Start(); err != nil {
			app.Dialog.Info().SetTitle("打开更新失败").SetMessage(err.Error()).Show()
			return
		}
		app.Dialog.Info().SetTitle("更新已下载").SetMessage("已打开安装包，请按提示完成安装。").Show()
	}
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

func newUIProxy(target *url.URL) http.Handler {
	proxy := httputil.NewSingleHostReverseProxy(target)
	// 不继承系统代理，否则 Hub 在 tailnet 时请求会被 Clash 等代理拦截。
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	proxy.Transport = transport
	original := proxy.Director
	proxy.Director = func(req *http.Request) {
		original(req)
		if token := bundledAuthToken(); token != "" && req.Header.Get("Authorization") == "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}
	return logFileProxyRequests(proxy, "ui-proxy", appendFileProxyDiagnostic)
}

func ensureHarnessOrigin(deviceID string) (string, error) {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return "", fmt.Errorf("缺少设备 ID")
	}
	harnessOriginMu.Lock()
	if existing := harnessOrigins[deviceID]; existing != "" {
		harnessOriginMu.Unlock()
		return existing, nil
	}
	harnessOriginMu.Unlock()

	base := strings.TrimRight(localUIURL, "/")
	if base == "" {
		return "", fmt.Errorf("本地 UI 未就绪")
	}
	target, err := url.Parse(base + "/harness-proxy/" + url.PathEscape(deviceID) + "/")
	if err != nil {
		return "", err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	// harness origin 上注入 window.open shim：DSH 侧边栏的「在系统浏览器中打开」
	// 是 window.open(url,'_blank','noopener')，Wails 的 WebView 会静默丢弃，
	// 所以把它转成一个同源 POST，由 Go 侧调 Browser.OpenURL。
	var review *reviewOpen
	if previewID.MatchString(deviceID) {
		review = &reviewOpen{
			host: previewHost{origin: base, device: deviceID}, authority: listener.Addr().String(),
			localID: localDeviceID, confirm: authorizeReviewOpen, open: harnessOpenExternalURL,
			gestureToken: newHarnessToken(),
		}
	}
	handler := newHarnessOriginHandler(target, harnessOpenURLToken, review)
	go func() {
		if serveErr := http.Serve(listener, handler); serveErr != nil {
			log.Printf("Harness 本地代理停止: %v", serveErr)
		}
	}()
	origin := "http://" + listener.Addr().String() + "/"
	harnessOriginMu.Lock()
	harnessOrigins[deviceID] = origin
	harnessOriginMu.Unlock()
	return origin, nil
}

func startUI() (string, error) {
	dist, err := fs.Sub(assets, "frontend/dist")
	if err != nil {
		return "", err
	}
	target, err := url.Parse(centerURL())
	if err != nil {
		return "", fmt.Errorf("center URL 无效: %w", err)
	}
	handler := buildUIHandler(dist, newUIProxy(target))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	go func() {
		if err := http.Serve(listener, handler); err != nil {
			log.Printf("desktop local server stopped: %v", err)
		}
	}()
	return "http://" + listener.Addr().String() + "/", nil
}

// buildUIHandler 组装桌面端本地 HTTP 路由（页面、桌面 API、Hub 反向代理）。
// 单独抽出来是为了让 httptest 能直接打这些端点，不用真起 Wails。
func buildUIHandler(dist fs.FS, proxy http.Handler) http.Handler {
	fileServer := http.FileServer(http.FS(dist))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/welcome" {
			// 已经有配置的用户不该再看到欢迎页：这里的两个按钮都会重写 config
			// （「加入」写 node、「创建」写 bundled），node 用户误入后点一下就会
			// 把 Hub 地址和配对令牌整段覆盖掉。直接送回主界面。
			if !firstRunPending() {
				http.Redirect(w, r, "/", http.StatusFound)
				return
			}
			data, err := assets.ReadFile("welcome.html")
			if err != nil {
				http.Error(w, "welcome page missing", http.StatusInternalServerError)
				return
			}
			w.Header().Set("content-type", "text/html; charset=utf-8")
			_, _ = w.Write(data)
			return
		}
		// 欢迎页「创建新工作区」：写 bundled 配置后重启，和加入工作区对称。
		if r.URL.Path == "/desktop/create-workspace" && r.Method == http.MethodPost {
			if err := createWorkspace(); err != nil {
				writeDesktopJSON(w, http.StatusInternalServerError, map[string]any{
					"error": map[string]string{"message": err.Error()},
				})
				return
			}
			setDesktopJSONHeader(w)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "restarting": true})
			go func() {
				time.Sleep(700 * time.Millisecond)
				restartSelf()
			}()
			return
		}
		// 欢迎页左上角的品牌图标。图标是从 build/appicon.png 缩出来的 128px PNG，
		// 和桌面/Android 图标同源；放在这里避免欢迎页用 emoji 冒充品牌。
		if r.URL.Path == "/welcome-icon.png" {
			data, err := assets.ReadFile("welcome-icon.png")
			if err != nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("content-type", "image/png")
			w.Header().Set("cache-control", "public, max-age=86400")
			_, _ = w.Write(data)
			return
		}
		// 只读：setup.html 用它决定要不要显示「← 返回」。
		// 首启时回 /welcome 是合理退路；已有配置的老用户从托盘进来没有欢迎页可回，
		// 显示返回只会把人带到一个不该出现的页面。
		if r.URL.Path == "/desktop/first-run" && r.Method == http.MethodGet {
			setDesktopJSONHeader(w)
			_ = json.NewEncoder(w).Encode(map[string]any{"pending": firstRunPending()})
			return
		}
		if r.URL.Path == "/setup" {
			data, err := assets.ReadFile("setup.html")
			if err != nil {
				http.Error(w, "setup page missing", http.StatusInternalServerError)
				return
			}
			w.Header().Set("content-type", "text/html; charset=utf-8")
			_, _ = w.Write(data)
			return
		}
		if r.URL.Path == "/desktop/invite" {
			code, err := createJoinCode()
			if err != nil {
				setDesktopJSONHeader(w)
				w.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"message": err.Error()}})
				return
			}
			setDesktopJSONHeader(w)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "hub_url": advertisedHubURL()})
			return
		}
		if r.URL.Path == "/desktop/join" && r.Method == http.MethodPost {
			var input struct {
				Code   string `json:"code"`
				HubURL string `json:"hub_url"`
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				http.Error(w, "invalid json", http.StatusBadRequest)
				return
			}
			payload, err := parseJoinCode(input.Code)
			if err == nil && strings.TrimSpace(input.HubURL) != "" {
				if parsed, parseErr := url.Parse(strings.TrimSpace(input.HubURL)); parseErr == nil && parsed.Host != "" {
					payload.HubURL = strings.TrimRight(strings.TrimSpace(input.HubURL), "/")
				} else {
					err = fmt.Errorf("Hub 地址无效")
				}
			}
			if err == nil {
				err = writeNodeConfig(payload)
			}
			if err != nil {
				setDesktopJSONHeader(w)
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"message": err.Error()}})
				return
			}
			setDesktopJSONHeader(w)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "restarting": true})
			go func() {
				time.Sleep(700 * time.Millisecond)
				restartSelf()
			}()
			return
		}
		// 设置页「打开加入工作区」走这里：在 App 内开一个加入窗口，而不是把
		// 用户踢到系统浏览器（浏览器里配对完还得等桌面 App 自己重启）。
		if r.URL.Path == "/desktop/open-setup" && r.Method == http.MethodPost {
			if desktopApp == nil {
				http.Error(w, "wails app not ready", http.StatusServiceUnavailable)
				return
			}
			openSetupWindow(desktopApp)
			setDesktopJSONHeader(w)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
			return
		}
		if r.URL.Path == "/desktop/update-check" {
			setDesktopJSONHeader(w)
			_ = json.NewEncoder(w).Encode(checkForUpdate())
			return
		}
		if r.URL.Path == "/desktop/harness-html-preview" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path == reviewConsentRoute {
			handleReviewConsent(w, r)
			return
		}
		if r.URL.Path == "/desktop/harness-origin" && r.Method == http.MethodGet {
			origin, err := ensureHarnessOrigin(r.URL.Query().Get("device"))
			if err != nil {
				http.Error(w, err.Error(), http.StatusServiceUnavailable)
				return
			}
			setDesktopJSONHeader(w)
			_ = json.NewEncoder(w).Encode(map[string]string{"url": origin})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/desktop/window/") && r.Method == http.MethodPost {
			if mainWindow == nil {
				http.Error(w, "window not ready", http.StatusServiceUnavailable)
				return
			}
			switch strings.TrimPrefix(r.URL.Path, "/desktop/window/") {
			case "minimise":
				mainWindow.Minimise()
			case "toggle-maximise":
				mainWindow.ToggleMaximise()
			case "close":
				mainWindow.Close()
			default:
				http.Error(w, "unknown window action", http.StatusNotFound)
				return
			}
			setDesktopJSONHeader(w)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
			return
		}
		if r.URL.Path == "/desktop/open-window" && r.Method == http.MethodPost {
			var input struct {
				URL   string `json:"url"`
				Title string `json:"title"`
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				http.Error(w, "invalid json", http.StatusBadRequest)
				return
			}
			parsed, err := url.Parse(strings.TrimSpace(input.URL))
			if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
				http.Error(w, "invalid url", http.StatusBadRequest)
				return
			}
			if desktopApp == nil {
				http.Error(w, "wails app not ready", http.StatusServiceUnavailable)
				return
			}
			title := strings.TrimSpace(input.Title)
			if title == "" {
				title = "Meerkat Harness"
			}
			child := desktopApp.Window.NewWithOptions(application.WebviewWindowOptions{
				Title:            title,
				Width:            1280,
				Height:           900,
				URL:              parsed.String(),
				BackgroundColour: application.NewRGB(20, 20, 22),
			})
			child.Show()
			child.Focus()
			setDesktopJSONHeader(w)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
			return
		}
		if r.URL.Path == "/desktop/open-url" && r.Method == http.MethodPost {
			var input struct {
				URL string `json:"url"`
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				http.Error(w, "invalid json", http.StatusBadRequest)
				return
			}
			parsed, err := url.Parse(strings.TrimSpace(input.URL))
			if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
				http.Error(w, "invalid url", http.StatusBadRequest)
				return
			}
			if desktopApp == nil {
				http.Error(w, "wails app not ready", http.StatusServiceUnavailable)
				return
			}
			if err := desktopApp.Browser.OpenURL(parsed.String()); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			setDesktopJSONHeader(w)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
			return
		}
		if r.URL.Path == "/desktop/notifications/permission" {
			status := notificationPermissionStatus(r.Method == http.MethodPost)
			if r.Method == http.MethodPost {
				status = requestNotificationPermission()
			}
			setDesktopJSONHeader(w)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status":    status,
				"supported": notificationService != nil,
			})
			return
		}
		if r.URL.Path == "/desktop/edge-network" {
			setDesktopJSONHeader(w)
			_ = json.NewEncoder(w).Encode(readEdgeNetworkStatus())
			return
		}
		if r.URL.Path == "/desktop/health" {
			setDesktopJSONHeader(w)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name":            "meerkat-desktop",
				"mode":            appCfg.Mode,
				"center_url":      centerURL(),
				"hub_url":         advertisedHubURL(),
				"center":          centerStatus(),
				"herdr":           herdrStatus(),
				"edge":            edgeStatus(),
				"local_device_id": localDeviceID(),
			})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api") || strings.HasPrefix(r.URL.Path, "/harness-proxy/") {
			if appCfg.Mode == "node" {
				// node 模式只能走 Edge 的本地 Hub 代理；未就绪时等待，绝不回退到系统 Tailscale。
				if code, message := edgeNotReady(); message != "" {
					writeDesktopJSON(w, http.StatusServiceUnavailable, map[string]any{
						"error": map[string]string{"code": code, "message": message},
					})
					return
				}
				local, waitErr := waitEdgeLocalAPIURL(r.Context())
				if waitErr != nil {
					writeDesktopJSON(w, http.StatusServiceUnavailable, map[string]any{
						"error": map[string]string{"code": "edge_proxy_unavailable", "message": waitErr.Error()},
					})
					return
				}
				if localTarget, parseErr := url.Parse(local); parseErr == nil {
					newUIProxy(localTarget).ServeHTTP(w, r)
					return
				}
			}
			proxy.ServeHTTP(w, r)
			return
		}
		ensureLocalSession(w, r)
		if r.URL.Path != "/" {
			clean := strings.TrimPrefix(r.URL.Path, "/")
			if _, err := fs.Stat(dist, clean); err != nil {
				r.URL.Path = "/"
			}
		}
		fileServer.ServeHTTP(w, r)
	})
}

func cleanupChildren() {
	cleanupOnce.Do(func() {
		if edgeProcess != nil {
			log.Printf("清理 Edge pid=%d", edgeProcess.Process.Pid)
			_ = edgeParentPipe.Close()
			timer := time.NewTimer(3 * time.Second)
			defer timer.Stop()
			select {
			case <-edgeDone:
			case <-timer.C:
				if err := edgeProcess.Process.Kill(); err == nil || errors.Is(err, os.ErrProcessDone) {
					<-edgeDone
				} else {
					log.Printf("结束 Edge 失败: %v", err)
				}
			}
		}
		if embeddedHub != nil {
			log.Printf("关闭 Go Hub")
			_ = embeddedHub.Close()
			embeddedHub = nil
		}
	})
}

func edgeStatus() string {
	if edgeProcess == nil {
		if bin := bundledEdge(); bin != "" {
			return "configured but not started (" + bin + ")"
		}
		return "not configured"
	}
	select {
	case <-edgeDone:
		if edgeLockMessage != "" {
			return edgeLockMessage
		}
		return "exited"
	default:
		return fmt.Sprintf("running pid=%d", edgeProcess.Process.Pid)
	}
}

func main() {
	lock, err := acquireInstanceLock(configDir())
	if err != nil {
		reportInstanceConflict(err)
		os.Exit(1)
	}
	desktopInstanceLock = lock
	defer func() { _ = desktopInstanceLock.Close() }()

	loadConfig()
	loadDownloadedUpdate()
	// 首次启动（没有 config.json）不启动 Hub 与 Edge，也不进主界面：
	// 先把窗口指向欢迎页，用户选完角色再写配置并重启。
	needsSetup := firstRunPending()
	if !needsSetup {
		startCenterIfNeeded()
		startEdge()
	}
	uiURL, err := startUI()
	if err != nil {
		cleanupChildren()
		log.Fatal(err)
	}
	localUIURL = uiURL
	desktopWindowURL := uiURL
	switch runtime.GOOS {
	case "darwin":
		desktopWindowURL = strings.TrimRight(uiURL, "/") + "/?desktop=1"
	case "windows":
		desktopWindowURL = strings.TrimRight(uiURL, "/") + "/?desktop=1&os=windows"
	}
	if needsSetup {
		desktopWindowURL = strings.TrimRight(uiURL, "/") + "/welcome"
	}
	log.Printf("Meerkat Desktop UI: %s (mode=%s center=%s first_run=%v)", uiURL, appCfg.Mode, centerURL(), needsSetup)

	setupNotificationService()
	services := []application.Service{}
	if notificationService != nil {
		services = append(services, application.NewService(notificationService))
	}
	app := application.New(application.Options{
		Name:        "Meerkat",
		Description: "Meerkat desktop control plane",
		Services:    services,
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		Mac: application.MacOptions{
			ActivationPolicy: application.ActivationPolicyRegular,
			ApplicationShouldTerminateAfterLastWindowClosed: false,
		},
		Windows: application.WindowsOptions{
			// WebView2 默认继承系统代理，访问本机 DSH 的 WebSocket 时可能被代理拦截；
			// Meerkat 的 Hub/DSH 都是本机或 tsnet 地址，禁用系统代理。
			AdditionalBrowserArgs: []string{"--no-proxy-server"},
		},
	})
	desktopApp = app
	go watchEdgeNetworkLogin(app)

	window := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:  "Meerkat",
		Width:  defaultWindowWidth,
		Height: defaultWindowHeight,
		URL:    desktopWindowURL,
		Mac: application.MacWindow{
			InvisibleTitleBarHeight: 40,
			TitleBar:                application.MacTitleBarHiddenInset,
			Backdrop:                application.MacBackdropTranslucent,
		},
		Windows: application.WindowsWindow{
			NonClientRegionSupport: true,
		},
		BackgroundColour: application.NewRGB(20, 20, 22),
	})
	mainWindow = window

	// 窗口在 app.Run() 之前创建，那时 Wails 还没填充屏幕缓存，拿不到可用区。
	// 所以先按默认尺寸把窗口显示出来（保持今天的行为：事件万一没送达也只是
	// 尺寸不变），等 ApplicationStarted 再按实际可用区收缩并居中。
	app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		go fitMainWindowToScreen(app, window)
		if edgeDone != nil {
			go func() {
				<-edgeDone
				if edgeLockMessage != "" {
					app.Dialog.Info().SetTitle("Meerkat Edge 已在运行").SetMessage(edgeLockMessage).Show()
				}
			}()
		}
	})

	tray := app.SystemTray.New()
	tray.SetTooltip("Meerkat · 多设备 Agent 总控")
	if runtime.GOOS == "darwin" {
		tray.SetTemplateIcon(icons.SystrayMacTemplate)
	}
	menu := app.Menu.New()
	menu.Add("打开 Meerkat").OnClick(func(*application.Context) {
		window.Show()
		window.Focus()
	})
	menu.Add("邀请设备").OnClick(func(*application.Context) {
		// 首启还没选角色时没有 Hub，也没有可邀请的东西：直接说清该先做什么，
		// 而不是弹「只有 Hub 模式可以生成邀请码」这种答非所问的错。
		if firstRunPending() {
			app.Dialog.Info().SetTitle("邀请设备").SetMessage("这台机器还没有选择工作区角色。\n\n请先在主窗口里选择「创建新工作区（本机作 Hub）」或「加入现有工作区」，再邀请其他设备。").Show()
			return
		}
		code, err := createJoinCode()
		if err != nil {
			app.Dialog.Info().SetTitle("邀请设备").SetMessage(err.Error()).Show()
			return
		}
		app.Clipboard.SetText(code)
		app.Dialog.Info().SetTitle("邀请设备").SetMessage(fmt.Sprintf("邀请码已复制到剪贴板。\n\nHub: %s\n\n在目标机器上打开 Meerkat「设置 → 加入工作区」，粘贴邀请码并加入。", advertisedHubURL())).Show()
	})
	menu.Add("加入工作区...").OnClick(func(*application.Context) {
		openSetupWindow(app)
	})
	menu.Add("检测 Herdr").OnClick(func(*application.Context) {
		app.Dialog.Info().SetTitle("Herdr 状态").SetMessage(herdrStatus()).Show()
	})
	menu.Add("桌面服务状态").OnClick(func(*application.Context) {
		if firstRunPending() {
			app.Dialog.Info().SetTitle("Meerkat Desktop").SetMessage(fmt.Sprintf("版本: %s\n模式: 尚未选择（首次启动，Hub 与 Edge 都未启动）\n\n在主窗口选择「创建新工作区」或「加入现有工作区」后，这里会显示 Hub 与 Edge 状态。", appVersion)).Show()
			return
		}
		app.Dialog.Info().SetTitle("Meerkat Desktop").SetMessage(fmt.Sprintf("版本: %s\n模式: %s\nCenter: %s (%s)\n\n本地 Edge: %s", appVersion, appCfg.Mode, centerURL(), centerStatus(), edgeStatus())).Show()
	})
	menu.Add("检查更新").OnClick(func(*application.Context) {
		showUpdateCheck(app)
	})
	menu.Add("安装已下载更新").OnClick(func(*application.Context) {
		promptInstallUpdate(app, true)
	})
	menu.AddSeparator()
	menu.Add("退出").OnClick(func(*application.Context) {
		cleanupChildren()
		app.Quit()
	})
	tray.SetMenu(menu)
	go startNotificationWatcher()

	quitSignal := make(chan os.Signal, 1)
	signal.Notify(quitSignal, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-quitSignal
		log.Printf("收到退出信号，清理子进程")
		cleanupChildren()
		time.Sleep(300 * time.Millisecond)
		app.Quit()
	}()

	startUpdateLoop(app)
	err = app.Run()
	cleanupChildren()
	if err != nil {
		log.Fatal(err)
	}
}
