package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"feedrank/internal/domain"
	"feedrank/internal/ingest"
)

type fakeSearcher struct {
	result domain.SearchResult
	got    domain.SearchQuery
	err    error
}

func (f *fakeSearcher) Search(_ context.Context, q domain.SearchQuery) (domain.SearchResult, bool, error) {
	f.got = q
	return f.result, true, f.err
}

type fakeIngester struct {
	err    error
	ctxErr *error // where to record the run context's state, if set
}

func (f fakeIngester) Run(ctx context.Context) ([]ingest.Result, error) {
	if f.ctxErr != nil {
		*f.ctxErr = ctx.Err()
	}
	if f.err != nil {
		return nil, f.err
	}
	return []ingest.Result{{Provider: "provider1", Pages: 1, Fetched: 4, Changed: 4}}, nil
}

type fakeLimiter struct {
	allowed bool
	err     error
}

func (f fakeLimiter) Allow(context.Context, string) (bool, time.Duration, error) {
	// A fractional wait, so the tests see that Retry-After rounds up.
	return f.allowed, 29200 * time.Millisecond, f.err
}

type fakeDB struct{ err error }

func (f fakeDB) Ping(context.Context) error { return f.err }

func newTestHandler(opts Options) http.Handler {
	if opts.Search == nil {
		opts.Search = &fakeSearcher{}
	}
	if opts.Ingest == nil {
		opts.Ingest = fakeIngester{}
	}
	if opts.Database == nil {
		opts.Database = fakeDB{}
	}
	opts.Logger = slog.New(slog.DiscardHandler)
	return NewHandler(opts)
}

func do(h http.Handler, method, target string, header ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, http.NoBody)
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestSearch(t *testing.T) {
	published := time.Date(2024, 3, 14, 0, 0, 0, 0, time.UTC)
	searcher := &fakeSearcher{result: domain.SearchResult{
		Total: 21, Page: 2, PageSize: 10,
		Items: []domain.Content{{
			ID: 7, Provider: "provider2", ExternalID: "a1", Title: "Cast Iron Care Guide",
			Type: domain.Text, PublishedAt: published,
			Metrics: domain.Metrics{ReadingTime: 8, Reactions: 450},
			Score:   domain.Score{Base: 17, TypeCoefficient: 1, Engagement: 281.25, Final: 298.25},
		}},
	}}
	h := newTestHandler(Options{Search: searcher})

	rec := do(h, http.MethodGet, "/api/v1/search?q=go&type=text&page=2&page_size=10")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}

	want := domain.SearchQuery{Keyword: "go", Type: domain.Text, Page: 2, PageSize: 10}
	if searcher.got != want {
		t.Errorf("searcher got %+v, want %+v", searcher.got, want)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	item := body["items"].([]any)[0].(map[string]any)
	if item["score"] != 298.25 || item["content_type"] != "text" {
		t.Errorf("item = %v", item)
	}
	metrics := item["metrics"].(map[string]any)
	if _, ok := metrics["views"]; ok || metrics["reactions"] != 450.0 {
		t.Errorf("metrics = %v, want only the text counters", metrics)
	}
	if p := body["pagination"].(map[string]any); p["total_pages"] != 3.0 {
		t.Errorf("pagination = %v", p)
	}
	if m := body["meta"].(map[string]any); m["sort"] != "relevance" || m["cached"] != true {
		t.Errorf("meta = %v, want the applied sort and the cache flag", m)
	}
}

func TestSearchReportsEveryProblemAtOnce(t *testing.T) {
	rec := do(newTestHandler(Options{}), http.MethodGet, "/api/v1/search?page=abc&type=podcast&sort=relevance")

	if rec.Code != http.StatusBadRequest || rec.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("status %d, content type %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	var p problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	var fields []string
	for _, f := range p.Errors {
		fields = append(fields, f.Field)
	}
	if strings.Join(fields, ",") != "page,type,sort" {
		t.Errorf("fields = %v, want page, type and sort", fields)
	}
	if p.RequestID == "" {
		t.Error("problem has no request id")
	}
}

func TestInternalErrorsAreNotLeaked(t *testing.T) {
	h := newTestHandler(Options{Search: &fakeSearcher{err: errors.New("pq: password authentication failed")}})
	rec := do(h, http.MethodGet, "/api/v1/search")

	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "password") {
		t.Errorf("status %d, body %s", rec.Code, rec.Body)
	}
}

