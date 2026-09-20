package platform

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/yvesas/wagering-core/internal/adapter/oidc"
)

// DatabaseConfig is what it takes to reach PostgreSQL.
type DatabaseConfig struct {
	Host     string
	Port     string
	Name     string
	User     string
	Password string
	SSLMode  string
}

// DatabaseConfigFromEnv reads the connection settings.
//
// DATABASE_URL wins when it is set, because that is what a managed platform
// hands over. Otherwise the parts come from DB_* and are assembled here, which
// is what .env.example documents for local work.
func DatabaseConfigFromEnv() (DatabaseConfig, error) {
	if raw := os.Getenv("DATABASE_URL"); raw != "" {
		return parseDatabaseURL(raw)
	}

	cfg := DatabaseConfig{
		Host:     envOr("DB_HOST", "localhost"),
		Port:     envOr("DB_PORT", "5432"),
		Name:     os.Getenv("DB_NAME"),
		User:     os.Getenv("DB_USER"),
		Password: os.Getenv("DB_PASSWORD"),
		SSLMode:  envOr("DB_SSLMODE", "disable"),
	}

	var missing []string
	if cfg.Name == "" {
		missing = append(missing, "DB_NAME")
	}
	if cfg.User == "" {
		missing = append(missing, "DB_USER")
	}
	if len(missing) > 0 {
		// Naming what is missing, and never what any of them contained: a
		// config error that prints a password is a config error that ends up
		// in a log aggregator.
		return DatabaseConfig{}, fmt.Errorf("missing database settings: %s", strings.Join(missing, ", "))
	}
	return cfg, nil
}

func parseDatabaseURL(raw string) (DatabaseConfig, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return DatabaseConfig{}, fmt.Errorf("DATABASE_URL is not a valid url")
	}
	password, _ := u.User.Password()
	cfg := DatabaseConfig{
		Host:     u.Hostname(),
		Port:     u.Port(),
		Name:     strings.TrimPrefix(u.Path, "/"),
		User:     u.User.Username(),
		Password: password,
		SSLMode:  envOr("DB_SSLMODE", u.Query().Get("sslmode")),
	}
	if cfg.Port == "" {
		cfg.Port = "5432"
	}
	if cfg.SSLMode == "" {
		cfg.SSLMode = "disable"
	}
	return cfg, nil
}

// DSN renders the connection string.
//
// It carries the password, so it is never logged and never put in an error
// message. Use [DatabaseConfig.Redacted] when something has to be printed.
func (c DatabaseConfig) DSN() string {
	return (&url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(c.User, c.Password),
		Host:     c.Host + ":" + c.Port,
		Path:     "/" + c.Name,
		RawQuery: url.Values{"sslmode": {c.SSLMode}}.Encode(),
	}).String()
}

// Redacted is the DSN with the password replaced, safe to log.
func (c DatabaseConfig) Redacted() string {
	return fmt.Sprintf("postgres://%s:xxxxx@%s:%s/%s?sslmode=%s",
		c.User, c.Host, c.Port, c.Name, c.SSLMode)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// OIDCConfig is what it takes to trust the identity provider.
type OIDCConfig struct {
	IssuerURL     string
	Audience      string
	ProviderClaim string
	CacheTTL      time.Duration
	Leeway        time.Duration
}

// OIDCConfigFromEnv reads the identity settings.
//
// The issuer and the audience have no default and no way to be switched off.
// An "auth disabled" flag would be the one setting whose wrong value is
// invisible -- everything works, and nothing is checked -- and it would exist
// only to make local development convenient, which is what the identity
// provider in docker-compose.yml is for.
func OIDCConfigFromEnv() (OIDCConfig, error) {
	cfg := OIDCConfig{
		IssuerURL:     os.Getenv("OIDC_ISSUER_URL"),
		Audience:      os.Getenv("OIDC_AUDIENCE"),
		ProviderClaim: envOr("OIDC_PROVIDER_CLAIM", oidc.DefaultProviderClaim),
	}

	var missing []string
	if cfg.IssuerURL == "" {
		missing = append(missing, "OIDC_ISSUER_URL")
	}
	if cfg.Audience == "" {
		missing = append(missing, "OIDC_AUDIENCE")
	}
	if len(missing) > 0 {
		return OIDCConfig{}, fmt.Errorf("missing identity settings: %s", strings.Join(missing, ", "))
	}

	var err error
	if cfg.CacheTTL, err = durationEnv("OIDC_JWKS_CACHE_TTL", oidc.DefaultCacheTTL); err != nil {
		return OIDCConfig{}, err
	}
	if cfg.Leeway, err = durationEnv("OIDC_CLOCK_SKEW", oidc.DefaultLeeway); err != nil {
		return OIDCConfig{}, err
	}
	return cfg, nil
}
