package subagentquota

import (
	"context"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
)

type fakeRegistry struct {
	records []agentcontrol.AgentRecord
}

func (f fakeRegistry) ListAgentControlAgents(_ context.Context, filter agentcontrol.AgentFilter) ([]agentcontrol.AgentRecord, error) {
	out := make([]agentcontrol.AgentRecord, 0, len(f.records))
	for _, record := range f.records {
		record = record.Normalize()
		if filter.RootSessionID != "" && record.RootSessionID != filter.RootSessionID {
			continue
		}
		out = append(out, record)
	}
	return out, nil
}

type fakeCounter struct {
	n int
}

func (f fakeCounter) CountLiveTasks(_ context.Context, _ subagentbatch.LiveTaskFilter) (int, error) {
	return f.n, nil
}

func activeChild(id string) agentcontrol.AgentRecord {
	return agentcontrol.AgentRecord{
		AgentID:       id,
		RootSessionID: "root",
		SessionID:     id,
		AgentPath:     "/root/" + id,
		AgentType:     agentcontrol.AgentTypeChild,
		Status:        agentcontrol.AgentStatusActive,
	}
}

// TestGateRejectsWhenLivePlusRequestedExceedsCeiling pins the core arithmetic:
// live registry children + live batch tasks + the requested batch size must fit
// under agents.maxThreads, and the rejection carries the shared next_action.
func TestGateRejectsWhenLivePlusRequestedExceedsCeiling(t *testing.T) {
	registry := fakeRegistry{records: []agentcontrol.AgentRecord{activeChild("a"), activeChild("b")}}
	gate := newWithCounter(registry, fakeCounter{n: 3}, 6, 6)
	if gate == nil {
		t.Fatal("gate must be built for a finite ceiling with a live counter")
	}

	// live = 2 (registry) + 3 (batch) = 5; 5+2 = 7 > 6 -> reject.
	err := gate.AdmitSubagentBatch(context.Background(), "sess", "root", 2)
	if err == nil {
		t.Fatal("expected the batch to be rejected over the live ceiling")
	}
	msg := err.Error()
	for _, want := range []string{"max_threads=6", "active_children=5", "batch_requested=2", "next_action=", agentcontrol.ThreadLimitNextAction} {
		if !strings.Contains(msg, want) {
			t.Fatalf("diagnostic %q missing %q", msg, want)
		}
	}

	// live + 1 = 6 <= 6 -> admit.
	if err := gate.AdmitSubagentBatch(context.Background(), "sess", "root", 1); err != nil {
		t.Fatalf("a batch that fits the ceiling must be admitted: %v", err)
	}
}

// TestGateIgnoresTerminalRegistryRows pins that closed/stale/root rows never
// hold a slot (the same filter the spawn_agent gate uses).
func TestGateIgnoresTerminalRegistryRows(t *testing.T) {
	closed := activeChild("closed")
	closed.Status = agentcontrol.AgentStatusClosed
	stale := activeChild("stale")
	stale.Status = agentcontrol.AgentStatusStale
	root := agentcontrol.AgentRecord{
		AgentID:       "root-row",
		RootSessionID: "root",
		AgentPath:     "/root",
		AgentType:     agentcontrol.AgentTypeRoot,
		Status:        agentcontrol.AgentStatusActive,
	}
	registry := fakeRegistry{records: []agentcontrol.AgentRecord{closed, stale, root}}
	gate := newWithCounter(registry, fakeCounter{n: 0}, 1, 1)
	if gate == nil {
		t.Fatal("gate must be built")
	}
	if err := gate.AdmitSubagentBatch(context.Background(), "sess", "root", 1); err != nil {
		t.Fatalf("terminal/root rows must not occupy a slot: %v", err)
	}
}

// TestGateUnlimitedAndDegenerateConfigs pins the off switches: an explicit -1
// disables the ceiling, and a store without a live counter yields a nil gate.
func TestGateUnlimitedAndDegenerateConfigs(t *testing.T) {
	if gate := newWithCounter(fakeRegistry{}, fakeCounter{n: 100}, -1, 6); gate != nil {
		t.Fatal("agents.maxThreads=-1 must disable the gate")
	}
	if gate := newWithCounter(fakeRegistry{}, nil, 6, 6); gate != nil {
		t.Fatal("a store without a live-task counter must yield a nil gate")
	}
	// A nil gate is a valid no-op.
	var nilGate *Gate
	if err := nilGate.AdmitSubagentBatch(context.Background(), "sess", "root", 99); err != nil {
		t.Fatalf("nil gate must admit: %v", err)
	}
}

// TestGateScopeFallbackUsesRootThenParent pins the scope resolution: rootScope
// wins, parent is the fallback, and an unscoped call admits rather than blocks.
func TestGateScopeFallbackUsesRootThenParent(t *testing.T) {
	gate := newWithCounter(fakeRegistry{}, fakeCounter{n: 6}, 6, 6)
	if gate == nil {
		t.Fatal("gate must be built")
	}
	// No scope at all: admit.
	if err := gate.AdmitSubagentBatch(context.Background(), "", "", 5); err != nil {
		t.Fatalf("unscoped admission must not block: %v", err)
	}
	// Zero requested tasks: admit.
	if err := gate.AdmitSubagentBatch(context.Background(), "sess", "root", 0); err != nil {
		t.Fatalf("empty batch must not block: %v", err)
	}
	// live already at the ceiling -> any new task is rejected.
	if err := gate.AdmitSubagentBatch(context.Background(), "sess", "root", 1); err == nil {
		t.Fatal("expected rejection when live already equals the ceiling")
	}
}
