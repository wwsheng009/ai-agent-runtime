package tunnel

import (
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"
)

func TestApplyHeartbeatDigests(t *testing.T) {
	merge := func(node *Node, digests []wire.SessionDigest) (bool, []sessionTransition) {
		return applyHeartbeatDigests(node, digests, time.Now())
	}
	node := &Node{Sessions: []wire.SessionSummary{
		{SessionID: "s1", State: wire.SessionStateIdle},
	}}

	// No digests / no node: nothing to do.
	if changed, _ := merge(nil, []wire.SessionDigest{{SessionID: "s1"}}); changed {
		t.Fatal("nil node must not report a change")
	}
	if changed, _ := merge(node, nil); changed {
		t.Fatal("empty digests must not report a change")
	}

	// An identical digest is the common heartbeat: no broadcast, no transition.
	if changed, transitions := merge(node, []wire.SessionDigest{{
		SessionID: "s1", State: wire.SessionStateIdle,
	}}); changed || len(transitions) != 0 {
		t.Fatalf("identical digest: changed=%v transitions=%v", changed, transitions)
	}

	// A real transition updates state, stamps StateChangedAt and reports one
	// transition. StatusText is owned by the ordered event stream and must NOT
	// be overwritten by a (possibly stale) digest snapshot.
	node.Sessions[0].StatusText = "Generating response"
	updatedAt := time.Now()
	digest := wire.SessionDigest{
		SessionID:  "s1",
		State:      wire.SessionStateRunning,
		StatusText: "Running shell",
		UpdatedAt:  updatedAt,
	}
	before := time.Now()
	changed, transitions := merge(node, []wire.SessionDigest{digest})
	if !changed {
		t.Fatal("state transition must report a change")
	}
	if len(transitions) != 1 || transitions[0].SessionID != "s1" || transitions[0].State != wire.SessionStateRunning {
		t.Fatalf("transitions = %+v", transitions)
	}
	if got := node.Sessions[0]; got.State != wire.SessionStateRunning || !got.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("merged session = %+v", got)
	}
	if got := node.Sessions[0]; got.StateChangedAt.IsZero() || got.StateChangedAt.Before(before) {
		t.Fatalf("StateChangedAt not stamped: %+v", got.StateChangedAt)
	}
	if got := node.Sessions[0].StatusText; got != "Generating response" {
		t.Fatalf("digest must not overwrite StatusText: %q", got)
	}
	// Replaying the same digest: no change and the stamp is preserved.
	stampedAt := node.Sessions[0].StateChangedAt
	if changed, transitions := merge(node, []wire.SessionDigest{digest}); changed || len(transitions) != 0 {
		t.Fatalf("replayed digest: changed=%v transitions=%v", changed, transitions)
	}
	if !node.Sessions[0].StateChangedAt.Equal(stampedAt) {
		t.Fatal("unchanged state must keep the previous StateChangedAt")
	}

	// Attention clearing is a change and a transition; unknown sessions are ignored.
	node.Sessions[0].Attention = "approval"
	changed, transitions = merge(node, []wire.SessionDigest{{SessionID: "s1"}})
	if !changed || len(transitions) != 1 || transitions[0].Attention != "" {
		t.Fatalf("attention clearing: changed=%v transitions=%v", changed, transitions)
	}
	if !node.Sessions[0].AttentionChangedAt.After(time.Time{}) {
		t.Fatal("AttentionChangedAt must be stamped")
	}
	if changed, _ := merge(node, []wire.SessionDigest{{SessionID: "other", State: wire.SessionStateRunning}}); changed {
		t.Fatal("unknown session must be ignored")
	}
}
