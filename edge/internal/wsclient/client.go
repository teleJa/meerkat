// Package wsclient 实现 Edge 到中心的 WebSocket 长连接：
// hello 鉴权、心跳、指标上报、ack 跟踪、指数退避重连与优雅关闭。
package wsclient

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"meerkat/edge/internal/agent"
	"meerkat/edge/internal/buildinfo"
	"meerkat/edge/internal/harnessweb"
	"meerkat/edge/internal/identity"
	"meerkat/edge/internal/metrics"
	"meerkat/edge/internal/protocol"
)

const (
	// helloTimeout 是发送 hello 后等待 hello.ack 的时间。
	helloTimeout = 10 * time.Second
	// writeTimeout 是单条消息的写超时。
	writeTimeout = 5 * time.Second
	// ackTimeout 超过后只记日志，不影响连接（P1 指标允许有界丢弃）。
	ackTimeout = 30 * time.Second
	// minInterval 防止服务端下发过小的间隔导致空转。
	minInterval = 100 * time.Millisecond

	defaultHeartbeat = 10 * time.Second
	defaultMetrics   = 5 * time.Second
)

// Config 是 WS 客户端配置。
type Config struct {
	Identity        identity.Identity
	OnAuthenticated func()
	// HeartbeatInterval/MetricsInterval 非 0 时覆盖服务端下发的间隔。
	HeartbeatInterval time.Duration
	MetricsInterval   time.Duration
	Logger            *log.Logger
	Dialer            *websocket.Dialer
	BackoffBase       time.Duration
	BackoffMax        time.Duration
	// Collector 仅供测试注入，默认使用 metrics.New()。
	Collector *metrics.Collector
	// Manager 是 P3 托管执行管理器；为 nil 时只做 P1 心跳/指标（向后兼容）。
	Manager *agent.Manager
	// Harness 是本机 DSH 实例管理器；连接建立后上报状态并接收 profile 动作。
	Harness *harnessweb.Manager
}

// Client 维护到中心的连接。Run 返回 nil 表示收到退出信号并已优雅关闭；
// 返回 *FatalError 表示鉴权/协议问题，重试没有意义。
type Client struct {
	cfg       Config
	log       *log.Logger
	collector *metrics.Collector

	streamID   string
	hbSeq      int64
	metricsSeq int64
	pending    map[string]time.Time
	lastIssues string
	backoff    *Backoff

	// writeMu 串行化所有 WebSocket 写（心跳/指标/指令回执/事件重发）。
	writeMu sync.Mutex
}

// FatalError 表示无法通过重连恢复的错误。
type FatalError struct {
	Msg           string
	DeviceRevoked bool
}

func (e *FatalError) Error() string {
	return e.Msg
}

// New 创建客户端。
func New(cfg Config) (*Client, error) {
	if cfg.Logger == nil {
		cfg.Logger = log.Default()
	}
	if cfg.Dialer == nil {
		cfg.Dialer = websocket.DefaultDialer
	}
	collector := cfg.Collector
	if collector == nil {
		collector = metrics.New()
	}
	streamID, err := newStreamID()
	if err != nil {
		return nil, err
	}
	return &Client{
		cfg:       cfg,
		log:       cfg.Logger,
		collector: collector,
		streamID:  streamID,
		pending:   make(map[string]time.Time),
	}, nil
}

// newStreamID 生成进程内稳定的指标流 ID，例如 str_4f2a。
func newStreamID() (string, error) {
	var buf [2]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("生成 stream_id 失败: %w", err)
	}
	return "str_" + hex.EncodeToString(buf[:]), nil
}

