package metrics

import (
	"context"
	"testing"
	"time"
)

func TestCollectorCollect(t *testing.T) {
	collector := New()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sample, issues := collector.Collect(ctx)
	if sample.CPUPct == nil {
		t.Fatalf("CPU 采集失败（issues=%v）", issues)
	}
	if *sample.CPUPct < 0 || *sample.CPUPct > 100 {
		t.Fatalf("CPU 使用率超出 0–100: %v", *sample.CPUPct)
	}
	if sample.MemUsedBytes == nil || sample.MemTotalBytes == nil || *sample.MemTotalBytes == 0 {
		t.Fatalf("内存采集失败: %+v", sample)
	}
	if sample.DiskUsedBytes == nil || sample.DiskTotalBytes == nil || *sample.DiskTotalBytes == 0 {
		t.Fatalf("磁盘采集失败: %+v", sample)
	}
	if *sample.MemUsedBytes > *sample.MemTotalBytes {
		t.Fatalf("已用内存大于总量: %d > %d", *sample.MemUsedBytes, *sample.MemTotalBytes)
	}
	if *sample.DiskUsedBytes > *sample.DiskTotalBytes {
		t.Fatalf("已用磁盘大于总量: %d > %d", *sample.DiskUsedBytes, *sample.DiskTotalBytes)
	}
}

func TestCollectorCPURepeated(t *testing.T) {
	collector := New()
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		sample, issues := collector.Collect(ctx)
		if sample.CPUPct == nil {
			t.Fatalf("第 %d 次 CPU 采集失败（issues=%v）", i, issues)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
