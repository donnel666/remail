package platform

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLoggerWithRequestID(t *testing.T) {
	SetupLogger(LogConfig{Level: "debug", Format: "text"})

	ctx := WithRequestID(context.Background(), "req_test_123")
	logger := Logger(ctx)

	assert.NotNil(t, logger)

	// Logger without request ID
	plainLogger := Logger(context.Background())
	assert.NotNil(t, plainLogger)
}

func TestParseLevel(t *testing.T) {
	assert.Equal(t, slog.LevelDebug, parseLevel("debug"))
	assert.Equal(t, slog.LevelInfo, parseLevel("info"))
	assert.Equal(t, slog.LevelWarn, parseLevel("warn"))
	assert.Equal(t, slog.LevelError, parseLevel("error"))
	assert.Equal(t, slog.LevelInfo, parseLevel("unknown"))
}
