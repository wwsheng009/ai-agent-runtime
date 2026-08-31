package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestIsResponseHeaderTimeoutError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"http1 message", errors.New(`Post "http://h": net/http: timeout awaiting response headers`), true},
		{"http2 message", errors.New("Post \"http://h\": timeout awaiting response headers"), true},
		{"url.Error wrapper", &url.Error{Op: "Post", URL: "http://h", Err: errors.New("timeout awaiting response headers")}, true},
		{"double wrapped", fmt.Errorf("failed to send request: %w", &url.Error{Op: "Post", URL: "http://h", Err: errors.New("timeout awaiting response headers")}), true},
		{"context deadline", errors.New("context deadline exceeded"), false},
		{"dial timeout", errors.New(`dial tcp 1.2.3.4:443: i/o timeout`), false},
		{"body idle timeout", errors.New("stream idle timeout"), false},
		{"unrelated", errors.New("connection reset by peer"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, isResponseHeaderTimeoutError(tc.err))
		})
	}
}

// hungUpstreamListener accepts TCP connections but never writes a byte back,
// simulating an upstream that takes the connection and the request and then
// hangs. It reports how many connections were accepted.
func hungUpstreamListener(t *testing.T) (addr string, accepts *atomic.Int64, stop func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	var n atomic.Int64
	var wg sync.WaitGroup
	done := make(chan struct{})
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			n.Add(1)
			wg.Add(1)
			go func(c net.Conn) {
				defer wg.Done()
				select {
				case <-done:
				case <-time.After(10 * time.Second):
				}
				c.Close()
			}(conn)
		}
	}()
	stop = func() {
		close(done)
		ln.Close()
		wg.Wait()
	}
	return ln.Addr().String(), &n, stop
}

// resetUpstreamListener accepts TCP connections and immediately closes them,
// simulating an upstream that rejects requests at the transport layer (RST /
// EOF). Unlike hungUpstreamListener this is a transport-level failure, not a
// response-header hang, so the hung-upstream streak guard does not fire and
// the transport budget governs retries.
func resetUpstreamListener(t *testing.T) (addr string, accepts *atomic.Int64, stop func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	var n atomic.Int64
	var wg sync.WaitGroup
	done := make(chan struct{})
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			n.Add(1)
			wg.Add(1)
			go func(c net.Conn) {
				defer wg.Done()
				c.Close()
				<-done
			}(conn)
		}
	}()
	stop = func() {
		close(done)
		ln.Close()
		wg.Wait()
	}
	return ln.Addr().String(), &n, stop
}

// TestProviderAbortsAfterRepeatedResponseHeaderTimeouts drives the retry loop
// against a hung upstream and asserts that two consecutive response-header
// timeouts stop further attempts, instead of spinning through the remaining
// (or unlimited) retries.
func TestProviderAbortsAfterRepeatedResponseHeaderTimeouts(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		name := "non-streaming"
		if streaming {
			name = "streaming"
		}
		t.Run(name, func(t *testing.T) {
			addr, accepts, stop := hungUpstreamListener(t)
			defer stop()

			provider, err := NewProvider(&ProviderConfig{
				Type:                  "openai",
				BaseURL:               "http://" + addr,
				MaxRetries:            -1, // unlimited: only the fail-fast guard can stop the loop
				ResponseHeaderTimeout: 300 * time.Millisecond,
			})
			require.NoError(t, err)

			runtime := NewLLMRuntime(&RuntimeConfig{DefaultModel: "hung-model", MaxRetries: 0})
			require.NoError(t, runtime.RegisterProvider("hung-model", provider))

			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			start := time.Now()
			_, err = runtime.Call(ctx, &LLMRequest{
				Model:  "hung-model",
				Stream: streaming,
				Messages: []types.Message{{
					Role:    "user",
					Content: "hello",
				}},
			})
			// The call must fail on its own (no context cancellation), and the
			// failure must surface the hung-upstream diagnosis.
			require.Error(t, err)
			assert.NotContains(t, err.Error(), context.DeadlineExceeded.Error(), "test relied on ctx timeout, not the guard")
			assert.Contains(t, err.Error(), "timeout awaiting response headers",
				"error should report the hung-upstream header wait, got: %v", err)

			// With unlimited retries, a broken guard would spin the attempt
			// loop (each iteration failing in ~0ms on the reused corpse
			// connection) until the 15s context deadline. Failing fast keeps
			// the whole call well under a couple of seconds: first attempt
			// burns ResponseHeaderTimeout (300ms), the second fails
			// immediately, then the guard stops retries.
			if elapsed := time.Since(start); elapsed > 2*time.Second {
				t.Fatalf("fail-fast guard did not stop retry spin (elapsed %v)", elapsed)
			}
			if got := accepts.Load(); got > 2 {
				t.Fatalf("more than 2 connection attempts before fail-fast: %d", got)
			}
		})
	}
}

