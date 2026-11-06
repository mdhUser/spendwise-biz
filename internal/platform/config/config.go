package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	HTTPAddr string

	MySQLDSN     string
	MigrationDSN string

	Timezone string

	AuthBaseURL string
	AuthTimeout time.Duration

	// JWT local verification. JWKS is preferred; the opaque /v1/auth/me call is
	// only a compatibility fallback when local verification fails.
	AuthJWKSURL          string
	AuthIssuer           string
	AuthAudience         string
	AuthJWKSCacheTTL     time.Duration
	AuthStaticPublicKey  string
	AuthStaticKeyID      string
	AuthAccessCookieName string
	AuthPlainCookieName  string

	IdempotencyTTL    time.Duration
	RequestTimeout    time.Duration
	DBTimeout         time.Duration
	DBConnMaxLifetime time.Duration
	DBMaxOpen         int
	DBMaxIdle         int

	LogLevel slog.Level

	// Worker / external I/O budgets (target architecture §2.7 and §4.6).
	RunTimeout        time.Duration
	ProviderTimeout   time.Duration
	LLMTotalTimeout   time.Duration
	LLMAttemptTimeout time.Duration
	LLMDailyBudgetFen int64
	WorkerID          string
	WorkerPoll        time.Duration
	WorkerHeartbeat   time.Duration

	TrustProxy     bool
	AllowedOrigins []string
}

func Load() (*Config, error) {
	_ = godotenv.Load()
	cfg := defaults()
	cfg.MySQLDSN = strings.TrimSpace(os.Getenv("BIZ_MYSQL_DSN"))
	cfg.MigrationDSN = strings.TrimSpace(os.Getenv("BIZ_MIGRATION_DSN"))
	if cfg.MigrationDSN == "" {
		cfg.MigrationDSN = cfg.MySQLDSN
	}
	cfg.HTTPAddr = env("BIZ_HTTP_ADDR", cfg.HTTPAddr)
	cfg.Timezone = env("BIZ_TIMEZONE", cfg.Timezone)
	cfg.AuthBaseURL = env("BIZ_AUTH_BASE_URL", cfg.AuthBaseURL)
	cfg.AuthJWKSURL = env("BIZ_AUTH_JWKS_URL", strings.TrimRight(cfg.AuthBaseURL, "/")+"/v1/auth/jwks")
	cfg.AuthIssuer = env("BIZ_AUTH_ISSUER", cfg.AuthIssuer)
	cfg.AuthAudience = env("BIZ_AUTH_AUDIENCE", cfg.AuthAudience)
	cfg.AuthStaticPublicKey = strings.TrimSpace(os.Getenv("BIZ_AUTH_JWKS_STATIC_PUBLIC_KEY"))
	cfg.AuthStaticKeyID = strings.TrimSpace(os.Getenv("BIZ_AUTH_JWKS_STATIC_KEY_ID"))
	cfg.AuthAccessCookieName = env("BIZ_AUTH_COOKIE_NAME", cfg.AuthAccessCookieName)
	cfg.AuthPlainCookieName = env("BIZ_AUTH_PLAIN_COOKIE_NAME", cfg.AuthPlainCookieName)

	if cfg.MySQLDSN == "" {
		return nil, errors.New("BIZ_MYSQL_DSN is required")
	}
	if err := parseDurations(cfg); err != nil {
		return nil, err
	}
	cfg.LogLevel = parseLevel(env("BIZ_LOG_LEVEL", "info"))
	cfg.TrustProxy = env("BIZ_TRUST_PROXY", "false") == "true"
	cfg.AllowedOrigins = splitCSV(os.Getenv("BIZ_ALLOWED_ORIGINS"))
	cfg.WorkerID = env("BIZ_WORKER_ID", cfg.WorkerID)
	return cfg, cfg.Validate()
}

func LoadMigration() (*Config, error) {
	_ = godotenv.Load()
	cfg := defaults()
	cfg.MySQLDSN = strings.TrimSpace(os.Getenv("BIZ_MYSQL_DSN"))
	cfg.MigrationDSN = strings.TrimSpace(os.Getenv("BIZ_MIGRATION_DSN"))
	if cfg.MigrationDSN == "" {
		cfg.MigrationDSN = cfg.MySQLDSN
	}
	if cfg.MigrationDSN == "" {
		return nil, errors.New("BIZ_MIGRATION_DSN or BIZ_MYSQL_DSN is required")
	}
	cfg.Timezone = env("BIZ_TIMEZONE", cfg.Timezone)
	cfg.DBTimeout = durationEnv("BIZ_DB_TIMEOUT", cfg.DBTimeout)
	cfg.LogLevel = parseLevel(env("BIZ_LOG_LEVEL", "info"))
	return cfg, cfg.Validate()
}

