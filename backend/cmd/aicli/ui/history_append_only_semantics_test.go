package ui

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// joinedBytes returns the concatenated physical byte stream. The blocking
// writer records one entry per transaction; callers must have drained (or
// unblocked) it first.
func (w *terminalSessionBlockingWriter) joinedBytes() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	var joined []byte
	for _, data := range w.writes {
		joined = append(joined, data...)
	}
	return joined
}

// postMarkedTranscript posts one replacement Scene with distinct per-row
// sources so physical delivery can be counted per row. Resize is posted by the
// caller (the fixture uses a fixed generation).
func postMarkedTranscript(t *testing.T, controller *UIController, rows []string, load bool) {
	t.Helper()
	cells := make([]*scene.TranscriptCell, 0, len(rows))
	for index, row := range rows {
		cells = append(cells, &scene.TranscriptCell{
			ID: scene.CellID(index + 1), Revision: 1, Kind: scene.KindAssistant,
			Source: row, Phase: scene.CellCommitted,
		})
	}
	if !controller.Post(ReplaceTranscriptAction{
		Snapshot:            &scene.Snapshot{Revision: 1, Cells: cells},
		ArmScrollbackReplay: load,
	}) {
		t.Fatal("post ReplaceTranscriptAction")
	}
}

// S5-1：装载后追加交付不重发。旧行在 native scrollback 保留（恰好一次），
// 装载替换只重证明计划，随后普通追加把新行按序交付，全程无清屏。
func TestSessionLoadThenAppendDeliversOnlyNewRows(t *testing.T) {
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
	if !controller.Post(Resize{Width: 80, Height: 10, Generation: 4}) {
		t.Fatal("post Resize")
	}

	rows := []string{"LOAD-ROW-A", "LOAD-ROW-B"}
	postMarkedTranscript(t, controller, rows, false)
	drainExecutorAllowingWrites(t, executor, controller, writer)

	// A session load re-proves the same Scene and imports one new row: the
	// already delivered prefix must not be re-emitted, the new row must be
	// appended by the ordinary ordered handoff.
	rows = append(rows, "LOAD-ROW-C")
	postMarkedTranscript(t, controller, rows, true)
	drainExecutorAllowingWrites(t, executor, controller, writer)

	// An ordinary append after the load must keep appending.
	rows = append(rows, "LOAD-ROW-D")
	postMarkedTranscript(t, controller, rows, false)
	drainExecutorAllowingWrites(t, executor, controller, writer)

	raw := writer.joinedBytes()
	if bytes.Contains(raw, []byte("\x1b[3J")) {
		t.Fatalf("load/append path cleared scrollback: %q", raw)
	}
	text := string(raw)
	for _, marker := range rows {
		if got := strings.Count(text, marker); got != 1 {
			t.Fatalf("row %q delivered %d times, want exactly one", marker, got)
		}
	}
	// Order: the physical stream appends the new rows after the delivered
	// prefix (no replay of earlier rows between them).
	for index := 1; index < len(rows); index++ {
		if strings.Index(text, rows[index]) < strings.Index(text, rows[index-1]) {
			t.Fatalf("row %q was delivered before %q", rows[index], rows[index-1])
		}
	}
	state := controller.State()
	if state.HistoryEffects.TerminalEpoch != 0 {
		t.Fatalf("load/append advanced the terminal epoch: %d", state.HistoryEffects.TerminalEpoch)
	}
	if state.HistoryEffects.ProjectionUnknown || state.HistoryEffects.ReconciliationRequired {
		t.Fatalf("load/append left a recovery obligation: %#v", state.HistoryEffects)
	}
}

