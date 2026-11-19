//go:build !windows

package metrics

// collectGPU 在非 Windows 平台不采集 GPU；协议允许字段为 null。
func collectGPU() (*GPU, error) {
	return nil, nil
}
