package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"meerkat/edge/internal/claude"
	"meerkat/edge/internal/protocol"
)

func intPtr(v int) *int { return &v }

func discardLogger() *log.Logger { return log.New(io.Discard, "", 0) }

type captureSender struct {
	mu   sync.Mutex
	msgs []any
	raw  [][]byte
}

func (s *captureSender) Send(v any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.msgs = append(s.msgs, v)
	return nil
}

func (s *captureSender) SendRaw(data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	copied := append([]byte(nil), data...)
	s.raw = append(s.raw, copied)
	return nil
}

func (s *captureSender) byType(typeName string) []any {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []any
	for _, msg := range s.msgs {
		if messageType(msg) == typeName {
			out = append(out, msg)
		}
	}
	return out
}

func (s *captureSender) countType(typeName string) int {
	return len(s.byType(typeName))
}

func (s *captureSender) rawBatches() []protocol.EventBatch {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []protocol.EventBatch
	for _, data := range s.raw {
		var batch protocol.EventBatch
		if err := json.Unmarshal(data, &batch); err == nil && batch.Type == protocol.TypeEventBatch {
			out = append(out, batch)
		}
	}
	return out
}

func (s *captureSender) rawTurnCompleted() []protocol.TurnCompleted {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []protocol.TurnCompleted
	for _, data := range s.raw {
		var tc protocol.TurnCompleted
		if err := json.Unmarshal(data, &tc); err == nil && tc.Type == protocol.TypeTurnCompleted {
			out = append(out, tc)
		}
	}
	return out
}

func messageType(v any) string {
	switch m := v.(type) {
	case protocol.CommandAck:
		return m.Type
	case *protocol.CommandAck:
		return m.Type
	case protocol.SessionStarted:
		return m.Type
	case *protocol.SessionStarted:
		return m.Type
	case protocol.TurnStarted:
		return m.Type
	case *protocol.TurnStarted:
		return m.Type
	case protocol.EventBatch:
		return m.Type
	case *protocol.EventBatch:
		return m.Type
	case protocol.TurnCompleted:
		return m.Type
	case *protocol.TurnCompleted:
		return m.Type
	default:
		return ""
	}
}

func waitFor(t *testing.T, timeout time.Duration, desc string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待超时：%s", desc)
}

type fakeRunner struct {
	mu        sync.Mutex
	calls     []claude.RunOptions
	entries   []claude.Entry
	result    claude.Result
	err       error
	waitCtx   bool
	gate      chan struct{}
	active    int
	maxActive int
}

func (f *fakeRunner) Run(ctx context.Context, opts claude.RunOptions, onEntry func(claude.Entry)) (claude.Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, opts)
	f.active++
	if f.active > f.maxActive {
		f.maxActive = f.active
	}
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.active--
		f.mu.Unlock()
	}()

	for _, entry := range f.entries {
		onEntry(entry)
	}
	if f.waitCtx {
		<-ctx.Done()
		return claude.Result{}, nil
	}
	if f.gate != nil {
		select {
		case <-f.gate:
		case <-ctx.Done():
			return claude.Result{}, nil
		}
	}
	return f.result, f.err
}

func (f *fakeRunner) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeRunner) callAt(i int) claude.RunOptions {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[i]
}

func (f *fakeRunner) maxConcurrent() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.maxActive
}

func successRunner(sessionID string) *fakeRunner {
	return &fakeRunner{
		entries: []claude.Entry{
			{Type: "system", Subtype: "init", SessionID: sessionID, HarnessVersion: "2.1.304", Model: "claude-test"},
			{Type: "assistant", Text: "OK"},
			{Type: "result", Subtype: "success", ResultText: "OK", DurationMS: 5},
		},
		result: claude.Result{
			SessionID:      sessionID,
			HarnessVersion: "2.1.304",
			ExitCode:       intPtr(0),
			ResultText:     "OK",
			DurationMS:     5,
		},
	}
}

