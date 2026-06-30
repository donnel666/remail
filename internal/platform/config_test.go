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
	assert.Equal(t, "", cfg.Diagnostics.PprofAddr)
}

func TestConfigValidateMissingFields(t *testing.T) {
	os.Clearenv()

	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "MYSQL_DSN")
}
