package collector

import (
	"testing"
)

func TestCollectWireGuard_NoWG(t *testing.T) {
	info := CollectWireGuard()

	if info.Interface != "wg0" {
		t.Errorf("Interface = %q, want %q", info.Interface, "wg0")
	}
	if info.TransferRx < 0 {
		t.Errorf("TransferRx = %d, want >= 0", info.TransferRx)
	}
	if info.TransferTx < 0 {
		t.Errorf("TransferTx = %d, want >= 0", info.TransferTx)
	}
}

func TestParseWgDump(t *testing.T) {
	tests := []struct {
		name              string
		input             string
		expectInterface   string
		expectPublicKey   string
		expectHandshake   string
		expectTransferRx  int64
		expectTransferTx  int64
	}{
		{
			name: "full output with peer",
			input: "PRIVATE_KEY\tPUBLIC_KEY_ABC\t51820\toff\n" +
				"PEER_PUBKEY\t(none)\t192.168.1.1:51820\t10.0.0.0/24\t1711900000\t123456\t789012\t25\n",
			expectInterface:  "wg0",
			expectPublicKey:  "PUBLIC_KEY_ABC",
			expectHandshake:  "2024-03-31T15:46:40Z",
			expectTransferRx: 123456,
			expectTransferTx: 789012,
		},
		{
			name:             "interface only, no peers",
			input:            "PRIVATE_KEY\tPUBLIC_KEY_XYZ\t51820\toff\n",
			expectInterface:  "wg0",
			expectPublicKey:  "PUBLIC_KEY_XYZ",
			expectHandshake:  "",
			expectTransferRx: 0,
			expectTransferTx: 0,
		},
		{
			name:             "empty output",
			input:            "",
			expectInterface:  "wg0",
			expectPublicKey:  "",
			expectHandshake:  "",
			expectTransferRx: 0,
			expectTransferTx: 0,
		},
		{
			name:             "single field line",
			input:            "PRIVATE_KEY\n",
			expectInterface:  "wg0",
			expectPublicKey:  "",
			expectHandshake:  "",
			expectTransferRx: 0,
			expectTransferTx: 0,
		},
		{
			name: "peer with zero handshake",
			input: "PRIVATE_KEY\tPUBLIC_KEY_123\t51820\toff\n" +
				"PEER_PUBKEY\t(none)\t192.168.1.1:51820\t10.0.0.0/24\t0\t5000\t3000\t25\n",
			expectInterface:  "wg0",
			expectPublicKey:  "PUBLIC_KEY_123",
			expectHandshake:  "", // ts=0 should not produce a handshake string
			expectTransferRx: 5000,
			expectTransferTx: 3000,
		},
		{
			name: "peer with too few fields",
			input: "PRIVATE_KEY\tPUBLIC_KEY_456\t51820\toff\n" +
				"PEER_PUBKEY\t(none)\t192.168.1.1:51820\n",
			expectInterface:  "wg0",
			expectPublicKey:  "PUBLIC_KEY_456",
			expectHandshake:  "",
			expectTransferRx: 0,
			expectTransferTx: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := parseWgDump(tt.input)
			if info.Interface != tt.expectInterface {
				t.Errorf("Interface = %q, want %q", info.Interface, tt.expectInterface)
			}
			if info.PublicKey != tt.expectPublicKey {
				t.Errorf("PublicKey = %q, want %q", info.PublicKey, tt.expectPublicKey)
			}
			if info.LatestHandshake != tt.expectHandshake {
				t.Errorf("LatestHandshake = %q, want %q", info.LatestHandshake, tt.expectHandshake)
			}
			if info.TransferRx != tt.expectTransferRx {
				t.Errorf("TransferRx = %d, want %d", info.TransferRx, tt.expectTransferRx)
			}
			if info.TransferTx != tt.expectTransferTx {
				t.Errorf("TransferTx = %d, want %d", info.TransferTx, tt.expectTransferTx)
			}
		})
	}
}
