package harnessweb

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"meerkat/edge/internal/console"
)

// Profile 是设备上的一个 DSH profile；Running 以有效发现文件为准。
type Profile struct {
	Name      string `json:"name"`
	Available bool   `json:"available"`
	Running   bool   `json:"running"`
}

var profileNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// reservedProfiles 是 DSH 自带的 profile 名，不能作为 --from-default-profile 的目标。
var reservedProfiles = map[string]bool{
	"web": true, "tui": true, "headless": true, "rescue": true,
	"desktop": true, "sdk": true, "sdk-minimal": true,
}

// ProfilesDir 返回 $DSH_HOME/profiles。
func ProfilesDir(home string) string {
	return filepath.Join(home, "profiles")
}

// ListProfiles 列出 profiles 目录下的 profile，并标记可用/运行中。
func ListProfiles(home string) []Profile {
	entries, err := os.ReadDir(ProfilesDir(home))
	if err != nil {
		return []Profile{}
	}
	profiles := make([]Profile, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || strings.HasPrefix(name, ".") || !profileNamePattern.MatchString(name) {
			continue
		}
		dir := filepath.Join(ProfilesDir(home), name)
		available := false
		if info, statErr := os.Stat(filepath.Join(dir, "package.json")); statErr == nil && !info.IsDir() {
			available = true
		}
		running := false
		if discovery, readErr := ReadDiscovery(home, name); readErr == nil && discovery.Alive() {
			running = true
		}
		profiles = append(profiles, Profile{Name: name, Available: available, Running: running})
	}
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].Name < profiles[j].Name })
	return profiles
}

// ValidProfileName 校验新建 profile 的名称。
func ValidProfileName(name string) error {
	name = strings.TrimSpace(name)
	if !profileNamePattern.MatchString(name) {
		return fmt.Errorf("profile 名只能包含小写字母、数字、点、下划线和连字符，且不超过 64 个字符")
	}
	if reservedProfiles[name] {
		return fmt.Errorf("profile %q 是 DSH 自带 profile，不能新建", name)
	}
	if strings.Contains(name, "..") {
		return fmt.Errorf("profile 名不能包含 ..")
	}
	return nil
}

// CreateProfile 用 DSH 自带模板创建一个 profile，不启动服务。
//
// `dsh --profile <name> --from-default-profile web --help` 会先复制模板创建
// profile，再打印帮助退出；这是 DSH 当前提供的“只建 profile”能力。
func CreateProfile(ctx context.Context, command, home, name string) error {
	name = strings.TrimSpace(name)
	if err := ValidProfileName(name); err != nil {
		return err
	}
	dir := filepath.Join(ProfilesDir(home), name)
	if _, err := os.Stat(dir); err == nil {
		return fmt.Errorf("profile %q 已存在", name)
	}
	if err := os.MkdirAll(ProfilesDir(home), 0o700); err != nil {
		return fmt.Errorf("创建 profiles 目录失败: %w", err)
	}
	runCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	command, err := LocateDSH(command)
	if err != nil {
		return err
	}
	cmdName, cmdArgs := execCommand(command, []string{
		"--profile", name, "--from-default-profile", "web", "--help",
	})
	cmd := exec.CommandContext(runCtx, cmdName, cmdArgs...)
	cmd.Env = commandEnv(command, map[string]string{"DSH_HOME": home})
	console.Apply(cmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		detail := tailText(string(output), 400)
		if detail != "" {
			return fmt.Errorf("创建 profile %q 失败: %w（%s）", name, err, detail)
		}
		return fmt.Errorf("创建 profile %q 失败: %w", name, err)
	}
	if info, statErr := os.Stat(filepath.Join(dir, "package.json")); statErr != nil || info.IsDir() {
		return fmt.Errorf("创建 profile %q 后没有找到 package.json", name)
	}
	return nil
}

func tailText(value string, max int) string {
	value = strings.TrimSpace(value)
	if len(value) <= max {
		return value
	}
	return "..." + value[len(value)-max:]
}
