package harnessweb

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTrustExactAuthority(t *testing.T) {
	for raw, want := range map[string]string{"http://Example.COM/": "example.com:80", "https://[2001:db8::1]/": "[2001:db8::1]:443", "http://127.2.3.4:51858": "", "http://[::1]:51900": ""} {
		got, err := trustedAuthority(raw)
		if err != nil || got != want {
			t.Fatalf("%q => %q %v", raw, got, err)
		}
	}
	for _, raw := range []string{"http://user@example.com", "http://host:012", "http://x:65536", "http://x:/", "http://example.com/path", "http://foo.*:12", "http://0x7f000001", "http://1.2.3", "http://%65xample.com", "http://a b", "https://foo/?token=x"} {
		if _, err := trustedAuthority(raw); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
}
func TestTrustLeaseLifecycle(t *testing.T) {
	h := t.TempDir()
	p := filepath.Join(h, "profiles", "lab")
	if err := os.MkdirAll(p, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(p, "cordis.patch.yml")
	original := []byte("# user config\r\n- id: connection\r\n  config:\r\n    cookieMaxAgeDays: 3 # custom\r\n    trustedHosts: [manual.test:1234]\r\n")
	if err := os.WriteFile(file, original, 0600); err != nil {
		t.Fatal(err)
	}
	a, b := NewTrustOwner("a", "one"), NewTrustOwner("b", "two")
	first, second := t.TempDir(), t.TempDir()
	run := func(dir string, owner *TrustOwner, profile, url string) {
		t.Helper()
		if err := UpdateTrust(h, dir, owner, profile, url); err != nil {
			t.Fatal(err)
		}
	}
	read := func() []byte {
		t.Helper()
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	const url = "http://edge.test:51858/"
	run(first, &a, "lab", url)
	initial := read()
	for _, s := range []string{"ctx.get('webStartup')", "edge.test:51858", "manual.test:1234", "!!js"} {
		if !strings.Contains(string(initial[len(original):]), s) {
			t.Fatalf("missing %s in %s", s, initial)
		}
	}
	past := time.Unix(1, 0)
	if err := os.Chtimes(file, past, past); err != nil {
		t.Fatal(err)
	}
	run(first, &a, "lab", url)
	run(second, &b, "lab", url)
	run(first, &a, "", "")
	run(second, &a, "", "")
	stat, err := os.Stat(file)
	if err != nil || !stat.ModTime().Equal(past) || !bytes.Equal(initial, read()) {
		t.Fatal("unchanged host set rewrote patch")
	}
	run(second, &b, "lab", "http://new.test:80")
	if strings.Contains(string(read()), "edge.test") {
		t.Fatal("old grant remains")
	}
	run(second, &b, "", "")
	if !bytes.Equal(original, read()) {
		t.Fatal("user configuration changed")
	}
}

func TestTrustRestoresFlowDocument(t *testing.T) {
	for _, source := range []string{"[]\n", "[{id: connection, config: {trustedHosts: [manual.test:80]}}]\n"} {
		home := t.TempDir()
		profile := filepath.Join(home, "profiles", "lab")
		if err := os.MkdirAll(profile, 0700); err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(profile, "cordis.patch.yml")
		if err := os.WriteFile(file, []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
		owner := NewTrustOwner("dev", "test")
		dir := t.TempDir()
		for _, u := range []string{"http://shared.test:1234", "http://new.test:80", ""} {
			if err := UpdateTrust(home, dir, &owner, "lab", u); err != nil {
				t.Fatal(err)
			}
		}
		b, err := os.ReadFile(file)
		if err != nil || string(b) != source {
			t.Fatalf("restoration failed: %q %v", b, err)
		}
	}
}
