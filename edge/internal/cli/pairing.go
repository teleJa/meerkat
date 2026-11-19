package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"meerkat/edge/internal/buildinfo"
	"meerkat/edge/internal/center"
	"meerkat/edge/internal/device"
	"meerkat/edge/internal/identity"
	"meerkat/edge/internal/protocol"
)

// pairingStatusFileName 是 Edge 自动配对的进度文件，供 Meerkat App 展示与排障。
// 与 tsnet-status.json 分开：后者由 tsnetedge 写入，这里只描述配对阶段。
const pairingStatusFileName = "pairing-status.json"

// 配对阶段。App 只读取这些值做展示，不参与配对判断。
const (
	pairPhaseWaitingNetwork = "waiting_network"
	pairPhasePairing        = "pairing"
	pairPhasePaired         = "paired"
	pairPhaseError          = "error"
)

// pairingStatus 是 pairing-status.json 的内容。
type pairingStatus struct {
	Phase    string `json:"phase"`
	Error    string `json:"error,omitempty"`
	HubURL   string `json:"hub_url,omitempty"`
	DeviceID string `json:"device_id,omitempty"`
}

// writePairingStatus 原子写入配对进度文件；失败只记录，不影响主流程。
func writePairingStatus(stateDir string, status pairingStatus, logger *log.Logger) {
	stateDir = strings.TrimSpace(stateDir)
	if stateDir == "" {
		return
	}
	data, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return
	}
	path := filepath.Join(stateDir, pairingStatusFileName)
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		if logger != nil {
			logger.Printf("写入配对进度失败（创建目录）：%v", err)
		}
		return
	}
	tmp, err := os.CreateTemp(stateDir, ".pairing-status-*.json")
	if err != nil {
		if logger != nil {
			logger.Printf("写入配对进度失败：%v", err)
		}
		return
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return
	}
	if err := tmp.Close(); err != nil {
		return
	}
	if err := os.Rename(tmpName, path); err != nil && logger != nil {
		logger.Printf("写入配对进度失败：%v", err)
	}
}

// pairRequest 描述一次“用一次性配对令牌注册设备”。
type pairRequest struct {
	// StateDir 是 Edge 状态目录，配对成功后写入 identity.json。
	StateDir string
	// HubURL 是 Hub 的访问地址；启用 tsnet 时为 tailnet 地址，否则为普通 HTTP 地址。
	HubURL string
	// Token 是 Hub 生成的一次性配对令牌。
	Token string
	// Name 覆盖设备显示名；为空时使用主机名。
	Name string
	// Role 覆盖设备角色；为空时使用默认角色。
	Role string
	// Dial 可选：非空时注册请求经该拨号发出（内嵌 tsnet）。
	Dial func(ctx context.Context, network, address string) (net.Conn, error)
	// Logger 可选：用于输出协议版本不匹配等提示，绝不打印令牌或 device_secret。
	Logger *log.Logger
}

// pairDevice 用一次性配对令牌在 Hub 注册本机，并把身份写入状态目录。
// 注册成功后返回写入的身份；任何失败都不会留下半成品身份文件。
func pairDevice(ctx context.Context, req pairRequest) (*identity.Identity, error) {
	if strings.TrimSpace(req.StateDir) == "" {
		return nil, fmt.Errorf("状态目录为空")
	}
	hubURL, err := protocol.NormalizeCenterURL(req.HubURL)
	if err != nil {
		return nil, err
	}
	token := strings.TrimSpace(req.Token)
	if token == "" {
		return nil, fmt.Errorf("缺少配对令牌：请在 Meerkat App 重新生成邀请码并重新加入工作区")
	}

	dev, err := device.Info()
	if err != nil {
		return nil, fmt.Errorf("读取设备信息失败: %w", err)
	}
	if name := strings.TrimSpace(req.Name); name != "" {
		dev.Name = name
	}
	if role := strings.TrimSpace(req.Role); role != "" {
		dev.Role = role
	}

	client := center.NewWithDialer(20*time.Second, req.Dial)
	resp, err := client.Register(ctx, hubURL, protocol.RegisterRequest{
		PairingToken:    token,
		ProtocolVersion: protocol.Version,
		EdgeVersion:     buildinfo.Version,
		Device:          dev,
	})
	if err != nil {
		return nil, err
	}
	if req.Logger != nil && resp.ProtocolVersion != "" && resp.ProtocolVersion != protocol.Version {
		req.Logger.Printf("警告：Hub 协议版本 %s 与 Edge 协议 %s 不一致，连接可能被拒绝", resp.ProtocolVersion, protocol.Version)
	}

	id := &identity.Identity{
		CenterURL:           hubURL,
		DeviceID:            resp.DeviceID,
		DeviceSecret:        resp.DeviceSecret,
		ProtocolVersion:     protocol.Version,
		EdgeVersion:         buildinfo.Version,
		Device:              dev,
		CreatedAt:           protocol.FormatTime(time.Now()),
		HeartbeatIntervalMS: resp.HeartbeatIntervalMS,
		MetricsIntervalMS:   resp.MetricsIntervalMS,
	}
	if err := identity.Save(req.StateDir, id); err != nil {
		return nil, err
	}
	return id, nil
}
