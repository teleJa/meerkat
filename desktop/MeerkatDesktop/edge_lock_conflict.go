//go:build !android

package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// Kept in sync with the Edge sidecar's runLockConflictExitCode.
const edgeRunLockConflictExitCode = 73

func edgeRunConflictMessage(stateDir string) string {
	name := "meerkat-edge"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	base := fmt.Sprintf("已有 %s 使用此状态目录，本次 Edge 启动已停止。请退出持有它的 Meerkat 后重新打开：%s", name, stateDir)
	file, err := os.Open(filepath.Join(stateDir, "run.lock"))
	if err != nil {
		return base
	}
	defer file.Close()
	if _, err := file.Seek(1, io.SeekStart); err != nil {
		return base
	}
	data, err := io.ReadAll(io.LimitReader(file, 32))
	if err != nil {
		return base
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 1 {
		return base
	}
	command := fmt.Sprintf(`ps -p %d -o comm= | grep -Eq '(^|/)meerkat-edge$' && kill -TERM %d`, pid, pid)
	confirmation := "执行后确认旧进程已退出，再打开 Meerkat。"
	if runtime.GOOS == "windows" {
		command = fmt.Sprintf(`taskkill /PID %d /FI "IMAGENAME eq meerkat-edge.exe" /F`, pid)
		confirmation = "看到终止成功的提示后，再打开 Meerkat。"
	}
	return fmt.Sprintf("%s。记录的 PID 为 %d。如它仍是遗留的 %s，核对 PID 和进程名后可执行：\n%s\n%s", base, pid, name, command, confirmation)
}
