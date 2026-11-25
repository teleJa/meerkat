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
- id: fixture-local-plugin
  disabled: true
`
		if err := os.WriteFile(patch, []byte(serialized), 0600); err != nil {
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
		"compact":   `"old.test:80","shared.test:80"`,
		"space":     `"old.test:80", "shared.test:80"`,
		"multiline": "\"old.test:80\", \n  'shared.test:80'",
	} {
		t.Run(name, func(t *testing.T) { testTrustExpressionAppendLifecycle(t, formatted) })
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
	manual := []string{"cli.test:80", "base.test:80", "extra.test:80", "outer.test:80", "shared.test:80"}
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
