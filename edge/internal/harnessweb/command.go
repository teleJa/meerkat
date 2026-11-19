package harnessweb

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"meerkat/edge/internal/console"
)

// ErrDSHNotFound 表示本机没有找到可执行的 dsh。
//
// macOS 上从 Finder / Dock 启动的 App 由 launchd 继承环境，PATH 往往只有
// /usr/bin:/bin:/usr/sbin:/sbin，既没有 nvm/pnpm 目录也没有 /opt/homebrew/bin，
// 因此不能只依赖 PATH。
var ErrDSHNotFound = errors.New("未找到 dsh")

const (
	// loginShellLookupTimeout 限制登录 shell 探测的耗时，避免拖慢状态刷新。
	loginShellLookupTimeout = 4 * time.Second
	// loginShellLookupTTL 是探测结果（包括“没找到”）的缓存时间。
	loginShellLookupTTL = 5 * time.Minute
)

// LocateDSH 按「显式配置 → PATH → 常见安装位置 → 登录 shell」的顺序定位 dsh。
// 找不到时返回包装了 ErrDSHNotFound 的可操作错误。
func LocateDSH(configured string) (string, error) {
	if value := strings.TrimSpace(configured); value != "" {
		if path, ok := executablePath(value); ok {
			return path, nil
		}
		// 显式写了一个路径却没找到：直接报错，别用一个同名的别的 dsh 顶替。
		if strings.ContainsAny(value, `/\`) {
			return "", dshNotFoundError(value)
		}
		// 只写了命令名（例如默认值 "dsh"）时继续按常规顺序探测。
	}
	if path, err := exec.LookPath(dshBinaryName()); err == nil {
		return path, nil
	}
	for _, candidate := range dshCandidatePaths() {
		if path, ok := executablePath(candidate); ok {
			return path, nil
		}
	}
	if path, ok := executablePath(loginShellLookup()); ok {
		return path, nil
	}
	return "", dshNotFoundError("")
}

// dshNotFoundError 生成用户能照做的错误文案；UI 会把它显示在设备卡片上。
func dshNotFoundError(configured string) error {
	if value := strings.TrimSpace(configured); value != "" {
		return fmt.Errorf("%w：dsh_web_command=%q 不是可执行文件，请改成 dsh 的绝对路径（终端里 `command -v dsh` 可查）", ErrDSHNotFound, value)
	}
	return fmt.Errorf("%w：请先安装（npm install -g @deepseek-ai/dsh），或在 Edge 配置里把 dsh_web_command 设成 dsh 的绝对路径", ErrDSHNotFound)
}

func dshBinaryName() string {
	if runtime.GOOS == "windows" {
		return "dsh.cmd"
	}
	return "dsh"
}

// dshCandidatePaths 返回常见安装位置下的 dsh 候选路径，已按优先级排序。
func dshCandidatePaths() []string {
	names := []string{"dsh"}
	if runtime.GOOS == "windows" {
		names = []string{"dsh.cmd", "dsh.exe", "dsh"}
	}
	var paths []string
	for _, dir := range dshCandidateDirs() {
		for _, name := range names {
			paths = append(paths, filepath.Join(dir, name))
		}
	}
	return paths
}

// dshCandidateDirs 覆盖 npm / pnpm / nvm / fnm / volta / bun / brew 的常见全局 bin。
func dshCandidateDirs() []string {
	// candidateDirsHook 只给测试用：测试机本身装了 dsh 时，
	// 需要能在一个“什么都没有”的环境里验证找不到 dsh 的分支。
	if candidateDirsHook != nil {
		return candidateDirsHook()
	}
	dirs := make([]string, 0, 24)
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		dirs = append(dirs,
			filepath.Join(home, ".local", "bin"),
			filepath.Join(home, ".local", "node22", "bin"),
			filepath.Join(home, ".local", "node", "bin"),
			filepath.Join(home, "Library", "pnpm"),
			filepath.Join(home, ".local", "share", "pnpm"),
			filepath.Join(home, ".npm-global", "bin"),
			filepath.Join(home, ".npm", "bin"),
			filepath.Join(home, ".pnpm-global", "bin"),
			filepath.Join(home, ".yarn", "bin"),
			filepath.Join(home, ".volta", "bin"),
			filepath.Join(home, ".bun", "bin"),
			filepath.Join(home, "bin"),
			filepath.Join(home, ".asdf", "shims"),
		)
		dirs = append(dirs, versionedBinDirs(home)...)
	}
	dirs = append(dirs, "/opt/homebrew/bin", "/usr/local/bin")
	if runtime.GOOS == "windows" {
		if appData := strings.TrimSpace(os.Getenv("APPDATA")); appData != "" {
			dirs = append(dirs, filepath.Join(appData, "npm"))
		}
		if localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); localAppData != "" {
			dirs = append(dirs, filepath.Join(localAppData, "pnpm"))
		}
	}
	return dirs
}

// versionedBinDirs 展开 nvm / fnm / asdf 这类按 Node 版本分目录的全局 bin，
// 新版本排在前面。
func versionedBinDirs(home string) []string {
	patterns := []string{
		filepath.Join(home, ".nvm", "versions", "node", "*", "bin"),
		filepath.Join(home, "Library", "Application Support", "fnm", "node-versions", "*", "installation", "bin"),
		filepath.Join(home, ".local", "share", "fnm", "node-versions", "*", "installation", "bin"),
		filepath.Join(home, ".asdf", "installs", "nodejs", "*", "bin"),
	}
	var dirs []string
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil || len(matches) == 0 {
			continue
		}
		sort.Sort(sort.Reverse(sort.StringSlice(matches)))
		dirs = append(dirs, matches...)
	}
	return dirs
}

// loginShellLookup 用登录 shell 查一次 dsh（读取用户的 .zshrc / .zprofile 等初始化脚本），
// 结果缓存一段时间：状态每 3 秒刷新一次，不能每次都起 shell。
func loginShellLookup() string {
	if runtime.GOOS == "windows" {
		// Windows 没有登录 shell，候选目录 + PATH 已覆盖 npm 全局 bin。
		return ""
	}
	shellLookup.mu.Lock()
	defer shellLookup.mu.Unlock()
	if shellLookup.path != "" {
		if _, ok := executablePath(shellLookup.path); ok {
			return shellLookup.path
		}
		shellLookup.path = ""
	}
	if !shellLookup.checkedAt.IsZero() && time.Since(shellLookup.checkedAt) < loginShellLookupTTL {
		return ""
	}
	shellLookup.path = runLoginShellLookup()
	shellLookup.checkedAt = time.Now()
	return shellLookup.path
}

// candidateDirsHook 让测试替换候选目录列表（生产代码里为 nil）。
var candidateDirsHook func() []string

var shellLookup struct {
	mu        sync.Mutex
	path      string
	checkedAt time.Time
}

func runLoginShellLookup() string {
	shell := loginShell()
	ctx, cancel := context.WithTimeout(context.Background(), loginShellLookupTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, shell, "-lic", "command -v "+dshBinaryName())
	console.Apply(cmd)
	output, err := cmd.Output()
	if err != nil && len(output) == 0 {
		return ""
	}
	// 初始化脚本可能往 stdout 打提示，只接受看起来是绝对路径的行。
	lines := strings.Split(string(output), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if !filepath.IsAbs(line) {
			continue
		}
		if path, ok := executablePath(line); ok {
			return path
		}
	}
	return ""
}

// executablePath 校验 value 是否可执行：带路径分隔符时直接看文件，
// 否则按命令名在 PATH 里查。
func executablePath(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	if strings.ContainsRune(value, '/') || (runtime.GOOS == "windows" && strings.ContainsRune(value, '\\')) {
		info, err := os.Stat(value)
		if err != nil || info.IsDir() {
			return "", false
		}
		if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
			return "", false
		}
		if abs, err := filepath.Abs(value); err == nil {
			return abs, true
		}
		return value, true
	}
	if path, err := exec.LookPath(value); err == nil {
		return path, true
	}
	return "", false
}

// commandEnv 返回 dsh 子进程要用的环境：保留现有变量，并把 dsh 所在目录与
// 常见 Node 目录前置到 PATH。
//
// dsh 的 bin.js 用 `#!/usr/bin/env node`，Finder 启动的 Edge 里 PATH 没有 node
// 目录时，即使找到了 dsh 也会以 "env: node: No such file or directory" 退出。
func commandEnv(command string, extra map[string]string) []string {
	return withCommandPath(mergeEnv(os.Environ(), extra), command)
}

func withCommandPath(env []string, command string) []string {
	prefix := commandPathDirs(command)
	if len(prefix) == 0 {
		return env
	}
	currentPath := ""
	rest := make([]string, 0, len(env))
	for _, item := range env {
		index := strings.IndexByte(item, '=')
		if index <= 0 {
			continue
		}
		if item[:index] == "PATH" {
			currentPath = item[index+1:]
			continue
		}
		rest = append(rest, item)
	}
	seen := make(map[string]bool, len(prefix)+8)
	parts := make([]string, 0, len(prefix)+8)
	appendDir := func(dir string) {
		if dir == "" || seen[dir] {
			return
		}
		seen[dir] = true
		parts = append(parts, dir)
	}
	for _, dir := range prefix {
		appendDir(dir)
	}
	for _, dir := range filepath.SplitList(currentPath) {
		appendDir(dir)
	}
	return append(rest, "PATH="+strings.Join(parts, string(os.PathListSeparator)))
}

// commandPathDirs 列出需要前置到 PATH 的目录：dsh 自身目录、符号链接目标目录、
// node 所在目录，以及常见的包管理器 bin。
func commandPathDirs(command string) []string {
	command = strings.TrimSpace(command)
	if command == "" {
		return nil
	}
	dirs := make([]string, 0, 6)
	add := func(dir string) {
		if dir == "" {
			return
		}
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			return
		}
		dirs = append(dirs, dir)
	}
	if abs, err := filepath.Abs(command); err == nil {
		add(filepath.Dir(abs))
	}
	if resolved, err := filepath.EvalSymlinks(command); err == nil {
		add(filepath.Dir(resolved))
	}
	if nodePath, err := exec.LookPath("node"); err == nil {
		add(filepath.Dir(nodePath))
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		add(filepath.Join(home, ".local", "bin"))
		add(filepath.Join(home, ".local", "node", "bin"))
		add(filepath.Join(home, ".local", "node22", "bin"))
		if matches := versionedBinDirs(home); len(matches) > 0 {
			add(matches[0])
		}
	}
	add("/opt/homebrew/bin")
	add("/usr/local/bin")
	// 兜底：登录 shell 的 PATH 里可能有任意安装位置（nvm、asdf 等）。
	for _, dir := range loginShellDirs() {
		add(dir)
	}
	return dirs
}

// loginShellDirs 返回登录 shell 的 PATH 目录列表，带缓存：
// 只在真正启动子进程时用到，不能每次都起 shell。
func loginShellDirs() []string {
	if runtime.GOOS == "windows" {
		return nil
	}
	loginShellPATH.mu.Lock()
	defer loginShellPATH.mu.Unlock()
	if !loginShellPATH.checkedAt.IsZero() && time.Since(loginShellPATH.checkedAt) < loginShellLookupTTL {
		return loginShellPATH.dirs
	}
	loginShellPATH.dirs = runLoginShellPATH()
	loginShellPATH.checkedAt = time.Now()
	return loginShellPATH.dirs
}

var loginShellPATH struct {
	mu        sync.Mutex
	dirs      []string
	checkedAt time.Time
}

func runLoginShellPATH() []string {
	shell := loginShell()
	ctx, cancel := context.WithTimeout(context.Background(), loginShellLookupTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, shell, "-lic", `printf %s "$PATH"`)
	console.Apply(cmd)
	output, err := cmd.Output()
	if err != nil && len(output) == 0 {
		return nil
	}
	value := strings.TrimSpace(string(output))
	// 初始化脚本可能往 stdout 打提示，取最后一行看起来像 PATH 的内容。
	lines := strings.Split(value, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		candidate := strings.TrimSpace(lines[i])
		if !strings.ContainsRune(candidate, os.PathListSeparator) {
			continue
		}
		return filepath.SplitList(candidate)
	}
	return nil
}

func loginShell() string {
	if shell := strings.TrimSpace(os.Getenv("SHELL")); shell != "" {
		return shell
	}
	if runtime.GOOS == "darwin" {
		return "/bin/zsh"
	}
	return "/bin/sh"
}
