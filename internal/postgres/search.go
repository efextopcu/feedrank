package postgres

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"feedrank/internal/domain"
	"feedrank/internal/scoring"
)

// keywordQuery turns a keyword (%s, a bound parameter) into a tsquery.
//
// websearch_to_tsquery accepts what people type (quotes, OR, -word) and never
// fails on malformed input; the keyword is folded the same way as the stored
// document. Each term of three or more letters then becomes a prefix match,
// so results show up while a word is still being typed: "begin" finds
// "Beginners", whose stem is "beginn". Shorter terms stay exact, or "go"
// would match every word that starts with it.
const keywordQuery = `regexp_replace(
        websearch_to_tsquery('english', search_fold(%s))::text,
        '''([^'']{3,})''', '''\1'':*', 'g')::tsquery`

// Search returns one page of results, scored as of now.
//
// Every value reaches Postgres as a bound parameter. The only SQL built from
// code is the recency CASE, which comes from scoring's own tiers.
//
// Ordering by final score means sorting the matching rows, since recency is
// added per query and an index on the stored score cannot provide the order.
// That is cheap at this size; the README covers what to do as the table grows.
func (s *Store) Search(ctx context.Context, q domain.SearchQuery, now time.Time) (domain.SearchResult, error) {
	result := domain.SearchResult{Page: q.Page, PageSize: q.PageSize}

	var args []any
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}

	from := "contents c"
	where := []string{"TRUE"}
	relevance := "0::float8"
	if q.Keyword != "" {
		from += ", (SELECT " + fmt.Sprintf(keywordQuery, arg(q.Keyword)) + " AS query) AS keyword"
		where = append(where, "c.search_vector @@ query")
		relevance = "ts_rank_cd(c.search_vector, query, 32)::float8"
	}
	if q.Type != "" {
		where = append(where, "c.content_type = "+arg(string(q.Type)))
	}
	filter := from + " WHERE " + strings.Join(where, " AND ")

	if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM "+filter, args...).Scan(&result.Total); err != nil {
		return domain.SearchResult{}, fmt.Errorf("counting matches: %w", err)
	}
	if q.Offset() >= result.Total {
		return result, nil
	}

	recency := recencySQL(now, arg)
	order := "final DESC, c.id"
	if q.Sort == domain.SortRelevance {
		order = "relevance DESC, final DESC, c.id"
	}

	query := `SELECT c.id, c.provider, c.external_id, c.title, c.content_type, c.tags, c.published_at,
        c.views, c.likes, c.reading_time, c.reactions,
        c.base_score, c.type_coefficient, c.engagement_score,
        ` + recency + ` AS recency,
        c.static_score + ` + recency + ` AS final,
        ` + relevance + ` AS relevance
    FROM ` + filter + `
    ORDER BY ` + order + `
    LIMIT ` + arg(q.PageSize) + ` OFFSET ` + arg(q.Offset())

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return domain.SearchResult{}, fmt.Errorf("searching: %w", err)
	}
	result.Items, err = pgx.CollectRows(rows, scanContent)
	if err != nil {
		return domain.SearchResult{}, fmt.Errorf("reading search results: %w", err)
	}
	return result, nil
}

// recencySQL renders scoring's recency tiers as a CASE over published_at.
// "Published within MaxAge of now" is written as a comparison against a bound
// cutoff, which matches scoring.Recency exactly, edges included.
func recencySQL(now time.Time, arg func(any) string) string {
	var b strings.Builder
	b.WriteString("(CASE")
	for _, tier := range scoring.RecencyTiers() {
		fmt.Fprintf(&b, " WHEN c.published_at >= %s THEN %g", arg(now.Add(-tier.MaxAge)), tier.Points)
	}
	b.WriteString(" ELSE 0 END)::float8")
	return b.String()
}

func scanContent(row pgx.CollectableRow) (domain.Content, error) {
	var c domain.Content
	err := row.Scan(
		&c.ID, &c.Provider, &c.ExternalID, &c.Title, &c.Type, &c.Tags, &c.PublishedAt,
		&c.Metrics.Views, &c.Metrics.Likes, &c.Metrics.ReadingTime, &c.Metrics.Reactions,
		&c.Score.Base, &c.Score.TypeCoefficient, &c.Score.Engagement,
		&c.Score.Recency, &c.Score.Final, &c.Relevance,
	)
	return c, err
}
