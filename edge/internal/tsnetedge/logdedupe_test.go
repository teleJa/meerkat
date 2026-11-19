package tsnetedge

import (
	"fmt"
	"reflect"
	"testing"
)

func TestDedupeLogfSuppressesConsecutiveRepeats(t *testing.T) {
	var lines []string
	logf := dedupeLogf(func(format string, args ...any) {
		lines = append(lines, fmt.Sprintf(format, args...))
	})

	for i := 0; i < 5; i++ {
		logf("To start this tsnet server, go to: %s", "https://login.tailscale.com/a/aaa")
	}
	logf("state changed to %s", "Running")

	want := []string{
		"To start this tsnet server, go to: https://login.tailscale.com/a/aaa",
		"上一条提示重复 4 次，已省略",
		"state changed to Running",
	}
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("日志去重结果不符\n got: %#v\nwant: %#v", lines, want)
	}
}

func TestDedupeLogfKeepsDifferentMessages(t *testing.T) {
	var lines []string
	logf := dedupeLogf(func(format string, args ...any) {
		lines = append(lines, fmt.Sprintf(format, args...))
	})
	logf("a")
	logf("b")
	logf("a")
	if len(lines) != 3 {
		t.Fatalf("不同的消息都应输出，实际 %d 条：%#v", len(lines), lines)
	}
}
