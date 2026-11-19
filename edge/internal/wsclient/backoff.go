package wsclient

import (
	"math/rand"
	"time"
)

// Backoff 实现指数退避 + 抖动：1s、2s、4s …… 上限 30s。
// 每次 Next 返回的是"等量抖动"后的实际等待时间（上限的 50%–100%），
// 避免多台 Edge 同时重连；Reset 在成功拿到 hello.ack 后调用。
type Backoff struct {
	Base time.Duration
	Max  time.Duration

	attempt int
	rnd     *rand.Rand
}

// NewBackoff 创建退避器，默认 1s 起、上限 30s。
func NewBackoff(base, max time.Duration) *Backoff {
	if base <= 0 {
		base = time.Second
	}
	if max <= 0 {
		max = 30 * time.Second
	}
	if max < base {
		max = base
	}
	return &Backoff{
		Base: base,
		Max:  max,
		rnd:  rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// Next 返回下一次重连前的等待时间（已含抖动，且不超过 Max）。
func (b *Backoff) Next() time.Duration {
	delay := b.Base
	for i := 0; i < b.attempt && delay < b.Max; i++ {
		delay *= 2
	}
	if delay > b.Max {
		delay = b.Max
	}
	if b.attempt < 32 {
		b.attempt++
	}

	// 等量抖动：实际等待落在 [delay/2, delay]。
	half := delay / 2
	wait := half
	if half > 0 {
		wait += time.Duration(b.rnd.Int63n(int64(half)))
	}
	if wait > b.Max {
		wait = b.Max
	}
	if wait <= 0 {
		wait = time.Millisecond
	}
	return wait
}

// Reset 在连接成功后清零退避次数。
func (b *Backoff) Reset() {
	b.attempt = 0
}
