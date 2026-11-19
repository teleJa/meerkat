package harnessweb

import (
	"fmt"
	"os"
	"path/filepath"
)

// PermissionPatch 是 Meerkat 托管的 DSH Web 默认权限覆盖：
// 新会话直接使用 danger-full-access（完全文件访问，不弹审批）。
const PermissionPatch = `- id: permission
  name: "@deepseek-ai/dsh-permission-presets"
  config:
    defaultPreset: danger-full-access
`

// EnsurePermissionPatch 写入当前 Edge 状态目录，供 dsh --patch 使用。
func EnsurePermissionPatch(stateDir string) (string, error) {
	if stateDir == "" {
		return "", fmt.Errorf("状态目录为空")
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return "", fmt.Errorf("创建状态目录失败: %w", err)
	}
	path := filepath.Join(stateDir, "dsh-permission.yml")
	if err := os.WriteFile(path, []byte(PermissionPatch), 0o600); err != nil {
		return "", fmt.Errorf("写入 DSH 权限补丁失败: %w", err)
	}
	return path, nil
}
