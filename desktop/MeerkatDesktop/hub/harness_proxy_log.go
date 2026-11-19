package hub

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	harnessLogName          = "harness-proxy.log"
	harnessLogSegmentBytes  = 32 << 20
	harnessLogRetainedBytes = 128 << 20
	harnessLogRetainedDays  = 7
	harnessLogArchiveTime   = "20060102T150405.000000000Z"
)

// All device proxies share one writer. Closing the active file before moving
// it also lets rotation work while the Hub is running on Windows.
type harnessProxyLog struct {
	*log.Logger
	mu          sync.Mutex
	path        string
	now         func() time.Time
	segment     int64
	budget      int64
	days        int
	file        *os.File
	lastWrite   time.Time
	size        int64
	archiveSize int64
	lastError   time.Time
	closed      bool
}

func newHarnessProxyLog(dir string) *harnessProxyLog {
	writer := &harnessProxyLog{
		path: filepath.Join(dir, harnessLogName), now: time.Now,
		segment: harnessLogSegmentBytes, budget: harnessLogRetainedBytes, days: harnessLogRetainedDays,
		archiveSize: -1,
	}
	writer.Logger = log.New(writer, "harness-proxy ", log.LstdFlags)
	return writer
}

func (writer *harnessProxyLog) Write(line []byte) (n int, err error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if writer.closed {
		return 0, os.ErrClosed
	}
	now := writer.now()
	defer func() {
		if err != nil && (writer.lastError.IsZero() || now.Sub(writer.lastError) >= time.Minute) {
			log.Printf("harness proxy diagnostic log: %v", err)
			writer.lastError = now
		}
	}()
	if int64(len(line)) > writer.segment {
		return 0, errors.New("diagnostic record exceeds segment limit")
	}
	if writer.file == nil {
		if err := writer.open(); err != nil {
			return 0, err
		}
	}
	if writer.size > 0 && (writer.lastWrite.In(now.Location()).Format(time.DateOnly) != now.Format(time.DateOnly) || writer.size+int64(len(line)) > writer.segment) {
		if err := writer.rotate(); err != nil {
			return 0, err
		}
	}
	if writer.archiveSize < 0 || writer.size+writer.archiveSize+int64(len(line)) > writer.budget {
		if err := writer.prune(now, int64(len(line))); err != nil {
			return 0, err
		}
	}
	n, err = writer.file.Write(line)
	writer.size += int64(n)
	if n > 0 {
		writer.lastWrite = now
	}
	return n, err
}

func (writer *harnessProxyLog) open() error {
	file, err := os.OpenFile(writer.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return err
	}
	writer.file, writer.size = file, info.Size()
	writer.lastWrite = info.ModTime()
	return nil
}

func (writer *harnessProxyLog) rotate() error {
	err := writer.file.Close()
	writer.file = nil
	if err != nil {
		return err
	}
	stamp := writer.lastWrite.UTC()
	for {
		archive := writer.path + "." + stamp.Format(harnessLogArchiveTime)
		if _, err := os.Lstat(archive); errors.Is(err, os.ErrNotExist) {
			if err := writer.archive(archive); err != nil {
				return err
			}
			break
		} else if err != nil {
			return err
		}
		stamp = stamp.Add(time.Nanosecond)
	}
	if err := writer.open(); err != nil {
		return err
	}
	writer.archiveSize = -1
	return nil
}

func (writer *harnessProxyLog) archive(path string) error {
	if writer.size <= writer.segment {
		return os.Rename(writer.path, path)
	}
	// Preserve a bounded, line-aligned tail when first rotating the old
	// unbounded log. Existing diagnostic lines may contain multibyte text.
	source, err := os.Open(writer.path)
	if err != nil {
		return err
	}
	reader := io.NewSectionReader(source, writer.size-writer.segment, writer.segment)
	data, readErr := io.ReadAll(reader)
	closeErr := source.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return err
	}
	start := bytes.IndexByte(data, '\n') + 1
	if start == 0 {
		start = len(data)
	}
	if err := os.WriteFile(path, data[start:], 0o600); err != nil {
		_ = os.Remove(path)
		return err
	}
	if err := os.Truncate(writer.path, 0); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}

func (writer *harnessProxyLog) prune(now time.Time, incoming int64) error {
	dir := filepath.Dir(writer.path)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	prefix := filepath.Base(writer.path) + "."
	cutoff := now.AddDate(0, 0, -writer.days)
	remaining := writer.budget - writer.size - incoming
	var kept int64
	// ReadDir sorts by filename. Retain the newest whole segments first.
	for i := len(entries) - 1; i >= 0; i-- {
		entry := entries[i]
		stamp, ok := strings.CutPrefix(entry.Name(), prefix)
		if !ok || !entry.Type().IsRegular() {
			continue
		}
		written, err := time.Parse(harnessLogArchiveTime, stamp)
		if err != nil {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if written.Before(cutoff) || kept+info.Size() > remaining {
			if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil {
				return fmt.Errorf("remove expired diagnostic segment: %w", err)
			}
		} else {
			kept += info.Size()
		}
	}
	writer.archiveSize = kept
	return nil
}

func (writer *harnessProxyLog) close() {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	writer.closed = true
	if writer.file != nil {
		_ = writer.file.Close()
		writer.file = nil
	}
}
