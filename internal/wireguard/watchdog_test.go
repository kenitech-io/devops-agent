package wireguard

import (
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
