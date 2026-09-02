package ui

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/renderengine"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

type terminalSessionBlockingWriter struct {
	mu        sync.Mutex
	writes    [][]byte
	started   chan int
	release   chan struct{}
	closeOnce sync.Once
}

func newTerminalSessionBlockingWriter() *terminalSessionBlockingWriter {
	return &terminalSessionBlockingWriter{
		started: make(chan int, 8),
		release: make(chan struct{}, 8),
	}
}

func (w *terminalSessionBlockingWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	w.writes = append(w.writes, append([]byte(nil), data...))
	index := len(w.writes)
	w.mu.Unlock()
	w.started <- index
	<-w.release
	return len(data), nil
}

func (w *terminalSessionBlockingWriter) waitStarted(t *testing.T, want int) []byte {
	t.Helper()
	select {
	case got := <-w.started:
		if got != want {
			t.Fatalf("write index = %d, want %d", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for write %d", want)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.writes[want-1]...)
}

func (w *terminalSessionBlockingWriter) allow() {
	w.release <- struct{}{}
}

func (w *terminalSessionBlockingWriter) unblock() {
	w.closeOnce.Do(func() { close(w.release) })
}

func (w *terminalSessionBlockingWriter) writeCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.writes)
}

func drainTerminalSessionExecutor(t *testing.T, writer *terminalSessionBlockingWriter, presenter *TerminalSessionPresenter) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		for {
			select {
			case <-writer.started:
				writer.allow()
				continue
			default:
			}
			break
		}
		presenter.executor.mu.Lock()
		running := presenter.executor.running
		presenter.executor.mu.Unlock()
		if !running {
			presenter.WaitIdle()
			presenter.controller.WaitIdle()
			schedule := presenter.controller.terminalSessionSchedule()
			if !schedule.recoveryActionable && schedule.pendingToken == 0 {
				return
			}
		}
		select {
		case <-writer.started:
			writer.allow()
		case <-time.After(10 * time.Millisecond):
		}
	}
	t.Fatal("timed out draining terminal session executor")
}

func TestTerminalSessionClaimMissRequiresRetryOnlyForActionableScheduleChange(t *testing.T) {
	claimed := terminalSessionScheduleSnapshot{pendingToken: 7, pendingGeneration: 3}
	tests := []struct {
		name   string
		latest terminalSessionScheduleSnapshot
		want   bool
	}{
		{
			name:   "projection invalidated after claim was scheduled",
			latest: terminalSessionScheduleSnapshot{projectionUnknown: true, recoveryActionable: true},
			want:   true,
		},
		{
			name: "viewport known but scrollback reconciliation remains",
			latest: terminalSessionScheduleSnapshot{
				reconciliationRequired: true,
				recoveryActionable:     true,
			},
			want: true,
		},
		{
			name:   "a replacement token is ready",
			latest: terminalSessionScheduleSnapshot{pendingToken: 8, pendingGeneration: 3},
			want:   true,
		},
		{
			name:   "the candidate was rebased to a new generation",
			latest: terminalSessionScheduleSnapshot{pendingToken: 7, pendingGeneration: 4},
			want:   true,
		},
		{
			name:   "lease or freeze removed actionable work",
			latest: terminalSessionScheduleSnapshot{},
			want:   false,
		},
		{
			name:   "unchanged candidate remains behind an ordering fence",
			latest: claimed,
			want:   false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := terminalSessionClaimMissRequiresRetry(claimed, test.latest); got != test.want {
				t.Fatalf("retry = %t, want %t; latest=%#v", got, test.want, test.latest)
			}
		})
	}
}

func TestTerminalSessionExecutorDrainsFinalResidentTailQueuedDuringBlockedFrameWrite(t *testing.T) {
	const width, height = 100, 24
	markers := make([]string, 40)
	lines := make([]string, len(markers))
	for index := range markers {
		markers[index] = fmt.Sprintf("ASYNC-FINAL-%02d terminal history validation", index+1)
		lines[index] = markers[index]
	}

	var presenter *TerminalSessionPresenter
	controller := NewUIController(UIControllerConfig{}, nil, nil)
	go controller.Run()
	writer := newTerminalSessionBlockingWriter()
	presenter = NewTerminalSessionPresenterForSession(controller, NewTerminalSession(writer), nil)
	if !presenter.Attach() {
		t.Fatal("attach presenter")
	}
	t.Cleanup(func() {
		writer.unblock()
		presenter.Close()
		controller.Close()
		controller.WaitIdle()
	})

	post := func(actions ...UIAction) {
		t.Helper()
		for _, action := range actions {
			if !controller.Post(action) {
				t.Fatalf("post %T", action)
			}
		}
		controller.WaitIdle()
	}
	post(
		Resize{Width: width, Height: height, Generation: 1},
		SetSemanticActiveCellProjectionAction{Enabled: true},
		ShowPromptAction{Line: "> "},
	)
	presenter.Request()
	writer.waitStarted(t, 1)
	writer.allow()
	drainTerminalSessionExecutor(t, writer, presenter)

	var source string
	for index, line := range lines {
		if source != "" {
			source += "\n"
		}
		source += line
		cell := &scene.TranscriptCell{
			ID: 91, Revision: uint64(index + 1), Kind: scene.KindAssistant,
			Source: source, Phase: scene.CellMutable,
		}
		if index == 0 {
			post(ReplaceTranscriptAction{Snapshot: &scene.Snapshot{
				Revision: uint64(index + 1), Cells: []*scene.TranscriptCell{cell},
			}})
		} else {
			current := controller.State().Active
			next := current
			next.Revision++
			next.Source = source
			post(
				UpdateActiveCellAction{ExpectedCellID: current.CellID, ExpectedRevision: current.Revision, Active: next},
				ReplaceTranscriptAction{Snapshot: &scene.Snapshot{
					Revision: uint64(index + 1), Cells: []*scene.TranscriptCell{cell},
				}},
			)
		}
		if index == 30 {
			select {
			case <-writer.started:
			case <-time.After(5 * time.Second):
				t.Fatal("timed out waiting for blocked streaming transaction")
			}
		}
	}

	streaming := controller.State()
	if streaming.Active.Acked.End != 0 || streaming.Active.Enqueued.End == 0 {
		t.Fatalf("blocked streaming frontier=%+v, want pending history and resident tail", streaming.Active)
	}
	finalCell := &scene.TranscriptCell{
		ID: 91, Revision: 41, Kind: scene.KindAssistant,
		Source: source, Phase: scene.CellCommitted,
	}
	post(FinalizeActiveCellAction{
		Snapshot:             &scene.Snapshot{Revision: 41, Cells: []*scene.TranscriptCell{finalCell}},
		ExpectedActiveCellID: 91, ExpectedActiveRevision: streaming.Active.Revision,
		ExpectedSceneRevision: 41,
		ExpectedActiveKind:    scene.KindAssistant, ExpectedActiveKindKnown: true,
	})

	writer.allow()
	drainTerminalSessionExecutor(t, writer, presenter)

	state := controller.State()
	if state.Active != (ActiveCellState{}) || state.HistoryEffects.HasPending() ||
		state.HistoryEffects.ProjectionUnknown || state.HistoryEffects.ReconciliationRequired {
		t.Fatalf("final drain left active/pending state: active=%+v effects=%+v", state.Active, state.HistoryEffects.Entries())
	}
	for _, entry := range state.HistoryEffects.Entries() {
		if entry.State == HistoryCommitPending || entry.State == HistoryCommitInFlight {
			t.Fatalf("final drain left token %d in state %s", entry.Commit.Token, entry.State)
		}
	}
	writer.mu.Lock()
	physical := make([]byte, 0)
	for _, write := range writer.writes {
		physical = append(physical, write...)
	}
	writer.mu.Unlock()
	assertPhysicalMarkersExactlyOnce(t, string(physical), width, height, markers)
	if strings.Contains(string(physical), markers[30]+"\r\n\r\n"+markers[31]) {
		t.Fatal("final resident-tail transition inserted an extra blank row")
	}
}

