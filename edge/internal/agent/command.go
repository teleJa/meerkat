package agent

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"meerkat/edge/internal/dedup"
	"meerkat/edge/internal/protocol"
)

// sessionState 是单个中心 session（sess_*）在本机的执行状态。
// harnessSessionID / harnessVersion / sessionStarted / sentVersion 只由该 session
// 的 worker 协程读写；queue / running / workerRunning 由 m.mu 保护。
type sessionState struct {
	id               string
	taskID           string
	cwd              string
	harnessSessionID string
	harnessVersion   string
	sessionStarted   bool
	sentVersion      string

	queue         []*protocol.Command
	running       *runningTurn
	workerRunning bool
}

// runningTurn 是当前执行中的轮次；cancel 由取消指令调用。
type runningTurn struct {
	cmd    *protocol.Command
	cancel context.CancelFunc
}

// rejectReason 返回拒绝指令的原因；空字符串表示可以执行。
func (m *Manager) rejectReason(cmd *protocol.Command) string {
	if cmd.Expired(time.Now()) {
		return "command_expired"
	}
	switch cmd.Kind {
	case protocol.CommandStartTurn, protocol.CommandSendTurn:
		if strings.TrimSpace(cmd.TaskID) == "" || strings.TrimSpace(cmd.SessionID) == "" || strings.TrimSpace(cmd.TurnID) == "" {
			return "invalid_command"
		}
		if strings.TrimSpace(cmd.Payload.Request) == "" {
			return "request_missing"
		}
		if err := ValidateCwd(m.allowRoot, cmd.Payload.Cwd); err != nil {
			return err.Error()
		}
		if m.queueFull(cmd.SessionID) {
			return "queue_full"
		}
	case protocol.CommandCancel:
		if strings.TrimSpace(cmd.SessionID) == "" {
			return "invalid_command"
		}
	case protocol.CommandInstallDSH:
		if m.dshInstall == nil {
			return "install_unsupported"
		}
	default:
		return "unknown_kind"
	}
	return ""
}

func (m *Manager) queueFull(sessionID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[sessionID]
	if s == nil {
		return false
	}
	return len(s.queue) >= maxQueuePerSession
}

// enqueue 把轮次放入 session 的串行队列，必要时启动 worker。
func (m *Manager) enqueue(cmd *protocol.Command) {
	m.mu.Lock()
	s := m.sessions[cmd.SessionID]
	if s == nil {
		s = &sessionState{id: cmd.SessionID}
		m.sessions[cmd.SessionID] = s
	}
	if cmd.TaskID != "" {
		s.taskID = cmd.TaskID
	}
	if s.cwd == "" {
		s.cwd = cmd.Payload.Cwd
	}
	s.queue = append(s.queue, cmd)
	if !s.workerRunning {
		s.workerRunning = true
		m.wg.Add(1)
		go m.sessionWorker(s)
	}
	m.mu.Unlock()
}

// handleCancel 执行 cancel 指令：终止匹配的运行中轮次，并把排队轮次直接标记为
// cancelled（发送 turn.completed），避免中心侧任务永远停在 cancel_requested。
func (m *Manager) handleCancel(cmd *protocol.Command) {
	m.mu.Lock()
	s := m.sessions[cmd.SessionID]
	var running *runningTurn
	var queued []*protocol.Command
	if s != nil {
		if s.running != nil && (cmd.TurnID == "" || s.running.cmd.TurnID == cmd.TurnID) {
			running = s.running
		}
		if cmd.TurnID == "" {
			queued = append(queued, s.queue...)
			s.queue = nil
		} else {
			keep := make([]*protocol.Command, 0, len(s.queue))
			for _, q := range s.queue {
				if q.TurnID == cmd.TurnID {
					queued = append(queued, q)
				} else {
					keep = append(keep, q)
				}
			}
			s.queue = keep
		}
	}
	m.mu.Unlock()

	if running != nil && running.cancel != nil {
		m.log.Printf("取消指令 %s 终止运行中的轮次 %s", cmd.CommandID, running.cmd.TurnID)
		running.cancel()
	}
	for _, q := range queued {
		m.log.Printf("取消指令 %s 丢弃排队中的轮次 %s", cmd.CommandID, q.TurnID)
		m.completeCancelled(q, "已取消（未开始执行）")
	}
	if running == nil && len(queued) == 0 {
		if cmd.TurnID != "" {
			m.resendCompletionForTurn(cmd.TurnID)
		} else {
			m.resendLatestCompletionForSession(cmd.SessionID)
		}
	}
}

// completeCancelled 为未执行的轮次直接上报 cancelled。
func (m *Manager) completeCancelled(cmd *protocol.Command, reason string) {
	duration := int64(0)
	tc := protocol.TurnCompleted{
		Type:       protocol.TypeTurnCompleted,
		TaskID:     cmd.TaskID,
		SessionID:  cmd.SessionID,
		TurnID:     cmd.TurnID,
		Status:     protocol.TurnCancelled,
		DurationMS: &duration,
		Error:      &reason,
		At:         protocol.FormatTime(time.Now()),
	}
	m.persistCompletion(cmd, tc)
}

// resendCompletionForTurn 在收到重复 cancel/旧 turn 时重发已记录的完成结果。
func (m *Manager) resendCompletionForTurn(turnID string) {
	for _, entry := range m.dedup.All() {
		tc, ok := decodeCompletion(entry)
		if !ok {
			continue
		}
		if tc.TurnID == turnID {
			m.appendTurnCompleted(tc)
			return
		}
	}
}

// resendLatestCompletionForSession 取消一个已经结束的 session 时，重发最近的
// turn.completed，让中心把 cancel_requested 归位。
func (m *Manager) resendLatestCompletionForSession(sessionID string) {
	var latest *protocol.TurnCompleted
	for _, entry := range m.dedup.All() {
		tc, ok := decodeCompletion(entry)
		if !ok || tc.SessionID != sessionID {
			continue
		}
		if latest == nil || tc.At > latest.At {
			copied := tc
			latest = &copied
		}
	}
	if latest != nil {
		m.appendTurnCompleted(*latest)
	}
}

func decodeCompletion(entry dedup.Entry) (protocol.TurnCompleted, bool) {
	var tc protocol.TurnCompleted
	if len(entry.TurnCompleted) == 0 {
		return tc, false
	}
	if err := json.Unmarshal(entry.TurnCompleted, &tc); err != nil {
		return tc, false
	}
	return tc, true
}
