package ui

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/renderengine"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/vt"
)

func terminalSessionPlan(generation uint64, width, height, outputBottom int, lease LeaseState) TerminalFramePlan {
	rows := make([]AppScreenRow, height)
	for index := range rows {
		rows[index] = AppScreenRow{Row: index + 1, Text: "row-" + string(rune('a'+index))}
	}
	return TerminalFramePlan{
		LayoutGeneration: generation,
		Geometry:         GeometryState{Width: width, Height: height, Generation: generation},
		Lease:            lease,
		OutputBottomRow:  outputBottom,
		Rows:             rows,
	}
}

func terminalSessionCommit(generation uint64, lines ...render.Line) HistoryCommit {
	return HistoryCommit{
		Token:            1,
		CellID:           scene.CellID(7),
		Revision:         3,
		SourceRange:      SourceRange{Start: 0, End: 9},
		DisplayRange:     DisplayRange{Start: 0, End: len(lines)},
		LayoutGeneration: generation,
		Lines:            lines,
	}
}

func TestTerminalSessionFlushCommitsOnlyAfterCompleteWrite(t *testing.T) {
	var output bytes.Buffer
	session := NewTerminalSession(&output)
	plan := terminalSessionPlan(1, 20, 6, 4, LeaseState{})

	first := session.Flush(plan)
	if first.Err != nil || first.Deferred || !first.FullRepaint || first.Frame != 1 {
		t.Fatalf("first flush = %#v", first)
	}
	state := session.ProjectionState()
	if state.Validity != renderengine.ProjectionKnown || state.Frame != 1 || state.OutputBottomRow != 4 {
		t.Fatalf("state after complete write = %#v", state)
	}
	if state.Viewport != (ViewportArea{Top: 5, Height: 2, Width: 20}) {
		t.Fatalf("inline viewport = %#v", state.Viewport)
	}
	if output.Len() == 0 {
		t.Fatal("full repaint emitted no terminal bytes")
	}

	before := output.Len()
	second := session.Flush(plan)
	if second.Err != nil || second.FullRepaint || second.Frame != 2 {
		t.Fatalf("incremental flush = %#v", second)
	}
	if output.Len() != before {
		t.Fatalf("unchanged frame wrote bytes: before=%d after=%d", before, output.Len())
	}
}

func TestTerminalSessionTransactionInsertsHistoryAboveInlineViewport(t *testing.T) {
	var output bytes.Buffer
	session := NewTerminalSession(&output)
	initial := terminalSessionPlan(1, 24, 5, 3, LeaseState{})
	initial.Rows[0].Text = "outgoing history"
	initial.Rows[1].Text = "retained middle"
	initial.Rows[2].Text = "retained latest"
	initial.Rows[3].Text = "> prompt"
	initial.Rows[4].Text = "status"
	if result := session.Flush(initial); result.Err != nil || !result.FullRepaint {
		t.Fatalf("initial frame = %#v", result)
	}

	target := initial
	target.Rows = append([]AppScreenRow(nil), initial.Rows...)
	commit := terminalSessionCommit(1, render.Line{Spans: []render.Span{{Text: "outgoing history"}}})
	result := session.FlushTransaction(TerminalTransactionPlan{Frame: target, History: &commit})
	if result.Frame.Err != nil || result.History == nil || result.History.Err != nil || result.History.Deferred || result.History.Frame == 0 {
		t.Fatalf("transaction = %#v", result)
	}

	screen := vt.NewScreen(24, 5)
	screen.Feed(output.String())
	if !strings.Contains(output.String(), "\x1b[1;3r") || strings.Contains(output.String(), "\x1b[1;5r") {
		t.Fatalf("history did not use the region above the inline viewport: %q", output.String())
	}
	if got := screen.Lines(3, 5); strings.Join(got, "\n") != "outgoing history\n> prompt\nstatus" {
		t.Fatalf("history/viewport boundary = %#v, screen:\n%s", got, screen.Dump())
	}
	if scrollback := strings.Join(screen.ScrollbackLines(), "\n"); strings.Contains(scrollback, "> prompt") || strings.Contains(scrollback, "status") {
		t.Fatalf("inline viewport leaked into scrollback: %q", scrollback)
	}
}

func TestTerminalSessionBootstrapBatchPushesHistoryOverflowIntoScrollback(t *testing.T) {
	var output bytes.Buffer
	session := NewTerminalSession(&output)
	initial := terminalSessionPlan(1, 24, 5, 3, LeaseState{})
	initial.Rows[0].Text = "old frame one"
	initial.Rows[1].Text = "old frame two"
	initial.Rows[2].Text = "old frame three"
	if result := session.Flush(initial); result.Err != nil || !result.FullRepaint {
		t.Fatalf("initial frame = %#v", result)
	}

	target := initial
	target.Rows = append([]AppScreenRow(nil), initial.Rows...)
	target.Rows[0].Text = "retained tail one"
	target.Rows[1].Text = "retained tail two"
	target.Rows[2].Text = "retained tail three"
	commit := terminalSessionCommit(1,
		render.Line{Spans: []render.Span{{Text: "bootstrap history one"}}},
		render.Line{Spans: []render.Span{{Text: "bootstrap history two"}}},
		render.Line{Spans: []render.Span{{Text: "bootstrap history three"}}},
		render.Line{Spans: []render.Span{{Text: "bootstrap history four"}}},
	)
	result := session.FlushTransaction(TerminalTransactionPlan{
		Frame:            target,
		History:          &commit,
		BootstrapHistory: []HistoryCommit{commit},
	})
	if result.Frame.Err != nil || result.History == nil || result.History.Err != nil || result.History.Deferred {
		t.Fatalf("bootstrap transaction frame=%#v history=%+v", result.Frame, result.History)
	}
	if len(result.History.Delivered) != 1 || result.History.Delivered[0].Token != commit.Token {
		t.Fatalf("bootstrap delivery = %#v, want commit token %d", result.History.Delivered, commit.Token)
	}

	screen := vt.NewScreen(24, 5)
	screen.Feed(output.String())
	if scrollback := strings.Join(screen.ScrollbackLines(), "\n"); !strings.Contains(scrollback, "bootstrap history one") {
		t.Fatalf("bootstrap overflow did not reach native scrollback:\n%s", screen.Dump())
	}
	if got := strings.Join(screen.Lines(3, 5), "\n"); got != "bootstrap history four\nrow-d\nrow-e" {
		t.Fatalf("history/inline tail after bootstrap = %q, screen:\n%s", got, screen.Dump())
	}
}

func TestTerminalSessionViewportExpansionPreservesOldHistoryRows(t *testing.T) {
	var output bytes.Buffer
	session := NewTerminalSession(&output)
	initial := terminalSessionPlan(1, 28, 6, 4, LeaseState{})
	initial.Rows[4].Text = "PROMPT-EXPAND"
	initial.Rows[5].Text = "STATUS-EXPAND"
	if result := session.Flush(initial); result.Err != nil {
		t.Fatalf("initial viewport = %#v", result)
	}
	commit := terminalSessionCommit(1,
		render.Line{Spans: []render.Span{{Text: "EXPAND-HISTORY-1"}}},
		render.Line{Spans: []render.Span{{Text: "EXPAND-HISTORY-2"}}},
		render.Line{Spans: []render.Span{{Text: "EXPAND-HISTORY-3"}}},
		render.Line{Spans: []render.Span{{Text: "EXPAND-HISTORY-4"}}},
		render.Line{Spans: []render.Span{{Text: "EXPAND-HISTORY-5"}}},
	)
	if result := session.FlushTransaction(TerminalTransactionPlan{Frame: initial, History: &commit}); result.Frame.Err != nil || result.History == nil || result.History.Err != nil {
		t.Fatalf("history insert = %#v", result)
	}

	expanded := terminalSessionPlan(1, 28, 6, 2, LeaseState{})
	expanded.Rows[4].Text = "PROMPT-EXPAND"
	expanded.Rows[5].Text = "STATUS-EXPAND"
	before := output.Len()
	if result := session.Flush(expanded); result.Err != nil || !result.FullRepaint {
		t.Fatalf("expanded viewport = %#v", result)
	}
	transition := output.String()[before:]
	if !strings.Contains(transition, "\x1b[1;4r\x1b[4;1H\r\n\r\n") {
		t.Fatalf("viewport expansion did not scroll exactly two occupied rows: %q", transition)
	}
	if strings.Contains(transition, "EXPAND-HISTORY-") {
		t.Fatalf("viewport expansion replayed already-resident history: %q", transition)
	}

	screen := vt.NewScreen(28, 6)
	screen.Feed(output.String())
	physical := strings.Join(append(screen.ScrollbackLines(), screen.Lines(1, 6)...), "\n")
	for index := 1; index <= 5; index++ {
		marker := fmt.Sprintf("EXPAND-HISTORY-%d", index)
		if count := strings.Count(physical, marker); count != 1 {
			t.Fatalf("expanded history marker %q count=%d, screen:\n%s", marker, count, screen.Dump())
		}
	}
}

