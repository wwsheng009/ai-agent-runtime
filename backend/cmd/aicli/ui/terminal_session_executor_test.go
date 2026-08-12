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

	// A complete bootstrap is one physical transaction: it must not first paint
	// a single-screen-only frame and rely on a later subregion handoff.
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
