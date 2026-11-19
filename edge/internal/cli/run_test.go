package cli

import (
	"reflect"
	"testing"

	"meerkat/edge/internal/agent"
	"meerkat/edge/internal/claude"
)

func TestResolveHarness(t *testing.T) {
	cfg := agent.FileConfig{
		HarnessCommand: agent.HarnessCommand{Executable: "/cfg/stub", PrefixArgs: []string{"--cfg-mode"}},
		HarnessArgs:    []string{"--cfg-arg"},
	}

	path, prefix, extra := resolveHarness("", "", cfg, []string{"--flag-arg"})
	if path != "/cfg/stub" {
		t.Fatalf("config harness = %q", path)
	}
	if !reflect.DeepEqual(prefix, []string{"--cfg-mode"}) {
		t.Fatalf("prefix = %v", prefix)
	}
	if !reflect.DeepEqual(extra, []string{"--cfg-arg", "--flag-arg"}) {
		t.Fatalf("extra = %v", extra)
	}

	path, prefix, extra = resolveHarness("/flag/claude", "", cfg, nil)
	if path != "/flag/claude" {
		t.Fatalf("flag harness = %q", path)
	}
	if len(prefix) != 0 {
		t.Fatalf("命令行覆盖 harness 时不应沿用 config 前缀参数: %v", prefix)
	}
	if !reflect.DeepEqual(extra, []string{"--cfg-arg"}) {
		t.Fatalf("extra = %v", extra)
	}

	path, prefix, extra = resolveHarness("", "/flag/harness-cmd", agent.FileConfig{}, nil)
	if path != "/flag/harness-cmd" || len(prefix) != 0 || len(extra) != 0 {
		t.Fatalf("--harness-cmd 解析错误: %q %v %v", path, prefix, extra)
	}

	path, prefix, extra = resolveHarness("", "", agent.FileConfig{}, nil)
	if path != claude.DefaultCommand || len(prefix) != 0 || len(extra) != 0 {
		t.Fatalf("默认 harness = %q %v %v", path, prefix, extra)
	}
}
