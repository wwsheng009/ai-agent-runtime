package commands

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/formatter"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

func TestClassifyAssistantFinalDivergence(t *testing.T) {
	t.Parallel()

	source := "alpha\nbeta\ngamma\n"
	cases := []struct {
		name        string
		emittedEnd  int
		enqueuedEnd int
		final       string
		want        assistantFinalDivergence
	}{
		{
			name:        "replace when nothing owned",
			emittedEnd:  0,
			enqueuedEnd: 0,
			final:       "fresh body",
			want:        assistantFinalReplace,
		},
		{
			name:        "append when final extends enqueued prefix",
			emittedEnd:  len("alpha\n"),
			enqueuedEnd: len("alpha\nbeta\n"),
			final:       source + "delta\n",
			want:        assistantFinalAppend,
		},
		{
			name:        "queue correct when only pending queue diverges",
			emittedEnd:  len("alpha\n"),
			enqueuedEnd: len("alpha\nbeta\n"),
			final:       "alpha\nbeta-fixed\n",
			want:        assistantFinalQueueCorrect,
		},
		{
			name:        "queue correct when only queue owned",
			emittedEnd:  0,
			enqueuedEnd: len("alpha\n"),
			final:       "replacement\n",
			want:        assistantFinalQueueCorrect,
		},
		{
			name:        "emitted diverged when terminal history already wrote source",
			emittedEnd:  len("alpha\n"),
			enqueuedEnd: len("alpha\nbeta\n"),
			final:       "ALPHA\nbeta\n",
			want:        assistantFinalEmittedDiverged,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := classifyAssistantFinalDivergence(source, tc.emittedEnd, tc.enqueuedEnd, tc.final)
			if got != tc.want {
				t.Fatalf("classifyAssistantFinalDivergence() = %v (%s), want %v (%s)",
					got, divergenceToken(got), tc.want, divergenceToken(tc.want))
			}
		})
	}
}

func TestResidualAfterEmittedPrefix(t *testing.T) {
	t.Parallel()

	source := "alpha\nbeta\n"
	residual, diverged := residualAfterEmittedPrefix(source, len("alpha\n"), source+"gamma\n")
	if diverged {
		t.Fatal("matching final should not report divergence")
	}
	if residual != "beta\ngamma\n" {
		t.Fatalf("residual = %q, want %q", residual, "beta\ngamma\n")
	}

	residual, diverged = residualAfterEmittedPrefix(source, len("alpha\n"), "ALPHA\nbeta\n")
	if !diverged || residual != "" {
		t.Fatalf("diverged residual = (%q, %v), want (\"\", true)", residual, diverged)
	}

	residual, diverged = residualAfterEmittedPrefix(source, 0, "fresh")
	if diverged || residual != "fresh" {
		t.Fatalf("zero emit residual = (%q, %v), want (\"fresh\", false)", residual, diverged)
	}
}

func TestAssistantTurnTranscript_ApplyFinalDivergenceAndBounds(t *testing.T) {
	t.Parallel()

	tr := &assistantTurnTranscript{}
	tr.syncFromCoordinator("alpha\nbeta\ngamma\n", len("alpha\n"), len("alpha\nbeta\n"), true)
	tr.recordEmittedBlock(0, len("alpha\n"), 40, true, []string{"alpha"})
	if tr.EmittedEnd != len("alpha\n") || tr.EnqueuedEnd < tr.EmittedEnd {
		t.Fatalf("unexpected ownership after emit: emitted=%d enqueued=%d", tr.EmittedEnd, tr.EnqueuedEnd)
	}

	tr.applyFinalDivergence(assistantFinalQueueCorrect)
	if tr.EnqueuedEnd != tr.EmittedEnd {
		t.Fatalf("queue correct should drop pending ownership, emitted=%d enqueued=%d", tr.EmittedEnd, tr.EnqueuedEnd)
	}
	if tr.LastDivergence != assistantFinalQueueCorrect {
		t.Fatalf("LastDivergence = %v, want queue_correct", tr.LastDivergence)
	}

	tr.applyFinalDivergence(assistantFinalEmittedDiverged)
	if tr.EnqueuedEnd != tr.EmittedEnd {
		t.Fatalf("emitted divergence should drop pending ownership, emitted=%d enqueued=%d", tr.EmittedEnd, tr.EnqueuedEnd)
	}
	if tr.LastDivergence != assistantFinalEmittedDiverged {
		t.Fatalf("LastDivergence = %v, want emitted_diverged", tr.LastDivergence)
	}

	tr.applyFinalDivergence(assistantFinalReplace)
	if tr.EmittedEnd != 0 || tr.EnqueuedEnd != 0 || len(tr.Blocks) != 0 || tr.RetainedSourceBytes != 0 {
		t.Fatalf("replace should clear ownership, got emitted=%d enqueued=%d blocks=%d bytes=%d",
			tr.EmittedEnd, tr.EnqueuedEnd, len(tr.Blocks), tr.RetainedSourceBytes)
	}
	if tr.LastDivergence != assistantFinalReplace {
		t.Fatalf("LastDivergence = %v, want replace", tr.LastDivergence)
	}

	// Bounds: retain only the newest blocks under the block cap.
	for i := 0; i < assistantTranscriptMaxBlocks+4; i++ {
		start := i * 4
		tr.recordEmittedBlock(start, start+4, 80, false, []string{"row"})
	}
	if len(tr.Blocks) > assistantTranscriptMaxBlocks {
		t.Fatalf("block cap exceeded: %d", len(tr.Blocks))
	}
	if tr.RetainedSourceBytes > assistantTranscriptMaxBytes {
		t.Fatalf("byte cap exceeded: %d", tr.RetainedSourceBytes)
	}
	if first := tr.Blocks[0].SourceStart; first < 4*4 {
		t.Fatalf("oldest blocks should be trimmed first, first start=%d", first)
	}
}

