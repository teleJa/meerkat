package tsnetedge

import (
	"fmt"
	"sync"
)

// dedupeLogf 包装 logf，抑制连续完全相同的消息：
// 相同消息只输出一次，消息变化时补一行“上一条提示重复 N 次，已省略”。
// tsnet 在等待登录时会每 5 秒输出同一条授权地址提示，直接透传会把 Edge 日志刷满。
func dedupeLogf(out func(format string, args ...any)) func(format string, args ...any) {
	var mu sync.Mutex
	last := ""
	repeats := 0
	return func(format string, args ...any) {
		message := fmt.Sprintf(format, args...)
		mu.Lock()
		if message == last {
			repeats++
			mu.Unlock()
			return
		}
		repeated := repeats
		last = message
		repeats = 0
		mu.Unlock()
		if repeated > 0 {
			out("上一条提示重复 %d 次，已省略", repeated)
		}
		out(format, args...)
	}
}