func TestUnknownAPIPathIsJSON(t *testing.T) {
	rec := do(newTestHandler(Options{}), http.MethodGet, "/api/v1/nope")
	if rec.Code != http.StatusNotFound || rec.Header().Get("Content-Type") != "application/problem+json" {
		t.Errorf("status %d, content type %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}

func TestIngest(t *testing.T) {
	h := newTestHandler(Options{AdminAPIKey: "secret"})

	if rec := do(h, http.MethodPost, "/api/v1/ingest"); rec.Code != http.StatusUnauthorized {
		t.Errorf("without a key: status %d, want 401", rec.Code)
	}
	if rec := do(h, http.MethodPost, "/api/v1/ingest", "X-API-Key", "wrong"); rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong key: status %d, want 401", rec.Code)
	}

	rec := do(h, http.MethodPost, "/api/v1/ingest", "X-API-Key", "secret")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"pages":1`) ||
		!strings.Contains(rec.Body.String(), `"changed":4`) {
		t.Errorf("status %d, body %s", rec.Code, rec.Body)
	}

	busy := newTestHandler(Options{AdminAPIKey: "secret", Ingest: fakeIngester{err: ingest.ErrAlreadyRunning}})
	if rec := do(busy, http.MethodPost, "/api/v1/ingest", "X-API-Key", "secret"); rec.Code != http.StatusConflict {
		t.Errorf("while running: status %d, want 409", rec.Code)
	}
}

// A client that stops waiting must not cancel a run that is half done.
func TestIngestOutlivesTheRequest(t *testing.T) {
	var runCtxErr error
	h := newTestHandler(Options{AdminAPIKey: "secret", Ingest: fakeIngester{ctxErr: &runCtxErr}})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/v1/ingest", http.NoBody)
	req.Header.Set("X-API-Key", "secret")
	h.ServeHTTP(httptest.NewRecorder(), req)

	if runCtxErr != nil {
		t.Errorf("the run's context was %v; it should not follow the request's", runCtxErr)
	}
}

func TestIngestIsDisabledWithoutAKey(t *testing.T) {
	rec := do(newTestHandler(Options{}), http.MethodPost, "/api/v1/ingest")
	if rec.Code == http.StatusOK {
		t.Error("the ingest endpoint is reachable with no admin key configured")
	}
}

func TestRateLimit(t *testing.T) {
	limited := newTestHandler(Options{RateLimiter: fakeLimiter{allowed: false}})
	rec := do(limited, http.MethodGet, "/api/v1/search")
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "30" {
		t.Errorf("status %d, Retry-After %q", rec.Code, rec.Header().Get("Retry-After"))
	}

	broken := newTestHandler(Options{RateLimiter: fakeLimiter{err: errors.New("redis down")}})
	if rec := do(broken, http.MethodGet, "/api/v1/search"); rec.Code != http.StatusOK {
		t.Errorf("with the limiter down: status %d, want the request let through", rec.Code)
	}

	// Probes are never rate limited.
	if rec := do(limited, http.MethodGet, "/healthz"); rec.Code != http.StatusOK {
		t.Errorf("/healthz: status %d", rec.Code)
	}
}

func TestReadiness(t *testing.T) {
	if rec := do(newTestHandler(Options{}), http.MethodGet, "/readyz"); rec.Code != http.StatusOK {
		t.Errorf("healthy: status %d", rec.Code)
	}
	down := newTestHandler(Options{Database: fakeDB{err: errors.New("connection refused")}})
	if rec := do(down, http.MethodGet, "/readyz"); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("database down: status %d, want 503", rec.Code)
	}
	if rec := do(down, http.MethodGet, "/healthz"); rec.Code != http.StatusOK {
		t.Errorf("liveness must not depend on the database: status %d", rec.Code)
	}
}

func TestRequestID(t *testing.T) {
	h := newTestHandler(Options{})

	if got := do(h, http.MethodGet, "/healthz", "X-Request-Id", "abc-123").Header().Get("X-Request-Id"); got != "abc-123" {
		t.Errorf("a valid incoming id was replaced with %q", got)
	}
	if got := do(h, http.MethodGet, "/healthz", "X-Request-Id", "bad id\nINJECTED").Header().Get("X-Request-Id"); strings.Contains(got, "INJECTED") || got == "" {
		t.Errorf("an unsafe incoming id was kept: %q", got)
	}
}

func TestSecurityHeaders(t *testing.T) {
	rec := do(newTestHandler(Options{}), http.MethodGet, "/healthz")
	for header, want := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
	} {
		if got := rec.Header().Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
}

func TestPanicsBecome500s(t *testing.T) {
	h := withRequestID(slog.New(slog.DiscardHandler), recoverPanics(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	})))
	if rec := do(h, http.MethodGet, "/"); rec.Code != http.StatusInternalServerError {
		t.Errorf("status %d, want 500", rec.Code)
	}
}
