package herdr

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"sync"
	"time"

	"meerkat/edge/internal/claude"
	"meerkat/edge/internal/console"
)

// Runner 让一个 Herdr agent pane 作为 Meerkat 的一轮执行器。
// 它实现了 claude.Runner，可在 Edge 的 agent.Manager 中替换 headless Claude。
type Runner struct {
	Client    *Client
	AgentKind string
	AgentArgs []string
	Logger    *log.Logger

	mu      sync.Mutex
	version string
}

func NewRunner(bin string, agentArgs []string, logger *log.Logger) *Runner {
	if strings.TrimSpace(bin) == "" {
		bin = "herdr"
	}
	if logger == nil {
		logger = log.Default()
	}
	return &Runner{Client: NewClient(bin), AgentKind: "claude", AgentArgs: agentArgs, Logger: logger}
}

func (r *Runner) ensureServer(ctx context.Context) error {
	status, err := r.Client.Status(ctx)
	if err == nil && !strings.Contains(status, "status: not running") {
		return nil
	}
	r.Logger.Printf("Herdr server 未运行，尝试启动本机 headless server")
	cmd := exec.CommandContext(ctx, r.Client.Bin, "server")
	console.Apply(cmd)
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 herdr server 失败: %w", err)
	}
	go func() { _ = cmd.Wait() }()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(300 * time.Millisecond)
		if status, err := r.Client.Status(ctx); err == nil && !strings.Contains(status, "status: not running") {
			return nil
		}
	}
	return errors.New("herdr server 启动超时")
}

func (r *Runner) ensureVersion(ctx context.Context) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.version != "" {
		return r.version
	}
	if out, err := r.Client.Version(ctx); err == nil {
		parts := strings.Fields(out)
		if len(parts) >= 2 {
			r.version = parts[1]
		} else {
			r.version = out
		}
	}
	return r.version
}

func (r *Runner) findAgent(ctx context.Context, resume string) *Agent {
	if strings.TrimSpace(resume) == "" {
		return nil
	}
	agent, err := r.Client.FindAgent(ctx, resume)
	if err != nil {
		return nil
	}
	return agent
}

func (r *Runner) createAgent(ctx context.Context, cwd string) (*Agent, error) {
	label := "meerkat-" + fmt.Sprintf("%d", time.Now().UnixNano())
	ws, err := r.Client.CreateWorkspace(ctx, cwd, label)
	if err != nil {
		return nil, err
	}
	name := label
	r.Logger.Printf("Herdr workspace %s pane %s，启动 agent %s（kind=%s）", ws.WorkspaceID, ws.RootPane.PaneID, name, r.AgentKind)
	startErr := r.Client.StartAgent(ctx, name, r.AgentKind, ws.RootPane.PaneID, r.AgentArgs)
	if startErr != nil {
		r.Logger.Printf("Herdr agent start 返回（可能等待 trust）：%v", startErr)
	}
	deadline := time.Now().Add(35 * time.Second)
	for time.Now().Before(deadline) {
		agent, err := r.Client.FindAgent(ctx, name)
		if err == nil && agent != nil {
			switch agent.AgentStatus {
			case "idle":
				if agent.Interactive {
					return agent, nil
				}
			case "blocked":
				screen, _ := r.Client.ReadScreen(ctx, name, 60)
				if strings.Contains(screen, "trust this folder") || strings.Contains(screen, "Quick safety check") || strings.Contains(screen, "Enter to confirm") {
					_ = r.Client.SendKeys(ctx, name, "Down", "Enter")
				}
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return nil, fmt.Errorf("Herdr agent %s 未在超时内就绪", name)
}

func extractAnswer(screen string) string {
	lines := strings.Split(screen, "\n")
	last := -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "⏺") || strings.HasPrefix(trimmed, "●") {
			last = i
		}
	}
	if last < 0 {
		return ""
	}
	first := strings.TrimSpace(lines[last])
	first = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(first, "⏺"), "●"))
	out := []string{}
	if first != "" {
		out = append(out, first)
	}
	for _, line := range lines[last+1:] {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "❯") || strings.HasPrefix(trimmed, "✻") || strings.HasPrefix(trimmed, "⏵") || strings.HasPrefix(trimmed, "─") {
			break
		}
		if trimmed == "" {
			if len(out) > 0 {
				break
			}
			continue
		}
		out = append(out, strings.TrimSpace(line))
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// Run 实现 claude.Runner。
func (r *Runner) Run(ctx context.Context, opts claude.RunOptions, onEntry func(claude.Entry)) (claude.Result, error) {
	started := time.Now()
	if strings.TrimSpace(opts.Request) == "" {
		return claude.Result{}, errors.New("request 为空")
	}
	if err := r.ensureServer(ctx); err != nil {
		return claude.Result{}, err
	}
	version := r.ensureVersion(ctx)

	agent := r.findAgent(ctx, opts.ResumeSessionID)
	if agent == nil {
		created, err := r.createAgent(ctx, opts.Cwd)
		if err != nil {
			return claude.Result{}, err
		}
		agent = created
	}
	if onEntry != nil {
		onEntry(claude.Entry{
			Type:           "system",
			Subtype:        "init",
			SessionID:      agent.AgentSession.Value,
			HarnessVersion: version,
			Model:          "herdr/" + r.AgentKind,
			Cwd:            opts.Cwd,
		})
	}
	timeout := 10 * time.Minute
	r.Logger.Printf("Herdr prompt → agent=%s session=%s cwd=%s", agent.Name, agent.AgentSession.Value, opts.Cwd)
	if err := r.Client.Prompt(ctx, agent.Name, opts.Request, timeout); err != nil {
		return claude.Result{}, fmt.Errorf("Herdr prompt 失败: %w", err)
	}
	screen, err := r.Client.ReadScreen(ctx, agent.Name, 500)
	if err != nil {
		return claude.Result{}, fmt.Errorf("读取 Herdr 屏幕失败: %w", err)
	}
	answer := extractAnswer(screen)
	if answer == "" {
		answer = strings.TrimSpace(screen)
	}
	if onEntry != nil {
		onEntry(claude.Entry{Type: "terminal.snapshot", Text: screen, SessionID: agent.AgentSession.Value})
	}
	if onEntry != nil && answer != "" {
		onEntry(claude.Entry{Type: "assistant", Text: answer, SessionID: agent.AgentSession.Value})
	}
	code := 0
	return claude.Result{
		SessionID:      agent.AgentSession.Value,
		HarnessVersion: version,
		ExitCode:       &code,
		ResultText:     answer,
		ResultSubtype:  "success",
		DurationMS:     time.Since(started).Milliseconds(),
	}, nil
}
