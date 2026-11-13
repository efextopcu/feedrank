package domain_test

import (
	"errors"
	"strings"
	"testing"

	"feedrank/internal/domain"
)

func TestNormalizeDefaults(t *testing.T) {
	tests := []struct {
		name string
		in   domain.SearchQuery
		want domain.SearchQuery
	}{
		{
			name: "listing defaults to popularity",
			in:   domain.SearchQuery{},
			want: domain.SearchQuery{Sort: domain.SortPopularity, Page: 1, PageSize: domain.DefaultPageSize},
		},
		{
			name: "keyword search defaults to relevance",
			in:   domain.SearchQuery{Keyword: "  go  "},
			want: domain.SearchQuery{Keyword: "go", Sort: domain.SortRelevance, Page: 1, PageSize: domain.DefaultPageSize},
		},
		{
			name: "explicit values are lowercased and kept",
			in:   domain.SearchQuery{Keyword: "go", Type: " Video ", Sort: "POPULARITY", Page: 3, PageSize: 5},
			want: domain.SearchQuery{Keyword: "go", Type: domain.Video, Sort: domain.SortPopularity, Page: 3, PageSize: 5},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.in
			got.Normalize()
			if got != tt.want {
				t.Errorf("Normalize() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	valid := domain.SearchQuery{Sort: domain.SortPopularity, Page: 1, PageSize: 20}

	tests := []struct {
		name       string
		mutate     func(*domain.SearchQuery)
		wantFields []string
	}{
		{name: "valid", mutate: func(*domain.SearchQuery) {}},
		{name: "unknown type", mutate: func(q *domain.SearchQuery) { q.Type = "podcast" }, wantFields: []string{"type"}},
		{name: "unknown sort", mutate: func(q *domain.SearchQuery) { q.Sort = "newest" }, wantFields: []string{"sort"}},
		{name: "relevance without keyword", mutate: func(q *domain.SearchQuery) { q.Sort = domain.SortRelevance }, wantFields: []string{"sort"}},
		{name: "page too deep", mutate: func(q *domain.SearchQuery) { q.Page = domain.MaxPage + 1 }, wantFields: []string{"page"}},
		{name: "page size too large", mutate: func(q *domain.SearchQuery) { q.PageSize = domain.MaxPageSize + 1 }, wantFields: []string{"page_size"}},
		{name: "keyword too long", mutate: func(q *domain.SearchQuery) { q.Keyword = strings.Repeat("ğ", 201) }, wantFields: []string{"q"}},
		{
			name: "every problem is reported at once",
			mutate: func(q *domain.SearchQuery) {
				q.Type, q.Sort, q.Page = "podcast", "newest", 0
			},
			wantFields: []string{"type", "sort", "page"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := valid
			tt.mutate(&q)
			err := q.Validate()

			if len(tt.wantFields) == 0 {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			var got []string
			if e, ok := errors.AsType[*domain.Error](err); ok {
				for _, f := range e.Fields {
					got = append(got, f.Field)
				}
			}
			if strings.Join(got, ",") != strings.Join(tt.wantFields, ",") {
				t.Errorf("invalid fields = %v, want %v (err: %v)", got, tt.wantFields, err)
			}
		})
	}
}

func TestCacheKeyIgnoresKeywordCase(t *testing.T) {
	a := domain.SearchQuery{Keyword: "Docker", Sort: domain.SortRelevance, Page: 1, PageSize: 20}
	b := a
	b.Keyword = "docker"
	if a.CacheKey() != b.CacheKey() {
		t.Error("keywords differing only in case should share a cache key")
	}

	b.Page = 2
	if a.CacheKey() == b.CacheKey() {
		t.Error("different pages must not share a cache key")
	}
}

func TestTotalPages(t *testing.T) {
	for _, tt := range []struct{ total, size, want int }{
		{0, 20, 0}, {1, 20, 1}, {20, 20, 1}, {21, 20, 2},
	} {
		got := domain.SearchResult{Total: tt.total, PageSize: tt.size}.TotalPages()
		if got != tt.want {
			t.Errorf("TotalPages(total=%d, size=%d) = %d, want %d", tt.total, tt.size, got, tt.want)
		}
	}
}
