package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"

	"meerkat/edge/internal/agent"
	"meerkat/edge/internal/buildinfo"
	"meerkat/edge/internal/center"
	"meerkat/edge/internal/claude"
	"meerkat/edge/internal/console"
	"meerkat/edge/internal/dshinstall"
	"meerkat/edge/internal/harnessweb"
	"meerkat/edge/internal/herdr"
	"meerkat/edge/internal/identity"
	"meerkat/edge/internal/proxyenv"
	"meerkat/edge/internal/tsnetedge"
	"meerkat/edge/internal/wsclient"
)

// cmdRun 处理 run 子命令：连接中心、上报心跳/指标，并执行 P3 托管指令。
func cmdRun(args []string, logger *log.Logger) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	stateDir := fs.String("state-dir", "", "身份目录，默认按平台")
	metricsInterval := fs.Duration("metrics-interval", 0, "指标间隔，默认使用中心下发的值（5s）")
	heartbeatInterval := fs.Duration("heartbeat-interval", 0, "心跳间隔，默认使用中心下发的值（10s）")
	allowRoot := fs.String("allow-root", "", "Claude 任务允许的根目录（默认用户 home，可被 config.json allow_root 覆盖）")
	claudePath := fs.String("claude-path", "", "harness 可执行文件路径，默认 claude")
	harnessCmd := fs.String("harness-cmd", "", "同 --claude-path，用于替换 harness（本机联调 stub）")
	harnessArgs := &stringListFlag{}
	fs.Var(harnessArgs, "harness-arg", "追加传给 harness 的参数，可重复")
	magpieURL := fs.String("magpie-url", "", "默认 magpie 网关，例如 http://127.0.0.1:3425（默认内置）")
	harnessModel := fs.String("harness-model", "", "默认 Claude 模型（magpie 路由组，默认 group/coding）")
	noMagpie := fs.Bool("no-magpie", false, "关闭默认 magpie 注入，使用用户自己的 Claude 配置")
	noConsole := fs.Bool("no-console", false, "隐藏子进程控制台窗口（Windows；由 Meerkat App 无窗口拉起时使用）")
	runtimeName := fs.String("runtime", "", "托管运行时：headless（默认）或 herdr")
	herdrBin := fs.String("herdr-bin", "", "herdr 可执行文件，默认 herdr")
	herdrArgs := &stringListFlag{}
	fs.Var(herdrArgs, "herdr-arg", "追加给 Herdr agent 的参数，可重复")
	if ok, code := parseFlags(fs, args); !ok {
		return code
	}
	if *metricsInterval < 0 || *heartbeatInterval < 0 {
		logger.Printf("参数错误：间隔不能为负数")
		return 2
	}
	if *claudePath != "" && *harnessCmd != "" && *claudePath != *harnessCmd {
		logger.Printf("参数错误：--claude-path 与 --harness-cmd 不能同时指定不同的值")
		return 2
	}

	// 手工在终端里运行时保留可见控制台；App 以无窗口方式拉起时隐藏所有子进程窗口。
	if console.EnableFromEnvOrFlag(*noConsole) {
		logger.Printf("已启用无窗口模式：Edge 拉起的子进程不弹出控制台（--no-console 或 MEERKAT_NO_CONSOLE）")
	}
	dir, err := resolveStateDir(*stateDir)
	if err != nil {
		logger.Printf("参数错误：%v", err)
		return 2
	}
	lock, err := acquireRunLock(dir)
	if err != nil {
		logger.Printf("无法启动 Edge：%v", err)
		if errors.Is(err, errRunLockHeld) {
			return runLockConflictExitCode
		}
		return 1
	}
	defer lock.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if os.Getenv("MEERKAT_EDGE_PARENT_STDIN") == "1" {
		// The desktop owns the write end. Closing it, including on a crash,
		// wakes this read without relying on a PID that the OS could reuse.
		go func() {
			var unused [1]byte
			_, _ = os.Stdin.Read(unused[:])
			logger.Print("桌面进程连接已关闭，正在退出 Edge")
			stop()
		}()
	}
	fileCfg, err := agent.LoadFileConfig(dir)
	if err != nil {
		logger.Printf("读取托管执行配置失败：%v", err)
		return 1
	}
	// 身份文件缺失表示本机还未配对：先入网，再用 config.json 里的配对令牌注册。
	// 身份文件存在但无效属于配置错误，直接退出，不尝试重新配对覆盖。
	id, identityErr := identity.Load(dir)
	if identityErr != nil && !errors.Is(identityErr, identity.ErrNotPaired) {
		logger.Printf("%v", identityErr)
		return 1
	}
	needsPairing := identityErr != nil
	if needsPairing {
		writePairingStatus(dir, pairingStatus{
			Phase:  pairPhaseWaitingNetwork,
			HubURL: strings.TrimSpace(fileCfg.TSNetHubURL),
		}, logger)
	}
	dshProxyEnv := map[string]string{}
	if fileCfg.DSHWebEnabled {
		dshProxyEnv = proxyenv.Detect(ctx, fileCfg.TSNetHubURL)
		if len(dshProxyEnv) > 0 {
			logger.Printf("DSH 已从登录 shell 读取代理环境（%d 项）", len(dshProxyEnv))
		}
	}
	root, err := agent.ResolveAllowRoot(*allowRoot, fileCfg.AllowRoot)
	if err != nil {
		logger.Printf("参数错误：%v", err)
		return 2
	}
	tsnetCtx, tsnetCancel := context.WithCancel(ctx)
	defer tsnetCancel()
	// 本机 Hub API 代理端口可配置；默认 51900 是老行为，绑不上会自动后移。
	localAPIAddr := strings.TrimSpace(fileCfg.LocalAPIAddr)
	if localAPIAddr == "" {
		localAPIAddr = "127.0.0.1:51900"
	}
	var tsnetSvc *tsnetedge.Service
	if fileCfg.TSNetEnabled {
		var tsErr error
		tsnetSvc, tsErr = tsnetedge.Start(tsnetCtx, tsnetedge.Config{
			Enabled:      true,
			Hostname:     fileCfg.TSNetHostname,
			HubURL:       fileCfg.TSNetHubURL,
			AuthKey:      fileCfg.TSNetAuthKey,
			StateDir:     filepath.Join(dir, "tsnet", "edge"),
			StatusPath:   filepath.Join(dir, "tsnet-status.json"),
			LocalAPIAddr: localAPIAddr,
		}, logger)
		if tsErr != nil {
			logger.Printf("启动 Edge tsnet 失败：%v", tsErr)
		} else if tsnetSvc != nil {
			defer tsnetSvc.Close()
			// 首次入网要在浏览器里完成一次 Tailscale 授权，等待时间放宽；已配对设备维持原上限。
			waitTimeout := 3 * time.Minute
			if needsPairing {
				waitTimeout = firstJoinWaitTimeout
			}
			waitCtx, waitCancel := context.WithTimeout(tsnetCtx, waitTimeout)
			waitErr := tsnetSvc.Wait(waitCtx)
			waitCancel()
			if waitErr != nil {
				logger.Printf("等待 Edge tsnet 登录失败：%v", waitErr)
			}
		}
	}
	if needsPairing {
		paired, pairErr := autoPair(ctx, dir, fileCfg, tsnetSvc, logger)
		if pairErr != nil {
			logger.Printf("自动配对失败：%v", pairErr)
			return 1
		}
		id = paired
		logger.Printf("自动配对完成：%s", id.Describe())
	}
	harnessPublicURL := fileCfg.DSHWebPublicURL
	dshPort := fileCfg.DSHWebPort
	if dshPort == 0 {
		dshPort = 51857
	}
	if tsnetSvc != nil {
		if tsnetSvc.IP() != "" {
			harnessPublicURL = fmt.Sprintf("http://%s:%d/", tsnetSvc.IP(), tsnetedge.DSHListenPort)
		}
		// 先建立 tailnet 监听，DSH 稍后启动；连接建立后由 TCP 转发等待本地端口。
		go func() {
			if listenErr := tsnetSvc.ListenDSH(tsnetCtx, dshPort); listenErr != nil {
				logger.Printf("Edge tsnet 暴露 DSH 失败：%v", listenErr)
			}
		}()
	}
	permissionPatch, patchErr := harnessweb.EnsurePermissionPatch(dir)
	if patchErr != nil {
		logger.Printf("写入 DSH 权限补丁失败：%v", patchErr)
	}
	dshCommand := fileCfg.DSHWebCommand
	owner := harnessweb.NewTrustOwner(id.DeviceID, id.DeviceSecret)
	publicSource := func() string {
		if tsnetSvc != nil {
			if ip := tsnetSvc.IP(); net.ParseIP(ip) != nil {
				return "http://" + net.JoinHostPort(ip, strconv.Itoa(tsnetedge.DSHListenPort)) + "/"
			}
		}
		if current, err := agent.LoadFileConfig(dir); err == nil {
			return current.DSHWebPublicURL
		}
		return fileCfg.DSHWebPublicURL
	}
	harnessManager, managerErr := harnessweb.NewManager(harnessweb.ManagerConfig{
		Enabled:         fileCfg.DSHWebEnabled,
		Command:         dshCommand,
		Port:            dshPort,
		PublicURL:       harnessPublicURL,
		TrustOwner:      &owner,
		PublicURLSource: publicSource,
		PatchPath:       permissionPatch,
		ProxyEnv:        dshProxyEnv,
		Home:            harnessweb.DSHHome(),
		StateDir:        dir,
		Logger:          logger,
	})
	if managerErr != nil {
		logger.Printf("初始化 DSH 管理器失败：%v", managerErr)
		return 1
	}
	if tsnetSvc != nil {
		harnessManager.SetTargetPort(tsnetSvc.SetDSHLocalPort)
	}
	harnessCtx, harnessCancel := context.WithCancel(ctx)
	defer harnessCancel()
	var startHarness sync.Once
	defer harnessManager.Close()
	installDSH := func(ctx context.Context) (string, error) {
		result, installErr := dshinstall.Ensure(ctx, logger)
		if installErr != nil {
			return "", installErr
		}
		if restartErr := harnessManager.Restart(ctx, result.Command); restartErr != nil {
			return "", restartErr
		}
		state := harnessManager.Snapshot()
		if state.URL == "" {
			return "", fmt.Errorf("DSH 已安装，但 dsh_web_enabled=false，未启动 Harness")
		}
		return state.URL, nil
	}
	harnessPath, prefixArgs, extraArgs := resolveHarness(*claudePath, *harnessCmd, fileCfg, []string(*harnessArgs))
	magpieEndpoint, magpieModel := resolveMagpie(*magpieURL, *harnessModel, fileCfg)
	extraArgs, harnessEnv, err := prepareHarness(harnessPath, prefixArgs, extraArgs, dir, magpieEndpoint, magpieModel, *noMagpie, logger)
	if err != nil {
		logger.Printf("准备 magpie 默认配置失败：%v", err)
		return 1
	}
	runtime := strings.TrimSpace(*runtimeName)
	if runtime == "" {
		runtime = strings.TrimSpace(fileCfg.Runtime)
	}
	if runtime == "" {
		runtime = "headless"
	}
	if runtime != "headless" && runtime != "herdr" {
		logger.Printf("参数错误：--runtime 只支持 headless 或 herdr")
		return 2
	}
	// 标准动作：所有 Edge 托管的 Claude 都以跳过权限确认的方式执行，避免 headless 任务卡在无审批 UI 的提示上。
	extraArgs = ensureHarnessArg(extraArgs, "--dangerously-skip-permissions")
	var runner claude.Runner
	if runtime == "herdr" {
		bin := strings.TrimSpace(*herdrBin)
		if bin == "" {
			bin = strings.TrimSpace(fileCfg.HerdrBin)
		}
		if bin == "" {
			bin = "herdr"
		}
		args := append([]string{}, extraArgs...)
		args = append(args, []string(*herdrArgs)...)
		runner = herdr.NewRunner(bin, args, logger)
		logger.Printf("托管运行时：Herdr（%s），agent kind=claude 参数=%v", bin, args)
	}

	manager, err := agent.New(agent.Options{
		StateDir:          dir,
		DSHInstall:        installDSH,
		AllowRoot:         root,
		HarnessPath:       harnessPath,
		HarnessPrefixArgs: prefixArgs,
		HarnessExtraArgs:  extraArgs,
		HarnessEnv:        harnessEnv,
		Runner:            runner,
		Logger:            logger,
	})
	if err != nil {
		logger.Printf("初始化托管执行失败：%v", err)
		return 1
	}
	defer manager.Close()

	if tsnetSvc != nil && strings.TrimSpace(fileCfg.TSNetHubURL) != "" {
		id.CenterURL = strings.TrimSpace(fileCfg.TSNetHubURL)
	}
	var wsDialer *websocket.Dialer
	if tsnetSvc != nil {
		wsDialer = &websocket.Dialer{NetDialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return tsnetSvc.Dial(ctx, network, address)
		}}
	}
	client, err := wsclient.New(wsclient.Config{
		Identity: *id,
		OnAuthenticated: func() {
			startHarness.Do(func() {
				harnessManager.Start(harnessCtx)
				if tsnetSvc != nil {
					tsnetSvc.OnIPChange(func(string) { harnessManager.Refresh() })
				}
			})
		},
		Dialer:            wsDialer,
		HeartbeatInterval: *heartbeatInterval,
		MetricsInterval:   *metricsInterval,
		Logger:            logger,
		Manager:           manager,
		Harness:           harnessManager,
	})
	if err != nil {
		logger.Printf("初始化 WebSocket 客户端失败：%v", err)
		return 1
	}

	logger.Printf("Meerkat Edge %s 启动（%s）", buildinfo.Version, id.Describe())
	logger.Printf("托管执行：allow_root=%s harness=%s 前缀参数=%d 追加参数=%d",
		manager.AllowRoot(), harnessPath, len(prefixArgs), len(extraArgs))
	if err := client.Run(ctx); err != nil {
		var fatal *wsclient.FatalError
		if errors.As(err, &fatal) && fatal.DeviceRevoked {
			harnessCancel()
			harnessManager.Close()
			if cleanupErr := harnessweb.UpdateTrust(harnessweb.DSHHome(), dir, &owner, "", ""); cleanupErr != nil {
				logger.Printf("清理设备信任失败：%v", cleanupErr)
			}
		}
		logger.Printf("运行失败：%v", err)
		return 1
	}
	logger.Printf("已退出")
	return 0
}