func TestTerminalSessionExecutorBootstrapsAndAcknowledgesOrderedHistoryInOneTransaction(t *testing.T) {
	controller := newHistoryExecutorController(t, nil)
	postHistoryEffectFixture(t, controller, 20)
	controller.WaitIdle()
	before := controller.State().HistoryEffects.Entries()
	if len(before) == 0 {
		t.Fatal("fixture did not create history effects")
	}

	writer := &terminalSessionShortWriter{}
	executor := NewTerminalSessionExecutor(controller, NewTerminalSession(writer))
	t.Cleanup(executor.Close)
	executor.Request()
	executor.WaitIdle()

	state := controller.State()
	first := historyCommitEntry(t, state, before[0].Commit.Token)
	if first.State != HistoryCommitAcked || state.HistoryEffects.ProjectionUnknown || writer.writes != 1 {
		t.Fatalf("initial bootstrap did not atomically hand off history: entry=%#v unknown=%t writes=%d", first, state.HistoryEffects.ProjectionUnknown, writer.writes)
	}

	// A complete bootstrap that fits the scheduling budget is one physical
	// transaction: it must not first paint a single-screen-only frame and rely
	// on a later subregion handoff.
	entries := state.HistoryEffects.Entries()
	if len(entries) != len(before) {
		t.Fatalf("history inventory changed across presenter bootstrap: before=%d after=%d", len(before), len(entries))
	}
	for index, entry := range entries {
		if entry.State != HistoryCommitAcked || entry.AckFrame == 0 {
			t.Fatalf("entry[%d] was not acknowledged after bootstrap: %#v", index, entry)
		}
		if index > 0 && entry.AckFrame < entries[index-1].AckFrame {
			t.Fatalf("ack frames regressed: previous=%#v current=%#v", entries[index-1], entry)
		}
	}
}

func TestTerminalSessionExecutorBoundsBootstrapAcrossTransactions(t *testing.T) {
	controller := newHistoryExecutorController(t, nil)
	postHistoryEffectFixture(t, controller, scene.CellID(terminalHistoryBatchMaxCommits+5))
	controller.WaitIdle()
	before := controller.State().HistoryEffects.Entries()
	if len(before) <= terminalHistoryBatchMaxCommits {
		t.Fatalf("fixture entries = %d, want more than batch limit %d", len(before), terminalHistoryBatchMaxCommits)
	}

	writer := &terminalSessionShortWriter{}
	executor := NewTerminalSessionExecutor(controller, NewTerminalSession(writer))
	t.Cleanup(executor.Close)
	executor.Request()
	executor.WaitIdle()

	entries := controller.State().HistoryEffects.Entries()
	ackFrames := make(map[uint64]struct{})
	for index, entry := range entries {
		if entry.State != HistoryCommitAcked || entry.AckFrame == 0 {
			t.Fatalf("entry[%d] was not acknowledged after bounded drain: %#v", index, entry)
		}
		ackFrames[entry.AckFrame] = struct{}{}
	}
	if writer.writes != 2 || len(ackFrames) != 2 {
		t.Fatalf("bounded bootstrap writes=%d ackFrames=%v, want two ordered transactions", writer.writes, ackFrames)
	}
}

func TestTerminalSessionExecutorConsumesActorWakeAndDrainsOrderedHistory(t *testing.T) {
	var executor *TerminalSessionExecutor
	controller := newHistoryExecutorController(t, func(effect Effect) {
		if executor != nil {
			executor.HandleEffect(effect)
		}
	})
	writer := &terminalSessionShortWriter{}
	executor = NewTerminalSessionExecutor(controller, NewTerminalSession(writer))
	t.Cleanup(executor.Close)

	postHistoryEffectFixture(t, controller, 20)
	controller.WaitIdle()
	executor.WaitIdle()
	controller.WaitIdle()

	entries := controller.State().HistoryEffects.Entries()
	if len(entries) == 0 {
		t.Fatal("wake fixture did not create history effects")
	}
	for index, entry := range entries {
		if entry.State != HistoryCommitAcked || entry.AckFrame == 0 {
			t.Fatalf("entry[%d] was not acknowledged through actor wake: %#v", index, entry)
		}
		if index > 0 && entry.AckFrame < entries[index-1].AckFrame {
			t.Fatalf("ack frame order regressed through actor wake: previous=%#v current=%#v", entries[index-1], entry)
		}
	}
	if writer.writes != 1 {
		t.Fatalf("expected one atomic bootstrap transaction, writes=%d", writer.writes)
	}
}

func TestTerminalSessionExecutorConsumesFlushEffectForFrameOnlyTransaction(t *testing.T) {
	var executor *TerminalSessionExecutor
	controller := NewUIController(UIControllerConfig{}, ReducerFunc(func(uint64, UIAction) []Effect {
		return []Effect{FlushEffect{}}
	}), func(effect Effect) {
		if executor != nil {
			executor.HandleEffect(effect)
		}
	})
	go controller.Run()
	writer := &terminalSessionShortWriter{}
	executor = NewTerminalSessionExecutor(controller, NewTerminalSession(writer))
	t.Cleanup(func() {
		executor.Close()
		controller.Close()
		controller.WaitIdle()
	})

	if !controller.Post(Resize{Width: 20, Height: 6, Generation: 1}) {
		t.Fatal("post Resize")
	}
	controller.WaitIdle()
	executor.WaitIdle()
	if writer.writes < 1 {
		t.Fatalf("frame-only FlushEffect emitted no target write")
	}
	if state := executor.session.ProjectionState(); state.Validity != renderengine.ProjectionKnown || state.Frame < 1 {
		t.Fatalf("frame-only FlushEffect did not confirm projection: %#v", state)
	}
}

func TestTerminalSessionExecutorConsumesControllerWakeWithoutExtraFrame(t *testing.T) {
	var executor *TerminalSessionExecutor
	controller := NewUIController(UIControllerConfig{}, nil, func(effect Effect) {
		if _, ok := effect.(HistoryCommitWakeEffect); ok && executor != nil {
			executor.Request()
		}
	})
	go controller.Run()
	writer := &terminalSessionShortWriter{}
	executor = NewTerminalSessionExecutor(controller, NewTerminalSession(writer))
	t.Cleanup(func() {
		executor.Close()
		controller.Close()
		controller.WaitIdle()
	})

	postHistoryEffectFixture(t, controller, 20)
	controller.WaitIdle()
	executor.WaitIdle()
	controller.WaitIdle()
	entries := controller.State().HistoryEffects.Entries()
	if len(entries) == 0 {
		t.Fatal("fixture did not create effects")
	}
	for _, entry := range entries {
		if entry.State != HistoryCommitAcked {
			t.Fatalf("wake-driven drain left entry unresolved: %#v", entry)
		}
	}
	// A write per token, or an initial screen-only recovery followed by a
	// subregion handoff, would recreate the host-dependent scrollback bug.
	if writer.writes != 1 {
		t.Fatalf("writes = %d, want one atomic bootstrap transaction", writer.writes)
	}
}

func TestTerminalSessionExecutorResizeRacingInFlightHistoryReconcilesAndDrains(t *testing.T) {
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
	beforeResize := controller.State()
	oldNextToken := beforeResize.HistoryEffects.NextToken
	oldToken := beforeResize.HistoryEffects.Entries()[0].Commit.Token
	if entry := historyCommitEntry(t, beforeResize, oldToken); entry.State != HistoryCommitInFlight {
		t.Fatalf("blocked history token = %#v, want in flight", entry)
	}
	if bytes.Contains(firstWrite, []byte("\x1b[3J")) {
		t.Fatalf("initial history transaction unexpectedly reset scrollback: %q", firstWrite)
	}

	if !controller.Post(Resize{Width: 80, Height: 12, Generation: 5}) {
		t.Fatal("post racing Resize")
	}
	controller.WaitIdle()
	resized := controller.State()
	if entry := historyCommitEntry(t, resized, oldToken); entry.State != HistoryCommitInvalidated || !entry.MayHavePartiallyWritten {
		t.Fatalf("resize did not quarantine in-flight history: %#v", entry)
	}
	if !resized.HistoryEffects.ProjectionUnknown {
		t.Fatal("resize race did not invalidate history projection")
	}
	writer.allow()
	resetWrite := writer.waitStarted(t, 2)
	if !bytes.Contains(resetWrite, []byte("\x1b[3J")) {
		t.Fatalf("current-generation recovery did not reset scrollback: %q", resetWrite)
	}
	writer.allow()
	freshWrite := writer.waitStarted(t, 3)
	if bytes.Contains(freshWrite, []byte("\x1b[3J")) {
		t.Fatalf("fresh history drain repeated scrollback reset: %q", freshWrite)
	}
	writer.allow()
	executor.WaitIdle()
	controller.WaitIdle()

	state := controller.State()
	assertTerminalSessionExecutorRecoveredHistory(t, state, oldToken, oldNextToken)
	if state.LayoutGeneration != 5 || state.HistoryEffects.TerminalEpoch == 0 || writer.writeCount() != 3 {
		t.Fatalf("resize recovery state = generation %d epoch %d writes %d", state.LayoutGeneration, state.HistoryEffects.TerminalEpoch, writer.writeCount())
	}
}

