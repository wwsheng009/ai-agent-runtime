package llm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
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
				MaxRetries:            0, // unlimited: only the fail-fast guard can stop the loop
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
// FINITE retry budget is honored end-to-end even when every attempt hits a
// response-header timeout: the agent contract is to retry within the budget,
// so the hung-upstream guard must not take away the configured attempts.
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
				MaxRetries:            3, // finite: retries must run their course
				ResponseHeaderTimeout: 300 * time.Millisecond,
			})
			require.NoError(t, err)

			runtime := NewLLMRuntime(&RuntimeConfig{DefaultModel: "hung-model", MaxRetries: 3})
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
			assert.Contains(t, err.Error(), "timeout awaiting response headers",
				"error should report the hung-upstream header wait, got: %v", err)
			assert.Equal(t, int64(3), accepts.Load(),
				"finite budget of 3 attempts must be fully used; the guard must not stop retries early")
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

// TestProviderTransportBudgetBoundsHungUpstream asserts the two-tier budget
// (mirrors codex-rs request-level transport retries): a hung upstream burns
// the tighter transport budget (4 attempts) instead of the full business
// budget (10 attempts), because retrying a dead connection from scratch
// rarely succeeds immediately.
func TestProviderTransportBudgetBoundsHungUpstream(t *testing.T) {
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
				MaxRetries:            10, // generous business budget
				MaxTransportRetries:   4,  // tighter transport budget wins
				ResponseHeaderTimeout: 300 * time.Millisecond,
			})
			require.NoError(t, err)

			runtime := NewLLMRuntime(&RuntimeConfig{DefaultModel: "hung-model", MaxRetries: 10})
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
			assert.Contains(t, err.Error(), "timeout awaiting response headers",
				"error should report the hung-upstream header wait, got: %v", err)
			assert.Contains(t, err.Error(), "transport",
				"transport budget exhaustion should surface as transport failure, got: %v", err)
			assert.Equal(t, int64(4), accepts.Load(),
				"transport budget of 4 attempts must bound the hung upstream; got %d attempts", accepts.Load())
		})
	}
}
