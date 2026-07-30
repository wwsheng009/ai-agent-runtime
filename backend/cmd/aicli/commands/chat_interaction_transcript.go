package commands

import (
	"strings"
)

const (
	// Bound the in-memory source-backed turn record so long streams cannot grow
	// without limit. Oldest emitted blocks are dropped first; offsets remain
	// authoritative even after block metadata is trimmed.
	assistantTranscriptMaxBlocks = 256
	assistantTranscriptMaxBytes  = 512 * 1024
)

// assistantFinalDivergence classifies how an authoritative final snapshot
// relates to the already-owned source regions of the live turn.
type assistantFinalDivergence int

const (
	// final matches or extends the full enqueued source prefix.
	assistantFinalAppend assistantFinalDivergence = iota
	// final matches the emitted prefix but corrects still-queued content.
	assistantFinalQueueCorrect
	// final rewrites source that was already written to terminal scrollback.
	// Scrollback is a one-way side effect, so callers must not replay the
	// emitted prefix and can only mark consolidation + append residuals.
	assistantFinalEmittedDiverged
	// nothing has been emitted or enqueued yet; final may fully replace.
	assistantFinalReplace
)

// assistantTranscriptBlock is one source-backed unit that has already been
// written to terminal scrollback. Reflow never rewrites these terminal lines;
// the block exists so later finalization/resize logic can reason from source
// ranges instead of ANSI history.
type assistantTranscriptBlock struct {
	SourceStart int
	SourceEnd   int
	Width       int
	Markdown    bool
	Lines       []string
}

// assistantTurnTranscript is the source-backed record for the current assistant
// stream turn. ActiveBand, the stable commit queue, and scrollback emission all
// derive from the same source offsets:
//
//	emittedEnd <= enqueuedEnd <= len(source)
//
// Terminal history cannot be rewritten; the residualAfterEmittedPrefix helper
// suppresses full replay of corrected bodies when final diverges from already-
// emitted source.
type assistantTurnTranscript struct {
	Source             string
	FinalSnapshot      string
	Markdown           bool
	EmittedEnd         int
	EnqueuedEnd        int
	LastEmitWidth      int
	Blocks             []assistantTranscriptBlock
	RetainedSourceBytes int
	LastDivergence     assistantFinalDivergence
}

func (t *assistantTurnTranscript) reset() {
	if t == nil {
		return
	}
	*t = assistantTurnTranscript{}
}

func (t *assistantTurnTranscript) syncFromCoordinator(source string, emittedEnd, enqueuedEnd int, markdown bool) {
	if t == nil {
		return
	}
	t.Source = source
	t.Markdown = markdown
	if emittedEnd < 0 {
		emittedEnd = 0
	}
	if enqueuedEnd < emittedEnd {
		enqueuedEnd = emittedEnd
	}
	if emittedEnd > len(source) {
		emittedEnd = len(source)
	}
	if enqueuedEnd > len(source) {
		enqueuedEnd = len(source)
	}
	t.EmittedEnd = emittedEnd
	t.EnqueuedEnd = enqueuedEnd
}

func (t *assistantTurnTranscript) noteFinalSnapshot(final string) {
	if t == nil {
		return
	}
	t.FinalSnapshot = final
}

func (t *assistantTurnTranscript) recordEmittedBlock(sourceStart, sourceEnd, width int, markdown bool, lines []string) {
	if t == nil || sourceEnd <= sourceStart {
		return
	}
	if width <= 0 {
		width = t.LastEmitWidth
	}
	if width <= 0 {
		width = 80
	}
	blockLines := append([]string(nil), lines...)
	t.Blocks = append(t.Blocks, assistantTranscriptBlock{
		SourceStart: sourceStart,
		SourceEnd:   sourceEnd,
		Width:       width,
		Markdown:    markdown,
		Lines:       blockLines,
	})
	t.LastEmitWidth = width
	if sourceEnd > t.EmittedEnd {
		t.EmittedEnd = sourceEnd
	}
	if sourceEnd > t.EnqueuedEnd {
		t.EnqueuedEnd = sourceEnd
	}
	t.RetainedSourceBytes += sourceEnd - sourceStart
	t.trimToBounds()
}

