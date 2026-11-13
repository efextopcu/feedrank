package ingest_test

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"feedrank/internal/domain"
	"feedrank/internal/ingest"
	"feedrank/internal/provider"
)

type fakeProvider struct {
	name  string
	batch provider.Batch
	err   error
}

func (p fakeProvider) Name() string { return p.name }

func (p fakeProvider) Fetch(context.Context) (provider.Batch, error) { return p.batch, p.err }

// fakeStore reports every item as changed unless told otherwise.
type fakeStore struct {
	mu        sync.Mutex
	stored    []domain.Content
	unchanged bool
}

func (s *fakeStore) Upsert(_ context.Context, items []domain.Content) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stored = append(s.stored, items...)
	if s.unchanged {
		return 0, nil
	}
	return len(items), nil
}

type fakeLocker struct {
	held     bool
	err      error
	released bool
}

func (l *fakeLocker) TryLock(context.Context, string, time.Duration) (func(), bool, error) {
	if l.err != nil || l.held {
		return nil, false, l.err
	}
	return func() { l.released = true }, true, nil
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
	p := fakeProvider{name: "p1", batch: provider.Batch{
		Items:    []domain.Content{video},
		Rejected: []provider.Rejection{{ExternalID: "x", Reason: "missing title"}},
	}}

	results, err := newService(store, locker, cache, p).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	r := results[0]
	if r.Fetched != 1 || r.Rejected != 1 || r.Changed != 1 || r.Err != nil {
		t.Errorf("result = %+v", r)
	}
	if got := store.stored[0].Score.Final; got != 27*1.5+0.8 {
		t.Errorf("stored score = %v; items must be scored before they are stored", got)
	}
	if cache.invalidations != 1 {
		t.Errorf("cache invalidated %d times, want 1", cache.invalidations)
	}
	if !locker.released {
		t.Error("the lock was not released")
	}
}

func TestOneFailingProviderDoesNotStopTheOthers(t *testing.T) {
	store := &fakeStore{}
	down := fakeProvider{name: "down", err: errors.New("503")}
	up := fakeProvider{name: "up", batch: provider.Batch{Items: []domain.Content{video}}}

	results, err := newService(store, nil, nil, down, up).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Err == nil {
		t.Error("the failing provider's error was lost")
	}
	if results[1].Err != nil || len(store.stored) != 1 {
		t.Errorf("the healthy provider was not ingested: %+v", results[1])
	}
}

func TestNothingChangedKeepsTheCache(t *testing.T) {
	cache := &fakeCache{}
	p := fakeProvider{name: "p1", batch: provider.Batch{Items: []domain.Content{video}}}

	if _, err := newService(&fakeStore{unchanged: true}, nil, cache, p).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if cache.invalidations != 0 {
		t.Error("the cache was invalidated although no rows changed")
	}
}

func TestRunRespectsTheLock(t *testing.T) {
	store := &fakeStore{}
	p := fakeProvider{name: "p1", batch: provider.Batch{Items: []domain.Content{video}}}

	_, err := newService(store, &fakeLocker{held: true}, nil, p).Run(context.Background())
	if !errors.Is(err, ingest.ErrAlreadyRunning) {
		t.Fatalf("err = %v, want ErrAlreadyRunning", err)
	}
	if len(store.stored) != 0 {
		t.Error("ingested while another run held the lock")
	}
}

func TestRunWithoutRedisStillIngests(t *testing.T) {
	store := &fakeStore{}
	p := fakeProvider{name: "p1", batch: provider.Batch{Items: []domain.Content{video}}}

	_, err := newService(store, &fakeLocker{err: errors.New("redis down")}, nil, p).Run(context.Background())
	if err != nil || len(store.stored) != 1 {
		t.Fatalf("err = %v, stored %d; an unreachable lock must not block ingestion", err, len(store.stored))
	}
}