func TestTerminalSessionViewportExpansionDoesNotCommitBlankScrollbackRows(t *testing.T) {
	const width, height = 28, 6
	var output bytes.Buffer
	session := NewTerminalSession(&output)
	initial := terminalSessionPlan(1, width, height, 4, LeaseState{})
	initial.Rows[4].Text = "PROMPT-NO-BLANK"
	initial.Rows[5].Text = "STATUS-NO-BLANK"
	if result := session.Flush(initial); result.Err != nil {
		t.Fatalf("initial viewport = %#v", result)
	}

	commit := terminalSessionCommit(1,
		render.Line{Spans: []render.Span{{Text: "NO-BLANK-HISTORY-1"}}},
		render.Line{Spans: []render.Span{{Text: "NO-BLANK-HISTORY-2"}}},
	)
	if result := session.FlushTransaction(TerminalTransactionPlan{Frame: initial, History: &commit}); result.Frame.Err != nil || result.History == nil || result.History.Err != nil {
		t.Fatalf("underfilled history insert = %#v", result)
	}

	expanded := terminalSessionPlan(1, width, height, 2, LeaseState{})
	expanded.Rows[2].Text = "ACTIVE-BAND-GREW"
	expanded.Rows[4].Text = "PROMPT-NO-BLANK"
	expanded.Rows[5].Text = "STATUS-NO-BLANK"
	if result := session.Flush(expanded); result.Err != nil {
		t.Fatalf("expanded viewport = %#v", result)
	}

	screen := vt.NewScreen(width, height)
	screen.Feed(output.String())
	if scrollback := screen.ScrollbackLines(); len(scrollback) != 0 {
		t.Fatalf("underfilled viewport growth committed non-semantic scrollback rows: %#v\n%s", scrollback, screen.Dump())
	}
	physical := strings.Join(append(screen.ScrollbackLines(), screen.Lines(1, height)...), "\n")
	for _, marker := range []string{"PROMPT-NO-BLANK", "STATUS-NO-BLANK"} {
		if count := strings.Count(physical, marker); count != 1 {
			t.Fatalf("marker %q count=%d after viewport growth\n%s", marker, count, screen.Dump())
		}
	}
}

func TestTerminalSessionViewportExpansionScrollsOnlySemanticOverflow(t *testing.T) {
	const width, height = 28, 6
	var output bytes.Buffer
	session := NewTerminalSession(&output)
	initial := terminalSessionPlan(1, width, height, 4, LeaseState{})
	if result := session.Flush(initial); result.Err != nil {
		t.Fatalf("initial viewport = %#v", result)
	}
	commit := terminalSessionCommit(1,
		render.Line{Spans: []render.Span{{Text: "SEMANTIC-OVERFLOW-1"}}},
		render.Line{Spans: []render.Span{{Text: "SEMANTIC-OVERFLOW-2"}}},
		render.Line{Spans: []render.Span{{Text: "SEMANTIC-OVERFLOW-3"}}},
		render.Line{Spans: []render.Span{{Text: "SEMANTIC-OVERFLOW-4"}}},
		render.Line{Spans: []render.Span{{Text: "SEMANTIC-OVERFLOW-5"}}},
	)
	if result := session.FlushTransaction(TerminalTransactionPlan{Frame: initial, History: &commit}); result.Frame.Err != nil || result.History == nil || result.History.Err != nil {
		t.Fatalf("history insert = %#v", result)
	}

	expanded := terminalSessionPlan(1, width, height, 2, LeaseState{})
	if result := session.Flush(expanded); result.Err != nil {
		t.Fatalf("expanded viewport = %#v", result)
	}
	screen := vt.NewScreen(width, height)
	screen.Feed(output.String())
	if got, want := screen.ScrollbackLines(), []string{
		"SEMANTIC-OVERFLOW-1", "SEMANTIC-OVERFLOW-2", "SEMANTIC-OVERFLOW-3",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("scrollback = %#v, want only semantic overflow %#v\n%s", got, want, screen.Dump())
	}
	visible := strings.Join(screen.Lines(1, height), "\n")
	for _, marker := range []string{"SEMANTIC-OVERFLOW-4", "SEMANTIC-OVERFLOW-5"} {
		if strings.Count(visible, marker) != 1 {
			t.Fatalf("visible tail missing or duplicated %q\n%s", marker, screen.Dump())
		}
	}
}

func TestTerminalSessionSequentialUnderfillScrollsOnlyAfterCapacityIsFull(t *testing.T) {
	const width, height = 24, 6
	var output bytes.Buffer
	session := NewTerminalSession(&output)
	plan := terminalSessionPlan(1, width, height, 4, LeaseState{})
	if result := session.Flush(plan); result.Err != nil {
		t.Fatalf("initial frame = %#v", result)
	}

	first := terminalSessionCommit(1,
		render.Line{Spans: []render.Span{{Text: "FILL-1"}}},
		render.Line{Spans: []render.Span{{Text: "FILL-2"}}},
	)
	if result := session.CommitHistory(first); result.Err != nil || result.Deferred {
		t.Fatalf("first underfill = %#v", result)
	}
	second := terminalSessionCommit(1,
		render.Line{Spans: []render.Span{{Text: "FILL-3"}}},
		render.Line{Spans: []render.Span{{Text: "FILL-4"}}},
	)
	second.Token = 2
	if result := session.CommitHistory(second); result.Err != nil || result.Deferred {
		t.Fatalf("fill to capacity = %#v", result)
	}

	screen := vt.NewScreen(width, height)
	screen.Feed(output.String())
	if got := screen.ScrollbackLines(); len(got) != 0 {
		t.Fatalf("filling free capacity created scrollback: %#v\n%s", got, screen.Dump())
	}
	if got := strings.Join(screen.Lines(1, 4), "\n"); got != "FILL-1\nFILL-2\nFILL-3\nFILL-4" {
		t.Fatalf("filled history region = %q\n%s", got, screen.Dump())
	}

	third := terminalSessionCommit(1, render.Line{Spans: []render.Span{{Text: "FILL-5"}}})
	third.Token = 3
	if result := session.CommitHistory(third); result.Err != nil || result.Deferred {
		t.Fatalf("overflow append = %#v", result)
	}
	screen = vt.NewScreen(width, height)
	screen.Feed(output.String())
	if got, want := screen.ScrollbackLines(), []string{"FILL-1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("overflow scrollback = %#v, want %#v\n%s", got, want, screen.Dump())
	}
	if got := strings.Join(screen.Lines(1, 4), "\n"); got != "FILL-2\nFILL-3\nFILL-4\nFILL-5" {
		t.Fatalf("overflow visible tail = %q\n%s", got, screen.Dump())
	}
	if state := session.ProjectionState(); !state.HistoryKnown || state.HistoryRows != 4 {
		t.Fatalf("history projection = %#v", state)
	}
}

func TestTerminalSessionViewportContractionKeepsNativeHistoryContiguous(t *testing.T) {
	const width, height = 30, 6
	var output bytes.Buffer
	session := NewTerminalSession(&output)
	streaming := terminalSessionPlan(1, width, height, 3, LeaseState{})
	if result := session.Flush(streaming); result.Err != nil {
		t.Fatalf("initial streaming frame = %#v", result)
	}

	prefix := terminalSessionCommit(1,
		render.Line{Spans: []render.Span{{Text: "CONTIGUOUS-01"}}},
		render.Line{Spans: []render.Span{{Text: "CONTIGUOUS-02"}}},
		render.Line{Spans: []render.Span{{Text: "CONTIGUOUS-03"}}},
		render.Line{Spans: []render.Span{{Text: "CONTIGUOUS-04"}}},
		render.Line{Spans: []render.Span{{Text: "CONTIGUOUS-05"}}},
	)
	if result := session.CommitHistory(prefix); result.Err != nil || result.Deferred {
		t.Fatalf("streaming prefix handoff = %#v", result)
	}

	final := terminalSessionPlan(1, width, height, 5, LeaseState{})
	final.Rows[5].Text = "CONTIGUOUS-STATUS"
	suffix := terminalSessionCommit(1,
		render.Line{Spans: []render.Span{{Text: "CONTIGUOUS-06"}}},
	)
	suffix.Token = 2
	result := session.FlushTransaction(TerminalTransactionPlan{Frame: final, History: &suffix})
	if result.Frame.Err != nil || result.History == nil || result.History.Err != nil || result.History.Deferred {
		t.Fatalf("final suffix transaction = %#v", result)
	}

	screen := vt.NewScreen(width, height)
	screen.Feed(output.String())
	physical := append(screen.ScrollbackLines(), screen.Lines(1, height)...)
	for index := 0; index < 6; index++ {
		want := fmt.Sprintf("CONTIGUOUS-%02d", index+1)
		got := ""
		if index < len(physical) {
			got = strings.TrimSpace(physical[index])
		}
		if got != want {
			t.Fatalf("physical row %d = %q, want %q without scrollback/viewport headroom\n%s",
				index, got, want, screen.Dump())
		}
	}
	if got := strings.TrimSpace(screen.Lines(5, 5)[0]); got != "" {
		t.Fatalf("unused capacity must follow the semantic tail, got row 5 = %q\n%s", got, screen.Dump())
	}
	if got := strings.TrimSpace(screen.Lines(6, 6)[0]); got != "CONTIGUOUS-STATUS" {
		t.Fatalf("bottom viewport status = %q\n%s", got, screen.Dump())
	}
}

