package platform

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"
)

// AppConfig is everything the process needs that is not the database.
type AppConfig struct {
	Env             string
	HTTPAddr        string
	ShutdownTimeout time.Duration
	LogLevel        slog.Level

	DBMaxConns int32
}

// AppConfigFromEnv reads the process settings, falling back to values that make
// a local run work with nothing set.
func AppConfigFromEnv() (AppConfig, error) {
	cfg := AppConfig{
		Env:      envOr("APP_ENV", "local"),
		HTTPAddr: envOr("APP_HTTP_ADDR", ":8080"),
	}

	var err error
	if cfg.ShutdownTimeout, err = durationEnv("APP_SHUTDOWN_TIMEOUT", 15*time.Second); err != nil {
		return AppConfig{}, err
	}
	if cfg.LogLevel, err = levelEnv("APP_LOG_LEVEL", slog.LevelInfo); err != nil {
		return AppConfig{}, err
	}
	if cfg.DBMaxConns, err = int32Env("DB_MAX_CONNS", 10); err != nil {
		return AppConfig{}, err
	}
	return cfg, nil
}

// A malformed setting fails the start rather than falling back to a default.
// Silently ignoring APP_SHUTDOWN_TIMEOUT=30 (no unit) would mean a deploy that
// looks configured and drains for fifteen seconds anyway.
func durationEnv(key string, fallback time.Duration) (time.Duration, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s=%q is not a duration (try 15s)", key, raw)
	}
	if value <= 0 {
		return 0, fmt.Errorf("%s=%q must be positive", key, raw)
	}
	return value, nil
}

func int32Env(key string, fallback int32) (int32, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseInt(raw, 10, 32)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s=%q must be a positive integer", key, raw)
	}
	return int32(value), nil
}

func levelEnv(key string, fallback slog.Level) (slog.Level, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(raw)); err != nil {
		return 0, fmt.Errorf("%s=%q is not a log level (debug, info, warn, error)", key, raw)
	}
	return level, nil
}

// NewLogger builds the structured logger and makes it the default, so packages
// that call slog directly -- the HTTP middleware, for one -- write in the same
// format instead of to a plain text logger nobody configured.
//
// JSON always, including locally. A log format that differs between
// environments is a log format that is only debugged in one of them.
func NewLogger(cfg AppConfig) *slog.Logger {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: cfg.LogLevel,
	})).With(slog.String("env", cfg.Env))

	slog.SetDefault(logger)
	return logger
}
