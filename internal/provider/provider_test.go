package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"feedrank/internal/domain"
)

// testClientConfig keeps retries fast and the breaker out of the way.
func testClientConfig() ClientConfig {
	cfg := DefaultClientConfig()
	cfg.RequestsPerSecond = 1000
	cfg.BaseDelay = time.Millisecond
	cfg.MaxDelay = 5 * time.Millisecond
	cfg.FailureThreshold = 100
	return cfg
}

// serve answers every request with body and counts the requests.
func serve(t *testing.T, body string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/plain") // what the live endpoints send
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestJSONProviderFixture(t *testing.T) {
	srv, calls := serve(t, fixture(t, "provider1.json"))
	p, err := NewJSONProvider("provider1", srv.URL, NewClient(testClientConfig()))
	if err != nil {
		t.Fatal(err)
	}

	batch, err := p.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if len(batch.Items) != 4 || len(batch.Rejected) != 0 {
		t.Fatalf("got %d items and %v rejections, want 4 and none", len(batch.Items), batch.Rejected)
	}
	got := batch.Items[0]
	want := domain.Content{
		Provider:    "provider1",
		ExternalID:  "v1",
		Title:       "Sourdough Starter Basics",
		Type:        domain.Video,
		Tags:        []string{"baking", "tutorial"},
		PublishedAt: time.Date(2024, 3, 15, 10, 0, 0, 0, time.UTC),
		Metrics:     domain.Metrics{Views: 15000, Likes: 1200},
	}
	assertContent(t, got, want)

	// The page is shorter than the page size, so there is nothing more to ask for.
	if n := calls.Load(); n != 1 {
		t.Errorf("made %d requests, want 1", n)
	}
}

func TestXMLProviderFixture(t *testing.T) {
	srv, _ := serve(t, fixture(t, "provider2.xml"))
	p, err := NewXMLProvider("provider2", srv.URL, NewClient(testClientConfig()))
	if err != nil {
		t.Fatal(err)
	}

	batch, err := p.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Items) != 4 || len(batch.Rejected) != 0 {
		t.Fatalf("got %d items and %v rejections, want 4 and none", len(batch.Items), batch.Rejected)
	}

	// The one article: "article" maps to text and a bare date is midnight UTC.
	got := batch.Items[3]
	want := domain.Content{
		Provider:    "provider2",
		ExternalID:  "a1",
		Title:       "Cast Iron Care Guide",
		Type:        domain.Text,
		Tags:        []string{"cooking", "kitchen"},
		PublishedAt: time.Date(2024, 3, 14, 0, 0, 0, 0, time.UTC),
		Metrics:     domain.Metrics{ReadingTime: 8, Reactions: 450},
	}
	assertContent(t, got, want)
}

// pagedSpec builds a small JSON spec so paging can be tested with tiny pages.
func pagedSpec(url string) Spec {
	return Spec{
		Name: "paged", URL: url,
		PageParam: "page", SizeParam: "size", PageSize: 2, MaxPages: 10,
		Decode:      decodeJSON,
		Types:       map[string]domain.ContentType{"video": domain.Video},
		DateLayouts: []string{time.RFC3339},
	}
}

func jsonItems(ids ...string) string {
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, fmt.Sprintf(
			`{"id": %q, "title": "Item %s", "type": "video", "published_at": "2024-01-01T00:00:00Z"}`, id, id))
	}
	return `{"contents": [` + strings.Join(parts, ",") + `]}`
}

func TestFetchWalksPagesUntilAShortPage(t *testing.T) {
	pages := map[string]string{
		"1": jsonItems("a", "b"),
		"2": jsonItems("c", "d"),
		"3": jsonItems("e"),
	}
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Query().Get("size") != "2" {
			t.Errorf("size param = %q, want 2", r.URL.Query().Get("size"))
		}
		fmt.Fprint(w, pages[r.URL.Query().Get("page")])
	}))
	defer srv.Close()

	p, err := New(pagedSpec(srv.URL), NewClient(testClientConfig()))
	if err != nil {
		t.Fatal(err)
	}
	batch, err := p.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Items) != 5 || calls.Load() != 3 {
		t.Errorf("got %d items in %d requests, want 5 in 3", len(batch.Items), calls.Load())
	}
}

