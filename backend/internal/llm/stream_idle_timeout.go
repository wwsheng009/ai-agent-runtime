package llm

import (
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// ErrStreamIdleTimeout is returned when a streaming response has not produced
// any bytes for StreamReadTimeout. It is wrapped by StreamIdleTimeoutError so
// callers can distinguish it from EOF and other read errors.
var ErrStreamIdleTimeout = errors.New("stream read idle timeout")

// StreamIdleTimeoutError reports that a streaming read stalled for the
// configured idle window. The underlying connection is closed to unblock the
// stuck read, so the error is terminal for that stream attempt.
type StreamIdleTimeoutError struct {
	Idle time.Duration
}

func (e *StreamIdleTimeoutError) Error() string {
	return fmt.Sprintf("%v: no data received for %s", ErrStreamIdleTimeout, e.Idle)
}

func (e *StreamIdleTimeoutError) Unwrap() error { return ErrStreamIdleTimeout }

// idleTimeoutReadCloser wraps an io.ReadCloser and aborts reads that stay
// idle for longer than timeout. Go's http.Response.Body has no read-deadline
// API for clients (http.NewResponseController is server-side only), so we use
// a watchdog goroutine: every successful read refreshes lastActivity, and a
// timer closes the underlying body when the idle window is exceeded, which
// unblocks any in-flight Read.
//
// A positive idle budget only guards "expected data but got none"; streams
// that keep producing bytes (even slowly, e.g. reasoning token deltas or
// keep-alive lines) never trip it, so long-running legitimate tasks are
// unaffected.
type idleTimeoutReadCloser struct {
	r      io.ReadCloser
	idle   time.Duration
	mu     sync.Mutex
	closed bool
	// lastActivity is guarded by mu; updated on every successful read.
	lastActivity time.Time
	// timedOut is set once the watchdog decides the stream is stuck.
	timedOut atomic.Bool
	// stop closes the watchdog goroutine when the reader is closed.
	stop chan struct{}
	// watchdogDone signals the watchdog has exited.
	watchdogDone chan struct{}
}

// wrapStreamIdleTimeout wraps rc with an idle timeout. A non-positive timeout
// returns rc unchanged.
func wrapStreamIdleTimeout(rc io.ReadCloser, timeout time.Duration) io.ReadCloser {
	if rc == nil || timeout <= 0 {
		return rc
	}
	w := &idleTimeoutReadCloser{
		r:            rc,
		idle:         timeout,
		lastActivity: time.Now(),
		stop:         make(chan struct{}),
		watchdogDone: make(chan struct{}),
	}
	go w.watchdog()
	return w
}

func (w *idleTimeoutReadCloser) Read(p []byte) (int, error) {
	if w.timedOut.Load() {
		return 0, &StreamIdleTimeoutError{Idle: w.idle}
	}
	n, err := w.r.Read(p)
	if n > 0 {
		w.mu.Lock()
		w.lastActivity = time.Now()
		w.mu.Unlock()
	}
	if err != nil {
		if w.timedOut.Load() {
			return n, &StreamIdleTimeoutError{Idle: w.idle}
		}
		if errors.Is(err, io.EOF) {
			w.stopWatchdog()
		}
	}
	return n, err
}

func (w *idleTimeoutReadCloser) Close() error {
	w.stopWatchdog()
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	return w.r.Close()
}

func (w *idleTimeoutReadCloser) stopWatchdog() {
	select {
	case <-w.stop:
	default:
		close(w.stop)
	}
}

func (w *idleTimeoutReadCloser) watchdog() {
	ticker := time.NewTicker(w.idle)
	defer ticker.Stop()
	defer close(w.watchdogDone)
	for {
		select {
		case <-w.stop:
			return
		case <-ticker.C:
			w.mu.Lock()
			idleFor := time.Since(w.lastActivity)
			w.mu.Unlock()
			if idleFor >= w.idle {
				// Close the underlying body to unblock a stuck Read.
				w.timedOut.Store(true)
				w.stopWatchdog()
				_ = w.r.Close()
				return
			}
		}
	}
}
