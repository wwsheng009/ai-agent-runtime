package hooks

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A host may inject its own http.Client (for proxies, tracing, tests). The hook
// timeout used to be applied only when the executor built the client itself, so
// an injected client silently disabled the per-handler deadline and a slow
// endpoint could stall a turn indefinitely.
func TestHTTPExecutorHonorsTimeoutWithInjectedClient(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer func() {
		close(release)
		server.Close()
	}()

	executor := &HTTPExecutor{Client: &http.Client{}} // no client-level timeout
	hook := HookConfig{
		ID:      "slow",
		Event:   EventPostToolUse,
		Timeout: 150 * time.Millisecond,
		Exec:    ExecConfig{Type: "http", URL: server.URL},
	}

	start := time.Now()
	_, err := executor.Execute(context.Background(), hook, map[string]interface{}{})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("hook ignored its timeout: took %v", elapsed)
	}
	if elapsed < 100*time.Millisecond {
		t.Fatalf("returned too fast to have applied the 150ms deadline: %v", elapsed)
	}
}