func TestTerminalSessionExecutorSecondResizeRacingScrollbackResetStillRecovers(t *testing.T) {
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
	writer.waitStarted(t, 1)
	controller.WaitIdle()
	beforeResize := controller.State()
	oldNextToken := beforeResize.HistoryEffects.NextToken
	oldToken := beforeResize.HistoryEffects.Entries()[0].Commit.Token

	if !controller.Post(Resize{Width: 80, Height: 12, Generation: 5}) {
		t.Fatal("post first racing Resize")
	}
	controller.WaitIdle()
	writer.allow()
	firstReset := writer.waitStarted(t, 2)
	if !bytes.Contains(firstReset, []byte("\x1b[3J")) {
		t.Fatalf("first recovery omitted scrollback reset: %q", firstReset)
	}

	if !controller.Post(Resize{Width: 80, Height: 14, Generation: 6}) {
		t.Fatal("post second racing Resize")
	}
	controller.WaitIdle()
	writer.allow()
	secondReset := writer.waitStarted(t, 3)
	if !bytes.Contains(secondReset, []byte("\x1b[3J")) {
		t.Fatalf("second current-generation recovery omitted scrollback reset: %q", secondReset)
	}
	writer.allow()
	writer.waitStarted(t, 4)
	writer.allow()
	executor.WaitIdle()
	controller.WaitIdle()

	state := controller.State()
	assertTerminalSessionExecutorRecoveredHistory(t, state, oldToken, oldNextToken)
	if state.LayoutGeneration != 6 || state.HistoryEffects.TerminalEpoch < 2 || writer.writeCount() != 4 {
		t.Fatalf("second resize recovery state = generation %d epoch %d writes %d", state.LayoutGeneration, state.HistoryEffects.TerminalEpoch, writer.writeCount())
	}
}

func assertTerminalSessionExecutorRecoveredHistory(t *testing.T, state UIControllerState, oldToken, oldNextToken uint64) {
	t.Helper()
	if state.HistoryEffects.ProjectionUnknown {
		t.Fatal("history projection remained unknown after scrollback reconciliation")
	}
	if state.HistoryEffects.NextToken <= oldNextToken {
		t.Fatalf("fresh epoch did not advance tokens: next=%d old=%d", state.HistoryEffects.NextToken, oldNextToken)
	}
	for _, entry := range state.HistoryEffects.Entries() {
		if entry.Commit.Token == oldToken {
			t.Fatalf("retired token survived reconciliation: %#v", entry)
		}
		if entry.Commit.Token <= oldNextToken || entry.State != HistoryCommitAcked || entry.AckFrame == 0 || entry.MayHavePartiallyWritten {
			t.Fatalf("fresh history entry unresolved after drain: %#v", entry)
		}
	}
}

func TestTerminalSessionExecutorFrameFailureReconcilesWithoutBlindHandoff(t *testing.T) {
	controller := newHistoryExecutorController(t, nil)
	postHistoryEffectFixture(t, controller, 20)
	controller.WaitIdle()
	firstToken := controller.State().HistoryEffects.Entries()[0].Commit.Token

	writer := &terminalSessionShortWriter{short: true}
	executor := NewTerminalSessionExecutor(controller, NewTerminalSession(writer))
	t.Cleanup(executor.Close)
	executor.Request()
	executor.WaitIdle()

	state := controller.State()
	entry := historyCommitEntry(t, state, firstToken)
	if entry.State != HistoryCommitStateFailed || !entry.MayHavePartiallyWritten || !state.HistoryEffects.ProjectionUnknown || writer.writes != 1 {
		t.Fatalf("failed bootstrap did not fail closed: entry=%#v unknown=%t writes=%d", entry, state.HistoryEffects.ProjectionUnknown, writer.writes)
	}

	writer.short = false
	executor.Request()
	executor.WaitIdle()
	state = controller.State()
	if state.HistoryEffects.ProjectionUnknown || state.HistoryEffects.TerminalEpoch == 0 {
		t.Fatalf("partial native history did not establish a fresh terminal epoch: %#v", state.HistoryEffects)
	}
	for _, current := range state.HistoryEffects.Entries() {
		if current.Commit.Token == firstToken || current.State != HistoryCommitAcked || current.MayHavePartiallyWritten {
			t.Fatalf("scrollback reconciliation left stale delivery: %#v", current)
		}
	}
	if !bytes.Contains(writer.bytes.Bytes(), []byte("\x1b[3J")) {
		t.Fatalf("recovery did not replace uncertain scrollback: %q", writer.bytes.String())
	}
}

func TestTerminalSessionExecutorSuccessBackoffReconcilesRequiredScrollback(t *testing.T) {
	controller := newHistoryExecutorController(t, nil)
	postHistoryEffectFixture(t, controller, 20)
	controller.WaitIdle()

	writer := &terminalSessionShortWriter{}
	executor := NewTerminalSessionExecutor(controller, NewTerminalSession(writer))
	t.Cleanup(executor.Close)

	// Commit the whole fixture through the normal path so the projection is
	// known, the ledger is clean and no token is pending.
	executor.Request()
	executor.WaitIdle()
	controller.WaitIdle()

	state := controller.State()
	if state.HistoryEffects.ProjectionUnknown || state.HistoryEffects.ReconciliationRequired || state.HistoryEffects.HasPending() {
		t.Fatalf("fixture did not settle into a known reconciled projection: %#v", state.HistoryEffects)
	}

	// Reproduce the post-resume production stall: the recovery obligation is
	// set (ReconciliationRequired=true) while the viewport projection is
	// known, and the executor's scrollback-reset backoff is armed in success
	// mode at the current (unchanged) layout generation.  In this state the
	// executor enters the success-mode backoff flush branch with
	// pendingToken=0 and must run the reconciliation plan to clear the
	// obligation — a plain viewport-only flush would leave it stuck forever.
	controller.mu.Lock()
	controller.state.HistoryEffects.ReconciliationRequired = true
	generation := controller.state.LayoutGeneration
	controller.mu.Unlock()
	executor.recordScrollbackReset(0, generation, false) // success-mode arm

	writer.bytes.Reset() // only observe the backoff-cycle transaction

	executor.Request()
	executor.WaitIdle()
	controller.WaitIdle()

	if !bytes.Contains(writer.bytes.Bytes(), []byte("\x1b[3J")) {
		t.Fatalf("success-mode backoff flush branch did not reconcile required scrollback (no reset): %q", writer.bytes.String())
	}

	// Drain the re-enqueued pending tokens that the reconciliation replan
	// created, so the final assertion looks at the settled state.
	deadline := time.Now().Add(10 * time.Second)
	for {
		state = controller.State()
		if !state.HistoryEffects.ProjectionUnknown && !state.HistoryEffects.ReconciliationRequired && !state.HistoryEffects.HasPending() {
			break
		}
		executor.Request()
		executor.WaitIdle()
		controller.WaitIdle()
		if time.Now().After(deadline) {
			t.Fatalf("timed out draining reconciliation replan: %#v", state.HistoryEffects)
		}
	}

	state = controller.State()
	if state.HistoryEffects.ReconciliationRequired {
		t.Fatalf("scrollback reconciliation obligation survived the backoff cycle: %#v", state.HistoryEffects)
	}
}

