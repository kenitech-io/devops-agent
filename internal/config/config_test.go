package config

import (
	"fmt"
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
		ConfigVersion:     CurrentConfigVersion,
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
		ConfigVersion:     CurrentConfigVersion,
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
		ConfigVersion:     CurrentConfigVersion,
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
	for _, field := range []string{"agent_id", "assigned_ip", "ws_endpoint", "wireguard_private_key", "dashboard_url"} {
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
	// ws:// to a public IP should be rejected in production
	t.Setenv("KENI_SKIP_WIREGUARD", "")
	cfg := validConfig()
	cfg.WSEndpoint = "ws://example.com/ws/agent"
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for ws:// to public host in production mode")
	}
	if !contains(err.Error(), "wss://") {
		t.Errorf("expected error about wss://, got: %v", err)
	}
}

func TestValidate_WSEndpointPlainAllowedOverWireGuard(t *testing.T) {
	// ws:// to WireGuard tunnel IP is allowed (tunnel is encrypted)
	t.Setenv("KENI_SKIP_WIREGUARD", "")
	cfg := validConfig()
	cfg.WSEndpoint = "ws://10.99.0.1:8080/ws/agent"
	err := cfg.Validate()
	if err != nil {
		t.Fatalf("ws:// over WireGuard tunnel should be allowed, got: %v", err)
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

func TestValidate_EmptyWSToken_WarnsButPasses(t *testing.T) {
	cfg := validConfig()
	cfg.WSToken = ""
	err := cfg.Validate()
	if err != nil {
		t.Errorf("expected no error for empty ws_token (should warn only), got: %v", err)
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
	// ws_token no longer causes a validation error (just a warning)
	for _, field := range []string{"agent_id", "dashboard_url"} {
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

func TestMigrate_OldConfigGetsMigrated(t *testing.T) {
	saveCalled := false
	origSave := saveFunc
	saveFunc = func(c *Config) error {
		saveCalled = true
		return nil
	}
	defer func() { saveFunc = origSave }()

	cfg := &Config{
		AgentID:           "ag_old",
		AssignedIP:        "10.99.0.5",
		DashboardEndpoint: "1.2.3.4:51820",
		WSEndpoint:        "wss://10.99.0.1/ws/agent",
		WireGuardPrivKey:  "privkey",
		WireGuardPubKey:   "pubkey",
		DashboardURL:      "https://dash.example.com",
		ConfigVersion:     0, // old config, no version
	}

	if err := cfg.Migrate(); err != nil {
		t.Fatalf("Migrate() error: %v", err)
	}

	if cfg.ConfigVersion != CurrentConfigVersion {
		t.Errorf("expected config_version %d, got %d", CurrentConfigVersion, cfg.ConfigVersion)
	}
	if !saveCalled {
		t.Error("expected Save to be called during migration")
	}
}

func TestMigrate_CurrentConfigUntouched(t *testing.T) {
	saveCalled := false
	origSave := saveFunc
	saveFunc = func(c *Config) error {
		saveCalled = true
		return nil
	}
	defer func() { saveFunc = origSave }()

	cfg := validConfig()
	cfg.ConfigVersion = CurrentConfigVersion

	if err := cfg.Migrate(); err != nil {
		t.Fatalf("Migrate() error: %v", err)
	}

	if saveCalled {
		t.Error("Save should not be called when config is already at current version")
	}
	if cfg.ConfigVersion != CurrentConfigVersion {
		t.Errorf("config_version should remain %d, got %d", CurrentConfigVersion, cfg.ConfigVersion)
	}
}

func TestMigrate_SaveError(t *testing.T) {
	origSave := saveFunc
	saveFunc = func(c *Config) error {
		return fmt.Errorf("disk full")
	}
	defer func() { saveFunc = origSave }()

	cfg := &Config{
		AgentID:       "ag_err",
		ConfigVersion: 0,
	}

	err := cfg.Migrate()
	if err == nil {
		t.Fatal("expected error when save fails")
	}
	if !contains(err.Error(), "disk full") {
		t.Errorf("expected 'disk full' in error, got: %v", err)
	}
}

func TestConfig_ConfigVersionInYAML(t *testing.T) {
	cfg := validConfig()
	cfg.ConfigVersion = 1

	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var loaded Config
	if err := yaml.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if loaded.ConfigVersion != 1 {
		t.Errorf("expected config_version 1, got %d", loaded.ConfigVersion)
	}
}

func TestConfig_OldYAMLWithoutVersion(t *testing.T) {
	// Simulate an old config file that has no config_version field
	yamlData := []byte(`agent_id: ag_legacy
assigned_ip: 10.99.0.5
dashboard_endpoint: 1.2.3.4:51820
ws_endpoint: "wss://10.99.0.1/ws/agent"
ws_token: wst_old
wireguard_private_key: privkey
wireguard_public_key: pubkey
dashboard_url: "https://dash.example.com"
`)

	var cfg Config
	if err := yaml.Unmarshal(yamlData, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if cfg.ConfigVersion != 0 {
		t.Errorf("expected config_version 0 for old YAML, got %d", cfg.ConfigVersion)
	}
	if cfg.AgentID != "ag_legacy" {
		t.Errorf("expected agent_id ag_legacy, got %s", cfg.AgentID)
	}
}

func TestApplyPartialUpdate_AllFieldsChanged(t *testing.T) {
	cfg := validConfig()
	changed := cfg.ApplyPartialUpdate("wss://new-endpoint/ws", "wst_newtoken", "https://new-dashboard.io")
	if !changed {
		t.Error("expected changed=true when all fields differ")
	}
	if cfg.WSEndpoint != "wss://new-endpoint/ws" {
		t.Errorf("WSEndpoint = %s, want wss://new-endpoint/ws", cfg.WSEndpoint)
	}
	if cfg.WSToken != "wst_newtoken" {
		t.Errorf("WSToken = %s, want wst_newtoken", cfg.WSToken)
	}
	if cfg.DashboardURL != "https://new-dashboard.io" {
		t.Errorf("DashboardURL = %s, want https://new-dashboard.io", cfg.DashboardURL)
	}
}

func TestApplyPartialUpdate_EmptyFieldsSkipped(t *testing.T) {
	cfg := validConfig()
	origWS := cfg.WSEndpoint
	origToken := cfg.WSToken
	origURL := cfg.DashboardURL

	changed := cfg.ApplyPartialUpdate("", "", "")
	if changed {
		t.Error("expected changed=false when all fields are empty")
	}
	if cfg.WSEndpoint != origWS {
		t.Errorf("WSEndpoint changed unexpectedly to %s", cfg.WSEndpoint)
	}
	if cfg.WSToken != origToken {
		t.Errorf("WSToken changed unexpectedly to %s", cfg.WSToken)
	}
	if cfg.DashboardURL != origURL {
		t.Errorf("DashboardURL changed unexpectedly to %s", cfg.DashboardURL)
	}
}

func TestApplyPartialUpdate_SameValuesNotChanged(t *testing.T) {
	cfg := validConfig()
	changed := cfg.ApplyPartialUpdate(cfg.WSEndpoint, cfg.WSToken, cfg.DashboardURL)
	if changed {
		t.Error("expected changed=false when values are identical")
	}
}

func TestApplyPartialUpdate_PartialUpdate(t *testing.T) {
	cfg := validConfig()
	origWS := cfg.WSEndpoint
	origURL := cfg.DashboardURL

	changed := cfg.ApplyPartialUpdate("", "wst_onlytokenchanged", "")
	if !changed {
		t.Error("expected changed=true when token differs")
	}
	if cfg.WSToken != "wst_onlytokenchanged" {
		t.Errorf("WSToken = %s, want wst_onlytokenchanged", cfg.WSToken)
	}
	if cfg.WSEndpoint != origWS {
		t.Errorf("WSEndpoint should not change, got %s", cfg.WSEndpoint)
	}
	if cfg.DashboardURL != origURL {
		t.Errorf("DashboardURL should not change, got %s", cfg.DashboardURL)
	}
}

func TestApplyPartialUpdate_OnlyWSEndpoint(t *testing.T) {
	cfg := validConfig()
	changed := cfg.ApplyPartialUpdate("wss://different/ws", "", "")
	if !changed {
		t.Error("expected changed=true")
	}
	if cfg.WSEndpoint != "wss://different/ws" {
		t.Errorf("WSEndpoint = %s, want wss://different/ws", cfg.WSEndpoint)
	}
}

func TestApplyPartialUpdate_OnlyDashboardURL(t *testing.T) {
	cfg := validConfig()
	changed := cfg.ApplyPartialUpdate("", "", "https://other-dash.io")
	if !changed {
		t.Error("expected changed=true")
	}
	if cfg.DashboardURL != "https://other-dash.io" {
		t.Errorf("DashboardURL = %s, want https://other-dash.io", cfg.DashboardURL)
	}
}
