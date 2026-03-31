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
