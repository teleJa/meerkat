package androidclient

import (
	"errors"
	"os"
	"testing"
	"time"
)

type fakeSecureStore struct {
	values map[string]string
	err    error
}

func newFakeSecureStore() *fakeSecureStore {
	return &fakeSecureStore{values: map[string]string{}}
}

func (s *fakeSecureStore) Set(key, value string) error {
	if s.err != nil {
		return s.err
	}
	s.values[key] = value
	return nil
}

func (s *fakeSecureStore) Get(key string) (string, bool, error) {
	if s.err != nil {
		return "", false, s.err
	}
	value, ok := s.values[key]
	return value, ok, nil
}

func (s *fakeSecureStore) Delete(key string) error {
	if s.err != nil {
		return s.err
	}
	delete(s.values, key)
	return nil
}

func TestLegacyCredentialsAreIgnored(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(ConfigPath(dir), []byte(`{"hub_url":"http://hub:7443","admin_token":"obsolete"}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(dir)
	if err != nil || cfg.HubURL != "http://hub:7443" {
		t.Fatalf("legacy configuration: %+v, %v", cfg, err)
	}
	for _, kind := range []string{"admin", "", "unknown"} {
		store := newFakeSecureStore()
		raw := `{"kind":"` + kind + `","token":"obsolete","hub_url":"http://saved:7443","proxy_port":18924}`
		store.values[CredentialStorageKey] = raw
		auth, found, err := LoadAuth(store)
		if err != nil || !found || auth.Token != "" || auth.AuthState(time.Now()) != "none" || auth.HasScope(ScopeDevicesRead) || auth.HubURL != "http://saved:7443" || auth.ProxyPort != 18924 || store.values[CredentialStorageKey] != raw {
			t.Fatalf("unexpected legacy state for kind %q", kind)
		}
	}
}

func TestPairedCredentialRoundTrip(t *testing.T) {
	store := newFakeSecureStore()
	want := Auth{Kind: AuthKindMobile, CredentialID: "paired", Token: "mobile", HubURL: "http://paired:7443", ProxyPort: 18924, Scopes: []string{ScopeDevicesRead}}
	if err := SaveAuth(store, want); err != nil {
		t.Fatal(err)
	}
	got, found, err := LoadAuth(store)
	if err != nil || !found || !got.Configured() || got.Token != want.Token || got.HubURL != want.HubURL || got.ProxyPort != want.ProxyPort || !got.HasScope(ScopeDevicesRead) {
		t.Fatal("paired credential not restored")
	}
	store.err = errors.New("keystore unavailable")
	if auth, _, err := LoadAuth(store); err == nil || auth.Configured() {
		t.Fatal("unavailable keystore must fail closed")
	}
}

func TestMobileAuthStateAndExpiryWindow(t *testing.T) {
	now := time.Now()
	auth := Auth{
		Kind: AuthKindMobile, Token: "mkm_x_secret", CredentialID: "test-credential",
		Scopes:    []string{ScopeDevicesRead, ScopeTasksRead, ScopeHarnessProxy},
		ExpiresAt: now.Add(8 * 24 * time.Hour).UTC().Format(time.RFC3339Nano),
	}
	if auth.AuthState(now) != "ok" {
		t.Fatalf("state = %s", auth.AuthState(now))
	}
	auth.ExpiresAt = now.Add(6 * 24 * time.Hour).UTC().Format(time.RFC3339Nano)
	if auth.AuthState(now) != "expiring" {
		t.Fatalf("state = %s", auth.AuthState(now))
	}
	if days, ok := auth.DayCountdown(now); !ok || days != 6 {
		t.Fatalf("days = %d ok=%v", days, ok)
	}
	auth.ExpiresAt = now.Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	if auth.AuthState(now) != "expired" {
		t.Fatalf("state = %s", auth.AuthState(now))
	}
	if !auth.HasScope(ScopeDevicesRead) || auth.HasScope("devices:write") {
		t.Fatal("scope check failed")
	}
}
