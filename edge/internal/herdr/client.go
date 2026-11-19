// Package herdr provides a small CLI client and a Meerkat runtime adapter for Herdr.
package herdr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"meerkat/edge/internal/console"
)

// Client 调用本机 herdr CLI；所有命令都通过 socket API 与 Herdr server 通信。
type Client struct {
	Bin string
}

func NewClient(bin string) *Client {
	if strings.TrimSpace(bin) == "" {
		bin = "herdr"
	}
	return &Client{Bin: bin}
}

type envelope struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (c *Client) run(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, c.Bin, args...)
	console.Apply(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("herdr %s 失败: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func (c *Client) call(ctx context.Context, args ...string) (json.RawMessage, error) {
	out, err := c.run(ctx, args...)
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var env envelope
		if err := json.Unmarshal([]byte(line), &env); err != nil {
			return nil, fmt.Errorf("解析 herdr 响应失败: %w", err)
		}
		if env.Error != nil {
			return nil, fmt.Errorf("herdr %s: %s (%s)", strings.Join(args, " "), env.Error.Message, env.Error.Code)
		}
		return env.Result, nil
	}
	return nil, fmt.Errorf("herdr %s 没有返回 JSON 响应", strings.Join(args, " "))
}

func (c *Client) text(ctx context.Context, args ...string) (string, error) {
	out, err := c.run(ctx, args...)
	return string(out), err
}

type Agent struct {
	Name         string `json:"name"`
	AgentStatus  string `json:"agent_status"`
	PaneID       string `json:"pane_id"`
	WorkspaceID  string `json:"workspace_id"`
	Cwd          string `json:"cwd"`
	Interactive  bool   `json:"interactive_ready"`
	AgentSession struct {
		Value string `json:"value"`
	} `json:"agent_session"`
}

type agentList struct {
	Agents []Agent `json:"agents"`
}

func (c *Client) ListAgents(ctx context.Context) ([]Agent, error) {
	raw, err := c.call(ctx, "agent", "list")
	if err != nil {
		return nil, err
	}
	var parsed agentList
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("解析 agent list 失败: %w", err)
	}
	return parsed.Agents, nil
}

func (c *Client) FindAgent(ctx context.Context, nameOrSession string) (*Agent, error) {
	agents, err := c.ListAgents(ctx)
	if err != nil {
		return nil, err
	}
	for i := range agents {
		if agents[i].Name == nameOrSession || agents[i].AgentSession.Value == nameOrSession {
			return &agents[i], nil
		}
	}
	return nil, nil
}

type Workspace struct {
	WorkspaceID string
	RootPane    struct {
		PaneID string
		Cwd    string
	}
}

type workspaceCreateResult struct {
	Workspace struct {
		WorkspaceID string `json:"workspace_id"`
	} `json:"workspace"`
	RootPane struct {
		PaneID string `json:"pane_id"`
		Cwd    string `json:"cwd"`
	} `json:"root_pane"`
}

func (c *Client) CreateWorkspace(ctx context.Context, cwd, label string) (*Workspace, error) {
	raw, err := c.call(ctx, "workspace", "create", "--cwd", cwd, "--label", label, "--no-focus")
	if err != nil {
		return nil, err
	}
	var parsed workspaceCreateResult
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("解析 workspace create 失败: %w", err)
	}
	ws := &Workspace{WorkspaceID: parsed.Workspace.WorkspaceID}
	ws.RootPane.PaneID = parsed.RootPane.PaneID
	ws.RootPane.Cwd = parsed.RootPane.Cwd
	if ws.WorkspaceID == "" || ws.RootPane.PaneID == "" {
		return nil, errors.New("workspace create 返回缺少 workspace_id/root_pane")
	}
	return ws, nil
}

func (c *Client) StartAgent(ctx context.Context, name, kind, pane string, args []string) error {
	cmdArgs := []string{"agent", "start", name, "--kind", kind, "--pane", pane, "--timeout", "30000"}
	if len(args) > 0 {
		cmdArgs = append(cmdArgs, "--")
		cmdArgs = append(cmdArgs, args...)
	}
	_, err := c.call(ctx, cmdArgs...)
	return err
}

func (c *Client) SendKeys(ctx context.Context, name string, keys ...string) error {
	args := append([]string{"agent", "send-keys", name}, keys...)
	_, err := c.call(ctx, args...)
	return err
}

func (c *Client) Prompt(ctx context.Context, name, text string, timeout time.Duration) error {
	ms := int(timeout / time.Millisecond)
	if ms <= 0 {
		ms = 600000
	}
	_, err := c.call(ctx, "agent", "prompt", name, text, "--wait", "--timeout", fmt.Sprintf("%d", ms))
	return err
}

func (c *Client) ReadScreen(ctx context.Context, name string, lines int) (string, error) {
	return c.text(ctx, "agent", "read", name, "--source", "recent", "--format", "text", "--lines", fmt.Sprintf("%d", lines))
}

func (c *Client) Status(ctx context.Context) (string, error) {
	return c.text(ctx, "status")
}

func (c *Client) Version(ctx context.Context) (string, error) {
	out, err := c.text(ctx, "--version")
	return strings.TrimSpace(out), err
}
