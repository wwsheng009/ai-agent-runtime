package ui

import (
	"bytes"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/vt"
	"golang.org/x/term"
)

type nativeScrollbackRegressionHarness struct {
	controller *UIController
	executor   *TerminalSessionExecutor
	physical   *bytes.Buffer
}

func newNativeScrollbackRegressionHarness(t *testing.T) *nativeScrollbackRegressionHarness {
	t.Helper()
	controller := NewUIController(UIControllerConfig{}, nil, nil)
	go controller.Run()
	physical := &bytes.Buffer{}
	executor := NewTerminalSessionExecutor(controller, NewTerminalSession(physical))
	t.Cleanup(func() {
		executor.Close()
		controller.Close()
		controller.WaitIdle()
	})
	return &nativeScrollbackRegressionHarness{controller: controller, executor: executor, physical: physical}
}

func (h *nativeScrollbackRegressionHarness) post(t *testing.T, actions ...UIAction) {
	t.Helper()
	for _, action := range actions {
		if !h.controller.Post(action) {
			t.Fatalf("post %T", action)
		}
	}
	h.controller.WaitIdle()
}

func (h *nativeScrollbackRegressionHarness) flush() {
	h.executor.Request()
	h.executor.WaitIdle()
	h.controller.WaitIdle()
}

func regressionCommittedSnapshot(revision uint64, cells ...*scene.TranscriptCell) *scene.Snapshot {
	return &scene.Snapshot{Revision: revision, Cells: cells}
}

func assertPhysicalMarkersExactlyOnce(t *testing.T, raw string, width, height int, markers []string) {
	t.Helper()
	counts := semanticLineCounts(t, width, height, raw)
	for _, marker := range markers {
		if counts[marker] != 1 {
			t.Fatalf("physical marker %q count=%d, want exactly one across native scrollback + visible screen", marker, counts[marker])
		}
	}
}

func TestTerminalResizeRebuildsCanonicalHistoryIntoFreshScrollbackEpoch(t *testing.T) {
	const initialWidth, initialHeight = 52, 10
	const resizedWidth, resizedHeight = 38, 12
	markers := make([]string, 36)
	for index := range markers {
		markers[index] = fmt.Sprintf("RESIZE-REFLOW-%03d", index+1)
	}
	source := strings.Join(markers, "\n")
	cell := &scene.TranscriptCell{
		ID: 801, Revision: 1, Kind: scene.KindAssistant,
		Source: source, Phase: scene.CellCommitted,
	}

	h := newNativeScrollbackRegressionHarness(t)
	h.post(t,
		Resize{Width: initialWidth, Height: initialHeight, Generation: 1},
		ShowPromptAction{Line: "> "},
		ReplaceTranscriptAction{Snapshot: regressionCommittedSnapshot(1, cell)},
	)
	h.flush()
	for _, entry := range h.controller.State().HistoryEffects.Entries() {
		if entry.State != HistoryCommitAcked {
			t.Fatalf("initial history entry not acked: %#v", entry)
		}
	}

	h.post(t, Resize{Width: resizedWidth, Height: resizedHeight, Generation: 2})
	h.flush()
	state := h.controller.State()
	if state.HistoryEffects.TerminalEpoch != 1 || state.HistoryEffects.ProjectionUnknown {
		t.Fatalf("resize did not establish a fresh history epoch: %+v", state.HistoryEffects)
	}
	entries := state.HistoryEffects.Entries()
	if len(entries) == 0 {
		t.Fatal("resize reconciliation did not replan canonical transcript")
	}
	for _, entry := range entries {
		if entry.State != HistoryCommitAcked {
			t.Fatalf("resize replay left history unresolved: %#v", entry)
		}
	}
	if !strings.Contains(h.physical.String(), "\x1b[3J") {
		t.Fatalf("resize transaction did not purge the stale scrollback epoch: %q", h.physical.String())
	}
	assertPhysicalMarkersExactlyOnce(t, h.physical.String(), resizedWidth, resizedHeight, markers)
}