func TestChatInteractionCoordinator_TranscriptRecordsOnlyOnDrain(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	coord.stableCommitDelay = time.Hour
	coord.stableCommitManual = true
	t.Cleanup(coord.Shutdown)

	var output bytes.Buffer
	coord.SetWriter(&output)
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(40, 24)
	coord.SetSurface(surface)

	coord.RenderAssistantDelta("one\ntwo\nthree\nfour\nfive\nsix\nseven\n")
	coord.RenderAssistantDelta("eight\n")

	coord.mu.Lock()
	queued := len(coord.stableCommitQueue)
	enqueued := coord.streamEnqueuedPrefixLen
	emitted := coord.streamRenderedPrefixLen
	blocksBefore := 0
	if coord.transcript != nil {
		blocksBefore = len(coord.transcript.Blocks)
	}
	sequence := coord.stableCommitTimerSeq
	if coord.stableCommitTimer != nil {
		coord.stableCommitTimer.Stop()
		coord.stableCommitTimer = nil
	}
	coord.mu.Unlock()

	if queued == 0 || enqueued <= emitted {
		t.Fatalf("precondition failed: queued=%d enqueued=%d emitted=%d", queued, enqueued, emitted)
	}
	if blocksBefore != 0 {
		t.Fatalf("queued-but-unemitted content must not become transcript Blocks yet, got %d", blocksBefore)
	}
	if summary := coord.DebugSummary(); !strings.Contains(summary, "stream_transcript_blocks=0") {
		t.Fatalf("live debug should report zero transcript blocks while only queued, got %q", summary)
	}

	coord.runActiveStableCommitTick(sequence)

	coord.mu.Lock()
	blocksAfter := len(coord.transcript.Blocks)
	emittedAfter := coord.streamRenderedPrefixLen
	enqueuedAfter := coord.streamEnqueuedPrefixLen
	coord.mu.Unlock()
	if blocksAfter == 0 {
		t.Fatal("drain must record source-backed transcript Blocks")
	}
	if emittedAfter != enqueuedAfter {
		t.Fatalf("drain should advance emitted ownership, emitted=%d enqueued=%d", emittedAfter, enqueuedAfter)
	}
	if got := output.String(); !strings.Contains(got, "one") {
		t.Fatalf("expected scrollback write after drain, got %q", got)
	}
}

func TestChatInteractionCoordinator_FinalQueueCorrectDropsPendingWithoutReplay(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	session := &ChatSession{Formatter: formatter.NewMarkdownFormatter(false)}
	coord := newChatInteractionCoordinator(session)
	coord.stableCommitDelay = time.Hour
	coord.stableCommitManual = true
	t.Cleanup(coord.Shutdown)

	var output bytes.Buffer
	coord.SetWriter(&output)
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(80, 24)
	coord.SetSurface(surface)

	coord.RenderAssistantDelta("- stale item\n")
	coord.RenderAssistantDelta("- retained item\n")

	coord.mu.Lock()
	if len(coord.stableCommitQueue) == 0 || coord.streamRenderedPrefixLen != 0 {
		coord.mu.Unlock()
		t.Fatalf("precondition failed: queue must own unemitted markdown")
	}
	coord.mu.Unlock()

	if !coord.CompleteAssistantResponse("- corrected item\n- retained item\n") {
		t.Fatal("expected active response completion")
	}

	rendered := output.String()
	if strings.Contains(rendered, "stale item") {
		t.Fatalf("queue-correct final must discard pending stale source, got %q", rendered)
	}
	if strings.Count(rendered, "corrected item") != 1 || strings.Count(rendered, "retained item") != 1 {
		t.Fatalf("authoritative final should render once, got %q", rendered)
	}

	summary := coord.DebugSummary()
	for _, expected := range []string{
		"stream_stable_queued=0",
		"stream_prefix_enqueued=0",
		"stream_prefix_emitted=0",
		"stream_final_divergence=queue_correct",
	} {
		if !strings.Contains(summary, expected) {
			t.Fatalf("DebugSummary missing %q after queue-correct finalization: %q", expected, summary)
		}
	}
	if strings.Contains(summary, "stream_needs_consolidation=") || strings.Contains(summary, "stream_emitted_diverged=") {
		t.Fatalf("DebugSummary still exposes removed divergence flags: %q", summary)
	}
}

