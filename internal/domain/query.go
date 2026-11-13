package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Sort orders search results.
type Sort string

// The supported orderings.
const (
	// SortPopularity orders by final score, highest first.
	SortPopularity Sort = "popularity"
	// SortRelevance orders by full-text rank, then by score. It needs a keyword.
	SortRelevance Sort = "relevance"
)

// Search limits.
const (
	DefaultPageSize = 20
	MaxPageSize     = 100
	// MaxPage bounds the OFFSET, which Postgres pays for by reading and
	// discarding every skipped row.
	MaxPage          = 500
	maxKeywordLength = 200
)

// SearchQuery is a content search request.
type SearchQuery struct {
	Keyword  string
	Type     ContentType // empty matches every type
	Sort     Sort
	Page     int // 1-based
	PageSize int
}

// Normalize trims the input and fills in defaults. A keyword search defaults
// to relevance order; listing everything defaults to popularity.
func (q *SearchQuery) Normalize() {
	q.Keyword = strings.TrimSpace(q.Keyword)
	q.Type = ContentType(strings.ToLower(strings.TrimSpace(string(q.Type))))
	q.Sort = Sort(strings.ToLower(strings.TrimSpace(string(q.Sort))))

	if q.Sort == "" {
		q.Sort = SortPopularity
		if q.Keyword != "" {
			q.Sort = SortRelevance
		}
	}
	if q.Page == 0 {
		q.Page = 1
	}
	if q.PageSize == 0 {
		q.PageSize = DefaultPageSize
	}
}

// Validate reports every problem with the query in one error.
func (q SearchQuery) Validate() error {
	err := Errorf(CodeInvalid, "invalid search query")

	if utf8.RuneCountInString(q.Keyword) > maxKeywordLength {
		err.AddField("q", fmt.Sprintf("must be at most %d characters", maxKeywordLength))
	}
	if q.Type != "" && !q.Type.Valid() {
		err.AddField("type", "must be video or text")
	}
	switch q.Sort {
	case SortPopularity:
	case SortRelevance:
		if q.Keyword == "" {
			err.AddField("sort", "relevance needs a search keyword (q)")
		}
	default:
		err.AddField("sort", "must be popularity or relevance")
	}
	if q.Page < 1 || q.Page > MaxPage {
		err.AddField("page", fmt.Sprintf("must be between 1 and %d", MaxPage))
	}
	if q.PageSize < 1 || q.PageSize > MaxPageSize {
		err.AddField("page_size", fmt.Sprintf("must be between 1 and %d", MaxPageSize))
	}

	if len(err.Fields) > 0 {
		return err
	}
	return nil
}

// Offset is the number of results before this page.
func (q SearchQuery) Offset() int { return (q.Page - 1) * q.PageSize }

// CacheKey identifies the query's result. Matching is case-insensitive, so
// the keyword is lowercased and "Go" and "go" share an entry. Call it after
// Normalize.
func (q SearchQuery) CacheKey() string {
	raw := fmt.Sprintf("%q|%s|%s|%d|%d", strings.ToLower(q.Keyword), q.Type, q.Sort, q.Page, q.PageSize)
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:16])
}

// SearchResult is one page of search results.
type SearchResult struct {
	Items    []Content
	Total    int // matches across all pages
	Page     int
	PageSize int
}

// TotalPages is the number of pages Total spans.
func (r SearchResult) TotalPages() int {
	if r.PageSize <= 0 {
		return 0
	}
	return (r.Total + r.PageSize - 1) / r.PageSize
}
