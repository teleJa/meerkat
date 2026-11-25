package harnessweb

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestReviewTrustAfterOrdinaryProfileEdit(t *testing.T) {
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
	managed, err := os.ReadFile(patch)
	if err != nil {
		t.Fatal(err)
	}
	formatted := bytes.ReplaceAll(managed, []byte("\n"), []byte("\r\n"))
	if err := os.WriteFile(patch, formatted, 0600); err != nil {
		t.Fatal(err)
	}
	revokeErr := UpdateTrust(home, state, &owner, "", "")
	after, err := os.ReadFile(patch)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("format-only line ending change: revoke=%v, old authority remains=%t", revokeErr, bytes.Contains(after, []byte("edge.test:51858")))
	if revokeErr != nil || bytes.Contains(after, []byte("edge.test:51858")) {
		t.Fail()
	}
}

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

func TestTrustRewriteKeepsIndependentEdits(t *testing.T) {
	home := t.TempDir()
	profile := filepath.Join(home, "profiles", "lab")
	if err := os.MkdirAll(profile, 0700); err != nil {
		t.Fatal(err)
	}
	patch := filepath.Join(profile, "cordis.patch.yml")
	write := func(text string) {
		t.Helper()
		if err := os.WriteFile(patch, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	read := func() string {
		t.Helper()
		b, err := os.ReadFile(patch)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	write("[{id: connection, config: {trustedHosts: [manual.test:80], oldSetting: one}}]\n")
	a, b, c := t.TempDir(), t.TempDir(), t.TempDir()
	update := func(state, host string) {
		t.Helper()
		owner := NewTrustOwner(state, "fixture")
		p := "lab"
		if host == "" {
			p = ""
		} else {
			host = "http://" + host
		}
		if err := UpdateTrust(home, state, &owner, p, host); err != nil {
			t.Fatal(err)
		}
	}
	update(a, "manual.test:80")
	update(b, "other.test:80")
	// Edit both the underlying config and the managed copy before release.
	edited := strings.Replace(read(), "oldSetting: one", "oldSetting: user-base", 1)
	edited = strings.Replace(edited, "oldSetting: one", "added: user-row, oldSetting: one", 1)
	edited = strings.ReplaceAll(edited, "\n", "\r\n")
	write(edited)
	update(a, "manual.test:80")
	update(c, "manual.test:80")
	update(a, "")
	if read() != edited {
		t.Fatal("same effective owners rewrote file")
	}
	update(b, "")
	active := read()
	for _, part := range []string{"manual.test:80", "ctx.get('webStartup')", "user-row", "user-base"} {
		if !strings.Contains(active, part) {
			t.Fatalf("lost %s in %s", part, active)
		}
	}
	if strings.Contains(active, "other.test:80") {
		t.Fatal("revoked host remains")
	}
	update(c, "")
	after := read()
	if strings.Contains(after, "webStartup") || strings.Contains(after, "oldSetting: one") || !strings.Contains(after, "manual.test:80") || !strings.Contains(after, "user-row") || !strings.Contains(after, "user-base") {
		t.Fatalf("wrong released profile: %s", after)
	}
}

func TestTrustExpressionAppendLifecycle(t *testing.T) {
	for name, formatted := range map[string]string{
		"compact":                   `"old.test:80","shared.test:80"`,
		"reordered and interleaved": `'shared.test:80', ("shared.test:80"), /* comma , */ "old.test:80"`,
		"space":                     `"old.test:80", "shared.test:80"`,
		"multiline":                 "\"old.test:80\", \n  'shared.test:80'",
	} {
		t.Run(name, func(t *testing.T) { testTrustExpressionAppendLifecycle(t, formatted) })
	}
}

func TestReleaseEditedTrustExpression(t *testing.T) {
	const original = `[...new Set([...(ctx.get('webStartup')?.trustedHosts ?? []), ...(["base"]), ...["device"]])]`
	saved := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!js", Value: original}
	const base = `(ctx.get('override') ?? [/* keep */ "device"])`
	quoted := strings.Replace(original, `'webStartup'`, `"webStartup"`, 1)
	edited := strings.Replace(quoted, `["base"]`, base, 1)
	for _, tc := range []struct {
		name, source, want string
		ok                 bool
	}{
		{"quote only", quoted, original, true},
		{"independent base", edited, strings.Replace(edited, `...["device"]`, `...[]`, 1), true},
		{"manual grouped duplicate", strings.Replace(edited, `...["device"]`, `...[("device") /* , */, ("device")]`, 1), strings.Replace(edited, `...["device"]`, `...[ ("device")]`, 1), true},
		{"owned prefix moved", strings.Replace(edited, `...["device"]`, `...["manual", "device"]`, 1), strings.Replace(edited, `...["device"]`, `...["manual",]`, 1), true},
		{"already deleted", strings.Replace(original, `...["device"]`, `...["manual"]`, 1), strings.Replace(original, `...["device"]`, `...["manual"]`, 1), true},
		{"different constructor", strings.Replace(edited, "new Set", "new Other", 1), "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			current := *saved
			current.Value = tc.source
			got, ok, err := releaseAppendedHosts(saved, &current, []string{"device"})
			if ok && err != nil || ok != tc.ok || got != tc.want {
				t.Fatalf("got (%q,%v), want (%q,%v)", got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestReleaseTrustArrayCounts(t *testing.T) {
	const template = `[...new Set([...(ctx.get('webStartup')?.trustedHosts ?? []), ...([]), ...[%s]])]`
	wrap := func(elements string) string { return strings.Replace(template, "%s", elements, 1) }
	saved := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!js", Value: wrap(`"a","a","b"`)}
	for _, tc := range []struct {
		name, elements, want string
		ok                   bool
	}{
		{"counts and escapes", `"before",("b"),"middle",'\x61',"a","tail",'a'`, `"before","middle","tail",'a'`, true},
		{"missing references", `"manual","a"`, `"manual",`, true},
		{"sparse array", `,,"a",,`, `,,,`, true},
		{"unresolved replacement", `lookupHost(),"b"`, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			current := *saved
			current.Value = wrap(tc.elements)
			got, ok, err := releaseAppendedHosts(saved, &current, []string{"a", "a", "b"})
			if (err == nil) != tc.ok || ok != tc.ok || ok && got != wrap(tc.want) {
				t.Fatalf("released %q, ok=%v", got, ok)
			}
		})
	}
}

func TestTrustMoveAfterManualAppendAndCRLF(t *testing.T) {
	for _, manual := range []string{"manual.test:80", "second.test:80"} {
		for _, final := range []string{"second.test:80", "third.test:80"} {
			t.Run(manual+"/"+final, func(t *testing.T) {
				home, state := t.TempDir(), t.TempDir()
				profile := filepath.Join(home, "profiles", "lab")
				if err := os.MkdirAll(profile, 0700); err != nil {
					t.Fatal(err)
				}
				patch := filepath.Join(profile, "cordis.patch.yml")
				read := func() string {
					t.Helper()
					b, err := os.ReadFile(patch)
					if err != nil {
						t.Fatal(err)
					}
					return string(b)
				}
				write := func(s string) {
					t.Helper()
					if err := os.WriteFile(patch, []byte(s), 0600); err != nil {
						t.Fatal(err)
					}
				}
				owner := NewTrustOwner(state, "fixture")
				update := func(host string) {
					t.Helper()
					p, url := "", ""
					if host != "" {
						p, url = "lab", "http://"+host
					}
					if err := UpdateTrust(home, state, &owner, p, url); err != nil {
						t.Fatal(err)
					}
				}
				update("first.test:80")
				var doc yaml.Node
				if err := yaml.Unmarshal([]byte(read()), &doc); err != nil {
					t.Fatal(err)
				}
				hosts := field(field(doc.Content[0].Content[0], "config"), "trustedHosts")
				hosts.Value = strings.Replace(hosts.Value, `["first.test:80"]`, `["first.test:80","`+manual+`"]`, 1)
				data, err := yaml.Marshal(&doc)
				if err != nil {
					t.Fatal(err)
				}
				write(string(data))
				moves := []string{"second.test:80"}
				if final != moves[0] {
					moves = append(moves, final)
				}
				for _, host := range append(moves, "") {
					update(host)
					write(strings.ReplaceAll(strings.ReplaceAll(read(), "\r\n", "\n"), "\n", "\r\n"))
				}
				if err := yaml.Unmarshal([]byte(read()), &doc); err != nil {
					t.Fatal(err)
				}
				rows := doc.Content[0].Content
				if len(rows) != 1 {
					t.Fatalf("expected one independent connection, got %d", len(rows))
				}
				got := field(field(rows[0], "config"), "trustedHosts").Value
				want := strings.Replace(hosts.Value, `"first.test:80",`, "", 1)
				if got != want {
					t.Fatalf("released expression %q, want %q", got, want)
				}
			})
		}
	}
}

func TestReleaseHistoricalSharedHost(t *testing.T) {
	const shared = "shared.test:80"
	owners := []string{shared, "exclusive.test:80"}
	for _, base := range []string{"", "- id: connection\n  config:\n    trustedHosts: !!js lookupHosts()\n"} {
		historical, err := trustBlock([]byte(base), []string{shared})
		if err != nil {
			t.Fatal(err)
		}
		block, err := trustBlock([]byte(historical), owners)
		if err != nil {
			t.Fatal(err)
		}
		for _, literal := range []string{`'shared.test:80'`, `"\u0073hared.test:80"`, ` /* retained */ ( 'shared.test:80' ) `, `'shared.test:80', 'added.test:80'`, `'added.test:80', 'shared.test:80'`, `'added.test:80'`} {
			var doc yaml.Node
			if err := yaml.Unmarshal([]byte(historical), &doc); err != nil {
				t.Fatal(err)
			}
			hosts := field(field(doc.Content[0].Content[0], "config"), "trustedHosts")
			hosts.Value = strings.Replace(hosts.Value, `"shared.test:80"`, literal, 1)
			manual, err := yaml.Marshal(&doc)
			if err != nil {
				t.Fatal(err)
			}
			for _, input := range []string{string(manual) + block, block + string(manual)} {
				got, err := removeSerializedTrust([]byte(strings.ReplaceAll(input, "\n", "\r\n")), block, owners)
				if err != nil {
					t.Fatal(err)
				}
				var result yaml.Node
				if err := yaml.Unmarshal(got, &result); err != nil {
					t.Fatal(err)
				}
				rows := result.Content[0].Content
				if len(rows) != 1 || field(field(rows[0], "config"), "trustedHosts").Value != hosts.Value {
					t.Fatalf("wrong released profile: %s", got)
				}
			}
		}
	}
}

func TestLegacyTrustCandidateOrder(t *testing.T) {
	block, err := trustBlock(nil, []string{"device.test:80"})
	if err != nil {
		t.Fatal(err)
	}
	block = withoutTestMarker(block)
	for _, dynamic := range []bool{false, true} {
		var doc yaml.Node
		if err := yaml.Unmarshal([]byte(block), &doc); err != nil {
			t.Fatal(err)
		}
		expr := field(field(doc.Content[0].Content[0], "config"), "trustedHosts")
		expr.Value = strings.Replace(expr.Value, "...([])", `...(["other.test:80"])`, 1)
		operand := `"manual.test:80"`
		if dynamic {
			operand = `ctx.get('ownedHost')`
		}
		expr.Value = strings.Replace(expr.Value, `"device.test:80"`, operand, 1)
		other, err := yaml.Marshal(&doc)
		if err != nil {
			t.Fatal(err)
		}
		// Edit an independent field on the managed row to exercise the
		// semantic fallback when the complete ledger row no longer exists.
		edited := strings.Replace(block, "id: connection", "id: connection\n  independent: true", 1)
		for _, input := range []string{string(other) + edited, edited + string(other)} {
			got, err := removeSerializedTrust([]byte(input), block, []string{"device.test:80"})
			if dynamic {
				if err != errTrustOwnership || got != nil {
					t.Fatalf("ambiguous release: %s %v", got, err)
				}
				continue
			}
			if err != nil || !bytes.Contains(got, []byte("manual.test:80")) || bytes.Contains(got, []byte("device.test:80")) {
				t.Fatalf("independent row: %s %v", got, err)
			}
		}
	}
}

func TestTrustReleaseAfterManualRemoval(t *testing.T) {
	block, err := trustBlock(nil, []string{"device"})
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]yaml.Node
	if err := yaml.Unmarshal([]byte(block), &rows); err != nil {
		t.Fatal(err)
	}
	cfg := rows[0]["config"]
	expr := field(&cfg, "trustedHosts")
	expr.Value = strings.Replace(expr.Value, `...["device"]`, `...[]`, 1)
	current, err := yaml.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	base, err := removeSerializedTrust(current, block, []string{"device"})
	if err != nil || strings.TrimSpace(string(base)) != "[]" {
		t.Fatalf("expected released row, got %s, %v", base, err)
	}
}

func TestAmbiguousReorderedTrustRows(t *testing.T) {
	block, err := trustBlock(nil, []string{"device"})
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]yaml.Node
	if err := yaml.Unmarshal([]byte(block), &rows); err != nil {
		t.Fatal(err)
	}
	config := rows[0]["config"]
	expr := field(&config, "trustedHosts")
	expr.Value = strings.Replace(expr.Value, `...["device"]`, `...["manual","device"]`, 1)
	data, err := yaml.Marshal(append(rows, rows[0]))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := removeSerializedTrust(data, block, []string{"device"}); err == nil || !strings.Contains(err.Error(), "归属不唯一") {
		t.Fatalf("ambiguous release: %v", err)
	}
}

func testTrustExpressionAppendLifecycle(t *testing.T, formatted string) {
	home := t.TempDir()
	profile := filepath.Join(home, "profiles", "lab")
	if err := os.MkdirAll(profile, 0700); err != nil {
		t.Fatal(err)
	}
	patch := filepath.Join(profile, "cordis.patch.yml")
	write := func(b []byte) {
		t.Helper()
		if err := os.WriteFile(patch, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write([]byte("- id: connection\n  config:\n    trustedHosts: [base.test:80]\n"))
	readRows := func() *yaml.Node {
		t.Helper()
		b, err := os.ReadFile(patch)
		if err != nil {
			t.Fatal(err)
		}
		var doc yaml.Node
		if err = yaml.Unmarshal(b, &doc); err != nil {
			t.Fatal(err)
		}
		return doc.Content[0]
	}
	lastExpr := func(root *yaml.Node) *yaml.Node {
		return field(field(root.Content[len(root.Content)-1], "config"), "trustedHosts")
	}
	a, b := t.TempDir(), t.TempDir()
	update := func(state, host string) {
		t.Helper()
		owner := NewTrustOwner(state, "fixture")
		p := ""
		if host != "" {
			p = "lab"
			host = "http://" + host
		}
		if err := UpdateTrust(home, state, &owner, p, host); err != nil {
			t.Fatal(err)
		}
	}
	update(a, "old.test:80")
	update(b, "shared.test:80")
	rows := readRows()
	expr := lastExpr(rows)
	// The persisted legacy block contains both devices. Append to its literal,
	// its Set input, and its outer array without introducing a new patch row.
	if !strings.HasSuffix(expr.Value, `"shared.test:80"]])]`) {
		t.Fatal(expr.Value)
	}
	expr.Value = strings.TrimSuffix(expr.Value, `]])]`) + `, 'shared.test:80'], ...['extra.test:80']]), 'outer.test:80']`
	expr.Value = strings.Replace(expr.Value, `"old.test:80","shared.test:80"`, formatted, 1)
	expr.Value = strings.Replace(expr.Value, `ctx.get('webStartup')`, `ctx /* wrapper */ .get( "webStartup" )`, 1)
	const editedBase = `[/* 用户编辑 */ 'edited.test:80']`
	expr.Value = strings.Replace(expr.Value, `["base.test:80"]`, editedBase, 1)

	data, err := yaml.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	write(data)
	update(a, "old.test:80")
	unchanged, err := os.ReadFile(patch)
	if err != nil || string(unchanged) != string(data) {
		t.Fatalf("unchanged lease rewrote formatted patch: %v", err)
	}
	check := func(want ...string) {
		t.Helper()
		expr := lastExpr(readRows()).Value
		if !strings.Contains(expr, editedBase) {
			t.Fatalf("lost current base source: %s", expr)
		}
		if entry := os.Getenv("DSH_INSTALL_DIR"); entry != "" {
			const fixture = `
const load = require('node:module').createRequire(require('node:path').join(process.argv[1], 'package.json'));
const ctx = new (load('@deepseek-ai/cordis').Context)();
ctx.provide('webStartup');
ctx.set('webStartup', {trustedHosts: ['cli.test:80']});
console.log(JSON.stringify(load('@deepseek-ai/cordis-plugin-loader').evaluate(ctx, process.argv[2])));
`
			out, err := exec.Command("node", "-e", fixture, entry, expr).CombinedOutput()
			if err != nil {
				t.Fatalf("loader: %v %s", err, out)
			}
			var got []string
			if err = json.Unmarshal(out, &got); err != nil {
				t.Fatal(err)
			}
			slices.Sort(got)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Fatalf("effective=%v want=%v", got, want)
			}
			t.Logf("effective=%v", got)
		} else {
			for _, h := range want {
				if h != "cli.test:80" && !strings.Contains(expr, h) {
					t.Fatalf("missing %s in %s", h, expr)
				}
			}
		}
	}
	manual := []string{"cli.test:80", "edited.test:80", "extra.test:80", "outer.test:80", "shared.test:80"}
	check(append(slices.Clone(manual), "old.test:80")...)
	update(a, "new.test:80")
	if strings.Contains(lastExpr(readRows()).Value, "old.test:80") {
		t.Fatal("old lease retained")
	}
	check(append(slices.Clone(manual), "new.test:80")...)
	update(b, "")
	check(append(slices.Clone(manual), "new.test:80")...)
	update(a, "")
	update(a, "")
	if strings.Contains(lastExpr(readRows()).Value, "new.test:80") {
		t.Fatal("new lease retained")
	}
	check(manual...)
}

func withoutTestMarker(block string) string {
	var out []string
	for _, line := range strings.Split(block, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), trustRowID+":") {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

func TestTrustLegacyPromotionAndMarkerConflict(t *testing.T) {
	home, state := t.TempDir(), t.TempDir()
	profile := filepath.Join(home, "profiles", "lab")
	if err := os.MkdirAll(profile, 0700); err != nil {
		t.Fatal(err)
	}
	patch := filepath.Join(profile, "cordis.patch.yml")
	ledgerPath := filepath.Join(home, "runtime", "meerkat-trusted-hosts.json")
	read := func(path string) []byte {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	write := func(path string, data []byte) {
		t.Helper()
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	owner := NewTrustOwner("fixture", "dummy")
	if err := UpdateTrust(home, state, &owner, "lab", "http://old.test"); err != nil {
		t.Fatal(err)
	}
	var ledger trustLedger
	if err := json.Unmarshal(read(ledgerPath), &ledger); err != nil {
		t.Fatal(err)
	}
	legacy := withoutTestMarker(ledger.Blocks["lab"])
	ledger.Blocks["lab"] = legacy
	data, err := json.Marshal(ledger)
	if err != nil {
		t.Fatal(err)
	}
	write(ledgerPath, data)
	write(patch, []byte(strings.ReplaceAll(legacy, "\n", "\r\n")))
	if err := UpdateTrust(home, state, &owner, "lab", "http://new.test"); err != nil {
		t.Fatal(err)
	}
	promoted := string(read(patch))
	before := read(ledgerPath)
	if strings.Contains(promoted, "old.test") || !strings.Contains(promoted, trustRowID) || !bytes.Contains(before, []byte(trustRowID)) {
		t.Fatal("legacy ledger did not promote")
	}
	for _, modified := range []string{withoutTestMarker(promoted), strings.Replace(promoted, "new.test", "changed.test", 1) + promoted} {
		write(patch, []byte(modified))
		if err := UpdateTrust(home, state, &owner, "", ""); err == nil || string(read(patch)) != modified || !bytes.Equal(before, read(ledgerPath)) {
			t.Fatal("missing or duplicate identity must leave profile and ledger intact")
		}
	}
	write(patch, []byte(promoted))
	if err := UpdateTrust(home, state, &owner, "", ""); err != nil {
		t.Fatal(err)
	}
}

func TestHistoricalEditsAcrossTrustMoves(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("Node unavailable for expression evaluation")
	}
	home, state := t.TempDir(), t.TempDir()
	profile := filepath.Join(home, "profiles", "lab")
	if err := os.MkdirAll(profile, 0700); err != nil {
		t.Fatal(err)
	}
	patch := filepath.Join(profile, "cordis.patch.yml")
	owner := NewTrustOwner("device", "fixture")
	update := func(host string) {
		t.Helper()
		p, url := "", ""
		if host != "" {
			p, url = "lab", "http://"+host
		}
		if err := UpdateTrust(home, state, &owner, p, url); err != nil {
			t.Fatal(err)
		}
	}
	rows := func() *yaml.Node {
		t.Helper()
		data, err := os.ReadFile(patch)
		if err != nil {
			t.Fatal(err)
		}
		var doc yaml.Node
		if err := yaml.Unmarshal(data, &doc); err != nil {
			t.Fatal(err)
		}
		return doc.Content[0]
	}
	editHistory := func(from, to string) {
		t.Helper()
		root := rows()
		expr := field(field(root.Content[0], "config"), "trustedHosts")
		next := strings.Replace(expr.Value, from, to, 1)
		if next == expr.Value {
			t.Fatal("missing history operand")
		}
		expr.Value = next
		data, err := yaml.Marshal(root)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(patch, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	check := func(want ...string) {
		t.Helper()
		root := rows()
		expr := field(field(root.Content[len(root.Content)-1], "config"), "trustedHosts")
		out, err := exec.Command("node", "-e", "const ctx={get:()=>null};console.log(JSON.stringify("+expr.Value+"))").CombinedOutput()
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got, want) {
			t.Fatalf("effective hosts %v, want %v", got, want)
		}
	}
	update("old.test")
	editHistory(`"old.test:80"`, `"old.test:80","shared.test:80"`)
	update("shared.test")
	editHistory(`"shared.test:80"`, `"a.test:80","shared.test:80","b.test:80"`)
	update("second.test")
	check("a.test:80", "shared.test:80", "b.test:80", "second.test:80")
	editHistory(`"a.test:80","shared.test:80","b.test:80"`, `"b.test:80","shared.test:80"`)
	update("third.test")
	check("b.test:80", "shared.test:80", "third.test:80")
	editHistory(`"b.test:80","shared.test:80"`, `"shared.test:80","b.test:80"`)
	update("")
	check("shared.test:80", "b.test:80")
}
