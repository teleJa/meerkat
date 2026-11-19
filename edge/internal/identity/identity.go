// Package identity 管理 Edge 的本地身份文件。
//
// 身份文件保存中心地址、device_id、device_secret 与设备描述；文件权限 0600，
// 目录 0700。device_secret 只允许写入磁盘并用于 hello，禁止打印或写入日志。
package identity

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"meerkat/edge/internal/protocol"
)

// FileName 是身份文件名。
const FileName = "identity.json"

// ErrNotPaired 表示尚未配对（身份文件不存在）。
var ErrNotPaired = errors.New("未找到身份文件")

// Identity 是 identity.json 的内容。
type Identity struct {
	CenterURL           string          `json:"center_url"`
	DeviceID            string          `json:"device_id"`
	DeviceSecret        string          `json:"device_secret"`
	ProtocolVersion     string          `json:"protocol_version"`
	EdgeVersion         string          `json:"edge_version"`
	Device              protocol.Device `json:"device"`
	CreatedAt           string          `json:"created_at"`
	HeartbeatIntervalMS int             `json:"heartbeat_interval_ms,omitempty"`
	MetricsIntervalMS   int             `json:"metrics_interval_ms,omitempty"`
}

// Validate 检查身份是否足以连接中心。
func (id *Identity) Validate() error {
	if id == nil {
		return errors.New("身份为空")
	}
	if _, err := protocol.NormalizeCenterURL(id.CenterURL); err != nil {
		return fmt.Errorf("center_url 无效: %w", err)
	}
	if strings.TrimSpace(id.DeviceID) == "" {
		return errors.New("缺少 device_id")
	}
	if strings.TrimSpace(id.DeviceSecret) == "" {
		return errors.New("缺少 device_secret")
	}
	return nil
}

// Describe 返回可安全打印的身份摘要；绝不包含 device_secret。
func (id *Identity) Describe() string {
	if id == nil {
		return "未配对"
	}
	dev := id.Device
	return fmt.Sprintf("device_id=%s 中心=%s 设备=%s（%s/%s）角色=%s 创建于 %s",
		id.DeviceID, id.CenterURL, dev.Name, dev.OS, dev.Arch, dev.Role, id.CreatedAt)
}

// Path 返回目录下的身份文件路径。
func Path(dir string) string {
	return filepath.Join(dir, FileName)
}

// DefaultDir 返回当前平台默认的状态目录。
func DefaultDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	return DefaultDirFor(runtime.GOOS, home, os.Getenv("LOCALAPPDATA"))
}

// DefaultDirFor 按平台返回默认状态目录，参数显式传入以便测试。
//
//	darwin:  ~/Library/Application Support/Meerkat/edge
//	windows: %LOCALAPPDATA%\Meerkat\edge
//	其他:    ~/.local/state/meerkat/edge
func DefaultDirFor(goos, home, localAppData string) (string, error) {
	switch goos {
	case "darwin":
		if home == "" {
			return "", errors.New("无法确定用户主目录，请用 --state-dir 指定身份目录")
		}
		return filepath.Join(home, "Library", "Application Support", "Meerkat", "edge"), nil
	case "windows":
		if localAppData == "" {
			return "", errors.New("无法确定 LOCALAPPDATA 目录，请用 --state-dir 指定身份目录")
		}
		return filepath.Join(localAppData, "Meerkat", "edge"), nil
	default:
		if home == "" {
			return "", errors.New("无法确定用户主目录，请用 --state-dir 指定身份目录")
		}
		return filepath.Join(home, ".local", "state", "meerkat", "edge"), nil
	}
}

// Load 从目录读取并校验身份文件。
func Load(dir string) (*Identity, error) {
	path := Path(dir)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w %s，请通过 Meerkat App 的「加入工作区」粘贴邀请码完成配对", ErrNotPaired, path)
		}
		return nil, fmt.Errorf("读取身份文件 %s 失败: %w", path, err)
	}
	var id Identity
	if err := json.Unmarshal(data, &id); err != nil {
		return nil, fmt.Errorf("身份文件 %s 不是合法 JSON: %w", path, err)
	}
	if err := id.Validate(); err != nil {
		return nil, fmt.Errorf("身份文件 %s 无效: %w", path, err)
	}
	return &id, nil
}

// Save 以原子方式写入身份文件：目录 0700，文件 0600。
func Save(dir string, id *Identity) error {
	if err := id.Validate(); err != nil {
		return fmt.Errorf("拒绝保存无效身份: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("创建状态目录 %s 失败: %w", dir, err)
	}
	// MkdirAll 受 umask 影响，这里显式收紧权限（Windows 上仅影响只读位）。
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("设置状态目录权限失败 %s: %w", dir, err)
	}
	data, err := json.MarshalIndent(id, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化身份失败: %w", err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(dir, ".identity-*.json")
	if err != nil {
		return fmt.Errorf("在 %s 创建临时文件失败: %w", dir, err)
	}
	defer os.Remove(tmp.Name())

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("设置身份文件权限失败: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("写入身份文件失败: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("同步身份文件失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭身份文件失败: %w", err)
	}
	if err := os.Rename(tmp.Name(), Path(dir)); err != nil {
		return fmt.Errorf("保存身份文件 %s 失败: %w", Path(dir), err)
	}
	return nil
}
