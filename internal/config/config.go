// Package config reads the service configuration from the environment.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"time"
)

// Config is the service configuration.
type Config struct {
	Addr        string // HTTP_ADDR
	DatabaseURL string // DATABASE_URL
	// RedisAddr is optional. Without it the service runs with no cache, no
	// ingestion lock and no API rate limit.
	RedisAddr string // REDIS_ADDR
	// AdminAPIKey guards POST /api/v1/ingest. Empty disables the endpoint.
	AdminAPIKey string // ADMIN_API_KEY

	// The provider endpoints are required and have no defaults; they come
	// with the brief and are kept out of the repository.
	Provider1URL   string        // PROVIDER1_URL
	Provider2URL   string        // PROVIDER2_URL
	IngestInterval time.Duration // INGEST_INTERVAL

	CacheTTL time.Duration // CACHE_TTL
	// RateLimitPerMinute is the request budget per client IP. Zero disables it.
	RateLimitPerMinute int // RATE_LIMIT_PER_MINUTE

	LogLevel slog.Level // LOG_LEVEL
}

// Load reads the configuration. A value that is set but malformed is an
// error, not a silent fallback to the default.
func Load() (Config, error) {
	r := reader{}
	cfg := Config{
		Addr:               r.str("HTTP_ADDR", ":8080"),
		DatabaseURL:        r.str("DATABASE_URL", "postgres://search:search@localhost:5432/search?sslmode=disable"),
		RedisAddr:          r.str("REDIS_ADDR", ""),
		AdminAPIKey:        r.str("ADMIN_API_KEY", ""),
		Provider1URL:       r.url("PROVIDER1_URL"),
		Provider2URL:       r.url("PROVIDER2_URL"),
		IngestInterval:     r.duration("INGEST_INTERVAL", 5*time.Minute),
		CacheTTL:           r.duration("CACHE_TTL", time.Minute),
		RateLimitPerMinute: r.integer("RATE_LIMIT_PER_MINUTE", 120),
		LogLevel:           r.level("LOG_LEVEL", slog.LevelInfo),
	}
	return cfg, errors.Join(r.errs...)
}

// reader collects every malformed variable, so one start-up reports them all.
type reader struct{ errs []error }

func (r *reader) str(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func (r *reader) url(key string) string {
	v := r.str(key, "")
	if v == "" {
		r.errs = append(r.errs, fmt.Errorf("%s is required: set it to the provider's endpoint (see .env.example)", key))
		return ""
	}
	if u, err := url.Parse(v); err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		r.errs = append(r.errs, fmt.Errorf("%s: %q is not an http(s) URL", key, v))
	}
	return v
}

func (r *reader) duration(key string, def time.Duration) time.Duration {
	v := r.str(key, "")
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		r.errs = append(r.errs, fmt.Errorf("%s: %q is not a positive duration such as 30s or 5m", key, v))
		return def
	}
	return d
}

func (r *reader) integer(key string, def int) int {
	v := r.str(key, "")
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		r.errs = append(r.errs, fmt.Errorf("%s: %q is not a non-negative integer", key, v))
		return def
	}
	return n
}

func (r *reader) level(key string, def slog.Level) slog.Level {
	v := r.str(key, "")
	if v == "" {
		return def
	}
	var l slog.Level
	if err := l.UnmarshalText([]byte(v)); err != nil {
		r.errs = append(r.errs, fmt.Errorf("%s: %q is not one of debug, info, warn, error", key, v))
		return def
	}
	return l
}
