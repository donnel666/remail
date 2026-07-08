package platform

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfigLoadDefaults(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("MYSQL_DSN", "test:test@tcp(127.0.0.1:3306)/test")
	t.Setenv("MINIO_ACCESS_KEY", "testkey")
	t.Setenv("MINIO_SECRET_KEY", "testsecret")
	t.Setenv("SESSION_SECRET", "testsecret")

	cfg, err := Load()
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, ":8080", cfg.Server.Addr)
	assert.Equal(t, "test:test@tcp(127.0.0.1:3306)/test", cfg.MySQL.DSN)
	assert.Equal(t, "127.0.0.1:6379", cfg.Redis.Addr)
	assert.Equal(t, 0, cfg.Redis.DB)
	assert.Equal(t, "testkey", cfg.MinIO.AccessKey)
	assert.Equal(t, "testsecret", cfg.MinIO.SecretKey)
	assert.Equal(t, "remail", cfg.MinIO.Bucket)
	assert.Equal(t, "", cfg.Migrations.Dir)
	assert.Equal(t, "info", cfg.Log.Level)
	assert.Equal(t, "json", cfg.Log.Format)
	assert.Equal(t, "direct", cfg.SMTP.Mode)
	assert.Equal(t, "no-reply@aishop6.com", cfg.SMTP.From)
	assert.Equal(t, "aishop6.com", cfg.SMTP.Domain)
	assert.Equal(t, "mx.aishop6.com", cfg.SMTP.HELODomain)
	assert.False(t, cfg.SMTP.DKIMEnabled)
	assert.Equal(t, "aishop6.com", cfg.SMTP.DKIMDomain)
	assert.Equal(t, "mx", cfg.SMTP.DKIMSelector)
	assert.Equal(t, "", cfg.SMTP.DKIMAlgorithm)
	assert.Equal(t, "", cfg.SMTP.DKIMPrivateKey)
	assert.Equal(t, "", cfg.SMTP.DKIMPrivateKeyFile)
	assert.False(t, cfg.SMTP.InboundEnabled)
	assert.Equal(t, ":2525", cfg.SMTP.InboundAddr)
	assert.Equal(t, "mx.aishop6.com", cfg.SMTP.InboundDomain)
	assert.Equal(t, int64(10<<20), cfg.SMTP.InboundMaxMessageBytes)
	assert.Equal(t, "", cfg.Diagnostics.PprofAddr)
}

func TestConfigValidateMissingFields(t *testing.T) {
	clearConfigEnv(t)

	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "MYSQL_DSN")
}

func TestConfigValidateRelayRequiresAddr(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("MYSQL_DSN", "test:test@tcp(127.0.0.1:3306)/test")
	t.Setenv("MINIO_ACCESS_KEY", "testkey")
	t.Setenv("MINIO_SECRET_KEY", "testsecret")
	t.Setenv("SESSION_SECRET", "testsecret")
	t.Setenv("SMTP_MODE", "relay")

	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SMTP_ADDR")
}

func TestConfigLoadDKIM(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("MYSQL_DSN", "test:test@tcp(127.0.0.1:3306)/test")
	t.Setenv("MINIO_ACCESS_KEY", "testkey")
	t.Setenv("MINIO_SECRET_KEY", "testsecret")
	t.Setenv("SESSION_SECRET", "testsecret")
	t.Setenv("SMTP_DKIM_ENABLED", "true")
	t.Setenv("SMTP_DKIM_DOMAIN", "example.com")
	t.Setenv("SMTP_DKIM_SELECTOR", "mx")
	t.Setenv("SMTP_DKIM_ALGORITHM", "ed25519-sha256")
	t.Setenv("SMTP_DKIM_IDENTITY", "@example.com")
	t.Setenv("SMTP_DKIM_PRIVATE_KEY_FILE", "/run/secrets/smtp-dkim-private-key.pem")

	cfg, err := Load()

	require.NoError(t, err)
	assert.True(t, cfg.SMTP.DKIMEnabled)
	assert.Equal(t, "example.com", cfg.SMTP.DKIMDomain)
	assert.Equal(t, "mx", cfg.SMTP.DKIMSelector)
	assert.Equal(t, "ed25519-sha256", cfg.SMTP.DKIMAlgorithm)
	assert.Equal(t, "@example.com", cfg.SMTP.DKIMIdentity)
	assert.Equal(t, "/run/secrets/smtp-dkim-private-key.pem", cfg.SMTP.DKIMPrivateKeyFile)
}

func TestConfigRejectsAmbiguousDKIMPrivateKeySource(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("MYSQL_DSN", "test:test@tcp(127.0.0.1:3306)/test")
	t.Setenv("MINIO_ACCESS_KEY", "testkey")
	t.Setenv("MINIO_SECRET_KEY", "testsecret")
	t.Setenv("SESSION_SECRET", "testsecret")
	t.Setenv("SMTP_DKIM_ENABLED", "true")
	t.Setenv("SMTP_DKIM_PRIVATE_KEY", "private-key")
	t.Setenv("SMTP_DKIM_PRIVATE_KEY_FILE", "/run/secrets/smtp-dkim-private-key.pem")

	_, err := Load()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot both be set")
}

func clearConfigEnv(t *testing.T) {
	t.Helper()

	keys := []string{
		"SERVER_ADDR",
		"SERVER_TIMEOUT",
		"MYSQL_DSN",
		"REDIS_ADDR",
		"REDIS_PASSWORD",
		"REDIS_DB",
		"MINIO_ENDPOINT",
		"MINIO_ACCESS_KEY",
		"MINIO_SECRET_KEY",
		"MINIO_USE_SSL",
		"MINIO_BUCKET",
		"MIGRATIONS_DIR",
		"SESSION_SECRET",
		"SESSION_MAX_AGE",
		"SESSION_SECURE",
		"LOG_LEVEL",
		"LOG_FORMAT",
		"SMTP_MODE",
		"SMTP_ADDR",
		"SMTP_USERNAME",
		"SMTP_PASSWORD",
		"SMTP_FROM",
		"SMTP_DOMAIN",
		"SMTP_HELO_DOMAIN",
		"SMTP_DKIM_ENABLED",
		"SMTP_DKIM_DOMAIN",
		"SMTP_DKIM_SELECTOR",
		"SMTP_DKIM_ALGORITHM",
		"SMTP_DKIM_IDENTITY",
		"SMTP_DKIM_PRIVATE_KEY",
		"SMTP_DKIM_PRIVATE_KEY_FILE",
		"SMTP_INBOUND_ENABLED",
		"SMTP_INBOUND_ADDR",
		"SMTP_INBOUND_DOMAIN",
		"SMTP_INBOUND_MAX_MESSAGE_BYTES",
		"SMTP_INBOUND_MAX_RECIPIENTS",
		"SMTP_INBOUND_READ_TIMEOUT",
		"SMTP_INBOUND_WRITE_TIMEOUT",
		"PPROF_ADDR",
		"SLOW_REQUEST_THRESHOLD",
		"SLOW_SQL_THRESHOLD",
		"PPROF_CPU_THRESHOLD",
		"PPROF_CPU_PROFILE_DIR",
		"PPROF_CPU_PROFILE_DURATION",
		"PPROF_CPU_CHECK_INTERVAL",
	}
	for _, key := range keys {
		t.Setenv(key, "")
	}
}
