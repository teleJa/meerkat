//go:build !windows

package harnessweb

import (
	"errors"
	"syscall"
)

// pidAlive 用 signal 0 探测进程；没有权限时也认为进程存在。
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
