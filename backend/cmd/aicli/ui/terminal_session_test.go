package ui

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/renderengine"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
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

func TestTerminalSessionStructuredFrameRetainsRoleStyle(t *testing.T) {
	var output bytes.Buffer
	session := NewTerminalSession(&output)
	theme := style.BuildThemeContext(style.ThemeSelection{
		PaletteName: style.PaletteFocus,
		Mode:        style.ThemeModeDark,
	}, style.ColorProfile{ColorProfile: render.TrueColorProfile(), Background: style.BackgroundDark})
	session.SetThemeContext(theme)

	plan := terminalSessionPlan(1, 20, 6, 4, LeaseState{})
	plan.Rows[0].Text = "styled row"
	plan.RenderRows = make([]render.Line, len(plan.Rows))
	for index, row := range plan.Rows {
		plan.RenderRows[index] = render.Line{Spans: []render.Span{{Text: row.Text}}}
	}
	plan.RenderRows[0] = render.Line{Spans: []render.Span{{
		Text: "styled row", Style: render.Style{Role: string(style.RoleUser)},
	}}}

	first := session.Flush(plan)
	if first.Err != nil || !first.FullRepaint {
		t.Fatalf("structured first flush = %#v", first)
	}
	if expected := style.RenderDocument(render.LinesDoc(plan.RenderRows[0]), theme); !strings.Contains(output.String(), expected) {
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
	leased := terminalSessionPlan(1, 20, 6, 4, LeaseState{ID: 42, Active: true})
	if result := session.Flush(leased); result.Err != nil || !result.Deferred {
		t.Fatalf("leased flush = %#v", result)
	}
	if output.Len() != beforeLease {
		t.Fatalf("leased primary flush wrote bytes: before=%d after=%d", beforeLease, output.Len())
	}

	released := session.Flush(base)
	if released.Err != nil || released.Deferred || !released.FullRepaint {
		t.Fatalf("release recovery = %#v", released)
	}
	if session.ProjectionState().Validity != renderengine.ProjectionKnown {
		t.Fatalf("release recovery did not confirm projection: %#v", session.ProjectionState())
	}
}

type terminalSessionShortWriter struct {
	short  bool
	panic  bool
	writes int
	bytes  bytes.Buffer
}

func (w *terminalSessionShortWriter) Write(data []byte) (int, error) {
	w.writes++
	if w.panic {
		panic("writer fault")
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

func TestTerminalSessionTransactionDefersHistoryUntilRecoveryFrameIsKnown(t *testing.T) {
	writer := &terminalSessionShortWriter{}
	session := NewTerminalSession(writer)
	plan := terminalSessionPlan(1, 20, 6, 4, LeaseState{})
	commit := terminalSessionCommit(1, render.Line{Spans: []render.Span{{Text: "handoff"}}})

	first := session.FlushTransaction(TerminalTransactionPlan{Frame: plan, History: &commit})
	if first.Frame.Err != nil || !first.Frame.FullRepaint || first.Frame.Frame != 1 || first.History == nil || !first.History.Deferred || first.History.Err != nil {
		t.Fatalf("recovery transaction = %#v", first)
	}
	if strings.Contains(writer.bytes.String(), "\x1b[s") {
		t.Fatalf("recovery transaction emitted premature handoff: %q", writer.bytes.String())
	}

	writer.bytes.Reset()
	writer.writes = 0
	second := session.FlushTransaction(TerminalTransactionPlan{Frame: plan, History: &commit})
	if second.Frame.Err != nil || second.Frame.FullRepaint || second.Frame.Frame != 2 || second.History == nil || second.History.Err != nil || second.History.Deferred || second.History.Frame != 2 {
		t.Fatalf("known transaction = %#v", second)
	}
	if writer.writes != 1 {
		t.Fatalf("known transaction target writes = %d, want one", writer.writes)
	}
	if !strings.Contains(writer.bytes.String(), "\x1b[s") {
		t.Fatalf("known transaction omitted handoff: %q", writer.bytes.String())
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
	next.Rows[0].Text = "changed"
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
	viewport := strings.Index(ansi, "\x1b[1;1H")
	cursor := strings.LastIndex(ansi, "\x1b[5;3H")
	if handoff < 0 || viewport < handoff || cursor < viewport {
		t.Fatalf("transaction order was not handoff -> viewport -> cursor: %q", ansi)
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
	for _, want := range []string{"\x1b[s", "\x1b[1;6r", "\x1b[6;1H", "\r\n", "rich handoff", "\x1b[1;38;2;10;20;30m", "\x1b[r", "\x1b[u"} {
		if !strings.Contains(ansi, want) {
			t.Fatalf("handoff bytes missing %q: %q", want, ansi)
		}
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
