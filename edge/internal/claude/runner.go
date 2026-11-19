package claude

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"meerkat/edge/internal/console"
)

// DefaultCommand 是未配置 harness 时使用的可执行文件名。
const DefaultCommand = "claude"

// stderrLimit 限制保留的 stderr 字节数，避免把大量日志写进 outbox。
const stderrLimit = 64 << 10

// RunOptions 描述一次 harness 调用。
type RunOptions struct {
	Request string
	// ResumeSessionID 非空时追加 --resume，用于续接既有 Claude 会话。
	ResumeSessionID string
	// Cwd 是子进程工作目录（已经过 allow-root 校验）。
	Cwd string
}

// Result 是一次 harness 调用的结果。
type Result struct {
	SessionID      string
	HarnessVersion string
	ExitCode       *int
	ResultText     string
	ResultSubtype  string
	IsError        bool
	DurationMS     int64
	Stderr         string
	ParseErrors    int
	ReadError      string
}

// Runner 抽象 harness 执行，便于用 fake 在测试中验证编排逻辑。
type Runner interface {
	Run(ctx context.Context, opts RunOptions, onEntry func(Entry)) (Result, error)
}

// ExecRunner 通过子进程执行 harness 并逐行解析 stream-json。
type ExecRunner struct {
	Path string
	// PrefixArgs 位于标准参数之前（config.json 的 harness_command 数组形式）。
	PrefixArgs []string
	// ExtraArgs 位于标准参数之后（config.json harness_args / --harness-arg）。
	ExtraArgs []string
	// Env 是追加或覆盖到子进程环境的变量（例如指向本机 magpie）。
	Env map[string]string
}

// NewExecRunner 创建执行器；path 为空时使用 claude。
func NewExecRunner(path string, prefixArgs, extraArgs []string) *ExecRunner {
	if strings.TrimSpace(path) == "" {
		path = DefaultCommand
	}
	return &ExecRunner{Path: path, PrefixArgs: prefixArgs, ExtraArgs: extraArgs}
}

// environment 返回子进程环境：继承当前进程，并覆盖 r.Env 中的键。
func (r *ExecRunner) environment() []string {
	base := os.Environ()
	if len(r.Env) == 0 {
		return base
	}
	values := make(map[string]string, len(base)+len(r.Env))
	order := make([]string, 0, len(base)+len(r.Env))
	for _, item := range base {
		key, value, found := strings.Cut(item, "=")
		if !found {
			continue
		}
		if _, exists := values[key]; !exists {
			order = append(order, key)
		}
		values[key] = value
	}
	for key, value := range r.Env {
		if _, exists := values[key]; !exists {
			order = append(order, key)
		}
		values[key] = value
	}
	result := make([]string, 0, len(order))
	for _, key := range order {
		result = append(result, key+"="+values[key])
	}
	return result
}

// Args 返回完整 argv（不含 argv[0]），便于单测断言。
func (r *ExecRunner) Args(opts RunOptions) []string {
	args := make([]string, 0, 8+len(r.PrefixArgs)+len(r.ExtraArgs))
	args = append(args, r.PrefixArgs...)
	args = append(args, "-p", opts.Request, "--output-format", "stream-json", "--verbose", "--include-partial-messages")
	if opts.ResumeSessionID != "" {
		args = append(args, "--resume", opts.ResumeSessionID)
	}
	args = append(args, r.ExtraArgs...)
	return args
}

// Run 启动 harness 并逐行解析 stdout；返回 error 仅表示无法启动或等待失败，
// 非零退出码通过 Result.ExitCode 返回，由调用方决定 turn 状态。
func (r *ExecRunner) Run(ctx context.Context, opts RunOptions, onEntry func(Entry)) (Result, error) {
	if strings.TrimSpace(opts.Request) == "" {
		return Result{}, errors.New("request 为空")
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	cmd := exec.Command(r.Path, r.Args(opts)...)
	cmd.Dir = opts.Cwd
	cmd.Env = r.environment()
	cmd.SysProcAttr = sysProcAttr()
	console.Apply(cmd)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Result{}, fmt.Errorf("创建 stdout 管道失败: %w", err)
	}
	stderr := &limitedBuffer{limit: stderrLimit}
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		return Result{}, fmt.Errorf("启动 %s 失败: %w", r.Path, err)
	}

	res := Result{SessionID: opts.ResumeSessionID}
	killed := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = killTree(cmd)
		case <-killed:
		}
	}()

	reader := bufio.NewReaderSize(stdout, 64<<10)
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			trimmed := bytes.TrimSpace(line)
			if len(trimmed) > 0 {
				entry, parseErr := ParseLine(trimmed)
				if parseErr != nil {
					res.ParseErrors++
				} else {
					if entry.SessionID != "" {
						res.SessionID = entry.SessionID
					}
					if entry.HarnessVersion != "" {
						res.HarnessVersion = entry.HarnessVersion
					}
					if entry.Type == "result" {
						res.ResultText = entry.ResultText
						res.ResultSubtype = entry.Subtype
						res.IsError = entry.IsError
						res.DurationMS = entry.DurationMS
					}
					if onEntry != nil {
						onEntry(*entry)
					}
				}
			}
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				res.ReadError = readErr.Error()
			}
			break
		}
	}

	waitErr := cmd.Wait()
	close(killed)
	res.Stderr = stderr.String()

	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			if code := exitErr.ExitCode(); code >= 0 {
				res.ExitCode = &code
			}
			return res, nil
		}
		return res, fmt.Errorf("等待 %s 退出失败: %w", r.Path, waitErr)
	}
	code := 0
	res.ExitCode = &code
	return res, nil
}

// limitedBuffer 保留前 limit 字节，超出部分丢弃并标记截断。
type limitedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	total := len(p)
	if b.buf.Len() < b.limit {
		remain := b.limit - b.buf.Len()
		if len(p) > remain {
			_, _ = b.buf.Write(p[:remain])
			b.truncated = true
		} else {
			_, _ = b.buf.Write(p)
		}
	} else {
		b.truncated = true
	}
	return total, nil
}

func (b *limitedBuffer) String() string {
	if !b.truncated {
		return b.buf.String()
	}
	return b.buf.String() + "…（stderr 已截断）"
}
