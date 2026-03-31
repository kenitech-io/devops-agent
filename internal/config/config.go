package config

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// CurrentConfigVersion is the latest config schema version.
const CurrentConfigVersion = 1

// IsDevMode returns true when WireGuard is skipped (Tailscale/local dev).
func IsDevMode() bool {
	return os.Getenv("KENI_SKIP_WIREGUARD") == "true"
}

const (
	ConfigDir  = "/etc/keni-agent"
	ConfigFile = "config.yml"
)

// Config holds the agent's persistent configuration.
type Config struct {
	ConfigVersion     int    `yaml:"config_version"`
	AgentID           string `yaml:"agent_id"`
	AssignedIP        string `yaml:"assigned_ip"`
	DashboardEndpoint string `yaml:"dashboard_endpoint"`
	WSEndpoint        string `yaml:"ws_endpoint"`
	WSToken           string `yaml:"ws_token"`
	WireGuardPrivKey  string `yaml:"wireguard_private_key"`
	WireGuardPubKey   string `yaml:"wireguard_public_key"`
	DashboardPubKey   string `yaml:"dashboard_public_key"`
	DashboardURL      string `yaml:"dashboard_url"`
}

// saveFunc is the function used to persist config. Override in tests.
var saveFunc func(c *Config) error

func init() {
	saveFunc = (*Config).Save
}

// Migrate updates old config files to the current schema version.
// It sets defaults for new fields and persists the result.
func (c *Config) Migrate() error {
	if c.ConfigVersion >= CurrentConfigVersion {
		return nil
	}

	slog.Info("migrating config", "from_version", c.ConfigVersion, "to_version", CurrentConfigVersion)

	// Migration from v0 to v1:
	// ws_token may be empty on old configs. That is acceptable, the agent
	// will need to re-register with the dashboard to obtain a new token.
	if c.ConfigVersion < 1 {
		slog.Info("config migration v0->v1: setting config_version, ws_token may be empty (re-register required)")
		c.ConfigVersion = 1
	}

	if err := saveFunc(c); err != nil {
		return fmt.Errorf("saving migrated config: %w", err)
	}

	slog.Info("config migration complete", "version", c.ConfigVersion)
	return nil
}

// Load reads the config from /etc/keni-agent/config.yml.
func Load() (*Config, error) {
	path := filepath.Join(ConfigDir, ConfigFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config %s: %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	if cfg.AgentID == "" {
		return nil, fmt.Errorf("config missing agent_id")
	}

	if cfg.ConfigVersion < CurrentConfigVersion {
		if err := cfg.Migrate(); err != nil {
			return nil, fmt.Errorf("migrating config: %w", err)
		}
	}

	return &cfg, nil
}

// Validate checks that all required fields are present and sane.
func (c *Config) Validate() error {
	var missing []string

	if c.AgentID == "" {
		missing = append(missing, "agent_id")
	}
	if c.AssignedIP == "" {
		missing = append(missing, "assigned_ip")
	} else if net.ParseIP(c.AssignedIP) == nil {
		return fmt.Errorf("invalid assigned_ip: %q is not a valid IP address", c.AssignedIP)
	}
	if c.DashboardEndpoint == "" {
		missing = append(missing, "dashboard_endpoint")
	}
	if c.WSEndpoint == "" {
		missing = append(missing, "ws_endpoint")
	} else if !strings.HasPrefix(c.WSEndpoint, "ws://") && !strings.HasPrefix(c.WSEndpoint, "wss://") {
		return fmt.Errorf("invalid ws_endpoint: %q must start with ws:// or wss://", c.WSEndpoint)
	} else if !IsDevMode() && strings.HasPrefix(c.WSEndpoint, "ws://") {
		return fmt.Errorf("ws_endpoint must use wss:// in production (ws:// only allowed in dev mode with KENI_SKIP_WIREGUARD=true)")
	}
	if c.WSToken == "" {
		slog.Warn("config: ws_token is empty, agent will need to re-register with the dashboard")
	}
	if c.WireGuardPrivKey == "" {
		missing = append(missing, "wireguard_private_key")
	}
	if c.WireGuardPubKey == "" {
		missing = append(missing, "wireguard_public_key")
	}
	// DashboardPubKey can be empty in dev mode (no WireGuard)
	if c.DashboardURL == "" {
		missing = append(missing, "dashboard_url")
	}

	if len(missing) > 0 {
		return fmt.Errorf("config missing required fields: %s", strings.Join(missing, ", "))
	}
	return nil
}

// ApplyPartialUpdate applies non-empty fields from a remote config update.
// Returns true if any field was changed.
func (c *Config) ApplyPartialUpdate(wsEndpoint, wsToken, dashboardURL string) bool {
	changed := false
	if wsEndpoint != "" && wsEndpoint != c.WSEndpoint {
		c.WSEndpoint = wsEndpoint
		changed = true
	}
	if wsToken != "" && wsToken != c.WSToken {
		c.WSToken = wsToken
		changed = true
	}
	if dashboardURL != "" && dashboardURL != c.DashboardURL {
		c.DashboardURL = dashboardURL
		changed = true
	}
	return changed
}

// Save writes the config to /etc/keni-agent/config.yml with restricted permissions.
func (c *Config) Save() error {
	if err := os.MkdirAll(ConfigDir, 0750); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}

	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}

	path := filepath.Join(ConfigDir, ConfigFile)
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}

	return nil
}
