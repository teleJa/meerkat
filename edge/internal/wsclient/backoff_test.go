package wsclient

import (
	"testing"
	"time"
)

func TestBackoffGrowsAndCaps(t *testing.T) {
	b := NewBackoff(100*time.Millisecond, time.Second)

	first := b.Next()
	if first < 50*time.Millisecond || first > 100*time.Millisecond {
		t.Fatalf("首次等待 = %v, want [50ms,100ms]", first)
	}

	for i := 0; i < 20; i++ {
		wait := b.Next()
		if wait <= 0 || wait > time.Second {
			t.Fatalf("第 %d 次等待 = %v, want (0, 1s]", i, wait)
		}
	}
	// 多次退避后应达到上限区间 [500ms, 1s]。
	last := b.Next()
	if last < 500*time.Millisecond || last > time.Second {
		t.Fatalf("上限区间等待 = %v, want [500ms,1s]", last)
	}
}

func TestBackoffReset(t *testing.T) {
	b := NewBackoff(100*time.Millisecond, time.Second)
	for i := 0; i < 10; i++ {
		b.Next()
	}
	b.Reset()
	first := b.Next()
	if first < 50*time.Millisecond || first > 100*time.Millisecond {
		t.Fatalf("Reset 后首次等待 = %v, want [50ms,100ms]", first)
	}
}

func TestBackoffDefaults(t *testing.T) {
	b := NewBackoff(0, 0)
	if b.Base != time.Second || b.Max != 30*time.Second {
		t.Fatalf("默认退避参数错误: base=%v max=%v", b.Base, b.Max)
	}
}
