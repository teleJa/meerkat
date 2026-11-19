package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"meerkat/edge/internal/claude"
	"meerkat/edge/internal/protocol"
)

// sessionWorker 串行执行同一 session 的轮次：一次只跑一个 turn，其余排队。
func (m *Manager) sessionWorker(s *sessionState) {
	defer m.wg.Done()
	for {
		m.mu.Lock()
		if m.ctx.Err() != nil || len(s.queue) == 0 {
			s.workerRunning = false
			// 空闲且没有排队轮次时回收 session 状态，避免长期运行累积；
			// 下一次 send_turn 会按指令里的 harness_session_id 重新建立。
			if len(s.queue) == 0 && m.sessions[s.id] == s {
				delete(m.sessions, s.id)
			}
			m.mu.Unlock()
			return
		}
		cmd := s.queue[0]
		s.queue = s.queue[1:]
		ctx, cancel := m.turnContext(cmd)
		s.running = &runningTurn{cmd: cmd, cancel: cancel}
		m.mu.Unlock()

		m.runTurn(ctx, s, cmd)
		cancel()

		m.mu.Lock()
		s.running = nil
		m.mu.Unlock()
	}
}

// turnContext 为一次轮次派生子上下文：进程退出时取消，或到 timeout 自动超时。
func (m *Manager) turnContext(cmd *protocol.Command) (context.Context, context.CancelFunc) {
	base, cancelBase := context.WithCancel(m.ctx)
	timeout := m.timeoutFor(cmd)
	if timeout <= 0 {
		return base, cancelBase
	}
	ctx, cancelTimeout := context.WithTimeout(base, timeout)
	return ctx, func() {
		cancelTimeout()
		cancelBase()
	}
}

func (m *Manager) timeoutFor(cmd *protocol.Command) time.Duration {
	seconds := 0
	if cmd != nil {
		seconds = cmd.Payload.TimeoutSeconds
	}
	if seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	return m.defaultTimeout
}

// runTurn 执行一个 start_turn/send_turn 指令并上报 turn.completed。
func (m *Manager) runTurn(ctx context.Context, s *sessionState, cmd *protocol.Command) {
	startedAt := time.Now()
	if cmd.Expired(startedAt) {
		duration := int64(0)
		reason := "指令已过期，未执行"
		tc := protocol.TurnCompleted{
			Type:       protocol.TypeTurnCompleted,
			TaskID:     cmd.TaskID,
			SessionID:  cmd.SessionID,
			TurnID:     cmd.TurnID,
			Status:     protocol.TurnFailed,
			DurationMS: &duration,
			Error:      &reason,
			At:         protocol.FormatTime(time.Now()),
		}
		m.log.Printf("轮次 %s 跳过：指令已过期", cmd.TurnID)
		m.persistCompletion(cmd, tc)
		return
	}

	m.sendTurnStarted(cmd)

	resumeID := s.harnessSessionID
	if cmd.Payload.HarnessSessionID != nil {
		if hinted := strings.TrimSpace(*cmd.Payload.HarnessSessionID); hinted != "" {
			resumeID = hinted
		}
	}
	if resumeID != "" && s.harnessSessionID == "" {
		s.harnessSessionID = resumeID
	}
	m.sendSessionStartedIfNeeded(s, cmd)

	timeout := m.timeoutFor(cmd)
	batcher := newEventBatcher(m)
	lastText := ""
	onEntry := func(entry claude.Entry) {
		if entry.SessionID != "" && entry.SessionID != s.harnessSessionID {
			s.harnessSessionID = entry.SessionID
		}
		if entry.HarnessVersion != "" {
			s.harnessVersion = entry.HarnessVersion
		}
		if entry.IsInit() {
			m.sendSessionStartedIfNeeded(s, cmd)
		}
		if entry.Type == "assistant" && strings.TrimSpace(entry.Text) != "" {
			lastText = entry.Text
		}
		if events := m.eventsFor(entry, cmd); len(events) > 0 {
			batcher.Add(events)
		}
	}

	request := cmd.Payload.Request
	if suffix, err := materializeAttachments(cmd.Payload.Cwd, cmd.Payload.Attachments); err != nil {
		m.log.Printf("附件落盘失败: %v", err)
	} else if suffix != "" {
		request += suffix
	}
	m.log.Printf("开始轮次 %s（session=%s cwd=%s resume=%q timeout=%s）",
		cmd.TurnID, cmd.SessionID, cmd.Payload.Cwd, resumeID, timeout)
	result, runErr := m.runner.Run(ctx, claude.RunOptions{
		Request:         request,
		ResumeSessionID: resumeID,
		Cwd:             cmd.Payload.Cwd,
	}, onEntry)
	batcher.Close()

	elapsed := time.Since(startedAt)
	tc := buildTurnCompleted(cmd, ctx, result, runErr, lastText, timeout, elapsed)
	m.log.Printf("轮次 %s 结束：status=%s exit=%s duration=%s",
		cmd.TurnID, tc.Status, formatExit(tc.ExitCode), elapsed.Round(time.Millisecond))
	if tc.Status == protocol.TurnSucceeded {
		m.collectArtifacts(cmd, startedAt)
	}
	m.persistCompletion(cmd, tc)
}

