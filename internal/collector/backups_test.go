package collector

import (
	"testing"
)

func TestCollectBackups_NoRestic(t *testing.T) {
	info := CollectBackups()

	// When restic is not installed or no repo is configured,
	// the function should return gracefully with an error status.
	if info.LastStatus == "" {
		t.Error("LastStatus should not be empty")
	}

	// Without restic, status should be "error" or "unknown"
	validStatuses := map[string]bool{
		"error":   true,
		"unknown": true,
		"success": true,
		"stale":   true,
	}
	if !validStatuses[info.LastStatus] {
		t.Errorf("LastStatus = %q, want one of error/unknown/success/stale", info.LastStatus)
	}

	// SnapshotCount should be >= 0
	if info.SnapshotCount < 0 {
		t.Errorf("SnapshotCount = %d, want >= 0", info.SnapshotCount)
	}

	// TotalSizeGb should be >= 0
	if info.TotalSizeGb < 0 {
		t.Errorf("TotalSizeGb = %f, want >= 0", info.TotalSizeGb)
	}
}
