package providerops

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

const probeContextSuccessResponse = `{"choices":[{}],"content":[],"output":[],"candidates":[]}`

func assertProbeContextMatrixShape(t *testing.T, matrix []ModelProbeMatrix, models, protocols []string) {
	t.Helper()
	if len(matrix) != len(models) {
		t.Fatalf("got %d models, want %d: %+v", len(matrix), len(models), matrix)
	}
	for i, entry := range matrix {
		if entry.Model != models[i] {
			t.Fatalf("model[%d] = %q, want %q", i, entry.Model, models[i])
		}
		if len(entry.Probes) != len(protocols) {
			t.Fatalf("model %q has %d probes, want %d", entry.Model, len(entry.Probes), len(protocols))
		}
		for j, probe := range entry.Probes {
			if probe.Protocol != protocols[j] {
				t.Fatalf("model %q probe[%d] protocol = %q, want %q", entry.Model, j, probe.Protocol, protocols[j])
			}
		}
	}
}

func assertProbeContextUnknown(t *testing.T, probe ModelSingleProbe, reason error) {
	t.Helper()
	if probe.Verdict != string(probeVerdictUnknown) || !strings.Contains(probe.Detail, reason.Error()) {
		t.Fatalf("want unknown with %q detail, got %+v", reason, probe)
	}
}

func waitProbeContextSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func waitProbeContextMatrix(t *testing.T, result <-chan []ModelProbeMatrix) []ModelProbeMatrix {
	t.Helper()
	select {
	case matrix := <-result:
		return matrix
	case <-time.After(5 * time.Second):
		t.Fatal("ProbeMatrix did not return promptly after cancellation/timeout")
		return nil
	}
}

func TestProbeMatrixPreCanceledContextDoesNotSendHTTP(t *testing.T) {
	for _, reason := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(reason.Error(), func(t *testing.T) {
			var requests int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				atomic.AddInt64(&requests, 1)
				_, _ = io.WriteString(w, probeContextSuccessResponse)
			}))
			defer server.Close()

			var ctx context.Context
			var cancel context.CancelFunc
			if reason == context.Canceled {
				ctx, cancel = context.WithCancel(context.Background())
				cancel()
			} else {
				ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			}
			defer cancel()
			matrix := ProbeMatrix(ProbeMatrixRequest{
				Context:   ctx,
				Provider:  config.Provider{Protocol: "openai", BaseURL: server.URL},
				Models:    []string{" model-b ", "", "model-a"},
				Protocols: []string{"anthropic", "openai", "openai"},
			})
			assertProbeContextMatrixShape(t, matrix, []string{"model-b", "model-a"}, []string{"anthropic", "openai"})
			for _, entry := range matrix {
				for _, probe := range entry.Probes {
					assertProbeContextUnknown(t, probe, reason)
					if probe.HTTPStatus != 0 {
						t.Fatalf("unstarted probe has an HTTP status: %+v", probe)
					}
				}
			}
			if got := atomic.LoadInt64(&requests); got != 0 {
				t.Fatalf("pre-canceled matrix sent %d HTTP requests", got)
			}
		})
	}
}

func TestProbeMatrixCancelsInFlightRequests(t *testing.T) {
	tests := []struct {
		name           string
		cancel         bool
		flushBody      bool
		parentTimeout  time.Duration
		requestTimeout time.Duration
	}{
		{name: "cancel_before_headers", cancel: true, requestTimeout: time.Minute},
		{name: "cancel_reading_body", cancel: true, flushBody: true, requestTimeout: time.Minute},
		{name: "parent_deadline", parentTimeout: time.Second, requestTimeout: time.Minute},
		{name: "request_timeout", flushBody: true, requestTimeout: time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			started := make(chan struct{}, 1)
			canceled := make(chan struct{}, 1)
			stopHandler := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Drain the request so the server can observe the client disconnect.
				_, _ = io.Copy(io.Discard, r.Body)
				if tt.flushBody {
					_, _ = io.WriteString(w, probeContextSuccessResponse)
					w.(http.Flusher).Flush()
				}
				started <- struct{}{}
				select {
				case <-r.Context().Done():
					canceled <- struct{}{}
				case <-stopHandler:
				}
			}))
			t.Cleanup(func() {
				close(stopHandler)
				server.Close()
			})
			var ctx context.Context
			var cancel context.CancelFunc
			if tt.parentTimeout > 0 {
				ctx, cancel = context.WithTimeout(context.Background(), tt.parentTimeout)
			} else {
				ctx, cancel = context.WithCancel(context.Background())
			}
			t.Cleanup(cancel)

			result := make(chan []ModelProbeMatrix, 1)
			go func() {
				result <- ProbeMatrix(ProbeMatrixRequest{
					Context:   ctx,
					Provider:  config.Provider{Protocol: "openai", BaseURL: server.URL},
					Models:    []string{"model-a"},
					Protocols: []string{"openai"},
					Timeout:   tt.requestTimeout,
				})
			}()
			waitProbeContextSignal(t, started, "HTTP request to start")
			reason := context.DeadlineExceeded
			if tt.cancel {
				cancel()
				reason = context.Canceled
			}
			matrix := waitProbeContextMatrix(t, result)
			assertProbeContextMatrixShape(t, matrix, []string{"model-a"}, []string{"openai"})
			assertProbeContextUnknown(t, matrix[0].Probes[0], reason)
			waitProbeContextSignal(t, canceled, "server request context to be canceled")
		})
	}
}

