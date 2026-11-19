package metrics

import (
	"strings"
	"testing"
)

func TestParseNvidiaSMI(t *testing.T) {
	out := "93, 10920, 16384, 72\n"
	gpu, err := ParseNvidiaSMI(out)
	if err != nil {
		t.Fatalf("ParseNvidiaSMI: %v", err)
	}
	if gpu.UtilPct == nil || *gpu.UtilPct != 93 {
		t.Fatalf("利用率 = %v", gpu.UtilPct)
	}
	if gpu.MemUsedBytes == nil || *gpu.MemUsedBytes != 11450449920 {
		t.Fatalf("已用显存 = %v, want 11450449920（10920 MiB）", gpu.MemUsedBytes)
	}
	if gpu.MemTotalBytes == nil || *gpu.MemTotalBytes != 17179869184 {
		t.Fatalf("总显存 = %v, want 17179869184", gpu.MemTotalBytes)
	}
	if gpu.TempC == nil || *gpu.TempC != 72 {
		t.Fatalf("温度 = %v", gpu.TempC)
	}
}

func TestParseNvidiaSMIMultipleGPUsUsesFirst(t *testing.T) {
	out := "10, 1000, 8192, 50\n20, 2000, 8192, 60\n"
	gpu, err := ParseNvidiaSMI(out)
	if err != nil {
		t.Fatalf("ParseNvidiaSMI: %v", err)
	}
	if gpu.UtilPct == nil || *gpu.UtilPct != 10 {
		t.Fatalf("应取第一张 GPU，实际 %v", gpu.UtilPct)
	}
}

func TestParseNvidiaSMIUnavailableValuesAreNil(t *testing.T) {
	gpu, err := ParseNvidiaSMI("[N/A], [Not Supported], N/A, 55\n")
	if err != nil {
		t.Fatalf("ParseNvidiaSMI: %v", err)
	}
	if gpu.UtilPct != nil || gpu.MemUsedBytes != nil || gpu.MemTotalBytes != nil {
		t.Fatalf("不可用字段应为 nil，实际 %+v", gpu)
	}
	if gpu.TempC == nil || *gpu.TempC != 55 {
		t.Fatalf("温度 = %v", gpu.TempC)
	}
}

func TestParseNvidiaSMIErrors(t *testing.T) {
	if _, err := ParseNvidiaSMI(""); err == nil || !strings.Contains(err.Error(), "为空") {
		t.Fatalf("空输出应报错，实际 %v", err)
	}
	if _, err := ParseNvidiaSMI("93, 10920\n"); err == nil || !strings.Contains(err.Error(), "字段不足") {
		t.Fatalf("字段不足应报错，实际 %v", err)
	}
	if _, err := ParseNvidiaSMI("abc, 1, 2, 3\n"); err == nil || !strings.Contains(err.Error(), "解析 GPU 利用率失败") {
		t.Fatalf("非法数值应报错，实际 %v", err)
	}
	if _, err := ParseNvidiaSMI("93, -1, 2, 3\n"); err == nil || !strings.Contains(err.Error(), "为负") {
		t.Fatalf("负显存应报错，实际 %v", err)
	}
}

func TestNvidiaSMIArgs(t *testing.T) {
	args := nvidiaSMIArgs()
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--query-gpu=utilization.gpu,memory.used,memory.total,temperature.gpu") {
		t.Fatalf("查询字段错误: %v", args)
	}
	if !strings.Contains(joined, "--format=csv,noheader,nounits") {
		t.Fatalf("格式参数错误: %v", args)
	}
}
