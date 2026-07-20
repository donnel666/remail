package platform

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// Config holds all application configuration loaded from environment variables.
type Config struct {
	Environment string
	Server      ServerConfig
	MySQL       MySQLConfig
	Redis       RedisConfig
	MinIO       MinIOConfig
	SMTP        SMTPConfig
	Migrations  MigrationsConfig
	Session     SessionConfig
	Turnstile   TurnstileConfig
	Log         LogConfig
	Diagnostics DiagnosticsConfig
}

// ServerConfig holds HTTP server settings.
type ServerConfig struct {
	Addr           string
	Timeout        time.Duration
	TrustedProxies []string
}

// MySQLConfig holds database connection settings.
type MySQLConfig struct {
	DSN          string
	MaxOpenConns int
	MaxIdleConns int
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
	Mode                   string
	Addr                   string
	Username               string
	Password               string
	From                   string
	Domain                 string
	HELODomain             string
	DKIMEnabled            bool
	DKIMDomain             string
	DKIMSelector           string
	DKIMAlgorithm          string
	DKIMIdentity           string
	DKIMPrivateKey         string
	DKIMPrivateKeyFile     string
	InboundEnabled         bool
	InboundAddr            string
	InboundDomain          string
	InboundMaxMessageBytes int64
	InboundMaxRecipients   int
	InboundReadTimeout     time.Duration
	InboundWriteTimeout    time.Duration
	// After-sales ticket email (opt-in). When enabled, ticket activity is
	// emailed to the customer with a per-ticket Reply-To so email replies feed
	// back into the ticket.
	TicketMailEnabled    bool
	TicketMailFrom       string
	TicketReplyLocalPart string
	TicketReplyDomain    string
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

// TurnstileConfig holds Cloudflare Turnstile credentials. The site key is
// public; the secret key must remain server-side.
type TurnstileConfig struct {
	SiteKey   string
	SecretKey string
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

	environment := strings.ToLower(strings.TrimSpace(getEnv("APP_ENV", "development")))
	trustedProxiesDefault := "127.0.0.1,::1"
	if environment == "production" {
		trustedProxiesDefault = ""
	}
	cfg := &Config{
		Environment: environment,
		Server: ServerConfig{
			Addr:           getEnv("SERVER_ADDR", ":8080"),
			Timeout:        getDuration("SERVER_TIMEOUT", 30*time.Second),
			TrustedProxies: splitCSV(getEnv("TRUSTED_PROXIES", trustedProxiesDefault)),
		},
		MySQL: MySQLConfig{
			DSN:          getEnv("MYSQL_DSN", ""),
			MaxOpenConns: getInt("MYSQL_MAX_OPEN_CONNS", 200),
			MaxIdleConns: getInt("MYSQL_MAX_IDLE_CONNS", 50),
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
		SMTP: loadSMTPConfig(),
		Migrations: MigrationsConfig{
			Dir: getEnv("MIGRATIONS_DIR", ""),
		},
		Session: SessionConfig{
			Secret: getEnv("SESSION_SECRET", ""),
			MaxAge: getInt("SESSION_MAX_AGE", 86400),
			Secure: getBool("SESSION_SECURE", false),
		},
		Turnstile: TurnstileConfig{
			SiteKey:   strings.TrimSpace(getEnv("TURNSTILE_SITE_KEY", "1x00000000000000000000AA")),
			SecretKey: strings.TrimSpace(getEnv("TURNSTILE_SECRET_KEY", "1x0000000000000000000000000000000AA")),
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
	if c.Environment != "development" && c.Environment != "test" && c.Environment != "production" {
		return fmt.Errorf("APP_ENV must be development, test, or production")
	}
	if c.Environment == "production" && !c.Session.Secure {
		return fmt.Errorf("SESSION_SECURE must be true in production")
	}
	if c.Environment == "production" && len(c.Server.TrustedProxies) == 0 {
		return fmt.Errorf("TRUSTED_PROXIES is required in production")
	}
	for _, trustedProxy := range c.Server.TrustedProxies {
		if net.ParseIP(trustedProxy) != nil {
			continue
		}
		if _, _, err := net.ParseCIDR(trustedProxy); err != nil {
			return fmt.Errorf("TRUSTED_PROXIES contains invalid IP or CIDR %q", trustedProxy)
		}
	}
	if c.Diagnostics.PprofAddr != "" {
		host, port, err := net.SplitHostPort(c.Diagnostics.PprofAddr)
		if err != nil || port == "" {
			return fmt.Errorf("PPROF_ADDR must be a loopback host:port")
		}
		if host == "" {
			c.Diagnostics.PprofAddr = net.JoinHostPort("127.0.0.1", port)
		} else if host != "localhost" {
			ip := net.ParseIP(host)
			if ip == nil || !ip.IsLoopback() {
				return fmt.Errorf("PPROF_ADDR must bind to loopback")
			}
		}
	}
	if c.MySQL.DSN == "" {
		return fmt.Errorf("MYSQL_DSN is required")
	}
	if c.MySQL.MaxOpenConns <= 0 {
		return fmt.Errorf("MYSQL_MAX_OPEN_CONNS must be positive")
	}
	if c.MySQL.MaxIdleConns <= 0 {
		return fmt.Errorf("MYSQL_MAX_IDLE_CONNS must be positive")
	}
	if c.MySQL.MaxIdleConns > c.MySQL.MaxOpenConns {
		return fmt.Errorf("MYSQL_MAX_IDLE_CONNS cannot exceed MYSQL_MAX_OPEN_CONNS")
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
	if c.Turnstile.SiteKey == "" {
		return fmt.Errorf("TURNSTILE_SITE_KEY is required")
	}
	if c.Turnstile.SecretKey == "" {
		return fmt.Errorf("TURNSTILE_SECRET_KEY is required")
	}
	if c.Environment == "production" && isTurnstileTestKey(c.Turnstile.SiteKey) {
		return fmt.Errorf("TURNSTILE_SITE_KEY is required in production")
	}
	if c.Environment == "production" && isTurnstileTestKey(c.Turnstile.SecretKey) {
		return fmt.Errorf("TURNSTILE_SECRET_KEY is required in production")
	}
	if c.SMTP.Mode != "direct" && c.SMTP.Mode != "relay" {
		return fmt.Errorf("SMTP_MODE must be direct or relay")
	}
	if c.SMTP.Mode == "relay" && c.SMTP.Addr == "" {
		return fmt.Errorf("SMTP_ADDR is required when SMTP_MODE=relay")
	}
	if c.SMTP.DKIMEnabled {
		if c.SMTP.DKIMDomain == "" {
			return fmt.Errorf("SMTP_DKIM_DOMAIN is required when SMTP_DKIM_ENABLED=true")
		}
		if c.SMTP.DKIMSelector == "" {
			return fmt.Errorf("SMTP_DKIM_SELECTOR is required when SMTP_DKIM_ENABLED=true")
		}
		hasRawDKIMPrivateKey := strings.TrimSpace(c.SMTP.DKIMPrivateKey) != ""
		hasDKIMPrivateKeyFile := strings.TrimSpace(c.SMTP.DKIMPrivateKeyFile) != ""
		if !hasRawDKIMPrivateKey && !hasDKIMPrivateKeyFile {
			return fmt.Errorf("SMTP_DKIM_PRIVATE_KEY or SMTP_DKIM_PRIVATE_KEY_FILE is required when SMTP_DKIM_ENABLED=true")
		}
		if hasRawDKIMPrivateKey && hasDKIMPrivateKeyFile {
			return fmt.Errorf("SMTP_DKIM_PRIVATE_KEY and SMTP_DKIM_PRIVATE_KEY_FILE cannot both be set")
		}
		switch c.SMTP.DKIMAlgorithm {
		case "", "ed25519-sha256", "rsa-sha256":
		default:
			return fmt.Errorf("SMTP_DKIM_ALGORITHM must be ed25519-sha256 or rsa-sha256")
		}
	}
	return nil
}

func isTurnstileTestKey(value string) bool {
	return strings.HasPrefix(value, "1x00000000000000000000") ||
		strings.HasPrefix(value, "2x00000000000000000000") ||
		strings.HasPrefix(value, "3x00000000000000000000")
}

func splitCSV(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			result = append(result, part)
		}
	}
	return result
}

func loadSMTPConfig() SMTPConfig {
	heloDomain := getEnv("SMTP_HELO_DOMAIN", "mx.aishop6.com")
	return SMTPConfig{
		Mode:                   getEnv("SMTP_MODE", "direct"),
		Addr:                   getEnv("SMTP_ADDR", ""),
		Username:               getEnv("SMTP_USERNAME", ""),
		Password:               getEnv("SMTP_PASSWORD", ""),
		From:                   getEnv("SMTP_FROM", "no-reply@aishop6.com"),
		Domain:                 getEnv("SMTP_DOMAIN", "aishop6.com"),
		HELODomain:             heloDomain,
		DKIMEnabled:            getBool("SMTP_DKIM_ENABLED", false),
		DKIMDomain:             getEnv("SMTP_DKIM_DOMAIN", getEnv("SMTP_DOMAIN", "aishop6.com")),
		DKIMSelector:           getEnv("SMTP_DKIM_SELECTOR", "mx"),
		DKIMAlgorithm:          getEnv("SMTP_DKIM_ALGORITHM", ""),
		DKIMIdentity:           getEnv("SMTP_DKIM_IDENTITY", ""),
		DKIMPrivateKey:         getEnv("SMTP_DKIM_PRIVATE_KEY", ""),
		DKIMPrivateKeyFile:     getEnv("SMTP_DKIM_PRIVATE_KEY_FILE", ""),
		InboundEnabled:         getBool("SMTP_INBOUND_ENABLED", false),
		InboundAddr:            getEnv("SMTP_INBOUND_ADDR", ":2525"),
		InboundDomain:          getEnv("SMTP_INBOUND_DOMAIN", heloDomain),
		InboundMaxMessageBytes: getInt64("SMTP_INBOUND_MAX_MESSAGE_BYTES", 10<<20),
		InboundMaxRecipients:   getInt("SMTP_INBOUND_MAX_RECIPIENTS", 20),
		InboundReadTimeout:     getDuration("SMTP_INBOUND_READ_TIMEOUT", 30*time.Second),
		InboundWriteTimeout:    getDuration("SMTP_INBOUND_WRITE_TIMEOUT", 30*time.Second),
		TicketMailEnabled:      getBool("SMTP_TICKET_MAIL_ENABLED", false),
		TicketMailFrom:         getEnv("SMTP_TICKET_MAIL_FROM", getEnv("SMTP_FROM", "no-reply@aishop6.com")),
		TicketReplyLocalPart:   getEnv("SMTP_TICKET_REPLY_LOCAL", "support"),
		TicketReplyDomain:      getEnv("SMTP_TICKET_REPLY_DOMAIN", getEnv("SMTP_INBOUND_DOMAIN", heloDomain)),
	}
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

func getInt64(key string, fallback int64) int64 {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.ParseInt(v, 10, 64); err == nil {
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
