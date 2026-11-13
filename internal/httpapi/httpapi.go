// Package httpapi is the HTTP layer: routing, request parsing, responses and
// middleware. The rules about what a valid query is or how content is scored
// live in the packages it calls.
package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"feedrank/internal/domain"
	"feedrank/internal/ingest"
)

// Searcher answers search queries.
type Searcher interface {
	Search(ctx context.Context, q domain.SearchQuery) (domain.SearchResult, bool, error)
}

// Ingester runs ingestion on demand.
type Ingester interface {
	Run(ctx context.Context) ([]ingest.Result, error)
}

// RateLimiter decides whether a client may make another request.
type RateLimiter interface {
	Allow(ctx context.Context, key string) (allowed bool, retryAfter time.Duration, err error)
}

// Pinger checks that a dependency is reachable.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Options configures the handler. RateLimiter is optional, and AdminAPIKey
// may be empty, which disables the ingest endpoint.
type Options struct {
	Search      Searcher
	Ingest      Ingester
	AdminAPIKey string
	RateLimiter RateLimiter
	Database    Pinger
	Logger      *slog.Logger
}

// NewHandler returns the service's HTTP handler.
func NewHandler(opts Options) http.Handler {
	h := &handlers{search: opts.Search, ingest: opts.Ingest, db: opts.Database}
	mux := http.NewServeMux()

	mux.Handle("GET /api/v1/search", rateLimit(opts.RateLimiter, http.HandlerFunc(h.handleSearch)))
	if opts.AdminAPIKey != "" {
		mux.Handle("POST /api/v1/ingest", requireAPIKey(opts.AdminAPIKey, http.HandlerFunc(h.handleIngest)))
	}
	// Without this, an unknown API path would fall through to the dashboard
	// and come back as HTML.
	mux.HandleFunc("GET /api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, r, domain.Errorf(domain.CodeNotFound, "no route for %s", r.URL.Path))
	})

	mux.HandleFunc("GET /healthz", h.handleLive)
	mux.HandleFunc("GET /readyz", h.handleReady)
	mux.HandleFunc("GET /openapi.yaml", serveOpenAPI)
	mux.HandleFunc("GET /docs", serveDocs)
	mux.Handle("GET /", dashboard(opts.Logger))

	// Outermost first: every log line, including a panic's, has a request id.
	return withRequestID(opts.Logger, securityHeaders(recoverPanics(logRequests(mux))))
}
