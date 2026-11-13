// Package ingest moves content from the providers into storage: fetch, score,
// upsert, and invalidate the search cache when anything changed.
package ingest

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"feedrank/internal/domain"
	"feedrank/internal/provider"
	"feedrank/internal/scoring"
)

// Store persists scored content and reports how many rows changed.
type Store interface {
	Upsert(ctx context.Context, items []domain.Content) (changed int, err error)
}

// Locker grants a lock that expires on its own. It keeps several replicas of
// the service from ingesting at the same moment.
type Locker interface {
	TryLock(ctx context.Context, name string, ttl time.Duration) (unlock func(), ok bool, err error)
}

// Invalidator discards cached search results.
type Invalidator interface {
	Invalidate(ctx context.Context) error
}

// ErrAlreadyRunning means another run, possibly on another replica, holds the
// ingestion lock.
var ErrAlreadyRunning = domain.Errorf(domain.CodeConflict, "an ingestion run is already in progress")

const (
	runTimeout = time.Minute
	// The lock outlives the run's deadline, so it cannot expire under a run
	// that is still finishing.
	lockTTL = runTimeout + 30*time.Second
)

// Options configures a Service. Locker and Cache are optional; without Redis
// the service runs unlocked and uncached.
type Options struct {
	Providers []provider.Provider
	Store     Store
	Locker    Locker
	Cache     Invalidator
	Logger    *slog.Logger
}

// Service runs ingestion.
type Service struct {
	providers []provider.Provider
	store     Store
	locker    Locker
	cache     Invalidator
	log       *slog.Logger
}

// NewService returns a Service.
func NewService(opts Options) *Service {
	return &Service{
		providers: opts.Providers,
		store:     opts.Store,
		locker:    opts.Locker,
		cache:     opts.Cache,
		log:       opts.Logger,
	}
}

// Result summarises one provider's part of a run.
type Result struct {
	Provider string
	Fetched  int // items that passed validation
	Rejected int
	Changed  int // rows inserted or updated; unchanged rows are not rewritten
	Duration time.Duration
	Err      error
}

// Run ingests every provider once, concurrently, so a slow provider does not
// hold up the others. One provider failing does not stop the rest; its error
// is in its Result.
func (s *Service) Run(ctx context.Context) ([]Result, error) {
	if s.locker != nil {
		unlock, ok, err := s.locker.TryLock(ctx, "ingest", lockTTL)
		switch {
		case err != nil:
			// Upserts are idempotent, so running unlocked risks duplicate work,
			// not bad data. Skipping would leave the corpus stale instead.
			s.log.WarnContext(ctx, "ingest lock unavailable, running without it", "error", err)
		case !ok:
			return nil, ErrAlreadyRunning
		default:
			defer unlock()
		}
	}

	runCtx, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()

	results := make([]Result, len(s.providers))
	var wg sync.WaitGroup
	for i, p := range s.providers {
		wg.Go(func() { results[i] = s.runProvider(runCtx, p) })
	}
	wg.Wait()

	if s.cache != nil && changedAny(results) {
		if err := s.cache.Invalidate(ctx); err != nil {
			s.log.WarnContext(ctx, "could not invalidate the search cache; it will expire on its own", "error", err)
		}
	}
	return results, nil
}

func (s *Service) runProvider(ctx context.Context, p provider.Provider) Result {
	start := time.Now()
	result := Result{Provider: p.Name()}
	log := s.log.With("provider", p.Name())

	batch, err := p.Fetch(ctx)
	if err != nil {
		result.Err, result.Duration = err, time.Since(start)
		log.ErrorContext(ctx, "fetch failed", "error", err)
		return result
	}

	for _, r := range batch.Rejected {
		log.WarnContext(ctx, "item rejected", "external_id", r.ExternalID, "reason", r.Reason)
	}
	for i := range batch.Items {
		batch.Items[i].Score = scoring.Static(batch.Items[i])
	}

	result.Fetched, result.Rejected = len(batch.Items), len(batch.Rejected)
	result.Changed, result.Err = s.store.Upsert(ctx, batch.Items)
	result.Duration = time.Since(start)

	if result.Err != nil {
		log.ErrorContext(ctx, "storing items failed", "error", result.Err)
		return result
	}
	log.InfoContext(ctx, "provider ingested",
		"fetched", result.Fetched, "rejected", result.Rejected, "changed", result.Changed,
		"duration_ms", result.Duration.Milliseconds())
	return result
}

// Schedule runs ingestion immediately, so a fresh deployment has data, then
// every interval until ctx is done. It returns once any run in progress has
// finished.
func (s *Service) Schedule(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		if _, err := s.Run(ctx); errors.Is(err, ErrAlreadyRunning) {
			s.log.InfoContext(ctx, "another instance is ingesting, skipping this run")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func changedAny(results []Result) bool {
	for _, r := range results {
		if r.Changed > 0 {
			return true
		}
	}
	return false
}
