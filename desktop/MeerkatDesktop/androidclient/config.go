package androidclient

import (
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// HubAddr is the documentation address shown until a Hub is configured.
const HubAddr = "192.0.2.10:7443"

// DefaultHubURL is used when the local configuration has no HubURL.
const DefaultHubURL = "http://" + HubAddr

var (
	ErrInvalidHubAddress = errors.New("请输入主机名或 IP 地址加端口，例如 hub.example:7443")
	dnsLabel             = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
	decimalPort          = regexp.MustCompile(`^[0-9]{1,5}$`)
)

// ParseHubAddress validates and canonicalizes the host:port shown in Settings.
func ParseHubAddress(raw string) (string, error) {
	address := strings.TrimSpace(raw)
	host, port, err := net.SplitHostPort(address)
	if err != nil || !decimalPort.MatchString(port) {
		return "", ErrInvalidHubAddress
	}
	portNumber, err := strconv.ParseUint(port, 10, 16)
	if err != nil || portNumber == 0 {
		return "", ErrInvalidHubAddress
	}
	if ip, err := netip.ParseAddr(host); err == nil && ip.Zone() == "" && strings.HasPrefix(address, "[") == ip.Is6() {
		host = ip.String()
	} else {
		host = strings.TrimSuffix(strings.ToLower(host), ".")
		if len(host) > 253 || strings.HasPrefix(address, "[") {
			return "", ErrInvalidHubAddress
		}
		for _, label := range strings.Split(host, ".") {
			if !dnsLabel.MatchString(label) {
				return "", ErrInvalidHubAddress
			}
		}
	}
	return "http://" + net.JoinHostPort(host, strconv.FormatUint(portNumber, 10)), nil
}

// Config stores the active Hub address in the app’s private directory.
type Config struct {
	HubURL string `json:"hub_url,omitempty"`
}

func normalizeConfig(cfg Config) Config {
	cfg.HubURL = strings.TrimSpace(cfg.HubURL)
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

// ClearConfig removes the hub override.
func ClearConfig(dataDir string) error {
	err := os.Remove(ConfigPath(dataDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