func TestTerminalSessionViewportExpansionMovesHeadroomBeforeSemanticOverflow(t *testing.T) {
	const width, height = 26, 6
	var output bytes.Buffer
	session := NewTerminalSession(&output)
	initial := terminalSessionPlan(1, width, height, 4, LeaseState{})
	if result := session.Flush(initial); result.Err != nil {
		t.Fatalf("initial frame = %#v", result)
	}
	commit := terminalSessionCommit(1,
		render.Line{Spans: []render.Span{{Text: "MIXED-1"}}},
		render.Line{Spans: []render.Span{{Text: "MIXED-2"}}},
		render.Line{Spans: []render.Span{{Text: "MIXED-3"}}},
	)
	if result := session.CommitHistory(commit); result.Err != nil || result.Deferred {
		t.Fatalf("history insert = %#v", result)
	}
	before := output.Len()
	expanded := terminalSessionPlan(1, width, height, 2, LeaseState{})
	if result := session.Flush(expanded); result.Err != nil {
		t.Fatalf("expanded frame = %#v", result)
	}
	transition := output.String()[before:]
	if !strings.Contains(transition, "\x1b[1;1H\x1b[1M") || !strings.Contains(transition, "\x1b[4;1H\r\n") {
		t.Fatalf("mixed expansion omitted headroom delete or semantic scroll: %q", transition)
	}
	screen := vt.NewScreen(width, height)
	screen.Feed(output.String())
	if got, want := screen.ScrollbackLines(), []string{"MIXED-1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("mixed expansion scrollback = %#v, want %#v\n%s", got, want, screen.Dump())
	}
	if got := strings.Join(screen.Lines(1, 2), "\n"); got != "MIXED-2\nMIXED-3" {
		t.Fatalf("mixed expansion visible tail = %q\n%s", got, screen.Dump())
	}
}

func TestTerminalSessionViewportContractionClearsFormerPromptRowsBeforeHistory(t *testing.T) {
	var output bytes.Buffer
	session := NewTerminalSession(&output)
	initial := terminalSessionPlan(1, 30, 6, 2, LeaseState{})
	initial.Rows[2].Text = "OLD-PROMPT-MUST-NOT-SCROLL"
	if result := session.Flush(initial); result.Err != nil {
		t.Fatalf("initial viewport = %#v", result)
	}

	contracted := terminalSessionPlan(1, 30, 6, 4, LeaseState{})
	contracted.Rows[4].Text = "CURRENT-PROMPT"
	contracted.Rows[5].Text = "CURRENT-STATUS"
	commit := terminalSessionCommit(1,
		render.Line{Spans: []render.Span{{Text: "CONTRACT-HISTORY-1"}}},
		render.Line{Spans: []render.Span{{Text: "CONTRACT-HISTORY-2"}}},
		render.Line{Spans: []render.Span{{Text: "CONTRACT-HISTORY-3"}}},
		render.Line{Spans: []render.Span{{Text: "CONTRACT-HISTORY-4"}}},
		render.Line{Spans: []render.Span{{Text: "CONTRACT-HISTORY-5"}}},
	)
	before := output.Len()
	result := session.FlushTransaction(TerminalTransactionPlan{Frame: contracted, History: &commit})
	if result.Frame.Err != nil || result.History == nil || result.History.Err != nil {
		t.Fatalf("contracted transaction = %#v", result)
	}
	transaction := output.String()[before:]
	clear := strings.Index(transaction, "\x1b[3;1H\x1b[0m\x1b[K")
	history := strings.Index(transaction, "\x1b[s\x1b[1;4r")
	if clear < 0 || history < clear {
		t.Fatalf("former viewport rows were not cleared before history scroll: %q", transaction)
	}

	screen := vt.NewScreen(30, 6)
	screen.Feed(output.String())
	scrollback := strings.Join(screen.ScrollbackLines(), "\n")
	if strings.Contains(scrollback, "OLD-PROMPT-MUST-NOT-SCROLL") || strings.Contains(scrollback, "CURRENT-PROMPT") || strings.Contains(scrollback, "CURRENT-STATUS") {
		t.Fatalf("prompt/status leaked into native scrollback: %q\n%s", scrollback, screen.Dump())
	}
}

func TestTerminalSessionViewportContractionMovesResidentHistoryToNewBottom(t *testing.T) {
	const width, height = 28, 6
	var output bytes.Buffer
	session := NewTerminalSession(&output)
	initial := terminalSessionPlan(1, width, height, 2, LeaseState{})
	initial.Rows[2].Text = "OLD-PROMPT-CONTRACT"
	if result := session.Flush(initial); result.Err != nil {
		t.Fatalf("initial frame = %#v", result)
	}
	commit := terminalSessionCommit(1,
		render.Line{Spans: []render.Span{{Text: "MOVE-DOWN-1"}}},
		render.Line{Spans: []render.Span{{Text: "MOVE-DOWN-2"}}},
	)
	if result := session.CommitHistory(commit); result.Err != nil || result.Deferred {
		t.Fatalf("history insert = %#v", result)
	}
	before := output.Len()
	contracted := terminalSessionPlan(1, width, height, 4, LeaseState{})
	contracted.Rows[4].Text = "NEW-PROMPT-CONTRACT"
	contracted.Rows[5].Text = "NEW-STATUS-CONTRACT"
	if result := session.Flush(contracted); result.Err != nil {
		t.Fatalf("contracted frame = %#v", result)
	}
	transition := output.String()[before:]
	clear := strings.Index(transition, "\x1b[3;1H\x1b[0m\x1b[K")
	move := strings.Index(transition, "\x1b[1;1H\x1b[2L")
	if clear < 0 || move < clear {
		t.Fatalf("contraction did not clear former viewport before moving history: %q", transition)
	}
	screen := vt.NewScreen(width, height)
	screen.Feed(output.String())
	if got := screen.ScrollbackLines(); len(got) != 0 {
		t.Fatalf("contraction created scrollback: %#v\n%s", got, screen.Dump())
	}
	if got := strings.Join(screen.Lines(1, 4), "\n"); got != "\n\nMOVE-DOWN-1\nMOVE-DOWN-2" {
		t.Fatalf("contracted history tail = %q\n%s", got, screen.Dump())
	}
	physical := strings.Join(screen.Lines(1, height), "\n")
	if strings.Contains(physical, "OLD-PROMPT-CONTRACT") {
		t.Fatalf("old prompt survived contraction: %q\n%s", physical, screen.Dump())
	}
}

func TestTerminalSessionHistoryGrowthAfterScrollbackKeepsTranscriptContinuous(t *testing.T) {
	const width, height = 32, 8
	var output bytes.Buffer
	session := NewTerminalSession(&output)
	initial := terminalSessionPlan(1, width, height, 4, LeaseState{})
	if result := session.Flush(initial); result.Err != nil {
		t.Fatalf("initial frame = %#v", result)
	}

	first := terminalSessionCommit(1,
		render.Line{Spans: []render.Span{{Text: "CONTINUOUS-1"}}},
		render.Line{Spans: []render.Span{{Text: "CONTINUOUS-2"}}},
		render.Line{Spans: []render.Span{{Text: "CONTINUOUS-3"}}},
		render.Line{Spans: []render.Span{{Text: "CONTINUOUS-4"}}},
		render.Line{Spans: []render.Span{{Text: "CONTINUOUS-5"}}},
	)
	if result := session.CommitHistory(first); result.Err != nil || result.Deferred {
		t.Fatalf("overflowing history insert = %#v", result)
	}

	// Completing an active cell grows the history region. Once a semantic row
	// is already in native scrollback, resident history must stay at row one;
	// otherwise bottom alignment puts blank headroom in the middle of the stream.
	expanded := terminalSessionPlan(1, width, height, 6, LeaseState{})
	if result := session.Flush(expanded); result.Err != nil {
		t.Fatalf("expanded history frame = %#v", result)
	}
	second := terminalSessionCommit(1,
		render.Line{Spans: []render.Span{{Text: "CONTINUOUS-6"}}},
		render.Line{Spans: []render.Span{{Text: "CONTINUOUS-7"}}},
	)
	second.Token = 2
	if result := session.CommitHistory(second); result.Err != nil || result.Deferred {
		t.Fatalf("resident suffix insert = %#v", result)
	}

	screen := vt.NewScreen(width, height)
	screen.Feed(output.String())
	if got, want := screen.ScrollbackLines(), []string{"CONTINUOUS-1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("scrollback = %#v, want %#v\n%s", got, want, screen.Dump())
	}
	if got, want := screen.Lines(1, 6), []string{
		"CONTINUOUS-2", "CONTINUOUS-3", "CONTINUOUS-4",
		"CONTINUOUS-5", "CONTINUOUS-6", "CONTINUOUS-7",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("resident transcript = %#v, want contiguous %#v\n%s", got, want, screen.Dump())
	}
}

