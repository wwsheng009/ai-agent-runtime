package tunnel_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/tunnel"
)

// TestCloseAllSignalsGoAwayAndMarksStale covers the active/passive failover
// handoff: CloseAll delivers GOAWAY to every live agent, the agent closes (its
// host then reconnects to the next gateway), and the registry keeps the entry
// as a stale snapshot — a graceful shutdown must leave the same last-known
// view a crash would, so the standby's restore path is identical.
func TestCloseAllSignalsGoAwayAndMarksStale(t *testing.T) {
	server := tunnel.NewServer(tunnel.Config{ServerVersion: "test"})
	tunnelHTTP := httptest.NewServer(http.HandlerFunc(server.ServeHTTP))
	t.Cleanup(tunnelHTTP.Close)

	controller := agent.NewController(agent.Config{Enabled: true, URL: tunnelHTTP.URL, Token: "agw_x"}, testRegisterFunc("node-drain"))
	if err := controller.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = controller.Disconnect(context.Background()) })
	waitFor(t, func() bool {
		_, ok := server.Registry().Get("node-drain")
		return ok && server.Registry().CountLive() == 1
	})

	server.CloseAll("failover drill")

	// The agent connection drops; the entry remains as a stale snapshot.
	waitFor(t, func() bool {
		node, ok := server.Registry().Get("node-drain")
		return ok && node.Stale && server.Registry().CountLive() == 0
	})

	// A reconnecting agent flips the entry back to live (same node id).
	controller2 := agent.NewController(agent.Config{Enabled: true, URL: tunnelHTTP.URL, Token: "agw_x"}, testRegisterFunc("node-drain"))
	if err := controller2.Connect(context.Background()); err != nil {
		t.Fatalf("reconnect after CloseAll: %v", err)
	}
	t.Cleanup(func() { _ = controller2.Disconnect(context.Background()) })
	waitFor(t, func() bool {
		node, ok := server.Registry().Get("node-drain")
		return ok && !node.Stale && server.Registry().CountLive() == 1
	})
}

func TestRegistryCountLive(t *testing.T) {
	reg := tunnel.NewRegistry()
	if reg.CountLive() != 0 {
		t.Fatal("empty registry must have zero live nodes")
	}
}
