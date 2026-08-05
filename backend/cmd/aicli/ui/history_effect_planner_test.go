package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// TestPlanMutableActiveCellHistoryCommits covers the overflow handoff planner
// for a still-mutable plain cell: blank lines (including the trailing newline
// of a streamed body) must not abort the whole prefix, Markdown stays out of
// scope, and an already-acknowledged source offset resumes the projection.
func TestPlanMutableActiveCellHistoryCommits(t *testing.T) {
	geometry := GeometryState{Width: 80, Height: 12}

	newActive := func(source string, ackedEnd int) ActiveCellState {
		return ActiveCellState{
			CellID:   41,
			Revision: 7,
			Kind:     scene.KindAssistant,
			Phase:    ActiveCellMutable,
			Source:   source,
			Stable:   SourceRange{Start: 0, End: len(source)},
			Acked:    SourceRange{Start: 0, End: ackedEnd},
		}
	}

	t.Run("blank lines and trailing newline do not abort the prefix", func(t *testing.T) {
		lines := make([]string, 0, 31)
		for index := 0; index < 30; index++ {
			if index == 10 {
				lines = append(lines, "")
			}
			lines = append(lines, fmt.Sprintf("mutable-row-%03d", index))
		}
		source := strings.Join(lines, "\n") + "\n"
		active := newActive(source, 0)

		commits := planMutableActiveCellHistoryCommits(active, geometry, 1)
		if len(commits) == 0 {
			t.Fatalf("expected a non-empty overflow prefix, got none")
		}
		if commits[0].Lines[0].Spans[0].Text != "mutable-row-000" {
			t.Fatalf("first handoff row = %q, want mutable-row-000", commits[0].Lines[0].Spans[0].Text)
		}
		wantRows := len(strings.Split(source, "\n")) - ActiveBandRows(geometry.Height)
		if len(commits) != wantRows {
			t.Fatalf("handoff rows = %d, want %d", len(commits), wantRows)
		}
		// Rows are contiguous and ordered by display row.
		for index, commit := range commits {
			if commit.DisplayRange.Start != index || commit.DisplayRange.End != index+1 {
				t.Fatalf("commit %d display range = %+v, want contiguous rows", index, commit.DisplayRange)
			}
			if commit.SourceRange.Start >= commit.SourceRange.End {
				t.Fatalf("commit %d source range %+v is empty", index, commit.SourceRange)
			}
		}
		// The last handoff row is the band's first visible row minus one.
		wantLast := strings.Split(source, "\n")[wantRows-1]
		last := commits[len(commits)-1]
		if last.Lines[0].Spans[0].Text != wantLast {
			t.Fatalf("last handoff row = %q, want %q", last.Lines[0].Spans[0].Text, wantLast)
		}
	})

	t.Run("markdown stays out of scope", func(t *testing.T) {
		source := "# heading\n\nplain body\n" + strings.Repeat("x\n", 30)
		active := newActive(source, 0)
		if commits := planMutableActiveCellHistoryCommits(active, geometry, 1); len(commits) != 0 {
			t.Fatalf("markdown source produced %d handoff commits, want none", len(commits))
		}
	})

	t.Run("body that fits the band budget produces no commits", func(t *testing.T) {
		active := newActive(strings.Join([]string{"one", "two", "three"}, "\n"), 0)
		if commits := planMutableActiveCellHistoryCommits(active, geometry, 1); len(commits) != 0 {
			t.Fatalf("short source produced %d handoff commits, want none", len(commits))
		}
	})

	t.Run("acknowledged offset resumes the projection", func(t *testing.T) {
		source := strings.Join([]string{
			"mutable-row-000", "mutable-row-001", "mutable-row-002",
			"mutable-row-003", "mutable-row-004", "mutable-row-005",
			"mutable-row-006", "mutable-row-007", "mutable-row-008",
			"mutable-row-009", "mutable-row-010", "mutable-row-011",
		}, "\n")
		// ack through row-007 (its trailing newline), leaving rows 008..011 live.
		ackedEnd := strings.Index(source, "mutable-row-008")
		active := newActive(source, ackedEnd)

		commits := planMutableActiveCellHistoryCommits(active, geometry, 1)
		if len(commits) != 0 {
			t.Fatalf("resumed projection produced %d commits, want none (tail fits the band)", len(commits))
		}
	})

	t.Run("partially acknowledged short body hands off the remaining prefix", func(t *testing.T) {
		source := strings.Join([]string{
			"mutable-row-000", "mutable-row-001", "mutable-row-002",
			"mutable-row-003", "mutable-row-004", "mutable-row-005",
			"mutable-row-006", "mutable-row-007", "mutable-row-008",
			"mutable-row-009", "mutable-row-010", "mutable-row-011",
			"mutable-row-012", "mutable-row-013", "mutable-row-014",
			"mutable-row-015", "mutable-row-016", "mutable-row-017",
			"mutable-row-018", "mutable-row-019", "mutable-row-020",
		}, "\n")
		ackedEnd := strings.Index(source, "mutable-row-010")
		active := newActive(source, ackedEnd)

		commits := planMutableActiveCellHistoryCommits(active, geometry, 1)
		want := 21 - ActiveBandRows(geometry.Height) - 10 // rows 010..020, minus the live tail
		if len(commits) != want {
			t.Fatalf("resumed handoff rows = %d, want %d", len(commits), want)
		}
		if commits[0].Lines[0].Spans[0].Text != "mutable-row-010" {
			t.Fatalf("first resumed handoff row = %q, want mutable-row-010", commits[0].Lines[0].Spans[0].Text)
		}
	})
}
