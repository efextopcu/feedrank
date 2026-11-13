package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"

	"golang.org/x/time/rate"
)

// ClientConfig tunes how one provider is called.
type ClientConfig struct {
	RequestsPerSecond float64       // outbound rate limit
	Timeout           time.Duration // per attempt
	MaxRetries        int           // retries after the first attempt
	BaseDelay         time.Duration // first backoff, doubled on each retry
	MaxDelay          time.Duration // caps the backoff, including Retry-After
	MaxBodyBytes      int64
	FailureThreshold  int           // failed calls in a row that open the breaker
	Cooldown          time.Duration // how long the breaker stays open
}

// DefaultClientConfig suits a provider polled every few minutes. The breaker
// cooldown spans two poll cycles, so a provider that keeps failing is left
// alone for a while instead of being retried on every run.
func DefaultClientConfig() ClientConfig {
	return ClientConfig{
		RequestsPerSecond: 2,
		Timeout:           5 * time.Second,
		MaxRetries:        2,
		BaseDelay:         250 * time.Millisecond,
		MaxDelay:          2 * time.Second,
		MaxBodyBytes:      4 << 20,
		FailureThreshold:  3,
		Cooldown:          10 * time.Minute,
	}
}

// Client calls one provider. Each provider gets its own, so one provider's
// rate limit or open circuit never holds up another.
type Client struct {
	cfg     ClientConfig
	http    *http.Client
	limiter *rate.Limiter
	breaker *Breaker
}

// NewClient builds a Client.
func NewClient(cfg ClientConfig) *Client {
	return &Client{
		cfg:     cfg,
		http:    &http.Client{},
		limiter: rate.NewLimiter(rate.Limit(cfg.RequestsPerSecond), 1),
		breaker: NewBreaker(cfg.FailureThreshold, cfg.Cooldown),
	}
}

// StatusError is a non-2xx response from a provider.
type StatusError struct {
	StatusCode int
	RetryAfter time.Duration
}

func (e *StatusError) Error() string { return "unexpected status " + strconv.Itoa(e.StatusCode) }

var errBodyTooLarge = errors.New("response body exceeds the size limit")

// Get fetches a URL and returns the response body.
//
// Every attempt waits for the rate limiter and has its own timeout. Network
// errors, 429 and 5xx are retried with exponential backoff and jitter; other
// statuses fail at once. The breaker wraps the whole call, retries included:
// a provider that has failed FailureThreshold calls in a row is not called
// again until the cooldown has passed.
func (c *Client) Get(ctx context.Context, url string) ([]byte, error) {
	if err := c.breaker.Allow(); err != nil {
		return nil, err
	}

	body, err := c.getWithRetry(ctx, url)
	switch {
	case err == nil:
		c.breaker.Success()
	case ctx.Err() != nil:
		// We cancelled (shutdown or the run's deadline). That says nothing
		// about the provider's health.
	default:
		c.breaker.Failure()
	}
	return body, err
}

func (c *Client) getWithRetry(ctx context.Context, url string) ([]byte, error) {
	for attempt := 0; ; attempt++ {
		if err := c.limiter.Wait(ctx); err != nil {
			return nil, err
		}

		body, err := c.do(ctx, url)
		if err == nil {
			return body, nil
		}
		if attempt == c.cfg.MaxRetries || !retryable(ctx, err) {
			return nil, err
		}
		if err := sleep(ctx, c.backoff(attempt, err)); err != nil {
			return nil, err
		}
	}
}

func (c *Client) do(ctx context.Context, url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, err
	}
	// Both live endpoints answer text/plain, so the format is never inferred
	// from the response; the header only states our preference.
	req.Header.Set("Accept", "application/json, application/xml;q=0.9, */*;q=0.8")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, &StatusError{
			StatusCode: resp.StatusCode,
			RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
		}
	}

	// Read one byte past the limit to tell a body at the limit from one over it.
	body, err := io.ReadAll(io.LimitReader(resp.Body, c.cfg.MaxBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading body: %w", err)
	}
	if int64(len(body)) > c.cfg.MaxBodyBytes {
		return nil, errBodyTooLarge
	}
	return body, nil
}

// retryable reports whether another attempt could succeed.
func retryable(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false // our own deadline or cancellation
	}
	if se, ok := errors.AsType[*StatusError](err); ok {
		return se.StatusCode == http.StatusTooManyRequests || se.StatusCode >= 500
	}
	// Network errors and per-attempt timeouts are worth another try; an
	// oversized body will be oversized again.
	return !errors.Is(err, errBodyTooLarge)
}

// backoff returns the wait before the next attempt: the provider's Retry-After
// if it sent one, otherwise BaseDelay doubled per attempt with full jitter, so
// callers that failed together do not retry together.
func (c *Client) backoff(attempt int, err error) time.Duration {
	if se, ok := errors.AsType[*StatusError](err); ok && se.RetryAfter > 0 {
		return min(se.RetryAfter, c.cfg.MaxDelay)
	}
	ceiling := min(c.cfg.BaseDelay<<attempt, c.cfg.MaxDelay)
	if ceiling <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(ceiling)) + 1)
}

// parseRetryAfter reads the delay-seconds form of Retry-After. The HTTP-date
// form is rare enough that it falls back to the normal backoff.
func parseRetryAfter(v string) time.Duration {
	secs, err := strconv.Atoi(v)
	if err != nil || secs <= 0 {
		return 0
	}
	return time.Duration(secs) * time.Second
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
