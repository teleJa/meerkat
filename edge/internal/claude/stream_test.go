package claude

import (
	"bufio"
	"os"
	"strings"
	"testing"
)

func parseFixture(t *testing.T, name string) []Entry {
	t.Helper()
	f, err := os.Open("testdata/" + name)
	if err != nil {
		t.Fatalf("打开 fixture %s 失败: %v", name, err)
	}
	defer f.Close()

	var entries []Entry
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		entry, err := ParseLine(line)
		if err != nil {
			t.Fatalf("解析 %s 失败: %v", name, err)
		}
		entries = append(entries, *entry)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("读取 %s 失败: %v", name, err)
	}
	return entries
}

func TestParseSimpleStream(t *testing.T) {
	entries := parseFixture(t, "stream-simple.jsonl")
	if len(entries) != 3 {
		t.Fatalf("事件数 = %d, want 3", len(entries))
	}
	init := entries[0]
	if !init.IsInit() || init.SessionID != "claude-sess-001" || init.HarnessVersion != "2.1.304" || init.Model != "claude-test" {
		t.Fatalf("init 事件解析错误: %+v", init)
	}
	if entries[1].Type != "assistant" || entries[1].Text != "OK" || entries[1].SessionID != "claude-sess-001" {
		t.Fatalf("assistant 事件解析错误: %+v", entries[1])
	}
	result := entries[2]
	if result.Type != "result" || result.ResultText != "OK" || result.IsError || result.DurationMS != 12 || result.Subtype != "success" {
		t.Fatalf("result 事件解析错误: %+v", result)
	}
}

func TestParseToolStream(t *testing.T) {
	entries := parseFixture(t, "stream-tools.jsonl")
	if len(entries) != 5 {
		t.Fatalf("事件数 = %d, want 5", len(entries))
	}
	var uses []ToolUse
	var results []ToolResult
	for _, entry := range entries {
		uses = append(uses, entry.ToolUses...)
		results = append(results, entry.ToolResults...)
	}
	if len(uses) != 1 || uses[0].Name != "Bash" || uses[0].ID != "toolu_1" {
		t.Fatalf("tool_use 解析错误: %+v", uses)
	}
	if !strings.Contains(string(uses[0].Input), "ls -la") {
		t.Fatalf("tool_use input 解析错误: %s", uses[0].Input)
	}
	if len(results) != 1 || results[0].ToolUseID != "toolu_1" || results[0].IsError {
		t.Fatalf("tool_result 解析错误: %+v", results)
	}
	if results[0].Content != "file-a\nfile-b" {
		t.Fatalf("tool_result content = %q", results[0].Content)
	}
	if entries[1].Text != "我先列目录" {
		t.Fatalf("assistant 文本 = %q", entries[1].Text)
	}
	if entries[4].ResultText != "共 2 个文件" || entries[4].Turns != 2 {
		t.Fatalf("result 解析错误: %+v", entries[4])
	}
}

func TestParseErrorStream(t *testing.T) {
	entries := parseFixture(t, "stream-error.jsonl")
	result := entries[len(entries)-1]
	if !result.IsError || result.Subtype != "error_during_execution" {
		t.Fatalf("错误 result 解析错误: %+v", result)
	}
	if !strings.Contains(result.ResultText, "ECONNREFUSED") {
		t.Fatalf("错误文本 = %q", result.ResultText)
	}
}

func TestParseLineRejectsInvalid(t *testing.T) {
	cases := []string{"", "   ", "not json", `{"foo":1}`}
	for _, input := range cases {
		if _, err := ParseLine([]byte(input)); err == nil {
			t.Fatalf("ParseLine(%q) 应返回错误", input)
		}
	}
}

func TestParseContentStringForm(t *testing.T) {
	line := []byte(`{"type":"assistant","message":{"role":"assistant","content":"纯文本"}}`)
	entry, err := ParseLine(line)
	if err != nil {
		t.Fatalf("ParseLine error: %v", err)
	}
	if entry.Text != "纯文本" {
		t.Fatalf("text = %q", entry.Text)
	}
}
func TestParsePartialStreamEvent(t *testing.T) {
	line := []byte(`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"STREAM"}}}`)
	entry, err := ParseLine(line)
	if err != nil {
		t.Fatalf("ParseLine error: %v", err)
	}
	if entry.Type != "stream_event" || entry.StreamEvent != "content_block_delta" {
		t.Fatalf("stream event 解析错误: %+v", entry)
	}
	if entry.DeltaType != "text_delta" || entry.DeltaText != "STREAM" || entry.BlockIndex != 0 {
		t.Fatalf("delta 解析错误: %+v", entry)
	}
}