// A stable mutable prefix and the finalized transcript describe the same
// semantic source. Finalization must transfer ownership, not render the prefix
// through a second HistoryCommit identity or drop the live tail in between.
func TestMutableActiveFinalizePreservesExactlyOncePhysicalFlow(t *testing.T) {
	const width, height = 72, 12
	markers := make([]string, 30)
	for index := range markers {
		markers[index] = fmt.Sprintf("FINALIZE-FLOW-%03d", index)
	}
	source := strings.Join(markers, "\n")

	h := newNativeScrollbackRegressionHarness(t)
	h.post(t,
		Resize{Width: width, Height: height, Generation: 1},
		SetSemanticActiveCellProjectionAction{Enabled: true},
		SetActiveCellAction{Active: ActiveCellState{
			CellID: 71, Revision: 4, Kind: scene.KindAssistant,
			Phase: ActiveCellMutable, Source: source,
			Stable: SourceRange{Start: 0, End: len(source)},
		}},
	)
	h.flush()

	streaming := h.controller.State()
	if streaming.Active.Acked.End == 0 {
		t.Fatalf("fixture never acknowledged a mutable overflow prefix: active=%+v effects=%+v", streaming.Active, streaming.HistoryEffects.Entries())
	}
	if !strings.Contains(h.physical.String(), markers[0]) || !strings.Contains(h.physical.String(), markers[len(markers)-1]) {
		t.Fatalf("streaming transaction did not contain both history prefix and live tail")
	}

	finalCell := &scene.TranscriptCell{
		ID: 71, Revision: 4, Kind: scene.KindAssistant,
		Source: source, Phase: scene.CellCommitted,
	}
	h.post(t, FinalizeActiveCellAction{
		Snapshot:             regressionCommittedSnapshot(5, finalCell),
		ExpectedActiveCellID: 71, ExpectedActiveRevision: 4,
		ExpectedActiveKind: scene.KindAssistant, ExpectedActiveKindKnown: true,
	})
	h.flush()

	finalState := h.controller.State()
	if finalState.Active != (ActiveCellState{}) {
		t.Fatalf("finalization left mutable ownership mounted: %+v", finalState.Active)
	}
	assertPhysicalMarkersExactlyOnce(t, h.physical.String(), width, height, markers)
}

func TestAuthoritativeFinalCorrectionReplacesAckedNativePrefixExactlyOnce(t *testing.T) {
	const width, height = 72, 12
	oldMarkers := make([]string, 30)
	finalMarkers := make([]string, 30)
	for index := range finalMarkers {
		oldMarkers[index] = fmt.Sprintf("STALE-ACTIVE-%03d", index)
		finalMarkers[index] = fmt.Sprintf("FINAL-CORRECT-%03d", index)
	}
	oldSource := strings.Join(oldMarkers, "\n")
	finalSource := strings.Join(finalMarkers, "\n")

	h := newNativeScrollbackRegressionHarness(t)
	h.post(t,
		Resize{Width: width, Height: height, Generation: 1},
		SetSemanticActiveCellProjectionAction{Enabled: true},
		SetActiveCellAction{Active: ActiveCellState{
			CellID: 72, Revision: 4, Kind: scene.KindAssistant,
			Phase: ActiveCellMutable, Source: oldSource,
			Stable: SourceRange{Start: 0, End: len(oldSource)},
		}},
	)
	h.flush()
	if h.controller.State().Active.Acked.End == 0 {
		t.Fatal("fixture did not hand off the stale mutable prefix")
	}

	finalCell := &scene.TranscriptCell{
		ID: 72, Revision: 5, Kind: scene.KindAssistant,
		Source: finalSource, Phase: scene.CellCommitted,
	}
	h.post(t, FinalizeActiveCellAction{
		Snapshot:             regressionCommittedSnapshot(5, finalCell),
		ExpectedActiveCellID: 72, ExpectedActiveRevision: 4,
		ExpectedSceneRevision: 5,
		ExpectedActiveKind:    scene.KindAssistant, ExpectedActiveKindKnown: true,
	})
	h.flush()

	raw := h.physical.String()
	if !strings.Contains(raw, "\x1b[3J") {
		t.Fatalf("authoritative correction did not reset stale scrollback: %q", raw)
	}
	assertPhysicalMarkersExactlyOnce(t, raw, width, height, finalMarkers)
	screen := vt.NewScreen(width, height)
	screen.Feed(raw)
	physical := strings.Join(append(screen.ScrollbackLines(), screen.Lines(0, height)...), "\n")
	for _, marker := range oldMarkers {
		if strings.Contains(physical, marker) {
			t.Fatalf("stale mutable marker survived authoritative reset: %q", marker)
		}
	}
}

