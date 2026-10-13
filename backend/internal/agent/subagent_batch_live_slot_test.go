package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
)

type stubLiveSlotGate struct {
	err           error
	calls         int
	lastRequested int
	lastRoot      string
}

func (g *stubLiveSlotGate) AdmitSubagentBatch(_ context.Context, _, rootScopeID string, requested int) error {
	g.calls++
	g.lastRequested = requested
	g.lastRoot = rootScopeID
	return g.err
}

func newGateCoordinator(t *testing.T, store subagentbatch.BatchStore, gate SubagentLiveSlotGate, exec subagentExecutor) *SubagentBatchCoordinator {
	t.Helper()
	return &SubagentBatchCoordinator{
		store:        store,
		executor:     exec,
		deadline:     time.Minute,
		cancels:      make(map[string]context.CancelFunc),
		owned:        make(map[string]struct{}),
		liveSlotGate: gate,
	}
}

// TestStartBackgroundRejectsWhenLiveSlotGateDenies pins the batch-path slot
// ceiling: a denied admission must fail before any durable row is written, so a
// rejected batch leaves no phantom batch/task record behind.
func TestStartBackgroundRejectsWhenLiveSlotGateDenies(t *testing.T) {
	store := testStore(t)
	gate := &stubLiveSlotGate{err: errString("agent spawn thread limit reached: max_threads=6 active_children=6")}
	c := newGateCoordinator(t, store, gate, &fakeExecutor{})

	_, err := c.StartBackground(context.Background(), BatchStartOptions{
		ParentSessionID: "session-1",
		RootScopeID:     "root-1",
	}, []SubagentTask{{ID: "t1", Goal: "g1"}, {ID: "t2", Goal: "g2"}})
	if err == nil {
		t.Fatal("expected StartBackground to reject an over-limit batch")
	}
	if !strings.Contains(err.Error(), "thread limit reached") {
		t.Fatalf("error = %v, want the gate diagnostic", err)
	}
	if gate.calls != 1 || gate.lastRequested != 2 || gate.lastRoot != "root-1" {
		t.Fatalf("gate calls=%d requested=%d root=%q, want 1/2/root-1", gate.calls, gate.lastRequested, gate.lastRoot)
	}

	batches, listErr := store.ListBatches(context.Background(), subagentbatch.BatchFilter{})
	if listErr != nil {
		t.Fatalf("ListBatches: %v", listErr)
	}
	if len(batches) != 0 {
		t.Fatalf("a rejected batch must not be persisted, got %d batches", len(batches))
	}
}

// TestStartBackgroundAdmitsWhenLiveSlotGateAllows pins the happy path: an
// admitting gate does not change batch creation.
func TestStartBackgroundAdmitsWhenLiveSlotGateAllows(t *testing.T) {
	store := testStore(t)
	gate := &stubLiveSlotGate{}
	exec := &fakeExecutor{results: []SubagentResult{{ID: "t1", Success: true, Summary: "ok"}}}
	c := newGateCoordinator(t, store, gate, exec)

	batch, err := c.StartBackground(context.Background(), BatchStartOptions{
		ParentSessionID: "session-1",
		RootScopeID:     "root-1",
	}, []SubagentTask{{ID: "t1", Goal: "g1"}})
	if err != nil {
		t.Fatalf("StartBackground: %v", err)
	}
	if batch == nil || batch.BatchID == "" {
		t.Fatal("expected a batch handle")
	}
	if gate.calls != 1 {
		t.Fatalf("gate calls = %d, want 1", gate.calls)
	}
	_ = waitTerminal(t, store, batch.BatchID)
}

// TestStartBackgroundSkipsGateForIdempotencyKey pins the replay rule: a
// caller-supplied idempotency key must not be rejected a second time by a
// ceiling the existing batch already counts against.
func TestStartBackgroundSkipsGateForIdempotencyKey(t *testing.T) {
	store := testStore(t)
	gate := &stubLiveSlotGate{err: errString("agent spawn thread limit reached: max_threads=1")}
	c := newGateCoordinator(t, store, gate, &fakeExecutor{})

	opts := BatchStartOptions{
		ParentSessionID: "session-1",
		RootScopeID:     "root-1",
		IdempotencyKey:  "idem-1",
	}
	if _, err := c.StartBackground(context.Background(), opts, []SubagentTask{{ID: "t1", Goal: "g1"}}); err != nil {
		t.Fatalf("an idempotency-keyed dispatch must bypass the gate: %v", err)
	}
	if gate.calls != 0 {
		t.Fatalf("gate must be skipped when an idempotency key is present, calls=%d", gate.calls)
	}
}

type errString string

func (e errString) Error() string { return string(e) }
