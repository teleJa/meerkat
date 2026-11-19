package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// 指令拒绝原因（command.ack.detail），文本直接作为 detail 上报。
var (
	ErrCwdMissing    = errors.New("cwd_missing")
	ErrCwdNotFound   = errors.New("cwd_not_found")
	ErrCwdNotAllowed = errors.New("cwd_not_allowed")
)

// ResolveAllowRoot 按 --allow-root > config.json allow_root > 用户 home 的优先级
// 解析允许根目录，返回绝对路径。
func ResolveAllowRoot(flagValue, configValue string) (string, error) {
	for _, raw := range []string{flagValue, configValue} {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		abs, err := filepath.Abs(value)
		if err != nil {
			return "", fmt.Errorf("解析 allow_root %q 失败: %w", value, err)
		}
		return abs, nil
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return "", errors.New("无法确定用户主目录，请用 --allow-root 或 config.json 的 allow_root 指定")
	}
	abs, err := filepath.Abs(home)
	if err != nil {
		return "", fmt.Errorf("解析用户主目录 %q 失败: %w", home, err)
	}
	return abs, nil
}

// ValidateAllowRoot 校验允许根存在且是目录。
func ValidateAllowRoot(root string) error {
	if strings.TrimSpace(root) == "" {
		return errors.New("allow_root 为空")
	}
	info, err := os.Stat(root)
	if err != nil {
		return fmt.Errorf("allow_root %s 不可用: %w", root, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("allow_root %s 不是目录", root)
	}
	return nil
}

// ValidateCwd 校验 cwd 存在、是目录且位于 allowRoot 之内。
// 返回的错误文本就是 command.ack.detail（cwd_missing/cwd_not_found/cwd_not_allowed）。
func ValidateCwd(allowRoot, cwd string) error {
	trimmed := strings.TrimSpace(cwd)
	if trimmed == "" {
		return ErrCwdMissing
	}
	abs, err := filepath.Abs(trimmed)
	if err != nil {
		return ErrCwdNotFound
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return ErrCwdNotFound
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return ErrCwdNotFound
	}
	rootResolved, err := filepath.EvalSymlinks(allowRoot)
	if err != nil {
		return ErrCwdNotAllowed
	}
	if !pathWithin(rootResolved, resolved) {
		return ErrCwdNotAllowed
	}
	return nil
}

// pathWithin 判断 target 是否等于 root 或位于 root 之下（Windows 忽略大小写）。
func pathWithin(root, target string) bool {
	root = filepath.Clean(root)
	target = filepath.Clean(target)
	if root == target {
		return true
	}
	if runtime.GOOS == "windows" {
		root = strings.ToLower(root)
		target = strings.ToLower(target)
	}
	sep := string(filepath.Separator)
	prefix := root
	if !strings.HasSuffix(prefix, sep) {
		prefix += sep
	}
	return strings.HasPrefix(target, prefix)
}