// Mutable Markdown cannot fall back to raw source rows when its stable prefix
// leaves the inline viewport. The handoff payload must remain renderer-owned
// rich IR while the newest rendered rows stay live at the bottom.
func TestMutableMarkdownOverflowHandsOffRichPrefixBeforeFinalize(t *testing.T) {
	const width, height = 56, 12
	items := make([]string, 20)
	for index := range items {
		items[index] = fmt.Sprintf("- **MD-OVERFLOW-%03d** with `code-%03d`", index, index)
	}
	source := "# Mutable Markdown\n\n" + strings.Join(items, "\n")

	h := newNativeScrollbackRegressionHarness(t)
	h.post(t,
		Resize{Width: width, Height: height, Generation: 1},
		SetSemanticActiveCellProjectionAction{Enabled: true},
		SetActiveCellAction{Active: ActiveCellState{
			CellID: 81, Revision: 2, Kind: scene.KindAssistant,
			Phase: ActiveCellMutable, Source: source,
			Stable: SourceRange{Start: 0, End: len(source)},
		}},
	)

	entries := h.controller.State().HistoryEffects.Entries()
	if len(entries) == 0 {
		t.Fatal("mutable Markdown overflow created no pre-finalize history effects")
	}
	var payload strings.Builder
	rich := false
	fragments := map[uint64]struct{}{}
	for _, entry := range entries {
		commit := entry.Commit
		if commit.Origin != HistoryCommitActive {
			continue
		}
		if commit.FragmentID == 0 {
			t.Fatalf("mutable Markdown handoff has no stable render fragment identity: %#v", commit)
		}
		if _, duplicate := fragments[commit.FragmentID]; duplicate {
			t.Fatalf("duplicate mutable Markdown fragment id %d", commit.FragmentID)
		}
		fragments[commit.FragmentID] = struct{}{}
		payload.WriteString(render.PlainBackend{}.Render(render.LinesDoc(commit.Lines...)))
		for _, line := range commit.Lines {
			for _, span := range line.Spans {
				if span.Style.Bold || span.Style.Italic || span.Style.Underline || span.Style.Role == string(style.RoleCodeInline) {
					rich = true
				}
			}
		}
	}
	if len(fragments) == 0 || !strings.Contains(payload.String(), "MD-OVERFLOW-000") {
		t.Fatalf("mutable Markdown early rendered prefix was not planned: fragments=%d payload=%q", len(fragments), payload.String())
	}
	if strings.Contains(payload.String(), "**") || strings.Contains(payload.String(), "# Mutable Markdown") {
		t.Fatalf("mutable Markdown history leaked raw source syntax: %q", payload.String())
	}
	if !rich {
		t.Fatalf("mutable Markdown history lost structured emphasis/code styling: %#v", entries)
	}

	h.flush()
	screen := vt.NewScreen(width, height)
	screen.Feed(h.physical.String())
	physicalText := strings.Join(append(screen.ScrollbackLines(), screen.Lines(0, height)...), "\n")
	for _, marker := range []string{"MD-OVERFLOW-000", "MD-OVERFLOW-019"} {
		if strings.Count(physicalText, marker) != 1 {
			t.Fatalf("mutable Markdown marker %q count=%d across scrollback + screen\n%s", marker, strings.Count(physicalText, marker), screen.Dump())
		}
	}
}

