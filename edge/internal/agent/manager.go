package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"meerkat/edge/internal/claude"
	"meerkat/edge/internal/dedup"
	"meerkat/edge/internal/outbox"
	"meerkat/edge/internal/protocol"
)

const (
	// DefaultTimeout 是未在指令中指定 timeout_seconds 时的轮次超时。
	DefaultTimeout = 1800 * time.Second
	// MaxBatchEvents 是一批 event.batch 的上限（中心 zod schema 限制 200）。
	MaxBatchEvents = 200

	defaultBatchSize     = 50
	defaultBatchInterval = 250 * time.Millisecond
	maxQueuePerSession   = 50
	maxCommandDetail     = 2000
	maxResultChars       = 8000
	maxErrorChars        = 4000
	maxAssistantChars    = 8000
	maxToolResultChars   = 4000
	maxToolInputChars    = 4000
	maxTerminalChars     = 12000
)

// Sender 是 Edge → 中心的消息发送接口；wsclient 在连接建立后挂载、断开后卸载。
// SendRaw 用于发送 outbox 中原样保存的 JSON，避免二次序列化导致字段漂移。
type Sender interface {
	Send(v any) error
	SendRaw(data []byte) error
}

// Options 是 Manager 的构造参数。
type Options struct {
	// StateDir 保存 outbox.jsonl、commands.jsonl 等状态文件。
	StateDir string
	// AllowRoot 是 Claude 任务允许的根目录（已经过 ResolveAllowRoot 解析）。
	AllowRoot string
	// HarnessPath 是 harness 可执行文件；为空时使用 claude。
	HarnessPath string
	// HarnessPrefixArgs 位于标准参数之前（config.json harness_command 数组形式）。
	HarnessPrefixArgs []string
	// HarnessExtraArgs 位于标准参数之后（config.json harness_args、--harness-arg）。
	HarnessExtraArgs []string
	// HarnessEnv 是追加/覆盖到 harness 子进程的环境变量。
	HarnessEnv map[string]string
	// DefaultTimeout 覆盖默认 1800s。
	DefaultTimeout time.Duration
	Logger         *log.Logger
	// Runner 仅供测试注入，默认使用 claude.ExecRunner。
	Runner claude.Runner
	// DSHInstall 由 Edge 主进程注入：安装 DSH 并启动 dsh web，返回访问 URL。
	DSHInstall func(ctx context.Context) (string, error)
	// EventID 仅供测试注入。
	EventID func() string
	// BatchSize / BatchInterval 控制 event.batch 的攒批。
	BatchSize     int
	BatchInterval time.Duration
}

// Manager 负责指令去重、会话串行执行、事件 outbox 与结果上报。
type Manager struct {
	opts           Options
	log            *log.Logger
	outbox         *outbox.Outbox
	dedup          *dedup.Store
	runner         claude.Runner
	dshInstall     func(ctx context.Context) (string, error)
	allowRoot      string
	eventID        func() string
	batchSize      int
	batchInterval  time.Duration
	defaultTimeout time.Duration

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu       sync.Mutex
	sessions map[string]*sessionState

	senderMu sync.Mutex
	sender   Sender

	flushCh chan struct{}
	flushMu sync.Mutex

	sendErrMu     sync.Mutex
	lastSendErr   string
	lastSendErrAt time.Time
}

// New 打开状态文件并启动 outbox 发送协程。
func New(opts Options) (*Manager, error) {
	if strings.TrimSpace(opts.StateDir) == "" {
		return nil, errors.New("状态目录为空")
	}
	allowRoot, err := resolveAllowRootAbs(opts.AllowRoot)
	if err != nil {
		return nil, err
	}
	if err := ValidateAllowRoot(allowRoot); err != nil {
		return nil, err
	}

	logger := opts.Logger
	if logger == nil {
		logger = log.Default()
	}
	ob, err := outbox.Open(opts.StateDir)
	if err != nil {
		return nil, err
	}
	dd, err := dedup.Open(opts.StateDir)
	if err != nil {
		ob.Close()
		return nil, err
	}
	runner := opts.Runner
	if runner == nil {
		execRunner := claude.NewExecRunner(opts.HarnessPath, opts.HarnessPrefixArgs, opts.HarnessExtraArgs)
		execRunner.Env = opts.HarnessEnv
		runner = execRunner
	}
	eventID := opts.EventID
	if eventID == nil {
		eventID = outbox.NewEventID
	}
	batchSize := opts.BatchSize
	if batchSize <= 0 || batchSize > MaxBatchEvents {
		batchSize = defaultBatchSize
	}
	batchInterval := opts.BatchInterval
	if batchInterval <= 0 {
		batchInterval = defaultBatchInterval
	}
	timeout := opts.DefaultTimeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}

	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{
		opts:           opts,
		log:            logger,
		outbox:         ob,
		dedup:          dd,
		runner:         runner,
		dshInstall:     opts.DSHInstall,
		allowRoot:      allowRoot,
		eventID:        eventID,
		batchSize:      batchSize,
		batchInterval:  batchInterval,
		defaultTimeout: timeout,
		ctx:            ctx,
		cancel:         cancel,
		sessions:       make(map[string]*sessionState),
		flushCh:        make(chan struct{}, 1),
	}
	if corrupted := ob.CorruptedLines(); corrupted > 0 {
		m.log.Printf("outbox 中有 %d 行损坏记录已跳过（可能是上次异常退出）", corrupted)
	}
	go m.flushLoop()
	return m, nil
}