func TestChatInteractionCoordinator_EmittedDivergenceSuppressesFullReplay(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	session := &ChatSession{Formatter: formatter.NewMarkdownFormatter(false)}
	coord := newChatInteractionCoordinator(session)
	coord.stableCommitDelay = time.Hour
	coord.stableCommitManual = true
	t.Cleanup(coord.Shutdown)

	var output bytes.Buffer
	coord.SetWriter(&output)
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(40, 24)
	coord.SetSurface(surface)

	// Build enough plain lines for a stable scrollback cut, then force one drain
	// so terminal history owns an irreversible prefix.
	coord.RenderAssistantDelta("one\ntwo\nthree\nfour\nfive\nsix\nseven\n")
	coord.RenderAssistantDelta("eight\n")

	coord.mu.Lock()
	sequence := coord.stableCommitTimerSeq
	if coord.stableCommitTimer != nil {
		coord.stableCommitTimer.Stop()
		coord.stableCommitTimer = nil
	}
	coord.mu.Unlock()
	coord.runActiveStableCommitTick(sequence)

	coord.mu.Lock()
	emittedBefore := coord.streamRenderedPrefixLen
	buffered := coord.streamBuffer.String()
	if emittedBefore == 0 || !strings.HasPrefix(buffered, "one\n") {
		coord.mu.Unlock()
		t.Fatalf("precondition failed: emitted=%d buffer=%q", emittedBefore, buffered)
	}
	// Authoritative final rewrites the already-emitted prefix. Residual helpers
	// must suppress full corrected-body replay on top of irreversible scrollback.
	final := "ONE\n" + buffered[len("one\n"):]
	coord.mu.Unlock()

	if !coord.CompleteAssistantResponse(final) {
		t.Fatal("expected completion after emitted prefix")
	}

	rendered := output.String()
	if strings.Count(rendered, "ONE") != 0 {
		t.Fatalf("emitted divergence must not rewrite terminal history with corrected body, got %q", rendered)
	}
	if strings.Count(rendered, "one") != 1 {
		t.Fatalf("already-emitted source should remain exactly once, got %q", rendered)
	}

	summary := coord.DebugSummary()
	if !strings.Contains(summary, "stream_final_divergence=emitted_diverged") {
		t.Fatalf("DebugSummary missing stream_final_divergence=emitted_diverged after emitted divergence: %q", summary)
	}
	if strings.Contains(summary, "stream_needs_consolidation=") || strings.Contains(summary, "stream_emitted_diverged=") {
		t.Fatalf("DebugSummary still exposes removed divergence flags: %q", summary)
	}
}

func TestChatInteractionCoordinator_RefreshRebuildsPendingStableQueue(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	session := &ChatSession{Formatter: formatter.NewMarkdownFormatter(false)}
	coord := newChatInteractionCoordinator(session)
	coord.stableCommitDelay = time.Hour
	coord.stableCommitManual = true
	t.Cleanup(coord.Shutdown)

	var output bytes.Buffer
	coord.SetWriter(&output)
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(80, 24)
	coord.SetSurface(surface)

	coord.RenderAssistantDelta("# Title\n\n")
	coord.RenderAssistantDelta("Hello stable paragraph.\n")
	coord.RenderAssistantDelta("Second stable paragraph.\n")

	coord.mu.Lock()
	if len(coord.stableCommitQueue) == 0 {
		coord.mu.Unlock()
		t.Fatal("expected pending stable queue before resize rebuild")
	}
	beforeQueued := len(coord.stableCommitQueue)
	beforeEnqueued := coord.streamEnqueuedPrefixLen
	beforeEmitted := coord.streamRenderedPrefixLen
	coord.mu.Unlock()

	// Resize rebuild reflows only the still-pending queue from source ranges.
	// Already-emitted scrollback ownership must stay put.
	coord.RefreshActiveStreamViewport()

	coord.mu.Lock()
	afterQueued := len(coord.stableCommitQueue)
	afterEnqueued := coord.streamEnqueuedPrefixLen
	afterEmitted := coord.streamRenderedPrefixLen
	coord.mu.Unlock()

	if afterEmitted != beforeEmitted {
		t.Fatalf("resize must not rewrite emitted ownership: before=%d after=%d", beforeEmitted, afterEmitted)
	}
	if afterEnqueued != beforeEnqueued {
		t.Fatalf("resize should preserve enqueued source ownership: before=%d after=%d", beforeEnqueued, afterEnqueued)
	}
	if afterQueued == 0 && beforeQueued > 0 && beforeEnqueued > beforeEmitted {
		t.Fatal("resize rebuild dropped pending queue unexpectedly")
	}
	if got := output.String(); got != "" {
		t.Fatalf("resize rebuild should not flush scrollback on its own, got %q", got)
	}
}

