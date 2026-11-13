//go:build integration

package postgres_test

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"feedrank/internal/domain"
	"feedrank/internal/postgres"
	"feedrank/internal/scoring"
)

// These tests need a real Postgres: generated columns, the conflict clause and
// ts_rank_cd's ordering are exactly what a fake could not check. Set
// TEST_DATABASE_URL to use an existing database; otherwise testcontainers
// starts a throwaway one (Docker required).

var pool *pgxpool.Pool

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	ctx := context.Background()

	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		container, err := tcpostgres.Run(ctx, "postgres:18-alpine",
			tcpostgres.WithDatabase("search_test"),
			tcpostgres.WithUsername("search"),
			tcpostgres.WithPassword("search"),
			tcpostgres.BasicWaitStrategies(),
		)
		if err != nil {
			fmt.Fprintln(os.Stderr, "starting postgres:", err)
			return 1
		}
		defer func() { _ = container.Terminate(context.Background()) }()

		if url, err = container.ConnectionString(ctx, "sslmode=disable"); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}

	var err error
	if pool, err = postgres.Connect(ctx, url); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer pool.Close()

	if err := postgres.Migrate(ctx, pool); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return m.Run()
}

func newStore(t *testing.T) *postgres.Store {
	t.Helper()
	if _, err := pool.Exec(context.Background(), "TRUNCATE contents"); err != nil {
		t.Fatal(err)
	}
	return postgres.NewStore(pool)
}

var now = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

func item(provider, id, title string, typ domain.ContentType, age time.Duration, m domain.Metrics, tags ...string) domain.Content {
	c := domain.Content{
		Provider: provider, ExternalID: id, Title: title, Type: typ,
		Tags: append([]string{}, tags...), PublishedAt: now.Add(-age), Metrics: m,
	}
	c.Score = scoring.Static(c)
	return c
}

func upsert(t *testing.T, s *postgres.Store, items ...domain.Content) int {
	t.Helper()
	changed, err := s.Upsert(context.Background(), items)
	if err != nil {
		t.Fatal(err)
	}
	return changed
}

