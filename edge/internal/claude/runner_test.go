package claude

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestExecRunnerArgs(t *testing.T) {
	r := NewExecRunner("", []string{"--stub-mode"}, []string{"--fixture", "x"})
	got := r.Args(RunOptions{Request: "hi", ResumeSessionID: "sess-1"})
	want := []string{"--stub-mode", "-p", "hi", "--output-format", "stream-json", "--verbose", "--include-partial-messages", "--resume", "sess-1", "--fixture", "x"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Args = %v, want %v", got, want)
	}
	plain := NewExecRunner("claude", nil, nil).Args(RunOptions{Request: "hi"})
	wantPlain := []string{"-p", "hi", "--output-format", "stream-json", "--verbose", "--include-partial-messages"}
	if !reflect.DeepEqual(plain, wantPlain) {
		t.Fatalf("默认 Args = %v, want %v", plain, wantPlain)
	}
}

func writeStub(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell stub 只在类 Unix 上运行")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "stub.sh")
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("写 stub 失败: %v", err)
	}
	return path
}

func TestExecRunnerParsesTempStub(t *testing.T) {
	path := writeStub(t, `#!/bin/sh
cat <<'EOF'
{"type":"system","subtype":"init","session_id":"stub-sess","model":"stub","claude_code_version":"9.9.9"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"OK"}]},"session_id":"stub-sess"}
{"type":"result","subtype":"success","is_error":false,"result":"OK","duration_ms":7,"session_id":"stub-sess"}
EOF
exit 0
`)

	r := NewExecRunner(path, nil, nil)
	var entries []Entry
	res, err := r.Run(context.Background(), RunOptions{Request: "请回复 OK", Cwd: t.TempDir()}, func(entry Entry) {
		entries = append(entries, entry)
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("事件数 = %d, want 3", len(entries))
	}
	if res.SessionID != "stub-sess" || res.HarnessVersion != "9.9.9" {
		t.Fatalf("session/version = %q/%q", res.SessionID, res.HarnessVersion)
	}
	if res.ExitCode == nil || *res.ExitCode != 0 {
		t.Fatalf("ExitCode = %v", res.ExitCode)
	}
	if res.ResultText != "OK" || res.IsError {
		t.Fatalf("result = %+v", res)
	}
	if res.ParseErrors != 0 {
		t.Fatalf("ParseErrors = %d", res.ParseErrors)
	}
}

func TestExecRunnerNonZeroExit(t *testing.T) {
	path := writeStub(t, `#!/bin/sh
echo "stub 启动失败" >&2
exit 3
`)
	r := NewExecRunner(path, nil, nil)
	res, err := r.Run(context.Background(), RunOptions{Request: "hi", Cwd: t.TempDir()}, nil)
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if res.ExitCode == nil || *res.ExitCode != 3 {
		t.Fatalf("ExitCode = %v", res.ExitCode)
	}
	if !strings.Contains(res.Stderr, "stub 启动失败") {
		t.Fatalf("Stderr = %q", res.Stderr)
	}
}

func TestExecRunnerCancelKillsProcessGroup(t *testing.T) {
	path := writeStub(t, `#!/bin/sh
printf '{"type":"system","subtype":"init","session_id":"slow-sess"}
'
sleep 30
`)
	r := NewExecRunner(path, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	_, err := r.Run(ctx, RunOptions{Request: "hi", Cwd: t.TempDir()}, nil)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("取消后 Run 不应返回错误: %v", err)
	}
	if ctx.Err() != context.Canceled {
		t.Fatalf("ctx.Err() = %v", ctx.Err())
	}
	if elapsed > 10*time.Second {
		t.Fatalf("取消后进程未及时退出，耗时 %s", elapsed)
	}
}

func TestExecRunnerStartFailure(t *testing.T) {
	r := NewExecRunner(filepath.Join(t.TempDir(), "missing-binary"), nil, nil)
	_, err := r.Run(context.Background(), RunOptions{Request: "hi", Cwd: t.TempDir()}, nil)
	if err == nil || !strings.Contains(err.Error(), "启动") {
		t.Fatalf("启动失败应返回可读错误，实际 %v", err)
	}
}
