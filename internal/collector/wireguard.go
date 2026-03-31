package collector

import (
	"os/exec"
	"strconv"
	"strings"
	"time"

	wsTypes "github.com/kenitech-io/devops-agent/internal/ws"
)

// CollectWireGuard gathers WireGuard interface status.
func CollectWireGuard() wsTypes.WireGuardInfo {
	info := wsTypes.WireGuardInfo{
		Interface: "wg0",
	}

	out, err := exec.Command("wg", "show", "wg0", "dump").Output()
	if err != nil {
		return info
	}

	return parseWgDump(string(out))
}

// parseWgDump parses the output of 'wg show wg0 dump'.
func parseWgDump(output string) wsTypes.WireGuardInfo {
	info := wsTypes.WireGuardInfo{
		Interface: "wg0",
	}

	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) < 1 {
		return info
	}

	// First line: private-key, public-key, listen-port, fwmark
	ifaceFields := strings.Split(lines[0], "\t")
	if len(ifaceFields) >= 2 {
		info.PublicKey = ifaceFields[1]
	}

	// Second line (peer): public-key, preshared-key, endpoint, allowed-ips,
	//   latest-handshake, transfer-rx, transfer-tx, persistent-keepalive
	if len(lines) >= 2 {
		peerFields := strings.Split(lines[1], "\t")
		if len(peerFields) >= 7 {
			// latest-handshake is a unix timestamp
			if ts, err := strconv.ParseInt(peerFields[4], 10, 64); err == nil && ts > 0 {
				info.LatestHandshake = time.Unix(ts, 0).UTC().Format(time.RFC3339)
			}
			if rx, err := strconv.ParseInt(peerFields[5], 10, 64); err == nil {
				info.TransferRx = rx
			}
			if tx, err := strconv.ParseInt(peerFields[6], 10, 64); err == nil {
				info.TransferTx = tx
			}
		}
	}

	return info
}
