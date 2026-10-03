// Package logx provides a small structured JSON logger built on log/slog.
// It writes all log lines as JSON with a UTC RFC3339 timestamp field and a
// configurable level. There is no external dependency.
package logx

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"time"
)

// Level values matching slog levels.
const (
	LevelDebug = slog.LevelDebug
	LevelInfo  = slog.LevelInfo
	LevelWarn  = slog.LevelWarn
	LevelError = slog.LevelError
)

// Logger is an alias for *slog.Logger. It is aliased so the rest of the
// codebase can depend on one consistent abstraction.
type Logger = *slog.Logger

// ParseLevel converts a string level name ("debug", "info", "warn", "error")
// to a slog.Level. Unknown values default to Info.
func ParseLevel(name string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(name)) {
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

// jsonHandler returns an slog JSON handler writing to stderr with a UTC
// RFC3339 (millisecond precision) timestamp and a log level attribute.
func jsonHandler(opts *slog.HandlerOptions, w *os.File) slog.Handler {
	return slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level: opts.Level,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				a.Value = slog.StringValue(a.Value.Time().UTC().Format(time.RFC3339Nano))
			}
			if a.Key == slog.LevelKey {
				a.Value = slog.StringValue(a.Value.Any().(slog.Level).String())
			}
			return a
		},
	})
}

// New returns a logger at the given level using the default stderr writer.
func New(level slog.Level) Logger {
	h := jsonHandler(&slog.HandlerOptions{Level: level}, os.Stderr)
	return slog.New(h)
}

// Discard returns a logger that drops all records. Useful in tests.
func Discard() Logger {
	return slog.New(&discardHandler{})
}

type discardHandler struct{}

func (*discardHandler) Enabled(context.Context, slog.Level) bool  { return false }
func (*discardHandler) Handle(context.Context, slog.Record) error { return nil }
func (h *discardHandler) WithAttrs([]slog.Attr) slog.Handler      { return h }
func (h *discardHandler) WithGroup(string) slog.Handler           { return h }

// With returns a child logger with the given key/value attributes appended.
func With(l Logger, args ...any) Logger {
	return l.With(args...)
}

// Ctx returns a child logger carrying ctx for structured context propagation.
func Ctx(ctx context.Context, l Logger) Logger {
	return l.With(slog.String("trace", ctxTrace(ctx)))
}

func ctxTrace(ctx context.Context) string {
	if v := ctx.Value(traceKey{}); v != nil {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

type traceKey struct{}
