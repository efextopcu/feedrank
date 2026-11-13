package redisstore

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"feedrank/internal/domain"
)

func newRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := NewClient(mr.Addr())
	t.Cleanup(func() { _ = client.Close() })
	return mr, client
}

func TestSearchCache(t *testing.T) {
	_, client := newRedis(t)
	cache := NewSearchCache(client, time.Minute, slog.New(slog.DiscardHandler))
	ctx := context.Background()

	loads := 0
	load := func(context.Context) (domain.SearchResult, error) {
		loads++
		return domain.SearchResult{Total: loads, Items: []domain.Content{{Title: "Go"}}}, nil
	}

	first, hit, err := cache.Fetch(ctx, "k", load)
	if err != nil || hit || first.Total != 1 {
		t.Fatalf("first fetch: result %+v hit %v err %v, want a miss that loads", first, hit, err)
	}

	second, hit, _ := cache.Fetch(ctx, "k", load)
	if !hit || second.Total != 1 || second.Items[0].Title != "Go" || loads != 1 {
		t.Fatalf("second fetch: result %+v hit %v after %d loads, want the cached result", second, hit, loads)
	}

	if err := cache.Invalidate(ctx); err != nil {
		t.Fatal(err)
	}
	third, hit, _ := cache.Fetch(ctx, "k", load)
	if hit || third.Total != 2 {
		t.Fatalf("after Invalidate: result %+v hit %v, want a fresh load", third, hit)
	}
}

func TestSearchCacheDoesNotStoreErrors(t *testing.T) {
	_, client := newRedis(t)
	cache := NewSearchCache(client, time.Minute, slog.New(slog.DiscardHandler))
	boom := errors.New("database down")

	_, _, err := cache.Fetch(context.Background(), "k", func(context.Context) (domain.SearchResult, error) {
		return domain.SearchResult{}, boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the load error", err)
	}
	_, hit, _ := cache.Fetch(context.Background(), "k", func(context.Context) (domain.SearchResult, error) {
		return domain.SearchResult{}, nil
	})
	if hit {
		t.Error("a failed load was cached")
	}
}

func TestSearchCacheWorksWithoutRedis(t *testing.T) {
	mr, client := newRedis(t)
	cache := NewSearchCache(client, time.Minute, slog.New(slog.DiscardHandler))
	mr.Close()

	result, hit, err := cache.Fetch(context.Background(), "k", func(context.Context) (domain.SearchResult, error) {
		return domain.SearchResult{Total: 7}, nil
	})
	if err != nil || hit || result.Total != 7 {
		t.Fatalf("result %+v hit %v err %v, want the loaded result despite Redis being down", result, hit, err)
	}
}

func TestLocker(t *testing.T) {
	mr, client := newRedis(t)
	locker := NewLocker(client)
	ctx := context.Background()

	unlock, ok, err := locker.TryLock(ctx, "ingest", time.Minute)
	if err != nil || !ok {
		t.Fatalf("first TryLock: ok %v err %v", ok, err)
	}
	if _, ok, _ := locker.TryLock(ctx, "ingest", time.Minute); ok {
		t.Fatal("a held lock was granted twice")
	}

	unlock()
	unlock2, ok, _ := locker.TryLock(ctx, "ingest", time.Minute)
	if !ok {
		t.Fatal("lock was not released")
	}

	// A holder whose lock expired must not release the next holder's lock.
	mr.FastForward(2 * time.Minute)
	_, ok, _ = locker.TryLock(ctx, "ingest", time.Minute)
	if !ok {
		t.Fatal("expired lock was not freed")
	}
	unlock2()
	if _, ok, _ := locker.TryLock(ctx, "ingest", time.Minute); ok {
		t.Fatal("a stale holder released someone else's lock")
	}
}

func TestRateLimiter(t *testing.T) {
	_, client := newRedis(t)
	limiter := NewRateLimiter(client, 2, time.Minute)
	clock := time.Date(2026, 1, 1, 10, 0, 15, 0, time.UTC)
	limiter.now = func() time.Time { return clock }
	ctx := context.Background()

	for i := range 2 {
		if ok, _, err := limiter.Allow(ctx, "1.2.3.4"); err != nil || !ok {
			t.Fatalf("request %d: allowed %v err %v", i+1, ok, err)
		}
	}
	ok, retryAfter, _ := limiter.Allow(ctx, "1.2.3.4")
	if ok || retryAfter != 45*time.Second {
		t.Fatalf("third request: allowed %v retry after %v, want denied for 45s", ok, retryAfter)
	}

	if ok, _, _ := limiter.Allow(ctx, "5.6.7.8"); !ok {
		t.Error("another client shares the first client's budget")
	}

	clock = clock.Add(45 * time.Second) // next window
	if ok, _, _ := limiter.Allow(ctx, "1.2.3.4"); !ok {
		t.Error("budget did not reset with the window")
	}
}
