package androidclient

import (
	"errors"
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

func TestParseHubAddress(t *testing.T) {
	for input, want := range map[string]string{
		" 192.0.2.34:7443 ":         "http://192.0.2.34:7443",
		"Hub.Example.:07443":        "http://hub.example:7443",
		"[fd7a:115c:a1e0::40]:7443": "http://[fd7a:115c:a1e0::40]:7443",
		"hub:1":                     "http://hub:1",
	} {
		if got, err := ParseHubAddress(input); err != nil || got != want {
			t.Errorf("ParseHubAddress(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	for _, input := range []string{
		"", "hub", ":7443", "hub:0", "hub:65536", "hub:+7443", "http://hub:7443",
		"user@hub:7443", "hub:7443/path", "hub:7443?next=elsewhere", "hub:7443#x",
		"hub..example:7443", "-hub:7443", "[hub]:7443", "[192.0.2.1]:7443", "[fe80::1%lo]:7443", "fd00::1:7443",
	} {
		if got, err := ParseHubAddress(input); got != "" || !errors.Is(err, ErrInvalidHubAddress) {
			t.Errorf("ParseHubAddress(%q) = %q, %v; want invalid address", input, got, err)
		}
	}
}

func TestHubBindingRestoresFromSecureStorage(t *testing.T) {
	dir := t.TempDir()
	legacy := Config{HubURL: "http://legacy.example:7443"}
	store := newFakeSecureStore()
	const savedURL = "http://new.example:7443"
	for _, wantAuth := range []Auth{
		{Kind: AuthKindAdmin, Token: "legacy-test-token"},
		{Kind: AuthKindMobile, Token: "paired-test-token", HubURL: savedURL, ProxyPort: 18924},
		{HubURL: savedURL, ProxyPort: 18924},
	} {
		if err := SaveAuth(store, wantAuth); err != nil {
			t.Fatal(err)
		}
		auth, config, err := loadOrMigrateAuth(dir, legacy, store)
		if err != nil {
			t.Fatal(err)
		}
		wantURL := wantAuth.HubURL
		if wantURL == "" {
			wantURL = legacy.HubURL
		}
		if config.HubURL != wantURL || auth.Token != wantAuth.Token || auth.ProxyPort != wantAuth.ProxyPort {
			t.Errorf("restored wrong Hub binding for %q", wantAuth.Kind)
		}
	}
}