func TestTerminalSessionTopAlignedHistorySurvivesViewportShrinkGrowCycle(t *testing.T) {
	const width, height = 32, 8
	var output bytes.Buffer
	session := NewTerminalSession(&output)
	initial := terminalSessionPlan(1, width, height, 6, LeaseState{})
	if result := session.Flush(initial); result.Err != nil {
		t.Fatalf("initial frame = %#v", result)
	}
	lines := make([]render.Line, 8)
	for index := range lines {
		lines[index] = render.Line{Spans: []render.Span{{Text: fmt.Sprintf("CYCLE-%02d", index+1)}}}
	}
	if result := session.CommitHistory(terminalSessionCommit(1, lines...)); result.Err != nil || result.Deferred {
		t.Fatalf("initial overflow = %#v", result)
	}

	// Growing the owned viewport reduces history capacity and must move only
	// semantic prefix rows into scrollback. Shrinking it again leaves the
	// top-aligned suffix in place, with new capacity below it.
	if result := session.Flush(terminalSessionPlan(1, width, height, 4, LeaseState{})); result.Err != nil {
		t.Fatalf("history shrink = %#v", result)
	}
	if result := session.Flush(terminalSessionPlan(1, width, height, 6, LeaseState{})); result.Err != nil {
		t.Fatalf("history regrowth = %#v", result)
	}
	more := terminalSessionCommit(1,
		render.Line{Spans: []render.Span{{Text: "CYCLE-09"}}},
		render.Line{Spans: []render.Span{{Text: "CYCLE-10"}}},
	)
	more.Token = 2
	if result := session.CommitHistory(more); result.Err != nil || result.Deferred {
		t.Fatalf("history append after cycle = %#v", result)
	}

	screen := vt.NewScreen(width, height)
	screen.Feed(output.String())
	if got, want := screen.ScrollbackLines(), []string{"CYCLE-01", "CYCLE-02", "CYCLE-03", "CYCLE-04"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("scrollback after cycle = %#v, want %#v\n%s", got, want, screen.Dump())
	}
	if got, want := screen.Lines(1, 6), []string{
		"CYCLE-05", "CYCLE-06", "CYCLE-07", "CYCLE-08", "CYCLE-09", "CYCLE-10",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("resident history after cycle = %#v, want %#v\n%s", got, want, screen.Dump())
	}
}

func TestTerminalSessionHeightResizeDoesNotReplayOldViewportBoundary(t *testing.T) {
	tests := []struct {
		name          string
		initialHeight int
		initialOutput int
		nextHeight    int
		nextOutput    int
		forbiddenCUP  string
	}{
		{name: "shrink", initialHeight: 6, initialOutput: 4, nextHeight: 4, nextOutput: 2},
		{name: "grow", initialHeight: 4, initialOutput: 2, nextHeight: 6, nextOutput: 4, forbiddenCUP: "\x1b[3;1H"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			session := NewTerminalSession(&output)
			initial := terminalSessionPlan(1, 28, test.initialHeight, test.initialOutput, LeaseState{})
			if result := session.Flush(initial); result.Err != nil {
				t.Fatalf("initial frame = %#v", result)
			}

			output.Reset()
			next := terminalSessionPlan(2, 28, test.nextHeight, test.nextOutput, LeaseState{})
			if result := session.Flush(next); result.Err != nil || !result.FullRepaint {
				t.Fatalf("height resize frame = %#v", result)
			}
			raw := output.String()
			if strings.Contains(raw, "\x1b[s") || strings.Contains(raw, fmt.Sprintf("\x1b[1;%dr", test.nextHeight)) {
				t.Fatalf("height resize replayed an old DECSTBM boundary: %q", raw)
			}
			if test.forbiddenCUP != "" && strings.Contains(raw, test.forbiddenCUP) {
				t.Fatalf("height resize addressed an old viewport row: %q", raw)
			}
			want := ViewportArea{Top: test.nextOutput + 1, Height: test.nextHeight - test.nextOutput, Width: 28}
			if state := session.ProjectionState(); state.Viewport != want || state.Validity != renderengine.ProjectionKnown {
				t.Fatalf("height resize projection = %#v, want viewport=%#v", state, want)
			}
		})
	}
}

func TestTerminalOffsetViewportANSIFencesRelativeCoordinates(t *testing.T) {
	area := ViewportArea{Top: 5, Height: 2, Width: 10}
	input := "\x1b[1;2Hfirst\x1b[31m\x1b[2;10fsecond\x1b[0m"
	got, err := terminalOffsetViewportANSI(input, area)
	if err != nil {
		t.Fatalf("offset valid viewport ANSI: %v", err)
	}
	want := "\x1b[5;2Hfirst\x1b[31m\x1b[6;10fsecond\x1b[0m"
	if got != want {
		t.Fatalf("offset viewport ANSI = %q, want %q", got, want)
	}

	invalid := []string{
		"\x1b[",
		"\x1b[3;1H",
		"\x1b[1;11H",
		"\x1b[1;0H",
		"\x1b[?;1H",
	}
	for _, value := range invalid {
		if _, err := terminalOffsetViewportANSI(value, area); !errors.Is(err, ErrInvalidTerminalFrame) {
			t.Fatalf("invalid viewport ANSI %q returned %v", value, err)
		}
	}

	// Top row one is still parsed instead of bypassing the coordinate fence.
	if _, err := terminalOffsetViewportANSI("\x1b[2;1H", ViewportArea{Top: 1, Height: 1, Width: 10}); !errors.Is(err, ErrInvalidTerminalFrame) {
		t.Fatalf("row-one viewport bypassed coordinate validation: %v", err)
	}
}

func TestComposeTerminalFramePlanCarriesStructuredFrameRows(t *testing.T) {
	state := composeFixtureState()
	plan := ComposeTerminalFramePlan(state)
	if !plan.Valid() {
		t.Fatalf("composed frame plan is invalid: %#v", plan)
	}
	if len(plan.RenderRows) != len(plan.Rows) {
		t.Fatalf("structured rows = %d, text rows = %d", len(plan.RenderRows), len(plan.Rows))
	}
	for index := range plan.Rows {
		plain := (render.PlainBackend{}).Render(render.LinesDoc(plan.RenderRows[index]))
		if plain != plan.Rows[index].Text {
			t.Fatalf("row %d structured text = %q, want %q", index+1, plain, plan.Rows[index].Text)
		}
	}
	for index, row := range plan.Rows {
		if row.CellID == 1 && len(plan.RenderRows[index].Spans) > 0 {
			if got, want := plan.RenderRows[index].Spans[0].Style.Role, string(style.RoleUser); got != want {
				t.Fatalf("user render row role = %q, want %q", got, want)
			}
			return
		}
	}
	t.Fatal("composed plan did not contain the user transcript row")
}

func TestComposeTerminalTransactionPlanRetainsCompleteFrameContract(t *testing.T) {
	plan := ComposeTerminalTransactionPlan(composeFixtureState(), nil)
	if !plan.Valid() {
		t.Fatalf("composed transaction plan is invalid: %#v", plan)
	}
	for _, row := range plan.Frame.Rows {
		if row.CellID == 1 {
			return
		}
	}
	t.Fatal("exported transaction composer omitted finalized transcript rows")
}

func TestTerminalSessionProjectionTracksCompletedScrollbackResets(t *testing.T) {
	var output bytes.Buffer
	session := NewTerminalSession(&output)
	initial := terminalSessionPlan(1, 24, 6, 4, LeaseState{})
	if result := session.Flush(initial); result.Err != nil {
		t.Fatalf("initial flush = %#v", result)
	}
	if state := session.ProjectionState(); state.ScrollbackResetCount != 0 || state.LastScrollbackResetReason != "" {
		t.Fatalf("initial projection reported a reset: %#v", state)
	}

	resized := terminalSessionPlan(2, 30, 8, 6, LeaseState{})
	if result := session.Flush(resized); result.Err != nil {
		t.Fatalf("resize flush = %#v", result)
	}
	state := session.ProjectionState()
	if state.ScrollbackResetCount != 1 || state.LastScrollbackResetReason != "resize" || state.TerminalEpoch != 1 {
		t.Fatalf("resize reset diagnostics = %#v", state)
	}

	result := session.FlushTransaction(TerminalTransactionPlan{
		Frame: resized, ResetScrollback: true, TerminalEpoch: state.TerminalEpoch,
	})
	if result.Frame.Err != nil || !result.ScrollbackReset {
		t.Fatalf("reconciliation reset = %#v", result)
	}
	state = session.ProjectionState()
	if state.ScrollbackResetCount != 2 || state.LastScrollbackResetReason != "reconciliation" || state.TerminalEpoch != 2 {
		t.Fatalf("reconciliation reset diagnostics = %#v", state)
	}
}