func TestTerminalSessionExecutorPartialHistoryWriteReconcilesWithoutResize(t *testing.T) {
	var executor *TerminalSessionExecutor
	controller := newHistoryExecutorController(t, func(effect Effect) {
		if executor != nil {
			executor.HandleEffect(effect)
		}
	})
	writer := &terminalSessionShortWriter{short: true}
	executor = NewTerminalSessionExecutor(controller, NewTerminalSession(writer))
	t.Cleanup(executor.Close)

	postHistoryEffectFixture(t, controller, 20)
	controller.WaitIdle()
	executor.WaitIdle()
	controller.WaitIdle()

	// The failed bootstrap flush woke the reconciliation attempt, which also
	// failed and armed the scrollback reset backoff (settled-revision guard).
	// The writer healing below is a physical change the reducer cannot
	// observe, so the retry must wait out the rate-limit window — the same
	// throttle a production transient writer failure gets when no state
	// change intervenes.
	time.Sleep(terminalScrollbackResetBackoff + 50*time.Millisecond)
	writer.short = false
	executor.Request()
	executor.WaitIdle()
	controller.WaitIdle()

	state := controller.State()
	if state.HistoryEffects.ProjectionUnknown || state.HistoryEffects.TerminalEpoch == 0 {
		t.Fatalf("partial history did not reconcile without resize: effects=%#v writes=%d", state.HistoryEffects, writer.writes)
	}
	for _, entry := range state.HistoryEffects.Entries() {
		if entry.State != HistoryCommitAcked || entry.MayHavePartiallyWritten {
			t.Fatalf("replanned history remained unresolved: %#v", entry)
		}
	}
	if !bytes.Contains(writer.bytes.Bytes(), []byte("\x1b[3J")) {
		t.Fatalf("recovery never reset uncertain native scrollback: %q", writer.bytes.String())
	}
}

func TestTerminalSessionExecutorZeroByteWriterErrorRecoversAndRetriesSameToken(t *testing.T) {
	controller := newHistoryExecutorController(t, nil)
	postHistoryEffectFixture(t, controller, 20)
	controller.WaitIdle()
	firstToken := controller.State().HistoryEffects.Entries()[0].Commit.Token

	writeErr := errors.New("terminal unavailable")
	writer := &terminalSessionShortWriter{zeroError: writeErr, failZero: 1}
	executor := NewTerminalSessionExecutor(controller, NewTerminalSession(writer))
	t.Cleanup(executor.Close)

	executor.Request()
	executor.WaitIdle()
	state := controller.State()
	entry := historyCommitEntry(t, state, firstToken)
	if entry.State != HistoryCommitPending || entry.Commit.Token != firstToken ||
		entry.MayHavePartiallyWritten || !state.HistoryEffects.ProjectionUnknown || writer.writes != 1 {
		t.Fatalf("zero-byte failure was not retained as retryable: entry=%#v unknown=%t writes=%d", entry, state.HistoryEffects.ProjectionUnknown, writer.writes)
	}

	// Recovery is viewport-only: it must establish a known cache before the
	// irreversible history token can be claimed again. The same worker run then
	// drains the now-eligible token, so the second Request observes the Acked
	// handoff (write 2 = source-backed recovery, write 3 = history retry).
	executor.Request()
	executor.WaitIdle()
	state = controller.State()
	entry = historyCommitEntry(t, state, firstToken)
	if entry.State != HistoryCommitAcked || entry.Commit.Token != firstToken || entry.AckFrame == 0 || writer.writes != 3 {
		t.Fatalf("same token was not recovered and acknowledged: entry=%#v writes=%d", entry, writer.writes)
	}
}

func TestTerminalSessionExecutorLeaseDefersFrameAndDoesNotClaimHistory(t *testing.T) {
	controller := newHistoryExecutorController(t, nil)
	postHistoryEffectFixture(t, controller, 20)
	if !controller.Post(LeaseAcquired{LeaseID: 21}) {
		t.Fatal("post LeaseAcquired")
	}
	controller.WaitIdle()
	firstToken := controller.State().HistoryEffects.Entries()[0].Commit.Token

	writer := &terminalSessionShortWriter{}
	executor := NewTerminalSessionExecutor(controller, NewTerminalSession(writer))
	t.Cleanup(executor.Close)
	executor.Request()
	executor.WaitIdle()

	entry := historyCommitEntry(t, controller.State(), firstToken)
	if entry.State != HistoryCommitPending || writer.writes != 0 {
		t.Fatalf("lease executor wrote or claimed history: entry=%#v writes=%d", entry, writer.writes)
	}
}

func TestTerminalSessionExecutorMissingHistoryResultFailsConservatively(t *testing.T) {
	controller := newHistoryExecutorController(t, nil)
	postHistoryEffectFixture(t, controller, 20)
	controller.WaitIdle()
	state := controller.State()
	commit := state.HistoryEffects.Pending()[0]
	if !controller.Post(BeginHistoryCommit{Token: commit.Token, LayoutGeneration: commit.LayoutGeneration}) {
		t.Fatal("post BeginHistoryCommit")
	}
	controller.WaitIdle()

	executor := NewTerminalSessionExecutor(controller, NewTerminalSession(&terminalSessionShortWriter{}))
	t.Cleanup(executor.Close)
	result := TerminalTransactionResult{Frame: TerminalFrameResult{Frame: 1}}
	executor.publishResult(commit.LayoutGeneration, &commit, result)
	controller.WaitIdle()
	entry := historyCommitEntry(t, controller.State(), commit.Token)
	if entry.State != HistoryCommitStateFailed || !entry.MayHavePartiallyWritten || !errors.Is(entry.Failure, ErrTerminalTransactionMissingResult) {
		t.Fatalf("missing terminal result was not conservatively failed: %#v", entry)
	}
	if !controller.State().HistoryEffects.ProjectionUnknown {
		t.Fatal("missing terminal result did not invalidate projection")
	}
}

func TestTerminalSessionExecutorCloseTimeoutAbortsBlockedWrite(t *testing.T) {
	var executor *TerminalSessionExecutor
	controller := NewUIController(UIControllerConfig{}, ReducerFunc(func(_ uint64, action UIAction) []Effect {
		switch action.(type) {
		case Resize, DrawRequested:
			return []Effect{FlushEffect{Dirty: renderengine.DirtyContent}}
		}
		return nil
	}), func(effect Effect) {
		if executor != nil {
			executor.HandleEffect(effect)
		}
	})
	writer := newTerminalSessionBlockingWriter()
	session := NewTerminalSession(writer)
	executor = NewTerminalSessionExecutor(controller, session)
	go controller.Run()
	defer func() {
		writer.unblock()
		executor.Close()
		controller.Close()
		controller.WaitIdle()
	}()

	if !controller.Post(Resize{Width: 80, Height: 24, Applied: true, Generation: 1}) {
		t.Fatal("failed to post blocked resize")
	}
	writer.waitStarted(t, 1)

	if executor.CloseTimeout(50 * time.Millisecond) {
		t.Fatal("CloseTimeout returned before the blocked physical write")
	}
	if err := session.AbortTerminalWrite(); err != nil {
		t.Fatalf("AbortTerminalWrite: %v", err)
	}
	if !executor.CloseTimeout(2 * time.Second) {
		t.Fatal("CloseTimeout did not complete after abort")
	}
	if got := writer.writeCount(); got != 1 {
		t.Fatalf("write count = %d, want only the abandoned write", got)
	}
}

func TestTerminalSessionExecutorWorkerTeardownReusesFreshDoneChannel(t *testing.T) {
	controller := NewUIController(UIControllerConfig{}, nil, nil)
	session := NewTerminalSession(&bytes.Buffer{})
	executor := NewTerminalSessionExecutor(controller, session)
	go controller.Run()
	t.Cleanup(func() {
		executor.Close()
		controller.Close()
		controller.WaitIdle()
	})

	// The worker used to publish running=false before its deferred close(done),
	// so a Request() arriving in that window could reuse the same per-worker
	// channel. Two workers then raced to close one channel. The churn below
	// used to panic deterministically under -count with the old teardown.
	for i := 0; i < 300; i++ {
		executor.Request()
		executor.WaitIdle()
	}
	if !executor.CloseTimeout(5 * time.Second) {
		t.Fatal("executor did not settle after repeated worker teardown")
	}
}

