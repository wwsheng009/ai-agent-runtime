package agent

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeConn struct {
	nodeID     string
	consoleURL string
	closed     bool
}

func (f *fakeConn) NodeID() string     { return f.nodeID }
func (f *fakeConn) ConsoleURL() string { return f.consoleURL }
func (f *fakeConn) Close() error       { f.closed = true; return nil }

type dialRecorder struct {
	mu    sync.Mutex
	calls int
	conn  *fakeConn
	err   error
}

func (d *dialRecorder) dial(_ context.Context, _ Config) (Conn, error) {
	d.mu.Lock()
	d.calls++
	conn, err := d.conn, d.err
	d.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if conn == nil {
		conn = &fakeConn{nodeID: "node-test", consoleURL: "https://gw/n/node-test/web/"}
	}
	return conn, nil
}

func (d *dialRecorder) callCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

func fixedClock() time.Time { return time.Unix(1_700_000_000, 0).UTC() }

func enabledConfig() Config {
	return Config{Enabled: true, URL: "wss://gw.example.com", Token: "agw_abcdef123456", AutoConnect: true}
}

func TestNewInitialState(t *testing.T) {
	if got := New(enabledConfig(), nil).Status().State; got != StateDisconnected {
		t.Fatalf("enabled config should start disconnected, got %s", got)
	}
	if got := New(Config{Enabled: false}, nil).Status().State; got != StateDisabled {
		t.Fatalf("disabled config should start disabled, got %s", got)
	}
}

func TestConnectSuccess(t *testing.T) {
	rec := &dialRecorder{}
	c := New(enabledConfig(), rec.dial, WithClock(fixedClock))
	if err := c.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	st := c.Status()
	if st.State != StateConnected {
		t.Fatalf("state = %s, want connected", st.State)
	}
	if st.NodeID != "node-test" || st.ConsoleURL == "" {
		t.Fatalf("connection identity not recorded: %+v", st)
	}
	if st.Mode != ModeAuto {
		t.Fatalf("auto connect should report mode=auto, got %s", st.Mode)
	}
	if st.OperatorDisconnected {
		t.Fatal("operator_disconnected must be false after connect")
	}
	if st.ConnectedAt.IsZero() || st.LastHeartbeat.IsZero() {
		t.Fatal("timestamps must be set on connect")
	}
	if rec.callCount() != 1 {
		t.Fatalf("dial calls = %d, want 1", rec.callCount())
	}
}

func TestConnectManualReportsManualMode(t *testing.T) {
	rec := &dialRecorder{}
	c := New(enabledConfig(), rec.dial, WithClock(fixedClock))
	if err := c.ConnectManual(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if got := c.Status().Mode; got != ModeManual {
		t.Fatalf("mode = %s, want manual", got)
	}
}

func TestConnectDisabledFailsClosed(t *testing.T) {
	rec := &dialRecorder{}
	c := New(Config{Enabled: false}, rec.dial)
	err := c.Connect(context.Background())
	var ce *ConnectError
	if !errors.As(err, &ce) || ce.Code != ErrCodeDisabled {
		t.Fatalf("expected disabled ConnectError, got %v", err)
	}
	if c.Status().State != StateDisabled {
		t.Fatalf("state = %s, want disabled", c.Status().State)
	}
	if rec.callCount() != 0 {
		t.Fatal("disabled connect must not dial")
	}
}

func TestConnectConfigInvalid(t *testing.T) {
	rec := &dialRecorder{}
	c := New(Config{Enabled: true, URL: "", Token: ""}, rec.dial)
	err := c.Connect(context.Background())
	if got := asConnectError(err).Code; got != ErrCodeConfigInvalid {
		t.Fatalf("code = %s, want config_invalid", got)
	}
	if c.Status().State != StateDisconnected {
		t.Fatalf("invalid config should stay disconnected, got %s", c.Status().State)
	}
	if rec.callCount() != 0 {
		t.Fatal("invalid config must not dial")
	}
}

func TestConnectDialFailureIsRetryableNetwork(t *testing.T) {
	rec := &dialRecorder{err: context.DeadlineExceeded}
	c := New(enabledConfig(), rec.dial)
	err := c.Connect(context.Background())
	ce := asConnectError(err)
	if ce.Code != ErrCodeNetwork || !ce.Retryable {
		t.Fatalf("network failure should be retryable, got %+v", ce)
	}
	st := c.Status()
	if st.State != StateError || !st.Retryable {
		t.Fatalf("state = %s retryable=%v, want error/retryable", st.State, st.Retryable)
	}
}

func TestConnectIdempotentConcurrent(t *testing.T) {
	rec := &dialRecorder{}
	c := New(enabledConfig(), rec.dial)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = c.Connect(context.Background())
		}()
	}
	wg.Wait()
	if rec.callCount() != 1 {
		t.Fatalf("concurrent connect dialed %d times, want 1", rec.callCount())
	}
	if c.Status().State != StateConnected {
		t.Fatalf("state = %s, want connected", c.Status().State)
	}
}

