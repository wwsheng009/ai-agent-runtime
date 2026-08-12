package llm

import (
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeReadCloser wraps a string reader with an optional per-read delay.
type fakeReadCloser struct {
	r     *strings.Reader
	delay time.Duration
}

func (f *fakeReadCloser) Read(p []byte) (int, error) {
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	return f.r.Read(p)
}

func (f *fakeReadCloser) Close() error { return nil }

// blockingReadCloser blocks forever on Read until Close is called.
type blockingReadCloser struct {
	closed chan struct{}
	once   sync.Once
}

func newBlockingReadCloser() *blockingReadCloser {
	return &blockingReadCloser{closed: make(chan struct{})}
}

func (b *blockingReadCloser) Read(p []byte) (int, error) {
	select {
	case <-b.closed:
		return 0, io.EOF
	}
}

func (b *blockingReadCloser) Close() error {
	b.once.Do(func() { close(b.closed) })
	return nil
}

func TestWrapStreamIdleTimeout_NormalStream(t *testing.T) {
	rc := wrapStreamIdleTimeout(&fakeReadCloser{r: strings.NewReader("hello world")}, 200*time.Millisecond)
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(data) != "hello world" {
		t.Fatalf("unexpected data: %q", string(data))
	}
}

func TestWrapStreamIdleTimeout_IdleTimeout(t *testing.T) {
	rc := wrapStreamIdleTimeout(newBlockingReadCloser(), 100*time.Millisecond)
	defer rc.Close()

	start := time.Now()
	var err error
	buf := make([]byte, 8)
	// First read blocks; watchdog should close the body and unblock it.
	_, err = rc.Read(buf)
	if err == nil {
		t.Fatal("expected an error from idle timeout")
	}
	var idleErr *StreamIdleTimeoutError
	if !errors.As(err, &idleErr) {
		t.Fatalf("expected StreamIdleTimeoutError, got %T: %v", err, err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("idle timeout took too long: %v", elapsed)
	}
}

func TestWrapStreamIdleTimeout_ActiveStreamResetsTimer(t *testing.T) {
	// A stream that yields a byte every 50ms for 400ms must NOT time out with
	// a 150ms idle budget, because each read refreshes the deadline.
	rc := wrapStreamIdleTimeout(&fakeReadCloser{
		r:     strings.NewReader(strings.Repeat("x", 400)),
		delay: 50 * time.Millisecond,
	}, 150*time.Millisecond)
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("active stream should not time out: %v", err)
	}
	if len(data) != 400 {
		t.Fatalf("unexpected data length: %d", len(data))
	}
}

func TestWrapStreamIdleTimeout_Disabled(t *testing.T) {
	// timeout <= 0 returns the reader unchanged (nil-safe).
	if got := wrapStreamIdleTimeout(nil, 0); got != nil {
		t.Fatal("nil input must stay nil")
	}
	orig := &fakeReadCloser{r: strings.NewReader("x")}
	if got := wrapStreamIdleTimeout(orig, 0); got != orig {
		t.Fatal("non-positive timeout must return the original reader")
	}
}

func TestStreamIdleTimeoutError_Unwrap(t *testing.T) {
	err := &StreamIdleTimeoutError{Idle: time.Second}
	if !errors.Is(err, ErrStreamIdleTimeout) {
		t.Fatal("expected ErrStreamIdleTimeout via errors.Is")
	}
	if err.Error() == "" {
		t.Fatal("expected non-empty error message")
	}
}
