//go:build windows

package metrics

import (
	"fmt"
	"os"
	"os/exec"

	"meerkat/edge/internal/console"
)

// nvidiaSMIStandardPaths 是 Windows 上 nvidia-smi 的常见安装位置。
var nvidiaSMIStandardPaths = []string{
	`C:\Program Files\NVIDIA Corporation\NVSMI\nvidia-smi.exe`,
}

// findNvidiaSMI 先查 PATH，再查标准安装路径。
func findNvidiaSMI() (string, error) {
	if path, err := exec.LookPath("nvidia-smi"); err == nil {
		return path, nil
	}
	for _, path := range nvidiaSMIStandardPaths {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path, nil
		}
	}
	return "", fmt.Errorf("nvidia-smi 不可用（PATH 与 %s 均未找到）", nvidiaSMIStandardPaths[0])
}

// collectGPU 通过 nvidia-smi 采集 NVIDIA GPU 指标；不可用时返回 nil 与可读错误。
func collectGPU() (*GPU, error) {
	path, err := findNvidiaSMI()
	if err != nil {
		return nil, err
	}
	nvidiaCmd := exec.Command(path, nvidiaSMIArgs()...)
	console.Apply(nvidiaCmd)
	out, err := nvidiaCmd.Output()
	if err != nil {
		return nil, fmt.Errorf("执行 nvidia-smi 失败: %w", err)
	}
	gpu, err := ParseNvidiaSMI(string(out))
	if err != nil {
		return nil, fmt.Errorf("解析 nvidia-smi 输出失败: %w", err)
	}
	return gpu, nil
}