func TestTerminalSessionStructuredFrameRetainsRoleStyle(t *testing.T) {
	var output bytes.Buffer
	session := NewTerminalSession(&output)
	theme := style.BuildThemeContext(style.ThemeSelection{
		PaletteName: style.PaletteFocus,
		Mode:        style.ThemeModeDark,
	}, style.ColorProfile{ColorProfile: render.TrueColorProfile(), Background: style.BackgroundDark})
	session.SetThemeContext(theme)

	plan := terminalSessionPlan(1, 20, 6, 4, LeaseState{})
	plan.Rows[4].Text = "styled row"
	plan.RenderRows = make([]render.Line, len(plan.Rows))
	for index, row := range plan.Rows {
		plan.RenderRows[index] = render.Line{Spans: []render.Span{{Text: row.Text}}}
	}
	plan.RenderRows[4] = render.Line{Spans: []render.Span{{
		Text: "styled row", Style: render.Style{Role: string(style.RoleUser)},
	}}}

	first := session.Flush(plan)
	if first.Err != nil || !first.FullRepaint {
		t.Fatalf("structured first flush = %#v", first)
	}
	if expected := style.RenderDocument(render.LinesDoc(plan.RenderRows[4]), theme); !strings.Contains(output.String(), expected) {
		t.Fatalf("structured role encoding %q missing from terminal output %q", expected, output.String())
	}
	before := output.Len()
	second := session.Flush(plan)
	if second.Err != nil || second.FullRepaint || output.Len() != before {
		t.Fatalf("unchanged structured frame = %#v, bytes %d -> %d", second, before, output.Len())
	}

	noColor := style.BuildThemeContext(style.ThemeSelection{
		PaletteName: style.PaletteFocus,
		Mode:        style.ThemeModeDark,
	}, style.ColorProfile{ColorProfile: render.NoColorProfile(), Background: style.BackgroundDark})
	session.SetThemeContext(noColor)
	repaint := session.Flush(plan)
	if repaint.Err != nil || !repaint.FullRepaint {
		t.Fatalf("theme transition must force a source-backed repaint: %#v", repaint)
	}
}

func TestTerminalSessionRejectsMismatchedStructuredFrameText(t *testing.T) {
	var output bytes.Buffer
	session := NewTerminalSession(&output)
	plan := terminalSessionPlan(1, 20, 6, 4, LeaseState{})
	plan.RenderRows = make([]render.Line, len(plan.Rows))
	for index, row := range plan.Rows {
		plan.RenderRows[index] = render.Line{Spans: []render.Span{{Text: row.Text}}}
	}
	plan.RenderRows[0] = render.Line{Spans: []render.Span{{Text: "different"}}}

	result := session.Flush(plan)
	if !errors.Is(result.Err, ErrInvalidTerminalFrame) {
		t.Fatalf("mismatched structured row result = %#v", result)
	}
	if output.Len() != 0 {
		t.Fatalf("invalid frame wrote terminal bytes: %q", output.String())
	}
	if state := session.ProjectionState(); state.Validity != renderengine.ProjectionUnknown {
		t.Fatalf("invalid frame left a known projection: %#v", state)
	}
}

func TestTerminalSessionLeaseReleaseForcesPrimaryRecovery(t *testing.T) {
	var output bytes.Buffer
	session := NewTerminalSession(&output)
	base := terminalSessionPlan(1, 20, 6, 4, LeaseState{})
	if result := session.Flush(base); result.Err != nil || !result.FullRepaint {
		t.Fatalf("base flush = %#v", result)
	}
	beforeLease := output.Len()
	leased := terminalSessionPlan(1, 20, 6, 2, LeaseState{ID: 42, Active: true})
	if result := session.Flush(leased); result.Err != nil || !result.Deferred {
		t.Fatalf("leased flush = %#v", result)
	}
	if output.Len() != beforeLease {
		t.Fatalf("leased primary flush wrote bytes: before=%d after=%d", beforeLease, output.Len())
	}

	releasePlan := terminalSessionPlan(1, 20, 6, 2, LeaseState{})
	releaseStart := output.Len()
	released := session.Flush(releasePlan)
	if released.Err != nil || released.Deferred || !released.FullRepaint {
		t.Fatalf("release recovery = %#v", released)
	}
	if session.ProjectionState().Validity != renderengine.ProjectionKnown {
		t.Fatalf("release recovery did not confirm projection: %#v", session.ProjectionState())
	}
	if raw := output.String()[releaseStart:]; strings.Contains(raw, "\x1b[s\x1b[1;4r\x1b[4;1H\r\n\r\n") {
		t.Fatalf("lease recovery committed blank rows while expanding the primary viewport: %q", raw)
	}
}

func TestTerminalSessionAlternateScreenOwnsTransportAndForcesRecovery(t *testing.T) {
	var output bytes.Buffer
	session := NewTerminalSession(&output)
	base := terminalSessionPlan(1, 20, 6, 4, LeaseState{})
	if result := session.Flush(base); result.Err != nil || !result.FullRepaint {
		t.Fatalf("base flush = %#v", result)
	}
	beforeLease := output.Len()

	if err := session.EnterAlternateScreen(77); err != nil {
		t.Fatalf("EnterAlternateScreen: %v", err)
	}
	if session.AlternateScreenLeaseID() != 77 {
		t.Fatalf("alternate lease id = %d, want 77", session.AlternateScreenLeaseID())
	}
	enterOutput := output.String()[beforeLease:]
	for _, want := range []string{"\x1b[?1049h", "\x1b[?25l", "\x1b[2J"} {
		if !strings.Contains(enterOutput, want) {
			t.Fatalf("enter transport missing %q in %q", want, enterOutput)
		}
	}

	beforeDeferred := output.Len()
	if result := session.Flush(base); result.Err != nil || !result.Deferred {
		t.Fatalf("primary frame during alternate lease = %#v", result)
	}
	if output.Len() != beforeDeferred {
		t.Fatalf("primary frame wrote while alternate lease active: %d -> %d", beforeDeferred, output.Len())
	}
	if err := session.WriteAlternateScreen(77, "pager-frame"); err != nil {
		t.Fatalf("WriteAlternateScreen: %v", err)
	}
	if !strings.Contains(output.String(), "pager-frame") {
		t.Fatalf("alternate frame did not use terminal session output: %q", output.String())
	}
	if err := session.ExitAlternateScreen(77); err != nil {
		t.Fatalf("ExitAlternateScreen: %v", err)
	}
	if session.AlternateScreenLeaseID() != 0 {
		t.Fatalf("alternate lease remained active: %d", session.AlternateScreenLeaseID())
	}
	if state := session.ProjectionState(); state.Validity != renderengine.ProjectionUnknown {
		t.Fatalf("exit must invalidate primary projection: %#v", state)
	}
	if !strings.Contains(output.String(), "\x1b[?1049l") {
		t.Fatalf("exit transport missing DEC 1049 exit: %q", output.String())
	}

	if result := session.Flush(base); result.Err != nil || result.Deferred || !result.FullRepaint {
		t.Fatalf("primary recovery after alternate lease = %#v", result)
	}
}

func TestTerminalSessionAlternateRoundTripPreservesTopAlignedHistory(t *testing.T) {
	const width, height = 28, 6
	var output bytes.Buffer
	session := NewTerminalSession(&output)
	base := terminalSessionPlan(1, width, height, 4, LeaseState{})
	if result := session.Flush(base); result.Err != nil {
		t.Fatalf("base frame = %#v", result)
	}
	first := terminalSessionCommit(1,
		render.Line{Spans: []render.Span{{Text: "ALT-TOP-1"}}},
		render.Line{Spans: []render.Span{{Text: "ALT-TOP-2"}}},
		render.Line{Spans: []render.Span{{Text: "ALT-TOP-3"}}},
		render.Line{Spans: []render.Span{{Text: "ALT-TOP-4"}}},
		render.Line{Spans: []render.Span{{Text: "ALT-TOP-5"}}},
	)
	if result := session.CommitHistory(first); result.Err != nil || result.Deferred {
		t.Fatalf("history overflow = %#v", result)
	}
	expanded := terminalSessionPlan(1, width, height, 5, LeaseState{})
	if result := session.Flush(expanded); result.Err != nil {
		t.Fatalf("history expansion = %#v", result)
	}
	if err := session.EnterAlternateScreen(81); err != nil {
		t.Fatalf("enter alternate: %v", err)
	}
	if err := session.ExitAlternateScreen(81); err != nil {
		t.Fatalf("exit alternate: %v", err)
	}
	if result := session.Flush(expanded); result.Err != nil || !result.FullRepaint {
		t.Fatalf("primary recovery = %#v", result)
	}

	output.Reset()
	next := terminalSessionCommit(1, render.Line{Spans: []render.Span{{Text: "ALT-TOP-6"}}})
	next.Token = 2
	if result := session.CommitHistory(next); result.Err != nil || result.Deferred {
		t.Fatalf("history after alternate round trip = %#v", result)
	}
	ansi := output.String()
	if !strings.Contains(ansi, "\x1b[5;1H") || strings.Contains(ansi, "\x1b[1;1H\x1b[1M") {
		t.Fatalf("alternate round trip lost top-aligned occupancy: %q", ansi)
	}
}

