package httpapi

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"time"

	"feedrank/internal/domain"
	"feedrank/internal/ingest"
)

// The wire types are separate from the domain types, so a change to the
// domain cannot quietly change the API.

type searchResponse struct {
	Items      []contentResponse `json:"items"`
	Pagination pagination        `json:"pagination"`
	Meta       searchMeta        `json:"meta"`
}

type pagination struct {
	Page       int `json:"page"`
	PageSize   int `json:"page_size"`
	Total      int `json:"total"`
	TotalPages int `json:"total_pages"`
}

type searchMeta struct {
	Sort   domain.Sort `json:"sort"`
	Cached bool        `json:"cached"`
}

type contentResponse struct {
	ID          int64              `json:"id"`
	Provider    string             `json:"provider"`
	ExternalID  string             `json:"external_id"`
	Title       string             `json:"title"`
	Type        domain.ContentType `json:"content_type"`
	Tags        []string           `json:"tags"`
	PublishedAt time.Time          `json:"published_at"`
	Metrics     metricsResponse    `json:"metrics"`
	Score       float64            `json:"score"`
	Breakdown   scoreBreakdown     `json:"score_breakdown"`
	Relevance   float64            `json:"relevance,omitempty"`
}

// metricsResponse carries only the counters that apply to the content type,
// so an article does not claim to have zero views.
type metricsResponse struct {
	Views       *int64 `json:"views,omitempty"`
	Likes       *int64 `json:"likes,omitempty"`
	ReadingTime *int64 `json:"reading_time,omitempty"`
	Reactions   *int64 `json:"reactions,omitempty"`
}

type scoreBreakdown struct {
	Base            float64 `json:"base"`
	TypeCoefficient float64 `json:"type_coefficient"`
	Engagement      float64 `json:"engagement"`
	Recency         float64 `json:"recency"`
	Final           float64 `json:"final"`
}

func newSearchResponse(r domain.SearchResult, sort domain.Sort, cached bool) searchResponse {
	items := make([]contentResponse, 0, len(r.Items))
	for _, c := range r.Items {
		items = append(items, newContentResponse(c))
	}
	return searchResponse{
		Items: items,
		Pagination: pagination{
			Page:       r.Page,
			PageSize:   r.PageSize,
			Total:      r.Total,
			TotalPages: r.TotalPages(),
		},
		Meta: searchMeta{Sort: sort, Cached: cached},
	}
}

func newContentResponse(c domain.Content) contentResponse {
	var m metricsResponse
	switch c.Type {
	case domain.Video:
		m.Views, m.Likes = &c.Metrics.Views, &c.Metrics.Likes
	case domain.Text:
		m.ReadingTime, m.Reactions = &c.Metrics.ReadingTime, &c.Metrics.Reactions
	}

	tags := c.Tags
	if tags == nil {
		tags = []string{}
	}
	return contentResponse{
		ID:          c.ID,
		Provider:    c.Provider,
		ExternalID:  c.ExternalID,
		Title:       c.Title,
		Type:        c.Type,
		Tags:        tags,
		PublishedAt: c.PublishedAt,
		Metrics:     m,
		Score:       round(c.Score.Final),
		Breakdown: scoreBreakdown{
			Base:            round(c.Score.Base),
			TypeCoefficient: c.Score.TypeCoefficient,
			Engagement:      round(c.Score.Engagement),
			Recency:         c.Score.Recency,
			Final:           round(c.Score.Final),
		},
		Relevance: round(c.Relevance),
	}
}

// round keeps four decimals, so float noise such as 0.8400000000000001 does
// not reach clients. Ranking always uses the unrounded values.
func round(v float64) float64 { return math.Round(v*1e4) / 1e4 }

type ingestResponse struct {
	Providers []ingestResult `json:"providers"`
}

type ingestResult struct {
	Provider   string `json:"provider"`
	Fetched    int    `json:"fetched"`
	Rejected   int    `json:"rejected"`
	Changed    int    `json:"changed"`
	DurationMS int64  `json:"duration_ms"`
	Error      string `json:"error,omitempty"`
}

func newIngestResponse(results []ingest.Result) ingestResponse {
	out := ingestResponse{Providers: make([]ingestResult, 0, len(results))}
	for _, r := range results {
		res := ingestResult{
			Provider:   r.Provider,
			Fetched:    r.Fetched,
			Rejected:   r.Rejected,
			Changed:    r.Changed,
			DurationMS: r.Duration.Milliseconds(),
		}
		if r.Err != nil {
			res.Error = r.Err.Error()
		}
		out.Providers = append(out.Providers, res)
	}
	return out
}

// problem is an RFC 7807 error body.
type problem struct {
	Type      string              `json:"type"`
	Title     string              `json:"title"`
	Status    int                 `json:"status"`
	Detail    string              `json:"detail"`
	Errors    []domain.FieldError `json:"errors,omitempty"`
	RequestID string              `json:"request_id,omitempty"`
}

var statusByCode = map[domain.Code]int{
	domain.CodeInvalid:      http.StatusBadRequest,
	domain.CodeNotFound:     http.StatusNotFound,
	domain.CodeConflict:     http.StatusConflict,
	domain.CodeUnauthorized: http.StatusUnauthorized,
	domain.CodeRateLimited:  http.StatusTooManyRequests,
}

// writeError renders err as a problem document. Only classified errors show
// their message; anything else is logged and reported as a generic 500, so
// internal details never reach the client.
func writeError(w http.ResponseWriter, r *http.Request, err error) {
	p := problem{Type: "about:blank", RequestID: requestID(r.Context())}

	if e, ok := errors.AsType[*domain.Error](err); ok && statusByCode[e.Code] != 0 {
		p.Status, p.Detail, p.Errors = statusByCode[e.Code], e.Message, e.Fields
	} else {
		logger(r.Context()).ErrorContext(r.Context(), "request failed", "error", err)
		p.Status, p.Detail = http.StatusInternalServerError, "Something went wrong. Quote the request id if you report this."
	}
	p.Title = http.StatusText(p.Status)

	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(p.Status)
	_ = json.NewEncoder(w).Encode(p)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
