package ui

import (
	"bytes"
	"testing"
)

// countWritesContaining reports how many physical transactions carried needle.
// It is deliberately index-based (not a boolean) so a test can prove a
// destructive replay happened exactly once and never again.
func (w *terminalSessionBlockingWriter) countWritesContaining(needle []byte) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	count := 0
	for _, data := range w.writes {
		if bytes.Contains(data, needle) {
			count++
		}
	}
	return count
}

// firstWriteIndexOf reports the 1-based index of the first transaction
// containing needle, or 0 when no transaction carried it.
func (w *terminalSessionBlockingWriter) firstWriteIndexOf(needle []byte) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	for index, data := range w.writes {
		if bytes.Contains(data, needle) {
			return index + 1
		}
	}
	return 0
}

// TestTerminalSessionExecutorArmedReplayWaitsForInFlightDelivery pins the
// ordering contract between the authorized destructive replay and a delivery
// that is already crossing the writer. Arming the replay must not invalidate or
// preempt the in-flight handoff (those bytes are already committed to the host,
// so invalidating them would make the range permanently un-mintable), the replay
// must run exactly once after that handoff settles, and it must consume the
// one-shot authorization so no later interaction resets native scrollback again.
func TestTerminalSessionExecutorArmedReplayWaitsForInFlightDelivery(t *testing.T) {
	var executor *TerminalSessionExecutor
	controller := newHistoryExecutorController(t, func(effect Effect) {
		if executor != nil {
			executor.HandleEffect(effect)
		}
	})
	writer := newTerminalSessionBlockingWriter()
	executor = NewTerminalSessionExecutor(controller, NewTerminalSession(writer))
	t.Cleanup(func() {
		writer.unblock()
		executor.Close()
	})

	postHistoryEffectFixture(t, controller, 20)
	firstWrite := writer.waitStarted(t, 1)
	controller.WaitIdle()
	before := controller.State()
	if bytes.Contains(firstWrite, []byte("\x1b[3J")) {
		t.Fatalf("initial history transaction unexpectedly reset scrollback: %q", firstWrite)
	}
	inflightToken := before.HistoryEffects.Entries()[0].Commit.Token
	if entry := historyCommitEntry(t, before, inflightToken); entry.State != HistoryCommitInFlight {
		t.Fatalf("blocked history token = %#v, want in flight", entry)
	}

	// A session load arms the one-shot replay while that handoff is still
	// crossing the writer.
	if !controller.Post(ReplaceTranscriptAction{
		Snapshot:            scrollbackGrantSnapshot(2, "loaded session"),
		ArmScrollbackReplay: true,
	}) {
		t.Fatal("post armed replacement")
	}
	controller.WaitIdle()
	armed := controller.State()
	if !armed.HistoryEffects.ScrollbackReplayArmed || !armed.HistoryEffects.ReconciliationRequired {
		t.Fatalf("load did not arm the replay: %#v", armed.HistoryEffects)
	}
	// The replacement supersedes the delivery crossing the writer. The ledger
	// cannot prove how many of those bytes reached the host, so it must escalate
	// the token to the unresolved partial-write state — that escalation is
	// precisely what the authorized replay is allowed to repair.
	if entry := historyCommitEntry(t, armed, inflightToken); entry.State != HistoryCommitInvalidated || !entry.MayHavePartiallyWritten {
		t.Fatalf("replacement did not escalate the in-flight delivery: %#v", entry)
	}
	if writer.countWritesContaining([]byte("\x1b[3J")) != 0 {
		t.Fatal("scrollback was replaced while the handoff was still in flight")
	}

	drainExecutorAllowingWrites(t, executor, controller, writer)

	state := controller.State()
	// The proven epoch replacement discards the superseded delivery records and
	// replans from the loaded Scene; keeping the escalated token alive would
	// re-emit a range whose physical outcome the reset already superseded.
	if entry, survived := state.HistoryEffects.Entry(inflightToken); survived {
		t.Fatalf("superseded delivery survived the proven replacement epoch: %#v", entry)
	}
	if state.HistoryEffects.ScrollbackReplayArmed {
		t.Fatal("the authorized replay did not consume the one-shot grant")
	}
	if state.HistoryEffects.ProjectionUnknown || state.HistoryEffects.ReconciliationRequired {
		t.Fatalf("authorized replay left an obligation: %#v", state.HistoryEffects)
	}
	if state.HistoryEffects.TerminalEpoch == 0 {
		t.Fatal("authorized replay did not mint a terminal epoch")
	}
	resetIndex := writer.firstWriteIndexOf([]byte("\x1b[3J"))
	if resetIndex == 0 {
		t.Fatal("authorized replay never replaced native scrollback")
	}
	loadIndex := writer.firstWriteIndexOf([]byte("loaded session"))
	if loadIndex == 0 {
		t.Fatal("authorized replay never rendered the loaded transcript")
	}
	if loadIndex < resetIndex {
		t.Fatalf("loaded transcript reached the host before the reset: load write %d, reset write %d", loadIndex, resetIndex)
	}
	if resets := writer.countWritesContaining([]byte("\x1b[3J")); resets != 1 {
		t.Fatalf("authorized replay wrote %d reset transactions, want 1", resets)
	}

	// The grant is one-shot: a later resize must repaint without replaying.
	if !controller.Post(Resize{Width: 80, Height: 14, Generation: 9}) {
		t.Fatal("post post-replay Resize")
	}
	controller.WaitIdle()
	executor.Request()
	executor.WaitIdle()
	controller.WaitIdle()
	if resets := writer.countWritesContaining([]byte("\x1b[3J")); resets != 1 {
		t.Fatalf("a later interaction replayed scrollback: %d reset transactions", resets)
	}
}