func TestTerminalSessionZeroByteAlternateEnterPreservesPrimaryHistory(t *testing.T) {
	errZero := errors.New("alternate enter unavailable")
	writer := &terminalSessionShortWriter{}
	session := NewTerminalSession(writer)
	base := terminalSessionPlan(1, 20, 6, 4, LeaseState{})
	if result := session.Flush(base); result.Err != nil {
		t.Fatalf("base frame = %#v", result)
	}
	if result := session.CommitHistory(terminalSessionCommit(1,
		render.Line{Spans: []render.Span{{Text: "primary history"}}},
	)); result.Err != nil || result.Deferred {
		t.Fatalf("primary history = %#v", result)
	}

	writer.zeroError = errZero
	writer.failZero = 1
	if err := session.EnterAlternateScreen(71); !errors.Is(err, errZero) {
		t.Fatalf("zero-byte alternate enter = %v", err)
	}
	state := session.ProjectionState()
	if session.AlternateScreenLeaseID() != 0 || state.Validity != renderengine.ProjectionKnown ||
		!state.HistoryKnown || state.HistoryRows != 1 {
		t.Fatalf("zero-byte enter discarded primary proof: %#v lease=%d", state, session.AlternateScreenLeaseID())
	}
}

func TestTerminalSessionZeroByteAlternateExitRetainsRetryableLease(t *testing.T) {
	errZero := errors.New("alternate exit unavailable")
	writer := &terminalSessionShortWriter{}
	session := NewTerminalSession(writer)
	base := terminalSessionPlan(1, 20, 6, 4, LeaseState{})
	if result := session.Flush(base); result.Err != nil {
		t.Fatalf("base frame = %#v", result)
	}
	if result := session.CommitHistory(terminalSessionCommit(1,
		render.Line{Spans: []render.Span{{Text: "primary history"}}},
	)); result.Err != nil || result.Deferred {
		t.Fatalf("primary history = %#v", result)
	}
	if err := session.EnterAlternateScreen(72); err != nil {
		t.Fatalf("enter alternate: %v", err)
	}

	writer.zeroError = errZero
	writer.failZero = 1
	if err := session.ExitAlternateScreen(72); !errors.Is(err, errZero) {
		t.Fatalf("zero-byte alternate exit = %v", err)
	}
	if session.AlternateScreenLeaseID() != 72 || !session.ProjectionState().HistoryKnown {
		t.Fatalf("zero-byte exit lost retryable lease/history: lease=%d state=%#v",
			session.AlternateScreenLeaseID(), session.ProjectionState())
	}
	if err := session.ExitAlternateScreen(72); err != nil || session.AlternateScreenLeaseID() != 0 {
		t.Fatalf("alternate exit retry failed: err=%v lease=%d", err, session.AlternateScreenLeaseID())
	}
}

func TestTerminalSessionPartialAlternateExitInvalidatesPrimaryHistory(t *testing.T) {
	writer := &terminalSessionShortWriter{}
	session := NewTerminalSession(writer)
	base := terminalSessionPlan(1, 20, 6, 4, LeaseState{})
	if result := session.Flush(base); result.Err != nil {
		t.Fatalf("base frame = %#v", result)
	}
	if result := session.CommitHistory(terminalSessionCommit(1,
		render.Line{Spans: []render.Span{{Text: "primary history"}}},
	)); result.Err != nil || result.Deferred {
		t.Fatalf("primary history = %#v", result)
	}
	if err := session.EnterAlternateScreen(73); err != nil {
		t.Fatalf("enter alternate: %v", err)
	}
	writer.short = true
	if err := session.ExitAlternateScreen(73); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("partial alternate exit = %v", err)
	}
	state := session.ProjectionState()
	if session.AlternateScreenLeaseID() != 0 || state.HistoryKnown || state.HistoryRows != 0 ||
		state.Validity != renderengine.ProjectionUnknown {
		t.Fatalf("partial exit retained primary proof: %#v lease=%d", state, session.AlternateScreenLeaseID())
	}
}

type terminalSessionShortWriter struct {
	short     bool
	panic     bool
	zeroError error
	failZero  int
	writes    int
	bytes     bytes.Buffer
}

func (w *terminalSessionShortWriter) Write(data []byte) (int, error) {
	w.writes++
	if w.panic {
		panic("writer fault")
	}
	if w.failZero > 0 {
		w.failZero--
		return 0, w.zeroError
	}
	if w.short && len(data) > 0 {
		n := len(data) / 2
		if n < 1 {
			n = 1
		}
		_, _ = w.bytes.Write(data[:n])
		return n, nil
	}
	return w.bytes.Write(data)
}

func TestTerminalSessionShortFrameWriteMarksProjectionUnknown(t *testing.T) {
	writer := &terminalSessionShortWriter{short: true}
	session := NewTerminalSession(writer)
	plan := terminalSessionPlan(1, 20, 6, 4, LeaseState{})
	result := session.Flush(plan)
	if !errors.Is(result.Err, io.ErrShortWrite) || result.Frame != 0 {
		t.Fatalf("short flush = %#v", result)
	}
	if state := session.ProjectionState(); state.Validity != renderengine.ProjectionUnknown || state.Cursor != nil {
		t.Fatalf("failed flush left a usable projection: %#v", state)
	}

	writer.short = false
	recovery := session.Flush(plan)
	if recovery.Err != nil || !recovery.FullRepaint || recovery.Frame != 1 {
		t.Fatalf("recovery after short write = %#v", recovery)
	}
}

func TestTerminalSessionRecoveryDoesNotReuseBoundaryAfterPartialTransition(t *testing.T) {
	writer := &terminalSessionShortWriter{}
	session := NewTerminalSession(writer)
	initial := terminalSessionPlan(1, 20, 6, 4, LeaseState{})
	if result := session.Flush(initial); result.Err != nil {
		t.Fatalf("initial frame = %#v", result)
	}

	expanded := terminalSessionPlan(1, 20, 6, 2, LeaseState{})
	writer.short = true
	if result := session.Flush(expanded); !errors.Is(result.Err, io.ErrShortWrite) {
		t.Fatalf("partial boundary transition = %#v", result)
	}

	writer.short = false
	writer.bytes.Reset()
	writer.writes = 0
	recovery := session.Flush(expanded)
	if recovery.Err != nil || !recovery.FullRepaint {
		t.Fatalf("boundary recovery = %#v", recovery)
	}
	if raw := writer.bytes.String(); strings.Contains(raw, "\x1b[s") || strings.Contains(raw, "\x1b[1;4r") {
		t.Fatalf("recovery reused an unconfirmed viewport boundary: %q", raw)
	}
}

func TestTerminalSessionZeroWriteRetriesHistoryBoundaryTransition(t *testing.T) {
	errZero := errors.New("zero-byte writer failure")
	writer := &terminalSessionShortWriter{}
	session := NewTerminalSession(writer)
	initial := terminalSessionPlan(1, 22, 6, 4, LeaseState{})
	if result := session.Flush(initial); result.Err != nil {
		t.Fatalf("initial frame = %#v", result)
	}
	commit := terminalSessionCommit(1,
		render.Line{Spans: []render.Span{{Text: "ZERO-RETRY-1"}}},
		render.Line{Spans: []render.Span{{Text: "ZERO-RETRY-2"}}},
	)
	if result := session.CommitHistory(commit); result.Err != nil || result.Deferred {
		t.Fatalf("history insert = %#v", result)
	}

	expanded := terminalSessionPlan(1, 22, 6, 2, LeaseState{})
	writer.zeroError = errZero
	writer.failZero = 1
	beforeFailure := writer.bytes.Len()
	if result := session.Flush(expanded); !errors.Is(result.Err, errZero) {
		t.Fatalf("zero-write transition = %#v", result)
	}
	if writer.bytes.Len() != beforeFailure {
		t.Fatalf("zero-write failure emitted bytes: before=%d after=%d", beforeFailure, writer.bytes.Len())
	}
	if state := session.ProjectionState(); !state.HistoryKnown || state.HistoryRows != 2 || state.Viewport.Top != 5 {
		t.Fatalf("zero-write failure discarded confirmed history boundary: %#v", state)
	}

	retryStart := writer.bytes.Len()
	if result := session.Flush(expanded); result.Err != nil || !result.FullRepaint {
		t.Fatalf("transition retry = %#v", result)
	}
	retry := writer.bytes.String()[retryStart:]
	if !strings.Contains(retry, "\x1b[1;1H\x1b[2M") {
		t.Fatalf("retry omitted the uncommitted headroom transition: %q", retry)
	}
	screen := vt.NewScreen(22, 6)
	screen.Feed(writer.bytes.String())
	if got := screen.ScrollbackLines(); len(got) != 0 {
		t.Fatalf("zero-write retry polluted scrollback: %#v\n%s", got, screen.Dump())
	}
	if got := strings.Join(screen.Lines(1, 2), "\n"); got != "ZERO-RETRY-1\nZERO-RETRY-2" {
		t.Fatalf("zero-write retry visible tail = %q\n%s", got, screen.Dump())
	}
}