// Run 连接中心并在断开后退避重连，直到 ctx 取消。
func (c *Client) Run(ctx context.Context) error {
	c.backoff = NewBackoff(c.cfg.BackoffBase, c.cfg.BackoffMax)
	for {
		if ctx.Err() != nil {
			return nil
		}
		c.log.Printf("正在连接中心 %s（device_id=%s）", c.cfg.Identity.CenterURL, c.cfg.Identity.DeviceID)
		runErr := c.session(ctx)
		if runErr == nil {
			return nil
		}
		if ctx.Err() != nil {
			return nil
		}
		var fatal *FatalError
		if errors.As(runErr, &fatal) {
			return fatal
		}
		wait := c.backoff.Next()
		c.log.Printf("连接中断：%v；%s 后重试", runErr, wait.Round(time.Millisecond))
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return nil
		}
	}
}

// session 是一次完整的连接生命周期；返回 nil 表示优雅关闭。
func (c *Client) session(ctx context.Context) error {
	wsURL, err := protocol.WSURL(c.cfg.Identity.CenterURL)
	if err != nil {
		return &FatalError{Msg: fmt.Sprintf("中心地址无效: %v", err)}
	}
	conn, resp, err := c.cfg.Dialer.DialContext(ctx, wsURL.String(), http.Header{})
	if err != nil {
		if resp != nil {
			return fmt.Errorf("WebSocket 握手被中心拒绝（HTTP %d）: %w", resp.StatusCode, err)
		}
		return fmt.Errorf("连接 %s 失败: %w", wsURL.String(), err)
	}
	defer conn.Close()

	if err := c.sendHello(conn); err != nil {
		return fmt.Errorf("发送 hello 失败: %w", err)
	}
	ack, err := c.waitHelloAck(conn)
	if err != nil {
		return err
	}
	if c.backoff != nil {
		c.backoff.Reset()
	}

	if c.cfg.OnAuthenticated != nil {
		c.cfg.OnAuthenticated()
	}
	heartbeat, metricsInterval := c.resolveIntervals(ack)
	c.log.Printf("已连接中心（heartbeat=%s metrics=%s 协议=%s）", heartbeat, metricsInterval, protocol.Version)
	// 即使 URL 为空也要上报一次，让 Hub 清掉重启前的旧 token，避免打开旧 DSH 页面。
	if harness := c.cfg.Harness; harness != nil {
		harness.SetSender(func(state harnessweb.State) {
			if err := c.writeJSON(conn, harnessUpdateMessage(state)); err != nil {
				c.log.Printf("上报 DSH 状态失败：%v", err)
			}
		})
		defer harness.SetSender(nil)
	} else if err := c.writeJSON(conn, map[string]any{"type": protocol.TypeHarnessUpdate, "url": ""}); err != nil {
		c.log.Printf("上报空 Harness URL 失败：%v", err)
	}

	// 新连接不重发旧连接未确认的 P1 指标（允许有界丢弃），seq 继续递增。
	c.pending = make(map[string]time.Time)

	// P3：挂载发送器并重发 outbox 中未确认的事件/结果（断网不丢）。
	if manager := c.cfg.Manager; manager != nil {
		manager.AttachSender(&connSender{client: c, conn: conn})
		defer manager.DetachSender()
		c.log.Printf("托管执行已启用（stream_id=%s，待确认事件=%d）", manager.StreamID(), manager.OutboxLen())
	}

	readCh := make(chan readResult, 1)
	done := make(chan struct{})
	defer close(done)
	go c.readLoop(conn, readCh, done, c.readTimeout(heartbeat))

	hbTicker := time.NewTicker(heartbeat)
	defer hbTicker.Stop()
	metricsTicker := time.NewTicker(metricsInterval)
	defer metricsTicker.Stop()
	ackTicker := time.NewTicker(5 * time.Second)
	defer ackTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			c.log.Printf("收到退出信号，发送正常关闭帧（1000）")
			c.closeGracefully(conn)
			return nil
		case result := <-readCh:
			if result.err != nil {
				return c.readError(result.err)
			}
			if err := c.handleServerMessage(result.data); err != nil {
				return err
			}
		case <-hbTicker.C:
			if err := c.sendHeartbeat(conn); err != nil {
				return err
			}
		case <-metricsTicker.C:
			if err := c.sendMetrics(ctx, conn); err != nil {
				return err
			}
		case <-ackTicker.C:
			c.checkAckTimeouts()
		}
	}
}

