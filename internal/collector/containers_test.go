package collector

import (
	"testing"
)

func TestParseHealth(t *testing.T) {
	tests := []struct {
		name     string
		status   string
		expected string
	}{
		{"healthy", "Up 2 hours (healthy)", "healthy"},
		{"unhealthy", "Up 5 minutes (unhealthy)", "unhealthy"},
		{"no health info", "Up 3 days", ""},
		{"exited", "Exited (0) 2 hours ago", ""},
		{"empty string", "", ""},
		{"healthy uppercase", "Up 2 hours (HEALTHY)", "healthy"},
		{"unhealthy mixed case", "Up 2 hours (Unhealthy)", "unhealthy"},
		{"created state", "Created", ""},
		{"paused state", "Up 5 hours (Paused)", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseHealth(tt.status)
			if got != tt.expected {
				t.Errorf("parseHealth(%q) = %q, want %q", tt.status, got, tt.expected)
			}
		})
	}
}

func TestParsePercent(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected float64
	}{
		{"normal", "1.23%", 1.23},
		{"zero", "0.00%", 0},
		{"high", "99.9%", 99.9},
		{"no percent sign", "50.5", 50.5},
		{"with spaces", " 12.5% ", 12.5},
		{"hundred", "100.00%", 100},
		{"invalid", "abc%", 0},
		{"empty", "", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parsePercent(tt.input)
			if got != tt.expected {
				t.Errorf("parsePercent(%q) = %f, want %f", tt.input, got, tt.expected)
			}
		})
	}
}

func TestParseMemUsage(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		expectUsage float64
		expectLimit float64
	}{
		{"MiB values", "128MiB / 512MiB", 128, 512},
		{"GiB values", "1.5GiB / 4GiB", 1536, 4096},
		{"KiB values", "256KiB / 1MiB", 0.25, 1},
		{"no slash", "128MiB", 0, 0},
		{"empty", "", 0, 0},
		{"GB values", "1GB / 2GB", 1000, 2000},
		{"MB values", "256MB / 512MB", 256, 512},
		{"mixed units", "512MiB / 2GiB", 512, 2048},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			usage, limit := parseMemUsage(tt.input)
			if diff := usage - tt.expectUsage; diff > 1 || diff < -1 {
				t.Errorf("parseMemUsage(%q) usage = %f, want %f", tt.input, usage, tt.expectUsage)
			}
			if diff := limit - tt.expectLimit; diff > 1 || diff < -1 {
				t.Errorf("parseMemUsage(%q) limit = %f, want %f", tt.input, limit, tt.expectLimit)
			}
		})
	}
}

func TestParseMemValue(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected float64
	}{
		{"MiB", "128MiB", 128},
		{"GiB", "2GiB", 2048},
		{"KiB", "1024KiB", 1},
		{"GB", "1GB", 1000},
		{"MB", "512MB", 512},
		{"no unit", "100", 100},
		{"with spaces", " 256MiB ", 256},
		{"zero", "0MiB", 0},
		{"fractional GiB", "1.5GiB", 1536},
		{"invalid number", "abcMiB", 0},
		{"empty", "", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseMemValue(tt.input)
			if diff := got - tt.expected; diff > 1 || diff < -1 {
				t.Errorf("parseMemValue(%q) = %f, want %f", tt.input, got, tt.expected)
			}
		})
	}
}

func TestParseUptime(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		minSecs int64
	}{
		{"hours", "2 hours ago", 7200},
		{"days", "3 days ago", 259200},
		{"about a minute", "About a minute ago", 60},
		{"minutes", "5 minutes ago", 300},
		{"seconds", "30 seconds ago", 30},
		{"one week", "1 week ago", 604800},
		{"one month", "1 month ago", 2592000},
		{"empty", "", 0},
		{"about an hour", "About an hour ago", 3600},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseUptime(tt.input)
			if got < tt.minSecs-1 || got > tt.minSecs+1 {
				t.Errorf("parseUptime(%q) = %d, want ~%d", tt.input, got, tt.minSecs)
			}
		})
	}
}

func TestParseDurationApprox(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected int64 // seconds
	}{
		{"seconds", "30 seconds", 30},
		{"minutes", "5 minutes", 300},
		{"hours", "2 hours", 7200},
		{"days", "3 days", 259200},
		{"weeks", "2 weeks", 1209600},
		{"months", "1 month", 2592000},
		{"a minute", "a minute", 60},
		{"an hour", "an hour", 3600},
		{"singular second", "1 second", 1},
		{"singular minute", "1 minute", 60},
		{"singular hour", "1 hour", 3600},
		{"singular day", "1 day", 86400},
		{"a day", "a day", 86400},
		{"empty", "", 0},
		{"unknown unit", "5 fortnights", 0},
		{"no number no unit", "xyz", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := int64(parseDurationApprox(tt.input).Seconds())
			if got != tt.expected {
				t.Errorf("parseDurationApprox(%q) = %d seconds, want %d", tt.input, got, tt.expected)
			}
		})
	}
}

func TestTruncateID(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"long id", "abcdef123456789", "abcdef123456"},
		{"short id", "short", "short"},
		{"exact 12", "abcdef123456", "abcdef123456"},
		{"empty", "", ""},
		{"13 chars", "abcdef1234567", "abcdef123456"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateID(tt.input)
			if got != tt.expected {
				t.Errorf("truncateID(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

// TestCollectContainers verifies the function does not panic when docker is unavailable.
func TestCollectContainers_NoDocker(t *testing.T) {
	containers, err := CollectContainers()
	// On machines without docker, this returns an error, which is fine.
	// The key thing is it does not panic.
	if err != nil {
		// Expected when docker is not available
		if containers != nil {
			t.Error("expected nil containers when error is returned")
		}
		return
	}
	// If docker is available, containers should be a non-nil slice
	if containers == nil {
		t.Error("expected non-nil containers slice when no error")
	}
}