// buildTurnCompleted 根据上下文状态与 harness 结果推导 turn.completed。
func buildTurnCompleted(cmd *protocol.Command, ctx context.Context, result claude.Result, runErr error, lastText string, timeout, elapsed time.Duration) protocol.TurnCompleted {
	duration := elapsed.Milliseconds()
	tc := protocol.TurnCompleted{
		Type:       protocol.TypeTurnCompleted,
		TaskID:     cmd.TaskID,
		SessionID:  cmd.SessionID,
		TurnID:     cmd.TurnID,
		DurationMS: &duration,
		At:         protocol.FormatTime(time.Now()),
	}
	if result.ExitCode != nil {
		tc.ExitCode = result.ExitCode
	}
	fail := func(text string) {
		tc.Status = protocol.TurnFailed
		trimmed := truncate(text, maxErrorChars)
		tc.Error = &trimmed
	}
	switch {
	case ctx.Err() == context.Canceled:
		tc.Status = protocol.TurnCancelled
	case ctx.Err() == context.DeadlineExceeded:
		fail(fmt.Sprintf("执行超时（%d 秒）", int(timeout.Seconds())))
	case runErr != nil:
		fail("无法启动 harness：" + runErr.Error())
	case result.IsError || (result.ExitCode != nil && *result.ExitCode != 0):
		fail(failureText(result))
	default:
		tc.Status = protocol.TurnSucceeded
		text := strings.TrimSpace(result.ResultText)
		if text == "" {
			text = strings.TrimSpace(lastText)
		}
		if text != "" {
			trimmed := truncate(text, maxResultChars)
			tc.Result = &trimmed
		}
	}
	return tc
}

// failureText 汇总失败原因：result 事件文本 > subtype > 读取错误 > stderr > 退出码。
func failureText(result claude.Result) string {
	text := strings.TrimSpace(result.ResultText)
	subtype := strings.TrimSpace(result.ResultSubtype)
	switch {
	case subtype != "" && subtype != "success":
		if text != "" {
			return text
		}
		return "执行失败（" + subtype + "）"
	case text != "":
		return text
	case strings.TrimSpace(result.ReadError) != "":
		return "读取 harness 输出失败：" + result.ReadError
	case strings.TrimSpace(result.Stderr) != "":
		return strings.TrimSpace(result.Stderr)
	case result.ExitCode != nil:
		return fmt.Sprintf("harness 退出码 %d", *result.ExitCode)
	default:
		return "harness 执行失败"
	}
}

func formatExit(code *int) string {
	if code == nil {
		return "null"
	}
	return fmt.Sprintf("%d", *code)
}

func (m *Manager) sendTurnStarted(cmd *protocol.Command) {
	msg := protocol.TurnStarted{
		Type:      protocol.TypeTurnStarted,
		CommandID: cmd.CommandID,
		TaskID:    cmd.TaskID,
		SessionID: cmd.SessionID,
		TurnID:    cmd.TurnID,
		At:        protocol.FormatTime(time.Now()),
	}
	if err := m.sendMessage(msg); err != nil {
		m.log.Printf("发送 turn.started 失败（turn=%s）：%v", cmd.TurnID, err)
	}
}

// sendSessionStartedIfNeeded 在拿到 harness session id（或 version 更新）后上报。
func (m *Manager) sendSessionStartedIfNeeded(s *sessionState, cmd *protocol.Command) {
	if strings.TrimSpace(s.harnessSessionID) == "" {
		return
	}
	if s.sessionStarted && s.sentVersion == s.harnessVersion {
		return
	}
	msg := protocol.SessionStarted{
		Type:             protocol.TypeSessionStarted,
		CommandID:        cmd.CommandID,
		TaskID:           cmd.TaskID,
		SessionID:        cmd.SessionID,
		HarnessSessionID: s.harnessSessionID,
		HarnessVersion:   s.harnessVersion,
		Cwd:              cmd.Payload.Cwd,
		At:               protocol.FormatTime(time.Now()),
	}
	if err := m.sendMessage(msg); err != nil {
		m.log.Printf("发送 session.started 失败（session=%s）：%v", s.id, err)
		return
	}
	s.sessionStarted = true
	s.sentVersion = s.harnessVersion
}
