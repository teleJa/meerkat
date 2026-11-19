package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"runtime"
	"strings"
	"time"

	"meerkat/edge/internal/buildinfo"
	"meerkat/edge/internal/center"
	"meerkat/edge/internal/identity"
	"meerkat/edge/internal/metrics"
)

// cmdDoctor 处理 doctor 子命令：只读检查身份、中心连通性与指标采集。
// 退出码：0 无失败项（可能有警告），1 存在失败项。
func cmdDoctor(args []string, logger *log.Logger) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	centerFlag := fs.String("center", "", "临时检查指定的中心地址（默认使用身份文件里的中心）")
	stateDir := fs.String("state-dir", "", "身份目录，默认按平台")
	if ok, code := parseFlags(fs, args); !ok {
		return code
	}

	dir, err := resolveStateDir(*stateDir)
	if err != nil {
		logger.Printf("参数错误：%v", err)
		return 2
	}

	failures, warnings := 0, 0
	report := func(level, label, detail string) {
		fmt.Printf("[%s] %-8s %s\n", level, label, detail)
		switch level {
		case "FAIL":
			failures++
		case "WARN":
			warnings++
		}
	}

	fmt.Printf("Meerkat Edge 体检（%s）\n\n", buildinfo.Line())
	report("OK", "环境", fmt.Sprintf("%s/%s，%s", runtime.GOOS, runtime.GOARCH, runtime.Version()))
	report("OK", "状态目录", dir)

	var id *identity.Identity
	loaded, loadErr := identity.Load(dir)
	switch {
	case loadErr == nil:
		id = loaded
		report("OK", "身份", id.Describe())
	case errors.Is(loadErr, identity.ErrNotPaired):
		report("WARN", "身份", fmt.Sprintf("%v（本机尚未配对）", loadErr))
	default:
		report("FAIL", "身份", loadErr.Error())
	}

	// 中心连通性：优先 --center，其次身份文件。
	target := strings.TrimSpace(*centerFlag)
	if target == "" && id != nil {
		target = id.CenterURL
	}
	if target == "" {
		report("WARN", "中心", "未提供中心地址且本机未配对，跳过连通性检查")
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		health, healthErr := center.New(6*time.Second).Health(ctx, target)
		cancel()
		if healthErr != nil {
			report("FAIL", "中心", fmt.Sprintf("%s 不可用：%v", target, healthErr))
		} else {
			report("OK", "中心", fmt.Sprintf("%s ok=%v version=%s uptime=%ds",
				target, health.OK, health.Version, health.UptimeSeconds))
		}
	}

	// 本机指标采集自检。
	reportMetrics(report)

	fmt.Printf("\n结果：%d 项失败，%d 项警告\n", failures, warnings)
	if failures > 0 {
		return 1
	}
	return 0
}

func reportMetrics(report func(level, label, detail string)) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	m, issues := metrics.New().Collect(ctx)

	if m.CPUPct != nil {
		report("OK", "CPU", fmt.Sprintf("整机 %.1f%%", *m.CPUPct))
	} else {
		report("FAIL", "CPU", "采集失败")
	}
	if m.MemUsedBytes != nil && m.MemTotalBytes != nil {
		report("OK", "内存", fmt.Sprintf("%s / %s", humanBytes(*m.MemUsedBytes), humanBytes(*m.MemTotalBytes)))
	} else {
		report("FAIL", "内存", "采集失败")
	}
	if m.DiskUsedBytes != nil && m.DiskTotalBytes != nil {
		report("OK", "磁盘", fmt.Sprintf("%s / %s（%s）", humanBytes(*m.DiskUsedBytes), humanBytes(*m.DiskTotalBytes), metrics.DiskPath()))
	} else {
		report("WARN", "磁盘", "采集失败")
	}

	gpuAvailable := m.GPUUtilPct != nil || m.GPUMemUsedBytes != nil || m.GPUMemTotalBytes != nil || m.GPUTempC != nil
	if gpuAvailable {
		report("OK", "GPU", fmt.Sprintf("利用率 %s，显存 %s / %s，温度 %s",
			formatFloat(m.GPUUtilPct, "%", 0),
			formatUint(m.GPUMemUsedBytes),
			formatUint(m.GPUMemTotalBytes),
			formatFloat(m.GPUTempC, "°C", 0)))
	} else if runtime.GOOS == "windows" {
		report("WARN", "GPU", "nvidia-smi 不可用或执行失败，gpu_* 字段将上报 null")
	} else {
		report("OK", "GPU", "当前平台不采集（仅 Windows 采集 NVIDIA GPU），gpu_* 字段为 null")
	}

	if issues != nil {
		report("WARN", "采集", fmt.Sprintf("存在采集问题：%v", issues))
	}
}

func formatFloat(v *float64, unit string, precision int) string {
	if v == nil {
		return "null"
	}
	return fmt.Sprintf("%.*f%s", precision, *v, unit)
}

func formatUint(v *uint64) string {
	if v == nil {
		return "null"
	}
	return humanBytes(*v)
}

// humanBytes 把字节数格式化为可读字符串。
func humanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for v := n / unit; v >= unit && exp < 4; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %sB", float64(n)/float64(div), string("KMGTP"[exp]))
}
