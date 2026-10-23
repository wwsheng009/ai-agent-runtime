package transport

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestParseRetryAfterBoundedAndLenient pins the header parser: only the
// delay-seconds form is honoured, garbage never waits, and a hostile hint is
// clamped instead of stalling the client.
func TestParseRetryAfterBoundedAndLenient(t *testing.T) {
	cases := []struct {
		value string
		want  time.Duration
	}{
		{"", 0},
		{"  ", 0},
		{"1", time.Second},
		{"0.25", 250 * time.Millisecond},
		{"3", 3 * time.Second},
		{"5", 5 * time.Second},
		{"3600", maxRateLimitWait},   // clamped, not obeyed
		{"-1", 0},                    // negative: no wait
		{"0", 0},                     // zero: no wait
		{"abc", 0},                   // malformed
		{"Wed, 21 Oct 2026 07:28:00 GMT", 0}, // HTTP-date: not guessed at
	}
	for _, tc := range cases {
		if got := parseRetryAfter(tc.value); got != tc.want {
			t.Errorf("parseRetryAfter(%q) = %v, want %v", tc.value, got, tc.want)
		}
	}
}

// TestRateLimitRoundTripperPreservesResponse locks the non-destructive contract:
// a 429 must still arrive as an HTTP response, because the MCP SDK only keeps the
// session alive for a *response* carrying a transient status. Turning it into an
// error would tear the session down on a single refusal.
func TestRateLimitRoundTripperPreservesResponse(t *testing.T) {
	var got string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	rt := rateLimitRoundTripper{base: http.DefaultTransport}
	req, err := http.NewRequest(http.MethodPost, server.URL, strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip must not convert 429 into an error: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 preserved", resp.StatusCode)
	}
	if got = resp.Header.Get("Retry-After"); got != "0" {
		t.Fatalf("Retry-After = %q, want the header preserved for diagnostics", got)
	}
}

// TestRateLimitRoundTripperWaitsThenReturns proves the part the SDK cannot do:
// it discards Retry-After, so the only place to honor the hint is here.
func TestRateLimitRoundTripperWaitsThenReturns(t *testing.T) {
	const hint = "0.35"
	var served atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		served.Add(1)
		w.Header().Set("Retry-After", hint)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	rt := rateLimitRoundTripper{base: http.DefaultTransport}
	req, err := http.NewRequest(http.MethodPost, server.URL, strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	started := time.Now()
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	elapsed := time.Since(started)
	if elapsed < 300*time.Millisecond {
		t.Fatalf("elapsed = %v, want at least the Retry-After hint", elapsed)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("elapsed = %v, Retry-After must be clamped to %v", elapsed, maxRateLimitWait)
	}
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", resp.StatusCode)
	}
}

// TestRateLimitRoundTripperContextCancel pins that a refused wait cannot outlive
// the caller: cancel must return immediately, not block on the server's schedule.
func TestRateLimitRoundTripperContextCancel(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", fmt.Sprint(int(maxRateLimitWait.Seconds())))
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	go func() {
		time.Sleep(120 * time.Millisecond)
		cancel()
	}()

	rt := rateLimitRoundTripper{base: http.DefaultTransport}
	started := time.Now()
	resp, err := rt.RoundTrip(req)
	elapsed := time.Since(started)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err == nil {
		t.Fatal("cancelled wait must return an error")
	}
	if elapsed > 3*time.Second {
		t.Fatalf("elapsed = %v, cancel must abort the wait early", elapsed)
	}
}

// TestRateLimitRoundTripperLeavesOtherStatusesAlone proves the wrapper is
// scoped to 429: OAuth replays on 401/403 and normal traffic must be untouched.
func TestRateLimitRoundTripperLeavesOtherStatusesAlone(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Retry-After", "3")
			w.WriteHeader(status)
			_, _ = io.WriteString(w, "body")
		}))

		req, err := http.NewRequest(http.MethodPost, server.URL, strings.NewReader(`{}`))
		if err != nil {
			server.Close()
			t.Fatalf("build request: %v", err)
		}
		rt := rateLimitRoundTripper{base: http.DefaultTransport}
		started := time.Now()
		resp, err := rt.RoundTrip(req)
		elapsed := time.Since(started)
		if err != nil {
			server.Close()
			t.Fatalf("status %d: RoundTrip: %v", status, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		server.Close()

		if resp.StatusCode != status {
			t.Fatalf("status = %d, want %d", resp.StatusCode, status)
		}
		if string(body) != "body" {
			t.Fatalf("status %d: body = %q, want the payload untouched", status, body)
		}
		if elapsed > time.Second {
			t.Fatalf("status %d: waited %v, only 429 may wait", status, elapsed)
		}
	}
}