// The live endpoints ignore the page parameter. A full page of ids already
// seen must end the walk instead of re-fetching the same page MaxPages times.
func TestFetchStopsWhenAPageRepeats(t *testing.T) {
	srv, calls := serve(t, jsonItems("a", "b"))
	p, err := New(pagedSpec(srv.URL), NewClient(testClientConfig()))
	if err != nil {
		t.Fatal(err)
	}

	batch, err := p.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Items) != 2 || calls.Load() != 2 {
		t.Errorf("got %d items in %d requests, want 2 in 2", len(batch.Items), calls.Load())
	}
}

func TestFetchFailsWhenAnyPageFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, jsonItems("a", "b"))
	}))
	defer srv.Close()

	p, err := New(pagedSpec(srv.URL), NewClient(testClientConfig()))
	if err != nil {
		t.Fatal(err)
	}
	batch, err := p.Fetch(context.Background())
	if err == nil || !strings.Contains(err.Error(), "page 2") {
		t.Fatalf("err = %v, want a page 2 failure", err)
	}
	if len(batch.Items) != 0 {
		t.Errorf("a failed fetch returned %d items; it should return none", len(batch.Items))
	}
}

func TestBadItemsAreRejectedNotFatal(t *testing.T) {
	body := `{"contents": [
		{"id": "ok", "title": "Fine", "type": "video", "published_at": "2024-01-01T00:00:00Z", "tags": [" Go ", "go", ""]},
		{"id": "", "title": "No id", "type": "video", "published_at": "2024-01-01T00:00:00Z"},
		{"id": "t", "title": "  ", "type": "video", "published_at": "2024-01-01T00:00:00Z"},
		{"id": "p", "title": "Podcast", "type": "podcast", "published_at": "2024-01-01T00:00:00Z"},
		{"id": "d", "title": "Bad date", "type": "video", "published_at": "15/03/2024"},
		{"id": "n", "title": "Negative", "type": "video", "published_at": "2024-01-01T00:00:00Z", "metrics": {"views": -1}}
	]}`
	srv, _ := serve(t, body)
	p, err := NewJSONProvider("provider1", srv.URL, NewClient(testClientConfig()))
	if err != nil {
		t.Fatal(err)
	}

	batch, err := p.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Items) != 1 || batch.Items[0].ExternalID != "ok" {
		t.Fatalf("items = %+v, want only \"ok\"", batch.Items)
	}
	if tags := strings.Join(batch.Items[0].Tags, ","); tags != "go" {
		t.Errorf("tags = %q, want them trimmed, lowercased and de-duplicated to \"go\"", tags)
	}

	wantReasons := []string{"missing id", "missing title", "unknown content type", "unparseable publish date", "negative metric"}
	if len(batch.Rejected) != len(wantReasons) {
		t.Fatalf("rejected = %+v, want %d rejections", batch.Rejected, len(wantReasons))
	}
	for i, want := range wantReasons {
		if !strings.Contains(batch.Rejected[i].Reason, want) {
			t.Errorf("rejection %d = %q, want it to mention %q", i, batch.Rejected[i].Reason, want)
		}
	}
}

func TestNewRejectsAnIncompleteSpec(t *testing.T) {
	spec := pagedSpec("ftp://example.com")
	if _, err := New(spec, NewClient(testClientConfig())); err == nil {
		t.Error("New accepted a non-http URL")
	}
	spec = pagedSpec("http://example.com")
	spec.Decode = nil
	if _, err := New(spec, NewClient(testClientConfig())); err == nil {
		t.Error("New accepted a spec without a decoder")
	}
}

func assertContent(t *testing.T, got, want domain.Content) {
	t.Helper()
	if got.Provider != want.Provider || got.ExternalID != want.ExternalID || got.Title != want.Title ||
		got.Type != want.Type || !got.PublishedAt.Equal(want.PublishedAt) || got.Metrics != want.Metrics ||
		strings.Join(got.Tags, ",") != strings.Join(want.Tags, ",") {
		t.Errorf("content = %+v\nwant      %+v", got, want)
	}
}
