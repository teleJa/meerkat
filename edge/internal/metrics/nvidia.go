package metrics

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// GPU 是一次 NVIDIA GPU 采样；字段不可用时为 nil，不记为 0。
type GPU struct {
	UtilPct       *float64
	MemUsedBytes  *uint64
	MemTotalBytes *uint64
	TempC         *float64
}

// nvidiaSMIQuery 是 nvidia-smi 的结构化查询字段。
const nvidiaSMIQuery = "utilization.gpu,memory.used,memory.total,temperature.gpu"

// nvidiaSMIArgs 返回结构化查询参数；csv,noheader,nounits 使输出为
// "93, 10920, 16384, 72" 形式的纯数字行（内存单位为 MiB）。
func nvidiaSMIArgs() []string {
	return []string{"--query-gpu=" + nvidiaSMIQuery, "--format=csv,noheader,nounits"}
}

// ParseNvidiaSMI 解析 nvidia-smi 的输出，内存由 MiB 换算为 bytes。
// 多 GPU 时取第一张；[N/A] 等不可用值映射为 nil。
func ParseNvidiaSMI(out string) (*GPU, error) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Split(line, ",")
		if len(fields) < 4 {
			return nil, fmt.Errorf("nvidia-smi 输出字段不足（需要 4 个，实际 %d 个）: %q", len(fields), line)
		}
		gpu := &GPU{}
		var err error
		if gpu.UtilPct, err = parseNvidiaValue(fields[0]); err != nil {
			return nil, fmt.Errorf("解析 GPU 利用率失败: %w", err)
		}
		if gpu.MemUsedBytes, err = parseNvidiaMiB(fields[1]); err != nil {
			return nil, fmt.Errorf("解析 GPU 已用显存失败: %w", err)
		}
		if gpu.MemTotalBytes, err = parseNvidiaMiB(fields[2]); err != nil {
			return nil, fmt.Errorf("解析 GPU 总显存失败: %w", err)
		}
		if gpu.TempC, err = parseNvidiaValue(fields[3]); err != nil {
			return nil, fmt.Errorf("解析 GPU 温度失败: %w", err)
		}
		return gpu, nil
	}
	return nil, errors.New("nvidia-smi 输出为空")
}

// parseNvidiaValue 解析单个数值；空值、[N/A]、N/A、[Not Supported] 视为不可用（nil）。
func parseNvidiaValue(raw string) (*float64, error) {
	s := strings.TrimSpace(raw)
	switch strings.ToLower(s) {
	case "", "n/a", "[n/a]", "not supported", "[not supported]":
		return nil, nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil, fmt.Errorf("无法解析数值 %q", s)
	}
	return &v, nil
}

// parseNvidiaMiB 把 nvidia-smi 的 MiB 数值换算为 bytes。
func parseNvidiaMiB(raw string) (*uint64, error) {
	v, err := parseNvidiaValue(raw)
	if err != nil {
		return nil, err
	}
	if v == nil {
		return nil, nil
	}
	if *v < 0 {
		return nil, fmt.Errorf("显存数值为负: %v", *v)
	}
	bytes := uint64(math.Round(*v * 1024 * 1024))
	return &bytes, nil
}
