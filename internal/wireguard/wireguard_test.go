package wireguard

import (
	"strings"
	"testing"
)

func TestGenerateConfig(t *testing.T) {
	cfg := Config{
		PrivateKey:        "test-private-key-base64",
		AssignedIP:        "10.99.0.5",
		DashboardPubKey:   "dashboard-public-key-base64",
		DashboardEndpoint: "203.0.113.10:51820",
	}

	content, err := GenerateConfig(cfg)
	if err != nil {
		t.Fatalf("GenerateConfig() error: %v", err)
	}

	// Verify all required fields are in the config
	checks := []struct {
		name     string
		expected string
	}{
		{"interface address", "Address = 10.99.0.5/32"},
		{"private key", "PrivateKey = test-private-key-base64"},
		{"peer public key", "PublicKey = dashboard-public-key-base64"},
		{"endpoint", "Endpoint = 203.0.113.10:51820"},
		{"allowed IPs", "AllowedIPs = 10.99.0.1/32"},
		{"keepalive", "PersistentKeepalive = 25"},
	}

	for _, check := range checks {
		if !strings.Contains(content, check.expected) {
			t.Errorf("config missing %s: expected %q in:\n%s", check.name, check.expected, content)
		}
	}
}

func TestGenerateConfig_Format(t *testing.T) {
	cfg := Config{
		PrivateKey:        "privkey",
		AssignedIP:        "10.99.0.2",
		DashboardPubKey:   "pubkey",
		DashboardEndpoint: "1.2.3.4:51820",
	}

	content, err := GenerateConfig(cfg)
	if err != nil {
		t.Fatalf("GenerateConfig() error: %v", err)
	}

	// Should have [Interface] and [Peer] sections
	if !strings.Contains(content, "[Interface]") {
		t.Error("missing [Interface] section")
	}
	if !strings.Contains(content, "[Peer]") {
		t.Error("missing [Peer] section")
	}

	// Should not contain any em dash
	if strings.Contains(content, "\u2014") {
		t.Error("config contains em dash character")
	}
}

func TestGenerateConfig_VariousInputs(t *testing.T) {
	tests := []struct {
		name   string
		cfg    Config
		wantIP string
		wantEP string
	}{
		{
			name: "standard config",
			cfg: Config{
				PrivateKey:        "abc123privkey",
				AssignedIP:        "10.99.0.10",
				DashboardPubKey:   "abc123pubkey",
				DashboardEndpoint: "5.6.7.8:51820",
			},
			wantIP: "Address = 10.99.0.10/32",
			wantEP: "Endpoint = 5.6.7.8:51820",
		},
		{
			name: "different subnet IP",
			cfg: Config{
				PrivateKey:        "key1",
				AssignedIP:        "10.99.0.200",
				DashboardPubKey:   "key2",
				DashboardEndpoint: "100.0.0.1:51820",
			},
			wantIP: "Address = 10.99.0.200/32",
			wantEP: "Endpoint = 100.0.0.1:51820",
		},
		{
			name: "hostname endpoint",
			cfg: Config{
				PrivateKey:        "privkeydata",
				AssignedIP:        "10.99.0.3",
				DashboardPubKey:   "pubkeydata",
				DashboardEndpoint: "dashboard.kenitech.io:51820",
			},
			wantIP: "Address = 10.99.0.3/32",
			wantEP: "Endpoint = dashboard.kenitech.io:51820",
		},
		{
			name: "empty fields still render",
			cfg: Config{
				PrivateKey:        "",
				AssignedIP:        "",
				DashboardPubKey:   "",
				DashboardEndpoint: "",
			},
			wantIP: "Address = /32",
			wantEP: "Endpoint = ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content, err := GenerateConfig(tt.cfg)
			if err != nil {
				t.Fatalf("GenerateConfig() error: %v", err)
			}
			if !strings.Contains(content, tt.wantIP) {
				t.Errorf("expected IP line %q in:\n%s", tt.wantIP, content)
			}
			if !strings.Contains(content, tt.wantEP) {
				t.Errorf("expected endpoint line %q in:\n%s", tt.wantEP, content)
			}
		})
	}
}

