package ratelimit

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAllowWithinLimitAndDeny(t *testing.T) {
	l := New(2, time.Minute)
	base := time.Unix(1_700_000_000, 0)
	l.now = func() time.Time { return base }

	if ok, _ := l.Allow("ip"); !ok {
		t.Fatal("first request must be allowed")
	}
	if ok, _ := l.Allow("ip"); !ok {
		t.Fatal("second request must be allowed")
	}
	ok, retry := l.Allow("ip")
	if ok {
		t.Fatal("third request must be denied")
	}
	if retry <= 0 || retry > time.Minute {
		t.Fatalf("retry-after = %v, want (0, 1m]", retry)
	}
	// A different key is independent.
	if ok, _ := l.Allow("other"); !ok {
		t.Fatal("a different key must have its own budget")
	}
}

func TestWindowSlides(t *testing.T) {
	l := New(1, time.Minute)
	base := time.Unix(1_700_000_000, 0)
	l.now = func() time.Time { return base }
	if ok, _ := l.Allow("ip"); !ok {
		t.Fatal("first must be allowed")
	}
	if ok, _ := l.Allow("ip"); ok {
		t.Fatal("second within window must be denied")
	}
	l.now = func() time.Time { return base.Add(2 * time.Minute) }
	if ok, _ := l.Allow("ip"); !ok {
		t.Fatal("after the window the key must be allowed again")
	}
}

func TestNilAndZeroAllowAll(t *testing.T) {
	var nilLimiter *Limiter
	if ok, _ := nilLimiter.Allow("x"); !ok {
		t.Fatal("nil limiter must allow")
	}
	zero := New(0, time.Minute)
	for i := 0; i < 100; i++ {
		if ok, _ := zero.Allow("x"); !ok {
			t.Fatal("max<=0 must allow all")
		}
	}
}

func TestMiddlewareReturns429(t *testing.T) {
	l := New(1, time.Minute)
	base := time.Unix(1_700_000_000, 0)
	l.now = func() time.Time { return base }
	handler := l.Middleware(ClientIP, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/n/node/web/", nil)
	req.RemoteAddr = "203.0.113.7:5555"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("first request = %d, want 200", rec.Code)
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second request = %d, want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("429 must carry Retry-After")
	}
}

func TestClientIP(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "198.51.100.9:1234"
	if got := ClientIP(req); got != "198.51.100.9" {
		t.Fatalf("ClientIP = %q", got)
	}
	if got := ClientIP(nil); got != "" {
		t.Fatalf("ClientIP(nil) = %q", got)
	}
}