func TestChatInteractionCoordinator_DebugSummarySurvivesResetStream(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	coord := newChatInteractionCoordinator(&ChatSession{})
	coord.stableCommitManual = true
	t.Cleanup(coord.Shutdown)

	coord.mu.Lock()
	coord.streamingActive = true
	coord.streamRenderedPrefixLen = 5
	coord.streamEnqueuedPrefixLen = 9
	coord.ensureAssistantTranscriptLocked()
	coord.transcript.syncFromCoordinator("hello world", 5, 9, false)
	coord.transcript.recordEmittedBlock(0, 5, 40, false, []string{"hello"})
	coord.transcript.noteFinalSnapshot("hello world!")
	coord.transcript.applyFinalDivergence(assistantFinalAppend)
	coord.mu.Unlock()

	live := coord.DebugSummary()
	for _, expected := range []string{
		"streaming_active=true",
		"stream_prefix_enqueued=9",
		"stream_prefix_emitted=5",
		"stream_transcript_blocks=1",
		"stream_final_divergence=append",
	} {
		if !strings.Contains(live, expected) {
			t.Fatalf("live DebugSummary missing %q: %q", expected, live)
		}
	}

	coord.mu.Lock()
	coord.resetStreamLocked()
	coord.mu.Unlock()

	after := coord.DebugSummary()
	for _, expected := range []string{
		"streaming_active=false",
		"stream_prefix_enqueued=0",
		"stream_prefix_emitted=0",
		"stream_transcript_blocks=1",
		"stream_transcript_bytes=5",
		"stream_final_divergence=append",
	} {
		if !strings.Contains(after, expected) {
			t.Fatalf("post-reset DebugSummary missing %q: %q", expected, after)
		}
	}
	if strings.Contains(after, "stream_needs_consolidation=") || strings.Contains(after, "stream_emitted_diverged=") {
		t.Fatalf("post-reset DebugSummary still exposes removed divergence flags: %q", after)
	}
}

func TestChatInteractionCoordinator_DrainNotesSoftEmittedTail(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	coord.stableCommitDelay = time.Hour
	coord.stableCommitManual = true
	t.Cleanup(coord.Shutdown)

	var output bytes.Buffer
	coord.SetWriter(&output)
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(40, 24)
	coord.SetSurface(surface)

	coord.RenderAssistantDelta("one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\n")
	coord.mu.Lock()
	if len(coord.stableCommitQueue) == 0 {
		coord.mu.Unlock()
		t.Fatal("expected pending stable queue before drain")
	}
	coord.stopActiveStableCommitLocked()
	coord.drainActiveStableCommitLocked(true)
	start := coord.softEmittedSourceStart
	end := coord.softEmittedSourceEnd
	lines := append([]string(nil), coord.softEmittedLines...)
	width := coord.softEmittedWidth
	emitted := coord.streamRenderedPrefixLen
	coord.mu.Unlock()

	if emitted == 0 || end != emitted || end <= start {
		t.Fatalf("soft source range should match emitted ownership: start=%d end=%d emitted=%d", start, end, emitted)
	}
	if len(lines) == 0 {
		t.Fatal("expected soft emitted lines after drain")
	}
	if width != 40 {
		t.Fatalf("soft emit width=%d want 40", width)
	}
	// plainStableScrollbackCut only pushes the overflow head out of the band
	// (bodyRows=7 keeps the newest seven lines). Soft ownership tracks that
 	// drained head, not the still-live ActiveBand tail.
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "one") {
		t.Fatalf("soft lines missing committed overflow head: %q", joined)
	}
 	if !strings.Contains(output.String(), "one") {
 		t.Fatalf("expected scrollback write, got %q", output.String())
 	}
}

func TestChatInteractionCoordinator_SoftEmittedTailReflowsFromSourceOnWidthChange(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	fmtr := formatter.NewMarkdownFormatter(false)
	fmtr.Width = 80
	session := &ChatSession{Formatter: fmtr}
	coord := newChatInteractionCoordinator(session)
	t.Cleanup(coord.Shutdown)

	var output bytes.Buffer
	coord.SetWriter(&output)
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(80, 24)
	coord.SetSurface(surface)

	source := "# Title\n\n" + strings.Repeat("word ", 48) + "end.\n\n"
	coord.mu.Lock()
	coord.streamingActive = true
	coord.streamMode = assistantStreamModeMarkdown
	coord.streamBuffer.WriteString(source)
	coord.streamRenderedPrefixLen = len(source)
	coord.streamEnqueuedPrefixLen = len(source)
	if coord.activeStream != nil {
		coord.activeStream.BeginAssistant("assistant")
		_ = coord.activeStream.SetAssistantSnapshot(source, true)
		coord.activeStream.CommitStablePrefix(len(source))
	}
	wideLines := coord.renderSoftEmittedLinesLocked(0, len(source), 80)
	if len(wideLines) == 0 {
		coord.mu.Unlock()
		t.Fatal("expected wide soft render lines")
	}
	coord.softEmittedSourceStart = 0
	coord.softEmittedSourceEnd = len(source)
	coord.softEmittedLines = append([]string(nil), wideLines...)
	coord.softEmittedWidth = 80
	coord.mu.Unlock()

	// Resize the surface geometry that currentStreamEmitWidthLocked reads.
	surface.EnableForTest(28, 24)

	coord.mu.Lock()
	beforeEmitted := coord.streamRenderedPrefixLen
	coord.reflowSoftEmittedTailLocked()
	afterLines := append([]string(nil), coord.softEmittedLines...)
	afterWidth := coord.softEmittedWidth
	afterStart := coord.softEmittedSourceStart
	afterEnd := coord.softEmittedSourceEnd
	afterEmitted := coord.streamRenderedPrefixLen
	coord.mu.Unlock()

	if afterEmitted != beforeEmitted {
		t.Fatalf("reflow must not change emitted ownership: before=%d after=%d", beforeEmitted, afterEmitted)
	}
	if afterStart != 0 || afterEnd != len(source) {
		t.Fatalf("reflow must keep source range 0..%d, got %d..%d", len(source), afterStart, afterEnd)
	}
	if afterWidth != 28 {
		t.Fatalf("soft width=%d want 28", afterWidth)
	}
	if len(afterLines) <= len(wideLines) {
		t.Fatalf("narrow reflow should produce more lines: wide=%d narrow=%d\nwide=%q\nnarrow=%q",
			len(wideLines), len(afterLines), wideLines, afterLines)
	}
	joined := strings.Join(afterLines, "\n")
	if !strings.Contains(joined, "Title") || !strings.Contains(joined, "end.") {
		t.Fatalf("reflowed soft lines lost content: %q", joined)
	}
	// surfaceWriter is false (buffer writer), so reflow is bookkeeping-only.
	if got := output.String(); got != "" {
		t.Fatalf("bookkeeping reflow must not rewrite scrollback, got %q", got)
	}
}

