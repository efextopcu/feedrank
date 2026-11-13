// Package redisstore holds everything the service keeps in Redis: the search
// cache, the ingestion lock and the API rate limiter. All of it can be lost
// without harm, and each caller carries on without Redis when it is down.
package redisstore

import (
	"time"

	"github.com/redis/go-redis/v9"
)

// keyPrefix namespaces every key, so the service can share a Redis instance.
const keyPrefix = "feedrank:"

// NewClient returns a client with short timeouts and no retries. Redis sits
// on the request path, so a slow or missing Redis should turn into a quick
// cache miss, not a slow page.
func NewClient(addr string) *redis.Client {
	return redis.NewClient(&redis.Options{
		Addr:         addr,
		DialTimeout:  250 * time.Millisecond,
		ReadTimeout:  250 * time.Millisecond,
		WriteTimeout: 250 * time.Millisecond,
		MaxRetries:   -1,
	})
}
