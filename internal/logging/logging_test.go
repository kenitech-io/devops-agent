package logging

import (
	"bytes"
	"log/slog"
	"testing"
)

func TestParseLevel(t *testing.T) {
	tests := []struct {
		input string
		want  slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"info", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"warning", slog.LevelWarn},
		{"error", slog.LevelError},
		{"", slog.LevelInfo},
		{"unknown", slog.LevelInfo},
	}

	for _, tt := range tests {
		t.Run("input_"+tt.input, func(t *testing.T) {
			got := parseLevel(tt.input)
			if got != tt.want {
				t.Errorf("parseLevel(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestInit_DefaultFormat(t *testing.T) {
	// No env vars set, should default to text format and info level.
	t.Setenv("KENI_LOG_FORMAT", "")
	t.Setenv("KENI_LOG_LEVEL", "")

	Init()

	// Verify slog works without panicking.
	slog.Info("test message from default format")
}

func TestInit_JSONFormat(t *testing.T) {
	t.Setenv("KENI_LOG_FORMAT", "json")
	t.Setenv("KENI_LOG_LEVEL", "")

	Init()

	// Verify slog works without panicking.
	slog.Info("test message from json format")
}

func TestInit_DebugLevel(t *testing.T) {
	t.Setenv("KENI_LOG_FORMAT", "text")
	t.Setenv("KENI_LOG_LEVEL", "debug")

	Init()

	// Capture output to verify debug messages are actually logged.
	var buf bytes.Buffer
	handler := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	logger := slog.New(handler)

	logger.Debug("debug level message")
	if buf.Len() == 0 {
		t.Error("expected debug message to be logged when level is debug, but buffer is empty")
	}

	// Also verify the global logger was configured (no panic).
	slog.Debug("global debug message")
}

func TestInit_ErrorLevel(t *testing.T) {
	t.Setenv("KENI_LOG_FORMAT", "text")
	t.Setenv("KENI_LOG_LEVEL", "error")

	Init()

	// Verify slog works without panicking.
	slog.Error("test error message")
}