// TestTerminalSessionExecutorScrollbackResetBackoffIsGenerationBased locks in
// the dual-factor progress guard: a failing writer is rate-limited by a bounded
// window, a successful-but-non-converging reset is rate-limited by a bounded
// retry window (above the ~500ms cycle to guarantee engagement) and a retry
// budget, and a genuine layout change (new generation) always allows the next
// recovery attempt. The predicate is exercised directly so the test is
// deterministic and has no wall-clock races.
func TestTerminalSessionExecutorScrollbackResetBackoffIsGenerationBased(t *testing.T) {
	controller := NewUIController(UIControllerConfig{}, nil, nil)
	executor := NewTerminalSessionExecutor(controller, NewTerminalSession(&bytes.Buffer{}))

	// No prior reset: recovery is always allowed.
	if executor.scrollbackResetBackoff(7) {
		t.Fatal("backoff engaged before any scrollback reset")
	}

	// A confirmed reset at generation 7 must block a same-generation retry
	// (Non-failed mode: success without convergence — persistent backoff.)
	executor.recordScrollbackReset(3, 7, false)
	if !executor.scrollbackResetBackoff(7) {
		t.Fatal("same-generation recovery was not rate-limited after a reset")
	}

	// A new generation (real geometry/theme change) always breaks the backoff,
	// even within the window — the reset is a genuine retry, not a
	// non-progressing loop. Revision advance alone must NOT break it: a
	// transcript replay advances Revision ~240/cycle without converging.
	if executor.scrollbackResetBackoff(8) {
		t.Fatal("new-generation recovery was blocked by stale backoff")
	}

	// A second reset at the new generation re-arms the guard for that generation.
	executor.recordScrollbackReset(4, 8, false)
	if !executor.scrollbackResetBackoff(8) {
		t.Fatal("same-generation recovery was not rate-limited after the second reset")
	}

	// The backoff is now also wall-clock gated, but the window is chosen
	// ABOVE the recovery cycle (~500ms) so the guard still engages between
	// retries. After the retry window expires at the same generation (and
	// the retry budget is not exhausted), the executor is allowed one more
	// same-generation reset attempt — this is the deadlock fix: a
	// reconciliation whose first barrier was dropped while ProjectionUnknown
	// was set can converge after the projection recovers.
	executor.mu.Lock()
	executor.lastResetAt = time.Now().Add(-time.Hour)
	executor.mu.Unlock()
	if executor.scrollbackResetBackoff(8) {
		t.Fatal("same-generation retry was blocked after the retry window expired (budget not exhausted)")
	}

	// After a second success-mode record at the same generation the retry
	// budget is consumed (retries=0 → 1). The window elapses, and the
	// backoff releases again (budget still has room).
	executor.recordScrollbackReset(6, 8, false)
	executor.mu.Lock()
	executor.lastResetAt = time.Now().Add(-time.Hour)
	executor.mu.Unlock()
	if executor.scrollbackResetBackoff(8) {
		t.Fatal("same-generation retry blocked after second window expiry")
	}

	// After the budget is exhausted (>= terminalScrollbackResetMaxRetries),
	// the guard parks permanently at the same generation until a real
	// geometry/theme change.
	executor.recordScrollbackReset(7, 8, false)  // retries = 2
	executor.recordScrollbackReset(8, 8, false)  // retries = 3 ≥ maxRetries
	if !executor.scrollbackResetBackoff(8) {
		t.Fatal("same-generation retry was not parked after budget exhausted")
	}
	executor.mu.Lock()
	executor.lastResetAt = time.Now().Add(-time.Hour)
	executor.mu.Unlock()
	if !executor.scrollbackResetBackoff(8) {
		t.Fatal("exhausted-budget same-generation retry was not still blocked after window expiry")
	}

	// A new generation always resets the budget and allows recovery.
	if executor.scrollbackResetBackoff(9) {
		t.Fatal("new-generation recovery was blocked by stale backoff")
	}

	// Writer-failure mode: unchanged — bounded rate-limit window so a
	// transient writer error can heal and retry.
	executor.recordScrollbackReset(5, 9, true)
	if !executor.scrollbackResetBackoff(9) {
		t.Fatal("failed-mode backoff did not rate-limit within window")
	}
	executor.mu.Lock()
	executor.lastResetAt = time.Now().Add(-terminalScrollbackResetBackoff - 50*time.Millisecond)
	executor.mu.Unlock()
	if executor.scrollbackResetBackoff(9) {
		t.Fatal("failed-mode expired window still blocked a same-generation retry")
	}

	// The epoch bookkeeping follows the reset that armed the guard.
	executor.mu.Lock()
	gotEpoch, gotGeneration, gotFailed, gotRetries := executor.lastResetEpoch, executor.lastResetGeneration, executor.lastResetFailed, executor.lastResetSuccessRetries
	executor.mu.Unlock()
	if gotEpoch != 5 || gotGeneration != 9 || !gotFailed || gotRetries != 0 {
		t.Fatalf("last reset bookkeeping = epoch %d gen %d failed %t retries %d, want 5 / 9 / true / 0", gotEpoch, gotGeneration, gotFailed, gotRetries)
	}
}

// TestTerminalSessionExecutorReconciliationRetryAfterDeadlock is the
// regression test for the unified-renderer freeze. The deadlock condition:
//   - ReconciliationRequired=true, ProjectionUnknown=false (projection
//     recovered but the reconciliation barrier was dropped earlier)
//   - Success-mode backoff armed at the same layout generation
//   - Without the fix: no more scrollback resets at this generation ever,
//     so ReconciliationRequired persists forever (epoch frozen, unified
//     scrollback renderer stopped, only active band updates).
//
// The fix bounds the success-mode backoff with a retry window. After the
// window expires the executor must retry the scrollback reconciliation reset,
// and since ProjectionUnknown is now false the reducer accepts the fresh
// HistoryScrollbackReconciled barrier, clearing ReconciliationRequired.
func TestTerminalSessionExecutorReconciliationRetryAfterDeadlock(t *testing.T) {
	controller := newHistoryExecutorController(t, nil)
	writer := &terminalSessionShortWriter{}
	executor := NewTerminalSessionExecutor(controller, NewTerminalSession(writer))
	t.Cleanup(executor.Close)

	postHistoryEffectFixture(t, controller, 2)
	controller.WaitIdle()
	executor.WaitIdle()
	controller.WaitIdle()

	startGen := controller.LayoutGeneration()

	// Model the deadlock state: the projection is known (recovered via a
	// later plain viewport flush) but the reconciliation barrier from the
	// first reset was dropped while ProjectionUnknown was still set, so
	// ReconciliationRequired still=true. The success-mode backoff is armed
	// at the same generation (non-converging reset).
	controller.mu.Lock()
	controller.state.HistoryEffects.ProjectionUnknown = false
	controller.state.HistoryEffects.ReconciliationRequired = true
	controller.mu.Unlock()
	executor.recordScrollbackReset(1, startGen, false)

	// Verify the backoff is engaged (within the retry window).
	if !executor.scrollbackResetBackoff(startGen) {
		t.Fatal("backoff not engaged after a non-converging reset at the same generation")
	}
	if !executor.scrollbackResetSuccessMode() {
		t.Fatal("backoff not in success mode")
	}

	// Expire the retry window: the fix allows a bounded retry at the same
	// generation after the window elapses.
	executor.mu.Lock()
	executor.lastResetAt = time.Now().Add(-terminalScrollbackResetRetryWindow - time.Millisecond)
	executor.mu.Unlock()
	if executor.scrollbackResetBackoff(startGen) {
		t.Fatal("backoff did not release after retry window expired (deadlock fix missing)")
	}

	// Second cycle: the executor sees recoveryActionable (ReconciliationRequired
	// is still true) and the backoff is released, so it performs a full
	// scrollback reconciliation reset. This time ProjectionUnknown is false,
	// so the reducer accepts HistoryScrollbackReconciled → reconcileScrollback
	// → ReconciliationRequired cleared.
	executor.Request()
	executor.WaitIdle()
	controller.WaitIdle()

	state := controller.State()
	if state.HistoryEffects.ReconciliationRequired {
		t.Fatal("second reconciliation reset did not converge: ReconciliationRequired still set")
	}
	if state.HistoryEffects.ProjectionUnknown {
		t.Fatal("second reconciliation reset left ProjectionUnknown set")
	}
	if got := state.HistoryEffects.TerminalEpoch; got == 0 {
		t.Fatalf("terminal epoch after retry = 0 (no fresh epoch barrier was accepted)")
	}
	// The executor must have actually performed the retry scrollback reset
	// (not just a plain viewport flush): verify a reset landed after the
	// backoff released, and the reconciled scrollback content was committed.
	foundReset := false
	for _, entry := range executor.RecoveryDiag().Entries {
		if entry.ScrollbackReset {
			foundReset = true
		}
	}
	if !foundReset {
		t.Fatal("retry cycle did not perform a scrollback reset (plain flush only)")
	}
	if !strings.Contains(writer.bytes.String(), "final") {
		t.Fatalf("reconciled scrollback missing cell content: %q", writer.bytes.String())
	}
}

