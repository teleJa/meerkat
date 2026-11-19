package harnessweb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// DSH 运行模式；保存在 Edge 状态目录，用于重启后恢复。
const (
	ModeConnect = "connect"
	ModeLaunch  = "launch"
)

// DSH 运行状态；只上报，不持久化。
const (
	StatusStopped  = "stopped"
	StatusStarting = "starting"
	StatusRunning  = "running"
	StatusError    = "error"
)

// 实例所有者。
const (
	OwnerEdge     = "edge"
	OwnerExternal = "external"
)

const (
	defaultProfile    = "web"
	selectionFileName = "harness-profile.json"
	refreshInterval   = 3 * time.Second
)

// State 是上报给 Hub 的 DSH 状态快照。
type State struct {
	Profile   string    `json:"profile"`
	Mode      string    `json:"mode"`
	Status    string    `json:"status"`
	Owner     string    `json:"owner,omitempty"`
	URL       string    `json:"url,omitempty"`
	Error     string    `json:"error,omitempty"`
	Installed bool      `json:"installed"`
	Profiles  []Profile `json:"profiles"`
	UpdatedAt string    `json:"updated_at,omitempty"`
}

// ManagerConfig 是 Manager 的依赖；字段与 harnessweb.Config 对齐。
type ManagerConfig struct {
	Enabled   bool
	Command   string
	Port      int
	PublicURL string
	PatchPath string
	ProxyEnv  map[string]string
	// Home 是 $DSH_HOME；为空时使用 DSHHome()。
	Home string
	// StateDir 是 Edge 状态目录，用来保存 profile/mode 选择。
	StateDir string
	Logger   *log.Logger
}

// Manager 管理本机唯一的活动 DSH 实例：接管外部实例或按 profile 启动一个。
type Manager struct {
	cfg ManagerConfig
	log *log.Logger

	opMu sync.Mutex

	mu         sync.Mutex
	state      State
	svc        *Service
	detached   bool
	saved      bool
	sender     func(State)
	targetPort func(int)

	ctx context.Context
}

type selection struct {
	Profile string `json:"profile"`
	Mode    string `json:"mode"`
}

// NewManager 读取已保存的选择；没有选择时保持旧默认（web + launch）。
func NewManager(cfg ManagerConfig) (*Manager, error) {
	if cfg.Logger == nil {
		cfg.Logger = log.Default()
	}
	if strings.TrimSpace(cfg.Home) == "" {
		cfg.Home = DSHHome()
	}
	if cfg.Port == 0 {
		cfg.Port = 51857
	}
	manager := &Manager{
		cfg: cfg,
		log: cfg.Logger,
		ctx: context.Background(),
		state: State{
			Profile: defaultProfile,
			Mode:    ModeLaunch,
			Status:  StatusStopped,
		},
	}
	manager.refreshProfileList()
	if saved, err := readSelection(cfg.StateDir); err == nil {
		manager.state.Profile = normalizeProfile(saved.Profile)
		manager.state.Mode = normalizeMode(saved.Mode, ModeLaunch)
		manager.saved = true
	}
	return manager, nil
}

// SetSender 设置状态上报回调；传 nil 解除。回调在锁外调用。
func (m *Manager) SetSender(sender func(State)) {
	m.mu.Lock()
	m.sender = sender
	m.mu.Unlock()
	if sender != nil {
		sender(m.Snapshot())
	}
}

// SetTargetPort 设置 tailnet 转发目标端口回调。
func (m *Manager) SetTargetPort(callback func(int)) {
	m.mu.Lock()
	m.targetPort = callback
	m.mu.Unlock()
}

// Snapshot 返回当前状态副本。
func (m *Manager) Snapshot() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.state
	state.Profiles = append([]Profile(nil), m.state.Profiles...)
	return state
}

