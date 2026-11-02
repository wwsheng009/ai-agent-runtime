package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// flakyDial returns retryable network failures for the first `failures` calls,
// then the configured connection (or a caller-supplied terminal error).
type flakyDial struct {
	mu       sync.Mutex
	calls    int
	failures int
	err      error
	conn     Conn
}

func (d *flakyDial) dial(_ context.Context, _ Config) (Conn, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls++
	if d.failures > 0 {
		d.failures--
		if d.err != nil {
			return nil, d.err
		}
		return nil, &ConnectError{Code: ErrCodeNetwork, Message: fmt.Sprintf("dial refused (call %d)", d.calls), Retryable: true}
	}
	return d.conn, nil
}

func (d *flakyDial) callCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

// logCollector captures lifecycle diagnostics for assertions.
type logCollector struct {
	mu    sync.Mutex
	lines []string
}

func (l *logCollector) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *logCollector) joined() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

// fastRetryDelays keeps retry tests well under a second.
func fastRetryDelays() []time.Duration {
	return []time.Duration{2 * time.Millisecond, 2 * time.Millisecond, 2 * time.Millisecond}
}

func TestConnectWithRetryRetriesUntilSuccess(t *testing.T) {
	rec := &flakyDial{failures: 2, conn: &fakeConn{nodeID: "node-retry"}}
	logs := &logCollector{}
	c := New(enabledConfig(), rec.dial, WithLogger(logs.logf), WithReconnectDelays(fastRetryDelays()))

	if err := c.ConnectWithRetry(context.Background()); err != nil {
		t.Fatalf("ConnectWithRetry: %v", err)
	}
	st := c.Status()
	if st.State != StateConnected || st.NodeID != "node-retry" {
		t.Fatalf("status = %+v, want connected as node-retry", st)
	}
	if st.ReconnectCount != 0 {
		t.Fatalf("reconnect count = %d, want 0 (pre-first-connect retries are not reconnects)", st.ReconnectCount)
	}
	if got := rec.callCount(); got != 3 {
		t.Fatalf("dial calls = %d, want 3 (two retries then success)", got)
	}
	joined := logs.joined()
	if !strings.Contains(joined, "retrying in") {
		t.Fatalf("retry diagnostics missing:\n%s", joined)
	}
	if !strings.Contains(joined, "connected as node node-retry") {
		t.Fatalf("success diagnostics missing:\n%s", joined)
	}
}

func TestConnectWithRetryStopsOnTerminalError(t *testing.T) {
	rec := &flakyDial{
		failures: 5,
		err:      &ConnectError{Code: ErrCodeAuthInvalid, Message: "device token revoked", Retryable: false},
	}
	logs := &logCollector{}
	c := New(enabledConfig(), rec.dial, WithLogger(logs.logf), WithReconnectDelays(fastRetryDelays()))

	err := c.ConnectWithRetry(context.Background())
	var ce *ConnectError
	if !errors.As(err, &ce) || ce.Code != ErrCodeAuthInvalid {
		t.Fatalf("err = %v, want auth_invalid ConnectError", err)
	}
	if got := rec.callCount(); got != 1 {
		t.Fatalf("dial calls = %d, want 1 (terminal failures must not retry)", got)
	}
	if st := c.Status(); st.State != StateError || st.Retryable {
		t.Fatalf("status = %+v, want terminal error state", st)
	}
	if !strings.Contains(logs.joined(), "not retrying") {
		t.Fatalf("terminal stop diagnostics missing:\n%s", logs.joined())
	}
}

func TestConnectWithRetryHonorsContextCancel(t *testing.T) {
	rec := &flakyDial{failures: 1000}
	c := New(enabledConfig(), rec.dial, WithReconnectDelays([]time.Duration{50 * time.Millisecond}))

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- c.ConnectWithRetry(ctx) }()

	waitFor(t, "first connect attempt", func() bool { return rec.callCount() >= 1 })
	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ConnectWithRetry did not return after context cancel")
	}
}