// TestTerminalSessionExecutorFailedReconciliationArmsBackoff locks in the
// flicker-loop fix: a scrollback reconciliation that FAILS must still arm the
// reset backoff, and the recorded revision must be the one settled AFTER the
// executor published its own outcome posts. The executor's own
// HistoryProjectionInvalidated/HistoryCommitFailed posts advance the actor
// revision; a guard recorded before them can never match the next cycle's
// read, so a persistently failing writer replayed the full history on every
// external wake (the reported constant-replay flicker).
func TestTerminalSessionExecutorFailedReconciliationArmsBackoff(t *testing.T) {
	var executor *TerminalSessionExecutor
	controller := newHistoryExecutorController(t, func(effect Effect) {
		if executor != nil {
			executor.HandleEffect(effect)
		}
	})
	writer := &terminalSessionShortWriter{short: true}
	executor = NewTerminalSessionExecutor(controller, NewTerminalSession(writer))
	t.Cleanup(executor.Close)

	postHistoryEffectFixture(t, controller, 20)
	controller.WaitIdle()
	executor.WaitIdle()
	controller.WaitIdle()

	// First Request: the claimed history flush fails (short write). The wake
	// from HistoryCommitFailed immediately drives the reconciliation attempt,
	// which fails again against the same writer. Both cycles settle inside
	// this WaitIdle.
	executor.Request()
	executor.WaitIdle()
	controller.WaitIdle()

	state := controller.State()
	if !state.HistoryEffects.ProjectionUnknown {
		t.Fatalf("persistent short writer left a known projection: %#v", state.HistoryEffects)
	}

	// The next Request arrives within the backoff window and the revision has
	// not advanced since the failed reconciliation settled: the executor must
	// yield without touching the writer again.
	writesBefore := writer.writes
	executor.Request()
	executor.WaitIdle()
	controller.WaitIdle()
	if writer.writes != writesBefore {
		t.Fatalf("backoff did not rate-limit the failed reconciliation: writes %d -> %d",
			writesBefore, writer.writes)
	}
	if !controller.State().HistoryEffects.ProjectionUnknown {
		t.Fatal("backoff cycle unexpectedly resolved the projection")
	}

	// A genuine external action (new revision) re-arms recovery immediately:
	// the next Request retries the reconciliation even inside the window.
	if !controller.Post(SetThemeContextAction{Theme: style.ThemeContext{SyntaxName: "test"}}) {
		t.Fatal("post external action")
	}
	controller.WaitIdle()
	executor.WaitIdle()
	controller.WaitIdle()
	if writer.writes <= writesBefore {
		t.Fatalf("new-revision recovery did not retry the reconciliation: writes=%d", writer.writes)
	}
}

// TestTerminalSessionExecutorArmRecoveryBackoffSuccessNonConverging locks in
// the fix for the continuous-replay root cause: a successful recovery flush
// that leaves the recovery obligation pending but DID NOT advance the layout
// generation (no real geometry/theme change) must arm the scrollback reset
// backoff at the start generation, so the next no-progress cycle yields
// instead of busy-replaying. A successful flush where the layout generation DID
// advance (genuine progress, e.g. a racing resize) must NOT arm the backoff.
// Revision advance alone must NOT disarm the guard: transcript replay advances
// Revision hundreds of times per cycle without ever converging.
func TestTerminalSessionExecutorArmRecoveryBackoffSuccessNonConverging(t *testing.T) {
	controller := NewUIController(UIControllerConfig{}, nil, nil)
	executor := NewTerminalSessionExecutor(controller, NewTerminalSession(&bytes.Buffer{}))

	// 1. Failed flush: always arms the guard at the settled generation
	// (unchanged from the existing behaviour — the executor's own failure posts
	// advance the revision, but the layout generation is the settled signal).
	if !executor.armRecoveryBackoff(TerminalTransactionResult{
		Frame: TerminalFrameResult{Err: errors.New("short write")},
	}, 0) {
		t.Fatal("failed flush did not arm backoff")
	}
	executor.mu.Lock()
	failGen := executor.lastResetGeneration
	executor.mu.Unlock()
	if failGen != controller.LayoutGeneration() {
		t.Fatalf("failed flush recorded generation %d, want controller generation %d", failGen, controller.LayoutGeneration())
	}

	// 2. Successful flush, obligation pending, no generation advance: the
	// signature of a non-progressing reset+replay loop. Must arm the guard at
	// the start generation (== settled generation) so the next cycle yields.
	controller.mu.Lock()
	controller.state.HistoryEffects.ProjectionUnknown = true
	controller.mu.Unlock()
	startGen := controller.LayoutGeneration()
	if !executor.armRecoveryBackoff(TerminalTransactionResult{}, startGen) {
		t.Fatal("non-converging success without generation advance did not arm backoff")
	}
	executor.mu.Lock()
	gotGen := executor.lastResetGeneration
	executor.mu.Unlock()
	if gotGen != startGen {
		t.Fatalf("non-converging success recorded generation %d, want start %d", gotGen, startGen)
	}

	// 3. Successful flush, obligation pending, revision advanced but generation
	// unchanged (transcript replay churn): still a non-progressing loop. Must
	// arm the backoff — this is the case that previously busy-looped at ~2
	// cores because revision advance was mistaken for genuine progress.
	controller.mu.Lock()
	controller.revision += 240
	controller.mu.Unlock()
	if controller.Revision() == 0 {
		t.Fatal("test setup failed: revision did not advance")
	}
	if !executor.armRecoveryBackoff(TerminalTransactionResult{}, startGen) {
		t.Fatal("replay churn (revision advance, same generation) did not arm backoff")
	}

	// 4. Successful flush, obligation pending, generation advanced (racing
	// resize/theme change): genuine progress. Must NOT arm.
	controller.mu.Lock()
	controller.state.LayoutGeneration++
	controller.mu.Unlock()
	if controller.LayoutGeneration() == startGen {
		t.Fatal("test setup failed: generation did not advance")
	}
	if executor.armRecoveryBackoff(TerminalTransactionResult{}, startGen) {
		t.Fatal("racing progress was blocked by stale backoff")
	}

	// 5. Successful scrollback reset, obligation already cleared (the executor's
	// own HistoryProjectionRecovered/HistoryScrollbackReconciled posts were
	// reduced by WaitIdle before armRecoveryBackoff runs), generation unchanged:
	// this is the actual production reset+replay loop — the reconcile handler
	// replanned the whole transcript, the next cycle is recoveryActionable
	// again, and the obligation check alone never sees it. A successful reset
	// at an unchanged layout generation must arm the guard.
	controller.mu.Lock()
	controller.state.LayoutGeneration = startGen // restore: no racing progress
	controller.state.HistoryEffects.ProjectionUnknown = false
	controller.state.HistoryEffects.ReconciliationRequired = false
	controller.mu.Unlock()
	if controller.LayoutGeneration() != startGen {
		t.Fatal("test setup failed: generation was not restored")
	}
	if !executor.armRecoveryBackoff(TerminalTransactionResult{
		ScrollbackReset: true,
		TerminalEpoch:   9,
	}, startGen) {
		t.Fatal("successful scrollback reset without generation advance did not arm backoff")
	}
	executor.mu.Lock()
	gotEpoch := executor.lastResetEpoch
	gotResetGen := executor.lastResetGeneration
	executor.mu.Unlock()
	if gotEpoch != 9 {
		t.Fatalf("scrollback-reset arm recorded epoch %d, want 9", gotEpoch)
	}
	if gotResetGen != startGen {
		t.Fatalf("scrollback-reset arm recorded generation %d, want start %d", gotResetGen, startGen)
	}
}

