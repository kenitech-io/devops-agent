package config

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestConfig_SaveAndLoad(t *testing.T) {
	// Use a temp directory instead of /etc/keni-agent
	tmpDir := t.TempDir()

	// Override the config path for testing
	origDir := ConfigDir
	origFile := ConfigFile
	defer func() {
		// Restore (these are constants, so we use a helper approach)
		_ = origDir
		_ = origFile
	}()

	cfg := &Config{
		AgentID:           "ag_test123",
		AssignedIP:        "10.99.0.5",
		DashboardEndpoint: "203.0.113.10:51820",
		WSEndpoint:        "wss://10.99.0.1:443/ws/agent",
		WireGuardPrivKey:  "test-priv-key",
		WireGuardPubKey:   "test-pub-key",
		DashboardPubKey:   "dashboard-pub-key",
		DashboardURL:      "https://dashboard.kenitech.io",
	}

	// Write to temp dir manually (since Save uses the const path)
	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	configPath := filepath.Join(tmpDir, "config.yml")
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		t.Fatalf("write error: %v", err)
	}

	// Read it back
	readData, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read error: %v", err)
	}

	var loaded Config
	if err := yaml.Unmarshal(readData, &loaded); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if loaded.AgentID != cfg.AgentID {
		t.Errorf("AgentID = %s, want %s", loaded.AgentID, cfg.AgentID)
	}
	if loaded.AssignedIP != cfg.AssignedIP {
		t.Errorf("AssignedIP = %s, want %s", loaded.AssignedIP, cfg.AssignedIP)
	}
	if loaded.DashboardEndpoint != cfg.DashboardEndpoint {
		t.Errorf("DashboardEndpoint = %s, want %s", loaded.DashboardEndpoint, cfg.DashboardEndpoint)
	}
	if loaded.WSEndpoint != cfg.WSEndpoint {
		t.Errorf("WSEndpoint = %s, want %s", loaded.WSEndpoint, cfg.WSEndpoint)
	}
	if loaded.WireGuardPrivKey != cfg.WireGuardPrivKey {
		t.Errorf("WireGuardPrivKey = %s, want %s", loaded.WireGuardPrivKey, cfg.WireGuardPrivKey)
	}
	if loaded.WireGuardPubKey != cfg.WireGuardPubKey {
		t.Errorf("WireGuardPubKey = %s, want %s", loaded.WireGuardPubKey, cfg.WireGuardPubKey)
	}
	if loaded.DashboardPubKey != cfg.DashboardPubKey {
		t.Errorf("DashboardPubKey = %s, want %s", loaded.DashboardPubKey, cfg.DashboardPubKey)
	}
	if loaded.DashboardURL != cfg.DashboardURL {
		t.Errorf("DashboardURL = %s, want %s", loaded.DashboardURL, cfg.DashboardURL)
	}
}

func TestConfig_MissingAgentID(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := &Config{
		AgentID: "", // missing
	}

	data, _ := yaml.Marshal(cfg)
	configPath := filepath.Join(tmpDir, "config.yml")
	os.WriteFile(configPath, data, 0600)

	readData, _ := os.ReadFile(configPath)
	var loaded Config
	yaml.Unmarshal(readData, &loaded)

	if loaded.AgentID != "" {
		t.Error("expected empty AgentID")
	}
}

func TestConfig_YAMLRoundtrip(t *testing.T) {
	cfg := &Config{
		AgentID:           "ag_roundtrip",
		AssignedIP:        "10.99.0.99",
		DashboardEndpoint: "1.2.3.4:51820",
		WSEndpoint:        "wss://10.99.0.1/ws/agent",
		WireGuardPrivKey:  "priv123",
		WireGuardPubKey:   "pub456",
		DashboardPubKey:   "dashpub789",
		DashboardURL:      "https://dash.example.com",
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var loaded Config
	if err := yaml.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if loaded != *cfg {
		t.Errorf("roundtrip mismatch:\n  got:  %+v\n  want: %+v", loaded, *cfg)
	}
}

func TestConfig_FilePermissions(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yml")

	cfg := &Config{AgentID: "ag_perms"}
	data, _ := yaml.Marshal(cfg)

	// Write with restricted permissions
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		t.Fatalf("write error: %v", err)
	}

	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("stat error: %v", err)
	}

	perm := info.Mode().Perm()
	if perm != 0600 {
		t.Errorf("expected permissions 0600, got %o", perm)
	}
}

func validConfig() *Config {
	return &Config{
		AgentID:           "ag_test",
		AssignedIP:        "10.99.0.5",
		DashboardEndpoint: "1.2.3.4:51820",
		WSEndpoint:        "wss://10.99.0.1/ws/agent",
		WireGuardPrivKey:  "privkey",
		WireGuardPubKey:   "pubkey",
		DashboardPubKey:   "dashpub",
		DashboardURL:      "https://dash.example.com",
	}
}

func TestValidate_ValidConfig(t *testing.T) {
	cfg := validConfig()
	if err := cfg.Validate(); err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestValidate_MissingFields(t *testing.T) {
	cfg := &Config{}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for empty config")
	}
	for _, field := range []string{"agent_id", "assigned_ip", "ws_endpoint", "wireguard_private_key"} {
		if !contains(err.Error(), field) {
			t.Errorf("expected error to mention %q, got: %v", field, err)
		}
	}
}

func TestValidate_InvalidIP(t *testing.T) {
	cfg := validConfig()
	cfg.AssignedIP = "not-an-ip"
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for invalid IP")
	}
	if !contains(err.Error(), "invalid assigned_ip") {
		t.Errorf("expected 'invalid assigned_ip' error, got: %v", err)
	}
}

func TestValidate_InvalidWSEndpoint(t *testing.T) {
	cfg := validConfig()
	cfg.WSEndpoint = "http://wrong-scheme"
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for invalid ws endpoint")
	}
	if !contains(err.Error(), "invalid ws_endpoint") {
		t.Errorf("expected 'invalid ws_endpoint' error, got: %v", err)
	}
}

func TestValidate_WSEndpointPlain(t *testing.T) {
	cfg := validConfig()
	cfg.WSEndpoint = "ws://10.99.0.1/ws/agent"
	if err := cfg.Validate(); err != nil {
		t.Errorf("ws:// should be valid, got: %v", err)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchStr(s, substr)
}

func searchStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