func search(t *testing.T, s *postgres.Store, q domain.SearchQuery) domain.SearchResult {
	t.Helper()
	q.Normalize()
	if err := q.Validate(); err != nil {
		t.Fatal(err)
	}
	r, err := s.Search(context.Background(), q, now)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func ids(r domain.SearchResult) []string {
	out := make([]string, 0, len(r.Items))
	for _, c := range r.Items {
		out = append(out, c.ExternalID)
	}
	return out
}

func TestUpsertIsIdempotent(t *testing.T) {
	s := newStore(t)
	day := 24 * time.Hour
	a := item("p1", "v1", "Sourdough Basics", domain.Video, day, domain.Metrics{Views: 1000, Likes: 10})
	b := item("p1", "v2", "Houseplant Care", domain.Video, day, domain.Metrics{Views: 2000, Likes: 20})

	if got := upsert(t, s, a, b); got != 2 {
		t.Fatalf("first write changed %d rows, want 2", got)
	}
	if got := upsert(t, s, a, b); got != 0 {
		t.Fatalf("rewriting identical items changed %d rows, want 0", got)
	}

	b.Metrics.Views = 3000
	b.Score = scoring.Static(b)
	if got := upsert(t, s, a, b); got != 1 {
		t.Fatalf("changing one item changed %d rows, want 1", got)
	}
}

func TestSameIDFromTwoProvidersIsTwoItems(t *testing.T) {
	s := newStore(t)
	upsert(t, s,
		item("provider1", "v1", "Sourdough Starter Basics", domain.Video, time.Hour, domain.Metrics{Views: 1}),
		item("provider2", "v1", "Introduction to Composting", domain.Video, time.Hour, domain.Metrics{Views: 1}),
	)
	if r := search(t, s, domain.SearchQuery{}); r.Total != 2 {
		t.Fatalf("total = %d, want 2", r.Total)
	}
}

// The SQL ranking must agree with the Go formula, recency included.
func TestPopularityMatchesTheFormula(t *testing.T) {
	s := newStore(t)
	day := 24 * time.Hour
	items := []domain.Content{
		item("p", "fresh-video", "A", domain.Video, 2*day, domain.Metrics{Views: 10000, Likes: 500}),
		item("p", "month-video", "B", domain.Video, 20*day, domain.Metrics{Views: 10000, Likes: 500}),
		item("p", "quarter-text", "C", domain.Text, 60*day, domain.Metrics{ReadingTime: 10, Reactions: 100}),
		item("p", "old-text", "D", domain.Text, 400*day, domain.Metrics{ReadingTime: 8, Reactions: 450}),
		item("p", "week-edge", "E", domain.Video, 7*day, domain.Metrics{Views: 100}),
	}
	upsert(t, s, items...)

	r := search(t, s, domain.SearchQuery{Sort: domain.SortPopularity})
	if len(r.Items) != len(items) {
		t.Fatalf("got %d items, want %d", len(r.Items), len(items))
	}

	for i, got := range r.Items {
		var src domain.Content
		for _, c := range items {
			if c.ExternalID == got.ExternalID {
				src = c
			}
		}
		wantRecency := scoring.Recency(src.PublishedAt, now)
		wantFinal := scoring.Static(src).Final + wantRecency
		if got.Score.Recency != wantRecency || math.Abs(got.Score.Final-wantFinal) > 1e-9 {
			t.Errorf("%s: recency %v final %v, want %v and %v",
				got.ExternalID, got.Score.Recency, got.Score.Final, wantRecency, wantFinal)
		}
		if i > 0 && r.Items[i-1].Score.Final < got.Score.Final {
			t.Errorf("results are not in descending score order at %d", i)
		}
	}

	// The ancient article still wins: its engagement term is 450/8 × 5.
	if r.Items[0].ExternalID != "old-text" {
		t.Errorf("top result = %s, want old-text", r.Items[0].ExternalID)
	}
}

// Popularity order ranks a candidate window, not every match. The window must
// never change the answer: every page has to match a full sort done in Go,
// with and without filters. Scores are packed within a few points of each
// other and ages are spread over every recency tier, so recency reorders a
// lot of rows.
func TestPopularityWindowMatchesAFullSort(t *testing.T) {
	s := newStore(t)
	rng := rand.New(rand.NewPCG(1, 2))
	ages := []time.Duration{24 * time.Hour, 10 * 24 * time.Hour, 60 * 24 * time.Hour, 400 * 24 * time.Hour}

	var items []domain.Content
	titles := []string{"Garden notes", "Kitchen notes"}
	for i := range 300 {
		age, title := ages[rng.IntN(len(ages))], titles[rng.IntN(len(titles))]
		if i%2 == 0 {
			items = append(items, item("p", fmt.Sprintf("v%03d", i), title, domain.Video, age,
				domain.Metrics{Views: 1000 + rng.Int64N(3000), Likes: rng.Int64N(50)}))
		} else {
			items = append(items, item("p", fmt.Sprintf("t%03d", i), title, domain.Text, age,
				domain.Metrics{ReadingTime: 1 + rng.Int64N(4), Reactions: rng.Int64N(10)}))
		}
	}
	upsert(t, s, items...) // ids follow this order, which breaks ties

	expected := func(typ domain.ContentType, keyword string) []string {
		type ranked struct {
			id    string
			final float64
			seq   int
		}
		var all []ranked
		for i, c := range items {
			if (typ == "" || c.Type == typ) && (keyword == "" || strings.HasPrefix(strings.ToLower(c.Title), keyword)) {
				all = append(all, ranked{c.ExternalID, c.Score.Final + scoring.Recency(c.PublishedAt, now), i})
			}
		}
		slices.SortFunc(all, func(a, b ranked) int {
			return cmp.Or(cmp.Compare(b.final, a.final), cmp.Compare(a.seq, b.seq))
		})
		out := make([]string, len(all))
		for i, r := range all {
			out[i] = r.id
		}
		return out
	}

	filters := []struct {
		typ     domain.ContentType
		keyword string
	}{{"", ""}, {domain.Video, ""}, {domain.Text, ""}, {"", "garden"}, {domain.Text, "kitchen"}}

	for _, f := range filters {

		want := expected(f.typ, f.keyword)
		for _, size := range []int{1, 7, 20, 100} {
			var got []string
			for page := 1; ; page++ {
				r := search(t, s, domain.SearchQuery{
					Keyword: f.keyword, Type: f.typ, Sort: domain.SortPopularity, Page: page, PageSize: size,
				})
				if r.Total != len(want) {
					t.Fatalf("type %q, keyword %q: total %d, want %d", f.typ, f.keyword, r.Total, len(want))
				}
				if len(r.Items) == 0 {
					break
				}
				got = append(got, ids(r)...)
			}
			if fmt.Sprint(got) != fmt.Sprint(want) {
				t.Errorf("type %q, keyword %q, page size %d: the windowed order differs from a full sort",
					f.typ, f.keyword, size)
			}
		}
	}
}

// Ingestion stores a feed one page per transaction. A run over many pages has
// to land every row, and running it again must change nothing.
func TestUpsertingManyPages(t *testing.T) {
	s := newStore(t)
	const pages, perPage = 50, 200

	run := func() (changed int) {
		for p := range pages {
			batch := make([]domain.Content, 0, perPage)
			for i := range perPage {
				batch = append(batch, item("p", fmt.Sprintf("%d-%d", p, i), "Item", domain.Video, time.Hour,
					domain.Metrics{Views: int64(i * 10), Likes: int64(i)}))
			}
			changed += upsert(t, s, batch...)
		}
		return changed
	}

	if got := run(); got != pages*perPage {
		t.Fatalf("first run changed %d rows, want %d", got, pages*perPage)
	}
	if got := run(); got != 0 {
		t.Fatalf("an identical second run changed %d rows, want 0", got)
	}
	if r := search(t, s, domain.SearchQuery{}); r.Total != pages*perPage {
		t.Fatalf("total = %d, want %d", r.Total, pages*perPage)
	}
}

func TestKeywordSearch(t *testing.T) {
	s := newStore(t)
	h := time.Hour
	upsert(t, s,
		item("p", "1", "Fermentation for Beginners", domain.Video, h, domain.Metrics{Views: 100}, "homestead"),
		item("p", "2", "Introduction to Composting", domain.Video, h, domain.Metrics{Views: 90000}, "homestead", "soil"),
		item("p", "3", "İstanbul'da Yazılım Eğitimi", domain.Text, h, domain.Metrics{ReadingTime: 5}),
		item("p", "4", "Cast Iron Care Guide", domain.Text, h, domain.Metrics{ReadingTime: 8}),
	)

	tests := []struct {
		name    string
		keyword string
		want    []string
	}{
		{"title match", "fermentation", []string{"1"}},
		{"tag match", "soil", []string{"2"}},
		{"stemming", "beginner", []string{"1"}},
		{"a partial word matches as a prefix", "begin", []string{"1"}},
		{"three letters are enough", "fer", []string{"1"}},
		{"shorter terms must match whole words", "fe", []string{}},
		{"operators still work on prefixes", "soi or fermen", []string{"1", "2"}},
		{"accents and Turkish İ are folded", "istanbul egitimi", []string{"3"}},
		{"case is ignored", "COMPOSTING", []string{"2"}},
		{"no match", "rust", []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ids(search(t, s, domain.SearchQuery{Keyword: tt.keyword}))
			if fmt.Sprint(got) != fmt.Sprint(tt.want) {
				t.Errorf("search(%q) = %v, want %v", tt.keyword, got, tt.want)
			}
		})
	}

	// "homestead" matches both by tag. Relevance ranks them equally and the
	// score breaks the tie; both orders put the popular composting video first.
	for _, sort := range []domain.Sort{domain.SortRelevance, domain.SortPopularity} {
		got := ids(search(t, s, domain.SearchQuery{Keyword: "homestead", Sort: sort}))
		if fmt.Sprint(got) != "[2 1]" {
			t.Errorf("sort=%s: got %v, want [2 1]", sort, got)
		}
	}
}

