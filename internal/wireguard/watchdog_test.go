package wireguard

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestHandshakeStaleThreshold(t *testing.T) {
	// Verify the constant is 3 minutes as per design
	if handshakeStaleAfter != 3*time.Minute {
		t.Errorf("expected stale threshold of 3 minutes, got %s", handshakeStaleAfter)
	}
}

func TestWatchdogInterval(t *testing.T) {
	if watchdogInterval != 60*time.Second {
		t.Errorf("expected watchdog interval of 60s, got %s", watchdogInterval)
	}
}

func TestStartWatchdog_SkipWireguard(t *testing.T) {
	// When KENI_SKIP_WIREGUARD=true, StartWatchdog should return immediately
	// without launching a goroutine.
	t.Setenv("KENI_SKIP_WIREGUARD", "true")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Should return immediately without blocking
	done := make(chan struct{})
	go func() {
		StartWatchdog(ctx)
		close(done)
	}()

	select {
	case <-done:
		// Good: returned immediately
	case <-time.After(2 * time.Second):
		t.Error("StartWatchdog did not return promptly with KENI_SKIP_WIREGUARD=true")
	}
}

func TestStartWatchdog_ContextCancel(t *testing.T) {
	// When KENI_SKIP_WIREGUARD is not set, watchdog launches a goroutine.
	// Cancelling the context should stop it. We verify it does not panic.
	t.Setenv("KENI_SKIP_WIREGUARD", "")

	ctx, cancel := context.WithCancel(context.Background())
	StartWatchdog(ctx)

	// Give the goroutine a moment to start
	time.Sleep(50 * time.Millisecond)

	// Cancel should stop the goroutine cleanly
	cancel()

	// Brief pause to let the goroutine exit
	time.Sleep(50 * time.Millisecond)
}

func TestInterfaceExists_NoInterface(t *testing.T) {
	// On macOS dev machines (or any system without wg0), this should return false.
	exists := interfaceExists()
	if exists {
		t.Skip("wg0 interface exists on this machine, skipping")
	}
	// Expected: false on macOS without WireGuard
}

func TestGetLastHandshake_NoWgCommand(t *testing.T) {
	// Without wg installed, getLastHandshake should return an error.
	ts, err := getLastHandshake()
	if err == nil {
		// wg command exists, skip
		t.Skipf("wg command available, got timestamp: %v", ts)
	}
	if ts != (time.Time{}) {
		t.Errorf("expected zero time on error, got: %v", ts)
	}
}

func TestCheckAndRecover_NoInterface(t *testing.T) {
	// On macOS, interfaceExists() returns false, so checkAndRecover
	// should attempt recovery (which will fail silently).
	// This test verifies it does not panic.
	if interfaceExists() {
		t.Skip("wg0 exists, skipping no-interface recovery test")
	}
	checkAndRecover()
}

func TestRecoverInterface_NoWgQuick(t *testing.T) {
	// recoverInterface calls wg-quick down then wg-quick up.
	// On macOS without wireguard-tools, both will fail.
	// Verify it does not panic.
	recoverInterface()
}

func TestStartWatchdog_NotSkippedValues(t *testing.T) {
	// Only "true" should skip. Other values should start the watchdog.
	tests := []struct {
		name  string
		value string
	}{
		{"empty string", ""},
		{"false", "false"},
		{"yes", "yes"},
		{"1", "1"},
		{"TRUE uppercase", "TRUE"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("KENI_SKIP_WIREGUARD", tt.value)

			ctx, cancel := context.WithCancel(context.Background())

			// StartWatchdog should NOT return immediately (it starts a goroutine).
			// We cancel right away to clean up.
			StartWatchdog(ctx)
			cancel()

			// Just verify no panic
		})
	}
}

func TestGetLastHandshake_ErrorMessage(t *testing.T) {
	_, err := getLastHandshake()
	if err == nil {
		t.Skip("wg command available, skipping error message test")
	}
	// The error should come from exec, not be empty
	if strings.TrimSpace(err.Error()) == "" {
		t.Error("expected non-empty error from getLastHandshake")
	}
}