func newTestManager(t *testing.T, runner claude.Runner) (*Manager, *captureSender, string) {
	t.Helper()
	root := t.TempDir()
	stateDir := t.TempDir()
	m, err := New(Options{
		StateDir:      stateDir,
		AllowRoot:     root,
		Logger:        discardLogger(),
		Runner:        runner,
		BatchInterval: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(m.Close)
	sender := &captureSender{}
	m.AttachSender(sender)
	return m, sender, root
}

func mustMkdir(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", path, err)
	}
	return path
}

func startCommand(commandID, turnID, cwd string) *protocol.Command {
	now := time.Now()
	return &protocol.Command{
		Type:      protocol.TypeCommand,
		CommandID: commandID,
		Kind:      protocol.CommandStartTurn,
		TaskID:    "tsk_1",
		SessionID: "sess_1",
		TurnID:    turnID,
		Payload: protocol.CommandPayload{
			Request:        "请回复 OK",
			Cwd:            cwd,
			RunProfile:     "default",
			TimeoutSeconds: 5,
		},
		CreatedAt: protocol.FormatTime(now),
		ExpiresAt: protocol.FormatTime(now.Add(10 * time.Minute)),
	}
}

func cancelCommand(commandID, turnID string) *protocol.Command {
	now := time.Now()
	return &protocol.Command{
		Type:      protocol.TypeCommand,
		CommandID: commandID,
		Kind:      protocol.CommandCancel,
		TaskID:    "tsk_1",
		SessionID: "sess_1",
		TurnID:    turnID,
		ExpiresAt: protocol.FormatTime(now.Add(10 * time.Minute)),
	}
}

func TestValidateCwd(t *testing.T) {
	root := t.TempDir()
	inside := mustMkdir(t, filepath.Join(root, "proj"))
	outside := t.TempDir()

	if err := ValidateCwd(root, inside); err != nil {
		t.Fatalf("允许目录内应通过: %v", err)
	}
	if err := ValidateCwd(root, root); err != nil {
		t.Fatalf("allow root 本身应通过: %v", err)
	}
	if err := ValidateCwd(root, outside); !errors.Is(err, ErrCwdNotAllowed) {
		t.Fatalf("根目录外应返回 cwd_not_allowed，实际 %v", err)
	}
	if err := ValidateCwd(root, ""); !errors.Is(err, ErrCwdMissing) {
		t.Fatalf("空 cwd 应返回 cwd_missing，实际 %v", err)
	}
	if err := ValidateCwd(root, filepath.Join(root, "missing")); !errors.Is(err, ErrCwdNotFound) {
		t.Fatalf("不存在的 cwd 应返回 cwd_not_found，实际 %v", err)
	}
	if runtime.GOOS != "windows" {
		link := filepath.Join(root, "escape")
		if err := os.Symlink(outside, link); err == nil {
			if err := ValidateCwd(root, link); !errors.Is(err, ErrCwdNotAllowed) {
				t.Fatalf("软链接逃逸应返回 cwd_not_allowed，实际 %v", err)
			}
		}
	}
}

func TestResolveAllowRoot(t *testing.T) {
	root, err := ResolveAllowRoot("/tmp/flag-root", "/tmp/config-root")
	if err != nil || root != "/tmp/flag-root" {
		t.Fatalf("flag 优先失败: %q %v", root, err)
	}
	root, err = ResolveAllowRoot("", "/tmp/config-root")
	if err != nil || root != "/tmp/config-root" {
		t.Fatalf("config 优先失败: %q %v", root, err)
	}
	home, err := os.UserHomeDir()
	if err == nil && home != "" {
		root, err = ResolveAllowRoot("", "")
		if err != nil || root != home {
			t.Fatalf("默认 home = %q, %v", root, err)
		}
	}
}

func TestLoadFileConfig(t *testing.T) {
	dir := t.TempDir()
	cfg, err := LoadFileConfig(dir)
	if err != nil || cfg.AllowRoot != "" {
		t.Fatalf("缺失 config.json 应返回零值: %+v %v", cfg, err)
	}

	writeConfig := func(content string) {
		t.Helper()
		if err := os.WriteFile(ConfigPath(dir), []byte(content), 0o600); err != nil {
			t.Fatalf("写 config.json: %v", err)
		}
	}
	writeConfig(`{"allow_root":"/tmp/root","harness_command":"/tmp/stub","harness_args":["--fixture","/tmp/f"]}`)
	cfg, err = LoadFileConfig(dir)
	if err != nil {
		t.Fatalf("LoadFileConfig: %v", err)
	}
	if cfg.AllowRoot != "/tmp/root" || cfg.HarnessCommand.Executable != "/tmp/stub" ||
		len(cfg.HarnessCommand.PrefixArgs) != 0 || len(cfg.HarnessArgs) != 2 {
		t.Fatalf("config 解析错误: %+v", cfg)
	}

	writeConfig(`{"harness_command":["/tmp/stub","--mode","stub"]}`)
	cfg, err = LoadFileConfig(dir)
	if err != nil {
		t.Fatalf("数组形式 harness_command 解析失败: %v", err)
	}
	if cfg.HarnessCommand.Executable != "/tmp/stub" || strings.Join(cfg.HarnessCommand.PrefixArgs, " ") != "--mode stub" {
		t.Fatalf("数组形式解析错误: %+v", cfg.HarnessCommand)
	}

	writeConfig(`{"harness_command":123}`)
	if _, err := LoadFileConfig(dir); err == nil {
		t.Fatal("非法 harness_command 应返回错误")
	}
}

func TestHandleCommandRunsTurn(t *testing.T) {
	runner := successRunner("claude-s1")
	m, sender, root := newTestManager(t, runner)
	cwd := mustMkdir(t, filepath.Join(root, "proj"))

	cmd := startCommand("cmd_1", "turn_1", cwd)
	m.HandleCommand(cmd)

	waitFor(t, 5*time.Second, "turn.completed", func() bool {
		return len(sender.rawTurnCompleted()) >= 1
	})

	acks := sender.byType(protocol.TypeCommandAck)
	if len(acks) == 0 || acks[0].(protocol.CommandAck).Status != protocol.CommandAckAccepted {
		t.Fatalf("缺少 accepted 的 command.ack: %+v", acks)
	}
	starts := sender.byType(protocol.TypeSessionStarted)
	if len(starts) == 0 {
		t.Fatal("缺少 session.started")
	}
	started := starts[0].(protocol.SessionStarted)
	if started.HarnessSessionID != "claude-s1" || started.HarnessVersion != "2.1.304" || started.Cwd != cwd {
		t.Fatalf("session.started = %+v", started)
	}
	if sender.countType(protocol.TypeTurnStarted) != 1 {
		t.Fatalf("turn.started 数量 = %d", sender.countType(protocol.TypeTurnStarted))
	}

	batches := sender.rawBatches()
	if len(batches) == 0 {
		t.Fatal("缺少 event.batch")
	}
	types := make(map[string]bool)
	for _, batch := range batches {
		if batch.StreamID != m.StreamID() {
			t.Fatalf("event.batch stream_id = %q, want %q", batch.StreamID, m.StreamID())
		}
		for _, event := range batch.Events {
			types[event.Type] = true
			if event.TaskID != "tsk_1" || event.SessionID != "sess_1" || event.TurnID != "turn_1" || event.EventID == "" || event.At == "" {
				t.Fatalf("事件字段不完整: %+v", event)
			}
		}
	}
	for _, want := range []string{protocol.EventSystemInit, protocol.EventAssistantMessage, protocol.EventResult} {
		if !types[want] {
			t.Fatalf("缺少事件类型 %s（已有 %v）", want, types)
		}
	}

	tc := sender.rawTurnCompleted()[0]
	if tc.Status != protocol.TurnSucceeded || tc.Result == nil || *tc.Result != "OK" {
		t.Fatalf("turn.completed = %+v", tc)
	}
	if tc.ExitCode == nil || *tc.ExitCode != 0 || tc.DurationMS == nil {
		t.Fatalf("turn.completed exit/duration = %+v", tc)
	}

	// event.ack 截断 outbox：最后一批确认后只剩 turn.completed 记录。
	lastSeq := batches[len(batches)-1].Seq
	m.HandleEventAck(&protocol.EventAck{Type: protocol.TypeEventAck, StreamID: m.StreamID(), Seq: lastSeq})
	if m.OutboxLen() != 1 {
		t.Fatalf("event.ack 后 outbox 记录 = %d, want 1（仅 turn.completed）", m.OutboxLen())
	}

	// 去重记录应持久存在，重复投递不重复执行，并重发 ack 与完成结果。
	if _, ok := m.dedup.Get("cmd_1"); !ok {
		t.Fatal("command_id 未持久记录")
	}
	before := runner.callCount()
	m.HandleCommand(cmd)
	waitFor(t, 5*time.Second, "重复指令重发完成结果", func() bool {
		return len(sender.rawTurnCompleted()) >= 2 && sender.countType(protocol.TypeCommandAck) >= 2
	})
	if runner.callCount() != before {
		t.Fatalf("重复指令不应重复执行，调用次数 %d -> %d", before, runner.callCount())
	}
}

func TestSessionSerialExecution(t *testing.T) {
	gate := make(chan struct{}, 2)
	runner := &fakeRunner{
		gate:    gate,
		entries: []claude.Entry{{Type: "assistant", Text: "ok"}},
		result:  claude.Result{ExitCode: intPtr(0), ResultText: "ok"},
	}
	m, sender, root := newTestManager(t, runner)
	cwd := mustMkdir(t, filepath.Join(root, "proj"))

	m.HandleCommand(startCommand("cmd_1", "turn_1", cwd))
	m.HandleCommand(startCommand("cmd_2", "turn_2", cwd))

	waitFor(t, 5*time.Second, "第一个 turn.started", func() bool {
		return sender.countType(protocol.TypeTurnStarted) >= 1
	})
	time.Sleep(100 * time.Millisecond)
	if got := sender.countType(protocol.TypeTurnStarted); got != 1 {
		t.Fatalf("同一 session 不应并行执行，turn.started 数量 = %d", got)
	}
	if got := runner.maxConcurrent(); got != 1 {
		t.Fatalf("runner 并发数 = %d, want 1", got)
	}

	gate <- struct{}{}
	waitFor(t, 5*time.Second, "第二个 turn.started", func() bool {
		return sender.countType(protocol.TypeTurnStarted) >= 2
	})
	gate <- struct{}{}
	waitFor(t, 5*time.Second, "两个 turn.completed", func() bool {
		return len(sender.rawTurnCompleted()) >= 2
	})
	if runner.callCount() != 2 {
		t.Fatalf("调用次数 = %d, want 2", runner.callCount())
	}
	if runner.maxConcurrent() != 1 {
		t.Fatalf("最大并发 = %d, want 1", runner.maxConcurrent())
	}
}

func TestCancelRunningTurn(t *testing.T) {
	runner := &fakeRunner{
		waitCtx: true,
		entries: []claude.Entry{{Type: "system", Subtype: "init", SessionID: "claude-cancel"}},
	}
	m, sender, root := newTestManager(t, runner)
	cwd := mustMkdir(t, filepath.Join(root, "proj"))

	m.HandleCommand(startCommand("cmd_1", "turn_1", cwd))
	waitFor(t, 5*time.Second, "turn.started", func() bool {
		return sender.countType(protocol.TypeTurnStarted) >= 1
	})
	m.HandleCommand(cancelCommand("cmd_cancel", ""))
	waitFor(t, 5*time.Second, "cancelled turn.completed", func() bool {
		return len(sender.rawTurnCompleted()) >= 1
	})
	tc := sender.rawTurnCompleted()[0]
	if tc.Status != protocol.TurnCancelled {
		t.Fatalf("turn.completed status = %s, want cancelled", tc.Status)
	}
	if runner.callCount() != 1 {
		t.Fatalf("取消不应重跑，调用次数 = %d", runner.callCount())
	}
	acks := sender.byType(protocol.TypeCommandAck)
	if len(acks) < 2 || acks[1].(protocol.CommandAck).Status != protocol.CommandAckAccepted {
		t.Fatalf("取消指令应 ack accepted: %+v", acks)
	}
}

func TestCancelQueuedTurnOnly(t *testing.T) {
	gate := make(chan struct{}, 2)
	runner := &fakeRunner{
		gate:    gate,
		entries: []claude.Entry{{Type: "assistant", Text: "ok"}},
		result:  claude.Result{ExitCode: intPtr(0), ResultText: "ok"},
	}
	m, sender, root := newTestManager(t, runner)
	cwd := mustMkdir(t, filepath.Join(root, "proj"))

	m.HandleCommand(startCommand("cmd_1", "turn_1", cwd))
	m.HandleCommand(startCommand("cmd_2", "turn_2", cwd))
	waitFor(t, 5*time.Second, "第一个 turn.started", func() bool {
		return sender.countType(protocol.TypeTurnStarted) >= 1
	})
	m.HandleCommand(cancelCommand("cmd_cancel", "turn_2"))
	waitFor(t, 5*time.Second, "排队轮次 cancelled", func() bool {
		completed := sender.rawTurnCompleted()
		return len(completed) >= 1 && completed[0].TurnID == "turn_2" && completed[0].Status == protocol.TurnCancelled
	})
	if runner.callCount() != 1 {
		t.Fatalf("排队轮次不应执行，调用次数 = %d", runner.callCount())
	}
	gate <- struct{}{}
	waitFor(t, 5*time.Second, "运行中轮次完成", func() bool {
		completed := sender.rawTurnCompleted()
		return len(completed) >= 2 && completed[1].TurnID == "turn_1" && completed[1].Status == protocol.TurnSucceeded
	})
}

func TestTurnTimeout(t *testing.T) {
	runner := &fakeRunner{
		waitCtx: true,
		entries: []claude.Entry{{Type: "assistant", Text: "slow"}},
	}
	m, sender, root := newTestManager(t, runner)
	cwd := mustMkdir(t, filepath.Join(root, "proj"))
	cmd := startCommand("cmd_1", "turn_1", cwd)
	cmd.Payload.TimeoutSeconds = 1
	m.HandleCommand(cmd)

	waitFor(t, 6*time.Second, "超时 turn.completed", func() bool {
		return len(sender.rawTurnCompleted()) >= 1
	})
	tc := sender.rawTurnCompleted()[0]
	if tc.Status != protocol.TurnFailed || tc.Error == nil || !strings.Contains(*tc.Error, "超时") {
		t.Fatalf("超时结果 = %+v", tc)
	}
}

func TestRejectWrongCwdAndExpired(t *testing.T) {
	runner := successRunner("claude-none")
	m, sender, root := newTestManager(t, runner)
	inside := mustMkdir(t, filepath.Join(root, "proj"))
	outside := t.TempDir()

	badCwd := startCommand("cmd_bad_cwd", "turn_bad", outside)
	m.HandleCommand(badCwd)
	waitFor(t, 5*time.Second, "拒绝 ack", func() bool {
		return sender.countType(protocol.TypeCommandAck) >= 1
	})
	ack := sender.byType(protocol.TypeCommandAck)[0].(protocol.CommandAck)
	if ack.Status != protocol.CommandAckRejected || ack.Detail != "cwd_not_allowed" {
		t.Fatalf("cwd 拒绝 ack = %+v", ack)
	}

	expired := startCommand("cmd_expired", "turn_expired", inside)
	expired.ExpiresAt = protocol.FormatTime(time.Now().Add(-time.Minute))
	m.HandleCommand(expired)
	waitFor(t, 5*time.Second, "过期拒绝 ack", func() bool {
		return sender.countType(protocol.TypeCommandAck) >= 2
	})
	ack = sender.byType(protocol.TypeCommandAck)[1].(protocol.CommandAck)
	if ack.Status != protocol.CommandAckRejected || ack.Detail != "command_expired" {
		t.Fatalf("过期拒绝 ack = %+v", ack)
	}

	if runner.callCount() != 0 {
		t.Fatalf("被拒绝指令不应执行，调用次数 = %d", runner.callCount())
	}
	if sender.countType(protocol.TypeTurnStarted) != 0 {
		t.Fatal("被拒绝指令不应发送 turn.started")
	}

	// 重复投递被拒绝的指令：只重发 ack，不再执行。
	m.HandleCommand(badCwd)
	waitFor(t, 5*time.Second, "重复拒绝 ack", func() bool {
		return sender.countType(protocol.TypeCommandAck) >= 3
	})
	last := sender.byType(protocol.TypeCommandAck)[2].(protocol.CommandAck)
	if last.Status != protocol.CommandAckRejected || last.Detail != "cwd_not_allowed" {
		t.Fatalf("重复拒绝 ack = %+v", last)
	}
	if runner.callCount() != 0 {
		t.Fatalf("重复拒绝指令不应执行，调用次数 = %d", runner.callCount())
	}
}

func TestSendTurnResumesSession(t *testing.T) {
	runner := successRunner("claude-resume")
	m, sender, root := newTestManager(t, runner)
	cwd := mustMkdir(t, filepath.Join(root, "proj"))

	m.HandleCommand(startCommand("cmd_1", "turn_1", cwd))
	waitFor(t, 5*time.Second, "第一轮完成", func() bool {
		return len(sender.rawTurnCompleted()) >= 1
	})
	if got := runner.callAt(0).ResumeSessionID; got != "" {
		t.Fatalf("首轮不应 resume，实际 %q", got)
	}

	hint := "claude-resume"
	cmd2 := startCommand("cmd_2", "turn_2", cwd)
	cmd2.Kind = protocol.CommandSendTurn
	cmd2.Payload.HarnessSessionID = &hint
	m.HandleCommand(cmd2)
	waitFor(t, 5*time.Second, "第二轮完成", func() bool {
		return len(sender.rawTurnCompleted()) >= 2
	})
	if got := runner.callAt(1).ResumeSessionID; got != "claude-resume" {
		t.Fatalf("续接 session = %q, want claude-resume", got)
	}
	if got := runner.callAt(1).Request; got != "请回复 OK" {
		t.Fatalf("request = %q", got)
	}
}

func TestOutboxResendAfterReconnect(t *testing.T) {
	runner := successRunner("claude-resend")
	m, sender, root := newTestManager(t, runner)
	cwd := mustMkdir(t, filepath.Join(root, "proj"))

	m.HandleCommand(startCommand("cmd_1", "turn_1", cwd))
	waitFor(t, 5*time.Second, "首轮完成", func() bool {
		return len(sender.rawTurnCompleted()) >= 1
	})

	m.DetachSender()
	before := len(sender.rawBatches())
	reconnected := &captureSender{}
	m.AttachSender(reconnected)
	waitFor(t, 5*time.Second, "重连后重发 outbox", func() bool {
		return len(reconnected.rawBatches()) >= 1 && len(reconnected.rawTurnCompleted()) >= 1
	})
	if len(reconnected.rawBatches()) < before {
		t.Fatalf("重连后事件批次数量异常: %d < %d", len(reconnected.rawBatches()), before)
	}
}

// TestExecRunnerStage 用真实子进程（假 claude stub）验证 Agent → 进程 → 解析 → 事件 → 完成。
func TestExecRunnerStage(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake-claude.sh 只在类 Unix 上运行")
	}
	stub, err := filepath.Abs("../../testdata/fake-claude.sh")
	if err != nil {
		t.Fatalf("解析 stub 路径: %v", err)
	}
	root := t.TempDir()
	stateDir := t.TempDir()
	m, err := New(Options{
		StateDir:      stateDir,
		AllowRoot:     root,
		Logger:        discardLogger(),
		Runner:        claude.NewExecRunner(stub, nil, nil),
		BatchInterval: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(m.Close)
	sender := &captureSender{}
	m.AttachSender(sender)
	cwd := mustMkdir(t, filepath.Join(root, "proj"))

	m.HandleCommand(startCommand("cmd_exec", "turn_exec", cwd))
	waitFor(t, 10*time.Second, "真实进程 turn.completed", func() bool {
		return len(sender.rawTurnCompleted()) >= 1
	})
	tc := sender.rawTurnCompleted()[0]
	if tc.Status != protocol.TurnSucceeded || tc.Result == nil || *tc.Result != "stub-OK" {
		t.Fatalf("turn.completed = %+v", tc)
	}
	if tc.ExitCode == nil || *tc.ExitCode != 0 {
		t.Fatalf("exit_code = %v", tc.ExitCode)
	}
	types := make(map[string]bool)
	for _, batch := range sender.rawBatches() {
		for _, event := range batch.Events {
			types[event.Type] = true
		}
	}
	for _, want := range []string{protocol.EventSystemInit, protocol.EventAssistantMessage, protocol.EventToolUse, protocol.EventToolResult, protocol.EventResult} {
		if !types[want] {
			t.Fatalf("缺少事件类型 %s（已有 %v）", want, types)
		}
	}
}

// TestExecRunnerCancelStage 验证取消指令真实 kill 子进程并上报 cancelled。
func TestExecRunnerCancelStage(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake-claude.sh 只在类 Unix 上运行")
	}
	t.Setenv("FAKE_CLAUDE_SLEEP", "30")
	stub, err := filepath.Abs("../../testdata/fake-claude.sh")
	if err != nil {
		t.Fatalf("解析 stub 路径: %v", err)
	}
	root := t.TempDir()
	m, err := New(Options{
		StateDir:      t.TempDir(),
		AllowRoot:     root,
		Logger:        discardLogger(),
		Runner:        claude.NewExecRunner(stub, nil, nil),
		BatchInterval: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(m.Close)
	sender := &captureSender{}
	m.AttachSender(sender)
	cwd := mustMkdir(t, filepath.Join(root, "proj"))

	cmd := startCommand("cmd_exec_cancel", "turn_exec_cancel", cwd)
	cmd.Payload.TimeoutSeconds = 60
	m.HandleCommand(cmd)
	waitFor(t, 10*time.Second, "stub 输出首批事件", func() bool {
		return len(sender.rawBatches()) >= 1
	})
	start := time.Now()
	m.HandleCommand(cancelCommand("cmd_cancel_exec", ""))
	waitFor(t, 10*time.Second, "取消后 turn.completed", func() bool {
		return len(sender.rawTurnCompleted()) >= 1
	})
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("取消后进程未及时退出，耗时 %s", elapsed)
	}
	tc := sender.rawTurnCompleted()[0]
	if tc.Status != protocol.TurnCancelled {
		t.Fatalf("turn.completed status = %s, want cancelled", tc.Status)
	}
}
