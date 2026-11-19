// Package protocol 定义 Meerkat Edge 与中心之间的 v1 协议。
//
// 唯一契约来源：shared/protocol/PROTOCOL-v1.md。本包只做消息编解码与地址构造，
// 不包含网络逻辑，便于跨平台单元测试。
package protocol

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	// Version 是当前实现的协议版本。
	Version = "1"

	// RegisterPath 是设备注册接口路径。
	RegisterPath = "/api/edges/register"
	// WSPath 是 Edge WebSocket 入口路径。
	WSPath = "/api/edges/ws"
	// HealthPath 是中心公开健康检查路径。
	HealthPath = "/api/health"
)

const timeLayout = "2006-01-02T15:04:05.000Z07:00"

// FormatTime 把时间格式化为协议要求的 UTC ISO 8601（毫秒）。
func FormatTime(t time.Time) string {
	return t.UTC().Format(timeLayout)
}

// ParseTime 解析协议时间，兼容无毫秒的 RFC3339。
func ParseTime(s string) (time.Time, error) {
	if strings.TrimSpace(s) == "" {
		return time.Time{}, errors.New("时间字段为空")
	}
	for _, layout := range []string{timeLayout, time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("无法解析时间 %q", s)
}

// NormalizeCenterURL 校验并规范化中心地址：必须带 http/https scheme，
// 去掉末尾斜杠与 fragment，保留端口与可选的基础路径。
func NormalizeCenterURL(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", errors.New("中心地址为空，示例：http://100.64.0.1:8787")
	}
	if !strings.Contains(s, "://") {
		return "", fmt.Errorf("中心地址 %q 必须以 http:// 或 https:// 开头", s)
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", fmt.Errorf("中心地址 %q 无法解析: %w", s, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("中心地址 %q 必须以 http:// 或 https:// 开头", s)
	}
	if u.Host == "" {
		return "", fmt.Errorf("中心地址 %q 缺少主机名或端口", s)
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	u.Fragment = ""
	return u.String(), nil
}

// BuildURL 在中心地址后拼接接口路径；如果中心地址带基础路径则保留该前缀。
func BuildURL(centerURL, path string) (*url.URL, error) {
	normalized, err := NormalizeCenterURL(centerURL)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(normalized)
	if err != nil {
		return nil, fmt.Errorf("中心地址 %q 无法解析: %w", normalized, err)
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	u.Path = strings.TrimRight(u.Path, "/") + path
	return u, nil
}

// WSURL 把中心地址转换为 WebSocket 地址：http→ws，https→wss。
func WSURL(centerURL string) (*url.URL, error) {
	u, err := BuildURL(centerURL, WSPath)
	if err != nil {
		return nil, err
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	default:
		return nil, fmt.Errorf("无法从 %q 推导 WebSocket 地址", centerURL)
	}
	return u, nil
}

// RegisterURL 返回设备注册接口地址。
func RegisterURL(centerURL string) (*url.URL, error) {
	return BuildURL(centerURL, RegisterPath)
}

// HealthURL 返回健康检查接口地址。
func HealthURL(centerURL string) (*url.URL, error) {
	return BuildURL(centerURL, HealthPath)
}

// Device 是设备描述，注册与身份文件共用。
type Device struct {
	Name         string   `json:"name"`
	Hostname     string   `json:"hostname"`
	OS           string   `json:"os"`
	Arch         string   `json:"arch"`
	Role         string   `json:"role"`
	Capabilities []string `json:"capabilities"`
	BootID       string   `json:"boot_id"`
}

// RegisterRequest 是 POST /api/edges/register 的请求体。
type RegisterRequest struct {
	PairingToken    string `json:"pairing_token"`
	ProtocolVersion string `json:"protocol_version"`
	EdgeVersion     string `json:"edge_version"`
	Device          Device `json:"device"`
}

// RegisterResponse 是注册成功响应；DeviceSecret 只在配对流程中出现，禁止打印。
type RegisterResponse struct {
	DeviceID            string `json:"device_id"`
	DeviceSecret        string `json:"device_secret"`
	ProtocolVersion     string `json:"protocol_version"`
	CenterTime          string `json:"center_time"`
	HeartbeatIntervalMS int    `json:"heartbeat_interval_ms"`
	MetricsIntervalMS   int    `json:"metrics_interval_ms"`
}

// Metrics 是一条采样；字段为 nil 时序列化为 null（采集失败/不支持，不记为 0）。
type Metrics struct {
	CPUPct           *float64 `json:"cpu_pct"`
	MemUsedBytes     *uint64  `json:"mem_used_bytes"`
	MemTotalBytes    *uint64  `json:"mem_total_bytes"`
	DiskUsedBytes    *uint64  `json:"disk_used_bytes"`
	DiskTotalBytes   *uint64  `json:"disk_total_bytes"`
	GPUUtilPct       *float64 `json:"gpu_util_pct"`
	GPUMemUsedBytes  *uint64  `json:"gpu_mem_used_bytes"`
	GPUMemTotalBytes *uint64  `json:"gpu_mem_total_bytes"`
	GPUTempC         *float64 `json:"gpu_temp_c"`
}

// 客户端 → 中心的消息类型。
const (
	TypeHello        = "hello"
	TypeHeartbeat    = "heartbeat"
	TypeMetricsBatch = "metrics.batch"
)

// Hello 是连接建立后的第一条消息。
type Hello struct {
	Type            string   `json:"type"`
	ProtocolVersion string   `json:"protocol_version"`
	DeviceID        string   `json:"device_id"`
	DeviceSecret    string   `json:"device_secret"`
	EdgeVersion     string   `json:"edge_version"`
	BootID          string   `json:"boot_id"`
	Capabilities    []string `json:"capabilities"`
}

// Heartbeat 是心跳消息。
type Heartbeat struct {
	Type   string `json:"type"`
	Seq    int64  `json:"seq"`
	SentAt string `json:"sent_at"`
}

// MetricsBatch 是指标批量上报消息。
type MetricsBatch struct {
	Type      string  `json:"type"`
	StreamID  string  `json:"stream_id"`
	Seq       int64   `json:"seq"`
	SampledAt string  `json:"sampled_at"`
	Metrics   Metrics `json:"metrics"`
}

// 中心 → 客户端的消息类型。
const (
	TypeHelloAck       = "hello.ack"
	TypeHeartbeatAck   = "heartbeat.ack"
	TypeMetricsAck     = "metrics.ack"
	TypeError          = "error"
	TypeHarnessProfile = "harness.profile"
)

// HarnessProfileCommand 是中心下发的 DSH profile 动作。
// action: select | connect | launch | stop | create | refresh。
type HarnessProfileCommand struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id,omitempty"`
	Action    string `json:"action"`
	Profile   string `json:"profile,omitempty"`
	Mode      string `json:"mode,omitempty"`
}

// HarnessUpdate 是 Edge 上报的 DSH 状态；URL 为空表示这台设备当前没有可用实例。
type HarnessUpdate struct {
	Type string          `json:"type"`
	URL  string          `json:"url"`
	DSH  *HarnessProfile `json:"dsh,omitempty"`
}

// HarnessProfile 是 Edge 上报的 DSH profile 状态快照。
type HarnessProfile struct {
	Profile   string        `json:"profile"`
	Mode      string        `json:"mode"`
	Status    string        `json:"status"`
	Owner     string        `json:"owner,omitempty"`
	Error     string        `json:"error,omitempty"`
	Installed bool          `json:"installed"`
	Profiles  []HarnessItem `json:"profiles"`
	UpdatedAt string        `json:"updated_at,omitempty"`
}

// HarnessItem 是设备上的单个 profile。
type HarnessItem struct {
	Name      string `json:"name"`
	Available bool   `json:"available"`
	Running   bool   `json:"running"`
}

// HelloAck 是中心对 hello 的确认，携带服务端要求的间隔。
type HelloAck struct {
	Type                string `json:"type"`
	ProtocolVersion     string `json:"protocol_version"`
	ServerTime          string `json:"server_time"`
	DeviceID            string `json:"device_id"`
	HeartbeatIntervalMS int    `json:"heartbeat_interval_ms"`
	MetricsIntervalMS   int    `json:"metrics_interval_ms"`
}

// Ack 是心跳或指标的确认。
type Ack struct {
	Type       string `json:"type"`
	Seq        int64  `json:"seq"`
	ReceivedAt string `json:"received_at"`
}

// Error 是中心返回的错误；P1 中随后会关闭连接。
type Error struct {
	Type    string `json:"type"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ---- P3：任务与会话（shared/protocol/PROTOCOL-v1-p3.md） ----

// 中心 → Edge 的 P3 消息类型。
const (
	// TypeCommand 是中心下发的托管指令。
	TypeCommand = "command"
	// TypeEventAck 是中心对 event.batch 的累计确认。
	TypeEventAck = "event.ack"
)

// CommandKind 是 command.kind 的取值。
const (
	CommandStartTurn  = "start_turn"
	CommandSendTurn   = "send_turn"
	CommandCancel     = "cancel"
	CommandInstallDSH = "install_dsh"
)

// CommandPayload 是指令载荷；cancel 指令不带 payload。
type Attachment struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Mime          string `json:"mime"`
	Size          int64  `json:"size"`
	SHA256        string `json:"sha256"`
	ContentBase64 string `json:"content_base64"`
}

type CommandPayload struct {
	Request          string       `json:"request,omitempty"`
	Cwd              string       `json:"cwd,omitempty"`
	RunProfile       string       `json:"run_profile,omitempty"`
	HarnessSessionID *string      `json:"harness_session_id"`
	TimeoutSeconds   int          `json:"timeout_seconds,omitempty"`
	Attachments      []Attachment `json:"attachments,omitempty"`
}

// Command 是中心下发的托管指令。cancel 指令可能没有 turn_id 与 payload。
type Command struct {
	Type      string         `json:"type"`
	CommandID string         `json:"command_id"`
	Kind      string         `json:"kind"`
	TaskID    string         `json:"task_id"`
	SessionID string         `json:"session_id"`
	TurnID    string         `json:"turn_id,omitempty"`
	Payload   CommandPayload `json:"payload,omitempty"`
	CreatedAt string         `json:"created_at,omitempty"`
	ExpiresAt string         `json:"expires_at,omitempty"`
}

// Expired 判断指令是否已过期；时间缺失或无法解析时按未过期处理（由中心兜底）。
// 过期指令不得执行，只能回 command.ack rejected 或在轮次结束时上报 failed。
func (c *Command) Expired(now time.Time) bool {
	if c == nil || strings.TrimSpace(c.ExpiresAt) == "" {
		return false
	}
	t, err := ParseTime(c.ExpiresAt)
	if err != nil {
		return false
	}
	return now.After(t)
}

// EventAck 是中心对 event.batch 的确认；seq 视为同 stream 的累计确认。
type EventAck struct {
	Type     string `json:"type"`
	StreamID string `json:"stream_id"`
	Seq      int64  `json:"seq"`
}

// 客户端 → 中心的 P3 消息类型。
const (
	TypeHarnessUpdate  = "harness.update"
	TypeCommandAck     = "command.ack"
	TypeSessionStarted = "session.started"
	TypeTurnStarted    = "turn.started"
	TypeEventBatch     = "event.batch"
	TypeTurnCompleted  = "turn.completed"
)

// 指令确认状态。
const (
	CommandAckAccepted = "accepted"
	CommandAckRejected = "rejected"
)

// 轮次结束状态。
const (
	TurnSucceeded = "succeeded"
	TurnFailed    = "failed"
	TurnCancelled = "cancelled"
)

// 事件类型（event.batch.events[].type）。
const (
	EventAssistantMessage = "assistant.message"
	EventAssistantDelta   = "assistant.delta"
	EventToolUse          = "tool.use"
	EventToolResult       = "tool.result"
	EventResult           = "result"
	EventSystemInit       = "system.init"
	EventStderr           = "stderr"
	EventPermission       = "permission.request"
	EventTerminalSnapshot = "terminal.snapshot"
	TypeArtifactRegister  = "artifact.register"
)

// Artifact 是 Edge 上报给中心的可预览工件（Phase 1：单文件 ≤2MB，base64 内联）。
type Artifact struct {
	TaskID        string `json:"task_id"`
	TurnID        string `json:"turn_id,omitempty"`
	SessionID     string `json:"session_id,omitempty"`
	Name          string `json:"name"`
	Path          string `json:"path"`
	Mime          string `json:"mime"`
	Size          int64  `json:"size"`
	SHA256        string `json:"sha256"`
	ContentBase64 string `json:"content_base64"`
}

// ArtifactRegister 是 artifact.register 消息。
type ArtifactRegister struct {
	Type     string   `json:"type"`
	Artifact Artifact `json:"artifact"`
}

// CommandAck 是 Edge 对 command 的确认。
type CommandAck struct {
	Type      string `json:"type"`
	CommandID string `json:"command_id"`
	Status    string `json:"status"`
	Detail    string `json:"detail,omitempty"`
	At        string `json:"at,omitempty"`
}

// SessionStarted 报告托管会话已启动（harness_session_id 来自 claude init 事件）。
type SessionStarted struct {
	Type             string `json:"type"`
	CommandID        string `json:"command_id,omitempty"`
	TaskID           string `json:"task_id"`
	SessionID        string `json:"session_id"`
	HarnessSessionID string `json:"harness_session_id,omitempty"`
	HarnessVersion   string `json:"harness_version,omitempty"`
	Cwd              string `json:"cwd,omitempty"`
	At               string `json:"at,omitempty"`
}

// TurnStarted 报告轮次开始执行。
type TurnStarted struct {
	Type      string `json:"type"`
	CommandID string `json:"command_id,omitempty"`
	TaskID    string `json:"task_id"`
	SessionID string `json:"session_id"`
	TurnID    string `json:"turn_id"`
	At        string `json:"at,omitempty"`
}

// Event 是一条托管事件；Payload 内容由事件类型决定。
type Event struct {
	EventID   string         `json:"event_id"`
	TaskID    string         `json:"task_id,omitempty"`
	SessionID string         `json:"session_id,omitempty"`
	TurnID    string         `json:"turn_id,omitempty"`
	Type      string         `json:"type"`
	At        string         `json:"at"`
	Payload   map[string]any `json:"payload,omitempty"`
}

// EventBatch 是一批托管事件；单批最多 200 条（中心 schema 限制）。
type EventBatch struct {
	Type     string  `json:"type"`
	StreamID string  `json:"stream_id"`
	Seq      int64   `json:"seq"`
	Events   []Event `json:"events"`
}

// TurnCompleted 报告轮次结束；result/error/exit_code/duration_ms 可为 null。
type TurnCompleted struct {
	Type       string  `json:"type"`
	TaskID     string  `json:"task_id"`
	SessionID  string  `json:"session_id"`
	TurnID     string  `json:"turn_id"`
	Status     string  `json:"status"`
	ExitCode   *int    `json:"exit_code"`
	DurationMS *int64  `json:"duration_ms"`
	Result     *string `json:"result"`
	Error      *string `json:"error"`
	At         string  `json:"at"`
}

// Envelope 只用于读取消息类型。
type Envelope struct {
	Type string `json:"type"`
}

// UnknownTypeError 表示收到未知消息类型（只记日志，不崩溃）。
type UnknownTypeError struct {
	Type string
}

func (e *UnknownTypeError) Error() string {
	return fmt.Sprintf("未知消息类型 %q", e.Type)
}

// DecodeServerMessage 按 type 解码中心消息，返回 *HelloAck、*Ack 或 *Error。
func DecodeServerMessage(data []byte) (any, error) {
	var env Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("消息不是合法 JSON: %w", err)
	}
	switch env.Type {
	case TypeHelloAck:
		var m HelloAck
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, fmt.Errorf("解析 hello.ack 失败: %w", err)
		}
		return &m, nil
	case TypeHeartbeatAck, TypeMetricsAck:
		var m Ack
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, fmt.Errorf("解析 %s 失败: %w", env.Type, err)
		}
		return &m, nil
	case TypeError:
		var m Error
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, fmt.Errorf("解析 error 失败: %w", err)
		}
		return &m, nil
	case TypeCommand:
		var m Command
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, fmt.Errorf("解析 command 失败: %w", err)
		}
		return &m, nil
	case TypeEventAck:
		var m EventAck
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, fmt.Errorf("解析 event.ack 失败: %w", err)
		}
		return &m, nil
	case TypeHarnessProfile:
		var m HarnessProfileCommand
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, fmt.Errorf("解析 harness.profile 失败: %w", err)
		}
		return &m, nil
	default:
		return nil, &UnknownTypeError{Type: env.Type}
	}
}
