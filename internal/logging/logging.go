// Package logging provides structured logging for the Keni Agent.
// Uses Go's slog package. Set KENI_LOG_FORMAT=json for JSON output (production),
// or KENI_LOG_FORMAT=text for human-readable output (development, default).
// Set KENI_LOG_LEVEL=debug|info|warn|error to control verbosity (default: info).
package logging

import (
	"log/slog"
	"os"
	"strings"
)

// Init configures the global slog logger based on environment variables.
func Init() {
	format := strings.ToLower(os.Getenv("KENI_LOG_FORMAT"))
	levelStr := strings.ToLower(os.Getenv("KENI_LOG_LEVEL"))

	level := parseLevel(levelStr)
	opts := &slog.HandlerOptions{Level: level}

	var handler slog.Handler
	if format == "json" {
		handler = slog.NewJSONHandler(os.Stderr, opts)
	} else {
		handler = slog.NewTextHandler(os.Stderr, opts)
	}

	logger := slog.New(handler)
	slog.SetDefault(logger)
}

func parseLevel(s string) slog.Level {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