// resolveAllowRootAbs 把 allow root 转成绝对路径（Manager 内部使用）。
func resolveAllowRootAbs(root string) (string, error) {
	trimmed := strings.TrimSpace(root)
	if trimmed == "" {
		return "", errors.New("allow root 为空")
	}
	abs, err := filepath.Abs(trimmed)
	if err != nil {
		return "", fmt.Errorf("解析 allow root %q 失败: %w", trimmed, err)
	}
	return abs, nil
}

// AllowRoot 返回解析后的允许根目录。
func (m *Manager) AllowRoot() string { return m.allowRoot }

// StreamID 返回事件流 ID。
func (m *Manager) StreamID() string { return m.outbox.StreamID() }

// OutboxLen 返回尚未确认的 outbox 记录数。
func (m *Manager) OutboxLen() int { return m.outbox.Len() }

// AttachSender 在新连接建立后挂载发送器，并触发未确认事件重发。
func (m *Manager) AttachSender(sender Sender) {
	m.senderMu.Lock()
	m.sender = sender
	m.senderMu.Unlock()
	if sender != nil {
		m.outbox.ResetSent()
		m.signalFlush()
	}
}

// DetachSender 在连接断开后卸载发送器；outbox 保留记录等待重连重发。
func (m *Manager) DetachSender() {
	m.senderMu.Lock()
	m.sender = nil
	m.senderMu.Unlock()
}

// HandleCommand 处理中心下发的指令；在 WS 读循环中调用，必须快速返回。
func (m *Manager) HandleCommand(cmd *protocol.Command) {
	if cmd == nil {
		return
	}
	if strings.TrimSpace(cmd.CommandID) == "" {
		m.log.Printf("忽略缺少 command_id 的指令（kind=%s）", cmd.Kind)
		return
	}
	if entry, ok := m.dedup.Get(cmd.CommandID); ok {
		m.log.Printf("指令 %s 已处理过（status=%s），只重发 ack，不重复执行", cmd.CommandID, entry.Status)
		status := entry.Status
		if status == "" {
			status = protocol.CommandAckAccepted
		}
		m.sendAck(cmd.CommandID, status, entry.Detail)
		if len(entry.TurnCompleted) > 0 {
			var tc protocol.TurnCompleted
			if err := json.Unmarshal(entry.TurnCompleted, &tc); err == nil {
				m.appendTurnCompleted(tc)
			}
		}
		return
	}
	if reason := m.rejectReason(cmd); reason != "" {
		m.log.Printf("拒绝指令 %s（kind=%s）：%s", cmd.CommandID, cmd.Kind, reason)
		m.recordCommand(cmd.CommandID, protocol.CommandAckRejected, reason)
		m.sendAck(cmd.CommandID, protocol.CommandAckRejected, reason)
		return
	}

	m.recordCommand(cmd.CommandID, protocol.CommandAckAccepted, "")
	m.sendAck(cmd.CommandID, protocol.CommandAckAccepted, "")
	switch cmd.Kind {
	case protocol.CommandCancel:
		m.handleCancel(cmd)
	case protocol.CommandInstallDSH:
		m.handleDSHInstall(cmd)
	default:
		m.enqueue(cmd)
	}
}

// HandleEventAck 处理中心的累计确认，截断 outbox。
func (m *Manager) HandleEventAck(ack *protocol.EventAck) {
	if ack == nil {
		return
	}
	if _, err := m.outbox.Ack(ack.StreamID, ack.Seq); err != nil {
		m.log.Printf("处理 event.ack 失败（stream=%s seq=%d）：%v", ack.StreamID, ack.Seq, err)
	}
}

// FlushOutbox 按顺序发送全部未确认记录；发送失败保留记录等待重连。
func (m *Manager) FlushOutbox() {
	m.flushMu.Lock()
	defer m.flushMu.Unlock()
	for _, rec := range m.outbox.Unsent() {
		if err := m.sendRaw(rec.Message); err != nil {
			m.reportSendError(err)
			return
		}
		m.outbox.MarkSent(rec.ID)
	}
	m.clearSendError()
}

