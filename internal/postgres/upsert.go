package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"feedrank/internal/domain"
)

// upsertSQL inserts an item or updates the stored copy. The WHERE clause skips
// rows whose values have not changed, so a poll that brings nothing new writes
// nothing and updated_at keeps its meaning. The scores are compared as well as
// the metrics, so a change to the formula rewrites every row on the next run.
const upsertSQL = `
INSERT INTO contents (
    provider, external_id, title, content_type, tags, published_at,
    views, likes, reading_time, reactions,
    base_score, type_coefficient, engagement_score
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
ON CONFLICT (provider, external_id) DO UPDATE SET
    title            = EXCLUDED.title,
    content_type     = EXCLUDED.content_type,
    tags             = EXCLUDED.tags,
    published_at     = EXCLUDED.published_at,
    views            = EXCLUDED.views,
    likes            = EXCLUDED.likes,
    reading_time     = EXCLUDED.reading_time,
    reactions        = EXCLUDED.reactions,
    base_score       = EXCLUDED.base_score,
    type_coefficient = EXCLUDED.type_coefficient,
    engagement_score = EXCLUDED.engagement_score,
    updated_at       = now()
WHERE (contents.title, contents.content_type, contents.tags, contents.published_at,
       contents.views, contents.likes, contents.reading_time, contents.reactions,
       contents.base_score, contents.type_coefficient, contents.engagement_score)
  IS DISTINCT FROM
      (EXCLUDED.title, EXCLUDED.content_type, EXCLUDED.tags, EXCLUDED.published_at,
       EXCLUDED.views, EXCLUDED.likes, EXCLUDED.reading_time, EXCLUDED.reactions,
       EXCLUDED.base_score, EXCLUDED.type_coefficient, EXCLUDED.engagement_score)`

// Upsert writes items in a single transaction, so a provider's batch is stored
// completely or not at all. It returns how many rows were inserted or changed.
// Items must already be scored.
func (s *Store) Upsert(ctx context.Context, items []domain.Content) (int, error) {
	if len(items) == 0 {
		return 0, nil
	}

	var changed int
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		batch := &pgx.Batch{}
		for _, c := range items {
			batch.Queue(upsertSQL,
				c.Provider, c.ExternalID, c.Title, string(c.Type), c.Tags, c.PublishedAt,
				c.Metrics.Views, c.Metrics.Likes, c.Metrics.ReadingTime, c.Metrics.Reactions,
				c.Score.Base, c.Score.TypeCoefficient, c.Score.Engagement,
			).Exec(func(tag pgconn.CommandTag) error {
				changed += int(tag.RowsAffected())
				return nil
			})
		}
		// One round trip for the whole batch.
		return tx.SendBatch(ctx, batch).Close()
	})
	if err != nil {
		return 0, fmt.Errorf("upserting %d items: %w", len(items), err)
	}
	return changed, nil
}
