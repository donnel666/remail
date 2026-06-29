package platform

import (
	"context"
	"log/slog"
	"os"
)

// contextKey is a private type used for context keys to avoid collisions.
type contextKey string

const (
	// RequestIDKey is the context key for storing the request ID.
	RequestIDKey contextKey = "request_id"
)

// SetupLogger configures the global slog logger based on config.
func SetupLogger(cfg LogConfig) {
	var handler slog.Handler

	level := parseLevel(cfg.Level)
	opts := &slog.HandlerOptions{Level: level}

	switch cfg.Format {
	case "text":
		handler = slog.NewTextHandler(os.Stdout, opts)
	default:
		handler = slog.NewJSONHandler(os.Stdout, opts)
	}

	slog.SetDefault(slog.New(handler))
}

// Logger returns a slog.Logger with request ID from context if present.
func Logger(ctx context.Context) *slog.Logger {
	l := slog.Default()
	if ctx == nil {
		return l
	}
	if rid, ok := ctx.Value(RequestIDKey).(string); ok && rid != "" {
		return l.With("request_id", rid)
	}
	return l
}

// WithRequestID adds a request ID to the context.
func WithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, RequestIDKey, requestID)
}

func parseLevel(level string) slog.Level {
	switch level {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
