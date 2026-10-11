package agent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"
)

var testUpgrader = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

type fakeGatewayConfig struct {
	rejectAuth   bool
	sendError    bool
	errorCode    string
	heartbeatSec int
	// silent makes the gateway swallow HEARTBEAT frames without acknowledging
	// them (half-open/wedged gateway simulation for the watchdog tests).
	silent bool
}

type fakeGateway struct {
	hello      chan wire.Hello
	register   chan wire.Register
	heartbeats chan struct{}
}

func newFakeGateway(t *testing.T, cfg fakeGatewayConfig) (*httptest.Server, *fakeGateway) {
	t.Helper()
	g := &fakeGateway{
		hello:      make(chan wire.Hello, 1),
		register:   make(chan wire.Register, 1),
		heartbeats: make(chan struct{}, 8),
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cfg.rejectAuth {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		ws, err := testUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()

		helloF, err := readWSFrame(ws)
		if err != nil {
			return
		}
		if h, err := wire.DecodePayload[wire.Hello](helloF); err == nil {
			select {
			case g.hello <- h:
			default:
			}
		}
		_ = writeWSJSON(ws, wire.TypeWelcome, wire.Welcome{ServerVersion: "test", HeartbeatIntervalSec: cfg.heartbeatSec})

		regF, err := readWSFrame(ws)
		if err != nil {
			return
		}
		if reg, err := wire.DecodePayload[wire.Register](regF); err == nil {
			select {
			case g.register <- reg:
			default:
			}
		}
		if cfg.sendError {
			_ = writeWSJSON(ws, wire.TypeError, wire.ErrorMessage{Code: cfg.errorCode, Message: "denied"})
			return
		}
		_ = writeWSJSON(ws, wire.TypeRegisterAck, wire.RegisterAck{
			NodeID:     "node-test",
			ConsoleURL: "https://gw/n/node-test/web/",
		})

		for {
			f, err := readWSFrame(ws)
			if err != nil {
				return
			}
			switch f.Type {
			case wire.TypeHeartbeat:
				select {
				case g.heartbeats <- struct{}{}:
				default:
				}
				if cfg.silent {
					// Half-open simulation: absorb the heartbeat, never ack.
					continue
				}
				_ = writeWSJSON(ws, wire.TypeHeartbeatAck, wire.HeartbeatAck{ServerTime: time.Now().UTC()})
			case wire.TypeDeregister:
				return
			}
		}
	}))
	return srv, g
}

func writeWSFrame(ws *websocket.Conn, f wire.Frame) error {
	data, err := wire.Encode(f)
	if err != nil {
		return err
	}
	return ws.WriteMessage(websocket.BinaryMessage, data)
}

func writeWSJSON(ws *websocket.Conn, t wire.FrameType, v any) error {
	f, err := wire.NewJSONFrame(t, 0, v)
	if err != nil {
		return err
	}
	return writeWSFrame(ws, f)
}

func readWSFrame(ws *websocket.Conn) (wire.Frame, error) {
	_, data, err := ws.ReadMessage()
	if err != nil {
		return wire.Frame{}, err
	}
	return wire.Decode(data)
}

func testConnector() *WSConnector {
	return &WSConnector{
		AgentVersion: "test",
		Register: func() wire.Register {
			return wire.Register{NodeID: "node-local", Kind: "aicli", DeviceName: "laptop", Capabilities: []string{"manifest"}}
		},
	}
}

func TestAgentEndpoint(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"https://gw.example.com", "wss://gw.example.com/gw/agent", false},
		{"http://127.0.0.1:8100", "ws://127.0.0.1:8100/gw/agent", false},
		{"wss://gw.example.com/custom", "wss://gw.example.com/custom", false},
		{"ftp://gw.example.com", "", true},
		{"", "", true},
	}
	for _, tc := range cases {
		got, err := AgentEndpoint(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("AgentEndpoint(%q) expected error", tc.in)
			}
			continue
		}
		if err != nil {
			t.Fatalf("AgentEndpoint(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("AgentEndpoint(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestDialHandshakeAndHeartbeat(t *testing.T) {
	srv, g := newFakeGateway(t, fakeGatewayConfig{heartbeatSec: 1})
	defer srv.Close()

	conn, err := testConnector().Dial(context.Background(), Config{Enabled: true, URL: srv.URL, Token: "agw_x"})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if conn.NodeID() != "node-test" {
		t.Fatalf("node id = %q, want node-test", conn.NodeID())
	}
	if conn.ConsoleURL() != "https://gw/n/node-test/web/" {
		t.Fatalf("console url = %q", conn.ConsoleURL())
	}

	select {
	case h := <-g.hello:
		if h.ProtocolVersion != wire.ProtocolVersion {
			t.Fatalf("hello protocol version = %d", h.ProtocolVersion)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("gateway did not receive HELLO")
	}
	select {
	case reg := <-g.register:
		if reg.NodeID != "node-local" || reg.DeviceName != "laptop" {
			t.Fatalf("register payload = %+v", reg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("gateway did not receive REGISTER")
	}
	select {
	case <-g.heartbeats:
	case <-time.After(3 * time.Second):
		t.Fatal("gateway did not receive HEARTBEAT")
	}
}

func TestDialAuthFailureIsTerminal(t *testing.T) {
	srv, _ := newFakeGateway(t, fakeGatewayConfig{rejectAuth: true})
	defer srv.Close()

	_, err := testConnector().Dial(context.Background(), Config{Enabled: true, URL: srv.URL, Token: "agw_x"})
	var ce *ConnectError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ConnectError, got %v", err)
	}
	if ce.Code != ErrCodeAuthInvalid {
		t.Fatalf("code = %s, want %s", ce.Code, ErrCodeAuthInvalid)
	}
	if ce.Retryable {
		t.Fatal("auth failure must be terminal (non-retryable)")
	}
}

func TestDialGatewayErrorFrameIsTerminal(t *testing.T) {
	srv, _ := newFakeGateway(t, fakeGatewayConfig{sendError: true, errorCode: wire.CodeAuthInvalid})
	defer srv.Close()

	_, err := testConnector().Dial(context.Background(), Config{Enabled: true, URL: srv.URL, Token: "agw_x"})
	var ce *ConnectError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ConnectError, got %v", err)
	}
	if ce.Code != wire.CodeAuthInvalid || ce.Retryable {
		t.Fatalf("gateway ERROR should be terminal: %+v", ce)
	}
}

func TestDialMissingConfig(t *testing.T) {
	_, err := testConnector().Dial(context.Background(), Config{Enabled: true})
	var ce *ConnectError
	if !errors.As(err, &ce) || ce.Code != ErrCodeConfigInvalid {
		t.Fatalf("expected config_invalid, got %v", err)
	}
}

func TestControllerWithWSConnector(t *testing.T) {
	srv, _ := newFakeGateway(t, fakeGatewayConfig{heartbeatSec: 1})
	defer srv.Close()

	c := New(Config{Enabled: true, URL: srv.URL, Token: "agw_x", AutoConnect: true}, testConnector().Dial)
	if err := c.Connect(context.Background()); err != nil {
		t.Fatalf("controller connect: %v", err)
	}
	defer func() { _ = c.Disconnect(context.Background()) }()

	st := c.Status()
	if st.State != StateConnected {
		t.Fatalf("state = %s, want connected", st.State)
	}
	if st.NodeID != "node-test" || st.ConsoleURL == "" {
		t.Fatalf("identity not propagated to status: %+v", st)
	}
}
