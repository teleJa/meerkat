package hub

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHarnessProxyLogRotationAndRetention(t *testing.T) {
	dir := t.TempDir()
	writer := newHarnessProxyLog(dir)
	t.Cleanup(writer.close)
	writer.segment, writer.budget = 12, 24
	now := time.Date(2026, 11, 19, 10, 0, 0, 0, time.FixedZone("UTC+8", 8*3600))
	writer.now = func() time.Time { return now }
	write := func(text string) {
		t.Helper()
		if _, err := writer.Write([]byte(text)); err != nil {
			t.Fatal(err)
		}
	}
	read := func() []string {
		t.Helper()
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		var result []string
		var total int
		for _, entry := range entries {
			if entry.Name() != harnessLogName {
				stamp, _ := strings.CutPrefix(entry.Name(), harnessLogName+".")
				if _, err := time.Parse(harnessLogArchiveTime, stamp); err != nil {
					continue
				}
			}
			data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
			if err != nil {
				t.Fatal(err)
			}
			if len(data) > int(writer.segment) {
				t.Fatalf("oversized segment: %s", entry.Name())
			}
			total += len(data)
			result = append(result, string(data))
		}
		if total > int(writer.budget) {
			t.Fatalf("retained %d bytes exceeds %d bytes", total, writer.budget)
		}
		return result
	}
	mismatch := filepath.Join(dir, "harness-proxy.log.manual-backup")
	if err := os.WriteFile(mismatch, []byte("manual"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Manual backups are excluded from the fixture's segment enumeration.
	t.Cleanup(func() { _ = os.Remove(mismatch) })
	for i := range 5 {
		write(fmt.Sprintf("line%d\n", i))
	}
	if got := fmt.Sprint(read()); strings.Contains(got, "line0") || !strings.Contains(got, "line4") {
		t.Fatalf("unexpected size retention: %s", got)
	}
	now = now.AddDate(0, 0, 1)
	write("next\n")
	if got := fmt.Sprint(read()); !strings.HasPrefix(got, "[next\n ") || !strings.Contains(got, "line4") {
		t.Fatalf("new local day did not rotate active log: %s", got)
	}
	now = now.AddDate(0, 0, 8)
	write("today\n")
	if got := fmt.Sprint(read()); got != "[today\n]" {
		t.Fatalf("expired diagnostic logs still present: %s", got)
	}
	if data, err := os.ReadFile(mismatch); err != nil || string(data) != "manual" {
		t.Fatalf("unrelated backup was touched: %q %v", data, err)
	}
}

func TestHarnessProxyLogPreservesRecentLegacyLines(t *testing.T) {
	writer := newHarnessProxyLog(t.TempDir())
	t.Cleanup(writer.close)
	writer.segment, writer.budget = 16, 64
	old := "too-long-old-prefix\nold-two\n最近\nlast\n"
	if err := os.WriteFile(writer.path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("fresh\n")); err != nil {
		t.Fatal(err)
	}
	archives, err := filepath.Glob(writer.path + ".*")
	if err != nil || len(archives) != 1 {
		t.Fatalf("expected one migrated archive: %v %v", archives, err)
	}
	if data, err := os.ReadFile(archives[0]); err != nil || string(data) != "最近\nlast\n" {
		t.Fatalf("expected complete recent lines: %q %v", data, err)
	}
	if data, err := os.ReadFile(writer.path); err != nil || string(data) != "fresh\n" {
		t.Fatalf("unexpected active log: %q %v", data, err)
	}
}