func TestProbeMatrixCancellationPreservesQueuedEntries(t *testing.T) {
	var requests int64
	started := make(chan struct{}, 1)
	stopHandler := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if atomic.AddInt64(&requests, 1) != 2 {
			_, _ = io.WriteString(w, probeContextSuccessResponse)
			return
		}
		started <- struct{}{}
		select {
		case <-r.Context().Done():
		case <-stopHandler:
		}
	}))
	t.Cleanup(func() {
		close(stopHandler)
		server.Close()
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	models := []string{"model-z", "model-a", "model-c", "model-b"}
	protocols := []string{"openai", "anthropic", "codex", "gemini"}
	result := make(chan []ModelProbeMatrix, 1)
	go func() {
		result <- ProbeMatrix(ProbeMatrixRequest{
			Context:       ctx,
			Provider:      config.Provider{Protocol: "openai", BaseURL: server.URL},
			Models:        models,
			Protocols:     protocols,
			Timeout:       time.Minute,
			MaxConcurrent: 1,
		})
	}()
	// One result has completed, one request is in flight, and the rest must
	// remain in the returned matrix even though they never reach the server.
	waitProbeContextSignal(t, started, "second HTTP request to start")
	cancel()
	matrix := waitProbeContextMatrix(t, result)
	assertProbeContextMatrixShape(t, matrix, models, protocols)
	if probe := matrix[0].Probes[0]; probe.Verdict != string(probeVerdictSupported) || probe.HTTPStatus != http.StatusOK {
		t.Fatalf("cancellation overwrote the completed probe: %+v", probe)
	}
	for i, entry := range matrix {
		for j, probe := range entry.Probes {
			if i == 0 && j == 0 {
				continue
			}
			assertProbeContextUnknown(t, probe, context.Canceled)
			if probe.HTTPStatus != 0 {
				t.Fatalf("canceled/unstarted probe has an HTTP status: %+v", probe)
			}
		}
	}
	if got := atomic.LoadInt64(&requests); got != 2 {
		t.Fatalf("got %d HTTP requests, want only the completed and in-flight requests", got)
	}
}

func TestProbeMatrixNilContextPreservesExistingBehavior(t *testing.T) {
	var requests int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&requests, 1)
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = io.WriteString(w, probeContextSuccessResponse)
	}))
	defer server.Close()

	matrix := ProbeMatrix(ProbeMatrixRequest{
		Provider:  config.Provider{Protocol: "openai", BaseURL: server.URL},
		Models:    []string{" model-z ", "", "model-a", "model-z"},
		Protocols: []string{"codex-apikey", "", "OPENAI", "codex-oauth", "anthropic", "gemini"},
	})
	assertProbeContextMatrixShape(t, matrix, []string{"model-z", "model-a", "model-z"}, []string{"codex", "openai", "anthropic", "gemini"})
	for _, entry := range matrix {
		for _, probe := range entry.Probes {
			if probe.Verdict != string(probeVerdictSupported) || probe.HTTPStatus != http.StatusOK || probe.Detail == "" {
				t.Fatalf("nil-context probe failed: %+v", probe)
			}
		}
	}
	if got := atomic.LoadInt64(&requests); got != 12 {
		t.Fatalf("got %d HTTP requests, want 12 (model duplicates preserved, protocols normalized/deduplicated)", got)
	}
}

func TestProbeMatrixPinsOneKeyForTheWholeMatrix(t *testing.T) {
	var (
		mu   sync.Mutex
		keys = make(map[string]struct{})
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimSpace(r.Header.Get("Authorization"))
		if key != "" {
			key = strings.TrimSpace(strings.TrimPrefix(key, "Bearer "))
		}
		if key == "" {
			key = strings.TrimSpace(r.Header.Get("x-api-key"))
		}
		if key == "" {
			key = strings.TrimSpace(r.Header.Get("x-goog-api-key"))
		}
		mu.Lock()
		keys[key] = struct{}{}
		mu.Unlock()
		_, _ = io.WriteString(w, probeContextSuccessResponse)
	}))
	defer server.Close()

	models := make([]string, 32)
	for i := range models {
		models[i] = "model-" + string(rune('a'+i))
	}
	matrix := ProbeMatrix(ProbeMatrixRequest{
		Provider:  config.Provider{Protocol: "openai", BaseURL: server.URL, APIKeys: []string{"key-a", "key-b"}},
		Models:    models,
		Protocols: []string{"openai", "codex", "anthropic", "gemini"},
	})
	if len(matrix) != len(models) {
		t.Fatalf("got %d models, want %d", len(matrix), len(models))
	}
	mu.Lock()
	defer mu.Unlock()
	if len(keys) != 1 {
		t.Fatalf("probe matrix used %d distinct API keys; want exactly one: %v", len(keys), keys)
	}
	for key := range keys {
		if key != "key-a" && key != "key-b" {
			t.Fatalf("probe matrix used unexpected API key %q", key)
		}
	}
}