// Start 恢复上次的选择并开始定时刷新。没有保存过选择时沿用旧行为：启动 web。
func (m *Manager) Start(ctx context.Context) {
	if ctx != nil {
		m.ctx = ctx
	}
	m.opMu.Lock()
	m.refreshProfileList()
	if !m.cfg.Enabled {
		m.setState(func(state *State) {
			state.Status = StatusStopped
			state.Owner = ""
			state.URL = ""
		})
		m.opMu.Unlock()
		go m.loop()
		return
	}
	if !m.saved {
		m.setState(func(state *State) {
			state.Profile = defaultProfile
			state.Mode = ModeLaunch
		})
		_ = m.opLaunch(defaultProfile, false)
	} else if m.state.Mode == ModeConnect {
		_ = m.opConnect(m.state.Profile, false)
	} else {
		_ = m.opLaunch(m.state.Profile, false)
	}
	m.opMu.Unlock()
	go m.loop()
}

func (m *Manager) loop() {
	ticker := time.NewTicker(refreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			m.Refresh()
		}
	}
}

// Refresh 重读发现文件与自己的子进程，更新状态并上报变化。
func (m *Manager) Refresh() {
	m.refreshProfileList()

	m.mu.Lock()
	profile := m.state.Profile
	mode := m.state.Mode
	status := m.state.Status
	owner := m.state.Owner
	currentURL := m.state.URL
	svc := m.svc
	detached := m.detached
	m.mu.Unlock()

	if svc != nil && !svc.Alive() {
		m.mu.Lock()
		if m.svc == svc {
			m.svc = nil
		}
		m.mu.Unlock()
		svc.removeDiscovery()
		m.setState(func(state *State) {
			state.Status = StatusStopped
			state.Owner = ""
			state.URL = ""
			state.Error = "DSH 进程已退出"
		})
		return
	}

	if mode != ModeConnect || detached {
		return
	}
	discovery, err := ReadDiscovery(m.cfg.Home, profile)
	if err == nil && discovery.Alive() {
		reported := m.reportURL(discovery.URL)
		if status != StatusRunning || owner != OwnerExternal || currentURL != reported {
			m.applyExternal(profile, discovery)
		}
		return
	}
	if status == StatusRunning && owner == OwnerExternal {
		m.setState(func(state *State) {
			state.Status = StatusStopped
			state.Owner = ""
			state.URL = ""
			state.Error = "外部实例已退出"
		})
	}
}

// Apply 执行 Hub 下发的动作；action 取值见 HarnessProfileCommand。
func (m *Manager) Apply(action, profile, mode string) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	switch strings.TrimSpace(action) {
	case "select":
		if normalizeMode(mode, m.Snapshot().Mode) == ModeConnect {
			return m.opConnect(profile, true)
		}
		return m.opLaunch(profile, true)
	case "connect":
		return m.opConnect(profile, true)
	case "launch":
		return m.opLaunch(profile, true)
	case "stop":
		return m.opStop()
	case "create":
		return m.opCreate(profile)
	case "refresh":
		m.Refresh()
		return nil
	default:
		return fmt.Errorf("未知的 DSH 动作 %q", action)
	}
}

// CreateProfile 用 DSH 模板创建 profile；不改变当前选择。
func (m *Manager) CreateProfile(ctx context.Context, name string) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	return m.opCreate(name)
}

// Restart 在 DSH 安装/更新后重启当前活动实例。
func (m *Manager) Restart(ctx context.Context, command string) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	if strings.TrimSpace(command) != "" {
		m.mu.Lock()
		m.cfg.Command = command
		m.mu.Unlock()
	}
	m.stopOwned()
	state := m.Snapshot()
	if state.Mode == ModeConnect {
		return m.opConnect(state.Profile, false)
	}
	return m.opLaunch(state.Profile, false)
}

// Close 停止 Edge 自己启动的实例；外部实例不动。
func (m *Manager) Close() {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	m.stopOwned()
}

func (m *Manager) opConnect(profile string, persist bool) error {
	profile = normalizeProfile(profile)
	m.setState(func(state *State) {
		state.Profile = profile
		state.Mode = ModeConnect
		state.Error = ""
	})
	m.saved = true
	m.mu.Lock()
	m.detached = false
	m.mu.Unlock()
	if persist {
		m.persist(profile, ModeConnect)
	}
	m.stopOwned()
	discovery, err := ReadDiscovery(m.cfg.Home, profile)
	if err != nil {
		m.failConnect(profile, fmt.Errorf("没有找到 profile %q 的运行中实例: %w", profile, err))
		return err
	}
	if !discovery.Alive() {
		err := fmt.Errorf("profile %q 的实例已退出", profile)
		m.failConnect(profile, err)
		return err
	}
	m.applyExternal(profile, discovery)
	return nil
}