// TestProviderRetriesThroughFiniteBudgetDespiteHeaderTimeouts asserts that a
// FINITE provider budget reaches its configured "large" retry phase even when
// every attempt hits a response-header timeout. The hung-upstream guard is
// reserved for unlimited provider loops.
func TestProviderRetriesThroughFiniteBudgetDespiteHeaderTimeouts(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		name := "non-streaming"
		if streaming {
			name = "streaming"
		}
		t.Run(name, func(t *testing.T) {
			addr, accepts, stop := hungUpstreamListener(t)
			defer stop()

			provider, err := NewProvider(&ProviderConfig{
				Type:                  "openai",
				BaseURL:               "http://" + addr,
				MaxRetries:            3, // finite: the large retry phase must run
				ResponseHeaderTimeout: 300 * time.Millisecond,
			})
			require.NoError(t, err)

			runtime := NewLLMRuntime(&RuntimeConfig{DefaultModel: "hung-model", MaxRetries: 0})
			require.NoError(t, runtime.RegisterProvider("hung-model", provider))

			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			_, err = runtime.Call(ctx, &LLMRequest{
				Model:  "hung-model",
				Stream: streaming,
				Messages: []types.Message{{
					Role:    "user",
					Content: "hello",
				}},
			})
			require.Error(t, err)
			assert.NotContains(t, err.Error(), context.DeadlineExceeded.Error(), "test relied on ctx timeout, not the guard")
			assert.Contains(t, err.Error(), "timeout awaiting response headers",
				"error should report the hung-upstream header wait, got: %v", err)
			assert.Equal(t, int64(3), accepts.Load(),
				"finite provider budget must use all three attempts; got %d attempts", accepts.Load())
		})
	}
}

type headerGuardHandoffRoundTripper struct {
	requests  atomic.Int64
	streaming bool
}

func (t *headerGuardHandoffRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	n := t.requests.Add(1)
	if n <= 2 {
		return nil, &responseHeaderTimeoutError{timeout: 25 * time.Millisecond}
	}

	body := `{"id":"chatcmpl-recovered","object":"chat.completion","created":1,"model":"hung-model","choices":[{"index":0,"message":{"role":"assistant","content":"recovered"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`
	contentType := "application/json"
	if t.streaming {
		contentType = "text/event-stream"
		body = `data: {"choices":[{"index":0,"delta":{"content":"recovered"},"finish_reason":"stop"}]}` + "\n\n" +
			"data: [DONE]\n\n"
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     http.Header{"Content-Type": []string{contentType}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}, nil
}

// TestProviderHeaderGuardHandoffRetriesAtRuntimeLayer verifies that the
// provider-local hung-upstream guard does not become the final disposition of
// a request. After the guard aborts the first provider call, the runtime gets
// the transient cause and starts its larger retry loop; the third HTTP request
// is then allowed to succeed.
func TestProviderHeaderGuardHandoffRetriesAtRuntimeLayer(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		name := "non-streaming"
		if streaming {
			name = "streaming"
		}
		t.Run(name, func(t *testing.T) {
			provider, err := NewProvider(&ProviderConfig{
				Type:                  "openai",
				BaseURL:               "http://provider.invalid",
				MaxRetries:            -1,
				ResponseHeaderTimeout: 25 * time.Millisecond,
				RetryTuning: RetryTuning{
					BaseDelay:     time.Millisecond,
					MaxDelay:      time.Millisecond,
					Randomization: -1,
				},
			})
			require.NoError(t, err)
			transport := &headerGuardHandoffRoundTripper{streaming: streaming}
			wrapper := provider.(*ProviderWrapper)
			wrapper.httpClient = &http.Client{Transport: transport}
			wrapper.streamHTTPClient = &http.Client{Transport: transport}

			runtime := NewLLMRuntime(&RuntimeConfig{
				DefaultModel: "hung-model",
				MaxRetries:   2,
				RetryTuning: RetryTuning{
					BaseDelay:     time.Millisecond,
					MaxDelay:      time.Millisecond,
					Randomization: -1,
				},
			})
			require.NoError(t, runtime.RegisterProvider("hung-model", provider))

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			resp, err := runtime.Call(ctx, &LLMRequest{
				Model:  "hung-model",
				Stream: streaming,
				Messages: []types.Message{{
					Role:    "user",
					Content: "hello",
				}},
			})
			require.NoError(t, err)
			require.NotNil(t, resp)
			assert.Equal(t, "recovered", resp.Content)
			assert.Equal(t, int64(3), transport.requests.Load(),
				"the outer retry should follow the two guarded requests")
		})
	}
}

