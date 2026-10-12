package tunnel

import (
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"
)

// TestStampSyncedAtHelpers pins the gateway-stamping rules: SyncedAt is
// server-owned metadata, stamping never counts as a visible change (so a
// heartbeat cannot trigger broadcasts/persist), and digest merging itself
// does not stamp (the server stamps separately, outside change detection).
func TestStampSyncedAtHelpers(t *testing.T) {
	node := &Node{Sessions: []wire.SessionSummary{
		{SessionID: "s1", State: wire.SessionStateIdle},
		{SessionID: "s2", State: wire.SessionStateIdle},
	}}

	t1 := time.Now().Add(-2 * time.Minute)
	stampSessionSynced(node, "s1", t1)
	if !node.Sessions[0].SyncedAt.Equal(t1) {
		t.Fatalf("s1 not stamped: %+v", node.Sessions[0])
	}
	if !node.Sessions[1].SyncedAt.IsZero() {
		t.Fatalf("s2 must stay unstamped: %+v", node.Sessions[1])
	}

	t2 := time.Now().Add(-time.Minute)
	stampDigestSessions(node, []wire.SessionDigest{{SessionID: "s2"}, {SessionID: "missing"}}, t2)
	if !node.Sessions[1].SyncedAt.Equal(t2) {
		t.Fatalf("s2 not stamped by digest: %+v", node.Sessions[1])
	}

	// Digest merge alone must not stamp (caller stamps outside detection).
	before := node.Sessions[1].SyncedAt
	if changed, _ := applyHeartbeatDigests(node, []wire.SessionDigest{{
		SessionID: "s2", State: wire.SessionStateIdle,
	}}, time.Now()); changed {
		t.Fatal("identical digest must not report a change")
	}
	if !node.Sessions[1].SyncedAt.Equal(before) {
		t.Fatal("digest merge must not stamp SyncedAt; the server stamps separately")
	}

	// Stamping is invisible to change detection even when the value moves.
	node.Sessions[0].SyncedAt = time.Now()
	if changed, _ := applyHeartbeatDigests(node, []wire.SessionDigest{{
		SessionID: "s1", State: wire.SessionStateIdle,
	}}, time.Now()); changed {
		t.Fatal("a fresh SyncedAt must not count as a visible change")
	}

	t3 := time.Now()
	stampAllSessionsSynced(node, t3)
	for i := range node.Sessions {
		if !node.Sessions[i].SyncedAt.Equal(t3) {
			t.Fatalf("session %d not stamped: %+v", i, node.Sessions[i])
		}
	}
}

// TestApplySessionUpdateDeltaPath pins the P1 broadcast split: with
// OnSessionChanged wired, session updates emit per-session deltas (plus the
// touch callback for the push notifier) and only structural removals fall
// back to the full snapshot broadcast.
func TestApplySessionUpdateDeltaPath(t *testing.T) {
	reg := NewRegistry()
	reg.Upsert(&Node{NodeID: "n1"})
	var full, touch int
	var deltas []string
	var transitions []string
	s := &Server{
		cfg: Config{
			OnRegistryChanged: func() { full++ },
			OnRegistryTouch:   func() { touch++ },
			OnSessionChanged: func(nodeID, sessionID string) {
				deltas = append(deltas, nodeID+"|"+sessionID)
			},
			OnSessionTransition: func(nodeID, sessionID, state, attention string) {
				transitions = append(transitions, nodeID+"|"+sessionID+"|"+state+"|"+attention)
			},
		},
		registry: reg,
	}

	// Upsert → delta only.
	s.applySessionUpdate("n1", wire.SessionUpdate{
		Op:      wire.SessionOpUpsert,
		Session: &wire.SessionSummary{SessionID: "s1", State: wire.SessionStateRunning},
	})
	if len(deltas) != 1 || deltas[0] != "n1|s1" || full != 0 || touch != 1 {
		t.Fatalf("upsert path: deltas=%v full=%d touch=%d", deltas, full, touch)
	}
	if node, _ := reg.Get("n1"); len(node.Sessions) != 1 || node.Sessions[0].SyncedAt.IsZero() {
		t.Fatalf("upsert must store+stamp: %+v", node)
	}
	if len(transitions) != 1 || transitions[0] != "n1|s1|running|" {
		t.Fatalf("upsert transition = %v", transitions)
	}
	if node, _ := reg.Get("n1"); node.Sessions[0].StateChangedAt.IsZero() {
		t.Fatal("upsert must stamp StateChangedAt for a new session")
	}

	// Reconcile without removals → deltas for every session, no full snapshot.
	s.applySessionUpdate("n1", wire.SessionUpdate{
		Op: wire.SessionOpReconcile,
		Sessions: []wire.SessionSummary{
			{SessionID: "s1", State: wire.SessionStateIdle},
			{SessionID: "s2", State: wire.SessionStateIdle},
		},
	})
	if len(deltas) != 3 || full != 0 || touch != 2 {
		t.Fatalf("reconcile path: deltas=%v full=%d touch=%d", deltas, full, touch)
	}
	// s1 running→idle and s2 (new) both produce transitions.
	if len(transitions) != 3 || transitions[1] != "n1|s1|idle|" || transitions[2] != "n1|s2|idle|" {
		t.Fatalf("reconcile transitions = %v", transitions)
	}

	// Reconcile that drops a session → full snapshot (structural change).
	s.applySessionUpdate("n1", wire.SessionUpdate{
		Op:       wire.SessionOpReconcile,
		Sessions: []wire.SessionSummary{{SessionID: "s2", State: wire.SessionStateIdle}},
	})
	if full != 1 || touch != 3 {
		t.Fatalf("removal path: deltas=%v full=%d touch=%d", deltas, full, touch)
	}
	// s2 is unchanged in the removal reconcile: no extra transition.
	if len(transitions) != 3 {
		t.Fatalf("removal must not emit transitions: %v", transitions)
	}
}

// TestWatchActions pins the P1-b heartbeat diff: watch/unwatch actions are
// emitted only for real transitions and are deterministically sorted.
func TestWatchActions(t *testing.T) {
	actions, next := watchActions(
		map[string]bool{"a": true, "b": true},
		map[string]bool{"b": true, "c": true},
	)
	if len(actions) != 2 || actions[0] != "unwatch:c" || actions[1] != "watch:a" {
		t.Fatalf("actions = %v", actions)
	}
	if !next["a"] || !next["b"] || next["c"] {
		t.Fatalf("next = %v", next)
	}

	actions, next = watchActions(map[string]bool{"a": true}, map[string]bool{"a": true})
	if len(actions) != 0 || !next["a"] {
		t.Fatalf("no-op diff: actions=%v next=%v", actions, next)
	}

	actions, _ = watchActions(nil, map[string]bool{"a": true})
	if len(actions) != 1 || actions[0] != "unwatch:a" {
		t.Fatalf("release-all actions = %v", actions)
	}
}
