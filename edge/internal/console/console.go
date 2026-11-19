// Package console 负责让 Edge 拉起的子进程在 Windows 上不弹出控制台窗口。
//
// 默认关闭：在终端里手工运行 meerkat-edge 时保留可见控制台，方便看日志。
// App 以无窗口方式拉起 Edge 时（run --no-console 或 MEERKAT_NO_CONSOLE=1），
// cmdRun 调用 Enable；之后所有经过 Apply 的 exec.Cmd 都不会创建可见窗口，
// stdout/stderr 管道与重定向不受影响。
package console

import (
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
)

var enabled atomic.Bool

// Enable 打开或关闭“隐藏子进程控制台”模式。非 Windows 平台上 Apply 不做事。
func Enable(on bool) { enabled.Store(on) }

// Enabled 返回当前模式，便于日志与测试。
func Enabled() bool { return enabled.Load() }

// EnableFromEnvOrFlag 按显式参数或 MEERKAT_NO_CONSOLE 环境变量启用，
// 返回最终是否启用。参数优先；环境变量接受 1/true/yes。
func EnableFromEnvOrFlag(flag bool) bool {
	on := flag
	if !on {
		switch strings.ToLower(strings.TrimSpace(os.Getenv("MEERKAT_NO_CONSOLE"))) {
		case "1", "true", "yes":
			on = true
		}
	}
	Enable(on)
	return on
}

// Apply 在已启用时让 cmd 在 Windows 上不弹出控制台窗口；管道重定向不受影响。
func Apply(cmd *exec.Cmd) {
	if cmd == nil || !Enabled() {
		return
	}
	apply(cmd)
}
