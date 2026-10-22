package remotetools

import (
	"net/http"
	"net/http/httptest"
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