func TestRelevanceRanksTitleMatchesFirst(t *testing.T) {
	s := newStore(t)
	h := time.Hour
	upsert(t, s,
		item("p", "tagged", "Weekly Roundup", domain.Video, h, domain.Metrics{Views: 1_000_000}, "golang"),
		item("p", "titled", "Golang Generics", domain.Video, h, domain.Metrics{Views: 10}),
	)

	// By relevance the title match wins despite far fewer views.
	if got := ids(search(t, s, domain.SearchQuery{Keyword: "golang", Sort: domain.SortRelevance})); fmt.Sprint(got) != "[titled tagged]" {
		t.Errorf("relevance order = %v, want [titled tagged]", got)
	}
	if got := ids(search(t, s, domain.SearchQuery{Keyword: "golang", Sort: domain.SortPopularity})); fmt.Sprint(got) != "[tagged titled]" {
		t.Errorf("popularity order = %v, want [tagged titled]", got)
	}
}

func TestTypeFilterAndPagination(t *testing.T) {
	s := newStore(t)
	var items []domain.Content
	for i := range 5 {
		items = append(items,
			item("p", fmt.Sprintf("v%d", i), "Video", domain.Video, time.Hour, domain.Metrics{Views: int64(1000 * (i + 1))}),
			item("p", fmt.Sprintf("t%d", i), "Text", domain.Text, time.Hour, domain.Metrics{ReadingTime: int64(i + 1)}),
		)
	}
	upsert(t, s, items...)

	page := search(t, s, domain.SearchQuery{Type: domain.Video, PageSize: 2, Page: 2})
	if page.Total != 5 || page.TotalPages() != 3 {
		t.Errorf("total %d over %d pages, want 5 over 3", page.Total, page.TotalPages())
	}
	if fmt.Sprint(ids(page)) != "[v2 v1]" {
		t.Errorf("page 2 = %v, want [v2 v1]", ids(page))
	}

	past := search(t, s, domain.SearchQuery{Type: domain.Text, PageSize: 2, Page: 4})
	if past.Total != 5 || len(past.Items) != 0 {
		t.Errorf("a page past the end returned total %d and %d items, want 5 and none", past.Total, len(past.Items))
	}
}