func TestChatInteractionCoordinator_SoftReflowClearsWhenLiveSurfaceWindowMissing(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	fmtr := formatter.NewMarkdownFormatter(false)
	fmtr.Width = 80
	session := &ChatSession{Formatter: fmtr}
	coord := newChatInteractionCoordinator(session)
	t.Cleanup(coord.Shutdown)

	var output bytes.Buffer
	coord.SetWriter(&output)
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(80, 24)
	coord.SetSurface(surface)
	// Force the live surface rewrite path even though the writer is a buffer.
	coord.mu.Lock()
	coord.surfaceWriter = true
	// Content must actually reflow (different rendered lines) so the equal-lines
	// early-return does not skip the missing-window clear path.
	source := "# Heading\n\n" + strings.Repeat("wrap ", 40) + "end.\n\n"
	coord.streamingActive = true
	coord.streamMode = assistantStreamModeMarkdown
	coord.streamBuffer.WriteString(source)
	coord.streamRenderedPrefixLen = len(source)
	coord.streamEnqueuedPrefixLen = len(source)
	lines := coord.renderSoftEmittedLinesLocked(0, len(source), 80)
	if len(lines) == 0 {
		coord.mu.Unlock()
		t.Fatal("expected soft render lines at width 80")
	}
	narrowPreview := coord.renderSoftEmittedLinesLocked(0, len(source), 28)
	if stringSlicesEqual(lines, narrowPreview) {
		coord.mu.Unlock()
		t.Fatalf("precondition: source must reflow at narrow width: wide=%q narrow=%q", lines, narrowPreview)
	}
	coord.softEmittedSourceStart = 0
	coord.softEmittedSourceEnd = len(source)
	coord.softEmittedLines = append([]string(nil), lines...)
	coord.softEmittedWidth = 80
	coord.mu.Unlock()

	if surface.SoftOutputTailValid() {
		t.Fatal("precondition: surface soft window should be empty")
	}

	surface.EnableForTest(28, 24)
	coord.mu.Lock()
	coord.reflowSoftEmittedTailLocked()
	if len(coord.softEmittedLines) != 0 || coord.softEmittedSourceEnd != 0 {
		coord.mu.Unlock()
		t.Fatalf("live surface path must drop soft ownership when window is missing: lines=%d end=%d",
			len(coord.softEmittedLines), coord.softEmittedSourceEnd)
	}
	coord.mu.Unlock()
}

func TestChatInteractionCoordinator_RefreshReflowsSoftTailAndKeepsEmittedOwnership(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	fmtr := formatter.NewMarkdownFormatter(false)
	fmtr.Width = 80
	session := &ChatSession{Formatter: fmtr}
	coord := newChatInteractionCoordinator(session)
	coord.stableCommitDelay = time.Hour
	coord.stableCommitManual = true
	t.Cleanup(coord.Shutdown)

	var output bytes.Buffer
	coord.SetWriter(&output)
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	// Plain overflow fixture mirrors StableCommitQueue: bodyRows keep 7 lines,
	// so eight completed lines force a non-empty pending queue.
	surface.EnableForTest(40, 24)
	coord.SetSurface(surface)

	coord.RenderAssistantDelta("one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\n")
	coord.RenderAssistantDelta("nine\nten\n")

	coord.mu.Lock()
	if len(coord.stableCommitQueue) == 0 {
		coord.mu.Unlock()
		t.Fatal("expected pending stable queue")
	}
	coord.stopActiveStableCommitLocked()
	coord.drainActiveStableCommitLocked(true)
	beforeEmitted := coord.streamRenderedPrefixLen
	beforeSoftEnd := coord.softEmittedSourceEnd
	beforeSoftLines := append([]string(nil), coord.softEmittedLines...)
	beforeSoftWidth := coord.softEmittedWidth
	coord.mu.Unlock()

	if beforeSoftEnd == 0 || len(beforeSoftLines) == 0 {
		t.Fatalf("expected soft emitted tail after drain, end=%d lines=%d output=%q",
			beforeSoftEnd, len(beforeSoftLines), output.String())
	}
	if beforeSoftWidth != 40 {
		t.Fatalf("soft width after drain=%d want 40", beforeSoftWidth)
	}

	// Keep more stable content pending so Refresh rebuilds the queue cut while
	// soft reflow only rewrites the already-emitted soft window.
	coord.mu.Lock()
	_ = coord.commitActiveStableScrollbackLocked(false)
	beforeEnqueued := coord.streamEnqueuedPrefixLen
	coord.mu.Unlock()

	surface.EnableForTest(20, 24)
	coord.RefreshActiveStreamViewport()

	coord.mu.Lock()
	afterEmitted := coord.streamRenderedPrefixLen
	afterSoftEnd := coord.softEmittedSourceEnd
	afterSoftWidth := coord.softEmittedWidth
	afterSoftLines := append([]string(nil), coord.softEmittedLines...)
	afterEnqueued := coord.streamEnqueuedPrefixLen
	coord.mu.Unlock()

	if afterEmitted != beforeEmitted {
		t.Fatalf("refresh must not rewrite emitted ownership: before=%d after=%d", beforeEmitted, afterEmitted)
	}
	if afterSoftEnd != beforeSoftEnd {
		t.Fatalf("soft source end changed: before=%d after=%d", beforeSoftEnd, afterSoftEnd)
	}
	if afterSoftWidth != 20 {
		t.Fatalf("soft width=%d want 20", afterSoftWidth)
	}
	if afterEnqueued < beforeEmitted {
		t.Fatalf("enqueued ownership regressed below emitted: enqueued=%d emitted=%d", afterEnqueued, afterEmitted)
	}
	if beforeEnqueued < beforeEmitted {
		t.Fatalf("precondition: enqueued should cover emitted source, enqueued=%d emitted=%d", beforeEnqueued, beforeEmitted)
	}
	joined := strings.Join(afterSoftLines, "\n")
	if !strings.Contains(joined, "one") {
		t.Fatalf("soft reflow lost committed content: %q", joined)
	}
}