func TestConnectWithRetryStopsOnOperatorDisconnect(t *testing.T) {
	rec := &flakyDial{failures: 1000}
	c := New(enabledConfig(), rec.dial, WithReconnectDelays([]time.Duration{200 * time.Millisecond}))

	errCh := make(chan error, 1)
	go func() { errCh <- c.ConnectWithRetry(context.Background()) }()

	waitFor(t, "first connect attempt", func() bool { return rec.callCount() >= 1 })
	if err := c.Disconnect(context.Background()); err != nil {
		t.Fatalf("disconnect: %v", err)
	}

	select {
	case err := <-errCh:
		var ce *ConnectError
		if !errors.As(err, &ce) || ce.Code != ErrCodeOperatorDisconnect || ce.Retryable {
			t.Fatalf("err = %v, want terminal operator_disconnect", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ConnectWithRetry did not stop after operator disconnect")
	}
	if st := c.Status(); st.State != StateDisconnected || !st.OperatorDisconnected {
		t.Fatalf("status = %+v, want operator-disconnected state", st)
	}
}

// TestAutoConnectRefusedAfterOperatorDisconnect pins the suppression contract:
// only ConnectManual may clear an explicit disconnect; the auto path must not
// silently undo it.
func TestAutoConnectRefusedAfterOperatorDisconnect(t *testing.T) {
	rec := &flakyDial{conn: &fakeConn{nodeID: "node-manual"}}
	c := New(enabledConfig(), rec.dial)

	if err := c.Disconnect(context.Background()); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	err := c.Connect(context.Background())
	var ce *ConnectError
	if !errors.As(err, &ce) || ce.Code != ErrCodeOperatorDisconnect || ce.Retryable {
		t.Fatalf("auto connect err = %v, want terminal operator_disconnect", err)
	}
	if got := rec.callCount(); got != 0 {
		t.Fatalf("dial calls = %d, want 0 (suppressed connect must not dial)", got)
	}

	if err := c.ConnectManual(context.Background()); err != nil {
		t.Fatalf("manual connect: %v", err)
	}
	st := c.Status()
	if st.State != StateConnected || st.OperatorDisconnected {
		t.Fatalf("status = %+v, want connected with suppression cleared", st)
	}
	if got := rec.callCount(); got != 1 {
		t.Fatalf("dial calls = %d, want 1 after manual connect", got)
	}
}

// TestOperatorDisconnectStopsAutoReconnectDuringBackoff covers the reconnect
// loop that is already backing off when the operator disconnects: without the
// generation invalidation it would dial again and silently resurrect the
// tunnel.
func TestOperatorDisconnectStopsAutoReconnectDuringBackoff(t *testing.T) {
	first := &doneFakeConn{fakeConn: fakeConn{nodeID: "node-a", consoleURL: "u"}, done: make(chan struct{})}
	second := &doneFakeConn{fakeConn: fakeConn{nodeID: "node-a", consoleURL: "u"}, done: make(chan struct{})}

	var mu sync.Mutex
	calls := 0
	dial := func(_ context.Context, _ Config) (Conn, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		switch calls {
		case 1:
			return first, nil
		case 2:
			return nil, &ConnectError{Code: ErrCodeNetwork, Message: "gateway refusing", Retryable: true}
		default:
			return second, nil
		}
	}

	c := New(enabledConfig(), dial, WithReconnectDelays([]time.Duration{5 * time.Millisecond, 100 * time.Millisecond}))
	if err := c.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	close(first.done) // the connection dies on its own

	waitFor(t, "one failed reconnect attempt", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return calls >= 2
	})
	if err := c.Disconnect(context.Background()); err != nil {
		t.Fatalf("disconnect: %v", err)
	}

	// The loop's next dial would land ~100ms after the failed attempt; give it
	// a full window and assert it never happened.
	time.Sleep(250 * time.Millisecond)
	mu.Lock()
	got := calls
	mu.Unlock()
	if got != 2 {
		t.Fatalf("dial calls = %d, want 2 (operator disconnect must stop the retry loop)", got)
	}
	if state := c.Status().State; state != StateDisconnected {
		t.Fatalf("state = %s, want disconnected", state)
	}
}