// TestTerminalSessionExecutorFlushesWhileSuccessBackoff locks in the
// frozen-prompt-input fix: when the scrollback-reset backoff is engaged in
// SUCCESS mode (the writer is healthy but the recovery obligation is not
// converging at an unchanged layout generation), the executor must still flush
// a plain viewport transaction so the bottom surface / prompt input keeps
// rendering. It must NOT perform the expensive reset+replay, and it must not
// starve the prompt surface either. In FAILED mode the writer is broken and
// stays untouched (covered by TestTerminalSessionExecutorFailedReconciliationArmsBackoff).
func TestTerminalSessionExecutorFlushesWhileSuccessBackoff(t *testing.T) {
	var executor *TerminalSessionExecutor
	controller := newHistoryExecutorController(t, func(effect Effect) {
		if executor != nil {
			executor.HandleEffect(effect)
		}
	})
	writer := &terminalSessionShortWriter{}
	executor = NewTerminalSessionExecutor(controller, NewTerminalSession(writer))
	t.Cleanup(executor.Close)

	// A minimal settled UI state with a prompt input surface, mirroring a
	// session that has resumed and settled.
	post := func(actions ...UIAction) {
		t.Helper()
		for _, action := range actions {
			if !controller.Post(action) {
				t.Fatalf("post %T", action)
			}
		}
		controller.WaitIdle()
	}
	post(
		Resize{Width: 80, Height: 10, Generation: 4},
		SetSemanticActiveCellProjectionAction{Enabled: true},
		ShowPromptAction{Line: "> "},
	)
	executor.WaitIdle()
	controller.WaitIdle()

	// Simulate the post-resume recovery obligation that transcript replay
	// re-arms but never converges at an unchanged layout generation.
	controller.mu.Lock()
	controller.state.HistoryEffects.ProjectionUnknown = true
	controller.mu.Unlock()
	startGen := controller.LayoutGeneration()

	// Arm the success-mode backoff exactly as armRecoveryBackoff does for a
	// successful-but-non-converging recovery: lastResetFailed=false at the
	// settled generation.
	if !executor.armRecoveryBackoff(TerminalTransactionResult{}, startGen) {
		t.Fatal("success-mode backoff was not armed")
	}
	executor.mu.Lock()
	successMode := !executor.lastResetFailed
	executor.mu.Unlock()
	if !successMode {
		t.Fatal("test setup: backoff not in success mode")
	}

	writesBefore := writer.writes
	executor.Request()
	executor.WaitIdle()
	controller.WaitIdle()

	if writer.writes <= writesBefore {
		t.Fatalf("success-mode backoff starved the prompt surface: writes %d -> %d",
			writesBefore, writer.writes)
	}

	diag := executor.RecoveryDiag()
	if diag.FlushesWhileBackoff == 0 {
		t.Fatalf("success-mode backoff flush not recorded: diag=%+v", diag)
	}
	if len(diag.Entries) == 0 {
		t.Fatal("no diag entries recorded")
	}
	last := diag.Entries[len(diag.Entries)-1]
	if !last.BackoffEngaged || !last.FlushedWhileBackoff {
		t.Fatalf("last diag entry missing backoff-flush markers: %+v", last)
	}
	if last.ScrollbackReset {
		t.Fatal("success-mode backoff flush unexpectedly performed a scrollback reset")
	}
}

// TestTerminalSessionExecutorHandsOffPendingHistoryWhileSuccessBackoff locks in
// the post-resume multi-turn regression: ReconciliationRequired can remain set
// at an unchanged layout generation while ProjectionUnknown is false. In that
// state new finalized cells still produce eligible history tokens. The
// success-mode reset backoff must suppress only reset+replay; if it also
// suppresses those tokens, viewport/active-band frames continue to move while
// every finalized turn is permanently absent from native scrollback.
func TestTerminalSessionExecutorHandsOffPendingHistoryWhileSuccessBackoff(t *testing.T) {
	controller := newHistoryExecutorController(t, nil)
	writer := &terminalSessionShortWriter{}
	executor := NewTerminalSessionExecutor(controller, NewTerminalSession(writer))
	t.Cleanup(executor.Close)

	postHistoryEffectFixture(t, controller, 1)
	controller.WaitIdle()

	// Model the settled state left by a successful-but-non-converging resume
	// recovery: the viewport projection is known, but source-backed scrollback
	// reconciliation remains required at this generation.
	controller.mu.Lock()
	controller.state.HistoryEffects.ProjectionUnknown = false
	controller.state.HistoryEffects.ReconciliationRequired = true
	controller.mu.Unlock()

	schedule := controller.terminalSessionSchedule()
	if !schedule.recoveryActionable || schedule.pendingToken == 0 {
		t.Fatalf("test setup did not expose recovery plus pending history: %+v", schedule)
	}
	executor.recordScrollbackReset(1, schedule.stateGeneration, false)
	if !executor.scrollbackResetBackoff(schedule.stateGeneration) {
		t.Fatal("test setup did not engage success-mode backoff")
	}

	executor.Request()
	executor.WaitIdle()
	controller.WaitIdle()
	drainSuccessBackoffExecutor(t, executor, controller)

	state := controller.State()
	if pending := state.HistoryEffects.Pending(); len(pending) != 0 {
		t.Fatalf("success-mode backoff starved pending history: %+v", pending)
	}
	if !strings.Contains(writer.bytes.String(), "final") {
		t.Fatalf("pending history never reached the terminal: %q", writer.bytes.String())
	}

	// Keep the same generation/backoff engaged and append another finalized
	// turn. This is the user-visible failure mode: the first recovered frame
	// can look healthy, then later turns update only the active band unless
	// each newly eligible token is allowed through the parked recovery guard.
	if !controller.Post(ReplaceTranscriptAction{Snapshot: &scene.Snapshot{
		Revision: 2,
		Cells: []*scene.TranscriptCell{
			{ID: 1, Revision: 1, Kind: scene.KindAssistant, Source: "final", Phase: scene.CellCommitted},
			{ID: 2, Revision: 1, Kind: scene.KindAssistant, Source: "second-final", Phase: scene.CellCommitted},
		},
	}}) {
		t.Fatal("post second finalized turn")
	}
	controller.WaitIdle()
	secondSchedule := controller.terminalSessionSchedule()
	// The reconciliation replan cleared the obligation (ReconciliationRequired
	// went false) and the second cell appends after the acked prefix, so
	// transcriptReplacementInvalidatesAckedHistory does not re-raise it. The
	// second turn must still enqueue an eligible pending token that the
	// executor hands off through the normal incremental-commit path — the
	// success-mode backoff must not suppress it.
	if secondSchedule.recoveryActionable || secondSchedule.pendingToken == 0 {
		t.Fatalf("second turn did not expose pending history after reconciliation: %+v", secondSchedule)
	}
	if secondSchedule.stateGeneration != schedule.stateGeneration {
		t.Fatalf("second turn unexpectedly changed layout generation: %d -> %d",
			schedule.stateGeneration, secondSchedule.stateGeneration)
	}
	executor.Request()
	executor.WaitIdle()
	controller.WaitIdle()
	drainSuccessBackoffExecutor(t, executor, controller)
	if pending := controller.State().HistoryEffects.Pending(); len(pending) != 0 {
		t.Fatalf("persistent success-mode backoff starved the second turn: %+v", pending)
	}
	if !strings.Contains(writer.bytes.String(), "second-final") {
		t.Fatalf("second finalized turn never reached the terminal: %q", writer.bytes.String())
	}

	diag := executor.RecoveryDiag()
	if diag.HandoffsWhileBackoff < 1 {
		t.Fatalf("backoff handoff was not recorded: %+v", diag)
	}
	// The second turn's projection was invalidated by the transcript change,
	// so its claim fails while ProjectionUnknown is set and the executor must
	// run the scrollback reconciliation plan under the engaged backoff instead
	// of a viewport-only flush (which would keep the obligation forever).
	if diag.ScrollbackResetsInWindow < 1 {
		t.Fatalf("reconciliation under success-mode backoff was not recorded: %+v", diag)
	}
	found := false
	for _, entry := range diag.Entries {
		if entry.BackoffEngaged && entry.HandoffWhileBackoff {
			found = true
			if entry.ScrollbackReset {
				t.Fatal("pending handoff unexpectedly performed a scrollback reset")
			}
		}
	}
	if !found {
		t.Fatalf("missing handoff-under-backoff diagnostic entry: %+v", diag.Entries)
	}
}

