package remotetools

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestConcurrencyMiddlewareLimitsInflight(t *testing.T) {
	server := &Server{slots: make(chan struct{}, 1)}
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	done := make(chan struct{})

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		w.WriteHeader(http.StatusOK)
	})
	handler := server.concurrencyMiddleware(next)

	go func() {
		defer close(done)
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/mcp", nil))
	}()

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first request did not enter the handler")
	}

	rejected := httptest.NewRecorder()
	handler.ServeHTTP(rejected, httptest.NewRequest(http.MethodPost, "/mcp", nil))
	if rejected.Code != http.StatusTooManyRequests {
		t.Fatalf("second request status = %d, want 429", rejected.Code)
	}
	if got := rejected.Header().Get("Retry-After"); got != "1" {
		t.Fatalf("Retry-After = %q, want 1", got)
	}

	close(release)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("first request did not finish")
	}

	after := httptest.NewRecorder()
	handler.ServeHTTP(after, httptest.NewRequest(http.MethodPost, "/mcp", nil))
	if after.Code != http.StatusOK {
		t.Fatalf("request after release status = %d, want 200", after.Code)
	}
}

// A standalone GET SSE stream is session state, not work: it must consume the
// stream budget, not the POST budget that executes tools.
func TestConcurrencyMiddlewareSeparatesStreamsFromWork(t *testing.T) {
	server := &Server{
		slots:       make(chan struct{}, 1),
		streamSlots: make(chan struct{}, 1),
	}
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	done := make(chan struct{})
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			entered <- struct{}{}
			<-release
		}
		w.WriteHeader(http.StatusOK)
	})
	handler := server.concurrencyMiddleware(next)

	go func() {
		defer close(done)
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/mcp", nil))
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("GET did not enter the handler")
	}

	rejected := httptest.NewRecorder()
	handler.ServeHTTP(rejected, httptest.NewRequest(http.MethodGet, "/mcp", nil))
	if rejected.Code != http.StatusTooManyRequests {
		t.Fatalf("second GET status = %d, want 429", rejected.Code)
	}
	if !strings.Contains(rejected.Body.String(), "too many concurrent streams") {
		t.Fatalf("second GET body = %q, want stream-budget message", rejected.Body.String())
	}

	// The GET stream is still open, but POST work must not be blocked by it.
	post := httptest.NewRecorder()
	handler.ServeHTTP(post, httptest.NewRequest(http.MethodPost, "/mcp", nil))
	if post.Code != http.StatusOK {
		t.Fatalf("POST status = %d, want 200 while a GET stream is open", post.Code)
	}

	close(release)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("GET did not finish")
	}
}

// DELETE is the teardown path. Even when every work and stream slot is held,
// it must reach the MCP handler so it can close the session and release them.
func TestConcurrencyMiddlewareDeleteBypassesFullSlots(t *testing.T) {
	server := &Server{
		slots:       make(chan struct{}, 1),
		streamSlots: make(chan struct{}, 1),
	}
	server.slots <- struct{}{}
	server.streamSlots <- struct{}{}

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	handler := server.concurrencyMiddleware(next)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/mcp", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE status = %d, want 204 while both budgets are full", rec.Code)
	}
}
