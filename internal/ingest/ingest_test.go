package ingest_test

import (
	"context"
	"errors"
	"iter"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"feedrank/internal/domain"
	"feedrank/internal/ingest"
	"feedrank/internal/provider"
)

// fakeProvider yields its pages and then, if set, err. Like the real one, it
// stops when the run's context is done.
type fakeProvider struct {
	name  string
	pages []provider.Page
	err   error
}

func (p fakeProvider) Name() string { return p.name }

func (p fakeProvider) Pages(ctx context.Context) iter.Seq2[provider.Page, error] {
	return func(yield func(provider.Page, error) bool) {
		for _, page := range p.pages {
			if err := ctx.Err(); err != nil {
				yield(provider.Page{}, err)
				return
			}
			if !yield(page, nil) {
				return
			}
		}
		if p.err != nil {
			yield(provider.Page{}, p.err)
		}
	}
}

// pages builds n pages of one video each.
func pages(n int) []provider.Page {
	out := make([]provider.Page, 0, n)
	for i := range n {
		out = append(out, provider.Page{Number: i + 1, Items: []domain.Content{video}})
	}
	return out
}

// fakeStore records each Upsert call, which stands for one transaction. It
// reports every item as changed unless told otherwise, and fails the call
// numbered failOn (counting from 1).
type fakeStore struct {
	mu        sync.Mutex
	calls     [][]domain.Content
	unchanged bool
	failOn    int
}

func (s *fakeStore) Upsert(_ context.Context, items []domain.Content) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failOn == len(s.calls)+1 {
		return 0, errors.New("connection reset")
	}
	s.calls = append(s.calls, items)
	if s.unchanged {
		return 0, nil
	}
	return len(items), nil
}

func (s *fakeStore) stored() int {
	n := 0
	for _, c := range s.calls {
		n += len(c)
	}
	return n
}

// fakeLocker grants the lock unless it is held or broken. With lost set, the
// lock is granted but its context is already cancelled, as if another
// instance had taken it over.
type fakeLocker struct {
	held     bool
	err      error
	lost     error
	released bool
}

func (l *fakeLocker) TryLock(ctx context.Context, _ string) (context.Context, func(), bool, error) {
	if l.err != nil || l.held {
		return nil, nil, false, l.err
	}
	held, cancel := context.WithCancelCause(ctx)
	if l.lost != nil {
		cancel(l.lost)
	}
	return held, func() { cancel(nil); l.released = true }, true, nil
}

type fakeCache struct{ invalidations int }

func (c *fakeCache) Invalidate(context.Context) error {
	c.invalidations++
	return nil
}

var video = domain.Content{
	Provider: "p1", ExternalID: "v1", Title: "Go", Type: domain.Video,
	Metrics: domain.Metrics{Views: 15000, Likes: 1200},
}

func newService(store ingest.Store, locker ingest.Locker, cache ingest.Invalidator, providers ...provider.Provider) *ingest.Service {
	return ingest.NewService(ingest.Options{
		Providers: providers, Store: store, Locker: locker, Cache: cache,
		Logger: slog.New(slog.DiscardHandler),
	})
}

func TestRunScoresAndStoresItems(t *testing.T) {
	store, cache, locker := &fakeStore{}, &fakeCache{}, &fakeLocker{}
	p := fakeProvider{name: "p1", pages: []provider.Page{{
		Number:   1,
		Items:    []domain.Content{video},
		Rejected: []provider.Rejection{{ExternalID: "x", Reason: "missing title"}},
	}}}

	results, err := newService(store, locker, cache, p).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	r := results[0]
	if r.Pages != 1 || r.Fetched != 1 || r.Rejected != 1 || r.Changed != 1 || r.Err != nil {
		t.Errorf("result = %+v", r)
	}
	if got := store.calls[0][0].Score.Final; got != 27*1.5+0.8 {
		t.Errorf("stored score = %v; items must be scored before they are stored", got)
	}
	if cache.invalidations != 1 {
		t.Errorf("cache invalidated %d times, want 1", cache.invalidations)
	}
	if !locker.released {
		t.Error("the lock was not released")
	}
}