// Startup replay is the hardest ownership boundary: no prior front buffer can
// be trusted, yet all semantic history must be inserted above the inline
// prompt/status viewport in one continuous flow.
func TestStartupMultiScreenHistoryKeepsPromptAndStatusOutOfScrollback(t *testing.T) {
	const (
		width        = 64
		height       = 12
		promptMarker = "PROMPT-VIEWPORT-ONLY> "
		statusMarker = "STATUS-VIEWPORT-ONLY"
	)
	markers := make([]string, 36)
	cells := make([]*scene.TranscriptCell, len(markers))
	for index := range markers {
		markers[index] = fmt.Sprintf("STARTUP-HISTORY-%03d", index)
		cells[index] = &scene.TranscriptCell{
			ID: scene.CellID(index + 1), Sequence: uint64(index + 1),
			Revision: 1, Kind: scene.KindAssistant,
			Source: markers[index], Phase: scene.CellCommitted,
		}
	}

	h := newNativeScrollbackRegressionHarness(t)
	h.post(t,
		Resize{Width: width, Height: height, Generation: 1},
		SetStatusModelAction{Status: style.StatusLineModel{State: style.RunReady, StateText: statusMarker}},
		ShowPromptAction{Line: promptMarker},
		ReplaceTranscriptAction{Snapshot: regressionCommittedSnapshot(1, cells...)},
	)
	if effects := h.controller.State().HistoryEffects.Entries(); len(effects) < height {
		t.Fatalf("startup fixture did not plan multi-screen history: effects=%d", len(effects))
	}
	h.flush()

	screen := vt.NewScreen(width, height)
	screen.Feed(h.physical.String())
	scrollback := strings.Join(screen.ScrollbackLines(), "\n")
	visible := strings.Join(screen.Lines(0, height), "\n")
	if !strings.Contains(scrollback, markers[0]) {
		t.Fatalf("oldest startup row never entered native scrollback: scrollback=%q\n%s", screen.ScrollbackLines(), screen.Dump())
	}
	promptNeedle := strings.TrimRight(promptMarker, " ")
	if !strings.Contains(visible, promptNeedle) || !strings.Contains(visible, statusMarker) {
		t.Fatalf("inline prompt/status missing after startup replay: visible=%q", visible)
	}
	for _, marker := range []string{promptNeedle, statusMarker} {
		if strings.Contains(scrollback, marker) {
			t.Fatalf("inline viewport marker %q leaked into native scrollback: %q", marker, screen.ScrollbackLines())
		}
	}
	assertPhysicalMarkersExactlyOnce(t, h.physical.String(), width, height, markers)
}

var absoluteCUPPattern = regexp.MustCompile("\\x1b\\[([0-9]+);([0-9]+)H")

func assertNoWholeDisplayClear(t *testing.T, raw string) {
	t.Helper()
	for _, sequence := range []string{"\x1b[2J", "\x1b[3J", "\x1b[J", "\x1b[0J", "\x1bc"} {
		if strings.Contains(raw, sequence) {
			t.Fatalf("primary inline transaction emitted whole-display clear %q in %q", sequence, raw)
		}
	}
}

