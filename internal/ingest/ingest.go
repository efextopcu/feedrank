// Package ingest moves content from the providers into storage: fetch, score,
// upsert, and invalidate the search cache when anything changed.
//
// Feeds are processed a page at a time, each page stored in its own
// transaction. Memory stays bounded however large a feed is, and a run that
// fails part-way keeps the pages it had stored. That is safe because upserts
// are idempotent and nothing relates one item to another: the next run simply
// goes over the same ground again.
package ingest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"feedrank/internal/domain"
	"feedrank/internal/provider"
	"feedrank/internal/scoring"
)

// Store persists scored content, all of one call or none of it, and reports
// how many rows changed.
type Store interface {
	Upsert(ctx context.Context, items []domain.Content) (changed int, err error)
}

// Locker grants a lock that keeps several replicas of the service from
// ingesting at the same moment. The held context is cancelled if the lock is
// lost while the run is still going; release must always be called.
type Locker interface {
	TryLock(ctx context.Context, name string) (held context.Context, release func(), ok bool, err error)
}

// Invalidator discards cached search results.
type Invalidator interface {
	Invalidate(ctx context.Context) error
}

// ErrAlreadyRunning means another run, possibly on another replica, holds the
// ingestion lock.
var ErrAlreadyRunning = domain.Errorf(domain.CodeConflict, "an ingestion run is already in progress")

// DefaultTimeout bounds a run when Options.Timeout is zero.
const DefaultTimeout = time.Hour

// Options configures a Service. Locker and Cache are optional; without Redis
// the service runs unlocked and uncached.
type Options struct {
	Providers []provider.Provider
	Store     Store
	Locker    Locker
	Cache     Invalidator
	Timeout   time.Duration // bounds one run; DefaultTimeout if zero
	Logger    *slog.Logger
}

// Service runs ingestion.
type Service struct {
	providers []provider.Provider
	store     Store
	locker    Locker
	cache     Invalidator
	timeout   time.Duration
	log       *slog.Logger
}

// NewService returns a Service.
func NewService(opts Options) *Service {
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultTimeout
	}
	return &Service{
		providers: opts.Providers,
		store:     opts.Store,
		locker:    opts.Locker,
		cache:     opts.Cache,
		timeout:   opts.Timeout,
		log:       opts.Logger,
	}
}

// Result summarises one provider's part of a run. When Err is set, the counts
// cover the pages stored before the failure.
type Result struct {
	Provider string
	Pages    int // pages stored
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
	runCtx := ctx
	if s.locker != nil {
		held, release, ok, err := s.locker.TryLock(ctx, "ingest")
		switch {
		case err != nil:
			// Upserts are idempotent, so running unlocked risks duplicate work,
			// not bad data. Skipping would leave the corpus stale instead.
			s.log.WarnContext(ctx, "ingest lock unavailable, running without it", "error", err)
		case !ok:
			return nil, ErrAlreadyRunning
		default:
			defer release()
			runCtx = held
		}
	}

	runCtx, cancel := context.WithTimeoutCause(runCtx, s.timeout,
		fmt.Errorf("the run took longer than its %s limit", s.timeout))
	defer cancel()

	results := make([]Result, len(s.providers))
	var wg sync.WaitGroup
	for i, p := range s.providers {
		wg.Go(func() { results[i] = s.runProvider(runCtx, p) })
	}
	wg.Wait()

	// Even a run that stopped part-way may have changed rows.
	if s.cache != nil && changedAny(results) {
		if err := s.cache.Invalidate(ctx); err != nil {
			s.log.WarnContext(ctx, "could not invalidate the search cache; it will expire on its own", "error", err)
		}
	}
	return results, nil
}

// runProvider streams one provider into storage, a page per transaction.
func (s *Service) runProvider(ctx context.Context, p provider.Provider) Result {
	start := time.Now()
	result := Result{Provider: p.Name()}
	log := s.log.With("provider", p.Name())

	for page, err := range p.Pages(ctx) {
		if err == nil {
			err = s.storePage(ctx, log, page, &result)
		}
		if err != nil {
			// When the run itself was stopped (timeout, lost lock, shutdown),
			// that is the more useful explanation.
			if ctx.Err() != nil {
				err = fmt.Errorf("%w (%w)", context.Cause(ctx), err)
			}
			result.Err = err
			break
		}
	}
	result.Duration = time.Since(start)

	attrs := []any{
		"pages", result.Pages, "fetched", result.Fetched, "rejected", result.Rejected,
		"changed", result.Changed, "duration_ms", result.Duration.Milliseconds(),
	}
	if result.Err != nil {
		log.ErrorContext(ctx, "provider ingestion stopped; the pages before the error are stored",
			append(attrs, "error", result.Err)...)
		return result
	}
	log.InfoContext(ctx, "provider ingested", attrs...)
	return result
}

func (s *Service) storePage(ctx context.Context, log *slog.Logger, page provider.Page, result *Result) error {
	for _, r := range page.Rejected {
		log.WarnContext(ctx, "item rejected", "page", page.Number, "external_id", r.ExternalID, "reason", r.Reason)
	}
	for i := range page.Items {
		page.Items[i].Score = scoring.Static(page.Items[i])
	}

	changed, err := s.store.Upsert(ctx, page.Items)
	if err != nil {
		return fmt.Errorf("storing page %d: %w", page.Number, err)
	}
	result.Pages++
	result.Fetched += len(page.Items)
	result.Rejected += len(page.Rejected)
	result.Changed += changed
	log.DebugContext(ctx, "page stored", "page", page.Number, "items", len(page.Items), "changed", changed)
	return nil
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