// drainSuccessBackoffExecutor keeps requesting the executor until the history
// ledger settles. Under success-mode backoff the executor may hand off pending
// tokens AND run a scrollback reconciliation (which replans the transcript
// under a fresh terminal epoch), so one Request is not enough to reach a
// quiescent state.
func drainSuccessBackoffExecutor(t *testing.T, executor *TerminalSessionExecutor, controller *UIController) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		state := controller.State()
		if !state.HistoryEffects.HasPending() && !terminalHistoryRecoveryActionable(state) {
			return
		}
		executor.Request()
		executor.WaitIdle()
		controller.WaitIdle()
		if time.Now().After(deadline) {
			t.Fatalf("timed out draining executor: %#v", state.HistoryEffects)
		}
	}
}

// TestExecutorDiagDiagnosisIdle verifies that a fresh diag reports "idle".
func TestExecutorDiagDiagnosisIdle(t *testing.T) {
	d := ExecutorRecoveryDiag{}
	if got := executorDiagDiagnosis(d); got != "idle" {
		t.Fatalf("empty diag: diagnosis=%q, want idle", got)
	}
}

// TestExecutorDiagDiagnosisDeadGuard verifies the dead-guard verdict:
// armed>0, engaged==0, recoveries>0 — the exact bug signature.
func TestExecutorDiagDiagnosisDeadGuard(t *testing.T) {
	d := ExecutorRecoveryDiag{
		TotalRecoveries: 100,
		ArmedBackoff:    100,
		BackoffEngaged:  0,
	}
	if got := executorDiagDiagnosis(d); got != "dead_guard" {
		t.Fatalf("dead-guard signature: diagnosis=%q, want dead_guard", got)
	}
}

// TestExecutorDiagDiagnosisBackoffEngaged verifies that when the last entry
// shows backoff engagement the verdict is "backoff_engaged".
func TestExecutorDiagDiagnosisBackoffEngaged(t *testing.T) {
	d := ExecutorRecoveryDiag{
		TotalRecoveries: 5,
		ArmedBackoff:    5,
		BackoffEngaged:  3,
		Entries: []ExecutorRecoveryDiagEntry{
			{Seq: 1, BackoffEngaged: false, Generation: 1},
			{Seq: 2, BackoffEngaged: false, Generation: 1},
			{Seq: 3, BackoffEngaged: true, Generation: 1},
			{Seq: 4, BackoffEngaged: true, Generation: 1},
			{Seq: 5, BackoffEngaged: true, Generation: 1},
		},
	}
	if got := executorDiagDiagnosis(d); got != "backoff_engaged" {
		t.Fatalf("backoff-engaged signature: diagnosis=%q, want backoff_engaged", got)
	}
}

func TestExecutorDiagDiagnosisBackoffEngagedHandingOff(t *testing.T) {
	d := ExecutorRecoveryDiag{
		TotalRecoveries:      5,
		ArmedBackoff:         5,
		BackoffEngaged:       3,
		HandoffsWhileBackoff: 2,
		Entries: []ExecutorRecoveryDiagEntry{
			{Seq: 5, BackoffEngaged: true, HandoffWhileBackoff: true, Generation: 1},
		},
	}
	if got := executorDiagDiagnosis(d); got != "backoff_engaged_handing_off" {
		t.Fatalf("handoff-under-backoff signature: diagnosis=%q, want backoff_engaged_handing_off", got)
	}
}

// TestExecutorDiagDiagnosisHealthy verifies that after a successful recovery
// with no backoff the verdict is "healthy".
func TestExecutorDiagDiagnosisHealthy(t *testing.T) {
	d := ExecutorRecoveryDiag{
		TotalRecoveries: 3,
		Entries: []ExecutorRecoveryDiagEntry{
			{Seq: 1, Generation: 1, FullRepaint: true},
			{Seq: 2, Generation: 2, FullRepaint: true},
			{Seq: 3, Generation: 3, FullRepaint: true},
		},
	}
	if got := executorDiagDiagnosis(d); got != "healthy" {
		t.Fatalf("healthy signature: diagnosis=%q, want healthy", got)
	}
}

// TestExecutorDiagGenerationAdvances verifies the counting of distinct
// layout generations across the ring buffer.
func TestExecutorDiagGenerationAdvances(t *testing.T) {
	entries := []ExecutorRecoveryDiagEntry{
		{Seq: 1, Generation: 1},
		{Seq: 2, Generation: 1},
		{Seq: 3, Generation: 2},
		{Seq: 4, Generation: 2},
		{Seq: 5, Generation: 1},
		{Seq: 6, Generation: 3},
	}
	if got := executorDiagGenerationAdvances(entries); got != 3 {
		t.Fatalf("generation advances: got %d, want 3", got)
	}
	if got := executorDiagGenerationAdvances(nil); got != 0 {
		t.Fatalf("empty: got %d, want 0", got)
	}
}

// TestExecutorDiagTextSummarySmoke verifies that the text summary renders
// without panic and contains expected key fields.
func TestExecutorDiagTextSummarySmoke(t *testing.T) {
	d := ExecutorRecoveryDiag{
		TotalRecoveries:            5,
		ArmedBackoff:               3,
		BackoffEngaged:             1,
		Diagnosis:                  "backoff_engaged",
		GeneratedAtUnixMs:          time.Now().UnixMilli(),
		LastGeneration:             2,
		GenerationAdvancesInWindow: 2,
		WindowRecoveriesPerSec:     0.5,
		Entries: []ExecutorRecoveryDiagEntry{
			{Seq: 1, Branch: "scheduled", Generation: 1, Revision: 10, RevisionAfter: 20, BackoffEngaged: false},
			{Seq: 2, Branch: "scheduled", Generation: 1, Revision: 20, RevisionAfter: 30, FrameErr: "write: broken pipe"},
			{Seq: 3, Branch: "scheduled", Generation: 1, Revision: 30, RevisionAfter: 40, BackoffEngaged: true},
			{Seq: 4, Branch: "scheduled", Generation: 2, Revision: 40, RevisionAfter: 50, FullRepaint: true},
			{Seq: 5, Branch: "scheduled", Generation: 2, Revision: 50, RevisionAfter: 60, ScrollbackReset: true},
		},
	}
	summary := executorDiagTextSummary(d)
	if summary == "" {
		t.Fatal("text summary is empty")
	}
	if !strings.Contains(summary, "backoff_engaged") {
		t.Fatal("text summary missing diagnosis")
	}
	if !strings.Contains(summary, "frameErrorsInWindow") {
		t.Fatal("text summary missing frameErrorsInWindow")
	}
	if !strings.Contains(summary, "scrollbackResetsInWindow") {
		t.Fatal("text summary missing scrollbackResetsInWindow")
	}
}
