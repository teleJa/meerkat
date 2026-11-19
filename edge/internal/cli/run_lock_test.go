package cli

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"meerkat/edge/internal/identity"
)

func TestRunLockAcrossProcesses(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := exec.CommandContext(ctx, executable, "-test.run=^TestRunLockHolder$")
	child.Env = append(os.Environ(), "MEERKAT_TEST_RUN_LOCK_DIR="+dir)
	output, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	child.Stderr = os.Stderr
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	if line, err := bufio.NewReader(output).ReadString('\n'); err != nil || strings.TrimSpace(line) != "locked" {
		t.Fatalf("lock holder readiness: %q, %v", line, err)
	}
	file, err := os.Open(filepath.Join(dir, "run.lock"))
	if err != nil {
		t.Fatal(err)
	}
	pid := make([]byte, 32)
	n, err := file.ReadAt(pid, 1)
	file.Close()
	if err != io.EOF || string(pid[:n]) != strconv.Itoa(child.Process.Pid) {
		t.Fatalf("holder metadata: %q, %v", pid[:n], err)
	}
	var diagnostics bytes.Buffer
	if code := cmdRun([]string{"--state-dir", dir}, log.New(&diagnostics, "", 0)); code != runLockConflictExitCode || !strings.Contains(diagnostics.String(), "Meerkat Edge 已在运行") {
		t.Fatalf("second run: exit=%d log=%s", code, diagnostics.String())
	}
	other, err := acquireRunLock(t.TempDir())
	if err != nil {
		t.Fatalf("isolated state directory: %v", err)
	}
	other.Close()
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()
	lock, err := acquireRunLock(dir)
	if err != nil {
		t.Fatalf("abrupt process exit left the directory locked: %v", err)
	}
	lock.Close()
}

func TestRunLockHolder(t *testing.T) {
	dir := os.Getenv("MEERKAT_TEST_RUN_LOCK_DIR")
	if dir == "" {
		t.Skip("subprocess helper")
	}
	lock, err := acquireRunLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	_, _ = io.WriteString(os.Stdout, "locked\n")
	time.Sleep(30 * time.Second)
}

func TestManagedRunStopsWhenParentPipeCloses(t *testing.T) {
	if dir := os.Getenv("MEERKAT_TEST_MANAGED_RUN_DIR"); dir != "" {
		os.Exit(cmdRun([]string{"--state-dir", dir, "--no-magpie"}, log.New(os.Stderr, "", 0)))
	}
	reached := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		select {
		case reached <- struct{}{}:
		default:
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"dsh_web_enabled":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := identity.Save(dir, &identity.Identity{CenterURL: server.URL, DeviceID: "dev_test", DeviceSecret: "test"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := exec.CommandContext(ctx, executable, "-test.run=^TestManagedRunStopsWhenParentPipeCloses$")
	child.Env = append(os.Environ(), "MEERKAT_TEST_MANAGED_RUN_DIR="+dir, "MEERKAT_EDGE_PARENT_STDIN=1")
	var diagnostics bytes.Buffer
	child.Stderr = &diagnostics
	pipe, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer child.Wait()
	defer cancel()
	select {
	case <-reached:
	case <-ctx.Done():
		_ = child.Wait()
		t.Fatalf("Edge did not attempt to connect: %s", diagnostics.String())
	}
	if err := pipe.Close(); err != nil {
		t.Fatal(err)
	}
	if err := child.Wait(); err != nil || !strings.Contains(diagnostics.String(), "桌面进程连接已关闭") {
		t.Fatalf("Edge did not exit cleanly after parent pipe closure: %v\n%s", err, diagnostics.String())
	}
}