func (c *Client) sendHello(conn *websocket.Conn) error {
	id := c.cfg.Identity
	hello := protocol.Hello{
		Type:            protocol.TypeHello,
		ProtocolVersion: protocol.Version,
		DeviceID:        id.DeviceID,
		DeviceSecret:    id.DeviceSecret,
		EdgeVersion:     buildinfo.Version,
		BootID:          id.Device.BootID,
		Capabilities:    id.Device.Capabilities,
	}
	if err := c.writeJSON(conn, hello); err != nil {
		return err
	}
	return nil
}

// waitHelloAck 读取并校验 hello.ack；错误消息与 4400/4401 关闭码视为致命错误。
func (c *Client) waitHelloAck(conn *websocket.Conn) (*protocol.HelloAck, error) {
	if err := conn.SetReadDeadline(time.Now().Add(helloTimeout)); err != nil {
		return nil, fmt.Errorf("设置读超时失败: %w", err)
	}
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return nil, c.readError(err)
		}
		msg, decodeErr := protocol.DecodeServerMessage(data)
		if decodeErr != nil {
			var unknown *protocol.UnknownTypeError
			if errors.As(decodeErr, &unknown) {
				c.log.Printf("等待 hello.ack 时收到未知消息类型 %q，已忽略", unknown.Type)
				continue
			}
			return nil, fmt.Errorf("解析 hello.ack 失败: %w", decodeErr)
		}
		switch m := msg.(type) {
		case *protocol.HelloAck:
			if m.ProtocolVersion != "" && m.ProtocolVersion != protocol.Version {
				return nil, &FatalError{Msg: fmt.Sprintf("中心协议版本 %s 与 Edge 协议 %s 不一致，请升级 Edge", m.ProtocolVersion, protocol.Version)}
			}
			return m, nil
		case *protocol.Error:
			return nil, c.serverError(m)
		default:
			c.log.Printf("等待 hello.ack 时收到其他消息 %T，已忽略", msg)
		}
	}
}

func (c *Client) resolveIntervals(ack *protocol.HelloAck) (time.Duration, time.Duration) {
	heartbeat := c.cfg.HeartbeatInterval
	if heartbeat <= 0 {
		ms := ack.HeartbeatIntervalMS
		if ms <= 0 {
			ms = c.cfg.Identity.HeartbeatIntervalMS
		}
		heartbeat = clampedInterval(ms, defaultHeartbeat)
	}
	metricsInterval := c.cfg.MetricsInterval
	if metricsInterval <= 0 {
		ms := ack.MetricsIntervalMS
		if ms <= 0 {
			ms = c.cfg.Identity.MetricsIntervalMS
		}
		metricsInterval = clampedInterval(ms, defaultMetrics)
	}
	if heartbeat < minInterval {
		heartbeat = minInterval
	}
	if metricsInterval < minInterval {
		metricsInterval = minInterval
	}
	return heartbeat, metricsInterval
}

func clampedInterval(ms int, fallback time.Duration) time.Duration {
	if ms <= 0 {
		return fallback
	}
	return time.Duration(ms) * time.Millisecond
}

func (c *Client) readTimeout(heartbeat time.Duration) time.Duration {
	timeout := 3 * heartbeat
	if timeout < 30*time.Second {
		timeout = 30 * time.Second
	}
	if timeout > 2*time.Minute {
		timeout = 2 * time.Minute
	}
	return timeout
}

func (c *Client) sendHeartbeat(conn *websocket.Conn) error {
	c.hbSeq++
	msg := protocol.Heartbeat{
		Type:   protocol.TypeHeartbeat,
		Seq:    c.hbSeq,
		SentAt: protocol.FormatTime(time.Now()),
	}
	if err := c.writeJSON(conn, msg); err != nil {
		return fmt.Errorf("发送心跳失败: %w", err)
	}
	c.pending[fmt.Sprintf("heartbeat:%d", c.hbSeq)] = time.Now()
	return nil
}

