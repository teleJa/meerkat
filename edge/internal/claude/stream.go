// Package claude 适配 Claude Code CLI 的 stream-json 输出：
// 解析 system/assistant/user/result 事件，并负责启动/续接/取消子进程。
package claude

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ToolUse 对应 assistant 消息中的 tool_use 内容块。
type ToolUse struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input,omitempty"`
}

// ToolResult 对应 user 消息中的 tool_result 内容块。
type ToolResult struct {
	ToolUseID string `json:"tool_use_id"`
	IsError   bool   `json:"is_error"`
	Content   string `json:"content"`
}

// Entry 是一条已解析的 stream-json 事件。
type Entry struct {
	// Type 是原始 type：system / assistant / user / result，其他值原样保留。
	Type string
	// Subtype 仅 system（如 init）与 result（success/error_*）使用。
	Subtype string
	// SessionID 是 Claude 的 harness session id，用于 --resume。
	SessionID string
	// HarnessVersion 来自 system init 的 claude_code_version。
	HarnessVersion string
	// Model 来自 system init。
	Model string
	// Cwd 来自 system init。
	Cwd string
	// Text 是 assistant 文本内容（多个 text 块用换行连接）。
	Text string
	// ToolUses / ToolResults 是工具调用与结果。
	ToolUses    []ToolUse
	ToolResults []ToolResult
	// ResultText / IsError / DurationMS / Turns 来自 result 事件。
	ResultText string
	IsError    bool
	DurationMS int64
	Turns      int
	// StreamEvent / DeltaType / DeltaText / BlockIndex 来自 --include-partial-messages 的 stream_event。
	StreamEvent string
	DeltaType   string
	DeltaText   string
	BlockIndex  int
}

// IsInit 判断是否为 system init 事件。
func (e Entry) IsInit() bool {
	return e.Type == "system" && e.Subtype == "init"
}

// rawEntry 是 stream-json 行的原始结构。
type rawEntry struct {
	Type              string          `json:"type"`
	Subtype           string          `json:"subtype"`
	SessionID         string          `json:"session_id"`
	ClaudeCodeVersion string          `json:"claude_code_version"`
	Model             string          `json:"model"`
	Cwd               string          `json:"cwd"`
	Result            string          `json:"result"`
	IsError           bool            `json:"is_error"`
	DurationMS        int64           `json:"duration_ms"`
	NumTurns          int             `json:"num_turns"`
	Message           json.RawMessage `json:"message"`
	Event             json.RawMessage `json:"event"`
}

type rawStreamEvent struct {
	Type  string `json:"type"`
	Index int    `json:"index"`
	Delta struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"delta"`
}

type rawMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type rawItem struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

// ParseLine 解析一行 stream-json 输出。
func ParseLine(line []byte) (*Entry, error) {
	trimmed := strings.TrimSpace(string(line))
	if trimmed == "" {
		return nil, errors.New("空行不是 stream-json 事件")
	}
	var raw rawEntry
	if err := json.Unmarshal([]byte(trimmed), &raw); err != nil {
		return nil, fmt.Errorf("解析 stream-json 行失败: %w", err)
	}
	if strings.TrimSpace(raw.Type) == "" {
		return nil, errors.New("stream-json 事件缺少 type")
	}
	entry := &Entry{
		Type:           raw.Type,
		Subtype:        raw.Subtype,
		SessionID:      raw.SessionID,
		HarnessVersion: raw.ClaudeCodeVersion,
		Model:          raw.Model,
		Cwd:            raw.Cwd,
		ResultText:     raw.Result,
		IsError:        raw.IsError,
		DurationMS:     raw.DurationMS,
		Turns:          raw.NumTurns,
	}
	if raw.Type == "stream_event" {
		if len(raw.Event) == 0 {
			return entry, nil
		}
		var se rawStreamEvent
		if err := json.Unmarshal(raw.Event, &se); err != nil {
			return nil, fmt.Errorf("解析 stream_event 失败: %w", err)
		}
		entry.StreamEvent = se.Type
		entry.BlockIndex = se.Index
		entry.DeltaType = se.Delta.Type
		entry.DeltaText = se.Delta.Text
		return entry, nil
	}
	if len(raw.Message) == 0 {
		return entry, nil
	}
	var msg rawMessage
	if err := json.Unmarshal(raw.Message, &msg); err != nil {
		return nil, fmt.Errorf("解析 %s.message 失败: %w", raw.Type, err)
	}
	items, err := parseContent(msg.Content)
	if err != nil {
		return nil, err
	}
	switch raw.Type {
	case "assistant":
		var texts []string
		for _, item := range items {
			switch item.Type {
			case "text":
				if item.Text != "" {
					texts = append(texts, item.Text)
				}
			case "tool_use":
				entry.ToolUses = append(entry.ToolUses, ToolUse{ID: item.ID, Name: item.Name, Input: item.Input})
			}
		}
		entry.Text = strings.Join(texts, "\n")
	case "user":
		for _, item := range items {
			if item.Type != "tool_result" {
				continue
			}
			entry.ToolResults = append(entry.ToolResults, ToolResult{
				ToolUseID: item.ToolUseID,
				IsError:   item.IsError,
				Content:   contentText(item.Content),
			})
		}
	}
	return entry, nil
}

// parseContent 兼容字符串与内容块数组两种 content 形态。
func parseContent(raw json.RawMessage) ([]rawItem, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return []rawItem{{Type: "text", Text: s}}, nil
	}
	var items []rawItem
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("解析 message.content 失败: %w", err)
	}
	return items, nil
}

// contentText 把 tool_result.content 归一化为字符串：字符串原样返回，
// 内容块数组拼接其中的 text 字段，其他形态序列化为 JSON。
func contentText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var items []rawItem
	if err := json.Unmarshal(raw, &items); err == nil {
		var texts []string
		for _, item := range items {
			if item.Text != "" {
				texts = append(texts, item.Text)
			}
		}
		if len(texts) > 0 {
			return strings.Join(texts, "\n")
		}
	}
	return string(raw)
}