// Once the top history region belongs to terminal scrollback, a viewport
// repaint must be physically incapable of clearing or addressing that region.
func TestInlineViewportUpdateCannotClearOrRewriteHistoryRegion(t *testing.T) {
	const width, height = 60, 12
	h := newNativeScrollbackRegressionHarness(t)
	h.post(t,
		Resize{Width: width, Height: height, Generation: 1},
		SetStatusModelAction{Status: style.StatusLineModel{State: style.RunReady, StateText: "STATUS-FENCE"}},
		SetPromptStateAction{Line: "> ", Input: "first", Rows: 1, CursorRow: 0, CursorCol: 7},
	)
	h.flush()
	assertNoWholeDisplayClear(t, h.physical.String())

	h.physical.Reset()
	h.post(t, SetPromptStateAction{Line: "> ", Input: "second", Rows: 1, CursorRow: 0, CursorCol: 8})
	state := h.controller.State()
	viewportTop := ComposeTerminalFramePlan(state.AppState).OutputBottomRow + 1
	if viewportTop <= 1 || viewportTop > height {
		t.Fatalf("fixture did not allocate a bounded bottom viewport: top=%d state=%+v", viewportTop, state.Bottom)
	}
	h.flush()

	raw := h.physical.String()
	if raw == "" || !strings.Contains(raw, "second") {
		t.Fatalf("prompt update produced no physical viewport diff: %q", raw)
	}
	assertNoWholeDisplayClear(t, raw)
	if strings.Contains(raw, "\x1b[H") {
		t.Fatalf("viewport diff used implicit home outside owned region: %q", raw)
	}
	for _, match := range absoluteCUPPattern.FindAllStringSubmatch(raw, -1) {
		row, err := strconv.Atoi(match[1])
		if err != nil {
			t.Fatalf("parse CUP row %q: %v", match[1], err)
		}
		if row < viewportTop || row > height {
			t.Fatalf("viewport diff addressed physical row %d outside %d..%d: %q", row, viewportTop, height, raw)
		}
	}
}

// This opt-in probe writes through the real stdout terminal. It is launched in
// a terminal emulator whose scrollback can be queried independently (for
// example, `wezterm cli get-text`). Unit tests cannot prove host scrollback by
// replaying bytes into vt.Screen, so the probe is skipped in ordinary CI.
func TestRealTTYNativeScrollbackProbe(t *testing.T) {
	if os.Getenv("AICLI_REAL_TTY_PROBE") != "1" {
		t.Skip("set AICLI_REAL_TTY_PROBE=1 and run inside a queryable real terminal")
	}
	fd := int(os.Stdout.Fd())
	if !term.IsTerminal(fd) {
		t.Fatal("AICLI_REAL_TTY_PROBE requires stdout to be a real terminal")
	}
	width, height, err := term.GetSize(fd)
	if err != nil || width < 40 || height < 10 {
		t.Fatalf("real terminal geometry = %dx%d, err=%v", width, height, err)
	}

	controller := NewUIController(UIControllerConfig{}, nil, nil)
	go controller.Run()
	executor := NewTerminalSessionExecutor(controller, NewTerminalSession(os.Stdout))
	defer func() {
		executor.Close()
		controller.Close()
		controller.WaitIdle()
	}()
	post := func(action UIAction) {
		if !controller.Post(action) {
			t.Fatalf("post %T", action)
		}
	}
	post(Resize{Width: width, Height: height, Generation: 1})
	post(SetStatusModelAction{Status: style.StatusLineModel{State: style.RunReady, StateText: "REALTTY-STATUS-VIEWPORT"}})
	post(ShowPromptAction{Line: "REALTTY-PROMPT-VIEWPORT> "})
	cells := make([]*scene.TranscriptCell, height*3)
	for index := range cells {
		cells[index] = &scene.TranscriptCell{
			ID: scene.CellID(index + 1), Sequence: uint64(index + 1),
			Revision: 1, Kind: scene.KindAssistant,
			Source: fmt.Sprintf("REALTTY-HISTORY-%03d", index), Phase: scene.CellCommitted,
		}
	}
	post(ReplaceTranscriptAction{Snapshot: regressionCommittedSnapshot(1, cells...)})
	controller.WaitIdle()
	executor.Request()
	executor.WaitIdle()
	controller.WaitIdle()

	for _, entry := range controller.State().HistoryEffects.Entries() {
		if entry.State != HistoryCommitAcked {
			t.Fatalf("real terminal left history unresolved: %#v", entry)
		}
	}
	hold := 20 * time.Second
	if raw := os.Getenv("AICLI_REAL_TTY_HOLD_MS"); raw != "" {
		if milliseconds, parseErr := strconv.Atoi(raw); parseErr == nil && milliseconds > 0 {
			hold = time.Duration(milliseconds) * time.Millisecond
		}
	}
	time.Sleep(hold)
}
