// Package dedup 持久去重 command_id：每条指令一行 JSONL，记录 ACK 状态，
// 可选记录该指令对应 turn.completed 的原始 JSON，便于重复投递时重发结果。
//
// 写入时机：指令校验通过、开始执行之前（at-most-once 执行）。进程崩溃后同一
// command_id 不会重跑，代价是该轮可能停在中心侧直到指令过期，符合协议
// “过期、已取消或能力不满足的指令不可执行”的要求。
package dedup

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"meerkat/edge/internal/protocol"
)

const (
	// FileName 是去重记录文件名。
	FileName = "commands.jsonl"

	// StatusAccepted 与 StatusRejected 是记录下来的 ACK 状态。
	StatusAccepted = protocol.CommandAckAccepted
	StatusRejected = protocol.CommandAckRejected

	// maxLines 触发压缩的阈值；超过后重写为每个 command_id 的最新记录。
	maxLines = 5000
)

// Entry 是一条指令的去重记录。
type Entry struct {
	CommandID     string          `json:"command_id"`
	Status        string          `json:"status"`
	Detail        string          `json:"detail,omitempty"`
	At            string          `json:"at"`
	TurnCompleted json.RawMessage `json:"turn_completed,omitempty"`
}

// Store 是并发安全的 command_id 去重表。
type Store struct {
	dir     string
	path    string
	mu      sync.Mutex
	entries map[string]*Entry
	lines   int
}

// Open 加载（必要时创建）目录中的去重表；行数超过阈值时自动压缩。
func Open(dir string) (*Store, error) {
	if dir == "" {
		return nil, errors.New("dedup 目录为空")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("创建状态目录 %s 失败: %w", dir, err)
	}
	s := &Store{dir: dir, path: filepath.Join(dir, FileName), entries: make(map[string]*Entry)}
	if err := s.load(); err != nil {
		return nil, err
	}
	if s.lines > maxLines {
		if err := s.compactLocked(); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *Store) load() error {
	f, err := os.Open(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("打开 %s 失败: %w", s.path, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var entry Entry
		if err := json.Unmarshal(line, &entry); err != nil || entry.CommandID == "" {
			continue
		}
		s.lines++
		copied := entry
		s.entries[entry.CommandID] = &copied
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("读取 %s 失败: %w", s.path, err)
	}
	return nil
}

// Get 返回命令的去重记录副本。
func (s *Store) Get(commandID string) (Entry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[commandID]
	if !ok {
		return Entry{}, false
	}
	return *entry, true
}

// Record 追加一条记录（同 command_id 以最后一行为准）。
func (s *Store) Record(entry Entry) error {
	if entry.CommandID == "" {
		return errors.New("command_id 为空")
	}
	if entry.At == "" {
		entry.At = protocol.FormatTime(nowUTC())
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.appendLocked(entry); err != nil {
		return err
	}
	copied := entry
	s.entries[entry.CommandID] = &copied
	if s.lines > maxLines {
		if err := s.compactLocked(); err != nil {
			return err
		}
	}
	return nil
}

// SetCompletion 追加该指令的 turn.completed，重复投递时可原样重发。
func (s *Store) SetCompletion(commandID string, tc protocol.TurnCompleted) error {
	if tc.Type == "" {
		tc.Type = protocol.TypeTurnCompleted
	}
	data, err := json.Marshal(tc)
	if err != nil {
		return fmt.Errorf("序列化 turn.completed 失败: %w", err)
	}
	s.mu.Lock()
	entry, ok := s.entries[commandID]
	var base Entry
	if ok {
		base = *entry
	} else {
		base = Entry{CommandID: commandID, Status: StatusAccepted}
	}
	base.TurnCompleted = data
	if base.At == "" {
		base.At = protocol.FormatTime(nowUTC())
	}
	s.mu.Unlock()
	return s.Record(base)
}

// All 返回全部记录的副本（用于取消时按 turn_id 找回已完成结果）。
func (s *Store) All() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Entry, 0, len(s.entries))
	for _, entry := range s.entries {
		out = append(out, *entry)
	}
	return out
}

// Count 返回已记录的 command_id 数量。
func (s *Store) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}

func (s *Store) appendLocked(entry Entry) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("序列化去重记录失败: %w", err)
	}
	data = append(data, '\n')
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("打开 %s 失败: %w", s.path, err)
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("写入 %s 失败: %w", s.path, err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("同步 %s 失败: %w", s.path, err)
	}
	s.lines++
	return nil
}

func (s *Store) compactLocked() error {
	var buf []byte
	for _, entry := range s.entries {
		data, err := json.Marshal(entry)
		if err != nil {
			return fmt.Errorf("序列化去重记录失败: %w", err)
		}
		buf = append(buf, data...)
		buf = append(buf, '\n')
	}
	tmp, err := os.CreateTemp(s.dir, ".commands-*.tmp")
	if err != nil {
		return fmt.Errorf("创建临时文件失败: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("设置临时文件权限失败: %w", err)
	}
	if _, err := tmp.Write(buf); err != nil {
		tmp.Close()
		return fmt.Errorf("写入临时文件失败: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("同步临时文件失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭临时文件失败: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("替换 %s 失败: %w", s.path, err)
	}
	s.lines = len(s.entries)
	return nil
}
