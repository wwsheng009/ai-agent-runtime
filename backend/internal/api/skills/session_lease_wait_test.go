package skills

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

// TestAcquireSessionLeaseWaitsForSameProcessHolder verifies that a lease
// conflict with another holder inside the same runtime-server process queues
// (waits) instead of failing immediately with 409: two web turns on one
// session, or a web turn behind a hub actor turn, must serialize rather than
// self-conflict.
func TestAcquireSessionLeaseWaitsForSameProcessHolder(t *testing.T) {
	ctx := context.Background()
	handler := NewHandler(nil, nil, nil)

	handle1, err := handler.acquireSessionLease(ctx, "session-same-proc", "runtime-server-agent-chat", "request-1")
	require.NoError(t, err)
	require.NotNil(t, handle1)

	// Second acquire for the same session from the same process must wait for
	// handle1 to be released instead of returning a conflict error.
	acquired := make(chan *chat.SessionLeaseHandle, 1)
	acquireErr := make(chan error, 1)
	go func() {
		h2, err := handler.acquireSessionLease(ctx, "session-same-proc", "runtime-server-agent-chat", "request-2")
		_ = h2
		if err != nil {
			acquireErr <- err
			return
		}
		acquired <- h2
	}()

	// Give the waiter a moment to hit the conflict path, then release holder 1.
	time.Sleep(300 * time.Millisecond)
	select {
	case err := <-acquireErr:
		t.Fatalf("same-process acquire failed unexpectedly: %v", err)
	case <-acquired:
		t.Fatal("same-process acquire returned before holder released")
	default:
	}

	require.NoError(t, handle1.Release(ctx))

	select {
	case h2 := <-acquired:
		require.NotNil(t, h2)
		require.NoError(t, h2.Release(ctx))
	case err := <-acquireErr:
		t.Fatalf("same-process acquire failed after holder released: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("same-process acquire did not proceed after holder released")
	}
}

// TestAcquireSessionLeaseWaitsConcurrently verifies concurrent acquires
// from the same process all eventually succeed (serialized), matching the
// "two turns on the same session" web scenario: each holder releases after
// acquiring, so the next waiter can proceed.
func TestAcquireSessionLeaseWaitsConcurrently(t *testing.T) {
	handler := NewHandler(nil, nil, nil)
	const n = 3
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			ctx := context.Background()
			h, err := handler.acquireSessionLease(ctx, "session-same-proc-concurrent",
				"runtime-server-agent-chat", "request-"+string(rune('a'+idx)))
			if err != nil {
				errs[idx] = err
				return
			}
			defer h.Release(ctx)
			errs[idx] = nil
		}(i)
	}
	close(start)
	wg.Wait()

	for i := 0; i < n; i++ {
		require.NoError(t, errs[i], "concurrent acquire %d must eventually succeed", i)
	}
}

// TestAcquireSessionLeaseWaitsRespectsContext verifies that a same-process
// wait aborts when the request context is cancelled.
func TestAcquireSessionLeaseWaitsRespectsContext(t *testing.T) {
	handler := NewHandler(nil, nil, nil)
	ctx := context.Background()
	handle1, err := handler.acquireSessionLease(ctx, "session-same-proc-cancel", "runtime-server-agent-chat", "request-1")
	require.NoError(t, err)
	defer handle1.Release(ctx)

	waitCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	_, err = handler.acquireSessionLease(waitCtx, "session-same-proc-cancel", "runtime-server-agent-chat", "request-2")
	require.Error(t, err, "acquire with cancelled context while same-process holder active must fail")
}
