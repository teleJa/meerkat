// Package harnessweb 启动本机 DSH Web 并解析它打印的访问 URL。
package harnessweb

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"meerkat/edge/internal/console"
	"meerkat/edge/internal/localport"
)

const startTimeout = 90 * time.Second

// Config 来自 edge-node 的 config.json。
type Config struct {
	Enabled   bool
	Command   string
	Port      int
	PublicURL string
	PatchPath string
	// ProxyEnv 只注入 DSH 子进程，不修改 Edge 自身的网络。
	ProxyEnv map[string]string
	// Profile 是要启动的 DSH profile；为空时保持旧行为（web）。
	Profile string
	// DSHHome 是发现文件所在的 $DSH_HOME，WriteDiscovery 开启时使用。
	DSHHome string
	// WriteDiscovery 为 true 时，启动成功后写入 $DSH_HOME/runtime/web-<profile>.json。
	WriteDiscovery bool
	// CleanupPort 为 true 时沿用旧行为，启动前清理端口上的残留 DSH 进程。
	CleanupPort bool
}

// Service 持有一个运行中的 dsh web 子进程。
type Service struct {
	cmd     *exec.Cmd
	cancel  context.CancelFunc
	url     string
	port    int
	log     *log.Logger
	profile string
	home    string
	done    chan struct{}
}

// Port 返回 dsh web 实际监听的本机端口（可能与请求的端口不同：撞上系统保留段时会自动后移）。
func (s *Service) Port() int {
	if s == nil {
		return 0
	}
	return s.port
}

// URL 返回带认证 token 的公开访问地址。
func (s *Service) URL() string {
	if s == nil {
		return ""
	}
	return s.url
}

// PID 返回 dsh 进程号。
func (s *Service) PID() int {
	if s == nil || s.cmd == nil || s.cmd.Process == nil {
		return 0
	}
	return s.cmd.Process.Pid
}

// Profile 返回该实例使用的 profile。
func (s *Service) Profile() string {
	if s == nil {
		return ""
	}
	return s.profile
}

// Alive 表示 dsh 进程还没有退出。
func (s *Service) Alive() bool {
	if s == nil || s.done == nil {
		return false
	}
	select {
	case <-s.done:
		return false
	default:
		return true
	}
}

// Close 结束 dsh web 子进程。
func (s *Service) Close() {
	if s == nil || s.cancel == nil {
		return
	}
	s.cancel()
	if s.cmd != nil && s.cmd.Process != nil {
		pid := s.cmd.Process.Pid
		_ = s.cmd.Process.Kill()
		if runtime.GOOS == "windows" {
			// cmd.exe 下面是 node 子进程，需杀进程树，避免下次启动端口被占。
			killCmd := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid))
			console.Apply(killCmd)
			_ = killCmd.Run()
		}
	}
	s.removeDiscovery()
}

// removeDiscovery 只删除属于本进程的发现文件，避免误删外部实例的记录。
func (s *Service) removeDiscovery() {
	if s == nil || s.profile == "" || s.home == "" {
		return
	}
	discovery, err := ReadDiscovery(s.home, s.profile)
	if err != nil || discovery.PID != s.PID() {
		return
	}
	if removeErr := RemoveDiscovery(s.home, s.profile); removeErr != nil && s.log != nil {
		s.log.Printf("删除发现文件失败：%v", removeErr)
	}
}

