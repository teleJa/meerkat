package harnessweb

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestReviewTrustAfterDSHYAMLRewrite(t *testing.T) {
	for _, mode := range []string{"fixture", "installed DSH"} {
		t.Run(mode, func(t *testing.T) { testDSHRewrite(t, mode) })
	}
}

func testDSHRewrite(t *testing.T, mode string) {
	home, state := t.TempDir(), t.TempDir()
	profile := filepath.Join(home, "profiles", "lab")
	if err := os.MkdirAll(profile, 0700); err != nil {
		t.Fatal(err)
	}
	patch := filepath.Join(profile, "cordis.patch.yml")
	owner := NewTrustOwner("fixture", "dummy")
	if err := UpdateTrust(home, state, &owner, "lab", "http://edge.test:51858/"); err != nil {
		t.Fatal(err)
	}
	const serialize = `
const fs = require('node:fs');
const requireDSH = require('node:module').createRequire(process.argv[2]);
const { parseDocument } = requireDSH('yaml');
const path = process.argv[1];
const doc = parseDocument(fs.readFileSync(path, 'utf8'), { customTags: [{tag:'tag:yaml.org,2002:js',resolve:value=>value}] });
if (doc.errors.length) throw doc.errors[0];
doc.add({id:'fixture-local-plugin',disabled:true});
fs.writeFileSync(path, String(doc));
`
	if mode == "fixture" {
		const serialized = `# Meerkat managed trusted hosts
- config:
    trustedHosts: !!js "[...new Set([...(ctx.get('webStartup')?.trustedHosts ?? []),
      ...([]), ...[\"edge.test:51858\"]])]"
  id: connection
  x-meerkat-trust-id: MARKER
- id: fixture-local-plugin
  disabled: true
`
		var rows []map[string]any
		data, err := os.ReadFile(patch)
		if err != nil {
			t.Fatal(err)
		}
		if err := yaml.Unmarshal(data, &rows); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(patch, []byte(strings.Replace(serialized, "MARKER", rows[0][trustRowID].(string), 1)), 0600); err != nil {
			t.Fatal(err)
		}
	} else {
		entry := installedDSHEntry(t)
		if output, err := exec.Command("node", "-e", serialize, patch, entry).CombinedOutput(); err != nil {
			t.Fatalf("serializer: %v: %s", err, output)
		}
	}
	revokeErr := UpdateTrust(home, state, &owner, "", "")
	after, err := os.ReadFile(patch)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(after, []byte("fixture-local-plugin")) || !bytes.Contains(after, []byte("disabled: true")) {
		t.Fatal("lost independent plugin edit")
	}
	t.Logf("DSH YAML serialization: revoke=%v, old authority remains=%t", revokeErr, bytes.Contains(after, []byte("edge.test:51858")))
	if revokeErr != nil || bytes.Contains(after, []byte("edge.test:51858")) {
		t.Fail()
	}
}

// These fixtures were emitted by the retired Edge writer, before row IDs existed.
func TestDiscardRetiredLedgerAndReconnect(t *testing.T) {
	for _, fixture := range []string{"legacy-trust", "legacy-trust-three"} {
		for _, variant := range []string{"manual heading", "device gap", "combined"} {
			t.Run(fixture+"/"+variant, func(t *testing.T) {
				home := t.TempDir()
				profile := filepath.Join(home, "profiles", "lab")
				runtimeDir := filepath.Join(home, "runtime")
				for _, dir := range []string{runtimeDir, profile} {
					if err := os.MkdirAll(dir, 0700); err != nil {
						t.Fatal(err)
					}
				}
				ledgerPath := filepath.Join(runtimeDir, "meerkat-trusted-hosts.json")
				patch := filepath.Join(profile, "cordis.patch.yml")
				manual := []byte("# 独立配置\r\n- id: connection\r\n  config: {trustedHosts: [base.test:80]} # 用户配置\r\n")
				for file, target := range map[string]string{"ledger.json": ledgerPath, "patch.yml": patch} {
					raw, err := os.ReadFile(filepath.Join("testdata", fixture, file))
					if err != nil {
						t.Fatal(err)
					}
					if file == "patch.yml" {
						split := bytes.LastIndex(raw, []byte("\n# Meerkat managed trusted hosts\n"))
						if split <= 0 {
							t.Fatal("missing recorded slot")
						}
						base, slot := raw[:split], raw[split:]
						if variant == "device gap" {
							base = bytes.Replace(base, []byte("# Meerkat managed trusted hosts"), []byte("# independent connection"), 1)
						}
						if variant != "manual heading" {
							slot = bytes.Replace(slot, []byte("hosts\n- config"), []byte("hosts\n\n- config"), 1)
						}
						manual = append(manual, base...)
						raw = append(bytes.Clone(manual), slot...)
					}
					if err := os.WriteFile(target, raw, 0600); err != nil {
						t.Fatal(err)
					}
				}
				state := t.TempDir()
				owner := NewTrustOwner("reconnect", "test")
				if err := UpdateTrust(home, state, nil, "", ""); err != nil {
					t.Fatal(err)
				}
				got, err := os.ReadFile(patch)
				if err != nil || !bytes.Equal(got, manual) {
					t.Fatalf("independent base after cleanup: %q, %v", got, err)
				}
				var ledger trustLedger
				raw, err := os.ReadFile(ledgerPath)
				if err != nil {
					t.Fatal(err)
				}
				if err = json.Unmarshal(raw, &ledger); err != nil || ledger.Version != trustLedgerVersion || len(ledger.Leases) != 0 {
					t.Fatalf("old leases survived retirement: %v", err)
				}
				if err := UpdateTrust(home, state, &owner, "lab", "https://new.test"); err != nil {
					t.Fatal(err)
				}
				got, err = os.ReadFile(patch)
				if err != nil || !bytes.HasPrefix(got, manual) || !bytes.Contains(got, []byte("new.test:443")) || bytes.Contains(got, []byte("exclusive.test")) {
					t.Fatalf("unexpected reconnect: %q, %v", got, err)
				}
			})
		}
	}
}