// ensureHarnessArg 保证标准动作参数存在，避免重复追加。
func ensureHarnessArg(args []string, target string) []string {
	for _, arg := range args {
		if arg == target {
			return args
		}
	}
	return append(args, target)
}

// resolveHarness 解析 harness 可执行文件与参数：
// --harness-cmd / --claude-path > config.json harness_command > claude；
// 前缀参数来自 harness_command 数组，追加参数来自 harness_args 与 --harness-arg。
func resolveHarness(claudePath, harnessCmd string, cfg agent.FileConfig, flagArgs []string) (string, []string, []string) {
	flagExec := strings.TrimSpace(harnessCmd)
	if flagExec == "" {
		flagExec = strings.TrimSpace(claudePath)
	}
	path := flagExec
	var prefix []string
	if path == "" {
		// 只有使用 config.json 的 harness_command 时才带上它的前缀参数；
		// 命令行显式指定可执行文件时，旧配置的前缀参数不再适用。
		path = strings.TrimSpace(cfg.HarnessCommand.Executable)
		prefix = append([]string{}, cfg.HarnessCommand.PrefixArgs...)
	}
	if path == "" {
		path = claude.DefaultCommand
	}
	extra := make([]string, 0, len(cfg.HarnessArgs)+len(flagArgs))
	extra = append(extra, cfg.HarnessArgs...)
	extra = append(extra, flagArgs...)
	return path, prefix, extra
}

