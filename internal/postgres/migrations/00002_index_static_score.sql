-- +goose Up

-- Popularity order reads its candidates from these, highest stored score
-- first; see Store.Search. On a large table already serving traffic they would
-- be built with CREATE INDEX CONCURRENTLY, outside a transaction.
CREATE INDEX contents_static_score_idx ON contents (static_score DESC);
CREATE INDEX contents_type_static_score_idx ON contents (content_type, static_score DESC);

-- +goose Down
DROP INDEX contents_type_static_score_idx;
DROP INDEX contents_static_score_idx;
