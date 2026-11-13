// Package search answers content queries, serving repeated queries from the
// cache.
package search

import (
	"context"
	"time"

	"feedrank/internal/domain"
)

// Repository runs a search against storage, scoring results as of now.
type Repository interface {
	Search(ctx context.Context, q domain.SearchQuery, now time.Time) (domain.SearchResult, error)
}

// Cache returns a cached result for key, or calls load and caches its result.
type Cache interface {
	Fetch(ctx context.Context, key string,
		load func(context.Context) (domain.SearchResult, error)) (domain.SearchResult, bool, error)
}

// Service answers searches.
type Service struct {
	repo  Repository
	cache Cache // optional
}

// NewService returns a Service. cache may be nil.
func NewService(repo Repository, cache Cache) *Service {
	return &Service{repo: repo, cache: cache}
}

// Search validates q and returns one page of results. cached reports whether
// the page came from the cache. A cached page's recency points are as old as
// the entry, which is at most one cache TTL; recency moves in days, so that
// is never visible.
func (s *Service) Search(ctx context.Context, q domain.SearchQuery) (result domain.SearchResult, cached bool, err error) {
	q.Normalize()
	if err := q.Validate(); err != nil {
		return domain.SearchResult{}, false, err
	}

	load := func(ctx context.Context) (domain.SearchResult, error) {
		return s.repo.Search(ctx, q, time.Now())
	}
	if s.cache == nil {
		result, err = load(ctx)
		return result, false, err
	}
	return s.cache.Fetch(ctx, q.CacheKey(), load)
}
