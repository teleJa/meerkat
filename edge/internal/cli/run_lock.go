package cli

import (
	"errors"
	"fmt"
	"meerkat/edge/internal/filelock"
	"os"
	"path/filepath"
)

// The desktop sidecar launcher interprets this exit code as a lock conflict.
const runLockConflictExitCode = 73

var errRunLockHeld = errors.New("Meerkat Edge 已在运行")

// Keep the file in place so concurrent contenders always lock the same file.
// The kernel releases the lock when the last owning handle closes.
func acquireRunLock(dir string) (*os.File, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "run.lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	locked, err := filelock.Try(file)
	if !locked {
		_ = file.Close()
		if err != nil {
			return nil, fmt.Errorf("锁定 %s: %w", path, err)
		}
		return nil, fmt.Errorf("%w，状态目录正在使用：%s", errRunLockHeld, dir)
	}
	// Byte zero is reserved for LockFileEx. Other handles can read the PID at byte one.
	if err := file.Truncate(0); err == nil {
		_, _ = file.WriteAt([]byte(fmt.Sprint(os.Getpid())), 1)
	}
	return file, nil
}