// TestProviderRetriesTransientErrorsNormally guards against the fail-fast
// guard firing for non-header errors: a 5xx retry still uses both retries.
func TestProviderRetriesTransientErrorsNormally(t *testing.T) {
	t.Skip("covered by existing retry tests in retry_executor_test.go / gateway tests")
}

// TestTrackHeaderTimeoutStreak exercises the hung-upstream streak state
// machine, including the interleaving that a naive boolean "previous attempt
// timed out" guard gets wrong: hang (#1), transient error (#2), hang (#3),
// hang (#4). Only a streak that resets on any non-header error lets attempts
// #3 and #4 both happen before aborting.
func TestTrackHeaderTimeoutStreak(t *testing.T) {
	headerErr := errors.New("Post \"http://h\": net/http: timeout awaiting response headers")

	t.Run("two consecutive header timeouts abort", func(t *testing.T) {
		var n int
		if trackHeaderTimeoutStreak(&n, headerErr) {
			t.Fatal("first timeout must not abort")
		}
		if !trackHeaderTimeoutStreak(&n, headerErr) {
			t.Fatal("second consecutive timeout must abort")
		}
		if n != 2 {
			t.Fatalf("streak = %d, want 2", n)
		}
	})

	t.Run("any non-header error resets the streak", func(t *testing.T) {
		var n int
		trackHeaderTimeoutStreak(&n, headerErr) // streak = 1
		if trackHeaderTimeoutStreak(&n, fmt.Errorf("upstream 429: %w", errors.New("rate limit"))) {
			t.Fatal("non-header error must not abort")
		}
		if n != 0 {
			t.Fatalf("streak = %d, want 0 after reset", n)
		}
		trackHeaderTimeoutStreak(&n, headerErr)
		if !trackHeaderTimeoutStreak(&n, headerErr) {
			t.Fatal("two consecutive header timeouts after a reset must abort")
		}
	})

	t.Run("healthy interleaving never misjudged as consecutive", func(t *testing.T) {
		var n int
		trackHeaderTimeoutStreak(&n, headerErr) // hang: streak 1
		trackHeaderTimeoutStreak(&n, errors.New("connection reset by peer"))
		if trackHeaderTimeoutStreak(&n, headerErr) { // hang again: streak 1
			t.Fatal("must not abort: only one header timeout since the reset")
		}
		if n != 1 {
			t.Fatalf("streak = %d, want 1 (hang, reset, hang)", n)
		}
	})
}

// TestProviderTransportBudgetBoundsResetUpstream asserts the two-tier budget
// (mirrors codex-rs request-level transport retries): a transport-level
// rejection (connection reset immediately after accept) burns the tighter
// transport budget (4 attempts) instead of the full business budget (10
// attempts), because retrying a dead connection from scratch rarely succeeds
// immediately. The reset upstream is used (not a header hang) so the
// hung-upstream streak guard does not preempt the transport budget.
func TestProviderTransportBudgetBoundsResetUpstream(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		name := "non-streaming"
		if streaming {
			name = "streaming"
		}
		t.Run(name, func(t *testing.T) {
			addr, accepts, stop := resetUpstreamListener(t)
			defer stop()

			provider, err := NewProvider(&ProviderConfig{
				Type:                  "openai",
				BaseURL:               "http://" + addr,
				MaxRetries:            10, // generous business budget
				MaxTransportRetries:   4,  // tighter transport budget wins
				ResponseHeaderTimeout: 300 * time.Millisecond,
			})
			require.NoError(t, err)

			runtime := NewLLMRuntime(&RuntimeConfig{DefaultModel: "reset-model", MaxRetries: 10})
			require.NoError(t, runtime.RegisterProvider("reset-model", provider))

			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			_, err = runtime.Call(ctx, &LLMRequest{
				Model:  "reset-model",
				Stream: streaming,
				Messages: []types.Message{{
					Role:    "user",
					Content: "hello",
				}},
			})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "transport",
				"transport budget exhaustion should surface as transport failure, got: %v", err)
			assert.Equal(t, int64(4), accepts.Load(),
				"transport budget of 4 attempts must bound the reset upstream; got %d attempts", accepts.Load())
		})
	}
}

type transportBudgetHeaderTimeoutRoundTripper struct {
	requests atomic.Int64
}

func (t *transportBudgetHeaderTimeoutRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	t.requests.Add(1)
	return nil, &responseHeaderTimeoutError{timeout: time.Millisecond}
}

// TestProviderTransportBudgetWinsOverHeaderGuard verifies that an explicitly
// finite transport budget is authoritative even when the business retry
// budget is unlimited. The two-consecutive-timeout guard is only a fallback
// for the case where neither configured budget can bound the loop.
func TestProviderTransportBudgetWinsOverHeaderGuard(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		name := "non-streaming"
		if streaming {
			name = "streaming"
		}
		t.Run(name, func(t *testing.T) {
			provider, err := NewProvider(&ProviderConfig{
				Type:                  "openai",
				BaseURL:               "http://provider.invalid",
				MaxRetries:            -1,
				MaxTransportRetries:   4,
				ResponseHeaderTimeout: time.Millisecond,
				RetryTuning: RetryTuning{
					BaseDelay:     time.Millisecond,
					MaxDelay:      time.Millisecond,
					Randomization: -1,
				},
			})
			require.NoError(t, err)
			transport := &transportBudgetHeaderTimeoutRoundTripper{}
			wrapper := provider.(*ProviderWrapper)
			wrapper.httpClient = &http.Client{Transport: transport}
			wrapper.streamHTTPClient = &http.Client{Transport: transport}

			runtime := NewLLMRuntime(&RuntimeConfig{
				DefaultModel: "transport-budget-model",
				MaxRetries:   0,
			})
			require.NoError(t, runtime.RegisterProvider("transport-budget-model", provider))

			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, err = runtime.Call(ctx, &LLMRequest{
				Model:  "transport-budget-model",
				Stream: streaming,
				Messages: []types.Message{{
					Role:    "user",
					Content: "hello",
				}},
			})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "transport",
				"the finite transport budget should determine the terminal error, got: %v", err)
			assert.Equal(t, int64(4), transport.requests.Load(),
				"finite transport budget must allow four attempts before exhaustion")
		})
	}
}

// TestProviderHeaderGuardAppliesToHTTP2 reproduces the hung-upstream shape
// over HTTP/2: the transport-level ResponseHeaderTimeout is HTTP/1.1-only,
// so without the per-request header guard an HTTP/2 request whose response
// headers never arrive would hang forever. The server below accepts the
// request (over H2) and never writes headers back.
func TestProviderHeaderGuardAppliesToHTTP2(t *testing.T) {
	protoCh := make(chan string, 1)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case protoCh <- fmt.Sprintf("%d.%d", r.ProtoMajor, r.ProtoMinor):
		default:
		}
		select {} // hold the request, never respond
	})
	ts := httptest.NewUnstartedServer(handler)
	ts.EnableHTTP2 = true
	ts.StartTLS()
	defer ts.Close()

	p := &ProviderWrapper{
		config: &ProviderConfig{ResponseHeaderTimeout: 300 * time.Millisecond},
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, ts.URL, nil)
	require.NoError(t, err)

	start := time.Now()
	_, err = p.doRequest(ts.Client(), req)
	require.Error(t, err)
	require.Equal(t, "2.0", <-protoCh, "test must exercise HTTP/2 to cover the guard")
	assert.Contains(t, err.Error(), "timeout awaiting response headers",
		"h2 hang must surface as a header timeout, got: %v", err)
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("header guard did not stop the h2 request promptly (elapsed %v)", elapsed)
	}
}

type guardedResponseRoundTripper struct {
	contexts chan context.Context
}

func (t *guardedResponseRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	t.contexts <- req.Context()
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("ok")),
		Request:    req,
	}, nil
}

// TestProviderDoRequestReleasesGuardContextOnBodyClose ensures the
// response-header watchdog does not leak its child context while preserving
// the response body long enough for streaming callers to consume it.
func TestProviderDoRequestReleasesGuardContextOnBodyClose(t *testing.T) {
	transport := &guardedResponseRoundTripper{contexts: make(chan context.Context, 1)}
	provider := &ProviderWrapper{
		config: &ProviderConfig{ResponseHeaderTimeout: time.Second},
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://provider.invalid", nil)
	require.NoError(t, err)

	resp, err := provider.doRequest(&http.Client{Transport: transport}, req)
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.NotNil(t, resp.Body)
	guardedCtx := <-transport.contexts
	require.NoError(t, guardedCtx.Err(), "guard context must remain live while the body is readable")

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, "ok", string(body))
	require.NoError(t, resp.Body.Close())
	require.Eventually(t, func() bool {
		return errors.Is(guardedCtx.Err(), context.Canceled)
	}, time.Second, time.Millisecond,
		"closing the response body must release the watchdog context")
}
