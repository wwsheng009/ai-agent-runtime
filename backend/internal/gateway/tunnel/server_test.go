package tunnel_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/tunnel"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"
)

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met within deadline")
}

func newTunnelServer(t *testing.T, cfg tunnel.Config) (*httptest.Server, *tunnel.Registry) {
	t.Helper()
	if cfg.Registry == nil {
		cfg.Registry = tunnel.NewRegistry()
	}
	server := tunnel.NewServer(cfg)
	srv := httptest.NewServer(http.HandlerFunc(server.ServeHTTP))
	t.Cleanup(srv.Close)
	return srv, cfg.Registry
}

func testConnector(nodeID string) *agent.WSConnector {
	return &agent.WSConnector{
		AgentVersion: "test",
		Register: func() wire.Register {
			return wire.Register{
				NodeID:       nodeID,
				Kind:         "aicli",
				DeviceName:   "laptop",
				Capabilities: []string{"manifest"},
				Routes:       []string{"/"},
			}
		},
		HeartbeatInterval: 200 * time.Millisecond,
	}
}

func TestHandshakeRegistersAndDeregisters(t *testing.T) {
	srv, registry := newTunnelServer(t, tunnel.Config{ServerVersion: "test", PublicBaseURL: "https://gw.example.com"})

	conn, err := testConnector("node-local").Dial(context.Background(), agent.Config{Enabled: true, URL: srv.URL, Token: "agw_x"})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if conn.NodeID() != "node-local" {
		t.Fatalf("node id = %q, want node-local", conn.NodeID())
	}
	if conn.ConsoleURL() != "https://gw.example.com/n/node-local/web/" {
		t.Fatalf("console url = %q", conn.ConsoleURL())
	}

	waitFor(t, func() bool {
		node, ok := registry.Get("node-local")
		return ok && node.DeviceName == "laptop" && node.Kind == "aicli"
	})

	_ = conn.Close()
	waitFor(t, func() bool {
		// Disconnect downgrades the entry to a stale last-known snapshot
		// (docs/gateway/08-notifications.md §10.1) rather than deleting it: the
		// console keeps the node/session view and the persisted snapshot feeds
		// a restart/failover restore. A reconnect flips it back to live.
		node, ok := registry.Get("node-local")
		return ok && node.Stale
	})
}

func TestHeartbeatAdvancesRegistryTimestamp(t *testing.T) {
	srv, registry := newTunnelServer(t, tunnel.Config{ServerVersion: "test"})
	conn, err := testConnector("node-hb").Dial(context.Background(), agent.Config{Enabled: true, URL: srv.URL, Token: "agw_x"})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	var first time.Time
	waitFor(t, func() bool {
		node, ok := registry.Get("node-hb")
		if !ok {
			return false
		}
		first = node.LastHeartbeat
		return true
	})
	waitFor(t, func() bool {
		node, ok := registry.Get("node-hb")
		return ok && node.LastHeartbeat.After(first)
	})
}

func TestGeneratedNodeID(t *testing.T) {
	srv, _ := newTunnelServer(t, tunnel.Config{ServerVersion: "test"})
	conn, err := testConnector("").Dial(context.Background(), agent.Config{Enabled: true, URL: srv.URL, Token: "agw_x"})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	if !strings.HasPrefix(conn.NodeID(), "node-aicli-") {
		t.Fatalf("generated node id = %q, want node-aicli- prefix", conn.NodeID())
	}
}

func TestAuthenticateRejectsUpgrade(t *testing.T) {
	srv, _ := newTunnelServer(t, tunnel.Config{
		ServerVersion: "test",
		Authenticate:  func(*http.Request) error { return errors.New("bad token") },
	})
	_, err := testConnector("node-x").Dial(context.Background(), agent.Config{Enabled: true, URL: srv.URL, Token: "agw_bad"})
	var ce *agent.ConnectError
	if !errors.As(err, &ce) || ce.Code != agent.ErrCodeAuthInvalid {
		t.Fatalf("expected auth_invalid ConnectError, got %v", err)
	}
}

func TestRegistryListStable(t *testing.T) {
	registry := tunnel.NewRegistry()
	registry.Upsert(&tunnel.Node{NodeID: "node-b"})
	registry.Upsert(&tunnel.Node{NodeID: "node-a"})
	list := registry.List()
	if len(list) != 2 || list[0].NodeID != "node-a" || list[1].NodeID != "node-b" {
		t.Fatalf("registry list not sorted: %+v", list)
	}
	registry.Remove("node-a")
	if _, ok := registry.Get("node-a"); ok {
		t.Fatal("Remove did not delete the node")
	}
}
