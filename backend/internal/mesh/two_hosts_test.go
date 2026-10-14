package mesh

import (
	"testing"
	"time"
)

// TestTwoHostsSeeEachOther verifies the P2 slice-A/B mesh contract end to end
// with real node records on disk (docs/bots/09 §7.5): two hosts started in the
// same mesh root with different kinds must see each other as live peers with
// endpoints — the addressing precondition for cross-host connector dispatch
// and attention fan-out.
func TestTwoHostsSeeEachOther(t *testing.T) {
	paths := testMeshPaths(t)

	hostA := NewHost(HostConfig{
		Kind:          "runtime-server",
		Origin:        "test",
		WorkspacePath: paths.Root,
		Capabilities:  []string{"mesh", "runner", "poller"},
		Now:           time.Now().UTC,
	})
	if hostA == nil {
		t.Fatal("hostA is nil")
	}
	aNodeID := hostA.NodeID()
	defer hostA.Stop(t.Context(), StopRequest{Target: aNodeID})
	if err := hostA.Start(); err != nil {
		t.Fatalf("hostA start: %v", err)
	}
	aNodeID = hostA.NodeID() // Start 后才分配（reserveNodeIDLocked）

	hostB := NewHost(HostConfig{
		Kind:          "chat",
		Origin:        "test",
		WorkspacePath: paths.Root,
		Capabilities:  []string{"mesh", "runner"},
		Now:           time.Now().UTC,
	})
	if hostB == nil {
		t.Fatal("hostB is nil")
	}
	bNodeID := hostB.NodeID()
	defer hostB.Stop(t.Context(), StopRequest{Target: bNodeID})
	if err := hostB.Start(); err != nil {
		t.Fatalf("hostB start: %v", err)
	}
	bNodeID = hostB.NodeID()

	// Endpoint on A so peers can address it (dispatch/fan-out precondition).
	hostA.SetEndpoint(EndpointInfo{
		Scheme: "http", Host: "127.0.0.1", Port: 8101,
		BaseURL: "http://127.0.0.1:8101", WebBaseURL: "http://127.0.0.1:8101/web",
	}, AuthInfo{})

	view := BuildView(paths, ViewOptions{})
	var sawA bool
	for _, node := range view.Nodes {
		if node.NodeID != aNodeID {
			continue
		}
		sawA = true
		if node.Kind != "runtime-server" {
			t.Fatalf("hostA kind = %q, want runtime-server", node.Kind)
		}
		if node.State != NodeStateLive {
			t.Fatalf("hostA state = %v, want live", node.State)
		}
		if node.Endpoint == nil || node.Endpoint.BaseURL != "http://127.0.0.1:8101" {
			t.Fatalf("hostA endpoint = %+v", node.Endpoint)
		}
		if node.Record == nil {
			t.Fatal("hostA record must be present for in-process readers")
		}
		if !HasCapability(node.Record.Capabilities, "runner") ||
			!HasCapability(node.Record.Capabilities, "poller") {
			t.Fatalf("hostA capabilities = %v, want runner+poller", node.Record.Capabilities)
		}
	}
	if !sawA {
		t.Fatalf("hostB view does not contain hostA %s; nodes = %d", aNodeID, len(view.Nodes))
	}

	// Reverse direction: A's view contains B (chat kind, live).
	viewA := BuildView(paths, ViewOptions{})
	var sawB bool
	for _, node := range viewA.Nodes {
		if node.NodeID == bNodeID && node.State == NodeStateLive && node.Kind == "chat" {
			sawB = true
		}
	}
	if !sawB {
		t.Fatal("hostA view does not contain live hostB")
	}
}
