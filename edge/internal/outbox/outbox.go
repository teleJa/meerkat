// Package outbox 实现 Edge 的事件 outbox：event.batch 与 turn.completed 追加写入
// 本地 JSONL 文件，收到 event.ack 后累计截断，断网/重启后按原 stream_id 与 seq 重发。
//
// 语义约定：
//   - event.batch 至少一次投递，中心按 event_id 去重；
//   - event.ack 的 seq 视为同 stream 的累计确认，删除 seq <= ack.seq 的批次，
//     以及这些批次之前的所有记录（含 turn.completed），保持发送顺序；
//   - turn.completed 没有独立 ack，只能由后续 event.ack 连带清理；没有后续批次时会
//     一直保留并随重连重发（中心按 turn_id 去重）。
package outbox

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
	// FileName 是 outbox 数据文件名。
	FileName = "outbox.jsonl"
	// MetaName 是 outbox 元数据文件名（stream_id、next_seq、next_id）。
	MetaName = "outbox.meta.json"

	// KindEventBatch 与 KindTurnCompleted 是 outbox 记录的两种类型。
	KindEventBatch    = "event.batch"
	KindTurnCompleted = "turn.completed"

	// StreamIDPrefix 是事件流 ID 前缀。
	StreamIDPrefix = "str_"
)

// Record 是 outbox 中的一条待确认记录。
type Record struct {
	ID       int64           `json:"id"`
	Kind     string          `json:"kind"`
	StreamID string          `json:"stream_id,omitempty"`
	Seq      int64           `json:"seq,omitempty"`
	Message  json.RawMessage `json:"message"`

	sent bool
}

type meta struct {
	StreamID string `json:"stream_id"`
	NextSeq  int64  `json:"next_seq"`
	NextID   int64  `json:"next_id"`
}

// Outbox 是并发安全的追加式事件队列。Open 后必须调用 Close 释放文件句柄。
type Outbox struct {
	dir  string
	path string

	mu        sync.Mutex
	file      *os.File
	records   []*Record
	streamID  string
	nextSeq   int64
	nextID    int64
	corrupted int
}

// Open 打开（必要时创建）目录中的 outbox。
func Open(dir string) (*Outbox, error) {
	if dir == "" {
		return nil, errors.New("outbox 目录为空")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("创建状态目录 %s 失败: %w", dir, err)
	}
	_ = os.Chmod(dir, 0o700)

	o := &Outbox{dir: dir, path: filepath.Join(dir, FileName)}
	if err := o.load(); err != nil {
		return nil, err
	}
	return o, nil
}

func (o *Outbox) load() error {
	if err := o.loadRecords(); err != nil {
		return err
	}

	var m meta
	metaPath := filepath.Join(o.dir, MetaName)
	data, err := os.ReadFile(metaPath)
	switch {
	case err == nil:
		if err := json.Unmarshal(data, &m); err != nil {
			m = meta{}
		}
	case errors.Is(err, os.ErrNotExist):
		m = meta{}
	default:
		return fmt.Errorf("读取 %s 失败: %w", metaPath, err)
	}

	// 元数据缺失或落后于记录时，从记录推导，避免 seq 回退。
	maxSeq, maxID, lastStream := int64(0), int64(0), ""
	for _, rec := range o.records {
		if rec.ID > maxID {
			maxID = rec.ID
		}
		if rec.Kind == KindEventBatch {
			if rec.Seq > maxSeq {
				maxSeq = rec.Seq
			}
			if lastStream == "" && rec.StreamID != "" {
				lastStream = rec.StreamID
			}
		}
	}
	if m.StreamID == "" {
		m.StreamID = lastStream
	}
	if m.StreamID == "" {
		m.StreamID = newStreamID()
	}
	if m.NextSeq <= maxSeq {
		m.NextSeq = maxSeq + 1
	}
	if m.NextSeq < 1 {
		m.NextSeq = 1
	}
	if m.NextID <= maxID {
		m.NextID = maxID + 1
	}
	if m.NextID < 1 {
		m.NextID = 1
	}

	o.streamID = m.StreamID
	o.nextSeq = m.NextSeq
	o.nextID = m.NextID
	return o.saveMeta()
}

func (o *Outbox) loadRecords() error {
	f, err := os.Open(o.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("打开 %s 失败: %w", o.path, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64<<10), 8<<20)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec Record
		if err := json.Unmarshal(line, &rec); err != nil || rec.ID <= 0 || rec.Kind == "" || len(rec.Message) == 0 {
			// 进程崩溃可能留下半行，跳过而不阻塞 Edge 启动；该批次由中心侧兜底。
			o.corrupted++
			continue
		}
		rec.sent = false
		copied := rec
		o.records = append(o.records, &copied)
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("读取 %s 失败: %w", o.path, err)
	}
	return nil
}

// CorruptedLines 返回加载时跳过的损坏行数（用于日志/体检）。
func (o *Outbox) CorruptedLines() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.corrupted
}

// StreamID 返回当前事件流 ID。
func (o *Outbox) StreamID() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.streamID
}

