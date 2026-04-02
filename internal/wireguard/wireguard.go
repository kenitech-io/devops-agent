package wireguard

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"text/template"
)

const wgConfPath = "/etc/wireguard/wg0.conf"

const wgConfTemplate = `[Interface]
Address = {{.AssignedIP}}/32
PrivateKey = {{.PrivateKey}}

[Peer]
PublicKey = {{.DashboardPubKey}}
Endpoint = {{.DashboardEndpoint}}
AllowedIPs = 10.99.0.1/32
PersistentKeepalive = 25
`

// Config holds the WireGuard configuration parameters.
type Config struct {
	PrivateKey        string
	AssignedIP        string
	DashboardPubKey   string
	DashboardEndpoint string
}

// GenerateKeyPair generates a WireGuard private/public key pair using the wg command.
func GenerateKeyPair() (privateKey, publicKey string, err error) {
	privOut, err := exec.Command("wg", "genkey").Output()
	if err != nil {
		return "", "", fmt.Errorf("generating private key: %w", err)
	}
	privKey := strings.TrimSpace(string(privOut))

	pubCmd := exec.Command("wg", "pubkey")
	pubCmd.Stdin = strings.NewReader(privKey)
	pubOut, err := pubCmd.Output()
	if err != nil {
		return "", "", fmt.Errorf("generating public key: %w", err)
	}
	pubKey := strings.TrimSpace(string(pubOut))

	return privKey, pubKey, nil
}

// validateEndpoint checks that the endpoint is a valid host:port format.
func validateEndpoint(endpoint string) error {
	host, port, err := net.SplitHostPort(endpoint)
	if err != nil {
		return fmt.Errorf("invalid endpoint format: %w", err)
	}
	if host == "" || port == "" {
		return fmt.Errorf("endpoint must have both host and port")
	}
	if _, err := strconv.Atoi(port); err != nil {
		return fmt.Errorf("invalid port: %w", err)
	}
	return nil
}

// ConfigureInterface writes the wg0 configuration and brings up the interface.
func ConfigureInterface(cfg Config) error {
	if err := validateEndpoint(cfg.DashboardEndpoint); err != nil {
		return fmt.Errorf("invalid dashboard endpoint: %w", err)
	}

	if err := writeConfig(cfg); err != nil {
		return err
	}

	// Bring down existing interface if it exists (ignore errors)
	exec.Command("wg-quick", "down", "wg0").Run()

	// Bring up the interface
	out, err := exec.Command("wg-quick", "up", "wg0").CombinedOutput()
	if err != nil {
		return fmt.Errorf("bringing up wg0: %w, output: %s", err, string(out))
	}

	return nil
}

// GenerateConfig returns the WireGuard config file content as a string.
// Useful for testing without writing to disk.
func GenerateConfig(cfg Config) (string, error) {
	tmpl, err := template.New("wg0").Parse(wgConfTemplate)
	if err != nil {
		return "", fmt.Errorf("parsing template: %w", err)
	}

	var buf strings.Builder
	if err := tmpl.Execute(&buf, cfg); err != nil {
		return "", fmt.Errorf("executing template: %w", err)
	}

	return buf.String(), nil
}

// writeConfig writes the WireGuard config to /etc/wireguard/wg0.conf.
func writeConfig(cfg Config) error {
	content, err := GenerateConfig(cfg)
	if err != nil {
		return err
	}

	if err := os.MkdirAll("/etc/wireguard", 0700); err != nil {
		return fmt.Errorf("creating wireguard config dir: %w", err)
	}

	if err := os.WriteFile(wgConfPath, []byte(content), 0600); err != nil {
		return fmt.Errorf("writing wg0.conf: %w", err)
	}

	return nil
}
