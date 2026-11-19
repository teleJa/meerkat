package dedup

import "time"

// nowUTC 返回当前 UTC 时间；独立成函数便于测试替换。
func nowUTC() time.Time {
	return time.Now().UTC()
}
