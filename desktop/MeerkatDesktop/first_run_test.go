//go:build !android

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withConfigDir 把 HOME 指到临时目录（configDir() 在 macOS 上跟随 HOME），
// 并恢复 appCfg。返回临时 HOME。
func withConfigDir(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	old := appCfg
	t.Cleanup(func() { appCfg = old })
	return home
}

// writeConfig 把 JSON 写进当前 configDir()；dir 存在时当成目录（制造读失败）。
func writeConfig(t *testing.T, payload string, asDir bool) {
	t.Helper()
	cfgDir := configDir()
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cfgDir, "config.json")
	if asDir {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		return
	}
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
}

// 真正的首次启动（没有 config.json）不应该默认成 bundled：
// 之前那样会让新机器一上来就把自己当 Hub，用户根本看不到「加入别人的工作区」。
func TestLoadConfigFirstRunLeavesModeEmpty(t *testing.T) {
	withConfigDir(t)

	loadConfig()

	if !firstRunPending() {
		t.Fatalf("首次启动应处于 pending 状态，实际 mode=%q", appCfg.Mode)
	}
	if appCfg.Mode == "bundled" {
		t.Fatal("首次启动不该默认 bundled")
	}
}

// 已经有 config.json 的用户完全不受影响，也不该看到第二次欢迎页。
func TestLoadConfigExistingBundledUnchanged(t *testing.T) {
	withConfigDir(t)
	writeConfig(t, `{"mode":"bundled","center_url":"http://127.0.0.1:8790","bind_host":"127.0.0.1"}`, false)

	loadConfig()

	if firstRunPending() {
		t.Fatal("已有 bundled 配置的用户不该进入首启欢迎页")
	}
	if appCfg.Mode != "bundled" {
		t.Fatalf("mode = %q，want bundled", appCfg.Mode)
	}
	if appCfg.CenterURL != "http://127.0.0.1:8790" {
		t.Fatalf("center_url 被改动了: %q", appCfg.CenterURL)
	}
}

// node 模式（已加入别人的工作区）同样不受影响。
func TestLoadConfigExistingNodeUnchanged(t *testing.T) {
	withConfigDir(t)
	writeConfig(t, `{"mode":"node","center_url":"http://192.0.2.10:7443"}`, false)

	loadConfig()

	if firstRunPending() {
		t.Fatal("已有 node 配置的用户不该进入首启欢迎页")
	}
	if appCfg.Mode != "node" {
		t.Fatalf("mode = %q，want node", appCfg.Mode)
	}
}

// 欢迎页「创建新工作区」写出的配置必须能直接被下一次启动识别成 bundled，
// 且不再触发欢迎页。
func TestCreateWorkspaceWritesBundledConfig(t *testing.T) {
	withConfigDir(t)
	loadConfig() // 先走一次首启态

	if err := createWorkspace(); err != nil {
		t.Fatalf("createWorkspace: %v", err)
	}

	path := filepath.Join(configDir(), "config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("config.json 未写入: %v", err)
	}
	var cfg appConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("写出的配置不是合法 JSON: %v", err)
	}
	if cfg.Mode != "bundled" {
		t.Fatalf("mode = %q，want bundled", cfg.Mode)
	}
	if strings.TrimSpace(cfg.CenterURL) == "" {
		t.Fatal("center_url 为空")
	}
	if strings.TrimSpace(cfg.EdgeStateDir) == "" {
		t.Fatal("edge_state_dir 为空")
	}

	// 再读一次，应该直接是 bundled，不再 pending。
	appCfg = appConfig{}
	loadConfig()
	if firstRunPending() {
		t.Fatal("创建完工作区后不该再进入欢迎页")
	}
	if appCfg.Mode != "bundled" {
		t.Fatalf("重启后 mode = %q，want bundled", appCfg.Mode)
	}
}

// 配置文件存在但读不出来时，不能误判成「首次启动」——那会把用户配置覆盖掉。
func TestLoadConfigUnreadableFileNotFirstRun(t *testing.T) {
	withConfigDir(t)
	// 造一个目录冒充 config.json：os.ReadFile 会返回非 ErrNotExist 的错误。
	writeConfig(t, "", true)

	loadConfig()

	if firstRunPending() {
		t.Fatal("读不出来的配置文件不能被当成首次启动")
	}
}