func TestDisconnectSuppressesAutoReconnect(t *testing.T) {
	rec := &dialRecorder{}
	c := New(enabledConfig(), rec.dial)
	if err := c.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := c.Disconnect(context.Background()); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	st := c.Status()
	if st.State != StateDisconnected {
		t.Fatalf("state = %s, want disconnected", st.State)
	}
	if !st.OperatorDisconnected {
		t.Fatal("operator_disconnected must be set after manual disconnect")
	}
	if st.NodeID != "" || st.ConsoleURL != "" {
		t.Fatal("identity must be cleared after disconnect")
	}
	// A late "connection lost" must not resurrect an automatic reconnect.
	c.HandleConnectionLost(ErrCodeNetwork, "dropped", true)
	if got := c.Status().State; got != StateDisconnected {
		t.Fatalf("state after loss = %s, want disconnected (suppressed)", got)
	}
}

func TestHandleConnectionLostRetryableEntersReconnecting(t *testing.T) {
	rec := &dialRecorder{}
	c := New(enabledConfig(), rec.dial)
	if err := c.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	c.HandleConnectionLost(ErrCodeNetwork, "socket closed", true)
	st := c.Status()
	if st.State != StateReconnecting {
		t.Fatalf("state = %s, want reconnecting", st.State)
	}
	if st.ReconnectCount != 1 {
		t.Fatalf("reconnect count = %d, want 1", st.ReconnectCount)
	}
	if !st.Retryable {
		t.Fatal("retryable flag should be set")
	}
}

func TestHandleConnectionLostTerminalStopsRetry(t *testing.T) {
	rec := &dialRecorder{}
	c := New(enabledConfig(), rec.dial)
	if err := c.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	c.HandleConnectionLost(ErrCodeAuthInvalid, "device token revoked", false)
	st := c.Status()
	if st.State != StateError {
		t.Fatalf("state = %s, want error", st.State)
	}
	if st.Retryable {
		t.Fatal("auth failure must not be retryable")
	}
	if st.ErrorCode != ErrCodeAuthInvalid {
		t.Fatalf("error code = %s, want %s", st.ErrorCode, ErrCodeAuthInvalid)
	}
}

func TestReconnectDialsAgain(t *testing.T) {
	rec := &dialRecorder{}
	c := New(enabledConfig(), rec.dial)
	if err := c.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := c.Reconnect(context.Background()); err != nil {
		t.Fatalf("reconnect: %v", err)
	}
	if rec.callCount() != 2 {
		t.Fatalf("dial calls = %d, want 2", rec.callCount())
	}
	st := c.Status()
	if st.State != StateConnected || st.OperatorDisconnected {
		t.Fatalf("reconnect left bad state: %+v", st)
	}
	if st.Mode != ModeManual {
		t.Fatalf("reconnect is operator-initiated, mode = %s", st.Mode)
	}
}

func TestConfigureDisableDisconnects(t *testing.T) {
	rec := &dialRecorder{}
	c := New(enabledConfig(), rec.dial)
	if err := c.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := c.Configure(Config{Enabled: false}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	if got := c.Status().State; got != StateDisabled {
		t.Fatalf("state = %s, want disabled", got)
	}
}

func TestConfigureEnableFromDisabled(t *testing.T) {
	c := New(Config{Enabled: false}, (&dialRecorder{}).dial)
	if err := c.Configure(enabledConfig()); err != nil {
		t.Fatalf("configure: %v", err)
	}
	if got := c.Status().State; got != StateDisconnected {
		t.Fatalf("state = %s, want disconnected", got)
	}
}
