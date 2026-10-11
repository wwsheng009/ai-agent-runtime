// Package ratelimit implements a small in-memory per-key sliding-window limiter
// and HTTP middleware. It guards the gateway's public entry points (the tunnel
// upgrade and the reverse proxy) against request floods.
//
// Design: docs/gateway/09-edge-hardening.md §5.
package ratelimit

import (
	"math"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Limiter is a per-key sliding-window limiter. A nil limiter or a non-positive
// Max allows everything.
type Limiter struct {
	mu     sync.Mutex
	window time.Duration
	max    int
	now    func() time.Time
	hits   map[string][]time.Time
}

// New creates a limiter allowing max requests per window per key.
func New(max int, window time.Duration) *Limiter {
	if window <= 0 {
		window = time.Minute
	}
	return &Limiter{
		window: window,
		max:    max,
		now:    time.Now,
		hits:   make(map[string][]time.Time),
	}
}

// Allow records a request for key and reports whether it is within the limit.
// When denied it returns the time to wait before retrying.
func (l *Limiter) Allow(key string) (bool, time.Duration) {
	if l == nil || l.max <= 0 {
		return true, 0
	}
	now := l.now()
	cutoff := now.Add(-l.window)

	l.mu.Lock()
	defer l.mu.Unlock()
	kept := l.hits[key][:0]
	for _, t := range l.hits[key] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= l.max {
		retry := l.window - now.Sub(kept[0])
		if retry < 0 {
			retry = 0
		}
		l.hits[key] = kept
		return false, retry
	}
	l.hits[key] = append(kept, now)
	return true, 0
}

// Middleware wraps next with rate limiting keyed by keyFn. Denied requests get
// 429 with Retry-After.
func (l *Limiter) Middleware(keyFn func(*http.Request) string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := ""
		if keyFn != nil {
			key = keyFn(r)
		}
		if ok, retry := l.Allow(key); !ok {
			w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(retry.Seconds()))))
			http.Error(w, "too many requests", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ClientIP returns the request's client IP (host part of RemoteAddr). It does
// not trust forwarded headers; a trusted-proxy resolver can wrap it later.
func ClientIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
