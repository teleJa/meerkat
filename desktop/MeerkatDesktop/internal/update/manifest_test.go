package update

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
)

func TestSignedUpdateDownload(t *testing.T) {
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	installer := []byte("installer contents")
	hash := sha256.Sum256(installer)
	var downloads atomic.Int32
	var envelope []byte
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest.json":
			_, _ = w.Write(envelope)
		case "/unavailable/latest.json":
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
		case "/truncated/setup.exe":
			w.Header().Set("Content-Length", "64")
			_, _ = w.Write(installer)
		default:
			downloads.Add(1)
			_, _ = w.Write(installer)
		}
	}))
	server.Start()
	defer server.Close()
	if net.ParseIP(server.Listener.Addr().(*net.TCPAddr).IP.String()).To4() == nil {
		t.Fatal("test requires the 127.0.0.1 loopback listener")
	}
	manifest := Manifest{Version: "0.2.0", Platforms: map[string]Platform{
		"windows-amd64": {URL: server.URL + "/setup.exe", SHA256: hex.EncodeToString(hash[:])},
	}}
	payload, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	signature := ed25519.Sign(private, payload)
	envelope, err = json.Marshal(Envelope{Manifest: payload, Signature: signature})
	if err != nil {
		t.Fatal(err)
	}
	_, manifest, err = Fetch(server.URL+"/latest.json", public)
	if err != nil {
		t.Fatal(err)
	}
	platform, err := manifest.Package("windows", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	t.Run("installer verified", func(t *testing.T) {
		dir := t.TempDir()
		if err := Download(platform, dir, func(_, _ int64) {}); err != nil {
			t.Fatal(err)
		}
		if err := VerifyFile(PackagePath(dir), platform.SHA256); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("wrong signature rejected before download", func(t *testing.T) {
		before := downloads.Load()
		untrusted, _, _ := ed25519.GenerateKey(nil)
		if _, _, err := Fetch(server.URL+"/latest.json", untrusted); err == nil {
			t.Fatal("accepted a manifest signed by a different key")
		}
		if downloads.Load() != before {
			t.Fatal("downloaded after rejecting a manifest")
		}
	})
	t.Run("wrong SHA-256 rejected and partial removed", func(t *testing.T) {
		dir := t.TempDir()
		changed := platform
		wrong := sha256.Sum256([]byte("different contents"))
		changed.SHA256 = hex.EncodeToString(wrong[:])
		if err := Download(changed, dir, func(_, _ int64) {}); err == nil {
			t.Fatal("accepted a different installer")
		}
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 0 {
			t.Fatalf("rejected package left files: %v, %v", entries, err)
		}
	})
	t.Run("tampered signed payload rejected", func(t *testing.T) {
		tampered := []byte(`{"version":"1.2.0","platforms":{}}`)
		data, err := json.Marshal(Envelope{Manifest: tampered, Signature: signature})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = Parse(data, public); err == nil || err.Error() != "更新清单签名验证失败" {
			t.Fatalf("expected a signature error, got %v", err)
		}
	})
	t.Run("missing platform rejected", func(t *testing.T) {
		if _, err := manifest.Package("windows", "arm64"); err == nil || err.Error() != "更新清单没有当前平台的安装包" {
			t.Fatalf("expected a missing-platform error, got %v", err)
		}
	})
	t.Run("manifest HTTP failure surfaced", func(t *testing.T) {
		if _, _, err := Fetch(server.URL+"/unavailable/latest.json", public); err == nil || err.Error() != "更新服务返回 HTTP 503" {
			t.Fatalf("expected HTTP 503, got %v", err)
		}
	})
	t.Run("interrupted download removes partial file", func(t *testing.T) {
		dir := t.TempDir()
		truncated := platform
		truncated.URL = server.URL + "/truncated/setup.exe"
		var downloaded int64
		err := Download(truncated, dir, func(written, _ int64) { downloaded = written })
		if !errors.Is(err, io.ErrUnexpectedEOF) || downloaded != int64(len(installer)) {
			t.Fatalf("expected an interrupted partial download, downloaded=%d error=%v", downloaded, err)
		}
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 0 {
			t.Fatalf("interrupted download left files: %v, %v", entries, err)
		}
	})
	t.Run("missing digest and cleartext remote rejected", func(t *testing.T) {
		platform := manifest.Platforms["windows-amd64"]
		platform.SHA256 = ""
		manifest.Platforms["windows-amd64"] = platform
		if _, err := manifest.Package("windows", "amd64"); err == nil {
			t.Fatal("accepted missing SHA-256")
		}
		for _, url := range []string{"http://example.com/a", "http://localhost/a", "http://127.0.0.2/a", "https://user:pass@example.com/a"} {
			if _, err := parseURL(url); err == nil {
				t.Fatalf("accepted %s", url)
			}
		}
	})
}

func TestVersionOrdering(t *testing.T) {
	for _, tc := range []struct {
		latest, installed string
		want              bool
	}{
		{"0.2.0", "0.1.0", true},
		{"0.2.0", "0.2.0-rc.1", true},
		{"0.2.0-rc.1", "0.2.0", false},
		{"0.2.0", "0.2.0", false},
		{"../0.2.0", "0.1.0", false},
		{"0.2", "0.1.0", false},
	} {
		if actual := Newer(tc.latest, tc.installed); actual != tc.want {
			t.Errorf("Newer(%s, %s) = %v, expected %v", tc.latest, tc.installed, actual, tc.want)
		}
	}
}
