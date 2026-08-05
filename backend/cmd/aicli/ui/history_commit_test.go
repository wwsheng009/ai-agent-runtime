package ui

import (
	"errors"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

func testHistoryCommit(token uint64, cellID scene.CellID, generation uint64) HistoryCommit {
	return HistoryCommit{
		Token:            token,
		CellID:           cellID,
		Revision:         7,
		SourceRange:      SourceRange{Start: 4, End: 12},
		DisplayRange:     DisplayRange{Start: 2, End: 5},
		LayoutGeneration: generation,
		Lines:            []render.Line{{Spans: []render.Span{{Text: "same visible text"}}}},
	}
}

func TestHistoryCommitLedger_AckExactlyOnceByTokenAndRange(t *testing.T) {
	ledger := NewHistoryCommitLedger()
	commit := testHistoryCommit(11, 42, 3)
	if err := ledger.Enqueue(commit); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if err := ledger.MarkInFlight(commit.Token); err != nil {
		t.Fatalf("MarkInFlight: %v", err)
	}
	if err := ledger.Ack(commit.Token, 99, 3); err != nil {
		t.Fatalf("Ack: %v", err)
	}
	if err := ledger.Ack(commit.Token, 100, 3); !errors.Is(err, ErrDuplicateCommitAck) {
		t.Fatalf("second Ack = %v, want ErrDuplicateCommitAck", err)
	}
	entry, ok := ledger.Entry(commit.Token)
	if !ok || entry.State != HistoryCommitAcked || entry.AckFrame != 99 {
		t.Fatalf("entry = %+v, found=%t", entry, ok)
	}
	if entry.Commit.Lines != nil {
		t.Fatalf("ack retained delivered payload: %#v", entry.Commit.Lines)
	}
}

func TestHistoryCommitLedger_SameTextDifferentIdentityIsAllowed(t *testing.T) {
	ledger := NewHistoryCommitLedger()
	first := testHistoryCommit(1, 41, 8)
	second := testHistoryCommit(2, 42, 8)
	if err := ledger.Enqueue(first); err != nil {
		t.Fatalf("first Enqueue: %v", err)
	}
	if err := ledger.Enqueue(second); err != nil {
		t.Fatalf("same-text different-cell Enqueue: %v", err)
	}
	duplicate := first
	duplicate.Token = 3
	if err := ledger.Enqueue(duplicate); !errors.Is(err, ErrDuplicateCommitRange) {
		t.Fatalf("same identity/range Enqueue = %v, want ErrDuplicateCommitRange", err)
	}
}

func TestHistoryCommitLedger_RichFragmentsShareSourceWithoutColliding(t *testing.T) {
	ledger := NewHistoryCommitLedger()
	first := testHistoryCommit(1, 41, 8)
	first.SourceRange = SourceRange{Start: 0, End: 40}
	first.FragmentID = 1
	first.DisplayRange = DisplayRange{Start: 0, End: 1}
	second := first
	second.Token = 2
	second.FragmentID = 2
	second.DisplayRange = DisplayRange{Start: 1, End: 2}
	if err := ledger.Enqueue(first); err != nil {
		t.Fatalf("first rich fragment Enqueue: %v", err)
	}
	if err := ledger.Enqueue(second); err != nil {
		t.Fatalf("second rich fragment Enqueue: %v", err)
	}
	if got := len(ledger.Entries()); got != 2 {
		t.Fatalf("rich fragment entries=%d want 2", got)
	}
}

func TestHistoryCommitLedger_RejectsIncompleteIdentity(t *testing.T) {
	ledger := NewHistoryCommitLedger()
	missingGeneration := testHistoryCommit(1, 41, 0)
	if err := ledger.Enqueue(missingGeneration); !errors.Is(err, ErrInvalidHistoryCommit) {
		t.Fatalf("missing layout generation = %v, want ErrInvalidHistoryCommit", err)
	}
	emptyDisplay := testHistoryCommit(2, 42, 1)
	emptyDisplay.DisplayRange = DisplayRange{Start: 3, End: 3}
	if err := ledger.Enqueue(emptyDisplay); !errors.Is(err, ErrInvalidHistoryCommit) {
		t.Fatalf("empty display range = %v, want ErrInvalidHistoryCommit", err)
	}
}

func TestHistoryCommitLedger_StaleGenerationDoesNotAdvanceAck(t *testing.T) {
	ledger := NewHistoryCommitLedger()
	commit := testHistoryCommit(9, 71, 4)
	if err := ledger.Enqueue(commit); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if err := ledger.MarkInFlight(commit.Token); err != nil {
		t.Fatalf("MarkInFlight: %v", err)
	}
	if err := ledger.Ack(commit.Token, 3, 5); !errors.Is(err, ErrStaleLayoutGeneration) {
		t.Fatalf("stale Ack = %v, want ErrStaleLayoutGeneration", err)
	}
	entry, ok := ledger.Entry(commit.Token)
	if !ok || entry.State != HistoryCommitInFlight || entry.AckFrame != 0 {
		t.Fatalf("stale Ack advanced entry: %+v, found=%t", entry, ok)
	}
}

func TestHistoryCommitLedger_FailurePreservesPartialWriteSignal(t *testing.T) {
	ledger := NewHistoryCommitLedger()
	commit := testHistoryCommit(13, 88, 2)
	if err := ledger.Enqueue(commit); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if err := ledger.MarkInFlight(commit.Token); err != nil {
		t.Fatalf("MarkInFlight: %v", err)
	}
	cause := errors.New("short write")
	if err := ledger.Fail(commit.Token, cause, true); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	entry, ok := ledger.Entry(commit.Token)
	if !ok || entry.State != HistoryCommitStateFailed || !entry.MayHavePartiallyWritten || !errors.Is(entry.Failure, cause) {
		t.Fatalf("entry = %+v, found=%t", entry, ok)
	}
}

func TestHistoryCommitLedger_SourceLookupKeepsLifecycleWithoutDetachedCopies(t *testing.T) {
	ledger := NewHistoryCommitLedger()
	for token := uint64(1); token <= 64; token++ {
		commit := testHistoryCommit(token, scene.CellID(token), 2)
		commit.Lines = make([]render.Line, 16)
		if err := ledger.Enqueue(commit); err != nil {
			t.Fatalf("Enqueue(%d): %v", token, err)
		}
	}
	target := testHistoryCommit(1, 1, 2)
	key := historyCommitSourceIdentity(target)
	if allocs := testing.AllocsPerRun(100, func() {
		if !ledger.hasTerminalRecordForSource(key) {
			t.Fatal("source index lost pending lifecycle")
		}
	}); allocs != 0 {
		t.Fatalf("source lookup allocated %v objects; detached payload copies must stay off the hot path", allocs)
	}
	if err := ledger.MarkInFlight(target.Token); err != nil {
		t.Fatalf("MarkInFlight: %v", err)
	}
	if err := ledger.Ack(target.Token, 10, target.LayoutGeneration); err != nil {
		t.Fatalf("Ack: %v", err)
	}
	if !ledger.hasTerminalRecordForSource(key) {
		t.Fatal("acked source lifecycle was lost")
	}
}

func TestTerminalEffectResultsKeepAckAndPartialFailureDistinct(t *testing.T) {
	ack := TerminalEffectAck{Token: 17, Frame: 4}.AsAction()
	if ack.Token != 17 || ack.Err != nil || ack.MayHavePartiallyWritten {
		t.Fatalf("ack action = %+v", ack)
	}
	failed := (TerminalEffectFailed{Token: 18, MayHavePartiallyWritten: true}).AsAction()
	if failed.Token != 18 || !errors.Is(failed.Err, ErrUIActionEffectFailed) || !failed.MayHavePartiallyWritten {
		t.Fatalf("failed action = %+v", failed)
	}
}
