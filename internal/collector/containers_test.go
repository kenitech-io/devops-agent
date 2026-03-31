package collector

import (
	"testing"
)

func TestParseHealth(t *testing.T) {
	tests := []struct {
		status   string
		expected string
	}{
		{"Up 2 hours (healthy)", "healthy"},
		{"Up 5 minutes (unhealthy)", "unhealthy"},
		{"Up 3 days", ""},
		{"Exited (0) 2 hours ago", ""},
	}

	for _, tt := range tests {
		got := parseHealth(tt.status)
		if got != tt.expected {
			t.Errorf("parseHealth(%q) = %q, want %q", tt.status, got, tt.expected)
		}
	}
}

func TestParsePercent(t *testing.T) {
	tests := []struct {
		input    string
		expected float64
	}{
		{"1.23%", 1.23},
		{"0.00%", 0},
		{"99.9%", 99.9},
	}

	for _, tt := range tests {
		got := parsePercent(tt.input)
		if got != tt.expected {
			t.Errorf("parsePercent(%q) = %f, want %f", tt.input, got, tt.expected)
		}
	}
}

func TestParseMemUsage(t *testing.T) {
	tests := []struct {
		input         string
		expectUsage   float64
		expectLimit   float64
	}{
		{"128MiB / 512MiB", 128, 512},
		{"1.5GiB / 4GiB", 1536, 4096},
		{"256KiB / 1MiB", 0.25, 1},
	}

	for _, tt := range tests {
		usage, limit := parseMemUsage(tt.input)
		if diff := usage - tt.expectUsage; diff > 1 || diff < -1 {
			t.Errorf("parseMemUsage(%q) usage = %f, want %f", tt.input, usage, tt.expectUsage)
		}
		if diff := limit - tt.expectLimit; diff > 1 || diff < -1 {
			t.Errorf("parseMemUsage(%q) limit = %f, want %f", tt.input, limit, tt.expectLimit)
		}
	}
}

func TestParseUptime(t *testing.T) {
	tests := []struct {
		input    string
		minSecs  int64
	}{
		{"2 hours ago", 7200},
		{"3 days ago", 259200},
		{"About a minute ago", 60},
		{"5 minutes ago", 300},
	}

	for _, tt := range tests {
		got := parseUptime(tt.input)
		if got < tt.minSecs-1 || got > tt.minSecs+1 {
			t.Errorf("parseUptime(%q) = %d, want ~%d", tt.input, got, tt.minSecs)
		}
	}
}

func TestTruncateID(t *testing.T) {
	if got := truncateID("abcdef123456789"); got != "abcdef123456" {
		t.Errorf("truncateID long = %q, want abcdef123456", got)
	}
	if got := truncateID("short"); got != "short" {
		t.Errorf("truncateID short = %q, want short", got)
	}
}
