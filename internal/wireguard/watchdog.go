package wireguard

import (
	"context"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
	watchdogInterval    = 60 * time.Second
	handshakeStaleAfter = 3 * time.Minute
)

// StartWatchdog periodically checks the wg0 interface health and attempts
// recovery if the interface is down or the last handshake is stale.
func StartWatchdog(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(watchdogInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				checkAndRecover()
			}
		}
	}()
}

func checkAndRecover() {
	// Check if the interface exists
	if !interfaceExists() {
		slog.Warn("wg0 interface not found, attempting recovery")
		recoverInterface()
		return
	}

	// Check last handshake freshness
	lastHandshake, err := getLastHandshake()
	if err != nil {
		slog.Warn("could not read wg0 handshake", "error", err)
		return
	}

	if lastHandshake.IsZero() {
		// No handshake yet (agent just started or peer not reachable)
		slog.Warn("wg0 has no handshake yet")
		return
	}

	staleDuration := time.Since(lastHandshake)
	if staleDuration > handshakeStaleAfter {
		slog.Warn("wg0 handshake stale, attempting recovery",
			"stale_duration", staleDuration.Round(time.Second).String(),
			"threshold", handshakeStaleAfter.String())
		recoverInterface()
	}
}

func interfaceExists() bool {
	err := exec.Command("ip", "link", "show", "wg0").Run()
	return err == nil
}

func getLastHandshake() (time.Time, error) {
	out, err := exec.Command("wg", "show", "wg0", "latest-handshakes").Output()
	if err != nil {
		return time.Time{}, err
	}

	// Output format: "<public_key>\t<unix_timestamp>\n"
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) == 0 {
		return time.Time{}, nil
	}

	fields := strings.Fields(lines[0])
	if len(fields) < 2 {
		return time.Time{}, nil
	}

	ts, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return time.Time{}, err
	}

	if ts == 0 {
		return time.Time{}, nil
	}

	return time.Unix(ts, 0), nil
}

func recoverInterface() {
	// Bring down (ignore errors, may already be down)
	exec.Command("wg-quick", "down", "wg0").Run()

	// Bring back up
	out, err := exec.Command("wg-quick", "up", "wg0").CombinedOutput()
	if err != nil {
		slog.Error("wg0 recovery failed", "error", err, "output", string(out))
		return
	}

	slog.Info("wg0 interface recovered successfully")
}
