// Package agent 负责 P3 托管执行：接收中心指令、串行执行 Claude 轮次、
// 回传 session/turn/事件与结果，并处理 outbox、去重与 cwd 权限校验。
package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ConfigFileName 是状态目录下可选的配置文件。
const ConfigFileName = "config.json"

// HarnessCommand 支持两种 JSON 形态：
//   - 字符串：可执行文件路径，例如 "/opt/stub/claude"；
//   - 字符串数组：第一个是可执行文件，其余是追加在标准参数之前的前缀参数。
type HarnessCommand struct {
	Executable string
	PrefixArgs []string
}

// UnmarshalJSON 兼容字符串与数组两种形式。
func (h *HarnessCommand) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		h.Executable = strings.TrimSpace(s)
		return nil
	}
	var list []string
	if err := json.Unmarshal(data, &list); err == nil {
		if len(list) > 0 {
			h.Executable = strings.TrimSpace(list[0])
			h.PrefixArgs = list[1:]
		}
		return nil
	}
	return errors.New("harness_command 必须是字符串或字符串数组")
}

// 默认 magpie 网关（本机模型代理）与路由模型组。
const (
	DefaultMagpieURL       = "http://127.0.0.1:3425"
	DefaultHarnessModel    = "group/coding"
	MagpieSettingsFileName = "magpie-settings.json"
)

// FileConfig 是状态目录 config.json 的内容；未知字段忽略。
type FileConfig struct {
	// AllowRoot 是允许 Claude 工作的根目录，默认用户 home。
	AllowRoot string `json:"allow_root"`
	// HarnessCommand 替换默认的 claude 可执行文件（联调 stub 时使用）。
	HarnessCommand HarnessCommand `json:"harness_command"`
	// HarnessArgs 追加在标准参数之后的参数。
	HarnessArgs []string `json:"harness_args"`
	// MagpieURL 覆盖默认 magpie 网关地址。
	MagpieURL string `json:"magpie_url"`
	// HarnessModel 覆盖默认 Claude 模型（magpie 路由组）。
	HarnessModel string `json:"harness_model"`
	// MagpieDisabled 为 true 时关闭默认 magpie 配置注入（使用用户自己的 Claude 配置）。
	MagpieDisabled bool `json:"magpie_disabled"`
	// Runtime 是托管执行运行时：headless（默认，Claude stream-json）或 herdr（Herdr agent pane）。
	Runtime string `json:"runtime"`
	// HerdrBin 是 herdr 可执行文件路径，默认 herdr。
	HerdrBin string `json:"herdr_bin"`
	// DSHWebEnabled 为 true 时由 Edge 启动本机 dsh web 并上报 Harness URL。
	DSHWebEnabled bool `json:"dsh_web_enabled"`
	// DSHWebCommand 是 dsh 可执行文件，默认 dsh。
	DSHWebCommand string `json:"dsh_web_command"`
	// DSHWebPort 是 dsh web 的首选监听端口，默认 51857。
	// 该端口被占用或落在系统保留段里时，Edge 会自动向后探测，实际端口上报给 Hub。
	DSHWebPort int `json:"dsh_web_port"`
	// LocalAPIAddr 是本机 Hub API 代理的首选监听地址，默认 127.0.0.1:51900。
	// 同样支持自动后退：绑不上就往后找可用端口，实际地址写进 tsnet-status.json。
	LocalAPIAddr string `json:"local_api_addr"`
	// DSHWebPublicURL 是 Tailscale Serve 暴露的公开根地址。
	DSHWebPublicURL string `json:"dsh_web_public_url"`
	// TSNetEnabled 为 true 时 Edge 内嵌 tsnet 节点，不依赖系统 Tailscale。
	TSNetEnabled bool `json:"tsnet_enabled"`
	// TSNetHostname 是 Edge 在 tailnet 内的主机名。
	TSNetHostname string `json:"tsnet_hostname"`
	// TSNetHubURL 是 Hub 的 tsnet 地址，例如 http://100.x.x.x:7443。
	TSNetHubURL string `json:"tsnet_hub_url"`
	// TSNetAuthKey 是可选的 tailnet auth key；为空时使用交互登录 URL。
	TSNetAuthKey string `json:"tsnet_auth_key"`
	// PairingToken 是 Hub 生成的一次性配对令牌。Edge 首次运行（状态目录还没有
	// identity.json）时用它注册设备，注册成功后写入 identity.json。
	//
	// 配对地址复用 TSNetHubURL，不额外增加字段：启用 tsnet 时经内嵌 tailnet 访问
	// Hub，未启用时按普通 HTTP 访问同一地址（bundled 模式即 http://127.0.0.1:8787）。
	// 令牌只在首次配对时使用；配对完成后可以保留（不会再被读取）或删除。
	PairingToken string `json:"pairing_token"`
}

// WriteMagpieSettings 在状态目录写入供 claude --settings 使用的配置，
// 将 Claude Code 的 ANTHROPIC_BASE_URL 指向 magpie，并使用路由模型组。
// 文件权限 0600；不包含任何密钥（鉴权仍由用户 Claude 配置提供）。
func WriteMagpieSettings(stateDir, magpieURL, model string) (string, error) {
	if strings.TrimSpace(stateDir) == "" {
		return "", errors.New("状态目录为空")
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return "", fmt.Errorf("创建状态目录失败: %w", err)
	}
	content := map[string]any{
		"env": map[string]string{
			"ANTHROPIC_BASE_URL":                  magpieURL,
			"ANTHROPIC_DEFAULT_SONNET_MODEL_NAME": model,
			"ANTHROPIC_DEFAULT_HAIKU_MODEL_NAME":  model,
			"ANTHROPIC_DEFAULT_OPUS_MODEL_NAME":   model,
			"ANTHROPIC_DEFAULT_FABLE_MODEL_NAME":  model,
		},
	}
	data, err := json.MarshalIndent(content, "", "  ")
	if err != nil {
		return "", fmt.Errorf("序列化 magpie 设置失败: %w", err)
	}
	absDir, err := filepath.Abs(stateDir)
	if err != nil {
		return "", fmt.Errorf("解析状态目录 %s 失败: %w", stateDir, err)
	}
	path := filepath.Join(absDir, MagpieSettingsFileName)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("写入 %s 失败: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return "", fmt.Errorf("设置 %s 权限失败: %w", path, err)
	}
	return path, nil
}

// ConfigPath 返回状态目录下的 config.json 路径。
func ConfigPath(stateDir string) string {
	return filepath.Join(stateDir, ConfigFileName)
}

// LoadFileConfig 读取状态目录 config.json；文件不存在时返回零值，不报错。
func LoadFileConfig(stateDir string) (FileConfig, error) {
	var cfg FileConfig
	if strings.TrimSpace(stateDir) == "" {
		return cfg, nil
	}
	path := ConfigPath(stateDir)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// 未显式配置时默认启用 DSH；缺少 CLI 时由 Harness 页面引导安装。
			cfg.DSHWebEnabled = true
			return cfg, nil
		}
		return cfg, fmt.Errorf("读取 %s 失败: %w", path, err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("%s 不是合法 JSON: %w", path, err)
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) == nil {
		if _, ok := raw["dsh_web_enabled"]; !ok {
			cfg.DSHWebEnabled = true
		}
	}
	return cfg, nil
}