func (c *Config) Validate() error {
	if c.HTTPAddr == "" {
		return errors.New("BIZ_HTTP_ADDR must not be empty")
	}
	if c.Timezone != "Asia/Shanghai" {
		return errors.New("BIZ_TIMEZONE must be Asia/Shanghai; ledger SQL uses a fixed +08:00 month boundary")
	}
	if _, err := time.LoadLocation(c.Timezone); err != nil {
		return fmt.Errorf("invalid timezone %q: %w", c.Timezone, err)
	}
	if c.AuthBaseURL == "" {
		return errors.New("BIZ_AUTH_BASE_URL must not be empty")
	}
	parsed, err := url.Parse(c.AuthBaseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return errors.New("BIZ_AUTH_BASE_URL must be an absolute http(s) URL")
	}
	if c.AuthTimeout <= 0 {
		return errors.New("BIZ_AUTH_TIMEOUT must be positive")
	}
	if c.AuthJWKSURL == "" {
		return errors.New("BIZ_AUTH_JWKS_URL must not be empty")
	}
	if parsed, err := url.Parse(c.AuthJWKSURL); err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return errors.New("BIZ_AUTH_JWKS_URL must be an absolute http(s) URL")
	}
	if c.AuthIssuer == "" || c.AuthAudience == "" {
		return errors.New("BIZ_AUTH_ISSUER and BIZ_AUTH_AUDIENCE must not be empty")
	}
	if c.AuthJWKSCacheTTL <= 0 {
		return errors.New("BIZ_AUTH_JWKS_CACHE_TTL must be positive")
	}
	if c.DBMaxOpen < 1 || c.DBMaxIdle < 0 || c.DBMaxIdle > c.DBMaxOpen {
		return errors.New("database pool sizes are invalid")
	}
	return nil
}

func (c *Config) Location() *time.Location {
	loc, err := time.LoadLocation(c.Timezone)
	if err != nil {
		return time.UTC
	}
	return loc
}

func defaults() *Config {
	return &Config{
		HTTPAddr:             "127.0.0.1:8080",
		Timezone:             "Asia/Shanghai",
		AuthBaseURL:          "http://127.0.0.1:8081",
		AuthJWKSURL:          "http://127.0.0.1:8081/v1/auth/jwks",
		AuthTimeout:          5 * time.Second,
		AuthIssuer:           "spendwise-auth",
		AuthAudience:         "spendwise-api",
		AuthJWKSCacheTTL:     5 * time.Minute,
		AuthAccessCookieName: "__Host-sw_at",
		AuthPlainCookieName:  "sw_at",
		IdempotencyTTL:       2160 * time.Hour,
		RequestTimeout:       15 * time.Second,
		DBTimeout:            5 * time.Second,
		DBConnMaxLifetime:    30 * time.Minute,
		DBMaxOpen:            10,
		DBMaxIdle:            5,
		LogLevel:             slog.LevelInfo,
		RunTimeout:           60 * time.Second,
		ProviderTimeout:      8 * time.Second,
		LLMTotalTimeout:      12 * time.Second,
		LLMAttemptTimeout:    5 * time.Second,
		LLMDailyBudgetFen:    0,
		WorkerPoll:           500 * time.Millisecond,
		WorkerHeartbeat:      30 * time.Second,
	}
}

func parseDurations(c *Config) error {
	c.AuthTimeout = durationEnv("BIZ_AUTH_TIMEOUT", c.AuthTimeout)
	c.AuthJWKSCacheTTL = durationEnv("BIZ_AUTH_JWKS_CACHE_TTL", c.AuthJWKSCacheTTL)
	c.RunTimeout = durationEnv("BIZ_RUN_TIMEOUT", c.RunTimeout)
	c.ProviderTimeout = durationEnv("BIZ_PROVIDER_TIMEOUT", c.ProviderTimeout)
	c.LLMTotalTimeout = durationEnv("BIZ_LLM_TOTAL_TIMEOUT", c.LLMTotalTimeout)
	c.LLMAttemptTimeout = durationEnv("BIZ_LLM_ATTEMPT_TIMEOUT", c.LLMAttemptTimeout)
	c.WorkerPoll = durationEnv("BIZ_WORKER_POLL", c.WorkerPoll)
	c.WorkerHeartbeat = durationEnv("BIZ_WORKER_HEARTBEAT", c.WorkerHeartbeat)
	c.LLMDailyBudgetFen = int64Env("BIZ_LLM_DAILY_BUDGET_FEN", c.LLMDailyBudgetFen)
	c.IdempotencyTTL = durationEnv("BIZ_IDEMPOTENCY_TTL", c.IdempotencyTTL)
	c.RequestTimeout = durationEnv("BIZ_REQUEST_TIMEOUT", c.RequestTimeout)
	c.DBTimeout = durationEnv("BIZ_DB_TIMEOUT", c.DBTimeout)
	c.DBConnMaxLifetime = durationEnv("BIZ_DB_CONN_MAX_LIFETIME", c.DBConnMaxLifetime)
	c.DBMaxOpen = intEnv("BIZ_DB_MAX_OPEN_CONNS", c.DBMaxOpen)
	c.DBMaxIdle = intEnv("BIZ_DB_MAX_IDLE_CONNS", c.DBMaxIdle)
	return nil
}

func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func durationEnv(key string, fallback time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return fallback
	}
	return d
}

func int64Env(key string, fallback int64) int64 {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return fallback
	}
	return n
}

func intEnv(key string, fallback int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return fallback
	}
	return n
}

func splitCSV(value string) []string {
	var out []string
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func parseLevel(value string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(value)) {
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
