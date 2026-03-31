package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	ConfigDir  = "/etc/keni-agent"
	ConfigFile = "config.yml"
)

// Config holds the agent's persistent configuration.
type Config struct {
	AgentID           string `yaml:"agent_id"`
	AssignedIP        string `yaml:"assigned_ip"`
	DashboardEndpoint string `yaml:"dashboard_endpoint"`
	WSEndpoint        string `yaml:"ws_endpoint"`
	WireGuardPrivKey  string `yaml:"wireguard_private_key"`
	WireGuardPubKey   string `yaml:"wireguard_public_key"`
	DashboardPubKey   string `yaml:"dashboard_public_key"`
	DashboardURL      string `yaml:"dashboard_url"`
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