func TestGenerateConfig_SectionOrder(t *testing.T) {
	cfg := Config{
		PrivateKey:        "key",
		AssignedIP:        "10.99.0.1",
		DashboardPubKey:   "pub",
		DashboardEndpoint: "1.2.3.4:51820",
	}

	content, err := GenerateConfig(cfg)
	if err != nil {
		t.Fatalf("GenerateConfig() error: %v", err)
	}

	ifaceIdx := strings.Index(content, "[Interface]")
	peerIdx := strings.Index(content, "[Peer]")

	if ifaceIdx == -1 || peerIdx == -1 {
		t.Fatal("missing [Interface] or [Peer] section")
	}
	if ifaceIdx >= peerIdx {
		t.Error("[Interface] section should appear before [Peer] section")
	}
}

func TestGenerateConfig_AllowedIPsHardcoded(t *testing.T) {
	// AllowedIPs must always be 10.99.0.1/32 regardless of config
	cfg := Config{
		PrivateKey:        "x",
		AssignedIP:        "10.99.0.99",
		DashboardPubKey:   "y",
		DashboardEndpoint: "9.9.9.9:51820",
	}

	content, err := GenerateConfig(cfg)
	if err != nil {
		t.Fatalf("GenerateConfig() error: %v", err)
	}

	if !strings.Contains(content, "AllowedIPs = 10.99.0.1/32") {
		t.Error("AllowedIPs should always be 10.99.0.1/32")
	}
}

func TestGenerateConfig_PersistentKeepalive(t *testing.T) {
	cfg := Config{
		PrivateKey:        "x",
		AssignedIP:        "10.99.0.2",
		DashboardPubKey:   "y",
		DashboardEndpoint: "1.2.3.4:51820",
	}

	content, err := GenerateConfig(cfg)
	if err != nil {
		t.Fatalf("GenerateConfig() error: %v", err)
	}

	if !strings.Contains(content, "PersistentKeepalive = 25") {
		t.Error("PersistentKeepalive should be 25")
	}
}

func TestGenerateKeyPair_NoWgCommand(t *testing.T) {
	// On macOS without wireguard-tools, wg command is unavailable.
	// GenerateKeyPair should return an error.
	_, _, err := GenerateKeyPair()
	if err == nil {
		// If wg is installed (CI or Linux), skip the check
		t.Skip("wg command is available, skipping error path test")
	}
	if !strings.Contains(err.Error(), "generating private key") {
		t.Errorf("expected 'generating private key' in error, got: %v", err)
	}
}

func TestConfigureInterface_NoWgQuick(t *testing.T) {
	// ConfigureInterface calls writeConfig first (which needs /etc/wireguard perms)
	// then wg-quick. On macOS dev machines, it should fail.
	cfg := Config{
		PrivateKey:        "testkey",
		AssignedIP:        "10.99.0.5",
		DashboardPubKey:   "pubkey",
		DashboardEndpoint: "1.2.3.4:51820",
	}

	err := ConfigureInterface(cfg)
	if err == nil {
		t.Skip("ConfigureInterface succeeded (likely running as root with wireguard), skipping error path test")
	}
	// Should fail either on writeConfig (permission denied for /etc/wireguard)
	// or on wg-quick not found.
	if err.Error() == "" {
		t.Error("expected non-empty error message")
	}
}

func TestWriteConfig_PermissionDenied(t *testing.T) {
	cfg := Config{
		PrivateKey:        "testkey",
		AssignedIP:        "10.99.0.5",
		DashboardPubKey:   "pubkey",
		DashboardEndpoint: "1.2.3.4:51820",
	}

	err := writeConfig(cfg)
	if err == nil {
		t.Skip("writeConfig succeeded (likely running as root), skipping permission test")
	}
	// Should fail on creating /etc/wireguard or writing the file
	errStr := err.Error()
	if !strings.Contains(errStr, "creating wireguard config dir") && !strings.Contains(errStr, "writing wg0.conf") {
		t.Errorf("expected permission-related error, got: %v", err)
	}
}