func TestChatInteractionCoordinator_SoftEmittedTailTrimsWithSurfaceWindow(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	t.Cleanup(coord.Shutdown)

	var output bytes.Buffer
	coord.SetWriter(&output)
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(40, 40)
	coord.SetSurface(surface)
	coord.mu.Lock()
	coord.surfaceWriter = true
	coord.streamingActive = true
	coord.streamMode = assistantStreamModeText

	var sourceBuilder strings.Builder
	total := ui.SoftOutputTailMaxLines + 12
	lineText := make([]string, total)
	offsets := make([]int, total+1)
	for i := 0; i < total; i++ {
		// Long enough that narrowing width reflows each retained line.
		lineText[i] = fmt.Sprintf("line-%03d wrap-me-please-with-extra-words-here", i)
		sourceBuilder.WriteString(lineText[i])
		sourceBuilder.WriteByte('\n')
		offsets[i+1] = sourceBuilder.Len()
	}
	source := sourceBuilder.String()
	coord.streamBuffer.WriteString(source)
	coord.streamRenderedPrefixLen = len(source)
	coord.streamEnqueuedPrefixLen = len(source)

	for i := 0; i < total; i++ {
		coord.noteSoftEmittedTailLocked(offsets[i], offsets[i+1], 40, []string{lineText[i]})
	}

	softLines := append([]string(nil), coord.softEmittedLines...)
	softStart := coord.softEmittedSourceStart
	softEnd := coord.softEmittedSourceEnd
	softWidth := coord.softEmittedWidth
	coord.mu.Unlock()

	if len(softLines) == 0 || len(softLines) > ui.SoftOutputTailMaxLines {
		t.Fatalf("soft lines=%d want 1..%d", len(softLines), ui.SoftOutputTailMaxLines)
	}
	if softStart <= 0 || softEnd != len(source) {
		t.Fatalf("trimmed soft source should advance start and keep end: start=%d end=%d source=%d", softStart, softEnd, len(source))
	}
	if softWidth != 40 {
		t.Fatalf("soft width=%d want 40", softWidth)
	}
	// Oldest overflowed lines must leave ownership; newest remain.
	joined := strings.Join(softLines, "\n")
	if strings.Contains(joined, lineText[0]) {
		t.Fatalf("soft window still owns dropped head line %q: %q", lineText[0], joined)
	}
	if !strings.Contains(joined, lineText[total-1]) {
		t.Fatalf("soft window lost newest line %q: %q", lineText[total-1], joined)
	}
	if !surface.SoftOutputTailValid() {
		t.Fatal("surface soft tail should stay valid after adopt")
	}
	if surface.SoftOutputTailTrimmed() {
		t.Fatal("adopted soft tail should clear the trimmed flag")
	}
	if got := surface.SoftOutputTailLineCount(); got != len(softLines) {
		t.Fatalf("surface soft count=%d want coordinator %d", got, len(softLines))
	}

	// Resize and reflow the retained suffix only.
	surface.EnableForTest(18, 40)
	coord.mu.Lock()
	beforeStart := coord.softEmittedSourceStart
	beforeEnd := coord.softEmittedSourceEnd
	coord.reflowSoftEmittedTailLocked()
	afterLines := append([]string(nil), coord.softEmittedLines...)
	afterWidth := coord.softEmittedWidth
	afterStart := coord.softEmittedSourceStart
	afterEnd := coord.softEmittedSourceEnd
	coord.mu.Unlock()

	if afterStart != beforeStart || afterEnd != beforeEnd {
		t.Fatalf("reflow changed trimmed source range: before=%d..%d after=%d..%d", beforeStart, beforeEnd, afterStart, afterEnd)
	}
	if afterWidth != 18 {
		t.Fatalf("soft width after reflow=%d want 18", afterWidth)
	}
	if len(afterLines) == 0 {
		t.Fatal("reflow cleared soft ownership after window trim alignment")
	}
	afterJoined := strings.Join(afterLines, "\n")
	if !strings.Contains(afterJoined, "line-") || strings.Contains(afterJoined, lineText[0]) {
		t.Fatalf("reflowed soft tail should keep newest suffix only: %q", afterJoined)
	}
}