func (c *Client) sendMetrics(ctx context.Context, conn *websocket.Conn) error {
	sample, issues := c.collector.Collect(ctx)
	c.reportIssues(issues)

	c.metricsSeq++
	batch := protocol.MetricsBatch{
		Type:      protocol.TypeMetricsBatch,
		StreamID:  c.streamID,
		Seq:       c.metricsSeq,
		SampledAt: protocol.FormatTime(time.Now()),
		Metrics:   sample,
	}
	if err := c.writeJSON(conn, batch); err != nil {
		return fmt.Errorf("发送指标失败: %w", err)
	}
	c.pending[fmt.Sprintf("metrics:%d", c.metricsSeq)] = time.Now()
	return nil
}

func (c *Client) writeJSON(conn *websocket.Conn, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("序列化消息失败: %w", err)
	}
	return c.writeRaw(conn, data)
}

// writeRaw 在写锁保护下发送原始 JSON（outbox 记录原样重发）。
func (c *Client) writeRaw(conn *websocket.Conn, data []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := conn.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
		return fmt.Errorf("设置写超时失败: %w", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		return err
	}
	return nil
}

// connSender 把 Manager 的消息写回当前 WebSocket 连接。
type connSender struct {
	client *Client
	conn   *websocket.Conn
}

// Send 发送结构体消息（command.ack / session.started / turn.started）。
func (s *connSender) Send(v any) error {
	return s.client.writeJSON(s.conn, v)
}

// SendRaw 原样发送 outbox 中的事件批次。
func (s *connSender) SendRaw(data []byte) error {
	return s.client.writeRaw(s.conn, data)
}

// handleServerMessage 处理 hello.ack 之后的中心消息；未知类型只记日志。
func (c *Client) handleServerMessage(data []byte) error {
	msg, err := protocol.DecodeServerMessage(data)
	if err != nil {
		var unknown *protocol.UnknownTypeError
		if errors.As(err, &unknown) {
			c.log.Printf("收到未知消息类型 %q，已忽略", unknown.Type)
			return nil
		}
		c.log.Printf("无法解析中心消息（%v），已忽略", err)
		return nil
	}
	switch m := msg.(type) {
	case *protocol.HelloAck:
		c.log.Printf("重复收到 hello.ack，已忽略")
	case *protocol.Ack:
		c.clearPending(m)
	case *protocol.Command:
		if c.cfg.Manager == nil {
			c.log.Printf("收到 command %s 但未启用托管执行，已忽略", m.CommandID)
			return nil
		}
		c.cfg.Manager.HandleCommand(m)
	case *protocol.HarnessProfileCommand:
		if c.cfg.Harness == nil {
			c.log.Printf("收到 harness.profile %s 但未启用 DSH 管理，已忽略", m.Action)
			return nil
		}
		go func(command *protocol.HarnessProfileCommand) {
			if err := c.cfg.Harness.Apply(command.Action, command.Profile, command.Mode); err != nil {
				c.log.Printf("执行 DSH 动作 %s（profile=%s）失败：%v", command.Action, command.Profile, err)
			}
		}(m)
	case *protocol.EventAck:
		if c.cfg.Manager != nil {
			c.cfg.Manager.HandleEventAck(m)
		}
	case *protocol.Error:
		c.log.Printf("中心返回错误 code=%s message=%s", m.Code, m.Message)
		// 中心明确返回 error 后会关闭连接，重连没有意义，直接按致命错误处理。
		return c.serverError(m)
	}
	return nil
}