func (m *Manager) opLaunch(profile string, persist bool) error {
	profile = normalizeProfile(profile)
	m.setState(func(state *State) {
		state.Profile = profile
		state.Mode = ModeLaunch
		state.Error = ""
	})
	m.saved = true
	m.mu.Lock()
	m.detached = false
	m.mu.Unlock()
	if persist {
		m.persist(profile, ModeLaunch)
	}
	m.mu.Lock()
	current := m.svc
	m.mu.Unlock()
	if current != nil && current.Alive() && current.Profile() == profile {
		m.setState(func(state *State) {
			state.Status = StatusRunning
			state.Owner = OwnerEdge
			state.URL = m.reportURL(current.URL())
		})
		m.setTargetPort(current.Port())
		return nil
	}
	// 已有同 profile 的有效实例时不再起第二份，避免抢 DSH 状态锁。
	if discovery, err := ReadDiscovery(m.cfg.Home, profile); err == nil && discovery.Alive() {
		m.stopOwned()
		m.applyExternal(profile, discovery)
		return nil
	}
	m.stopOwned()
	m.setState(func(state *State) {
		state.Status = StatusStarting
		state.Owner = ""
		state.URL = ""
	})
	service, err := Start(m.ctx, Config{
		Enabled:        true,
		Command:        m.cfg.Command,
		Port:           m.cfg.Port,
		PublicURL:      m.cfg.PublicURL,
		PatchPath:      m.cfg.PatchPath,
		ProxyEnv:       m.cfg.ProxyEnv,
		Profile:        profile,
		DSHHome:        m.cfg.Home,
		WriteDiscovery: true,
		// 只有“没有保存过选择、按旧默认启动 web”时保留旧的端口清理行为；
		// 用户显式选择的 profile 不杀未知进程，避免误杀外部实例。
		CleanupPort: !m.saved && profile == defaultProfile,
	}, m.log)
	if err != nil {
		m.setError(err)
		return err
	}
	m.mu.Lock()
	m.svc = service
	m.mu.Unlock()
	// 实际端口可能后移过（撞上系统保留段），tsnet 转发必须跟实际端口走，不能写死配置值。
	m.setTargetPort(service.Port())
	m.setState(func(state *State) {
		state.Status = StatusRunning
		state.Owner = OwnerEdge
		state.URL = m.reportURL(service.URL())
		state.Error = ""
	})
	return nil
}

func (m *Manager) opStop() error {
	m.stopOwned()
	hadExternal := false
	m.mu.Lock()
	if m.state.Owner == OwnerExternal {
		hadExternal = true
	}
	m.mu.Unlock()
	if hadExternal {
		// 外部实例只断开，不发送任何信号；detached 阻止心跳自动重连。
		m.mu.Lock()
		m.detached = true
		m.mu.Unlock()
	}
	m.setState(func(state *State) {
		state.Status = StatusStopped
		state.Owner = ""
		state.URL = ""
		state.Error = ""
	})
	return nil
}

func (m *Manager) opCreate(name string) error {
	name = strings.TrimSpace(name)
	if err := CreateProfile(m.ctx, m.cfg.Command, m.cfg.Home, name); err != nil {
		return err
	}
	m.refreshProfileList()
	return nil
}

// stopOwned 只停止 Edge 启动的实例，并删除对应发现文件。
func (m *Manager) stopOwned() {
	m.mu.Lock()
	service := m.svc
	m.svc = nil
	m.mu.Unlock()
	if service != nil {
		service.Close()
	}
}

func (m *Manager) applyExternal(profile string, discovery Discovery) {
	m.setTargetPort(discovery.Port)
	m.setState(func(state *State) {
		state.Profile = profile
		state.Status = StatusRunning
		state.Owner = OwnerExternal
		state.URL = m.reportURL(discovery.URL)
		state.Error = ""
	})
}

func (m *Manager) failConnect(profile string, err error) {
	m.setState(func(state *State) {
		state.Profile = profile
		state.Status = StatusStopped
		state.Owner = ""
		state.URL = ""
		state.Error = err.Error()
	})
	m.log.Printf("连接 DSH profile %s 失败：%v", profile, err)
}

