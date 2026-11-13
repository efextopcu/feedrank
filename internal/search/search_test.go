package search_test

import (
	"context"
	"testing"
	"time"

	"feedrank/internal/domain"
	"feedrank/internal/search"
)

type fakeRepo struct {
	calls int
	last  domain.SearchQuery
}

func (r *fakeRepo) Search(_ context.Context, q domain.SearchQuery, _ time.Time) (domain.SearchResult, error) {
	r.calls++
	r.last = q
	return domain.SearchResult{Total: 1, Page: q.Page, PageSize: q.PageSize}, nil
}

type mapCache map[string]domain.SearchResult

func (c mapCache) Fetch(ctx context.Context, key string,
	load func(context.Context) (domain.SearchResult, error),
) (domain.SearchResult, bool, error) {
	if r, ok := c[key]; ok {
		return r, true, nil
	}
	r, err := load(ctx)
	if err == nil {
		c[key] = r
	}
	return r, false, err
}

func TestSearchNormalizesBeforeQuerying(t *testing.T) {
	repo := &fakeRepo{}
	if _, _, err := search.NewService(repo, nil).Search(context.Background(), domain.SearchQuery{Keyword: " go "}); err != nil {
		t.Fatal(err)
	}
	want := domain.SearchQuery{Keyword: "go", Sort: domain.SortRelevance, Page: 1, PageSize: domain.DefaultPageSize}
	if repo.last != want {
		t.Errorf("repository got %+v, want %+v", repo.last, want)
	}
}

func TestSearchRejectsInvalidQueries(t *testing.T) {
	repo := &fakeRepo{}
	_, _, err := search.NewService(repo, nil).Search(context.Background(), domain.SearchQuery{Type: "podcast"})
	if domain.CodeOf(err) != domain.CodeInvalid {
		t.Fatalf("err = %v, want a validation error", err)
	}
	if repo.calls != 0 {
		t.Error("an invalid query reached the repository")
	}
}

func TestSearchUsesTheCache(t *testing.T) {
	repo := &fakeRepo{}
	svc := search.NewService(repo, mapCache{})
	ctx := context.Background()

	if _, cached, _ := svc.Search(ctx, domain.SearchQuery{Keyword: "Go"}); cached {
		t.Error("first search was served from the cache")
	}
	if _, cached, _ := svc.Search(ctx, domain.SearchQuery{Keyword: "go"}); !cached {
		t.Error("an equivalent second search missed the cache")
	}
	if repo.calls != 1 {
		t.Errorf("repository called %d times, want 1", repo.calls)
	}
}
