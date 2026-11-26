package harnessweb

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// An interrupted legacy update can leave different profiles on either side of
// the same journal. Retire each installed slot using its own snapshot.
func TestRetireInterruptedLegacyUpdate(t *testing.T) {
	fixture := func(name, file string) []byte {
		t.Helper()
		data, err := os.ReadFile(filepath.Join("testdata", name, file))
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	before := fixture("legacy-trust", "patch.yml")
	after := fixture("legacy-trust-three", "patch.yml")
	ledger := fixture("legacy-trust", "ledger.json")
	journal, err := json.Marshal(struct {
		Ledger  json.RawMessage
		Changes []patchChange
	}{
		fixture("legacy-trust-three", "ledger.json"), []patchChange{{"lab", before, after}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, current := range map[string][]byte{"before": before, "after": after, "edited": append(append([]byte{}, after...), '#')} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			profile := filepath.Join(home, "profiles", "lab")
			runtimeDir := filepath.Join(home, "runtime")
			for _, dir := range []string{profile, runtimeDir} {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			patch := filepath.Join(profile, "cordis.patch.yml")
			lp := filepath.Join(runtimeDir, "meerkat-trusted-hosts.json")
			inputs := map[string][]byte{patch: current, lp: ledger, lp + ".pending": journal}
			for path, data := range inputs {
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			err := UpdateTrust(home, t.TempDir(), nil, "", "")
			if name == "edited" {
				if err == nil {
					t.Fatal("unexpected success on edited snapshot")
				}
				for path, want := range inputs {
					got, e := os.ReadFile(path)
					if e != nil || !bytes.Equal(got, want) {
						t.Fatalf("changed %s after conflict: %v", path, e)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(patch)
			split := bytes.LastIndex(before, []byte("\n# Meerkat"))
			if err != nil || split <= 0 || !bytes.Equal(got, before[:split]) {
				t.Fatalf("wrong base after retirement: %q %v", got, err)
			}
			if _, err := os.Stat(lp + ".pending"); !os.IsNotExist(err) {
				t.Fatalf("pending journal: %v", err)
			}
		})
	}
}

func TestRetiredDuplicateDoesNotCommit(t *testing.T) {
	fixture := filepath.Join("testdata", "legacy-trust")
	raw, err := os.ReadFile(filepath.Join(fixture, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	patch, err := os.ReadFile(filepath.Join(fixture, "patch.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var old retiredLedger
	if err := json.Unmarshal(raw, &old); err != nil {
		t.Fatal(err)
	}
	_, row, ok := strings.Cut(old.Blocks["lab"], legacyHeading+"\n")
	if !ok {
		t.Fatal("missing recorded row")
	}
	home := t.TempDir()
	profile := filepath.Join(home, "profiles", "lab")
	runtimeDir := filepath.Join(home, "runtime")
	for _, dir := range []string{profile, runtimeDir} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	lp := filepath.Join(runtimeDir, "meerkat-trusted-hosts.json")
	pp := filepath.Join(profile, "cordis.patch.yml")
	inputs := map[string][]byte{lp: raw, pp: append(patch, []byte(row)...)}
	for path, data := range inputs {
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := UpdateTrust(home, t.TempDir(), nil, "", ""); err == nil {
		t.Fatal("retired a ledger with duplicate rows")
	}
	for path, want := range inputs {
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("changed %s: %v", path, err)
		}
	}
	if _, err := os.Stat(lp + ".pending"); !os.IsNotExist(err) {
		t.Fatalf("unexpected journal: %v", err)
	}
}

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
