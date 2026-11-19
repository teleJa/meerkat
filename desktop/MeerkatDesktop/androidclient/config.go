package androidclient

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// HubAddr is the documentation address shown until a Hub is configured.
const HubAddr = "192.0.2.10:7443"

// DefaultHubURL is used when the local configuration has no HubURL.
const DefaultHubURL = "http://" + HubAddr

// Config is the durable client configuration. The admin token is stored in the
// app's private data directory (mode 0600); it is never compiled into the APK.
type Config struct {
	HubURL     string `json:"hub_url,omitempty"`
	AdminToken string `json:"admin_token,omitempty"`
}

// Configured reports whether the client has an admin token to call the Hub.
func (c Config) Configured() bool {
	return strings.TrimSpace(c.AdminToken) != ""
}

func normalizeConfig(cfg Config) Config {
	cfg.HubURL = strings.TrimSpace(cfg.HubURL)
	cfg.AdminToken = strings.TrimSpace(cfg.AdminToken)
	if cfg.HubURL == "" {
		cfg.HubURL = DefaultHubURL
	}
	return cfg
}

// ConfigPath returns the config file path under the app's private directory.
func ConfigPath(dataDir string) string {
	return filepath.Join(dataDir, "client-config.json")
}

// LoadConfig reads the client config. A missing file is not an error: it
// returns a default config with no token.
func LoadConfig(dataDir string) (Config, error) {
	path := ConfigPath(dataDir)
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return normalizeConfig(Config{}), nil
	}
	if err != nil {
		return normalizeConfig(Config{}), err
	}
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return normalizeConfig(Config{}), err
	}
	return normalizeConfig(cfg), nil
}

// SaveConfig writes the config atomically with 0600 permissions.
func SaveConfig(dataDir string, cfg Config) error {
	cfg = normalizeConfig(cfg)
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := ConfigPath(dataDir) + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, ConfigPath(dataDir)); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Chmod(ConfigPath(dataDir), 0o600)
}

// ClearConfig removes the stored token and hub override.
func ClearConfig(dataDir string) error {
	err := os.Remove(ConfigPath(dataDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