// stringListFlag 是可重复的字符串参数。
type stringListFlag []string

func (f *stringListFlag) String() string { return strings.Join(*f, " ") }

func (f *stringListFlag) Set(value string) error {
	*f = append(*f, value)
	return nil
}

// resolveMagpie 解析默认 magpie 网关与模型：flag > config.json > 内置默认。
func resolveMagpie(urlFlag, modelFlag string, cfg agent.FileConfig) (string, string) {
	url := strings.TrimSpace(urlFlag)
	if url == "" {
		url = strings.TrimSpace(cfg.MagpieURL)
	}
	if url == "" {
		url = agent.DefaultMagpieURL
	}
	model := strings.TrimSpace(modelFlag)
	if model == "" {
		model = strings.TrimSpace(cfg.HarnessModel)
	}
	if model == "" {
		model = agent.DefaultHarnessModel
	}
	return url, model
}

func hasArg(args []string, name string) bool {
	for _, arg := range args {
		if arg == name || strings.HasPrefix(arg, name+"=") {
			return true
		}
	}
	return false
}

// prepareHarness 为默认 claude harness 注入 magpie 配置：
// 生成 --settings（ANTHROPIC_BASE_URL 指向 magpie）并追加 --model。
// 自定义 harness（stub）或用户显式传 --settings 时不注入；--no-magpie 可关闭。
func prepareHarness(
	harnessPath string,
	prefixArgs, extraArgs []string,
	stateDir, magpieURL, model string,
	disabled bool,
	logger *log.Logger,
) ([]string, map[string]string, error) {
	env := map[string]string{}
	if disabled || harnessPath != claude.DefaultCommand {
		return extraArgs, env, nil
	}
	if hasArg(prefixArgs, "--settings") || hasArg(extraArgs, "--settings") {
		logger.Printf("检测到自定义 --settings，跳过 magpie 默认注入")
		return extraArgs, env, nil
	}
	settingsPath, err := agent.WriteMagpieSettings(stateDir, magpieURL, model)
	if err != nil {
		return extraArgs, env, err
	}
	extraArgs = append(extraArgs, "--settings", settingsPath)
	if !hasArg(prefixArgs, "--model") && !hasArg(extraArgs, "--model") {
		extraArgs = append(extraArgs, "--model", model)
	}
	env["ANTHROPIC_BASE_URL"] = magpieURL
	logger.Printf("默认 harness 使用 magpie：%s model=%s settings=%s", magpieURL, model, settingsPath)
	return extraArgs, env, nil
}

