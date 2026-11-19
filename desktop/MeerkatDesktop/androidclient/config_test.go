package androidclient

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Configured() {
		t.Fatal("fresh config should not be configured")
	}
	if cfg.HubURL != DefaultHubURL {
		t.Fatalf("hub url = %q", cfg.HubURL)
	}

	cfg.HubURL = "  http://192.0.2.20:7443  "
	cfg.AdminToken = "  test-token  "
	if err := SaveConfig(dir, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.HubURL != "http://192.0.2.20:7443" {
		t.Fatalf("hub url = %q", loaded.HubURL)
	}
	if loaded.AdminToken != "test-token" {
		t.Fatalf("token = %q", loaded.AdminToken)
	}
	info, err := os.Stat(filepath.Join(dir, "client-config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("config mode = %o", info.Mode().Perm())
	}
}

func TestDecodePathParam(t *testing.T) {
	value := "https://login.tailscale.com/a/abc123"
	encoded := EncodePathParam(value)
	got, err := DecodePathParam("/client/open", "/client/open/"+encoded)
	if err != nil {
		t.Fatal(err)
	}
	if got != value {
		t.Fatalf("decoded = %q", got)
	}
	if _, err := DecodePathParam("/client/open", "/client/open"); err == nil {
		t.Fatal("missing parameter should fail")
	}
	if _, err := DecodePathParam("/client/open", "/client/open/%%%"); err == nil {
		t.Fatal("invalid base64 should fail")
	}
}
