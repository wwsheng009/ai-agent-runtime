package tunnel_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/tunnel"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"
)

// TestNotifyUpdatesRegistryAttention drives a NOTIFY frame end-to-end: the agent
// emits it, the gateway folds it into the registry attention without clobbering
// the SESSION_UPDATE fields, and honors the dedup key.
func TestNotifyUpdatesRegistryAttention(t *testing.T) {
	server := tunnel.NewServer(tunnel.Config{ServerVersion: "test"})
	tunnelHTTP := httptest.NewServer(http.HandlerFunc(server.ServeHTTP))
	t.Cleanup(tunnelHTTP.Close)

	controller := agent.NewController(agent.Config{Enabled: true, URL: tunnelHTTP.URL, Token: "agw_x"}, testRegisterFunc("node-notify"))
	if err := controller.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = controller.Disconnect(context.Background()) })
	waitFor(t, func() bool { _, ok := server.Registry().Get("node-notify"); return ok })

	controller.UpdateSessions([]wire.SessionSummary{{SessionID: "s1", Title: "one", State: "running"}})
	waitFor(t, func() bool {
		node, ok := server.Registry().Get("node-notify")
		return ok && len(node.Sessions) == 1
	})

	controller.SendNotify(wire.Notify{SessionID: "s1", Kind: wire.NotifyKindApproval, Priority: wire.NotifyPriorityHigh, Title: "one"})
	waitFor(t, func() bool {
		node, _ := server.Registry().Get("node-notify")
		return len(node.Sessions) == 1 && node.Sessions[0].Attention == "approval"
	})

	// The first event with dedup key "turn-1" applies.
	controller.SendNotify(wire.Notify{SessionID: "s1", Kind: wire.NotifyKindTurnCompleted, DedupKey: "turn-1"})
	waitFor(t, func() bool {
		node, _ := server.Registry().Get("node-notify")
		return len(node.Sessions) == 1 && node.Sessions[0].Attention == "completed"
	})

	// A second event reusing the key must be dropped, so attention stays put.
	controller.SendNotify(wire.Notify{SessionID: "s1", Kind: wire.NotifyKindApproval, DedupKey: "turn-1"})
	time.Sleep(150 * time.Millisecond)
	node, _ := server.Registry().Get("node-notify")
	if node.Sessions[0].Attention != "completed" {
		t.Fatalf("dedup key was not honored: attention = %q", node.Sessions[0].Attention)
	}
	if node.Sessions[0].State != "running" {
		t.Fatalf("applyNotify clobbered state: %q", node.Sessions[0].State)
	}
}
