package hub

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

type wsArtifact struct {
	TaskID        string `json:"task_id"`
	TurnID        string `json:"turn_id"`
	SessionID     string `json:"session_id"`
	Name          string `json:"name"`
	Path          string `json:"path"`
	Mime          string `json:"mime"`
	Size          int64  `json:"size"`
	SHA256        string `json:"sha256"`
	ContentBase64 string `json:"content_base64"`
}

type wsMessage struct {
	Type             string          `json:"type"`
	ProtocolVersion  string          `json:"protocol_version"`
	DeviceID         string          `json:"device_id"`
	DeviceSecret     string          `json:"device_secret"`
	EdgeVersion      string          `json:"edge_version"`
	BootID           string          `json:"boot_id"`
	Capabilities     []string        `json:"capabilities"`
	Seq              int64           `json:"seq"`
	SampledAt        string          `json:"sampled_at"`
	Metrics          *MetricsPayload `json:"metrics"`
	CommandID        string          `json:"command_id"`
	Status           string          `json:"status"`
	Detail           string          `json:"detail"`
	At               string          `json:"at"`
	TaskID           string          `json:"task_id"`
	SessionID        string          `json:"session_id"`
	TurnID           string          `json:"turn_id"`
	HarnessSessionID string          `json:"harness_session_id"`
	HarnessVersion   string          `json:"harness_version"`
	Cwd              string          `json:"cwd"`
	StreamID         string          `json:"stream_id"`
	Events           []IncomingEvent `json:"events"`
	ExitCode         *int64          `json:"exit_code"`
	Result           *string         `json:"result"`
	Error            *string         `json:"error"`
	Artifact         *wsArtifact     `json:"artifact"`
	HarnessURL       string          `json:"url"`
	DSH              *DSHState       `json:"dsh"`
}

