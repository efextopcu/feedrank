package redisstore

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"feedrank/internal/domain"
)

const generationKey = keyPrefix + "search:generation"

// SearchCache caches pages of search results.
//
// Every key carries a generation number. Invalidate bumps it, which retires
// all cached pages in one step; the old entries are never read again and
// expire on their own. A Redis error is logged and treated as a miss, so
// searches keep working, just uncached, while Redis is down.
type SearchCache struct {
	client *redis.Client
	ttl    time.Duration
	log    *slog.Logger
}

// NewSearchCache returns a cache whose entries live for ttl.
func NewSearchCache(client *redis.Client, ttl time.Duration, log *slog.Logger) *SearchCache {
	return &SearchCache{client: client, ttl: ttl, log: log}
}

// Fetch returns the cached result for key, or calls load and caches what it
// returns. hit reports whether the result came from the cache.
func (c *SearchCache) Fetch(
	ctx context.Context,
	key string,
	load func(context.Context) (domain.SearchResult, error),
) (result domain.SearchResult, hit bool, err error) {
	// The generation is read once and used for both the read and the write.
	// Reading it again before the write could file a result computed before an
	// ingestion under the generation that ingestion just started.
	gen, err := c.client.Get(ctx, generationKey).Int64()
	if err != nil && !errors.Is(err, redis.Nil) {
		c.log.WarnContext(ctx, "search cache unavailable", "error", err)
		result, err = load(ctx)
		return result, false, err
	}
	entryKey := keyPrefix + "search:" + strconv.FormatInt(gen, 10) + ":" + key

	raw, err := c.client.Get(ctx, entryKey).Bytes()
	if err == nil && json.Unmarshal(raw, &result) == nil {
		return result, true, nil
	}
	if err != nil && !errors.Is(err, redis.Nil) {
		c.log.WarnContext(ctx, "search cache read failed", "error", err)
	}

	result, err = load(ctx)
	if err != nil {
		return domain.SearchResult{}, false, err
	}
	if raw, err := json.Marshal(result); err == nil {
		if err := c.client.Set(ctx, entryKey, raw, c.ttl).Err(); err != nil {
			c.log.WarnContext(ctx, "search cache write failed", "error", err)
		}
	}
	return result, false, nil
}

// Invalidate retires every cached page.
func (c *SearchCache) Invalidate(ctx context.Context) error {
	return c.client.Incr(ctx, generationKey).Err()
}
