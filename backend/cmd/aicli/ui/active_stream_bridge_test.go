package ui

import (
	"errors"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/cell"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

func TestActiveCellStateFromSourceSnapshotUsesExplicitEffectRanges(t *testing.T) {
	active, err := ActiveCellStateFromSourceSnapshot(ActiveStreamSourceSnapshot{
		Active:       true,
		Kind:         cell.ActiveAssistant,
		Source:       "abcdef",
		StableEnd:    6,
		CommittedEnd: 6,
	}, 42, 7, 5, 3)
	if err != nil {
		t.Fatalf("map source snapshot: %v", err)
	}
	if active.CellID != 42 || active.Revision != 7 || active.Kind != scene.KindAssistant || active.Phase != ActiveCellMutable {
		t.Fatalf("mapped identity = %+v", active)
	}
	if active.Stable.End != 6 || active.Enqueued.End != 5 || active.Acked.End != 3 {
		t.Fatalf("mapped ranges = %+v", active)
	}
	// Controller-local CommittedEnd must never be guessed as a physical Ack.
	if active.Acked.End == 6 {
		t.Fatalf("controller committed progress leaked into Acked: %+v", active)
	}
}

func TestUpdateActiveCellActionFromSourceSnapshotCarriesAppStateRanges(t *testing.T) {
	current := activeRangeFixture("abc", 4, 3, 2, 1)
	action, err := UpdateActiveCellActionFromSourceSnapshot(current, ActiveStreamSourceSnapshot{
		Active:    true,
		Kind:      cell.ActiveAssistant,
		Source:    "abcdef",
		StableEnd: 6,
	})
	if err != nil {
		t.Fatalf("build update action: %v", err)
	}
	if action.ExpectedCellID != current.CellID || action.ExpectedRevision != current.Revision {
		t.Fatalf("update fence = %d/%d", action.ExpectedCellID, action.ExpectedRevision)
	}
	if action.Active.Revision != current.Revision+1 || action.Active.Source != "abcdef" {
		t.Fatalf("update payload = %+v", action.Active)
	}
	if action.Active.Enqueued.End != current.Enqueued.End || action.Active.Acked.End != current.Acked.End {
		t.Fatalf("AppState effect ranges were not carried forward: %+v", action.Active)
	}
}

func TestUpdateActiveCellActionFromSourceSnapshotRejectsMissingMount(t *testing.T) {
	_, err := UpdateActiveCellActionFromSourceSnapshot(ActiveCellState{}, ActiveStreamSourceSnapshot{
		Active:    true,
		Kind:      cell.ActiveAssistant,
		Source:    "source",
		StableEnd: 6,
	})
	if !errors.Is(err, ErrInvalidActiveCellRanges) {
		t.Fatalf("missing mount error = %v, want ErrInvalidActiveCellRanges", err)
	}
}

func TestUpdateActiveCellActionFromSourceSnapshotClearsRangesForCorrection(t *testing.T) {
	current := activeRangeFixture("old source", 4, len("old source"), 5, 3)
	action, err := UpdateActiveCellActionFromSourceSnapshot(current, ActiveStreamSourceSnapshot{
		Active:    true,
		Kind:      cell.ActiveAssistant,
		Source:    "replacement",
		StableEnd: len("replacement"),
	})
	if err != nil {
		t.Fatalf("build correction action: %v", err)
	}
	if action.Active.Enqueued.End != 0 || action.Active.Acked.End != 0 {
		t.Fatalf("correction retained prior effect ranges: %+v", action.Active)
	}
}

func TestActiveCellStateFromSourceSnapshotRejectsToolDisplayAsSource(t *testing.T) {
	_, err := ActiveCellStateFromSourceSnapshot(ActiveStreamSourceSnapshot{
		Active: true,
		Kind:   cell.ActiveTool,
		Source: "running: dir",
	}, 9, 1, 0, 0)
	if !errors.Is(err, ErrInvalidActiveCellRanges) {
		t.Fatalf("tool source error = %v, want ErrInvalidActiveCellRanges", err)
	}

	active, err := ActiveCellStateFromSourceSnapshot(ActiveStreamSourceSnapshot{
		Active: true,
		Kind:   cell.ActiveTool,
	}, 9, 1, 0, 0)
	if err != nil || active.Kind != scene.KindToolChain || active.Source != "" {
		t.Fatalf("tool overlay mapping = %+v, err=%v", active, err)
	}
}

func TestActiveCellStateFromSourceSnapshotRejectsInvalidIdentityAndBoundary(t *testing.T) {
	base := ActiveStreamSourceSnapshot{
		Active:    true,
		Kind:      cell.ActiveAssistant,
		Source:    "中文",
		StableEnd: len("中文"),
	}
	cases := []struct {
		name     string
		id       scene.CellID
		revision uint64
		enqueued int
		acked    int
		snapshot ActiveStreamSourceSnapshot
	}{
		{name: "missing id", id: 0, revision: 1, snapshot: base},
		{name: "missing revision", id: 1, revision: 0, snapshot: base},
		{name: "backward ack", id: 1, revision: 1, enqueued: 1, acked: 2, snapshot: base},
		{name: "rune split", id: 1, revision: 1, enqueued: 1, snapshot: base},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ActiveCellStateFromSourceSnapshot(tc.snapshot, tc.id, tc.revision, tc.enqueued, tc.acked)
			if !errors.Is(err, ErrInvalidActiveCellRanges) {
				t.Fatalf("error = %v, want ErrInvalidActiveCellRanges", err)
			}
		})
	}
}
