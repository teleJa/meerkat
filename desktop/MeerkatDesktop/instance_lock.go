//go:build !android

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// instanceLockInfo is diagnostic data written into the lock file after the
// kernel lock is acquired. It is intentionally best-effort: the lock itself is
// the source of truth.
type instanceLockInfo struct {
	PID       int    `json:"pid"`
	StartedAt string `json:"started_at"`
	Version   string `json:"version"`
}

type instanceLock struct {
	file *os.File
	path string
}

type instanceConflictError struct {
	Path   string
	Holder instanceLockInfo
}

func (e *instanceConflictError) Error() string {
	holder := "另一个 Meerkat 实例"
	if e.Holder.PID > 0 {
		holder = fmt.Sprintf("另一个 Meerkat 实例（PID %d", e.Holder.PID)
		if e.Holder.StartedAt != "" {
			holder += "，启动于 " + e.Holder.StartedAt
		}
		holder += "）"
	}
	return fmt.Sprintf("Meerkat 已在运行：%s 正在使用配置目录：\n%s\n\n为避免覆盖 tsnet 状态和抢占 8790/51857 端口，本次启动已停止。请先退出已有实例。", holder, e.Path)
}

var desktopInstanceLock *instanceLock

// acquireInstanceLock takes an OS-level exclusive lock on <dir>/instance.lock.
// The lock is released automatically when the process exits, so a crash does
// not leave a permanent stale lock.
func acquireInstanceLock(dir string) (*instanceLock, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil, fmt.Errorf("配置目录为空，无法建立单实例锁")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("创建配置目录失败: %w", err)
	}
	path := filepath.Join(dir, "instance.lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("打开单实例锁失败: %w", err)
	}
	locked, err := lockFileExclusiveNB(file)
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("建立单实例锁失败: %w", err)
	}
	if !locked {
		info := readInstanceLockInfo(file)
		_ = file.Close()
		return nil, &instanceConflictError{Path: path, Holder: info}
	}
	info := instanceLockInfo{
		PID:       os.Getpid(),
		StartedAt: time.Now().Format(time.RFC3339),
		Version:   appVersion,
	}
	// Windows locks byte zero for both reads and writes by other handles.
	// Reserve it as JSON whitespace so contenders can still read the holder.
	_ = file.Truncate(0)
	if _, err := file.WriteAt([]byte{'\n'}, 0); err == nil {
		if _, err := file.Seek(1, io.SeekStart); err == nil {
			_ = json.NewEncoder(file).Encode(info)
			_ = file.Sync()
		}
	}
	return &instanceLock{file: file, path: path}, nil
}

func readInstanceLockInfo(file *os.File) instanceLockInfo {
	var info instanceLockInfo
	if _, err := file.Seek(1, io.SeekStart); err != nil {
		return info
	}
	data, err := io.ReadAll(io.LimitReader(file, 4096))
	if err != nil {
		return info
	}
	_ = json.Unmarshal(data, &info)
	return info
}

func (l *instanceLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	_ = unlockFile(l.file)
	err := l.file.Close()
	l.file = nil
	return err
}

func reportInstanceConflict(err error) {
	log.Printf("单实例检查失败: %v", err)
	if strings.TrimSpace(os.Getenv("MEERKAT_NO_DIALOG")) == "1" {
		return
	}
	showInstanceConflictDialog("Meerkat 已在运行", err.Error())
}
