package tunnel

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"
)

func TestRestoreMarksStaleAndReconnectClears(t *testing.T) {
	reg := NewRegistry()
	n := restoredNode("node-a", time.Now().Add(-time.Hour))
	if got := reg.Restore([]*Node{n}); got != 1 {
		t.Fatalf("Restore = %d, want 1", got)
	}
	got, ok := reg.Get("node-a")
	if !ok || !got.Stale {
		t.Fatalf("restored node missing or not stale: %+v", got)
	}
	if len(got.Sessions) != 1 || got.Sessions[0].Attention != "approval" {
		t.Fatalf("restored sessions = %+v", got.Sessions)
	}

	// Empty node ids are skipped.
	if got := reg.Restore([]*Node{{Name: "x"}}); got != 0 {
		t.Fatalf("Restore with empty NodeID = %d, want 0", got)
	}

	// A fresh registration replaces the entry and clears the stale flag.
	live := &Node{NodeID: "node-a", Name: "live", LastHeartbeat: time.Now()}
	reg.Upsert(live)
	got, _ = reg.Get("node-a")
	if got.Stale || got.Name != "live" {
		t.Fatalf("reconnect did not clear stale: %+v", got)
	}
}

func TestPersisterRoundTrip(t *testing.T) {
	dir := t.TempDir()
	reg := NewRegistry()
	reg.Restore([]*Node{restoredNode("node-a", time.Now())})

	p := NewPersister(reg, dir, 20*time.Millisecond)
	defer p.Close()
	p.Schedule()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(dir, persistPath)); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	snap := LoadSnapshot(dir)
	if len(snap) != 1 || snap[0].NodeID != "node-a" || len(snap[0].Sessions) != 1 {
		t.Fatalf("snapshot = %+v", snap)
	}

	// Last change wins: the flush after the debounce window reflects the final state.
	reg.Remove("node-a")
	p.Schedule()
	time.Sleep(120 * time.Millisecond)
	if snap := LoadSnapshot(dir); len(snap) != 0 {
		t.Fatalf("snapshot after remove = %+v", snap)
	}
}

// TestPersisterTrimsStatusTail pins D3: the ephemeral live-output tail never
// reaches registry.json, and trimming the snapshot copy does not clear the
// live registry (the SSE `event: session` stream keeps its tail).
func TestPersisterTrimsStatusTail(t *testing.T) {
	dir := t.TempDir()
	reg := NewRegistry()
	reg.Upsert(&Node{NodeID: "node-a", Sessions: []wire.SessionSummary{{
		SessionID:  "s1",
		StatusTail: "live-tail-must-not-persist",
	}}})

	p := NewPersister(reg, dir, 20*time.Millisecond)
	defer p.Close()
	p.Schedule()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(dir, persistPath)); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	snap := LoadSnapshot(dir)
	if len(snap) != 1 || len(snap[0].Sessions) != 1 {
		t.Fatalf("snapshot = %+v", snap)
	}
	if got := snap[0].Sessions[0].StatusTail; got != "" {
		t.Fatalf("snapshot must trim StatusTail, got %q", got)
	}
	if live, _ := reg.Get("node-a"); live.Sessions[0].StatusTail != "live-tail-must-not-persist" {
		t.Fatalf("live registry must keep the tail, got %q", live.Sessions[0].StatusTail)
	}
}

func TestLoadSnapshotMissingOrCorrupt(t *testing.T) {
	if got := LoadSnapshot(t.TempDir()); got != nil {
		t.Fatalf("missing snapshot = %+v", got)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, persistPath), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := LoadSnapshot(dir); got != nil {
		t.Fatalf("corrupt snapshot = %+v", got)
	}
	if got := LoadSnapshot(""); got != nil {
		t.Fatalf("empty dir = %+v", got)
	}
}

// restoredNode builds a persisted-style node with one attentive session.
func restoredNode(id string, hb time.Time) *Node {
	return &Node{
		NodeID:        id,
		Name:          "laptop",
		LastHeartbeat: hb,
		Sessions: []wire.SessionSummary{
			{SessionID: "s1", Title: "one", Attention: "approval"},
		},
	}
}