func TestChatInteractionCoordinator_ForeignWriteInvalidatesSoftEmittedTail(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	t.Cleanup(coord.Shutdown)

	var output bytes.Buffer
	coord.SetWriter(&output)
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(40, 24)
	coord.SetSurface(surface)

	coord.mu.Lock()
	coord.surfaceWriter = true
	coord.streamingActive = true
	source := "alpha owned soft line\n"
	coord.streamBuffer.WriteString(source)
	coord.streamRenderedPrefixLen = len(source)
	coord.noteSoftEmittedTailLocked(0, len(source), 40, []string{"alpha owned soft line"})
	if len(coord.softEmittedLines) == 0 {
		coord.mu.Unlock()
		t.Fatal("precondition: soft ownership should exist")
	}
	if !surface.SoftOutputTailValid() {
		coord.mu.Unlock()
		t.Fatal("precondition: surface soft tail should exist after note/adopt")
	}

	// Tool/notice style output is not a soft-committed assistant drain.
	coord.writeLineLocked("tool result foreign line")
	if len(coord.softEmittedLines) != 0 || coord.softEmittedSourceEnd != 0 {
		coord.mu.Unlock()
		t.Fatalf("foreign write must drop coordinator soft ownership: lines=%d end=%d",
			len(coord.softEmittedLines), coord.softEmittedSourceEnd)
	}
	coord.mu.Unlock()

	if surface.SoftOutputTailValid() {
		t.Fatal("foreign write must invalidate surface soft tail")
	}
}

// End-to-end progressive commit path with a live surface writer:
// deltas → stable queue → soft-tracked drain → surface soft ownership →
// resize reflow (emitted ownership stable) → reset clears both sides.
func TestChatInteractionCoordinator_ProgressiveCommitSoftTailEndToEnd(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	coord.stableCommitDelay = time.Hour
	coord.stableCommitManual = true
	t.Cleanup(coord.Shutdown)

	var output bytes.Buffer
	coord.SetWriter(&output)
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	// bodyRows keep ~7 lines; eight completed lines force a non-empty queue.
	surface.EnableForTest(40, 24)
	coord.SetSurface(surface)

	coord.mu.Lock()
	// Buffer writers default surfaceWriter=false; force the live surface path
	// so drain uses WriteSoftTrackedOutput and soft adopt reaches the surface.
	coord.surfaceWriter = true
	coord.mu.Unlock()

	coord.RenderAssistantDelta("one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\n")
	coord.RenderAssistantDelta("nine\nten\n")

	coord.mu.Lock()
	if len(coord.stableCommitQueue) == 0 {
		coord.mu.Unlock()
		t.Fatal("expected pending stable queue before drain")
	}
	coord.stopActiveStableCommitLocked()
	coord.drainActiveStableCommitLocked(true)

	emitted := coord.streamRenderedPrefixLen
	softStart := coord.softEmittedSourceStart
	softEnd := coord.softEmittedSourceEnd
	softLines := append([]string(nil), coord.softEmittedLines...)
	softWidth := coord.softEmittedWidth
	blocks := 0
	if coord.transcript != nil {
		blocks = len(coord.transcript.Blocks)
	}
	coord.mu.Unlock()

	if emitted == 0 || softEnd != emitted || softEnd <= softStart {
		t.Fatalf("soft source range must match emitted ownership: start=%d end=%d emitted=%d", softStart, softEnd, emitted)
	}
	if len(softLines) == 0 {
		t.Fatal("expected coordinator soft lines after drain")
	}
	if softWidth != 40 {
		t.Fatalf("soft width after drain=%d want 40", softWidth)
	}
	if blocks == 0 {
		t.Fatal("transcript must record irreversible scrollback blocks on drain")
	}
	if !surface.SoftOutputTailValid() {
		t.Fatal("live drain must open surface soft rewrite window")
	}
	if got := surface.SoftOutputTailLineCount(); got != len(softLines) {
		t.Fatalf("surface soft line count=%d want coordinator %d", got, len(softLines))
	}
	if !strings.Contains(output.String(), "one") {
		t.Fatalf("expected scrollback write of drained head, got %q", output.String())
	}

	// Keep still-pending stable content so Refresh rebuilds queue cut while
	// soft reflow only rewrites the already-emitted soft window.
	coord.mu.Lock()
	_ = coord.commitActiveStableScrollbackLocked(false)
	beforeEnqueued := coord.streamEnqueuedPrefixLen
	beforeEmitted := coord.streamRenderedPrefixLen
	coord.mu.Unlock()

	surface.EnableForTest(20, 24)
	coord.RefreshActiveStreamViewport()

	coord.mu.Lock()
	afterEmitted := coord.streamRenderedPrefixLen
	afterSoftEnd := coord.softEmittedSourceEnd
	afterSoftWidth := coord.softEmittedWidth
	afterSoftLines := append([]string(nil), coord.softEmittedLines...)
	afterEnqueued := coord.streamEnqueuedPrefixLen
	coord.mu.Unlock()

	if afterEmitted != beforeEmitted {
		t.Fatalf("refresh must not rewrite emitted ownership: before=%d after=%d", beforeEmitted, afterEmitted)
	}
	if afterSoftEnd != softEnd {
		t.Fatalf("soft source end changed across reflow: before=%d after=%d", softEnd, afterSoftEnd)
	}
	if afterSoftWidth != 20 {
		t.Fatalf("soft width after reflow=%d want 20", afterSoftWidth)
	}
	if afterEnqueued < beforeEmitted {
		t.Fatalf("enqueued ownership regressed below emitted: enqueued=%d emitted=%d", afterEnqueued, afterEmitted)
	}
	if beforeEnqueued < beforeEmitted {
		t.Fatalf("precondition: enqueued should cover emitted, enqueued=%d emitted=%d", beforeEnqueued, beforeEmitted)
	}
	if !surface.SoftOutputTailValid() {
		t.Fatal("surface soft window should survive source-backed reflow")
	}
	if got := surface.SoftOutputTailLineCount(); got != len(afterSoftLines) {
		t.Fatalf("surface soft count after reflow=%d want %d", got, len(afterSoftLines))
	}
	joined := strings.Join(afterSoftLines, "\n")
	if !strings.Contains(joined, "one") {
		t.Fatalf("soft reflow lost committed content: %q", joined)
	}

	// Turn end must drop both coordinator and surface soft ownership.
	coord.mu.Lock()
	coord.resetStreamLocked()
	if len(coord.softEmittedLines) != 0 || coord.softEmittedSourceEnd != 0 {
		coord.mu.Unlock()
		t.Fatalf("reset must clear coordinator soft ownership: lines=%d end=%d",
			len(coord.softEmittedLines), coord.softEmittedSourceEnd)
	}
	coord.mu.Unlock()
	if surface.SoftOutputTailValid() {
		t.Fatal("reset must invalidate surface soft rewrite window")
	}
}