// S5-2：settle 后从最后已证明行续写。首笔交付短写失败（部分写入）后，settle
// 把该笔原地隔离；其余行必须继续按序交付且恰好一次，settle 不推进语义 epoch、
// 不写 3J，已隔离的来源不再重铸。
func TestSettleAfterPartialWriteContinuesRemainingRowsExactlyOnce(t *testing.T) {
	controller := newHistoryExecutorController(t, nil)
	if !controller.Post(Resize{Width: 80, Height: 10, Generation: 4}) {
		t.Fatal("post Resize")
	}
	rows := []string{"SETTLE-ROW-A", "SETTLE-ROW-B", "SETTLE-ROW-C", "SETTLE-ROW-D"}
	postMarkedTranscript(t, controller, rows, false)
	controller.WaitIdle()

	writer := &terminalSessionShortWriter{short: true}
	executor := NewTerminalSessionExecutor(controller, NewTerminalSession(writer))
	t.Cleanup(executor.Close)
	executor.Request()
	executor.WaitIdle()
	controller.WaitIdle()

	failed := controller.State()
	if !failed.HistoryEffects.ProjectionUnknown {
		t.Fatalf("short write did not mark the projection unknown: %#v", failed.HistoryEffects)
	}

	// Let the writer heal and drain: the settle quarantines the unproven first
	// delivery, then the remaining rows must continue in order.
	writer.short = false
	deadline := time.Now().Add(10 * time.Second)
	for {
		state := controller.State()
		if !state.HistoryEffects.ProjectionUnknown &&
			!state.HistoryEffects.ReconciliationRequired &&
			!state.HistoryEffects.HasPending() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("settle continuation never converged: %#v", state.HistoryEffects)
		}
		executor.Request()
		executor.WaitIdle()
		controller.WaitIdle()
	}

	state := controller.State()
	if state.HistoryEffects.TerminalEpoch != 0 {
		t.Fatalf("settle advanced the terminal epoch without a physical act: %d", state.HistoryEffects.TerminalEpoch)
	}
	raw := writer.bytes.String()
	if strings.Contains(raw, "\x1b[3J") {
		t.Fatalf("settle continuation cleared scrollback: %q", raw)
	}
	// The first row's transaction short-wrote: its marker may be absent or
	// present once, but it must never be re-emitted. Every later row must be
	// delivered exactly once.
	for index, marker := range rows {
		count := strings.Count(raw, marker)
		if index == 0 {
			if count > 1 {
				t.Fatalf("partially written row %q was re-emitted %d times", marker, count)
			}
			continue
		}
		if count != 1 {
			t.Fatalf("row %q delivered %d times after settle, want exactly one", marker, count)
		}
	}
	for index := 2; index < len(rows); index++ {
		if strings.Index(raw, rows[index]) < strings.Index(raw, rows[index-1]) {
			t.Fatalf("row %q was delivered before %q after settle", rows[index], rows[index-1])
		}
	}
	// The unproven delivery stays in the ledger as a settled quarantine, and
	// its source identity still blocks a second mint for the same range.
	settled := false
	var settledCommit HistoryCommit
	for _, entry := range state.HistoryEffects.Entries() {
		if entry.IsSettled() {
			settled = true
			settledCommit = entry.Commit
		}
	}
	if !settled {
		t.Fatalf("unproven delivery disappeared from the ledger: %#v", state.HistoryEffects.Entries())
	}
	for _, entry := range state.HistoryEffects.Entries() {
		if entry.State == HistoryCommitQueued &&
			historyCommitSourceIdentity(entry.Commit) == historyCommitSourceIdentity(settledCommit) {
			t.Fatalf("settled source was minted again: %#v", entry)
		}
	}
}

// S5-3（装载场景扩展）：未决在途交付期间到达的装载替换不得复活旧 token。
// 装载只重证明（不推进 epoch、不重铸同源 token），随后旧 token 的 fail/settle/
// 迟到 ack 都不得把它变成已交付。
func TestHistoryEffectsReducer_LoadAfterFailedHandoffCannotResurrectOldToken(t *testing.T) {
	state := historyEffectTestState(t, 2)
	oldToken := state.HistoryEffects.Entries()[0].Commit.Token
	tokensBefore := state.HistoryEffects.NextToken
	state = reduceUIControllerState(state, BeginHistoryCommit{Token: oldToken, LayoutGeneration: 2}, 3)

	// The session load re-publishes the same Scene while the handoff is still
	// unresolved. It must re-prove the plan without retiring the in-flight
	// record or minting a duplicate token for the same source.
	state = reduceUIControllerState(state, ReplaceTranscriptAction{
		Snapshot:            state.Transcript.Snapshot(),
		ArmScrollbackReplay: true,
	}, 4)
	if state.HistoryEffects.TerminalEpoch != 0 {
		t.Fatalf("load advanced the terminal epoch without a physical act: %d", state.HistoryEffects.TerminalEpoch)
	}
	if state.HistoryEffects.NextToken != tokensBefore {
		t.Fatalf("load re-minted a planned source: next=%d want %d", state.HistoryEffects.NextToken, tokensBefore)
	}
	if entry := historyCommitEntry(t, state, oldToken); entry.State != HistoryCommitQueued {
		t.Fatalf("load retired the in-flight delivery: %#v", entry)
	}

	// The old token fails with a partial write, then settles in place.
	state = reduceUIControllerState(state, HistoryCommitFailed{
		Token: oldToken, LayoutGeneration: 2, Err: errors.New("terminal failed"), MayHavePartiallyWritten: true,
	}, 5)
	state = reduceUIControllerState(state, HistoryReconciliationSettled{LayoutGeneration: 2}, 6)
	state = reduceUIControllerState(state, HistoryProjectionRecovered{LayoutGeneration: 2}, 7)

	// A late acknowledgement cannot resurrect the settled token.
	state = reduceUIControllerState(state, HistoryCommitAcknowledged{
		Token: oldToken, Frame: 99, LayoutGeneration: 2,
	}, 8)
	for _, entry := range state.HistoryEffects.Entries() {
		if entry.Commit.Token == oldToken && entry.State == HistoryCommitDelivered {
			t.Fatalf("late acknowledgement resurrected a settled token: %#v", entry)
		}
	}
	if state.HistoryEffects.ProjectionUnknown || state.HistoryEffects.ReconciliationRequired {
		t.Fatalf("settle left the recovery obligation set: %#v", state.HistoryEffects)
	}
	if state.HistoryEffects.TerminalEpoch != 0 {
		t.Fatalf("settle advanced the terminal epoch: %d", state.HistoryEffects.TerminalEpoch)
	}

	// Another load after the settle must not re-mint the settled source.
	state = reduceUIControllerState(state, ReplaceTranscriptAction{
		Snapshot:            state.Transcript.Snapshot(),
		ArmScrollbackReplay: true,
	}, 9)
	if state.HistoryEffects.NextToken != tokensBefore {
		t.Fatalf("post-settle load re-minted a settled source: next=%d want %d", state.HistoryEffects.NextToken, tokensBefore)
	}
}
