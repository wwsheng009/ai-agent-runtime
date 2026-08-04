package ui

import (
	"errors"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/renderengine"
)

func TestTerminalSessionExecutorRecoversThenAcknowledgesOrderedHistory(t *testing.T) {
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
	if first.State != HistoryCommitPending || state.HistoryEffects.ProjectionUnknown || writer.writes != 1 {
		t.Fatalf("initial recovery did not defer first handoff cleanly: entry=%#v unknown=%t writes=%d", first, state.HistoryEffects.ProjectionUnknown, writer.writes)
	}

	// The recovery frame is Known now. A later explicit wake claims the oldest
	// token and drains only after each Ack is published back to the actor.
	executor.Request()
	executor.WaitIdle()
	state = controller.State()
	entries := state.HistoryEffects.Entries()
	if len(entries) != len(before) {
		t.Fatalf("history inventory changed across presenter recovery: before=%d after=%d", len(before), len(entries))
	}
	for index, entry := range entries {
		if entry.State != HistoryCommitAcked || entry.AckFrame == 0 {
			t.Fatalf("entry[%d] was not acknowledged after recovery: %#v", index, entry)
		}
		if index > 0 && entry.AckFrame <= entries[index-1].AckFrame {
			t.Fatalf("ack frames were not ordered: previous=%#v current=%#v", entries[index-1], entry)
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
		if index > 0 && entry.AckFrame <= entries[index-1].AckFrame {
			t.Fatalf("ack frame order lost through actor wake: previous=%#v current=%#v", entries[index-1], entry)
		}
	}
	if writer.writes < 2 {
		t.Fatalf("expected recovery plus history transactions, writes=%d", writer.writes)
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
	// The initial source-backed recovery and every handoff are physical writes.
	// A final no-op frame would be a worker-loop regression.
	if writer.writes != len(entries)+1 {
		t.Fatalf("writes = %d, want recovery + each history effect = %d", writer.writes, len(entries)+1)
	}
}

func TestTerminalSessionExecutorFrameFailureInvalidatesThenRecoversWithoutBlindHandoff(t *testing.T) {
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
	if entry.State != HistoryCommitPending || !state.HistoryEffects.ProjectionUnknown || writer.writes != 1 {
		t.Fatalf("failed recovery frame did not preserve pending history: entry=%#v unknown=%t writes=%d", entry, state.HistoryEffects.ProjectionUnknown, writer.writes)
	}

	writer.short = false
	executor.Request()
	executor.WaitIdle()
	state = controller.State()
	if state.HistoryEffects.ProjectionUnknown {
		t.Fatal("successful source-backed recovery did not restore known projection")
	}
	entry = historyCommitEntry(t, state, firstToken)
	if entry.State != HistoryCommitPending {
		t.Fatalf("recovery blindly handed off pending token: %#v", entry)
	}

	executor.Request()
	executor.WaitIdle()
	entry = historyCommitEntry(t, controller.State(), firstToken)
	if entry.State != HistoryCommitAcked {
		t.Fatalf("post-recovery handoff did not acknowledge original token: %#v", entry)
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