func TestTerminalSessionPartialHistoryTransitionFailsClosed(t *testing.T) {
	writer := &terminalSessionShortWriter{}
	session := NewTerminalSession(writer)
	initial := terminalSessionPlan(1, 22, 6, 4, LeaseState{})
	if result := session.Flush(initial); result.Err != nil {
		t.Fatalf("initial frame = %#v", result)
	}
	commit := terminalSessionCommit(1,
		render.Line{Spans: []render.Span{{Text: "PARTIAL-1"}}},
		render.Line{Spans: []render.Span{{Text: "PARTIAL-2"}}},
		render.Line{Spans: []render.Span{{Text: "PARTIAL-3"}}},
	)
	if result := session.CommitHistory(commit); result.Err != nil || result.Deferred {
		t.Fatalf("history insert = %#v", result)
	}

	writer.short = true
	expanded := terminalSessionPlan(1, 22, 6, 2, LeaseState{})
	if result := session.Flush(expanded); !errors.Is(result.Err, io.ErrShortWrite) {
		t.Fatalf("partial history transition = %#v", result)
	}
	if state := session.ProjectionState(); state.HistoryKnown || state.HistoryRows != 0 {
		t.Fatalf("partial transition retained a guessed history projection: %#v", state)
	}

	writer.short = false
	if result := session.Flush(expanded); result.Err != nil || !result.FullRepaint {
		t.Fatalf("bottom viewport recovery = %#v", result)
	}
	next := terminalSessionCommit(1, render.Line{Spans: []render.Span{{Text: "MUST-DEFER"}}})
	next.Token = 2
	before := writer.bytes.Len()
	if result := session.CommitHistory(next); !result.Deferred || result.Err != nil {
		t.Fatalf("history write after unknown transition = %#v", result)
	}
	if writer.bytes.Len() != before {
		t.Fatalf("unknown history projection accepted new bytes: before=%d after=%d", before, writer.bytes.Len())
	}
}

func TestTerminalSessionWriterPanicMarksProjectionUnknown(t *testing.T) {
	writer := &terminalSessionShortWriter{panic: true}
	session := NewTerminalSession(writer)
	plan := terminalSessionPlan(1, 20, 6, 4, LeaseState{})
	plan.Cursor = &AppCursor{Row: 5, Col: 3, Focus: BottomFocusPrompt}

	result := session.Flush(plan)
	if !errors.Is(result.Err, ErrTerminalWriterPanic) || result.Frame != 0 {
		t.Fatalf("panic flush = %#v", result)
	}
	if writer.writes != 1 {
		t.Fatalf("writer calls = %d, want one", writer.writes)
	}
	if state := session.ProjectionState(); state.Validity != renderengine.ProjectionUnknown || state.Cursor != nil || state.Frame != 0 {
		t.Fatalf("panic flush retained projection state: %#v", state)
	}
}

func TestTerminalSessionColorProfileInvalidatesViewportCache(t *testing.T) {
	var output bytes.Buffer
	session := NewTerminalSession(&output)
	plan := terminalSessionPlan(1, 20, 6, 4, LeaseState{})
	if result := session.Flush(plan); result.Err != nil {
		t.Fatalf("initial flush = %#v", result)
	}

	session.SetColorProfile(render.TrueColorProfile())
	if state := session.ProjectionState(); state.Validity != renderengine.ProjectionUnknown || state.Cursor != nil {
		t.Fatalf("profile update left a known cache: %#v", state)
	}
	if result := session.Flush(plan); result.Err != nil || !result.FullRepaint || result.Frame != 2 {
		t.Fatalf("profile recovery flush = %#v", result)
	}
}

func TestTerminalSessionTransactionCommitsHistoryWithRecoveryViewport(t *testing.T) {
	writer := &terminalSessionShortWriter{}
	session := NewTerminalSession(writer)
	plan := terminalSessionPlan(1, 20, 6, 4, LeaseState{})
	commit := terminalSessionCommit(1, render.Line{Spans: []render.Span{{Text: "handoff"}}})

	first := session.FlushTransaction(TerminalTransactionPlan{Frame: plan, History: &commit})
	if first.Frame.Err != nil || !first.Frame.FullRepaint || first.Frame.Frame != 1 || first.History == nil || first.History.Deferred || first.History.Err != nil || first.History.Frame != 1 {
		t.Fatalf("recovery transaction = %#v", first)
	}
	if writer.writes != 1 || !strings.Contains(writer.bytes.String(), "\x1b[1;4r") {
		t.Fatalf("recovery did not atomically insert history and viewport: writes=%d bytes=%q", writer.writes, writer.bytes.String())
	}
}

func TestTerminalSessionTransactionBatchesHistoryViewportAndCursor(t *testing.T) {
	writer := &terminalSessionShortWriter{}
	session := NewTerminalSession(writer)
	base := terminalSessionPlan(1, 20, 6, 4, LeaseState{})
	if result := session.Flush(base); result.Err != nil {
		t.Fatalf("initial flush = %#v", result)
	}
	writer.bytes.Reset()
	writer.writes = 0

	next := terminalSessionPlan(1, 20, 6, 4, LeaseState{})
	next.Rows[4].Text = "changed"
	next.Cursor = &AppCursor{Row: 5, Col: 3, Focus: BottomFocusPrompt}
	commit := terminalSessionCommit(1, render.Line{Spans: []render.Span{{Text: "older row"}}})
	result := session.FlushTransaction(TerminalTransactionPlan{Frame: next, History: &commit})
	if result.Frame.Err != nil || result.Frame.Frame != 2 || result.History == nil || result.History.Err != nil || result.History.Deferred || result.History.Frame != 2 {
		t.Fatalf("batched transaction = %#v", result)
	}
	if writer.writes != 1 {
		t.Fatalf("target writes = %d, want one", writer.writes)
	}
	ansi := writer.bytes.String()
	handoff := strings.Index(ansi, "\x1b[s")
	viewport := strings.Index(ansi, "\x1b[5;1H")
	cursor := strings.LastIndex(ansi, "\x1b[5;3H")
	if handoff < 0 || viewport < handoff || cursor < viewport {
		t.Fatalf("transaction order was not handoff -> viewport -> cursor: %q", ansi)
	}
}

func TestTerminalSessionTransactionDefersPreparedHistoryWhenFramePreflightFails(t *testing.T) {
	var output bytes.Buffer
	session := NewTerminalSession(&output)
	base := terminalSessionPlan(1, 20, 6, 4, LeaseState{})
	if result := session.Flush(base); result.Err != nil {
		t.Fatalf("initial frame = %#v", result)
	}

	invalid := terminalSessionPlan(1, 20, 6, 4, LeaseState{})
	invalid.RenderRows = make([]render.Line, len(invalid.Rows))
	for index, row := range invalid.Rows {
		invalid.RenderRows[index] = render.Line{Spans: []render.Span{{Text: row.Text}}}
	}
	invalid.RenderRows[0] = render.Line{Spans: []render.Span{{Text: "different"}}}
	commit := terminalSessionCommit(1, render.Line{Spans: []render.Span{{Text: "older row"}}})
	output.Reset()

	result := session.FlushTransaction(TerminalTransactionPlan{Frame: invalid, History: &commit})
	if !errors.Is(result.Frame.Err, ErrInvalidTerminalFrame) || result.Frame.Frame != 1 {
		t.Fatalf("invalid transaction frame = %#v", result.Frame)
	}
	if result.History == nil || !result.History.Deferred || result.History.Err != nil || result.History.Frame != 0 {
		t.Fatalf("prepared history was acknowledged before frame write: %#v", result.History)
	}
	if output.Len() != 0 {
		t.Fatalf("frame preflight failure wrote terminal bytes: %q", output.String())
	}
	if state := session.ProjectionState(); state.Validity != renderengine.ProjectionKnown || state.Frame != 1 {
		t.Fatalf("preflight failure changed confirmed projection: %#v", state)
	}
}

func TestTerminalSessionHigherGenerationResizePreflightFailurePreservesConfirmedState(t *testing.T) {
	var output bytes.Buffer
	session := NewTerminalSession(&output)
	base := terminalSessionPlan(1, 20, 6, 4, LeaseState{})
	base.Cursor = &AppCursor{Row: 5, Col: 3, Focus: BottomFocusPrompt}
	if result := session.Flush(base); result.Err != nil {
		t.Fatalf("initial frame = %#v", result)
	}
	before := session.ProjectionState()

	invalid := terminalSessionPlan(2, 28, 8, 5, LeaseState{})
	invalid.RenderRows = make([]render.Line, len(invalid.Rows))
	for index, row := range invalid.Rows {
		invalid.RenderRows[index] = render.Line{Spans: []render.Span{{Text: row.Text}}}
	}
	invalid.RenderRows[0] = render.Line{Spans: []render.Span{{Text: "mismatched"}}}
	output.Reset()
	result := session.Flush(invalid)
	if !errors.Is(result.Err, ErrInvalidTerminalFrame) || output.Len() != 0 {
		t.Fatalf("invalid resize preflight = %#v bytes=%q", result, output.String())
	}
	after := session.ProjectionState()
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("preflight failure changed confirmed state:\nbefore=%#v\nafter=%#v", before, after)
	}
}

