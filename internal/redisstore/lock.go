package redisstore

import (
	"context"
	"crypto/rand"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

// ErrLockLost is the cause given to a held lock's context when a renewal
// finds that the lock now belongs to someone else.
var ErrLockLost = errors.New("the lock was lost: it expired and another holder took it")

// unlockScript deletes the lock and renewScript extends it, each only if this
// holder still owns it. Checking and acting in one script matters: a holder
// whose lock had expired could otherwise delete or extend a lock someone else
// has taken since.
var (
	unlockScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
	return redis.call("DEL", KEYS[1])
end
return 0`)

	renewScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
	return redis.call("PEXPIRE", KEYS[1], ARGV[2])
end
return 0`)
)

// Locker hands out named locks that expire unless renewed, so a holder that
// crashes cannot keep a lock for longer than one TTL.
type Locker struct {
	client *redis.Client
	ttl    time.Duration
}

// NewLocker returns a Locker whose locks expire ttl after their last renewal.
func NewLocker(client *redis.Client, ttl time.Duration) *Locker {
	return &Locker{client: client, ttl: ttl}
}

// TryLock takes the named lock if it is free; ok is false when someone else
// holds it.
//
// While held, the lock is renewed every third of its TTL, so a run that takes
// hours keeps it. If a renewal finds the lock gone (Redis was unreachable for
// longer than the TTL and another instance took over), the returned context is
// cancelled with ErrLockLost as its cause, so the work stops instead of
// overlapping the new holder's. A renewal that merely fails to reach Redis is
// not treated as a loss.
//
// release stops the renewals and deletes the lock. It must be called.
func (l *Locker) TryLock(ctx context.Context, name string) (held context.Context, release func(), ok bool, err error) {
	key := keyPrefix + "lock:" + name
	token := rand.Text() // identifies this holder

	ok, err = l.client.SetNX(ctx, key, token, l.ttl).Result()
	if err != nil || !ok {
		return nil, nil, false, err
	}

	held, cancel := context.WithCancelCause(ctx)
	renewed := make(chan struct{})
	go func() {
		defer close(renewed)
		ticker := time.NewTicker(l.ttl / 3)
		defer ticker.Stop()
		for {
			select {
			case <-held.Done():
				return
			case <-ticker.C:
				n, err := renewScript.Run(held, l.client, []string{key}, token, l.ttl.Milliseconds()).Int()
				if err == nil && n == 0 {
					cancel(ErrLockLost)
					return
				}
			}
		}
	}()

	release = func() {
		cancel(nil)
		<-renewed
		// Detached from the caller's context, which may already be done.
		ctx, stop := context.WithTimeout(context.Background(), 2*time.Second)
		defer stop()
		_ = unlockScript.Run(ctx, l.client, []string{key}, token).Err()
	}
	return held, release, true, nil
}