// Start 启动 dsh web，等待它打印 URL。Enabled=false 时返回 nil。
func Start(ctx context.Context, cfg Config, logger *log.Logger) (*Service, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	if logger == nil {
		logger = log.Default()
	}
	port := cfg.Port
	if port == 0 {
		port = 51857
	}
	// 旧行为：启动前清掉端口上残留的 DSH 进程。必须在探测可用端口之前做，
	// 否则会把「自己的残留进程」当成占用而白白后移端口。
	if cfg.CleanupPort {
		if runtime.GOOS == "windows" {
			clearWindowsPort(port, logger)
		} else {
			clearUnixPort(port, logger)
		}
	}
	// 端口可能被别的进程占着，也可能落在 Windows 的「系统保留段」里（Hyper-V/WSL/Docker
	// 会保留大量高位端口，落在其中的端口即使没人用也绑不上，报 listen EACCES）。
	// 撞上就往后找，别让用户手改配置再重启。
	if actual, err := localport.FindAvailable(localport.DefaultHost, port, localport.DefaultScanLimit); err != nil {
		return nil, fmt.Errorf("DSH 端口不可用：%w（可在 Edge 配置里用 dsh_web_port 指定别的端口）", err)
	} else if actual != port {
		logger.Printf("DSH 端口 %d 被占用或被系统保留，改用 %d", port, actual)
		port = actual
	}
	publicURL := strings.TrimSpace(cfg.PublicURL)
	webArgs := []string{"--no-open", "--host", "127.0.0.1", "--port", strconv.Itoa(port)}
	if publicURL != "" {
		parsed, err := url.Parse(publicURL)
		if err != nil || parsed.Host == "" {
			return nil, fmt.Errorf("dsh_web_public_url 无效: %q", publicURL)
		}
		webArgs = append(webArgs, "--public-url", publicURL)
	}
	command, err := LocateDSH(cfg.Command)
	if err != nil {
		return nil, err
	}
	profile := strings.TrimSpace(cfg.Profile)
	args := append([]string{"web"}, webArgs...)
	if profile != "" {
		// --profile 形式的 launcher 参数在前，web 参数作为 app 参数透传。
		args = append([]string{"--profile", profile}, webArgs...)
		if strings.TrimSpace(cfg.PatchPath) != "" {
			args = append([]string{"--patch", cfg.PatchPath}, args...)
		}
	} else if strings.TrimSpace(cfg.PatchPath) != "" {
		args = append([]string{"--patch", cfg.PatchPath, "--profile", "web"}, webArgs...)
	}
	cmdName, cmdArgs := execCommand(command, args)
	runCtx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(runCtx, cmdName, cmdArgs...)
	console.Apply(cmd)
	cmd.Env = commandEnv(command, cfg.ProxyEnv)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("读取 dsh web stdout 失败: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("读取 dsh web stderr 失败: %w", err)
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("启动 dsh web 失败（command=%s）: %w", command, err)
	}
	urlCh := make(chan string, 1)
	tail := &stderrTail{}
	go scanURL(stdout, urlCh, logger)
	go scanLog(stderr, logger, tail)
	service := &Service{cmd: cmd, cancel: cancel, log: logger, profile: profile, home: cfg.DSHHome, port: port, done: make(chan struct{})}
	var exitErr error
	go func() {
		exitErr = cmd.Wait()
		close(service.done)
		if exitErr != nil {
			logger.Printf("dsh web 退出: %v", exitErr)
		}
		service.removeDiscovery()
	}()

	select {
	case raw := <-urlCh:
		return finishStart(service, raw, cfg, port, logger)
	case <-service.done:
		// 进程退出与 URL 输出同时就绪时，先看有没有拿到 URL，别误报失败。
		select {
		case raw := <-urlCh:
			return finishStart(service, raw, cfg, port, logger)
		default:
		}
		// dsh 立即退出时不要傻等到 90 秒超时：把退出码与 stderr 尾部回给上层，
		// 否则 UI 只能看到「等待 URL 超时」，看不出是 node 缺失还是别的原因。
		cancel()
		return nil, exitError(command, exitErr, tail.String())
	case <-time.After(startTimeout):
		killDSHProcess(cmd)
		cancel()
		return nil, fmt.Errorf("等待 dsh web 输出 URL 超时（command=%s）", command)
	case <-ctx.Done():
		cancel()
		return nil, ctx.Err()
	}
}

// finishStart 记录已就绪的 URL 并写发现文件。
func finishStart(service *Service, raw string, cfg Config, port int, logger *log.Logger) (*Service, error) {
	logger.Printf("dsh web 已启动：%s", redactURL(raw))
	go warmup(context.Background(), raw, logger)
	service.url = raw
	if cfg.WriteDiscovery && service.profile != "" {
		discovery := Discovery{Profile: service.profile, URL: raw, PID: service.PID(), Port: port, StartedAt: time.Now().UTC().Format(time.RFC3339)}
		if err := WriteDiscovery(cfg.DSHHome, discovery); err != nil {
			logger.Printf("写入发现文件失败（profile=%s）：%v", service.profile, err)
		}
	}
	return service, nil
}

func mergeEnv(base []string, extra map[string]string) []string {
	values := map[string]string{}
	order := make([]string, 0, len(base)+len(extra))
	for _, item := range base {
		index := strings.IndexByte(item, '=')
		if index <= 0 {
			continue
		}
		key := item[:index]
		if _, ok := values[key]; !ok {
			order = append(order, key)
		}
		values[key] = item[index+1:]
	}
	for key, value := range extra {
		if _, ok := values[key]; !ok {
			order = append(order, key)
		}
		values[key] = value
	}
	result := make([]string, 0, len(order))
	for _, key := range order {
		result = append(result, key+"="+values[key])
	}
	return result
}

func scanURL(reader io.Reader, out chan<- string, logger *log.Logger) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		logger.Printf("dsh web: %s", redactURL(line))
		if raw := parseURL(line); raw != "" {
			select {
			case out <- raw:
			default:
			}
		}
	}
}

