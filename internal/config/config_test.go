package config_test

import (
	"log/slog"
	"strings"
	"testing"
	"time"

	"feedrank/internal/config"
)

// clearEnv blanks every variable Load reads (an empty value counts as unset)
// and then sets the two that are required.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"HTTP_ADDR", "DATABASE_URL", "REDIS_ADDR", "ADMIN_API_KEY",
		"INGEST_INTERVAL", "CACHE_TTL", "RATE_LIMIT_PER_MINUTE", "LOG_LEVEL",
	} {
		t.Setenv(key, "")
	}
	t.Setenv("PROVIDER1_URL", "https://example.com/provider1")
	t.Setenv("PROVIDER2_URL", "https://example.com/provider2")
}

func TestLoadDefaults(t *testing.T) {
	clearEnv(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != ":8080" || cfg.IngestInterval != 5*time.Minute || cfg.CacheTTL != time.Minute ||
		cfg.RateLimitPerMinute != 120 ||
		cfg.RedisAddr != "" || cfg.AdminAPIKey != "" || cfg.LogLevel != slog.LevelInfo {
		t.Errorf("unexpected defaults: %+v", cfg)
	}
}

func TestLoadOverrides(t *testing.T) {
	clearEnv(t)
	t.Setenv("REDIS_ADDR", "redis:6379")
	t.Setenv("INGEST_INTERVAL", "30s")
	t.Setenv("RATE_LIMIT_PER_MINUTE", "0")
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("PROVIDER2_URL", "http://stub:9000/feed")

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RedisAddr != "redis:6379" || cfg.IngestInterval != 30*time.Second || cfg.RateLimitPerMinute != 0 ||
		cfg.LogLevel != slog.LevelDebug || cfg.Provider2URL != "http://stub:9000/feed" {
		t.Errorf("overrides not applied: %+v", cfg)
	}
}

func TestLoadRequiresTheProviderURLs(t *testing.T) {
	clearEnv(t)
	t.Setenv("PROVIDER1_URL", "")
	t.Setenv("PROVIDER2_URL", "")

	_, err := config.Load()
	for _, key := range []string{"PROVIDER1_URL", "PROVIDER2_URL"} {
		if err == nil || !strings.Contains(err.Error(), key+" is required") {
			t.Errorf("missing %s not reported: %v", key, err)
		}
	}
}

func TestLoadReportsEveryMalformedValue(t *testing.T) {
	clearEnv(t)
	t.Setenv("INGEST_INTERVAL", "often")
	t.Setenv("RATE_LIMIT_PER_MINUTE", "-5")
	t.Setenv("PROVIDER1_URL", "not a url")

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, key := range []string{"INGEST_INTERVAL", "RATE_LIMIT_PER_MINUTE", "PROVIDER1_URL"} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("error does not mention %s: %v", key, err)
		}
	}
}
