package agent

import (
	"context"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"
)

// TestStaleHeartbeatTimeout scales the deadline with the negotiated interval
// and keeps a floor of the default for zero values.
func TestStaleHeartbeatTimeout(t *testing.T) {
	cases := []struct {
		interval time.Duration
		want     time.Duration
	}{
		{0, 3 * DefaultHeartbeatInterval},
		{5 * time.Second, 15 * time.Second},
		{20 * time.Second, 60 * time.Second},
	}
	for _, tc := range cases {
		if got := staleHeartbeatTimeout(tc.interval); got != tc.want {
			t.Fatalf("staleHeartbeatTimeout(%s) = %s, want %s", tc.interval, got, tc.want)
		}
	}
}

// TestWatchdogClosesSilentGateway drives the half-open scenario end to end:
// the gateway accepts the handshake then never acknowledges heartbeats. The
// watchdog must close the connection within the 3× interval deadline, which
// surfaces as a lost connection the controller's auto-reconnect watcher sees.
func TestWatchdogClosesSilentGateway(t *testing.T) {
	srv, fake := newFakeGateway(t, fakeGatewayConfig{silent: true})
	defer srv.Close()

	connector := &WSConnector{
		Register: func() wire.Register { return wire.Register{NodeID: "node-silent"} },
		// Shorten the heartbeat cadence (and therefore the watchdog deadline)
		// so the test finishes quickly. The interval must be set before Dial:
		// the watchdog is armed from it at connection setup.
		HeartbeatInterval: 200 * time.Millisecond,
	}
	conn, err := connector.Dial(context.Background(), Config{Enabled: true, URL: srv.URL, Token: "agw_x"})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	ws := conn.(*wsConn)

	acks := 0
	ws.onHeartbeatAck = func() { acks++ }

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-fake.heartbeats:
			// heartbeats are flowing; the gateway is just not acking (silent)
		default:
		}
		select {
		case <-ws.Done():
			// Watchdog fired: the half-open connection was closed.
			if acks != 0 {
				t.Fatalf("acks = %d, want 0 on a silent gateway", acks)
			}
			return
		case <-time.After(50 * time.Millisecond):
		}
	}
	t.Fatal("watchdog did not close the silent connection within deadline")
}

// TestWatchdogKeepsAckedConnection verifies the watchdog does not close a
// healthy connection: the fake gateway acks every heartbeat, so Done() must
// never fire within several watchdog windows.
func TestWatchdogKeepsAckedConnection(t *testing.T) {
	srv, _ := newFakeGateway(t, fakeGatewayConfig{})
	defer srv.Close()

	connector := &WSConnector{
		Register:          func() wire.Register { return wire.Register{NodeID: "node-healthy"} },
		HeartbeatInterval: 200 * time.Millisecond,
	}
	conn, err := connector.Dial(context.Background(), Config{Enabled: true, URL: srv.URL, Token: "agw_x"})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	ws := conn.(*wsConn)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ws.Done():
			t.Fatal("watchdog closed an acked (healthy) connection")
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// TestMarkHeartbeatOnAck verifies the controller refreshes Status.LastHeartbeat
// when the transport reports a gateway liveness signal.
func TestMarkHeartbeatOnAck(t *testing.T) {
	c := NewController(Config{Enabled: false}, nil)
	if !c.Status().LastHeartbeat.IsZero() {
		t.Fatalf("LastHeartbeat pre-connect = %v, want zero", c.Status().LastHeartbeat)
	}
	c.MarkHeartbeat()
	if c.Status().LastHeartbeat.IsZero() {
		t.Fatal("MarkHeartbeat did not refresh LastHeartbeat")
	}
}
