package redisstore

import (
	"context"
	"crypto/rand"
	"time"

	"github.com/redis/go-redis/v9"
)

// unlockScript deletes the lock only if this holder still owns it. Checking
// and deleting in one script matters: a holder whose lock had expired could
// otherwise delete a lock someone else has taken since.
var unlockScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
	return redis.call("DEL", KEYS[1])
end
return 0`)

// Locker hands out named locks that expire after a TTL, so a holder that
// crashes cannot keep a lock forever.
type Locker struct {
	client *redis.Client
}

// NewLocker returns a Locker.
func NewLocker(client *redis.Client) *Locker { return &Locker{client: client} }

// TryLock takes the named lock if it is free. ok is false when someone else
// holds it. The returned unlock releases it early; if unlock is never called,
// or fails, the lock still expires after ttl.
func (l *Locker) TryLock(ctx context.Context, name string, ttl time.Duration) (unlock func(), ok bool, err error) {
	key := keyPrefix + "lock:" + name
	token := rand.Text() // identifies this holder

	ok, err = l.client.SetNX(ctx, key, token, ttl).Result()
	if err != nil || !ok {
		return nil, false, err
	}

	unlock = func() {
		// Detached from the caller's context, which may already be done.
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = unlockScript.Run(ctx, l.client, []string{key}, token).Err()
	}
	return unlock, true, nil
}
