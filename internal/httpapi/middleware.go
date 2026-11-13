package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"log/slog"
	"math"
	"net"
	"net/http"
	"regexp"
	"runtime/debug"
	"strconv"
	"time"

	"feedrank/internal/domain"
)

type ctxKey int

const (
	requestIDKey ctxKey = iota
	loggerKey
)

// validRequestID guards an incoming X-Request-Id before it is echoed and
// logged, so a client cannot inject arbitrary text into the logs.
var validRequestID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// withRequestID gives every request an id, reusing a sane incoming
// X-Request-Id, and attaches a logger that includes it.
func withRequestID(base *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if !validRequestID.MatchString(id) {
			id = rand.Text()[:16]
		}
		w.Header().Set("X-Request-Id", id)

		ctx := context.WithValue(r.Context(), requestIDKey, id)
		ctx = context.WithValue(ctx, loggerKey, base.With("request_id", id))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func requestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

func logger(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(loggerKey).(*slog.Logger); ok {
		return l
	}
	return slog.Default()
}

// securityHeaders sets the headers browsers use to limit what a page can do:
// no MIME sniffing, no framing (clickjacking), and no referrer sent to other
// sites. A Content-Security-Policy is left out because /docs loads Swagger UI
// from a CDN; the dashboard itself would be fine with one.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

// recoverPanics turns a panic into a logged 500 instead of a dropped
// connection.
func recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler { //nolint:errorlint // sentinel panic value, not a wrapped error
					panic(v)
				}
				logger(r.Context()).ErrorContext(r.Context(), "panic", "value", v, "stack", string(debug.Stack()))
				writeError(w, r, domain.Errorf(domain.CodeInternal, "panic"))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// statusRecorder remembers the status code for the access log.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.ResponseController reach the real writer.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		level := slog.LevelInfo
		if rec.status >= 500 {
			level = slog.LevelError
		}
		logger(r.Context()).Log(r.Context(), level, "request",
			"method", r.Method, "path", r.URL.Path, "query", r.URL.RawQuery,
			"status", rec.status, "duration_ms", time.Since(start).Milliseconds())
	})
}

// rateLimit applies the per-client budget. If the limiter itself fails, the
// request is let through: losing rate limiting is better than losing the API.
func rateLimit(limiter RateLimiter, next http.Handler) http.Handler {
	if limiter == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		allowed, retryAfter, err := limiter.Allow(r.Context(), clientIP(r))
		if err != nil {
			logger(r.Context()).WarnContext(r.Context(), "rate limiter unavailable, allowing request", "error", err)
			next.ServeHTTP(w, r)
			return
		}
		if !allowed {
			// Rounded up: a client that waits exactly this long must find the
			// window reset.
			seconds := max(1, int(math.Ceil(retryAfter.Seconds())))
			w.Header().Set("Retry-After", strconv.Itoa(seconds))
			writeError(w, r, domain.Errorf(domain.CodeRateLimited, "too many requests, try again in %ds", seconds))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// clientIP is the address the request came from. X-Forwarded-For is ignored
// on purpose: without a trusted proxy in front, any client could set it and
// pick a fresh budget for every request.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// requireAPIKey guards admin endpoints with a shared key in X-API-Key.
func requireAPIKey(key string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-API-Key")), []byte(key)) != 1 {
			writeError(w, r, domain.Errorf(domain.CodeUnauthorized, "a valid X-API-Key header is required"))
			return
		}
		next.ServeHTTP(w, r)
	})
}