// harnessUpdateMessage 把 Manager 状态转成协议消息；URL 单独放在顶层。
func harnessUpdateMessage(state harnessweb.State) map[string]any {
	profiles := make([]protocol.HarnessItem, 0, len(state.Profiles))
	for _, profile := range state.Profiles {
		profiles = append(profiles, protocol.HarnessItem{Name: profile.Name, Available: profile.Available, Running: profile.Running})
	}
	return map[string]any{
		"type": protocol.TypeHarnessUpdate,
		"url":  state.URL,
		"dsh": protocol.HarnessProfile{
			Profile:   state.Profile,
			Mode:      state.Mode,
			Status:    state.Status,
			Owner:     state.Owner,
			Error:     state.Error,
			Installed: state.Installed,
			Profiles:  profiles,
			UpdatedAt: state.UpdatedAt,
		},
	}
}

func (c *Client) clearPending(ack *protocol.Ack) {
	switch ack.Type {
	case protocol.TypeHeartbeatAck:
		delete(c.pending, fmt.Sprintf("heartbeat:%d", ack.Seq))
	case protocol.TypeMetricsAck:
		delete(c.pending, fmt.Sprintf("metrics:%d", ack.Seq))
	}
}

// checkAckTimeouts 只记日志并丢弃过期等待项，不中断连接。
func (c *Client) checkAckTimeouts() {
	now := time.Now()
	for key, sentAt := range c.pending {
		if now.Sub(sentAt) > ackTimeout {
			c.log.Printf("等待 ack 超时（%s，已等待 %s），继续运行", key, now.Sub(sentAt).Round(time.Second))
			delete(c.pending, key)
		}
	}
}

// reportIssues 只在采集问题发生变化时写日志，避免每 5s 刷屏。
func (c *Client) reportIssues(err error) {
	current := ""
	if err != nil {
		current = err.Error()
	}
	if current == c.lastIssues {
		return
	}
	switch {
	case current == "" && c.lastIssues != "":
		c.log.Printf("指标采集已恢复正常")
	case c.lastIssues == "":
		c.log.Printf("指标采集异常：%s（对应字段上报为 null）", current)
	default:
		c.log.Printf("指标采集异常变化：%s", current)
	}
	c.lastIssues = current
}

func (c *Client) readError(err error) error {
	switch {
	case websocket.IsCloseError(err, 4401):
		var closed *websocket.CloseError
		if errors.As(err, &closed) && closed.Text == "device_revoked" {
			return &FatalError{Msg: "4401 device_revoked", DeviceRevoked: true}
		}
		return &FatalError{Msg: "中心鉴权失败（关闭码 4401）：device_secret 无效或设备已被撤销，请重新配对"}
	case websocket.IsCloseError(err, 4400):
		return &FatalError{Msg: "中心判定协议错误（关闭码 4400）：请检查 Edge 与中心协议版本"}
	default:
		return fmt.Errorf("WebSocket 读取失败: %w", err)
	}
}

func (c *Client) serverError(m *protocol.Error) error {
	message := m.Message
	if message == "" {
		message = "（中心未提供错误描述）"
	}
	hint := ""
	if m.Code == "auth_failed" {
		hint = "；请重新配对或确认设备未被撤销"
	}
	return &FatalError{Msg: fmt.Sprintf("中心返回错误 %s：%s%s", m.Code, message, hint)}
}

func (c *Client) closeGracefully(conn *websocket.Conn) {
	deadline := time.Now().Add(time.Second)
	c.writeMu.Lock()
	_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), deadline)
	c.writeMu.Unlock()
	_ = conn.Close()
}

type readResult struct {
	data []byte
	err  error
}

func (c *Client) readLoop(conn *websocket.Conn, out chan<- readResult, done <-chan struct{}, timeout time.Duration) {
	for {
		if timeout > 0 {
			_ = conn.SetReadDeadline(time.Now().Add(timeout))
		}
		_, data, err := conn.ReadMessage()
		select {
		case out <- readResult{data: data, err: err}:
		case <-done:
			return
		}
		if err != nil {
			return
		}
	}
}