// firstJoinWaitTimeout 是首次加入工作区时等待内嵌网络上线的时间上限。
// 首次入网需要用户在浏览器完成一次 Tailscale 授权，因此比已配对设备宽松。
const firstJoinWaitTimeout = 10 * time.Minute

// autoPair 在首次运行时用 config.json 的 pairing_token 向 Hub 注册。
// 启用 tsnet 时必须已经入网（Wait 到 Running），否则注册请求根本到不了 Hub；
// 未启用 tsnet（bundled 模式）时按普通 HTTP 访问 tsnet_hub_url 指向的本地 Hub。
// 配对失败会写入 pairing-status.json，App 据此展示失败原因。
func autoPair(ctx context.Context, dir string, cfg agent.FileConfig, tsnetSvc *tsnetedge.Service, logger *log.Logger) (*identity.Identity, error) {
	hubURL := strings.TrimSpace(cfg.TSNetHubURL)
	token := strings.TrimSpace(cfg.PairingToken)
	if hubURL == "" || token == "" {
		msg := "本机尚未配对：config.json 缺少 pairing_token 或 tsnet_hub_url，请在 Meerkat App「加入工作区」重新加入"
		writePairingStatus(dir, pairingStatus{Phase: pairPhaseError, Error: msg, HubURL: hubURL}, logger)
		return nil, errors.New(msg)
	}
	var dial func(ctx context.Context, network, address string) (net.Conn, error)
	if cfg.TSNetEnabled {
		if tsnetSvc == nil || tsnetSvc.IP() == "" {
			msg := "内嵌网络尚未就绪，无法配对：请先在 Meerkat App 完成 Tailscale 登录"
			writePairingStatus(dir, pairingStatus{Phase: pairPhaseError, Error: msg, HubURL: hubURL}, logger)
			return nil, errors.New(msg)
		}
		dial = tsnetSvc.Dial
	}
	writePairingStatus(dir, pairingStatus{Phase: pairPhasePairing, HubURL: hubURL}, logger)
	logger.Printf("开始自动配对：%s（首次入网需在浏览器完成一次 Tailscale 授权）", hubURL)
	var paired *identity.Identity
	var err error
	for attempt := 1; attempt <= maxPairAttempts; attempt++ {
		pairCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		paired, err = pairDevice(pairCtx, pairRequest{
			StateDir: dir,
			HubURL:   hubURL,
			Token:    token,
			Dial:     dial,
			Logger:   logger,
		})
		cancel()
		if err == nil {
			break
		}
		// 令牌无效/协议不匹配等 4xx 是确定失败，重试只会浪费一次性令牌。
		if attempt == maxPairAttempts || !retryablePairError(err) {
			break
		}
		logger.Printf("自动配对失败（第 %d/%d 次）：%v；3 秒后重试", attempt, maxPairAttempts, err)
		select {
		case <-ctx.Done():
			err = ctx.Err()
			attempt = maxPairAttempts
		case <-time.After(3 * time.Second):
		}
	}
	if err != nil {
		writePairingStatus(dir, pairingStatus{Phase: pairPhaseError, Error: err.Error(), HubURL: hubURL}, logger)
		return nil, err
	}
	writePairingStatus(dir, pairingStatus{Phase: pairPhasePaired, HubURL: hubURL, DeviceID: paired.DeviceID}, logger)
	return paired, nil
}

// maxPairAttempts 是自动配对的最大尝试次数，用于跨过 Hub 尚未就绪等瞬时失败。
const maxPairAttempts = 3

// retryablePairError 判断配对错误是否值得重试：
// 4xx（令牌无效、协议不匹配、设备冲突等）是确定失败；网络错误与 5xx 可能是瞬时的。
func retryablePairError(err error) bool {
	var apiErr *center.APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode >= http.StatusInternalServerError || apiErr.StatusCode == http.StatusTooManyRequests
	}
	return true
}
