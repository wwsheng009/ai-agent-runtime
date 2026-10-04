package ui

import (
	"bytes"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/vt"
)

// A still-mutable cell's overflow handoff must cross the physical writer
// without becoming a resident of the primary history region: the finalized
// tail the layout keeps above the active band must not be evicted by the live
// cell that follows it.
func TestTerminalSessionActiveOverflowArchivesBeyondRetainedHistoryTail(t *testing.T) {
	var output bytes.Buffer
	session := NewTerminalSession(&output)
	plan := terminalSessionPlan(1, 24, 5, 3, LeaseState{})
	if result := session.Flush(plan); result.Err != nil || !result.FullRepaint {
		t.Fatalf("initial frame = %#v", result)
	}

	finalized := terminalSessionCommit(1,
		render.Line{Spans: []render.Span{{Text: "final-one"}}},
		render.Line{Spans: []render.Span{{Text: "final-two"}}},
		render.Line{Spans: []render.Span{{Text: "final-three"}}},
	)
	if result := session.FlushTransaction(TerminalTransactionPlan{Frame: plan, History: &finalized}); result.History == nil || result.History.Err != nil || result.History.Deferred {
		t.Fatalf("finalized history transaction = %#v", result)
	}

	active := terminalSessionCommit(1,
		render.Line{Spans: []render.Span{{Text: "active-head-one"}}},
		render.Line{Spans: []render.Span{{Text: "active-head-two"}}},
	)
	active.Origin = HistoryCommitActive
	active.Token = 2
	if result := session.FlushTransaction(TerminalTransactionPlan{Frame: plan, History: &active}); result.History == nil || result.History.Err != nil || result.History.Deferred {
		t.Fatalf("active history transaction = %#v", result)
	}

	screen := vt.NewScreen(24, 5)
	screen.Feed(output.String())
	if got := strings.Join(screen.Lines(1, 3), "\n"); got != "final-one\nfinal-two\nfinal-three" {
		t.Fatalf("finalized tail displaced by active overflow = %q\n%s", got, screen.Dump())
	}
	scrollback := strings.Join(screen.ScrollbackLines(), "\n")
	for _, want := range []string{"active-head-one", "active-head-two"} {
		if count := strings.Count(scrollback, want); count != 1 {
			t.Fatalf("active overflow row %q archived %d times, want exactly once:\n%s", want, count, screen.Dump())
		}
	}
	if strings.Contains(scrollback, "final-") {
		t.Fatalf("archiving leaked retained finalized rows into native scrollback: %q", scrollback)
	}
}

// An archive longer than the history region must cross the writer in document
// order and leave the retained tail sticky top-aligned: rows have reached
// native scrollback, so the resident suffix must stay contiguous with them.
func TestTerminalSessionActiveOverflowArchiveChunksLargerThanRegion(t *testing.T) {
	var output bytes.Buffer
	session := NewTerminalSession(&output)
	plan := terminalSessionPlan(1, 24, 6, 2, LeaseState{})
	if result := session.Flush(plan); result.Err != nil || !result.FullRepaint {
		t.Fatalf("initial frame = %#v", result)
	}

	finalized := terminalSessionCommit(1, render.Line{Spans: []render.Span{{Text: "keep-me"}}})
	if result := session.FlushTransaction(TerminalTransactionPlan{Frame: plan, History: &finalized}); result.History == nil || result.History.Err != nil || result.History.Deferred {
		t.Fatalf("finalized history transaction = %#v", result)
	}

	active := terminalSessionCommit(1,
		render.Line{Spans: []render.Span{{Text: "archive-one"}}},
		render.Line{Spans: []render.Span{{Text: "archive-two"}}},
		render.Line{Spans: []render.Span{{Text: "archive-three"}}},
		render.Line{Spans: []render.Span{{Text: "archive-four"}}},
		render.Line{Spans: []render.Span{{Text: "archive-five"}}},
	)
	active.Origin = HistoryCommitActive
	active.Token = 2
	if result := session.FlushTransaction(TerminalTransactionPlan{Frame: plan, History: &active}); result.History == nil || result.History.Err != nil || result.History.Deferred {
		t.Fatalf("active history transaction = %#v", result)
	}

	screen := vt.NewScreen(24, 6)
	screen.Feed(output.String())
	if got := screen.Line(1); got != "keep-me" {
		t.Fatalf("retained tail after oversized archive = %q, want top-aligned keep-me\n%s", got, screen.Dump())
	}
	if got := screen.Line(2); strings.TrimSpace(got) != "" {
		t.Fatalf("row below the archived tail must be empty, got %q\n%s", got, screen.Dump())
	}
	want := []string{"archive-one", "archive-two", "archive-three", "archive-four", "archive-five"}
	if got := screen.ScrollbackLines(); !equalTrimmedLines(got, want) {
		t.Fatalf("archived order = %q, want %q\n%s", got, want, screen.Dump())
	}
}

func equalTrimmedLines(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if strings.TrimRight(got[index], " ") != want[index] {
			return false
		}
	}
	return true
}
