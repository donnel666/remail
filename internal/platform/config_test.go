package platform

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfigLoadDefaults(t *testing.T) {
	// Set required env vars
	os.Setenv("MYSQL_DSN", "test:test@tcp(127.0.0.1:3306)/test")
	os.Setenv("MINIO_ACCESS_KEY", "testkey")
	os.Setenv("MINIO_SECRET_KEY", "testsecret")
	os.Setenv("SESSION_SECRET", "testsecret")
	defer func() {
		os.Unsetenv("MYSQL_DSN")
		os.Unsetenv("MINIO_ACCESS_KEY")
		os.Unsetenv("MINIO_SECRET_KEY")
		os.Unsetenv("SESSION_SECRET")
	}()

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
	os.Clearenv()

	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "MYSQL_DSN")
}

func TestConfigValidateRelayRequiresAddr(t *testing.T) {
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
