package androidclient

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
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

func TestLegacyTokenMigratesAndPlaintextIsRemoved(t *testing.T) {
	dir := t.TempDir()
	if err := SaveConfig(dir, Config{HubURL: "http://hub:7443", AdminToken: "legacy-secret"}); err != nil {
		t.Fatal(err)
	}
	store := newFakeSecureStore()
	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	auth, migrated, err := loadOrMigrateAuth(dir, cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	if migrated.AdminToken != "" {
		t.Fatalf("plaintext token still in config: %q", migrated.AdminToken)
	}
	if auth.Kind != AuthKindAdmin || auth.Token != "legacy-secret" {
		t.Fatalf("auth = %+v", auth)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "client-config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "legacy-secret") {
		t.Fatalf("plaintext token remains in %s: %s", ConfigPath(dir), raw)
	}
	if _, found, err := LoadAuth(store); err != nil || !found {
		t.Fatalf("secure auth missing: found=%v err=%v", found, err)
	}
}

func TestSecureStoreFailureNeverFallsBackToPlaintext(t *testing.T) {
	dir := t.TempDir()
	if err := SaveConfig(dir, Config{AdminToken: "legacy-secret"}); err != nil {
		t.Fatal(err)
	}
	store := newFakeSecureStore()
	store.err = errors.New("keystore unavailable")
	cfg, _ := LoadConfig(dir)
	auth, _, err := loadOrMigrateAuth(dir, cfg, store)
	if err == nil {
		t.Fatal("expected migration failure")
	}
	if auth.Configured() {
		t.Fatalf("must not return plaintext auth: %+v", auth)
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