func TestEachPageIsStoredOnItsOwn(t *testing.T) {
	store := &fakeStore{}
	results, err := newService(store, nil, nil, fakeProvider{name: "p1", pages: pages(3)}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(store.calls) != 3 || results[0].Pages != 3 || results[0].Changed != 3 {
		t.Errorf("%d upserts, result %+v; want one upsert per page", len(store.calls), results[0])
	}
}

// A provider failing part-way keeps the pages it already stored, reports how
// far it got, and still invalidates the cache for what changed.
func TestAFailedPageKeepsTheEarlierOnes(t *testing.T) {
	store, cache := &fakeStore{}, &fakeCache{}
	p := fakeProvider{name: "p1", pages: pages(2), err: errors.New("p1 page 3: unexpected status 503")}

	results, err := newService(store, nil, cache, p).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	r := results[0]
	if r.Err == nil || !strings.Contains(r.Err.Error(), "page 3") {
		t.Errorf("err = %v, want the page 3 failure", r.Err)
	}
	if r.Pages != 2 || r.Fetched != 2 || store.stored() != 2 {
		t.Errorf("result %+v with %d stored; want pages 1 and 2 kept", r, store.stored())
	}
	if cache.invalidations != 1 {
		t.Error("the cache was not invalidated although the stored pages changed rows")
	}
}

func TestAStoreFailureStopsTheProvider(t *testing.T) {
	store := &fakeStore{failOn: 2}
	results, err := newService(store, nil, nil, fakeProvider{name: "p1", pages: pages(3)}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	r := results[0]
	if r.Err == nil || !strings.Contains(r.Err.Error(), "storing page 2") {
		t.Errorf("err = %v, want the page 2 store failure", r.Err)
	}
	if r.Pages != 1 || len(store.calls) != 1 {
		t.Errorf("result %+v; want page 1 stored and nothing after the failure", r)
	}
}

func TestOneFailingProviderDoesNotStopTheOthers(t *testing.T) {
	store := &fakeStore{}
	down := fakeProvider{name: "down", err: errors.New("503")}
	up := fakeProvider{name: "up", pages: pages(1)}

	results, err := newService(store, nil, nil, down, up).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Err == nil {
		t.Error("the failing provider's error was lost")
	}
	if results[1].Err != nil || store.stored() != 1 {
		t.Errorf("the healthy provider was not ingested: %+v", results[1])
	}
}

func TestNothingChangedKeepsTheCache(t *testing.T) {
	cache := &fakeCache{}
	p := fakeProvider{name: "p1", pages: pages(1)}

	if _, err := newService(&fakeStore{unchanged: true}, nil, cache, p).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if cache.invalidations != 0 {
		t.Error("the cache was invalidated although no rows changed")
	}
}

func TestRunRespectsTheLock(t *testing.T) {
	store := &fakeStore{}
	p := fakeProvider{name: "p1", pages: pages(1)}

	_, err := newService(store, &fakeLocker{held: true}, nil, p).Run(context.Background())
	if !errors.Is(err, ingest.ErrAlreadyRunning) {
		t.Fatalf("err = %v, want ErrAlreadyRunning", err)
	}
	if store.stored() != 0 {
		t.Error("ingested while another run held the lock")
	}
}

func TestALostLockStopsTheRun(t *testing.T) {
	store, locker := &fakeStore{}, &fakeLocker{lost: errors.New("the lock was lost")}
	p := fakeProvider{name: "p1", pages: pages(2)}

	results, err := newService(store, locker, nil, p).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if r := results[0]; r.Err == nil || !strings.Contains(r.Err.Error(), "the lock was lost") {
		t.Errorf("err = %v, want the lost lock as the reason", r.Err)
	}
	if store.stored() != 0 {
		t.Error("kept ingesting after the lock was lost")
	}
}

func TestRunStopsAtItsTimeout(t *testing.T) {
	store := &fakeStore{}
	svc := ingest.NewService(ingest.Options{
		Providers: []provider.Provider{fakeProvider{name: "p1", pages: pages(2)}},
		Store:     store,
		Timeout:   time.Nanosecond,
		Logger:    slog.New(slog.DiscardHandler),
	})

	results, err := svc.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if r := results[0]; r.Err == nil || !strings.Contains(r.Err.Error(), "longer than its 1ns limit") {
		t.Errorf("err = %v, want the timeout as the reason", r.Err)
	}
}

func TestRunWithoutRedisStillIngests(t *testing.T) {
	store := &fakeStore{}
	p := fakeProvider{name: "p1", pages: pages(1)}

	_, err := newService(store, &fakeLocker{err: errors.New("redis down")}, nil, p).Run(context.Background())
	if err != nil || store.stored() != 1 {
		t.Fatalf("err = %v, stored %d; an unreachable lock must not block ingestion", err, store.stored())
	}
}
