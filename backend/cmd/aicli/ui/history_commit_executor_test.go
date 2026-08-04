package ui

import (
	"errors"
	"sync"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

type historyCommitSinkFunc func(HistoryCommit) HistoryCommitResult

func (f historyCommitSinkFunc) CommitHistory(commit HistoryCommit) HistoryCommitResult {
	return f(commit)
}

func TestHistoryCommitExecutor_AcksOldestTokensThroughWakeEffect(t *testing.T) {
	var executor *HistoryCommitExecutor
	var wakeCount int
	var wakeMu sync.Mutex
	controller := NewUIController(UIControllerConfig{}, nil, func(effect Effect) {
		if _, ok := effect.(HistoryCommitWakeEffect); ok && executor != nil {
			wakeMu.Lock()
			wakeCount++
			wakeMu.Unlock()
			executor.Request()
		}
	})
	go controller.Run()
	t.Cleanup(func() {
		executor.Close()
		controller.Close()
		controller.WaitIdle()
	})

	var mu sync.Mutex
	var tokens []uint64
	executor = NewHistoryCommitExecutor(controller, historyCommitSinkFunc(func(commit HistoryCommit) HistoryCommitResult {
		mu.Lock()
		tokens = append(tokens, commit.Token)
		mu.Unlock()
		return HistoryCommitResult{Frame: commit.Token + 100}
	}))
	postHistoryEffectFixture(t, controller, 20)
	controller.WaitIdle()
	executor.WaitIdle()

	state := controller.State()
	entries := state.HistoryEffects.Entries()
	if len(entries) == 0 {
		t.Fatal("expected eligible history effects")
	}
	wakeMu.Lock()
	wakes := wakeCount
	wakeMu.Unlock()
	if wakes == 0 {
		t.Fatalf("history effect was enqueued but no wake was delivered: %#v", entries)
	}
	mu.Lock()
	got := append([]uint64(nil), tokens...)
	mu.Unlock()
	if len(got) != len(entries) {
		t.Fatalf("sink calls = %v, entries = %#v", got, entries)
	}
	for index, entry := range entries {
		if entry.State != HistoryCommitAcked || entry.AckFrame != entry.Commit.Token+100 {
			t.Fatalf("entry[%d] not acknowledged: %#v", index, entry)
		}
		if got[index] != entry.Commit.Token {
			t.Fatalf("sink token order = %v, want token %d at %d", got, entry.Commit.Token, index)
		}
	}
}

func TestHistoryCommitExecutor_FailureStopsDrainAndMarksUnknown(t *testing.T) {
	controller := newHistoryExecutorController(t, nil)
	postHistoryEffectFixture(t, controller, 20)
	controller.WaitIdle()

	var calls int
	executor := NewHistoryCommitExecutor(controller, historyCommitSinkFunc(func(HistoryCommit) HistoryCommitResult {
		calls++
		return HistoryCommitResult{Err: errors.New("short write"), MayHavePartiallyWritten: true}
	}))
	t.Cleanup(executor.Close)
	executor.Request()
	executor.WaitIdle()

	state := controller.State()
	if calls != 1 || !state.HistoryEffects.ProjectionUnknown {
		t.Fatalf("calls=%d unknown=%t, want one failed call and Unknown", calls, state.HistoryEffects.ProjectionUnknown)
	}
	entries := state.HistoryEffects.Entries()
	if len(entries) < 2 || entries[0].State != HistoryCommitStateFailed || !entries[0].MayHavePartiallyWritten {
		t.Fatalf("failed head entry = %#v", entries)
	}
	if entries[1].State != HistoryCommitPending {
		t.Fatalf("later token advanced after failure: %#v", entries[1])
	}
}

func TestHistoryCommitExecutor_PossiblePartialWriteWithoutErrorFails(t *testing.T) {
	controller := newHistoryExecutorController(t, nil)
	postHistoryEffectFixture(t, controller, 20)
	controller.WaitIdle()

	executor := NewHistoryCommitExecutor(controller, historyCommitSinkFunc(func(HistoryCommit) HistoryCommitResult {
		return HistoryCommitResult{MayHavePartiallyWritten: true}
	}))
	t.Cleanup(executor.Close)
	executor.Request()
	executor.WaitIdle()

	entry := controller.State().HistoryEffects.Entries()[0]
	if entry.State != HistoryCommitStateFailed || !entry.MayHavePartiallyWritten || !errors.Is(entry.Failure, ErrHistoryCommitPartialWriteWithoutError) {
		t.Fatalf("partial-without-error entry = %#v", entry)
	}
	if !controller.State().HistoryEffects.ProjectionUnknown {
		t.Fatal("possible partial write did not invalidate projection")
	}
}

func TestHistoryCommitExecutor_SinkPanicFailsAndLeavesNoWorkerHang(t *testing.T) {
	controller := newHistoryExecutorController(t, nil)
	postHistoryEffectFixture(t, controller, 20)
	controller.WaitIdle()

	executor := NewHistoryCommitExecutor(controller, historyCommitSinkFunc(func(HistoryCommit) HistoryCommitResult {
		panic("writer panic")
	}))
	t.Cleanup(executor.Close)
	executor.Request()
	executor.WaitIdle()

	entry := controller.State().HistoryEffects.Entries()[0]
	if entry.State != HistoryCommitStateFailed || !entry.MayHavePartiallyWritten || !errors.Is(entry.Failure, ErrHistoryCommitSinkPanic) {
		t.Fatalf("panic entry = %#v", entry)
	}
	if !controller.State().HistoryEffects.ProjectionUnknown {
		t.Fatal("sink panic did not invalidate projection")
	}
}

func TestHistoryCommitExecutor_DeferredWithoutBytesRequeuesSameToken(t *testing.T) {
	controller := newHistoryExecutorController(t, nil)
	postHistoryEffectFixture(t, controller, 20)
	controller.WaitIdle()

	calls := 0
	executor := NewHistoryCommitExecutor(controller, historyCommitSinkFunc(func(HistoryCommit) HistoryCommitResult {
		calls++
		if calls == 1 {
			return HistoryCommitResult{Deferred: true}
		}
		return HistoryCommitResult{Frame: 77}
	}))
	t.Cleanup(executor.Close)
	first := controller.State().HistoryEffects.Entries()[0].Commit.Token
	executor.Request()
	executor.WaitIdle()
	entry := historyCommitEntry(t, controller.State(), first)
	if calls != 1 || entry.State != HistoryCommitPending || controller.State().HistoryEffects.ProjectionUnknown {
		t.Fatalf("defer entry = %#v calls=%d state=%#v", entry, calls, controller.State().HistoryEffects)
	}

	executor.Request()
	executor.WaitIdle()
	entry = historyCommitEntry(t, controller.State(), first)
	// A successful second request is allowed to drain later tokens too; the
	// invariant here is that the original token was not replaced or failed.
	if calls < 2 || entry.State != HistoryCommitAcked || entry.AckFrame != 77 {
		t.Fatalf("requeued entry = %#v calls=%d", entry, calls)
	}
}

func TestHistoryCommitExecutor_DoesNotClaimWhileLeaseFrozen(t *testing.T) {
	controller := newHistoryExecutorController(t, nil)
	postHistoryEffectFixture(t, controller, 20)
	if !controller.Post(LeaseAcquired{LeaseID: 33}) {
		t.Fatal("post LeaseAcquired")
	}
	controller.WaitIdle()

	calls := 0
	executor := NewHistoryCommitExecutor(controller, historyCommitSinkFunc(func(HistoryCommit) HistoryCommitResult {
		calls++
		return HistoryCommitResult{}
	}))
	t.Cleanup(executor.Close)
	executor.Request()
	executor.WaitIdle()
	if calls != 0 {
		t.Fatalf("frozen lease called sink %d times", calls)
	}
	if entry := controller.State().HistoryEffects.Entries()[0]; entry.State != HistoryCommitPending {
		t.Fatalf("frozen lease changed entry: %#v", entry)
	}
}

func newHistoryExecutorController(t *testing.T, onEffect func(Effect)) *UIController {
	t.Helper()
	controller := NewUIController(UIControllerConfig{}, nil, onEffect)
	go controller.Run()
	t.Cleanup(func() {
		controller.Close()
		controller.WaitIdle()
	})
	return controller
}

func postHistoryEffectFixture(t *testing.T, controller *UIController, count scene.CellID) {
	t.Helper()
	if !controller.Post(Resize{Width: 80, Height: 10, Generation: 4}) {
		t.Fatal("post Resize")
	}
	cells := make([]*scene.TranscriptCell, 0, count)
	for id := scene.CellID(1); id <= count; id++ {
		cells = append(cells, &scene.TranscriptCell{
			ID: id, Revision: 1, Kind: scene.KindAssistant,
			Source: "final", Phase: scene.CellCommitted,
		})
	}
	if !controller.Post(ReplaceTranscriptAction{Snapshot: &scene.Snapshot{Revision: 1, Cells: cells}}) {
		t.Fatal("post ReplaceTranscriptAction")
	}
}
