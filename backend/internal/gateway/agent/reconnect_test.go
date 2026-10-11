package agent

import (
	"context"
	"sync"
	"testing"
	"time"
)

// doneFakeConn is a fakeConn with the optional death signal; it powers the
// auto-reconnect watcher tests.
type doneFakeConn struct {
	fakeConn
	done chan struct{}
}

func (f *doneFakeConn) Done() <-chan struct{} { return f.done }

type dialFuncRecorder struct {
	mu    sync.Mutex
	conns []Conn
	calls int
}

func (d *dialFuncRecorder) dial(_ context.Context, _ Config) (Conn, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls++
	i := d.calls - 1
	if i < len(d.conns) {
		return d.conns[i], nil
	}
	return d.conns[len(d.conns)-1], nil
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("condition not met within deadline: %s", what)
}

func TestAutoReconnectAfterConnectionLoss(t *testing.T) {
	first := &doneFakeConn{fakeConn: fakeConn{nodeID: "node-a", consoleURL: "u"}, done: make(chan struct{})}
	second := &doneFakeConn{fakeConn: fakeConn{nodeID: "node-a", consoleURL: "u"}, done: make(chan struct{})}
	rec := &dialFuncRecorder{conns: []Conn{first, second}}

	c := New(Config{Enabled: true, URL: "wss://gw", Token: "agw_x", AutoConnect: true}, rec.dial)
	if err := c.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if got := c.Status().State; got != StateConnected {
		t.Fatalf("state = %s, want connected", got)
	}

	// The gateway drains: the connection dies on its own (GOAWAY close).
	close(first.done)

	// The watcher marks the loss and re-dials automatically.
	waitFor(t, "state enters Reconnecting after loss", func() bool {
		return c.Status().State == StateReconnecting
	})
	waitFor(t, "automatic reconnect to connected state", func() bool {
		return c.Status().State == StateConnected
	})
	rec.mu.Lock()
	calls := rec.calls
	rec.mu.Unlock()
	if calls < 2 {
		t.Fatalf("dial calls = %d, want >= 2 (re-dial after loss)", calls)
	}
	if got := c.Status().ReconnectCount; got < 1 {
		t.Fatalf("reconnect count = %d, want >= 1", got)
	}

	// A live reconnect must be watchable again: kill the second conn and expect
	// another automatic dial.
	close(second.done)
	waitFor(t, "second automatic reconnect", func() bool {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		return rec.calls >= 3
	})
}

func TestOperatorDisconnectSuppressesAutoReconnect(t *testing.T) {
	first := &doneFakeConn{fakeConn: fakeConn{nodeID: "node-a", consoleURL: "u"}, done: make(chan struct{})}
	rec := &dialFuncRecorder{conns: []Conn{first}}

	c := New(Config{Enabled: true, URL: "wss://gw", Token: "agw_x", AutoConnect: true}, rec.dial)
	if err := c.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := c.Disconnect(context.Background()); err != nil {
		t.Fatalf("disconnect: %v", err)
	}

	// Simulate the socket dying after the operator disconnect: no auto redial.
	close(first.done)
	time.Sleep(300 * time.Millisecond)
	rec.mu.Lock()
	calls := rec.calls
	rec.mu.Unlock()
	if calls != 1 {
		t.Fatalf("dial calls = %d, want 1 (operator disconnect suppresses reconnect)", calls)
	}
	if got := c.Status().State; got != StateDisconnected {
		t.Fatalf("state = %s, want disconnected", got)
	}
}
