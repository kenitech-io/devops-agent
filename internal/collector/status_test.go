package collector

import (
	"testing"
)

func TestCollectStatusReport(t *testing.T) {
	report := CollectStatusReport()
	if report == nil {
		t.Fatal("CollectStatusReport returned nil")
	}

	// Containers may be nil or empty when docker is not available, but should not cause panic.
	// The function logs the error internally and sets containers to nil.

	// Backups should have a status set
	if report.Backups.LastStatus == "" {
		t.Error("Backups.LastStatus should not be empty")
	}

	// WireGuard should have default interface
	if report.WireGuard.Interface != "wg0" {
		t.Errorf("WireGuard.Interface = %q, want %q", report.WireGuard.Interface, "wg0")
	}
}

func TestCollectStatusReport_NoPanic(t *testing.T) {
	// Call multiple times to ensure stability
	for i := 0; i < 3; i++ {
		report := CollectStatusReport()
		if report == nil {
			t.Fatalf("CollectStatusReport returned nil on call %d", i+1)
		}
	}
}
