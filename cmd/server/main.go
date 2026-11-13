// Command server runs the content search API and, in the background, the
// ingestion schedule that keeps its data fresh.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"feedrank/internal/config"
	"feedrank/internal/httpapi"
	"feedrank/internal/ingest"
	"feedrank/internal/postgres"
	"feedrank/internal/provider"
	"feedrank/internal/redisstore"
	"feedrank/internal/search"
)

func main() {
	if err := run(); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := postgres.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := postgres.Migrate(ctx, pool); err != nil {
		return err
	}
	store := postgres.NewStore(pool)

	// Redis is optional. The interfaces stay nil without it, which the services
	// read as "no cache, no lock, no rate limit".
	var (
		cache       search.Cache
		invalidator ingest.Invalidator
		locker      ingest.Locker
		limiter     httpapi.RateLimiter
	)
	if cfg.RedisAddr != "" {
		rdb := redisstore.NewClient(cfg.RedisAddr)
		defer func() { _ = rdb.Close() }()
		// Not fatal: the client reconnects on its own once Redis is back.
		if err := rdb.Ping(ctx).Err(); err != nil {
			log.Warn("redis is not reachable yet; carrying on without it until it is", "error", err)
		}

		searchCache := redisstore.NewSearchCache(rdb, cfg.CacheTTL, log)
		cache, invalidator = searchCache, searchCache
		locker = redisstore.NewLocker(rdb)
		if cfg.RateLimitPerMinute > 0 {
			limiter = redisstore.NewRateLimiter(rdb, cfg.RateLimitPerMinute, time.Minute)
		}
	}

	providers, err := newProviders(cfg)
	if err != nil {
		return err
	}
	ingester := ingest.NewService(ingest.Options{
		Providers: providers,
		Store:     store,
		Locker:    locker,
		Cache:     invalidator,
		Logger:    log,
	})

	srv := &http.Server{
		Addr: cfg.Addr,
		Handler: httpapi.NewHandler(httpapi.Options{
			Search:      search.NewService(store, cache),
			Ingest:      ingester,
			AdminAPIKey: cfg.AdminAPIKey,
			RateLimiter: limiter,
			Database:    store,
			Logger:      log,
		}),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	var background sync.WaitGroup
	background.Go(func() { ingester.Schedule(ctx, cfg.IngestInterval) })

	serveErr := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.Addr, "redis", cfg.RedisAddr != "")
		serveErr <- srv.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
	}

	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	err = srv.Shutdown(shutdownCtx)
	background.Wait() // let an in-flight ingestion finish or roll back cleanly
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("shutting down: %w", err)
	}
	return nil
}

// newProviders builds the two providers from the brief. A new provider is one
// more entry here, plus its decoder in package provider.
func newProviders(cfg config.Config) ([]provider.Provider, error) {
	p1, err := provider.NewJSONProvider("provider1", cfg.Provider1URL, provider.NewClient(provider.DefaultClientConfig()))
	if err != nil {
		return nil, err
	}
	p2, err := provider.NewXMLProvider("provider2", cfg.Provider2URL, provider.NewClient(provider.DefaultClientConfig()))
	if err != nil {
		return nil, err
	}
	return []provider.Provider{p1, p2}, nil
}