// Len 返回尚未确认的记录数。
func (o *Outbox) Len() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.records)
}

// AppendEventBatch 追加一批事件，自动分配 stream_id 与 seq，返回已编码的批次。
func (o *Outbox) AppendEventBatch(events []protocol.Event) (*protocol.EventBatch, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	batch := protocol.EventBatch{
		Type:     protocol.TypeEventBatch,
		StreamID: o.streamID,
		Seq:      o.nextSeq,
		Events:   events,
	}
	data, err := json.Marshal(batch)
	if err != nil {
		return nil, fmt.Errorf("序列化 event.batch 失败: %w", err)
	}
	rec := &Record{ID: o.nextID, Kind: KindEventBatch, StreamID: o.streamID, Seq: o.nextSeq, Message: data, sent: false}
	if err := o.appendLocked(rec); err != nil {
		return nil, err
	}
	o.records = append(o.records, rec)
	o.nextID++
	o.nextSeq++
	if err := o.saveMeta(); err != nil {
		return nil, err
	}
	return &batch, nil
}

// AppendTurnCompleted 追加一条 turn.completed。
func (o *Outbox) AppendTurnCompleted(tc protocol.TurnCompleted) error {
	if tc.Type == "" {
		tc.Type = protocol.TypeTurnCompleted
	}
	data, err := json.Marshal(tc)
	if err != nil {
		return fmt.Errorf("序列化 turn.completed 失败: %w", err)
	}
	o.mu.Lock()
	defer o.mu.Unlock()

	rec := &Record{ID: o.nextID, Kind: KindTurnCompleted, Message: data, sent: false}
	if err := o.appendLocked(rec); err != nil {
		return err
	}
	o.records = append(o.records, rec)
	o.nextID++
	return o.saveMeta()
}

// Unsent 按顺序返回尚未成功发送（本次连接内）的记录副本。
func (o *Outbox) Unsent() []Record {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]Record, 0, len(o.records))
	for _, rec := range o.records {
		if rec.sent {
			continue
		}
		copied := *rec
		copied.sent = false
		out = append(out, copied)
	}
	return out
}

// MarkSent 标记一条记录已在当前连接成功写出。
func (o *Outbox) MarkSent(id int64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, rec := range o.records {
		if rec.ID == id {
			rec.sent = true
			return
		}
	}
}

// ResetSent 在新连接建立时清空发送标记，让全部未确认记录重发。
func (o *Outbox) ResetSent() {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, rec := range o.records {
		rec.sent = false
	}
}

// Ack 处理 event.ack：累计删除同 stream 中 seq <= seq 的批次及其之前的记录。
func (o *Outbox) Ack(streamID string, seq int64) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	if streamID == "" || streamID != o.streamID {
		return 0, nil
	}
	last := -1
	for i, rec := range o.records {
		if rec.Kind == KindEventBatch && rec.StreamID == streamID && rec.Seq <= seq {
			last = i
		}
	}
	if last < 0 {
		return 0, nil
	}
	removed := last + 1
	o.records = append([]*Record{}, o.records[removed:]...)
	return removed, o.rewriteLocked()
}

// Close 关闭底层文件。
func (o *Outbox) Close() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.file == nil {
		return nil
	}
	err := o.file.Close()
	o.file = nil
	return err
}

func (o *Outbox) appendLocked(rec *Record) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("序列化 outbox 记录失败: %w", err)
	}
	data = append(data, '\n')
	f, err := o.ensureFileLocked()
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("写入 outbox 失败: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("同步 outbox 失败: %w", err)
	}
	return nil
}

func (o *Outbox) ensureFileLocked() (*os.File, error) {
	if o.file != nil {
		return o.file, nil
	}
	f, err := os.OpenFile(o.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("打开 %s 失败: %w", o.path, err)
	}
	o.file = f
	return f, nil
}

func (o *Outbox) saveMeta() error {
	m := meta{StreamID: o.streamID, NextSeq: o.nextSeq, NextID: o.nextID}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化 outbox 元数据失败: %w", err)
	}
	data = append(data, '\n')
	return writeFileAtomic(filepath.Join(o.dir, MetaName), data, 0o600)
}

// rewriteLocked 原子重写数据文件（截断后调用）。
func (o *Outbox) rewriteLocked() error {
	if o.file != nil {
		if err := o.file.Close(); err != nil {
			return fmt.Errorf("关闭 outbox 失败: %w", err)
		}
		o.file = nil
	}
	var buf []byte
	for _, rec := range o.records {
		data, err := json.Marshal(rec)
		if err != nil {
			return fmt.Errorf("序列化 outbox 记录失败: %w", err)
		}
		buf = append(buf, data...)
		buf = append(buf, '\n')
	}
	if err := writeFileAtomic(o.path, buf, 0o600); err != nil {
		return err
	}
	if err := o.saveMeta(); err != nil {
		return err
	}
	return nil
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".outbox-*.tmp")
	if err != nil {
		return fmt.Errorf("在 %s 创建临时文件失败: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return fmt.Errorf("设置临时文件权限失败: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
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
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("替换 %s 失败: %w", path, err)
	}
	return nil
}
