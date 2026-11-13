package provider

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// statusServer answers with the given statuses in order, repeating the last.
func statusServer(t *testing.T, statuses ...int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := int(calls.Add(1))
		status := statuses[min(n, len(statuses))-1]
		w.WriteHeader(status)
		_, _ = w.Write([]byte("body"))
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func TestClientRetriesTransientFailures(t *testing.T) {
	srv, calls := statusServer(t, 503, 429, 200)
	body, err := NewClient(testClientConfig()).Get(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "body" || calls.Load() != 3 {
		t.Errorf("body %q after %d calls, want \"body\" after 3", body, calls.Load())
	}
}

func TestClientDoesNotRetryClientErrors(t *testing.T) {
	srv, calls := statusServer(t, 404)
	_, err := NewClient(testClientConfig()).Get(context.Background(), srv.URL)

	if se, ok := errors.AsType[*StatusError](err); !ok || se.StatusCode != http.StatusNotFound {
		t.Fatalf("err = %v, want a 404 StatusError", err)
	}
	if calls.Load() != 1 {
		t.Errorf("made %d calls, want 1", calls.Load())
	}
}

func TestClientGivesUpAfterMaxRetries(t *testing.T) {
	srv, calls := statusServer(t, 500)
	cfg := testClientConfig()
	cfg.MaxRetries = 2

	if _, err := NewClient(cfg).Get(context.Background(), srv.URL); err == nil {
		t.Fatal("expected an error")
	}
	if calls.Load() != 3 {
		t.Errorf("made %d calls, want 1 attempt + 2 retries", calls.Load())
	}
}

func TestClientRejectsOversizedBodies(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 11)))
	}))
	defer srv.Close()

	cfg := testClientConfig()
	cfg.MaxBodyBytes = 10
	if _, err := NewClient(cfg).Get(context.Background(), srv.URL); !errors.Is(err, errBodyTooLarge) {
		t.Errorf("err = %v, want errBodyTooLarge", err)
	}
}

func TestClientOpensTheCircuitAfterRepeatedFailures(t *testing.T) {
	srv, calls := statusServer(t, 500)
	cfg := testClientConfig()
	cfg.MaxRetries = 0
	cfg.FailureThreshold = 2
	client := NewClient(cfg)

	for range 2 {
		if _, err := client.Get(context.Background(), srv.URL); errors.Is(err, ErrCircuitOpen) {
			t.Fatal("circuit opened before the threshold")
		}
	}
	if _, err := client.Get(context.Background(), srv.URL); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("err = %v, want ErrCircuitOpen", err)
	}
	if calls.Load() != 2 {
		t.Errorf("provider called %d times, want 2: an open circuit must not call it", calls.Load())
	}
}

func TestCancellationDoesNotCountAgainstTheProvider(t *testing.T) {
	srv, _ := statusServer(t, 200)
	cfg := testClientConfig()
	cfg.FailureThreshold = 1
	client := NewClient(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Get(ctx, srv.URL); err == nil {
		t.Fatal("expected an error from a cancelled context")
	}
	if client.breaker.State() != StateClosed {
		t.Errorf("breaker is %s, want closed", client.breaker.State())
	}
}

func TestBackoff(t *testing.T) {
	c := NewClient(ClientConfig{BaseDelay: 100 * time.Millisecond, MaxDelay: time.Second})

	t.Run("Retry-After is honoured up to MaxDelay", func(t *testing.T) {
		if got := c.backoff(0, &StatusError{StatusCode: 429, RetryAfter: 500 * time.Millisecond}); got != 500*time.Millisecond {
			t.Errorf("backoff = %v, want 500ms", got)
		}
		if got := c.backoff(0, &StatusError{StatusCode: 429, RetryAfter: time.Minute}); got != time.Second {
			t.Errorf("backoff = %v, want MaxDelay", got)
		}
	})

	t.Run("exponential with full jitter", func(t *testing.T) {
		for attempt, ceiling := range []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond, time.Second, time.Second} {
			for range 50 {
				if got := c.backoff(attempt, errors.New("boom")); got <= 0 || got > ceiling {
					t.Fatalf("attempt %d: backoff %v outside (0, %v]", attempt, got, ceiling)
				}
			}
		}
	})
}
