package outbox

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// newStreamID 生成事件流 ID，例如 str_ab12。
func newStreamID() string {
	var buf [2]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "str_0000"
	}
	return StreamIDPrefix + hex.EncodeToString(buf[:])
}

// NewEventID 生成事件 ID，例如 evt_1a2b3c4d。
func NewEventID() string {
	var buf [4]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "evt_00000000"
	}
	return "evt_" + hex.EncodeToString(buf[:])
}

// ValidateStreamID 校验 stream_id 形态（仅用于测试与日志断言）。
func ValidateStreamID(id string) error {
	if len(id) != len(StreamIDPrefix)+4 || id[:len(StreamIDPrefix)] != StreamIDPrefix {
		return fmt.Errorf("stream_id %q 不符合 str_<4 hex>", id)
	}
	return nil
}
