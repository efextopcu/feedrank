-- +goose Up

CREATE EXTENSION IF NOT EXISTS unaccent;

-- Folds text for matching, so "egitim" finds "eğitim" and "istanbul" finds
-- "İstanbul". unaccent() has no mapping for the Turkish dotted İ and dotless ı,
-- so those are translated first. Generated columns need IMMUTABLE functions;
-- unaccent() is only STABLE because its dictionary could be edited, which this
-- service never does.
CREATE FUNCTION search_fold(text) RETURNS text
    LANGUAGE sql IMMUTABLE PARALLEL SAFE STRICT
    RETURN lower(public.unaccent('public.unaccent'::regdictionary, translate($1, 'İı', 'ii')));

-- The searchable document: title matches rank above tag matches.
CREATE FUNCTION search_document(title text, tags text[]) RETURNS tsvector
    LANGUAGE sql IMMUTABLE PARALLEL SAFE
    RETURN setweight(to_tsvector('english', search_fold(title)), 'A')
        || setweight(to_tsvector('english', search_fold(array_to_string(tags, ' '))), 'B');

CREATE TABLE contents (
    id               bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    provider         text        NOT NULL,
    external_id      text        NOT NULL,
    title            text        NOT NULL,
    content_type     text        NOT NULL CHECK (content_type IN ('video', 'text')),
    tags             text[]      NOT NULL DEFAULT '{}',
    published_at     timestamptz NOT NULL,

    views            bigint NOT NULL DEFAULT 0 CHECK (views >= 0),
    likes            bigint NOT NULL DEFAULT 0 CHECK (likes >= 0),
    reading_time     bigint NOT NULL DEFAULT 0 CHECK (reading_time >= 0),
    reactions        bigint NOT NULL DEFAULT 0 CHECK (reactions >= 0),

    -- The time-independent score terms, computed at ingestion. Recency depends
    -- on when the query runs, so it is added by the query instead of stored.
    base_score       double precision NOT NULL,
    type_coefficient double precision NOT NULL,
    engagement_score double precision NOT NULL,
    static_score     double precision GENERATED ALWAYS AS (base_score * type_coefficient + engagement_score) STORED,

    search_vector    tsvector GENERATED ALWAYS AS (search_document(title, tags)) STORED,

    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),

    -- Ids are only unique within a provider: both live providers use v1, v2, ...
    UNIQUE (provider, external_id)
);

CREATE INDEX contents_search_vector_idx ON contents USING gin (search_vector);

-- +goose Down
DROP TABLE contents;
DROP FUNCTION search_document(text, text[]);
DROP FUNCTION search_fold(text);
