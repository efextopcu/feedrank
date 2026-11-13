package redisstore

import (
	"context"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// RateLimiter allows each client a fixed number of requests per window. The
// counters live in Redis, so every replica enforces the same budget.
//
// A fixed window is the simplest scheme that works. Its known weakness is
// that a client can send up to twice the limit across a window boundary; a
// sliding window or token bucket would smooth that out if it ever mattered.
type RateLimiter struct {
	client *redis.Client
	limit  int
	window time.Duration
	now    func() time.Time // replaced in tests
}

// NewRateLimiter allows limit requests per window for each key.
func NewRateLimiter(client *redis.Client, limit int, window time.Duration) *RateLimiter {
	return &RateLimiter{client: client, limit: limit, window: window, now: time.Now}
}

// Allow counts one request for key. When the request is over the limit it
// reports how long until the window resets.
func (l *RateLimiter) Allow(ctx context.Context, key string) (allowed bool, retryAfter time.Duration, err error) {
	now := l.now()
	start := now.Truncate(l.window)
	counter := keyPrefix + "ratelimit:" + key + ":" + strconv.FormatInt(start.Unix(), 10)

	// INCR and EXPIRE run in one transaction, so no counter is left without
	// an expiry if the connection drops between them.
	var count *redis.IntCmd
	_, err = l.client.TxPipelined(ctx, func(p redis.Pipeliner) error {
		count = p.Incr(ctx, counter)
		p.Expire(ctx, counter, l.window)
		return nil
	})
	if err != nil {
		return false, 0, err
	}

	if count.Val() > int64(l.limit) {
		return false, start.Add(l.window).Sub(now), nil
	}
	return true, 0, nil
}