func (h *Hub) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	client := &wsClient{hub: h, conn: conn}
	defer func() {
		h.connMu.Lock()
		if client.deviceID != "" && h.connections[client.deviceID] == client {
			delete(h.connections, client.deviceID)
		}
		h.connMu.Unlock()
		_ = conn.Close()
	}()

	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, raw, err := conn.ReadMessage()
	if err != nil {
		return
	}
	var hello wsMessage
	if err := json.Unmarshal(raw, &hello); err != nil || hello.Type != "hello" {
		client.fail(4400, "hello_required", "首条消息必须为 hello")
		return
	}
	if hello.ProtocolVersion != ProtocolVersion {
		client.fail(4400, "unsupported_protocol", "仅支持协议版本 "+ProtocolVersion)
		return
	}
	device, err := h.getDeviceBySecret(hello.DeviceID, hello.DeviceSecret)
	if err != nil || device == nil {
		client.fail(4401, "auth_failed", "设备不存在或凭据已撤销")
		return
	}
	client.deviceID = device.ID
	client.hub.connMu.Lock()
	if previous := h.connections[device.ID]; previous != nil && previous != client {
		_ = previous.conn.Close()
	}
	h.connections[device.ID] = client
	h.connMu.Unlock()

	seen := nowISO(time.Now())
	var bootID, edgeVersion, capabilities any
	if hello.BootID != "" {
		bootID = hello.BootID
	}
	if hello.EdgeVersion != "" {
		edgeVersion = hello.EdgeVersion
	}
	if hello.Capabilities != nil {
		data, _ := json.Marshal(hello.Capabilities)
		capabilities = string(data)
	}
	h.touchDevice(device.ID, &seen, nil, ptrString(bootID), ptrString(edgeVersion), ptrString(capabilities))
	detail, _ := json.Marshal(map[string]any{"remote": r.RemoteAddr, "edge_version": hello.EdgeVersion})
	detailStr := string(detail)
	h.logEvent("device.connected", &device.ID, &detailStr)

	_ = client.send(map[string]any{
		"type": "hello.ack", "protocol_version": ProtocolVersion, "server_time": nowISO(time.Now()),
		"device_id": device.ID, "heartbeat_interval_ms": 10000, "metrics_interval_ms": 5000,
	})
	h.deliverPendingCommands(device.ID, time.Now())
	// Hub 是 profile 选择的权威来源：Edge 重连后恢复上次选择。
	h.syncDSHSelection(device.ID)
	_ = conn.SetReadDeadline(time.Now().Add(90 * time.Second))

	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			break
		}
		_ = conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		var message wsMessage
		if err := json.Unmarshal(raw, &message); err != nil {
			client.fail(4400, "invalid_json", "消息不是合法 JSON")
			break
		}
		if message.Type == "hello" {
			client.fail(4400, "duplicate_hello", "连接已初始化，不能重复 hello")
			break
		}
		now := time.Now()
		switch message.Type {
		case "heartbeat":
			seen := nowISO(now)
			h.touchDevice(device.ID, &seen, nil, nil, nil, nil)
			_ = client.send(map[string]any{"type": "heartbeat.ack", "seq": message.Seq, "received_at": nowISO(now)})
		case "metrics.batch":
			if message.Metrics == nil {
				client.fail(4400, "invalid_message", "metrics 缺失")
				continue
			}
			if _, err := time.Parse(time.RFC3339Nano, message.SampledAt); err != nil {
				client.fail(4400, "invalid_timestamp", "sampled_at 不是合法时间")
				continue
			}
			received := nowISO(now)
			if err := h.insertMetric(device.ID, message.SampledAt, *message.Metrics, now); err != nil {
				log.Printf("insert metric failed: %v", err)
			}
			h.touchDevice(device.ID, &received, &received, nil, nil, nil)
			_ = client.send(map[string]any{"type": "metrics.ack", "seq": message.Seq, "received_at": received})
		case "harness.update":
			previous, _ := h.getHarnessURL(device.ID)
			if err := h.setHarnessURL(device.ID, message.HarnessURL, now); err != nil {
				log.Printf("保存 Harness URL 失败: %v", err)
			} else if previous != message.HarnessURL {
				// Edge 重新上报地址后，重建旧代理，避免继续使用过期目标。
				h.dropHarnessProxy(device.ID)
			}
			if message.DSH != nil {
				if err := h.setDSHState(device.ID, *message.DSH, now); err != nil {
					log.Printf("保存 DSH 状态失败: %v", err)
				}
			}
		case "command.ack":
			h.handleCommandAck(message.CommandID, message.Status, message.Detail, message.At, now)
		case "session.started":
			h.handleSessionStarted(message.TaskID, message.SessionID, message.HarnessSessionID, message.HarnessVersion, message.At, now)
		case "turn.started":
			h.handleTurnStarted(message.TaskID, message.SessionID, message.TurnID, message.At, now)
		case "event.batch":
			inserted := h.handleEventBatch(device.ID, message.Events, now)
			_ = client.send(map[string]any{
				"type": "event.ack", "stream_id": message.StreamID, "seq": message.Seq,
				"inserted": inserted, "received_at": nowISO(now),
			})
		case "artifact.register":
			ack, err := h.handleArtifactRegister(device.ID, message.Artifact, now)
			if err != nil {
				_ = client.send(map[string]any{"type": "artifact.rejected", "error": err.Error()})
				continue
			}
			_ = client.send(ack)
		case "turn.completed":
			h.handleTurnCompleted(TurnCompletedInput{
				TaskID: message.TaskID, SessionID: message.SessionID, TurnID: message.TurnID,
				Status: message.Status, ExitCode: message.ExitCode, Result: message.Result,
				Error: message.Error, At: message.At,
			}, now)
		default:
			client.fail(4400, "invalid_message", "不支持的消息类型: "+message.Type)
			return
		}
	}
	closedDetail, _ := json.Marshal(map[string]any{"code": 1000, "reason": ""})
	closedStr := string(closedDetail)
	h.logEvent("device.disconnected", &device.ID, &closedStr)
}

func (c *wsClient) fail(code int, reason, message string) {
	_ = c.send(map[string]any{"type": "error", "code": reason, "message": message})
	_ = c.conn.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(code, reason), time.Now().Add(2*time.Second))
	_ = c.conn.Close()
}

func ptrString(value any) *string {
	if value == nil {
		return nil
	}
	if text, ok := value.(string); ok {
		return &text
	}
	return nil
}
