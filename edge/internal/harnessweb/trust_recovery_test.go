package harnessweb

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestTrustConcurrentProcesses(t *testing.T) {
	if home := os.Getenv("MEERKAT_TEST_LOCK_HOME"); home != "" {
		dir := os.Getenv("MEERKAT_TEST_OWNER")
		owner := NewTrustOwner(dir, "test")
		if err := UpdateTrust(home, dir, &owner, "lab", "http://shared.test:51858"); err != nil {
			t.Fatal(err)
		}
		return
	}
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "profiles", "lab"), 0700); err != nil {
		t.Fatal(err)
	}
	var commands []*exec.Cmd
	for i := 0; i < 4; i++ {
		cmd := exec.Command(os.Args[0], "-test.run=^TestTrustConcurrentProcesses$")
		cmd.Env = append(os.Environ(), "MEERKAT_TEST_LOCK_HOME="+home, "MEERKAT_TEST_OWNER="+filepath.Join(home, fmt.Sprint(i)))
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		commands = append(commands, cmd)
	}
	for _, cmd := range commands {
		if err := cmd.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	var ledger trustLedger
	data, err := os.ReadFile(filepath.Join(home, "runtime", "meerkat-trusted-hosts.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &ledger); err != nil || len(ledger.Leases) != 4 {
		t.Fatalf("concurrent lease count %d, %v", len(ledger.Leases), err)
	}
}

func TestTrustReplaysInterruptedCommit(t *testing.T) {
	home := t.TempDir()
	profile := filepath.Join(home, "profiles", "lab")
	if err := os.MkdirAll(profile, 0700); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	a := NewTrustOwner("one", "test")
	if err := UpdateTrust(home, dir, &a, "lab", "http://shared.test:1234"); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(profile, "cordis.patch.yml")
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	tx := trustTransaction{Ledger: trustLedger{Leases: map[string]trustLease{}, Version: trustLedgerVersion}, Changes: []patchChange{{Profile: "lab", Before: before, After: nil}}}
	data, err := json.Marshal(tx)
	if err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(home, "runtime", "meerkat-trusted-hosts.json.pending")
	if err := os.WriteFile(journal, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := UpdateTrust(home, dir, &a, "", ""); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{journal, file} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("recovery left %s: %v", path, err)
		}
	}
}
