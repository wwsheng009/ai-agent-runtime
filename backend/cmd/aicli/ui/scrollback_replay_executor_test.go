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

// TestTerminalSessionExecutorLoadKeepsScrollbackAppendOnly pins the S1 contract:
// a session load arriving while a handoff is crossing the writer must not reset
// native scrollback, must not retire the delivery ledger, and must let the
// in-flight delivery complete as an ordinary record. Loaded content reaches the
// host by the ordinary ordered handoff — appended, never replayed from a cleared
// screen.
func TestTerminalSessionExecutorLoadKeepsScrollbackAppendOnly(t *testing.T) {
	var executor *TerminalSessionExecutor
	controller := newHistoryExecutorController(t, func(effect Effect) {
		if executor != nil {
			executor.HandleEffect(effect)
		}
	})
	writer := newTerminalSessionBlockingWriter()
	session := NewTerminalSession(writer)
	executor = NewTerminalSessionExecutor(controller, session)
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
	if entry := historyCommitEntry(t, before, inflightToken); entry.State != HistoryCommitQueued || before.HistoryEffects.WriteCursor != inflightToken {
		t.Fatalf("blocked history token = %#v cursor=%d, want claimed pending", entry, before.HistoryEffects.WriteCursor)
	}

	// A session load arrives while that handoff is still crossing the writer.
	if !controller.Post(ReplaceTranscriptAction{
		Snapshot:            scrollbackGrantSnapshot(2, "loaded session"),
		ArmScrollbackReplay: true,
	}) {
		t.Fatal("post load replacement")
	}
	controller.WaitIdle()
	loaded := controller.State()
	if loaded.HistoryEffects.ScrollbackReplayArmed {
		t.Fatal("load armed a destructive replay")
	}
	if loaded.HistoryEffects.TerminalEpoch != 0 {
		t.Fatalf("load started a terminal epoch without a physical act: %d", loaded.HistoryEffects.TerminalEpoch)
	}
	// The in-flight record must survive the load: those bytes are already
	// crossing the writer and append-only delivery must not re-emit them.
	if _, ok := loaded.HistoryEffects.Entry(inflightToken); !ok {
		t.Fatal("load retired the in-flight delivery record")
	}
	if writer.countWritesContaining([]byte("\x1b[3J")) != 0 {
		t.Fatal("scrollback was reset while the handoff was still in flight")
	}

	drainExecutorAllowingWrites(t, executor, controller, writer)

	state := controller.State()
	if _, ok := state.HistoryEffects.Entry(inflightToken); !ok {
		t.Fatalf("in-flight delivery record disappeared after the load: %#v", state.HistoryEffects)
	}
	if state.HistoryEffects.ScrollbackReplayArmed {
		t.Fatal("load left a destructive authorization armed")
	}
	if state.HistoryEffects.ProjectionUnknown || state.HistoryEffects.ReconciliationRequired {
		t.Fatalf("load left an obligation: %#v", state.HistoryEffects)
	}
	if resets := writer.countWritesContaining([]byte("\x1b[3J")); resets != 0 {
		t.Fatalf("session load wrote %d scrollback resets, want 0", resets)
	}
	if loadIndex := writer.firstWriteIndexOf([]byte("loaded session")); loadIndex == 0 {
		t.Fatal("loaded transcript never reached the host")
	}

	// A later resize must also repaint without replaying.
	if !controller.Post(Resize{Width: 80, Height: 14, Generation: 9}) {
		t.Fatal("post post-load Resize")
	}
	controller.WaitIdle()
	executor.Request()
	executor.WaitIdle()
	controller.WaitIdle()
	if resets := writer.countWritesContaining([]byte("\x1b[3J")); resets != 0 {
		t.Fatalf("a later interaction reset scrollback: %d", resets)
	}
}
