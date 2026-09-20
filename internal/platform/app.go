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

	// MetricsAddr is where the exposition endpoint listens. It is a second
	// listener on purpose -- see docs/adr/0012 -- and the port is not published
	// outside the local network, so the scraper needs no credential.
	MetricsAddr string

	// How long a reversal waits for the operation it undoes.
	ReferenceMaxAttempts int
	ReferenceBaseBackoff time.Duration
	ReferenceTTL         time.Duration

	// The queue. Endpoint is empty in production, where the SDK finds AWS by
	// itself, and points at the emulator locally.
	QueueEndpoint          string
	QueueRegion            string
	QueueName              string
	QueueDLQName           string
	QueueVisibilityTimeout time.Duration
	QueueWaitTime          time.Duration
	QueueMaxReceiveCount   int
	QueueBatchSize         int
	QueueConsumers         int
	AWSAccessKeyID         string
	AWSSecretAccessKey     string

	QueueEventsName      string
	PublisherBatchSize   int
	PublisherInterval    time.Duration
	PublisherBaseBackoff time.Duration
	PublisherTimeout     time.Duration
}

// AppConfigFromEnv reads the process settings, falling back to values that make
// a local run work with nothing set.
func AppConfigFromEnv() (AppConfig, error) {
	cfg := AppConfig{
		Env:         envOr("APP_ENV", "local"),
		HTTPAddr:    envOr("APP_HTTP_ADDR", ":8080"),
		MetricsAddr: envOr("APP_METRICS_ADDR", ":9090"),
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
	if cfg.ReferenceMaxAttempts, err = intEnv("REFERENCE_MAX_ATTEMPTS", 8); err != nil {
		return AppConfig{}, err
	}
	if cfg.ReferenceBaseBackoff, err = durationEnv("REFERENCE_BASE_BACKOFF", time.Second); err != nil {
		return AppConfig{}, err
	}
	if cfg.ReferenceTTL, err = durationEnv("REFERENCE_TTL", 2*time.Minute); err != nil {
		return AppConfig{}, err
	}

	cfg.QueueEndpoint = os.Getenv("QUEUE_ENDPOINT")
	cfg.QueueRegion = envOr("QUEUE_REGION", "us-east-1")
	cfg.QueueName = envOr("QUEUE_NAME", "wager-transactions.fifo")
	cfg.QueueDLQName = envOr("QUEUE_DLQ_NAME", "wager-transactions-dlq.fifo")
	cfg.AWSAccessKeyID = os.Getenv("AWS_ACCESS_KEY_ID")
	cfg.AWSSecretAccessKey = os.Getenv("AWS_SECRET_ACCESS_KEY")

	if cfg.QueueVisibilityTimeout, err = durationEnv("QUEUE_VISIBILITY_TIMEOUT", 30*time.Second); err != nil {
		return AppConfig{}, err
	}
	if cfg.QueueWaitTime, err = durationEnv("QUEUE_WAIT_TIME", 10*time.Second); err != nil {
		return AppConfig{}, err
	}
	if cfg.QueueMaxReceiveCount, err = intEnv("QUEUE_MAX_RECEIVE_COUNT", 5); err != nil {
		return AppConfig{}, err
	}
	if cfg.QueueBatchSize, err = intEnv("QUEUE_BATCH_SIZE", 10); err != nil {
		return AppConfig{}, err
	}
	if cfg.QueueConsumers, err = intEnv("QUEUE_CONSUMERS", 2); err != nil {
		return AppConfig{}, err
	}

	cfg.QueueEventsName = envOr("QUEUE_EVENTS_NAME", "wager-events.fifo")
	if cfg.PublisherBatchSize, err = intEnv("PUBLISHER_BATCH_SIZE", 50); err != nil {
		return AppConfig{}, err
	}
	if cfg.PublisherInterval, err = durationEnv("PUBLISHER_INTERVAL", 500*time.Millisecond); err != nil {
		return AppConfig{}, err
	}
	if cfg.PublisherBaseBackoff, err = durationEnv("PUBLISHER_BASE_BACKOFF", time.Second); err != nil {
		return AppConfig{}, err
	}
	if cfg.PublisherTimeout, err = durationEnv("PUBLISHER_TIMEOUT", 10*time.Second); err != nil {
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

func intEnv(key string, fallback int) (int, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s=%q must be a positive integer", key, raw)
	}
	return value, nil
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
