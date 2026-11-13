package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"feedrank/internal/domain"
)

type handlers struct {
	search Searcher
	ingest Ingester
	db     Pinger
}

func (h *handlers) handleSearch(w http.ResponseWriter, r *http.Request) {
	q, err := parseSearchQuery(r.URL.Query())
	if err != nil {
		writeError(w, r, err)
		return
	}

	result, cached, err := h.search.Search(r.Context(), q)
	if err != nil {
		writeError(w, r, err)
		return
	}
	q.Normalize() // echo the sort that was actually applied
	writeJSON(w, http.StatusOK, newSearchResponse(result, q.Sort, cached))
}

// parseSearchQuery reads the query string. Values that fail to parse and
// values that parse but are invalid are reported together, so a client sees
// every problem in one response.
func parseSearchQuery(v url.Values) (domain.SearchQuery, error) {
	invalid := domain.Errorf(domain.CodeInvalid, "invalid search query")
	q := domain.SearchQuery{
		Keyword: v.Get("q"),
		Type:    domain.ContentType(v.Get("type")),
		Sort:    domain.Sort(v.Get("sort")),
	}

	for _, param := range []struct {
		name string
		dst  *int
	}{{"page", &q.Page}, {"page_size", &q.PageSize}} {
		raw := v.Get(param.name)
		if raw == "" {
			continue
		}
		n, err := strconv.Atoi(raw)
		if err != nil {
			invalid.AddField(param.name, "must be an integer")
			continue
		}
		*param.dst = n
	}

	// The search service validates as well; doing it here too is what lets a
	// parse error and a validation error appear in the same response.
	checked := q
	checked.Normalize()
	if e, ok := errors.AsType[*domain.Error](checked.Validate()); ok {
		invalid.Fields = append(invalid.Fields, e.Fields...)
	}
	if len(invalid.Fields) > 0 {
		return domain.SearchQuery{}, invalid
	}
	return q, nil
}

// handleIngest runs ingestion and reports each provider's outcome once the run
// is over. A large feed can take a long time, so the server's write deadline
// is lifted (INGEST_TIMEOUT bounds the run instead), and the run is detached
// from the request: a client that gives up waiting does not throw away a run
// that is half done.
func (h *handlers) handleIngest(w http.ResponseWriter, r *http.Request) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})

	results, err := h.ingest.Run(context.WithoutCancel(r.Context()))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newIngestResponse(results))
}

// handleLive reports that the process is up. It checks no dependencies: a
// database outage should take an instance out of rotation, not restart it.
func (h *handlers) handleLive(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleReady reports whether the instance can serve traffic, which needs
// the database. Redis is optional, so it is not checked.
func (h *handlers) handleReady(w http.ResponseWriter, r *http.Request) {
	if err := h.db.Ping(r.Context()); err != nil {
		logger(r.Context()).WarnContext(r.Context(), "readiness check failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable", "database": "unreachable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
