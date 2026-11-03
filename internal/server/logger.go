package server

import (
	"io"
	"log/slog"
	"strings"
)

// NewLogger builds a slog.Logger from the configured level and format
// (DEV_SPEC §14, §15). format is "json" (default) or "text"; level is one of
// debug|info|warn|error.
func NewLogger(level, format string, w io.Writer) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: lvl}

	var h slog.Handler
	if strings.ToLower(format) == "text" {
		h = slog.NewTextHandler(w, opts)
	} else {
		h = slog.NewJSONHandler(w, opts)
	}
	return slog.New(h)
}
