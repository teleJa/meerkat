// Package metrics 采集本机资源指标：CPU、内存、磁盘与（Windows）NVIDIA GPU。
//
// 采集失败或平台不支持的字段保持 nil，序列化为 null，绝不写 0。
package metrics

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/mem"

	"meerkat/edge/internal/protocol"
)

// cpuSampleWindow 是两次 CPU 采样之间的最小窗口。
// 使用 cpu.Percent(0) 计算"自上次调用以来"的整机使用率：macOS 的 CPU tick 粒度约为 1s，
// 小于 1s 的间隔采样经常得到 0（gopsutil 的 interval 采样模式同样如此），
// 因此先建立基准、按真实间隔计算，并在窗口不足时补足到 1s。
const cpuSampleWindow = time.Second

// Collector 是资源指标采集器，可重复使用（非并发安全时由内部互斥锁保护 CPU 基准）。
type Collector struct {
	mu          sync.Mutex
	cpuWindow   time.Duration
	lastCPUTime time.Time
}

// New 创建采集器并建立 CPU 基准。
func New() *Collector {
	c := &Collector{
		cpuWindow:   cpuSampleWindow,
		lastCPUTime: time.Now(),
	}
	// 第一次 cpu.Percent(0) 返回 0，仅用于建立基准，不进入上报。
	_, _ = cpu.Percent(0, false)
	c.lastCPUTime = time.Now()
	return c
}

// Collect 采集一次指标。返回的 Metrics 中采集失败的字段为 nil；
// error 汇总采集问题供调用方记录日志（不为 nil 时指标仍可用于上报）。
func (c *Collector) Collect(ctx context.Context) (protocol.Metrics, error) {
	var m protocol.Metrics
	var problems []error

	cpuPct, err := c.collectCPU(ctx)
	if err != nil {
		problems = append(problems, err)
	} else if cpuPct != nil {
		m.CPUPct = cpuPct
	}

	if vm, err := mem.VirtualMemory(); err != nil {
		problems = append(problems, fmt.Errorf("内存采集失败: %w", err))
	} else {
		used, total := vm.Used, vm.Total
		m.MemUsedBytes = &used
		m.MemTotalBytes = &total
	}

	diskPath := DiskPath()
	if du, err := disk.Usage(diskPath); err != nil {
		problems = append(problems, fmt.Errorf("磁盘采集失败 %s: %w", diskPath, err))
	} else {
		used, total := du.Used, du.Total
		m.DiskUsedBytes = &used
		m.DiskTotalBytes = &total
	}

	gpu, gpuErr := collectGPU()
	if gpuErr != nil {
		problems = append(problems, gpuErr)
	}
	if gpu != nil {
		m.GPUUtilPct = gpu.UtilPct
		m.GPUMemUsedBytes = gpu.MemUsedBytes
		m.GPUMemTotalBytes = gpu.MemTotalBytes
		m.GPUTempC = gpu.TempC
	}

	return m, errors.Join(problems...)
}

// collectCPU 返回整机 CPU 使用率（0–100）；窗口不足时先补足再采样。
func (c *Collector) collectCPU(ctx context.Context) (*float64, error) {
	c.mu.Lock()
	wait := c.cpuWindow - time.Since(c.lastCPUTime)
	c.mu.Unlock()
	if wait > 0 {
		timer := time.NewTimer(wait)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return nil, nil
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	percentages, err := cpu.PercentWithContext(ctx, 0, false)
	c.lastCPUTime = time.Now()
	if err != nil {
		return nil, fmt.Errorf("CPU 采集失败: %w", err)
	}
	if len(percentages) == 0 {
		return nil, errors.New("CPU 采集失败: 没有采样结果")
	}
	pct := percentages[0]
	return &pct, nil
}

// DiskPath 返回用于统计磁盘用量的挂载点：Windows 为系统盘，其余为根分区。
func DiskPath() string {
	if runtime.GOOS == "windows" {
		if drive := os.Getenv("SystemDrive"); drive != "" {
			return drive + `\`
		}
		return `C:\`
	}
	return "/"
}