// Close 取消所有执行中的轮次并等待协程退出。
func (m *Manager) Close() {
	m.cancel()
	m.mu.Lock()
	for _, s := range m.sessions {
		if s.running != nil && s.running.cancel != nil {
			s.running.cancel()
		}
	}
	m.mu.Unlock()

	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		m.log.Printf("等待执行中的 Claude 轮次退出超时（10s），强制关闭")
	}
	if err := m.outbox.Close(); err != nil {
		m.log.Printf("关闭 outbox 失败：%v", err)
	}
}

func (m *Manager) flushLoop() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-m.flushCh:
		case <-ticker.C:
		}
		m.FlushOutbox()
	}
}

func (m *Manager) signalFlush() {
	select {
	case m.flushCh <- struct{}{}:
	default:
	}
}

func (m *Manager) sendMessage(v any) error {
	m.senderMu.Lock()
	sender := m.sender
	m.senderMu.Unlock()
	if sender == nil {
		return errors.New("尚未连接中心")
	}
	return sender.Send(v)
}

func (m *Manager) sendRaw(data []byte) error {
	m.senderMu.Lock()
	sender := m.sender
	m.senderMu.Unlock()
	if sender == nil {
		return errors.New("尚未连接中心")
	}
	return sender.SendRaw(data)
}

func (m *Manager) reportSendError(err error) {
	now := time.Now()
	text := err.Error()
	m.sendErrMu.Lock()
	defer m.sendErrMu.Unlock()
	if text == m.lastSendErr && now.Sub(m.lastSendErrAt) < 30*time.Second {
		return
	}
	m.lastSendErr = text
	m.lastSendErrAt = now
	m.log.Printf("发送 outbox 记录失败（保留待重连重发）：%v", err)
}

func (m *Manager) clearSendError() {
	m.sendErrMu.Lock()
	m.lastSendErr = ""
	m.sendErrMu.Unlock()
}

func (m *Manager) sendAck(commandID, status, detail string) {
	msg := protocol.CommandAck{
		Type:      protocol.TypeCommandAck,
		CommandID: commandID,
		Status:    status,
		Detail:    truncate(detail, maxCommandDetail),
		At:        protocol.FormatTime(time.Now()),
	}
	if err := m.sendMessage(msg); err != nil {
		m.log.Printf("发送 command.ack 失败（重连后由重复投递补发）：%v", err)
	}
}

// recordCommand 持久记录指令的 ACK 状态；写入失败只记日志，不阻塞执行。
func (m *Manager) recordCommand(commandID, status, detail string) {
	entry := dedup.Entry{
		CommandID: commandID,
		Status:    status,
		Detail:    truncate(detail, maxCommandDetail),
		At:        protocol.FormatTime(time.Now()),
	}
	if err := m.dedup.Record(entry); err != nil {
		m.log.Printf("持久化指令去重记录失败（%s）：%v", commandID, err)
	}
}

func (m *Manager) persistCompletion(cmd *protocol.Command, tc protocol.TurnCompleted) {
	if err := m.outbox.AppendTurnCompleted(tc); err != nil {
		m.log.Printf("写入 turn.completed 到 outbox 失败（turn=%s）：%v", tc.TurnID, err)
	}
	if err := m.dedup.SetCompletion(cmd.CommandID, tc); err != nil {
		m.log.Printf("持久化 turn.completed 失败（command=%s）：%v", cmd.CommandID, err)
	}
	m.signalFlush()
}

func (m *Manager) appendTurnCompleted(tc protocol.TurnCompleted) {
	if tc.Type == "" {
		tc.Type = protocol.TypeTurnCompleted
	}
	if err := m.outbox.AppendTurnCompleted(tc); err != nil {
		m.log.Printf("重发 turn.completed 失败（turn=%s）：%v", tc.TurnID, err)
		return
	}
	m.signalFlush()
}

func (m *Manager) newEventID() string { return m.eventID() }

// truncate 按 UTF-16 码元数量截断（与中心 zod String.max 口径一致），并加省略号。
func truncate(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	units := 0
	for i, r := range s {
		width := 1
		if r > 0xFFFF {
			width = 2
		}
		if units+width > limit {
			return s[:i] + "…"
		}
		units += width
	}
	return s
}
func (m *Manager) handleDSHInstall(cmd *protocol.Command) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		url, err := m.dshInstall(ctx)
		if err != nil {
			m.log.Printf("安装 DSH 失败：%v", err)
			_ = m.sendMessage(protocol.CommandAck{
				Type: protocol.TypeCommandAck, CommandID: cmd.CommandID,
				Status: "failed", Detail: err.Error(), At: protocol.FormatTime(time.Now()),
			})
			return
		}
		_ = m.sendMessage(protocol.CommandAck{
			Type: protocol.TypeCommandAck, CommandID: cmd.CommandID,
			Status: "done", At: protocol.FormatTime(time.Now()),
		})
		if strings.TrimSpace(url) != "" {
			_ = m.sendMessage(map[string]any{"type": "harness.update", "url": url})
		}
	}()
}