func TestTerminalSessionZeroByteResizeFailurePreservesConfirmedState(t *testing.T) {
	errZero := errors.New("zero-byte resize failure")
	writer := &terminalSessionShortWriter{}
	session := NewTerminalSession(writer)
	base := terminalSessionPlan(1, 20, 6, 4, LeaseState{})
	if result := session.Flush(base); result.Err != nil {
		t.Fatalf("initial frame = %#v", result)
	}
	if result := session.CommitHistory(terminalSessionCommit(1,
		render.Line{Spans: []render.Span{{Text: "resident"}}},
	)); result.Err != nil || result.Deferred {
		t.Fatalf("resident history = %#v", result)
	}
	before := session.ProjectionState()

	writer.zeroError = errZero
	writer.failZero = 1
	result := session.Flush(terminalSessionPlan(2, 28, 8, 5, LeaseState{}))
	if !errors.Is(result.Err, errZero) {
		t.Fatalf("zero-byte resize = %#v", result)
	}
	after := session.ProjectionState()
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("zero-byte resize changed confirmed state:\nbefore=%#v\nafter=%#v", before, after)
	}
}

func TestTerminalSessionPartialResizeInvalidatesViewportAndHistoryProofs(t *testing.T) {
	writer := &terminalSessionShortWriter{}
	session := NewTerminalSession(writer)
	base := terminalSessionPlan(1, 20, 6, 4, LeaseState{})
	if result := session.Flush(base); result.Err != nil {
		t.Fatalf("initial frame = %#v", result)
	}
	if result := session.CommitHistory(terminalSessionCommit(1,
		render.Line{Spans: []render.Span{{Text: "resident"}}},
	)); result.Err != nil || result.Deferred {
		t.Fatalf("resident history = %#v", result)
	}

	writer.short = true
	result := session.Flush(terminalSessionPlan(2, 28, 8, 5, LeaseState{}))
	if !errors.Is(result.Err, io.ErrShortWrite) {
		t.Fatalf("partial resize = %#v", result)
	}
	state := session.ProjectionState()
	if state.Validity != renderengine.ProjectionUnknown || state.HistoryKnown ||
		state.HistoryRows != 0 || state.Cursor != nil {
		t.Fatalf("partial resize retained physical proof: %#v", state)
	}
}

func TestTerminalSessionTransactionShortWriteFailsFrameAndHistoryTogether(t *testing.T) {
	writer := &terminalSessionShortWriter{}
	session := NewTerminalSession(writer)
	plan := terminalSessionPlan(1, 20, 6, 4, LeaseState{})
	if result := session.Flush(plan); result.Err != nil {
		t.Fatalf("initial flush = %#v", result)
	}
	writer.short = true
	writer.bytes.Reset()
	writer.writes = 0
	commit := terminalSessionCommit(1, render.Line{Spans: []render.Span{{Text: "older row"}}})

	result := session.FlushTransaction(TerminalTransactionPlan{Frame: plan, History: &commit})
	if !errors.Is(result.Frame.Err, io.ErrShortWrite) || result.Frame.Frame != 1 || result.History == nil || !errors.Is(result.History.Err, io.ErrShortWrite) || !result.History.MayHavePartiallyWritten {
		t.Fatalf("short transaction = %#v", result)
	}
	if writer.writes != 1 {
		t.Fatalf("short transaction target writes = %d, want one", writer.writes)
	}
	if state := session.ProjectionState(); state.Validity != renderengine.ProjectionUnknown || state.Cursor != nil || state.Frame != 1 {
		t.Fatalf("short transaction retained projection: %#v", state)
	}
}

func TestTerminalSessionCommitHistoryUsesRichHandoffAndUpdatesFrame(t *testing.T) {
	var output bytes.Buffer
	session := NewTerminalSession(&output)
	session.SetColorProfile(render.TrueColorProfile())
	plan := terminalSessionPlan(1, 30, 8, 6, LeaseState{})
	if result := session.Flush(plan); result.Err != nil || !result.FullRepaint {
		t.Fatalf("initial primary frame = %#v", result)
	}
	output.Reset()
	commit := terminalSessionCommit(1, render.Line{Spans: []render.Span{{
		Text:  "rich handoff",
		Style: render.Style{Bold: true, Foreground: render.RGB(10, 20, 30)},
	}}})
	result := session.CommitHistory(commit)
	if result.Err != nil || result.Deferred || result.MayHavePartiallyWritten || result.Frame != 2 {
		t.Fatalf("history handoff = %#v", result)
	}
	ansi := output.String()
	for _, want := range []string{"\x1b[s", "\x1b[1;6r", "\x1b[6;1H", "rich handoff", "\x1b[1;38;2;10;20;30m", "\x1b[r", "\x1b[u"} {
		if !strings.Contains(ansi, want) {
			t.Fatalf("handoff bytes missing %q: %q", want, ansi)
		}
	}
	if strings.Contains(ansi, "\r\n") {
		t.Fatalf("underfilled history insertion scrolled instead of painting free capacity: %q", ansi)
	}
	if state := session.ProjectionState(); state.Validity != renderengine.ProjectionKnown || state.Frame != 2 {
		t.Fatalf("successful handoff did not retain known projection: %#v", state)
	}
}

func TestTerminalSessionCommitHistoryResolvesSemanticRoleWithFrameTheme(t *testing.T) {
	var output bytes.Buffer
	session := NewTerminalSession(&output)
	theme := style.BuildThemeContext(style.ThemeSelection{
		PaletteName: style.PaletteFocus,
		Mode:        style.ThemeModeDark,
	}, style.ColorProfile{ColorProfile: render.TrueColorProfile(), Background: style.BackgroundDark})
	session.SetThemeContext(theme)
	plan := terminalSessionPlan(1, 30, 8, 6, LeaseState{})
	if result := session.Flush(plan); result.Err != nil {
		t.Fatalf("initial primary frame = %#v", result)
	}
	output.Reset()
	line := render.Line{Spans: []render.Span{{
		Text: "semantic history", Style: render.Style{Role: string(style.RoleUser)},
	}}}
	commit := terminalSessionCommit(1, line)
	result := session.CommitHistory(commit)
	if result.Err != nil || result.Deferred {
		t.Fatalf("semantic role history handoff = %#v", result)
	}
	expected := style.RenderDocument(render.LinesDoc(line), theme)
	if !strings.Contains(output.String(), expected) {
		t.Fatalf("history handoff omitted resolved role %q: %q", expected, output.String())
	}
}

func TestTerminalSessionCommitHistoryDefersWithoutKnownCurrentPrimary(t *testing.T) {
	var output bytes.Buffer
	session := NewTerminalSession(&output)
	commit := terminalSessionCommit(1, render.Line{Spans: []render.Span{{Text: "pending"}}})
	if result := session.CommitHistory(commit); !result.Deferred || result.Err != nil {
		t.Fatalf("unframed history handoff = %#v", result)
	}
	if output.Len() != 0 {
		t.Fatalf("unframed history handoff wrote %q", output.String())
	}

	plan := terminalSessionPlan(1, 20, 6, 4, LeaseState{})
	if result := session.Flush(plan); result.Err != nil {
		t.Fatalf("frame flush = %#v", result)
	}
	output.Reset()
	stale := terminalSessionCommit(2, render.Line{Spans: []render.Span{{Text: "stale"}}})
	if result := session.CommitHistory(stale); !result.Deferred || result.Err != nil {
		t.Fatalf("stale generation handoff = %#v", result)
	}
	if output.Len() != 0 {
		t.Fatalf("stale generation handoff wrote %q", output.String())
	}
}

func TestTerminalSessionHistoryShortWriteInvalidatesProjection(t *testing.T) {
	writer := &terminalSessionShortWriter{}
	session := NewTerminalSession(writer)
	plan := terminalSessionPlan(1, 20, 6, 4, LeaseState{})
	if result := session.Flush(plan); result.Err != nil {
		t.Fatalf("initial primary frame = %#v", result)
	}
	writer.bytes.Reset()
	writer.short = true
	commit := terminalSessionCommit(1, render.Line{Spans: []render.Span{{Text: "one row"}}})
	result := session.CommitHistory(commit)
	if !errors.Is(result.Err, io.ErrShortWrite) || !result.MayHavePartiallyWritten || result.Frame != 0 {
		t.Fatalf("short history handoff = %#v", result)
	}
	if state := session.ProjectionState(); state.Validity != renderengine.ProjectionUnknown || state.Cursor != nil {
		t.Fatalf("short history handoff retained projection: %#v", state)
	}
}

func TestTerminalSessionHistoryRejectsWrappedDisplayRowsBeforeWriting(t *testing.T) {
	var output bytes.Buffer
	session := NewTerminalSession(&output)
	plan := terminalSessionPlan(1, 4, 6, 4, LeaseState{})
	if result := session.Flush(plan); result.Err != nil {
		t.Fatalf("initial primary frame = %#v", result)
	}
	output.Reset()
	commit := terminalSessionCommit(1, render.Line{Spans: []render.Span{{Text: "too-wide"}}})
	if result := session.CommitHistory(commit); !errors.Is(result.Err, ErrInvalidHistoryHandoff) || result.MayHavePartiallyWritten {
		t.Fatalf("wrapped handoff = %#v", result)
	}
	if output.Len() != 0 {
		t.Fatalf("invalid handoff emitted bytes: %q", output.String())
	}
}
