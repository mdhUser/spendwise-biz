package config

import (
	"errors"
	"fmt"
	"log/slog"
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

	SessionTTL        time.Duration
	AbsoluteTTL       time.Duration
	IdempotencyTTL    time.Duration
	RequestTimeout    time.Duration
	DBTimeout         time.Duration
	DBConnMaxLifetime time.Duration
	DBMaxOpen         int
	DBMaxIdle         int

	InvitePepper     string
	RegistrationMode string
	LogLevel         slog.Level

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
	cfg.InvitePepper = os.Getenv("BIZ_INVITE_PEPPER")
	cfg.RegistrationMode = env("BIZ_REGISTRATION_MODE", "invite")
	cfg.HTTPAddr = env("BIZ_HTTP_ADDR", cfg.HTTPAddr)
	cfg.Timezone = env("BIZ_TIMEZONE", cfg.Timezone)

	if cfg.MySQLDSN == "" {
		return nil, errors.New("BIZ_MYSQL_DSN is required")
	}
	if len(cfg.InvitePepper) < 32 {
		return nil, errors.New("BIZ_INVITE_PEPPER must contain at least 32 characters")
	}
	if cfg.RegistrationMode != "invite" && cfg.RegistrationMode != "closed" {
		return nil, errors.New("BIZ_REGISTRATION_MODE must be invite or closed")
	}
	if err := parseDurations(cfg); err != nil {
		return nil, err
	}
	cfg.LogLevel = parseLevel(env("BIZ_LOG_LEVEL", "info"))
	cfg.TrustProxy = env("BIZ_TRUST_PROXY", "false") == "true"
	cfg.AllowedOrigins = splitCSV(os.Getenv("BIZ_ALLOWED_ORIGINS"))
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
	if _, err := time.LoadLocation(c.Timezone); err != nil {
		return fmt.Errorf("invalid timezone %q: %w", c.Timezone, err)
	}
	if c.SessionTTL <= 0 || c.AbsoluteTTL < c.SessionTTL {
		return errors.New("session TTLs are invalid")
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
		HTTPAddr:          "127.0.0.1:8080",
		Timezone:          "Asia/Shanghai",
		SessionTTL:        720 * time.Hour,
		AbsoluteTTL:       2160 * time.Hour,
		IdempotencyTTL:    2160 * time.Hour,
		RequestTimeout:    15 * time.Second,
		DBTimeout:         5 * time.Second,
		DBConnMaxLifetime: 30 * time.Minute,
		DBMaxOpen:         10,
		DBMaxIdle:         5,
		LogLevel:          slog.LevelInfo,
	}
}

func parseDurations(c *Config) error {
	c.SessionTTL = durationEnv("BIZ_SESSION_TTL", c.SessionTTL)
	c.AbsoluteTTL = durationEnv("BIZ_SESSION_ABSOLUTE_TTL", c.AbsoluteTTL)
	c.IdempotencyTTL = durationEnv("BIZ_IDEMPOTENCY_TTL", c.IdempotencyTTL)
	c.RequestTimeout = durationEnv("BIZ_REQUEST_TIMEOUT", c.RequestTimeout)
	c.DBTimeout = durationEnv("BIZ_DB_TIMEOUT", c.DBTimeout)
	c.DBConnMaxLifetime = durationEnv("BIZ_DB_CONN_MAX_LIFETIME", c.DBConnMaxLifetime)
	c.DBMaxOpen = intEnv("BIZ_DB_MAX_OPEN_CONNS", c.DBMaxOpen)
	c.DBMaxIdle = intEnv("BIZ_DB_MAX_IDLE_CONNS", c.DBMaxIdle)
	if c.AbsoluteTTL < c.SessionTTL {
		return errors.New("BIZ_SESSION_ABSOLUTE_TTL must be >= BIZ_SESSION_TTL")
	}
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