func (m *Manager) setError(err error) {
	m.setState(func(state *State) {
		state.Status = StatusError
		state.Owner = ""
		state.URL = ""
		state.Error = err.Error()
	})
	m.log.Printf("DSH 启动失败：%v", err)
}

func (m *Manager) setTargetPort(port int) {
	m.mu.Lock()
	callback := m.targetPort
	m.mu.Unlock()
	if callback != nil && port > 0 {
		callback(port)
	}
}

// setState 修改状态；有实质变化时在锁外调用 sender（updated_at 不参与比较）。
func (m *Manager) setState(mutate func(*State)) {
	m.mu.Lock()
	previous := m.state
	previous.UpdatedAt = ""
	previousJSON, _ := json.Marshal(previous)
	mutate(&m.state)
	m.state.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	state := m.state
	next := state
	next.UpdatedAt = ""
	nextJSON, _ := json.Marshal(next)
	changed := string(nextJSON) != string(previousJSON)
	sender := m.sender
	m.mu.Unlock()
	if changed && sender != nil {
		sender(state)
	}
}

func (m *Manager) refreshProfileList() {
	profiles := ListProfiles(m.cfg.Home)
	m.mu.Lock()
	command := m.cfg.Command
	m.mu.Unlock()
	installed := commandAvailable(command)
	m.setState(func(state *State) {
		state.Profiles = profiles
		state.Installed = installed
	})
}

func (m *Manager) reportURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.TrimSpace(m.cfg.PublicURL) == "" {
		return raw
	}
	base, err := url.Parse(m.cfg.PublicURL)
	if err != nil || base.Host == "" {
		return raw
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	switch strings.ToLower(parsed.Hostname()) {
	case "127.0.0.1", "localhost", "::1":
		parsed.Scheme = base.Scheme
		parsed.Host = base.Host
	}
	return parsed.String()
}

func normalizeProfile(profile string) string {
	profile = strings.TrimSpace(profile)
	if profile == "" {
		return defaultProfile
	}
	return profile
}

func normalizeMode(mode, fallback string) string {
	switch strings.TrimSpace(mode) {
	case ModeConnect:
		return ModeConnect
	case ModeLaunch:
		return ModeLaunch
	}
	if fallback == ModeConnect || fallback == ModeLaunch {
		return fallback
	}
	return ""
}

func commandAvailable(command string) bool {
	_, err := LocateDSH(command)
	return err == nil
}

func selectionPath(stateDir string) string {
	if strings.TrimSpace(stateDir) == "" {
		return ""
	}
	return filepath.Join(stateDir, selectionFileName)
}

func readSelection(stateDir string) (selection, error) {
	path := selectionPath(stateDir)
	if path == "" {
		return selection{}, errors.New("未配置状态目录")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return selection{}, err
	}
	var saved selection
	if err := json.Unmarshal(data, &saved); err != nil {
		return selection{}, err
	}
	if saved.Profile == "" || (saved.Mode != ModeConnect && saved.Mode != ModeLaunch) {
		return selection{}, errors.New("选择文件内容不完整")
	}
	return saved, nil
}

func (m *Manager) persist(profile, mode string) {
	path := selectionPath(m.cfg.StateDir)
	if path == "" {
		return
	}
	data, err := json.MarshalIndent(selection{Profile: profile, Mode: mode}, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		m.log.Printf("保存 DSH 选择失败：%v", err)
		return
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".harness-profile-*.tmp")
	if err != nil {
		m.log.Printf("保存 DSH 选择失败：%v", err)
		return
	}
	tempName := temp.Name()
	cleanup := func() { _ = os.Remove(tempName) }
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		cleanup()
		m.log.Printf("保存 DSH 选择失败：%v", err)
		return
	}
	if _, err := temp.Write(append(data, '\n')); err != nil {
		_ = temp.Close()
		cleanup()
		m.log.Printf("保存 DSH 选择失败：%v", err)
		return
	}
	if err := temp.Close(); err != nil {
		cleanup()
		m.log.Printf("保存 DSH 选择失败：%v", err)
		return
	}
	if err := os.Rename(tempName, path); err != nil {
		cleanup()
		m.log.Printf("保存 DSH 选择失败：%v", err)
	}
}
