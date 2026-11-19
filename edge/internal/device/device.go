// Package device 采集本机设备描述：主机名、OS 名称、架构与 boot_id。
package device

import (
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/shirou/gopsutil/v4/host"

	"meerkat/edge/internal/protocol"
)

// DefaultRole 是未指定 --role 时使用的角色标签。
const DefaultRole = "Edge"

// DefaultCapabilities 是 P1 声明的能力。
func DefaultCapabilities() []string {
	return []string{"metrics"}
}

// Info 返回当前设备描述；boot_id 由系统启动时间派生，可在同一启动周期内稳定去重。
func Info() (protocol.Device, error) {
	hi, err := host.Info()
	if err != nil {
		return protocol.Device{}, fmt.Errorf("读取主机信息失败: %w", err)
	}
	hostname := strings.TrimSpace(hi.Hostname)
	if hostname == "" {
		hostname, _ = os.Hostname()
	}
	bootID, err := BootID()
	if err != nil {
		return protocol.Device{}, err
	}
	return protocol.Device{
		Name:         hostname,
		Hostname:     hostname,
		OS:           FormatOS(runtime.GOOS, hi.Platform, hi.PlatformVersion),
		Arch:         runtime.GOARCH,
		Role:         DefaultRole,
		Capabilities: DefaultCapabilities(),
		BootID:       bootID,
	}, nil
}

// BootID 用系统启动时间派生 boot_id，例如 boot_1e2d3c4b。
func BootID() (string, error) {
	bootTime, err := host.BootTime()
	if err != nil {
		return "", fmt.Errorf("读取系统启动时间失败: %w", err)
	}
	return fmt.Sprintf("boot_%08x", bootTime), nil
}

// FormatOS 把 gopsutil 的平台信息转成可读名称：
// darwin → "macOS 15.4"，windows → "Windows 11 Pro 10.0.26100"，其余回落 GOOS。
func FormatOS(goos, platform, platformVersion string) string {
	version := strings.TrimSpace(platformVersion)
	switch goos {
	case "darwin":
		if version != "" {
			return "macOS " + version
		}
		return "macOS"
	case "windows":
		name := strings.TrimSpace(platform)
		name = strings.TrimPrefix(name, "Microsoft ")
		if name == "" {
			name = "Windows"
		}
		if version != "" {
			return name + " " + version
		}
		return name
	default:
		return goos
	}
}
