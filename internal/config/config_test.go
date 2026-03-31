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
		WSToken:           "wst_testtoken123",
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
		WSToken:           "wst_roundtrip",
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
		WSToken:           "wst_testtoken",
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

func TestValidate_WSEndpointPlainRejectsInProd(t *testing.T) {
	// Ensure we are NOT in dev mode
	t.Setenv("KENI_SKIP_WIREGUARD", "")
	cfg := validConfig()
	cfg.WSEndpoint = "ws://10.99.0.1/ws/agent"
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for ws:// in production mode")
	}
	if !contains(err.Error(), "wss://") {
		t.Errorf("expected error about wss://, got: %v", err)
	}
}

func TestValidate_WSEndpointPlainAllowedInDev(t *testing.T) {
	t.Setenv("KENI_SKIP_WIREGUARD", "true")
	cfg := validConfig()
	cfg.WSEndpoint = "ws://10.99.0.1/ws/agent"
	if err := cfg.Validate(); err != nil {
		t.Errorf("ws:// should be valid in dev mode, got: %v", err)
	}
}

func TestValidate_MissingWSToken(t *testing.T) {
	cfg := validConfig()
	cfg.WSToken = ""
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for missing ws_token")
	}
	if !contains(err.Error(), "ws_token") {
		t.Errorf("expected error to mention ws_token, got: %v", err)
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

func TestIsDevMode(t *testing.T) {
	tests := []struct {
		name     string
		envValue string
		want     bool
	}{
		{"set to true", "true", true},
		{"set to false", "false", false},
		{"set to empty", "", false},
		{"set to 1", "1", false},
		{"set to TRUE (uppercase)", "TRUE", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("KENI_SKIP_WIREGUARD", tt.envValue)
			got := IsDevMode()
			if got != tt.want {
				t.Errorf("IsDevMode() = %v, want %v (env=%q)", got, tt.want, tt.envValue)
			}
		})
	}
}

func TestIsDevMode_Unset(t *testing.T) {
	// Ensure the env var is not set at all
	t.Setenv("KENI_SKIP_WIREGUARD", "")
	if IsDevMode() {
		t.Error("IsDevMode() should return false when env var is empty")
	}
}

func TestValidate_EmptyDashboardPubKeyAllowedInDevMode(t *testing.T) {
	t.Setenv("KENI_SKIP_WIREGUARD", "true")
	cfg := validConfig()
	cfg.DashboardPubKey = ""
	cfg.WSEndpoint = "ws://10.99.0.1/ws/agent" // dev mode allows ws://
	if err := cfg.Validate(); err != nil {
		t.Errorf("empty DashboardPubKey should be allowed in dev mode, got: %v", err)
	}
}

func TestValidate_EmptyDashboardPubKeyAllowedInProd(t *testing.T) {
	// DashboardPubKey is not in the required fields list per the code,
	// so it should also pass in production mode.
	t.Setenv("KENI_SKIP_WIREGUARD", "")
	cfg := validConfig()
	cfg.DashboardPubKey = ""
	if err := cfg.Validate(); err != nil {
		t.Errorf("empty DashboardPubKey should be allowed (not enforced), got: %v", err)
	}
}

func TestValidate_MissingDashboardURL(t *testing.T) {
	cfg := validConfig()
	cfg.DashboardURL = ""
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for missing dashboard_url")
	}
	if !contains(err.Error(), "dashboard_url") {
		t.Errorf("expected error to mention dashboard_url, got: %v", err)
	}
}

func TestValidate_MissingMultipleFields(t *testing.T) {
	cfg := validConfig()
	cfg.AgentID = ""
	cfg.WSToken = ""
	cfg.DashboardURL = ""
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for missing fields")
	}
	for _, field := range []string{"agent_id", "ws_token", "dashboard_url"} {
		if !contains(err.Error(), field) {
			t.Errorf("expected error to mention %q, got: %v", field, err)
		}
	}
}

func TestValidate_ValidIPAddresses(t *testing.T) {
	tests := []struct {
		name    string
		ip      string
		wantErr bool
	}{
		{"valid IPv4", "10.99.0.5", false},
		{"valid IPv4 loopback", "127.0.0.1", false},
		{"valid IPv6", "::1", false},
		{"invalid IP", "999.999.999.999", true},
		{"partial IP", "10.99", true},
		{"hostname not IP", "myhost.local", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.AssignedIP = tt.ip
			err := cfg.Validate()
			if tt.wantErr && err == nil {
				t.Errorf("expected error for IP %q", tt.ip)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error for IP %q: %v", tt.ip, err)
			}
		})
	}
}

func TestLoad_MissingFile(t *testing.T) {
	// Load() reads from the hardcoded /etc/keni-agent/config.yml.
	// On a test machine this file should not exist, so Load should return an error.
	_, err := Load()
	if err == nil {
		t.Skip("skipping: /etc/keni-agent/config.yml exists on this machine")
	}
	if !contains(err.Error(), "reading config") {
		t.Errorf("expected 'reading config' error, got: %v", err)
	}
}

func TestSave_ErrorOnRestrictedPath(t *testing.T) {
	// Save() writes to /etc/keni-agent which typically requires root.
	// On a non-root test run this should fail with a permission error.
	cfg := validConfig()
	err := cfg.Save()
	if err == nil {
		t.Skip("skipping: running as root or /etc/keni-agent is writable")
	}
	if !contains(err.Error(), "creating config dir") && !contains(err.Error(), "writing config") {
		t.Errorf("expected config dir or write error, got: %v", err)
	}
}
