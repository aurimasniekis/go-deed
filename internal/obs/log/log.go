// Adapted from Porch internal/obs/log/log.go (Apache-2.0).

// Package log builds Deed's structured logger: JSON (default) or text, with sensitive attributes redacted.
package log

import (
	"io"
	"log/slog"

	"github.com/aurimasniekis/go-deed/internal/obs/redact"
)

// New returns a logger writing to w at level ("debug", "info", "warn", "error") in format ("json" or "text").
func New(w io.Writer, level, format string) *slog.Logger {
	var lv slog.Level
	switch level {
	case "debug":
		lv = slog.LevelDebug
	case "warn":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: lv, ReplaceAttr: redact.ReplaceAttr}
	if format == "text" {
		return slog.New(slog.NewTextHandler(w, opts))
	}
	return slog.New(slog.NewJSONHandler(w, opts))
}