func scanLog(reader io.Reader, logger *log.Logger, tail *stderrTail) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		line = redactURL(line)
		tail.Add(line)
		logger.Printf("dsh web stderr: %s", line)
	}
}

// stderrTail 保留 dsh 最后几行 stderr，用于把启动失败原因带给 UI。
type stderrTail struct {
	mu    sync.Mutex
	lines []string
}

const stderrTailLines = 6

func (t *stderrTail) Add(line string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lines = append(t.lines, line)
	if len(t.lines) > stderrTailLines {
		t.lines = t.lines[len(t.lines)-stderrTailLines:]
	}
}

func (t *stderrTail) String() string {
	if t == nil {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.Join(t.lines, " | ")
}

// exitError 把「dsh 启动后立即退出」转成可读错误，并识别常见的 node 缺失。
func exitError(command string, exitErr error, tail string) error {
	detail := strings.TrimSpace(tail)
	lowered := strings.ToLower(detail)
	hint := ""
	if strings.Contains(lowered, "node: no such file or directory") || strings.Contains(lowered, "env: node") {
		hint = "；dsh 需要 node，请确认 Node.js 已安装且在 PATH 中"
	}
	status := "未知状态"
	if exitErr != nil {
		status = exitErr.Error()
	}
	if detail != "" {
		return fmt.Errorf("dsh 启动后立即退出（command=%s，%s）：%s%s", command, status, detail, hint)
	}
	return fmt.Errorf("dsh 启动后立即退出（command=%s，%s）%s", command, status, hint)
}

func parseURL(line string) string {
	index := strings.Index(line, "dsh web:")
	if index < 0 {
		return ""
	}
	s := strings.TrimSpace(line[index+len("dsh web:"):])
	for _, prefix := range []string{"http://", "https://"} {
		if pos := strings.Index(s, prefix); pos >= 0 {
			s = s[pos:]
			break
		}
	}
	if !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") {
		return ""
	}
	if _, err := url.Parse(s); err != nil {
		return ""
	}
	return s
}

// redactURL 隐藏日志中的 token 查询参数。
func redactURL(value string) string {
	parsed, err := url.Parse(value)
	if err != nil || parsed.RawQuery == "" {
		return value
	}
	query := parsed.Query()
	if query.Get("token") != "" {
		query.Set("token", "REDACTED")
		parsed.RawQuery = query.Encode()
	}
	return parsed.String()
}

// clearWindowsPort 清理启动时残留的旧 DSH 进程，避免端口被占用后新进程启动失败。
func clearWindowsPort(port int, logger *log.Logger) {
	netstatCmd := exec.Command("netstat", "-ano")
	console.Apply(netstatCmd)
	out, err := netstatCmd.Output()
	if err != nil {
		return
	}
	needle := fmt.Sprintf("127.0.0.1:%d", port)
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, needle) || !strings.Contains(strings.ToUpper(line), "LISTENING") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		pid := fields[len(fields)-1]
		if _, err := strconv.Atoi(pid); err != nil {
			continue
		}
		logger.Printf("发现 DSH 端口 %d 的残留进程 PID=%s，正在清理", port, pid)
		killCmd := exec.Command("taskkill", "/F", "/PID", pid)
		console.Apply(killCmd)
		_ = killCmd.Run()
	}
}

// execCommand 在 Windows 上通过 cmd.exe 运行 .cmd 形式的 dsh。
func execCommand(command string, args []string) (string, []string) {
	if runtime.GOOS == "windows" {
		return "cmd.exe", append([]string{"/c", command}, args...)
	}
	return command, args
}

// clearUnixPort 清理上次未退出、仍占用 DSH 端口的进程。
func clearUnixPort(port int, logger *log.Logger) {
	lsofCmd := exec.Command("lsof", "-ti", fmt.Sprintf("tcp:%d", port))
	console.Apply(lsofCmd)
	out, err := lsofCmd.Output()
	if err != nil {
		return
	}
	for _, field := range strings.Fields(string(out)) {
		pid, err := strconv.Atoi(field)
		if err != nil || pid == os.Getpid() {
			continue
		}
		logger.Printf("发现 DSH 端口 %d 的残留进程 PID=%d，正在清理", port, pid)
		killCmd := exec.Command("kill", "-9", strconv.Itoa(pid))
		console.Apply(killCmd)
		_ = killCmd.Run()
	}
}
func killDSHProcess(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Kill()
	if runtime.GOOS == "windows" {
		killCmd := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid))
		console.Apply(killCmd)
		_ = killCmd.Run()
	}
}