func (t *assistantTurnTranscript) trimToBounds() {
	if t == nil {
		return
	}
	for len(t.Blocks) > assistantTranscriptMaxBlocks || t.RetainedSourceBytes > assistantTranscriptMaxBytes {
		if len(t.Blocks) == 0 {
			t.RetainedSourceBytes = 0
			return
		}
		dropped := t.Blocks[0]
		t.Blocks = t.Blocks[1:]
		size := dropped.SourceEnd - dropped.SourceStart
		if size > 0 {
			t.RetainedSourceBytes -= size
		}
		if t.RetainedSourceBytes < 0 {
			t.RetainedSourceBytes = 0
		}
	}
}

func (t *assistantTurnTranscript) dropPendingBeyondEmitted() {
	if t == nil {
		return
	}
	t.EnqueuedEnd = t.EmittedEnd
}

// classifyAssistantFinalDivergence reports how final relates to the live source
// ownership cursors. source is the delta-built buffer (not yet replaced by final).
func classifyAssistantFinalDivergence(source string, emittedEnd, enqueuedEnd int, final string) assistantFinalDivergence {
	if emittedEnd < 0 {
		emittedEnd = 0
	}
	if enqueuedEnd < emittedEnd {
		enqueuedEnd = emittedEnd
	}
	if emittedEnd > len(source) {
		emittedEnd = len(source)
	}
	if enqueuedEnd > len(source) {
		enqueuedEnd = len(source)
	}
	if emittedEnd == 0 && enqueuedEnd == 0 {
		return assistantFinalReplace
	}
	if enqueuedEnd > 0 && strings.HasPrefix(final, source[:enqueuedEnd]) {
		return assistantFinalAppend
	}
	if emittedEnd > 0 && strings.HasPrefix(final, source[:emittedEnd]) {
		return assistantFinalQueueCorrect
	}
	if emittedEnd == 0 {
		// Queued-only content can be discarded and fully replaced.
		return assistantFinalQueueCorrect
	}
	return assistantFinalEmittedDiverged
}

// residualAfterEmittedPrefix returns the final content that is safe to paint
// after the already-emitted source prefix. When the final snapshot diverges
// from emitted terminal history, residual is empty and diverged is true so
// callers do not replay corrected body on top of irreversible scrollback.
func residualAfterEmittedPrefix(source string, emittedEnd int, final string) (residual string, diverged bool) {
	if emittedEnd <= 0 {
		return final, false
	}
	if emittedEnd > len(source) {
		emittedEnd = len(source)
	}
	if emittedEnd == 0 {
		return final, false
	}
	emitted := source[:emittedEnd]
	if strings.HasPrefix(final, emitted) {
		return final[emittedEnd:], false
	}
	return "", true
}

func (t *assistantTurnTranscript) applyFinalDivergence(kind assistantFinalDivergence) {
	if t == nil {
		return
	}
	t.LastDivergence = kind
	switch kind {
	case assistantFinalEmittedDiverged:
		// Residual helpers suppress full replay; drop only mutable queue.
		t.dropPendingBeyondEmitted()
	case assistantFinalQueueCorrect:
		t.dropPendingBeyondEmitted()
	case assistantFinalReplace:
		t.EmittedEnd = 0
		t.EnqueuedEnd = 0
		t.Blocks = nil
		t.RetainedSourceBytes = 0
	case assistantFinalAppend:
		// Keep ownership cursors; caller drains or appends residual.
	}
}

func (t *assistantTurnTranscript) debugParts() []string {
	if t == nil {
		return nil
	}
	parts := []string{
		"stream_transcript_blocks=" + itoaDecimal(len(t.Blocks)),
		"stream_transcript_bytes=" + itoaDecimal(t.RetainedSourceBytes),
		"stream_final_divergence=" + divergenceToken(t.LastDivergence),
	}
	return parts
}

func divergenceToken(kind assistantFinalDivergence) string {
	switch kind {
	case assistantFinalAppend:
		return "append"
	case assistantFinalQueueCorrect:
		return "queue_correct"
	case assistantFinalEmittedDiverged:
		return "emitted_diverged"
	case assistantFinalReplace:
		return "replace"
	default:
		return "unknown"
	}
}

func boolToken(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

func itoaDecimal(v int) string {
	if v == 0 {
		return "0"
	}
	negative := v < 0
	if negative {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if negative {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
