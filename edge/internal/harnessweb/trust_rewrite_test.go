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
		entry, err := exec.LookPath("dsh")
		if err != nil {
			t.Skip("DSH CLI unavailable")
		}
		entry, err = filepath.EvalSymlinks(entry)
		if err != nil {
			t.Fatal(err)
		}
		if filepath.Ext(entry) == ".cmd" {
			entry = filepath.Join(filepath.Dir(entry), "node_modules", "@deepseek-ai", "dsh", "package.json")
		}
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
		t.Run(fixture, func(t *testing.T) {
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
					raw = append(bytes.Clone(manual), raw...)
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
