package harnessweb

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The fixture was written by 225d6c3. Only its machine-local lease keys
// were shortened to directory names so they can be relocated for each test.
func TestLegacyExactRow(t *testing.T) {
	for _, route := range []string{"release", "promote", "reordered", "duplicate", "edited-copy-first", "edited-copy-last"} {
		t.Run(route, func(t *testing.T) {
			read := func(path string) []byte {
				t.Helper()
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				return data
			}
			write := func(path string, data []byte) {
				t.Helper()
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			home := t.TempDir()
			profile := filepath.Join(home, "profiles", "lab")
			dir := filepath.Join(home, "runtime")
			for _, p := range []string{profile, dir} {
				if err := os.MkdirAll(p, 0700); err != nil {
					t.Fatal(err)
				}
			}
			var ledger trustLedger
			fixture := filepath.Join("testdata", "legacy-trust")
			if err := json.Unmarshal(read(filepath.Join(fixture, "ledger.json")), &ledger); err != nil {
				t.Fatal(err)
			}
			owners := ledger.Leases
			ledger.Leases = map[string]trustLease{}
			real, err := filepath.EvalSymlinks(home)
			if err != nil {
				t.Fatal(err)
			}
			for k, v := range owners {
				key := filepath.Join(real, k)
				if runtime.GOOS == "windows" {
					key = strings.ToLower(key)
				}
				ledger.Leases[key] = v
			}
			data, err := json.MarshalIndent(ledger, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			lp := filepath.Join(dir, "meerkat-trusted-hosts.json")
			write(lp, data)
			patch := filepath.Join(profile, "cordis.patch.yml")
			block := ledger.Blocks["lab"]
			base, ok := strings.CutSuffix(string(read(filepath.Join(fixture, "patch.yml"))), block)
			if !ok {
				t.Fatal("legacy block mismatch")
			}
			manual := strings.Replace(base, `\"manual.test:80\"`, `\"manual.test:80\",\"added.test:80\"`, 1)
			if manual == base {
				t.Fatal("manual edit missed")
			}
			cur := manual + block
			if route == "reordered" {
				cur = block + manual
			}
			if route == "duplicate" {
				cur += block
			}
			if strings.HasPrefix(route, "edited-copy-") {
				edited := strings.Replace(block, `\"exclusive.test:80\",\"manual.test:80\"`, `\"exclusive.test:80\",\"manual.test:80\",\"added.test:80\"`, 1)
				if edited == block {
					t.Fatal("managed edit missed")
				}
				cur = base + edited + block
				if route == "edited-copy-last" {
					cur = base + block + edited
				}
			}
			write(patch, []byte(cur))
			exclusive := filepath.Join(real, "exclusive")
			owner := owners["exclusive"].TrustOwner
			update := func(profile, url string) error { return UpdateTrust(home, exclusive, &owner, profile, url) }
			if route == "promote" {
				if err := update("lab", "http://exclusive.test"); err != nil || string(read(patch)) != cur || !bytes.Equal(read(lp), data) {
					t.Fatalf("unchanged legacy lease: %v", err)
				}
				if err := update("lab", "http://changed.test"); err != nil {
					t.Fatal(err)
				}
				if !bytes.Contains(read(lp), []byte("x-meerkat-trust-id")) {
					t.Fatal("missing upgraded row ID")
				}
			}
			err = update("", "")
			if route == "duplicate" || strings.HasPrefix(route, "edited-copy-") {
				if err == nil || !strings.Contains(err.Error(), "归属不唯一") || string(read(patch)) != cur || !bytes.Equal(read(lp), data) {
					t.Fatal("duplicate row must preserve patch and ledger")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := UpdateTrust(home, filepath.Join(real, "device"), &owner, "", ""); err != nil {
				t.Fatal(err)
			}
			result := string(read(patch))
			if !strings.Contains(result, "added.test:80") || !strings.Contains(result, "manual.test:80") || strings.Contains(result, "exclusive.test:80") || strings.Contains(result, "changed.test:80") || strings.Count(result, "id: connection") != 1 {
				t.Fatalf("unexpected remaining profile: %s", result)
			}
			if route != "reordered" && result != manual {
				t.Fatalf("manual row bytes changed: %q", result)
			}
		})
	}
}
