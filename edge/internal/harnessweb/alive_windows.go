//go:build windows

package harnessweb

// pidAlive 在 Windows 上不做进程探测，交给端口 TCP 探测兜底。
func pidAlive(pid int) bool {
	return false
}
