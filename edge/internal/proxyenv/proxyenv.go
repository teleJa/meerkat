// Package proxyenv 从用户登录 shell 读取代理环境，供 DSH 子进程使用。
package proxyenv

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"

	"meerkat/edge/internal/console"
)

var proxyKeys = []string{
	"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY",
	"http_proxy", "https_proxy", "all_proxy", "no_proxy",
}

// Detect 优先读取登录 shell 的显式代理变量；没有时回退到当前进程环境。
// 返回值只包含代理相关变量，并补充 NODE_USE_ENV_PROXY=1。
func Detect(ctx context.Context, hubURL string) map[string]string {
	values := map[string]string{}
	if runtime.GOOS != "windows" {
		for key, value := range parseEnv(runLoginShell(ctx)) {
			values[key] = value
		}
	}
	if len(values) == 0 {
		for _, key := range proxyKeys {
			if value := strings.TrimSpace(os.Getenv(key)); value != "" {
				values[key] = value
			}
		}
	}
	return normalize(values, hubURL)
}

// Parse 从一组 "KEY=VALUE" 行中提取代理变量，便于测试。
func Parse(output string) map[string]string {
	return parseEnv(output)
}

func parseEnv(output string) map[string]string {
	values := map[string]string{}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		index := strings.IndexByte(line, '=')
		if index <= 0 {
			continue
		}
		key, value := line[:index], strings.TrimSpace(line[index+1:])
		if value == "" || !isProxyKey(key) {
			continue
		}
		values[strings.ToUpper(key)] = value
	}
	return values
}

func runLoginShell(ctx context.Context) string {
	shell := strings.TrimSpace(os.Getenv("SHELL"))
	if shell == "" {
		if runtime.GOOS == "darwin" {
			shell = "/bin/zsh"
		} else {
			shell = "/bin/sh"
		}
	}
	timeout, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	cmd := exec.CommandContext(timeout, shell, "-lic", "env")
	console.Apply(cmd)
	cmd.Env = os.Environ()
	output, err := cmd.Output()
	if err != nil && len(output) == 0 {
		return ""
	}
	if len(output) > 1024*1024 {
		output = output[:1024*1024]
	}
	return string(output)
}

func normalize(values map[string]string, hubURL string) map[string]string {
	result := map[string]string{}
	for key, value := range values {
		key = strings.ToUpper(key)
		if !isProxyKey(key) || strings.TrimSpace(value) == "" {
			continue
		}
		result[key] = strings.TrimSpace(value)
	}
	hasProxy := result["HTTP_PROXY"] != "" || result["HTTPS_PROXY"] != "" || result["ALL_PROXY"] != ""
	if !hasProxy {
		return nil
	}
	result["NODE_USE_ENV_PROXY"] = "1"
	result["NO_PROXY"] = mergeNoProxy(result["NO_PROXY"], hubURL)
	return result
}

func mergeNoProxy(value, hubURL string) string {
	parts := []string{"localhost", "127.0.0.1", "::1"}
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			parts = append(parts, item)
		}
	}
	if hub := hostOnly(hubURL); hub != "" {
		parts = append(parts, hub)
	}
	seen := map[string]bool{}
	unique := make([]string, 0, len(parts))
	for _, item := range parts {
		if !seen[item] {
			seen[item] = true
			unique = append(unique, item)
		}
	}
	sort.Strings(unique)
	return strings.Join(unique, ",")
}

func hostOnly(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if index := strings.Index(raw, "://"); index >= 0 {
		raw = raw[index+3:]
	}
	raw = strings.TrimSuffix(raw, "/")
	if index := strings.IndexAny(raw, "/?#"); index >= 0 {
		raw = raw[:index]
	}
	if index := strings.LastIndex(raw, ":"); index > 0 && !strings.Contains(raw[index+1:], "]") {
		raw = raw[:index]
	}
	return strings.Trim(raw, "[]")
}

func isProxyKey(key string) bool {
	key = strings.ToUpper(strings.TrimSpace(key))
	for _, candidate := range proxyKeys {
		if strings.ToUpper(candidate) == key {
			return true
		}
	}
	return false
}