func installedDSHEntry(t *testing.T) string {
	t.Helper()
	entry, err := exec.LookPath("dsh")
	if err != nil {
		if os.Getenv("MEERKAT_REQUIRE_DSH") == "1" {
			t.Fatal(err)
		}
		t.Skip("DSH CLI unavailable")
	}
	entry, err = filepath.EvalSymlinks(entry)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Ext(entry) == ".cmd" {
		return filepath.Join(filepath.Dir(entry), "node_modules", "@deepseek-ai", "dsh", "package.json")
	}
	return entry
}

// The base is saved independently as the byte-for-byte cleanup oracle.
func TestRetireResavedProfile(t *testing.T) {
	for _, fixture := range []string{"legacy-trust", "legacy-trust-three", "legacy-trust-alias"} {
		for _, mode := range []string{"original", "CRLF", "DSH"} {
			t.Run(fixture+"/"+mode, func(t *testing.T) {
				load := func(file string) []byte {
					t.Helper()
					b, err := os.ReadFile(filepath.Join("testdata", fixture, file))
					if err != nil {
						t.Fatal(err)
					}
					return b
				}
				full := load("patch.yml")
				split := bytes.LastIndex(full, []byte("\n"+legacyHeading+"\n"))
				if split < 1 {
					t.Fatal("missing device heading")
				}
				save := func(b []byte) []byte {
					t.Helper()
					if mode == "original" {
						return b
					}
					if mode == "CRLF" {
						return bytes.ReplaceAll(b, []byte("\n"), []byte("\r\n"))
					}
					const serialize = `
const requireDSH = require('node:module').createRequire(process.argv[1]);
const source = require('node:fs').readFileSync(0, 'utf8');
const doc = requireDSH('yaml').parseDocument(source, {
  customTags: [{tag: 'tag:yaml.org,2002:js', resolve: value => value}]
});
if (doc.errors.length) throw doc.errors[0];
process.stdout.write(String(doc));`
					cmd := exec.Command("node", "-e", serialize, installedDSHEntry(t))
					cmd.Stdin = bytes.NewReader(b)
					out, err := cmd.Output()
					if err != nil {
						t.Fatal(err)
					}
					return out
				}
				prefix := []byte("- id: independent-plugin\n  disabled: true\n")
				input := append(bytes.Clone(prefix), full...)
				current := save(input)
				want := save(append(prefix, full[:split]...))
				if mode != "original" && bytes.Equal(current, input) {
					t.Fatal("missing rewrite")
				}
				home := t.TempDir()
				patch := filepath.Join(home, "profiles", "lab", "cordis.patch.yml")
				ledger := filepath.Join(home, "runtime", "meerkat-trusted-hosts.json")
				for file, data := range map[string][]byte{patch: current, ledger: load("ledger.json")} {
					if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(file, data, 0600); err != nil {
						t.Fatal(err)
					}
				}
				if err := UpdateTrust(home, t.TempDir(), nil, "", ""); err != nil {
					t.Fatal(err)
				}
				got, err := os.ReadFile(patch)
				if err != nil || !bytes.Equal(got, want) {
					t.Fatalf("base mismatch: %q, want %q (%v)", got, want, err)
				}
				state := t.TempDir()
				owner := NewTrustOwner("reconnect", "test")
				if err := UpdateTrust(home, state, &owner, "lab", "http://new.test"); err != nil {
					t.Fatal(err)
				}
				if err := UpdateTrust(home, state, &owner, "", ""); err != nil {
					t.Fatal(err)
				}
				if got, err := os.ReadFile(patch); err != nil || !bytes.Equal(got, want) {
					t.Fatalf("base after reconnect and revoke: %q (%v)", got, err)
				}
			})
		}
	}
}

func TestTrustRewritePreservesBaseBytes(t *testing.T) {
	for _, base := range []string{"# 注释\r\n- id: connection\r\n  config: {trustedHosts: ['manual.test:80'], cookieMaxAgeDays: 9} # 保留", "[{ id: connection, config: { trustedHosts: ['manual.test:80'] } }, ] # 原文\r\n", "---\n- id: connection\n  config: {trustedHosts: ['manual.test:80']}\n...\n"} {
		hosts := []string{"a.test:80", "b.test:80"}
		first, err := rewriteTrustSlot([]byte(base), hosts)
		if err != nil {
			t.Fatal(err)
		}
		edited := bytes.Replace(first, []byte("b.test:80"), []byte("stale.test:80"), 1)
		if bytes.Equal(edited, first) {
			t.Fatal("missing managed address")
		}
		rebuilt, err := rewriteTrustSlot(edited, hosts)
		if err != nil || !bytes.Equal(rebuilt, first) {
			t.Fatalf("rebuild mismatch: %q %v", rebuilt, err)
		}
		after, err := rewriteTrustSlot(rebuilt, nil)
		if err != nil || string(after) != base {
			t.Fatalf("base changed: %q %v", after, err)
		}
	}
}
