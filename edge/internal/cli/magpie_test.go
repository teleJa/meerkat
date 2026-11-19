package cli

import (
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"meerkat/edge/internal/agent"
)

func testLogger() *log.Logger { return log.New(io.Discard, "", 0) }

func TestPrepareHarnessMagpieDefaults(t *testing.T) {
	dir := t.TempDir()
	logger := testLogger()

	extra, env, err := prepareHarness("claude", nil, []string{"--verbose"}, dir, "http://127.0.0.1:3425", "group/coding", false, logger)
	if err != nil {
		t.Fatalf("prepareHarness: %v", err)
	}
	settingsPath := filepath.Join(dir, agent.MagpieSettingsFileName)
	if !containsPair(extra, "--settings", settingsPath) {
		t.Fatalf("缺少 --settings: %v", extra)
	}
	if !containsPair(extra, "--model", "group/coding") {
		t.Fatalf("缺少 --model: %v", extra)
	}
	if env["ANTHROPIC_BASE_URL"] != "http://127.0.0.1:3425" {
		t.Fatalf("env = %v", env)
	}

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("读取 settings: %v", err)
	}
	var parsed struct {
		Env map[string]string `json:"env"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("settings 不是合法 JSON: %v", err)
	}
	if parsed.Env["ANTHROPIC_BASE_URL"] != "http://127.0.0.1:3425" {
		t.Fatalf("settings env = %v", parsed.Env)
	}
	if parsed.Env["ANTHROPIC_DEFAULT_SONNET_MODEL_NAME"] != "group/coding" {
		t.Fatalf("settings model = %v", parsed.Env)
	}
	info, err := os.Stat(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("settings 权限 = %v", info.Mode().Perm())
	}
}

func TestPrepareHarnessSkipsCustomOrDisabled(t *testing.T) {
	dir := t.TempDir()
	logger := testLogger()

	extra, env, err := prepareHarness("stub", nil, []string{"--stub"}, dir, "http://127.0.0.1:3425", "group/coding", false, logger)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(extra, []string{"--stub"}) || len(env) != 0 {
		t.Fatalf("自定义 harness 不应注入: %v %v", extra, env)
	}

	extra, env, err = prepareHarness("claude", nil, nil, dir, "http://127.0.0.1:3425", "group/coding", true, logger)
	if err != nil {
		t.Fatal(err)
	}
	if len(extra) != 0 || len(env) != 0 {
		t.Fatalf("--no-magpie 不应注入: %v %v", extra, env)
	}

	extra, env, err = prepareHarness("claude", nil, []string{"--settings", "/custom/settings.json"}, dir, "http://127.0.0.1:3425", "group/coding", false, logger)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(extra, []string{"--settings", "/custom/settings.json"}) || len(env) != 0 {
		t.Fatalf("用户 settings 不应被覆盖: %v %v", extra, env)
	}
}

func TestResolveMagpie(t *testing.T) {
	cfg := agent.FileConfig{MagpieURL: "http://cfg:3425", HarnessModel: "cfg/model"}
	url, model := resolveMagpie("", "", cfg)
	if url != "http://cfg:3425" || model != "cfg/model" {
		t.Fatalf("config 解析错误: %s %s", url, model)
	}
	url, model = resolveMagpie("http://flag:3425", "flag/model", cfg)
	if url != "http://flag:3425" || model != "flag/model" {
		t.Fatalf("flag 优先级错误: %s %s", url, model)
	}
	url, model = resolveMagpie("", "", agent.FileConfig{})
	if url != agent.DefaultMagpieURL || model != agent.DefaultHarnessModel {
		t.Fatalf("默认值错误: %s %s", url, model)
	}
}

func containsPair(args []string, key, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == key && args[i+1] == value {
			return true
		}
	}
	return false
}
