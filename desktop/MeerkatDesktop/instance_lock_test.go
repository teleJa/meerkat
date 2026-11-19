//go:build !android

package main

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestInstanceLockRejectsSecondHolder(t *testing.T) {
	dir := t.TempDir()
	first, err := acquireInstanceLock(dir)
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}
	defer func() { _ = first.Close() }()

	second, err := acquireInstanceLock(dir)
	if err == nil {
		_ = second.Close()
		t.Fatal("second instance should not acquire the same lock")
	}
	var conflict *instanceConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("expected instanceConflictError, got %T: %v", err, err)
	}
	if conflict.Holder.PID != os.Getpid() {
		t.Fatalf("holder PID = %d, want %d", conflict.Holder.PID, os.Getpid())
	}
	if !strings.Contains(err.Error(), "已在运行") || !strings.Contains(err.Error(), "8790/51857") {
		t.Fatalf("conflict message is not actionable: %v", err)
	}

	if err := first.Close(); err != nil {
		t.Fatalf("release first lock: %v", err)
	}
	third, err := acquireInstanceLock(dir)
	if err != nil {
		t.Fatalf("lock should be reusable after release: %v", err)
	}
	_ = third.Close()
}
