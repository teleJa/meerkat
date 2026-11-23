package androidclient

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// SecureStore is the tiny surface the Android Keystore-backed storage exposes.
// It is an interface so config migration and 401 handling can be tested without
// an Android device.
type SecureStore interface {
	Set(key, value string) error
	Get(key string) (value string, found bool, err error)
	Delete(key string) error
}

const (
	// CredentialStorageKey holds the encrypted Auth JSON. The value never
	// reaches client-config.json.
	CredentialStorageKey = "meerkat.mobile.credential.v1"
	AuthKindMobile       = "mobile"
	ScopeDevicesRead     = "devices:read"
	ScopeTasksRead       = "tasks:read"
	ScopeHarnessProxy    = "harness:proxy"
)

// Auth is the credential the client uses for Hub requests. It is issued by mobile
// pairing. The token itself only
// exists in memory and in Android Keystore-backed storage.
type Auth struct {
	Kind         string   `json:"kind"`
	Token        string   `json:"token"`
	HubURL       string   `json:"hub_url,omitempty"`
	ProxyPort    int      `json:"proxy_port,omitempty"`
	CredentialID string   `json:"credential_id,omitempty"`
	Label        string   `json:"label,omitempty"`
	Scopes       []string `json:"scopes,omitempty"`
	CreatedAt    string   `json:"created_at,omitempty"`
	ExpiresAt    string   `json:"expires_at,omitempty"`
	LastUsedAt   string   `json:"last_used_at,omitempty"`
}

func (a Auth) Configured() bool {
	return a.IsMobile() && strings.TrimSpace(a.Token) != ""
}

func (a Auth) IsMobile() bool {
	return a.Kind == AuthKindMobile && strings.TrimSpace(a.CredentialID) != ""
}

func (a Auth) HasScope(scope string) bool {
	if !a.Configured() {
		return false
	}
	for _, granted := range a.Scopes {
		if granted == scope {
			return true
		}
	}
	return false
}

// ExpiresAtTime returns the parsed expiry for mobile credentials.
func (a Auth) ExpiresAtTime() (time.Time, bool) {
	if !a.IsMobile() || strings.TrimSpace(a.ExpiresAt) == "" {
		return time.Time{}, false
	}
	value, err := time.Parse(time.RFC3339Nano, a.ExpiresAt)
	if err != nil {
		return time.Time{}, false
	}
	return value, true
}

// DayCountdown returns whole days until expiry, clamped at zero.
func (a Auth) DayCountdown(now time.Time) (int, bool) {
	expiry, ok := a.ExpiresAtTime()
	if !ok {
		return 0, false
	}
	days := int(expiry.Sub(now).Hours() / 24)
	if days < 0 {
		days = 0
	}
	return days, true
}

// AuthState summarizes the credential for the UI.
func (a Auth) AuthState(now time.Time) string {
	if !a.Configured() {
		return "none"
	}
	expiry, ok := a.ExpiresAtTime()
	if !ok {
		return "unknown"
	}
	if !expiry.After(now) {
		return "expired"
	}
	if expiry.Sub(now) <= 7*24*time.Hour {
		return "expiring"
	}
	return "ok"
}

func LoadAuth(store SecureStore) (Auth, bool, error) {
	if store == nil {
		return Auth{}, false, fmt.Errorf("secure storage unavailable")
	}
	raw, found, err := store.Get(CredentialStorageKey)
	if err != nil {
		return Auth{}, false, err
	}
	if !found {
		return Auth{}, false, nil
	}
	var auth Auth
	if err := json.Unmarshal([]byte(raw), &auth); err != nil {
		return Auth{}, true, fmt.Errorf("credential is unreadable: %w", err)
	}
	auth.Kind = strings.TrimSpace(auth.Kind)
	auth.Token = strings.TrimSpace(auth.Token)
	auth.CredentialID = strings.TrimSpace(auth.CredentialID)
	if !auth.IsMobile() {
		return Auth{HubURL: auth.HubURL, ProxyPort: auth.ProxyPort}, true, nil
	}
	if auth.Token == "" {
		if auth.HubURL != "" {
			return Auth{HubURL: auth.HubURL, ProxyPort: auth.ProxyPort}, true, nil
		}
		return Auth{}, true, fmt.Errorf("credential is empty")
	}
	return auth, true, nil
}

func SaveAuth(store SecureStore, auth Auth) error {
	if store == nil {
		return fmt.Errorf("secure storage unavailable")
	}
	if auth.Token != "" && !auth.IsMobile() {
		return fmt.Errorf("invalid mobile credential")
	}
	if !auth.Configured() && auth.HubURL == "" {
		return fmt.Errorf("credential is empty")
	}
	raw, err := json.Marshal(auth)
	if err != nil {
		return err
	}
	return store.Set(CredentialStorageKey, string(raw))
}

func DeleteAuth(store SecureStore) error {
	if store == nil {
		return fmt.Errorf("secure storage unavailable")
	}
	return store.Delete(CredentialStorageKey)
}
