package harnessweb

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// dsh 启动后立刻退出（例如 node 缺失）时，错误里要带上 stderr 尾部而不是干等 90 秒超时。
func TestStartReportsImmediateExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("脚本形式的假 dsh 只在类 Unix 上运行")
	}
	dir := t.TempDir()
	command := filepath.Join(dir, "dsh")
	script := "#!/bin/sh\necho 'env: node: No such file or directory' >&2\nexit 127\n"
	if err := os.WriteFile(command, []byte(script), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	started := time.Now()
	_, err := Start(context.Background(), Config{Enabled: true, Command: command, Port: 0}, nil)
	if err == nil {
		t.Fatalf("Start 应该失败")
	}
	if time.Since(started) > 20*time.Second {
		t.Fatalf("应立即返回，实际耗时 %s", time.Since(started))
	}
	for _, want := range []string{"dsh 启动后立即退出", "No such file or directory", "需要 node"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("错误缺少 %q：%v", want, err)
		}
	}
}
