package platform

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
)

// Config holds all application configuration loaded from environment variables.
type Config struct {
	Server      ServerConfig
	MySQL       MySQLConfig
	Redis       RedisConfig
	MinIO       MinIOConfig
	SMTP        SMTPConfig
	Migrations  MigrationsConfig
	Session     SessionConfig
	Log         LogConfig
	Diagnostics DiagnosticsConfig
}

// ServerConfig holds HTTP server settings.
type ServerConfig struct {
	Addr    string
	Timeout time.Duration
}

// MySQLConfig holds database connection settings.
type MySQLConfig struct {
	DSN string
}

// RedisConfig holds Redis connection settings.
type RedisConfig struct {
	Addr     string
	Password string
	DB       int
}

// MinIOConfig holds MinIO object storage settings.
type MinIOConfig struct {
	Endpoint  string
	AccessKey string
	SecretKey string
	UseSSL    bool
	Bucket    string
}

// SMTPConfig holds outbound mail settings.
type SMTPConfig struct {
	Addr     string
	Username string
	Password string
	From     string
}

// MigrationsConfig holds database migration settings.
type MigrationsConfig struct {
	Dir string
}

// SessionConfig holds session settings.
type SessionConfig struct {
	Secret string
	MaxAge int
	Secure bool
}

// LogConfig holds logging settings.
type LogConfig struct {
	Level  string
	Format string
}

// DiagnosticsConfig holds opt-in runtime diagnostics settings.
type DiagnosticsConfig struct {
	PprofAddr               string
	SlowRequestThreshold    time.Duration
	SlowSQLThreshold        time.Duration
	PprofCPUThreshold       float64
	PprofCPUProfileDir      string
	PprofCPUProfileDuration time.Duration
	PprofCPUCheckInterval   time.Duration
}

// Load reads configuration from environment variables.
// It attempts to load .env file first (non-fatal if missing).
func Load() (*Config, error) {
	// Load .env file for local development; ignore error if file doesn't exist
	_ = godotenv.Load()

	cfg := &Config{
		Server: ServerConfig{
			Addr:    getEnv("SERVER_ADDR", ":8080"),
			Timeout: getDuration("SERVER_TIMEOUT", 30*time.Second),
		},
		MySQL: MySQLConfig{
			DSN: getEnv("MYSQL_DSN", ""),
		},
		Redis: RedisConfig{
			Addr:     getEnv("REDIS_ADDR", "127.0.0.1:6379"),
			Password: getEnv("REDIS_PASSWORD", ""),
			DB:       getInt("REDIS_DB", 0),
		},
		MinIO: MinIOConfig{
			Endpoint:  getEnv("MINIO_ENDPOINT", "127.0.0.1:9000"),
			AccessKey: getEnv("MINIO_ACCESS_KEY", ""),
			SecretKey: getEnv("MINIO_SECRET_KEY", ""),
			UseSSL:    getBool("MINIO_USE_SSL", false),
			Bucket:    getEnv("MINIO_BUCKET", "remail"),
		},
		SMTP: SMTPConfig{
			Addr:     getEnv("SMTP_ADDR", ""),
			Username: getEnv("SMTP_USERNAME", ""),
			Password: getEnv("SMTP_PASSWORD", ""),
			From:     getEnv("SMTP_FROM", ""),
		},
		Migrations: MigrationsConfig{
			Dir: getEnv("MIGRATIONS_DIR", ""),
		},
		Session: SessionConfig{
			Secret: getEnv("SESSION_SECRET", ""),
			MaxAge: getInt("SESSION_MAX_AGE", 86400),
			Secure: getBool("SESSION_SECURE", false),
		},
		Log: LogConfig{
			Level:  getEnv("LOG_LEVEL", "info"),
			Format: getEnv("LOG_FORMAT", "json"),
		},
		Diagnostics: DiagnosticsConfig{
			PprofAddr:               getEnv("PPROF_ADDR", ""),
			SlowRequestThreshold:    getDuration("SLOW_REQUEST_THRESHOLD", time.Second),
			SlowSQLThreshold:        getDuration("SLOW_SQL_THRESHOLD", 200*time.Millisecond),
			PprofCPUThreshold:       getFloat("PPROF_CPU_THRESHOLD", 80),
			PprofCPUProfileDir:      getEnv("PPROF_CPU_PROFILE_DIR", "pprof"),
			PprofCPUProfileDuration: getDuration("PPROF_CPU_PROFILE_DURATION", 10*time.Second),
			PprofCPUCheckInterval:   getDuration("PPROF_CPU_CHECK_INTERVAL", 30*time.Second),
		},
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

func (c *Config) validate() error {
	if c.MySQL.DSN == "" {
		return fmt.Errorf("MYSQL_DSN is required")
	}
	if c.MinIO.AccessKey == "" {
		return fmt.Errorf("MINIO_ACCESS_KEY is required")
	}
	if c.MinIO.SecretKey == "" {
		return fmt.Errorf("MINIO_SECRET_KEY is required")
	}
	if c.Session.Secret == "" {
		return fmt.Errorf("SESSION_SECRET is required")
	}
	return nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return fallback
}

func getBool(key string, fallback bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return fallback
}

func getFloat(key string, fallback float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return fallback
}

func getDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}