// Production resize entry is the live stream paint path: geometry is probed on
// each frame and soft reflow runs without an explicit RefreshActiveStreamViewport
// call (theme/command paths still use Refresh directly).
func TestChatInteractionCoordinator_PaintPathReflowsSoftTailOnResize(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	coord.stableCommitDelay = time.Hour
	coord.stableCommitManual = true
	t.Cleanup(coord.Shutdown)

	var output bytes.Buffer
	coord.SetWriter(&output)
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(40, 24)
	coord.SetSurface(surface)

	coord.mu.Lock()
	coord.surfaceWriter = true
	coord.mu.Unlock()

	coord.RenderAssistantDelta("one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\n")
	coord.RenderAssistantDelta("nine\nten\n")

	coord.mu.Lock()
	if len(coord.stableCommitQueue) == 0 {
		coord.mu.Unlock()
		t.Fatal("expected pending stable queue before drain")
	}
	coord.stopActiveStableCommitLocked()
	coord.drainActiveStableCommitLocked(true)
	beforeEmitted := coord.streamRenderedPrefixLen
	beforeSoftEnd := coord.softEmittedSourceEnd
	beforeSoftWidth := coord.softEmittedWidth
	if beforeSoftWidth != 40 || beforeSoftEnd == 0 || beforeEmitted == 0 {
		coord.mu.Unlock()
		t.Fatalf("precondition soft ownership: width=%d end=%d emitted=%d",
			beforeSoftWidth, beforeSoftEnd, beforeEmitted)
	}
	coord.mu.Unlock()

	if !surface.SoftOutputTailValid() {
		t.Fatal("expected surface soft window after drain")
	}

	// Pin a narrower geometry. EnableForTest updates both terminal size and
	// layout cache, so SyncTerminalGeometry may report sizeChanged=false; the
	// paint path still reflows because softEmittedWidth (40) != current width.
	surface.EnableForTest(20, 24)

	coord.mu.Lock()
	// Direct production entry: scheduled/coalesced frame paint, not Refresh API.
	_ = coord.publishActiveStreamFrameLocked(false)
	afterEmitted := coord.streamRenderedPrefixLen
	afterSoftEnd := coord.softEmittedSourceEnd
	afterSoftWidth := coord.softEmittedWidth
	afterSoftLines := append([]string(nil), coord.softEmittedLines...)
	coord.mu.Unlock()

	if afterEmitted != beforeEmitted {
		t.Fatalf("paint-path resize must not rewrite emitted ownership: before=%d after=%d",
			beforeEmitted, afterEmitted)
	}
	if afterSoftEnd != beforeSoftEnd {
		t.Fatalf("soft source end changed across paint-path reflow: before=%d after=%d",
			beforeSoftEnd, afterSoftEnd)
	}
	if afterSoftWidth != 20 {
		t.Fatalf("paint path must reflow soft width to 20, got %d (was %d)", afterSoftWidth, beforeSoftWidth)
	}
	if !surface.SoftOutputTailValid() {
		t.Fatal("surface soft window should survive paint-path reflow")
	}
	if got := surface.SoftOutputTailLineCount(); got != len(afterSoftLines) {
		t.Fatalf("surface soft count after paint reflow=%d want %d", got, len(afterSoftLines))
	}
	if !strings.Contains(strings.Join(afterSoftLines, "\n"), "one") {
		t.Fatalf("paint-path soft reflow lost committed content: %q", afterSoftLines)
	}
}
