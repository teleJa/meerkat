// Package dshinstall 负责在 Edge 机器上安装 DSH CLI。
package dshinstall

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"meerkat/edge/internal/console"
)

// PackageSpec 是默认安装的 DSH npm 包版本。
const PackageSpec = "@deepseek-ai/dsh@0.2.2-rc.1"

type Result struct {
	Command string
	Output  string
}

// Ensure 安装 DSH CLI，返回可执行的 dsh 路径。
func Ensure(ctx context.Context, logger *log.Logger) (Result, error) {
	if logger == nil {
		logger = log.Default()
	}
	npm := "npm"
	if runtime.GOOS == "windows" {
		npm = "npm.cmd"
	}
	if _, err := exec.LookPath(npm); err != nil {
		return Result{}, fmt.Errorf("未找到 npm，请先安装 Node.js: %w", err)
	}
	args := []string{"install", "-g", PackageSpec, "--no-audit", "--no-fund"}
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd.exe", append([]string{"/c", npm}, args...)...)
	} else {
		cmd = exec.CommandContext(ctx, npm, args...)
	}
	console.Apply(cmd)
	logger.Printf("安装 DSH：%s %s", npm, strings.Join(args, " "))
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		if text != "" {
			return Result{}, fmt.Errorf("安装 DSH 失败: %w: %s", err, truncate(text, 1200))
		}
		return Result{}, fmt.Errorf("安装 DSH 失败: %w", err)
	}
	command, err := resolveDSH(ctx)
	if err != nil {
		return Result{}, err
	}
	logger.Printf("DSH 安装完成：%s", command)
	return Result{Command: command, Output: text}, nil
}

func resolveDSH(ctx context.Context) (string, error) {
	if path, err := exec.LookPath("dsh"); err == nil {
		return path, nil
	}
	npm := "npm"
	if runtime.GOOS == "windows" {
		npm = "npm.cmd"
	}
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd.exe", "/c", npm, "prefix", "-g")
	} else {
		cmd = exec.CommandContext(ctx, npm, "prefix", "-g")
	}
	console.Apply(cmd)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("安装完成但找不到 dsh，请检查 npm 全局 bin 是否在 PATH 中")
	}
	prefix := strings.TrimSpace(string(out))
	if prefix == "" {
		return "", fmt.Errorf("安装完成但 npm prefix 为空")
	}
	candidates := []string{filepath.Join(prefix, "dsh"), filepath.Join(prefix, "bin", "dsh"), filepath.Join(prefix, "dsh.cmd")}
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("安装完成但找不到 dsh 可执行文件（prefix=%s）", prefix)
}

func truncate(text string, max int) string {
	if len(text) <= max {
		return text
	}
	return text[:max] + "…"
}
