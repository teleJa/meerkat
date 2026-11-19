package harnessweb

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// writeFakeExecutable 在 dir 下生成一个可执行脚本。
func writeFakeExecutable(t *testing.T, dir, name string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func TestLocateDSHUsesExplicitCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("脚本形式的假 dsh 只在类 Unix 上运行")
	}
	path := writeFakeExecutable(t, t.TempDir(), "dsh")
	got, err := LocateDSH(path)
	if err != nil {
		t.Fatalf("locate: %v", err)
	}
	if got != path {
		t.Fatalf("got %q, want %q", got, path)
	}
}

func TestLocateDSHRejectsMissingExplicitPath(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope", "dsh")
	_, err := LocateDSH(missing)
	if !errors.Is(err, ErrDSHNotFound) {
		t.Fatalf("err = %v, want ErrDSHNotFound", err)
	}
	if !strings.Contains(err.Error(), "dsh_web_command") {
		t.Fatalf("错误文案要提示 dsh_web_command，实际 %q", err.Error())
	}
}

// 覆盖 MBP 场景：Finder 启动的 Edge PATH 只有系统目录，dsh 在 ~/.local/bin。
func TestLocateDSHFindsInstalledDSHWithoutPATH(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("候选路径按 Unix 布局断言")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	// 清空 PATH 并禁用登录 shell 探测，确保只靠候选路径命中。
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("SHELL", filepath.Join(home, "no-such-shell"))
	resetLoginShellLookup(t)

	want := writeFakeExecutable(t, filepath.Join(home, ".local", "bin"), "dsh")
	got, err := LocateDSH("")
	if err != nil {
		t.Fatalf("locate: %v", err)
	}
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestLocateDSHFindsPNPMAndNode22Layouts(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("候选路径按 Unix 布局断言")
	}
	for _, dir := range []string{
		filepath.Join("Library", "pnpm"),
		filepath.Join(".local", "node22", "bin"),
	} {
		t.Run(dir, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("PATH", "/usr/bin:/bin")
			t.Setenv("SHELL", filepath.Join(home, "no-such-shell"))
			resetLoginShellLookup(t)
			want := writeFakeExecutable(t, filepath.Join(home, dir), "dsh")
			got, err := LocateDSH("")
			if err != nil {
				t.Fatalf("locate: %v", err)
			}
			if got != want {
				t.Fatalf("got %q, want %q", got, want)
			}
		})
	}
}

func TestLocateDSHNotFoundIsActionable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("SHELL", filepath.Join(home, "no-such-shell"))
	resetLoginShellLookup(t)
	// 测试机本身装了 dsh（例如 /opt/homebrew/bin），要显式清空候选目录才测得到“找不到”。
	useCandidateDirs(t, nil)
	_, err := LocateDSH("")
	if !errors.Is(err, ErrDSHNotFound) {
		t.Fatalf("err = %v, want ErrDSHNotFound", err)
	}
	for _, want := range []string{"npm install -g", "dsh_web_command"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("错误文案缺少 %q：%q", want, err.Error())
		}
	}
}

func TestLoginShellLookupParsesCommandV(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("假 shell 只在类 Unix 上运行")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", "/usr/bin:/bin")
	want := writeFakeExecutable(t, filepath.Join(home, "opt", "bin"), "dsh")
	shell := filepath.Join(home, "fake-shell")
	// 模拟登录 shell：初始化脚本往 stdout 打一行提示，再输出 command -v 的结果。
	script := "#!/bin/sh\nprintf 'welcome to zsh\\n'\nprintf '" + want + "\\n'\n"
	if err := os.WriteFile(shell, []byte(script), 0o755); err != nil {
		t.Fatalf("write shell: %v", err)
	}
	t.Setenv("SHELL", shell)
	resetLoginShellLookup(t)
	// 只验证登录 shell 这一条通路，不要让候选目录先命中机器上真实的 dsh。
	useCandidateDirs(t, nil)

	got, err := LocateDSH("")
	if err != nil {
		t.Fatalf("locate: %v", err)
	}
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// 只写了命令名、PATH 里却没有时，应该继续走候选路径与登录 shell。
func TestLocateDSHIgnoresBareCommandNameWhenMissing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("候选路径按 Unix 布局断言")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("SHELL", filepath.Join(home, "no-such-shell"))
	resetLoginShellLookup(t)
	want := writeFakeExecutable(t, filepath.Join(home, ".local", "bin"), "dsh")
	got, err := LocateDSH("dsh")
	if err != nil {
		t.Fatalf("locate: %v", err)
	}
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// dsh 的 bin.js 是 `#!/usr/bin/env node`，子进程 PATH 里必须有 node 所在目录。
func TestCommandEnvPrependsDSHDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PATH 断言按 Unix 分隔符")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".local", "lib", "node_modules", "@deepseek-ai", "dsh", "lib")
	command := writeFakeExecutable(t, dir, "dsh")
	t.Setenv("PATH", "/usr/bin:/bin")

	env := commandEnv(command, map[string]string{"DSH_HOME": home})
	pathValue := ""
	for _, item := range env {
		if strings.HasPrefix(item, "PATH=") {
			pathValue = strings.TrimPrefix(item, "PATH=")
		}
	}
	parts := filepath.SplitList(pathValue)
	if len(parts) == 0 || parts[0] != dir {
		t.Fatalf("PATH 首项应为 dsh 所在目录 %q，实际 %q", dir, pathValue)
	}
	if !strings.Contains(pathValue, "/usr/bin") {
		t.Fatalf("原有 PATH 应保留，实际 %q", pathValue)
	}
	if !containsEnv(env, "DSH_HOME="+home) {
		t.Fatalf("额外环境变量应保留：%v", env)
	}
	// 同目录不能重复。
	seen := map[string]int{}
	for _, part := range parts {
		seen[part]++
	}
	if seen[dir] != 1 {
		t.Fatalf("PATH 里 %q 出现 %d 次：%q", dir, seen[dir], pathValue)
	}
}

func containsEnv(env []string, want string) bool {
	for _, item := range env {
		if item == want {
			return true
		}
	}
	return false
}

// useCandidateDirs 在测试期间替换候选目录列表（nil 表示没有候选目录）。
func useCandidateDirs(t *testing.T, dirs []string) {
	t.Helper()
	previous := candidateDirsHook
	candidateDirsHook = func() []string { return dirs }
	t.Cleanup(func() { candidateDirsHook = previous })
}

func resetLoginShellLookup(t *testing.T) {
	t.Helper()
	shellLookup.mu.Lock()
	shellLookup.path = ""
	shellLookup.checkedAt = time.Time{}
	shellLookup.mu.Unlock()
}
