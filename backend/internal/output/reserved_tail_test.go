package output

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/observability"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

// reservedTailFixture mirrors what Bridge.AppendToResult puts at the tail of an
// edit receipt (docs/lsp 03 I1): the block plus its trailing newline. It is one
// instance of the generic appended-block shape, not the only allowed one.
const reservedTailFixture = "<lsp_diagnostics file=\"main.go\" count=\"1\" shown=\"1\" scope=\"all\" servers=\"gopls\">\n" +
	"error 3:9 [gopls] undefined: os\n" +
	"</lsp_diagnostics>\n"

// nonLSPTailFixture is a second producer's block: a different tag, a
// self-closing form, and content that is not diagnostics. It exists to keep the
// contract honest — if the fold only ever recognizes <lsp_...>, this is the test
// that fails.
const nonLSPTailFixture = "<tool_receipt_ref mode=\"full\">\n" +
	"backup kept at .trash/2026-10-02/undo.json\n" +
	"</tool_receipt_ref>\n"

func reservedTailEnvelope(declared int) *Envelope {
	metadata := map[string]interface{}{}
	if declared != 0 {
		metadata[toolresult.MetadataReservedTailBytesKey] = declared
	}
	return &Envelope{ToolName: "edit", ToolCallID: "call-tail", Metadata: metadata}
}

// The whole point of the declaration: a body that blows the budget still reaches
// the model with the appended block attached, because the head-only fold would
// otherwise drop exactly the tail the producer appended.
func TestReservedTailSurvivesHeadOnlyFold(t *testing.T) {
	body := strings.Repeat("receipt line for a very large edit result\n", 2*1024)
	full := body + reservedTailFixture

	rendered := RenderToolResultContentForModel(full, "", reservedTailEnvelope(len(reservedTailFixture)))

	if !hasTail(rendered) {
		t.Fatalf("the declared tail must survive the fold verbatim, got tail:\n%s", tailOf(rendered))
	}
	if !strings.Contains(rendered, "Tool result lines:") {
		t.Fatalf("the head must still be folded to a window, got:\n%s", headOf(rendered))
	}
	// Exempt means exempt: the block rides on top of the budget instead of
	// competing with the head for it.
	if limit := modelToolTextByteBudget + len(reservedTailFixture) + 2; len(rendered) > limit {
		t.Fatalf("rendered %d bytes exceeds budget+reserved tail (%d)", len(rendered), limit)
	}
}

// The declaration is a generic contract, not an LSP one: a block from any
// producer, under any tag, gets the same exemption. Without this the feature
// would only work for diagnostics and the "generic" claim would be untrue.
func TestNonLSPProducerTailIsExemptedToo(t *testing.T) {
	body := strings.Repeat("receipt line for a very large edit result\n", 2*1024)
	full := body + nonLSPTailFixture

	rendered := RenderToolResultContentForModel(full, "", reservedTailEnvelope(len(nonLSPTailFixture)))

	if !strings.Contains(rendered, "output truncated for history safety") {
		t.Fatalf("this test is only meaningful if the fold ran, got:\n%s", headOf(rendered))
	}
	if !strings.HasSuffix(strings.TrimSpace(rendered), strings.TrimSpace(nonLSPTailFixture)) {
		t.Fatalf("a non-LSP appended block must survive too, got tail:\n%s", tailOf(rendered))
	}
	// The generic notice must not name a producer the model never heard of.
	if strings.Contains(rendered, "LSP diagnostics") {
		t.Fatalf("the fold notice must stay producer-neutral, got:\n%s", headOf(rendered))
	}
}

// A self-closing block satisfies the delimiter contract too, so it must not be
// rejected as unterminated.
func TestSelfClosingTailIsExempted(t *testing.T) {
	const block = "<tool_receipt_ref mode=\"full\"/>\n"
	full := strings.Repeat("receipt line\n", 2*1024) + block

	rendered := RenderToolResultContentForModel(full, "", reservedTailEnvelope(len(block)))

	if !strings.Contains(rendered, "output truncated for history safety") {
		t.Fatalf("this test is only meaningful if the fold ran, got:\n%s", headOf(rendered))
	}
	if !strings.HasSuffix(strings.TrimSpace(rendered), strings.TrimSpace(block)) {
		t.Fatalf("a self-closing block must be exempt too, got tail:\n%s", tailOf(rendered))
	}
}

// A tag-shaped line in the BODY must not make a genuine tail look unverified:
// the closing check is scoped to the declared tail, not the whole result.
//
// The body must clear the fold budget, and the test asserts the fold actually
// ran. A body that fits would leave the tail intact for the trivial reason that
// nothing was folded, so the test would pass while proving nothing - the same
// trap as the undeclared control.
func TestTagShapedBodyLineDoesNotInvalidateAGenuineTail(t *testing.T) {
	const body = "<html>\n  <body>fetched page</body>\n</html>\n"
	full := strings.Repeat(body, 2048) + reservedTailFixture

	rendered := RenderToolResultContentForModel(full, "", reservedTailEnvelope(len(reservedTailFixture)))

	if !strings.Contains(rendered, "output truncated for history safety") {
		t.Fatalf("this test is only meaningful if the fold ran, got:\n%s", headOf(rendered))
	}
	if !hasTail(rendered) {
		t.Fatalf("body tags must not disqualify a real tail, got tail:\n%s", tailOf(rendered))
	}
}

// Negative control: without the declaration the fold drops the appended block.
// If this ever starts passing, the test above proves nothing.
func TestUndeclaredTailIsFoldedAway(t *testing.T) {
	full := strings.Repeat("receipt line for a very large edit result\n", 2*1024) + reservedTailFixture

	rendered := RenderToolResultContentForModel(full, "", &Envelope{ToolName: "edit", ToolCallID: "call-nodecl"})

	if strings.Contains(rendered, "<lsp_diagnostics") {
		t.Fatalf("an undeclared tail must keep the plain head-only fold, got:\n%s", tailOf(rendered))
	}
}

// A declaration that does not describe this text (re-rendered body, foreign
// producer, stale metadata) must not exempt arbitrary bytes.
func TestForeignReservedDeclarationFallsBackToHeadOnlyFold(t *testing.T) {
	full := strings.Repeat("receipt line for a very large edit result\n", 2*1024) + "tail that is not an lsp block at all\n"

	// Declares a size that lands mid-receipt, not on the real tail.
	rendered := RenderToolResultContentForModel(full, "", reservedTailEnvelope(32))

	if strings.Contains(rendered, "to preserve the trailing LSP diagnostics block") {
		t.Fatalf("an unhonorable declaration must not claim an exemption, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "Tool result lines:") {
		t.Fatalf("expected the plain head-only fold, got:\n%s", rendered)
	}
}

// Many edited files can append more than the whole budget. The block still wins:
// it is the model's only copy, and the notice states what was dropped instead of
// leaving a window that reads as complete.
func TestReservedTailWinsWhenItAloneFillsTheBudget(t *testing.T) {
	bigTail := "<lsp_diagnostics file=\"main.go\" count=\"1\" shown=\"1\" scope=\"all\" servers=\"gopls\">\n" +
		strings.Repeat("error 1:1 [gopls] undefined: os\n", 600) +
		"</lsp_diagnostics>\n"
	full := strings.Repeat("receipt line\n", 2*1024) + bigTail

	rendered := RenderToolResultContentForModel(full, "", reservedTailEnvelope(len(bigTail)))

	bigTail = strings.TrimRight(bigTail, "\n")
	if !strings.HasSuffix(strings.TrimSpace(rendered), bigTail) {
		t.Fatalf("the preserved block must survive even when it exceeds the budget, got tail:\n%s", tailOf(rendered))
	}
	if !strings.Contains(rendered, "to preserve the trailing LSP diagnostics block") {
		t.Fatalf("dropping the head must be stated, got:\n%s", headOf(rendered))
	}
}

// declared == len(full): the result is nothing but the block (a mutation whose
// own output was empty). Nothing may be folded away.
func TestWholeResultIsReservedTail(t *testing.T) {
	full := "<lsp_diagnostics file=\"main.go\" count=\"900\" shown=\"900\" scope=\"all\" servers=\"gopls\">\n" +
		strings.Repeat("error 9:1 [gopls] undefined: x\n", 900) +
		"</lsp_diagnostics>\n"

	rendered := RenderToolResultContentForModel(full, "", reservedTailEnvelope(len(full)))

	if strings.TrimSpace(rendered) != strings.TrimSpace(full) {
		t.Fatalf("a result that is entirely reserved must pass through untouched (%d -> %d bytes)", len(full), len(rendered))
	}
}

// Below the budget nothing is folded, so the declaration must not change a byte.
func TestReservedDeclarationIsInertWhenBodyFits(t *testing.T) {
	full := "patch applied\n" + reservedTailFixture

	rendered := RenderToolResultContentForModel(full, "", reservedTailEnvelope(len(reservedTailFixture)))

	if strings.TrimSpace(rendered) != strings.TrimSpace(full) {
		t.Fatalf("a fitting body must render unchanged, got:\n%s", rendered)
	}
}

// TestReservedTailSplitRejectsImpossibleDeclarations pins the checks that stop
// a stale or foreign declaration from exempting arbitrary bytes. The tags here
// are deliberately NOT <lsp_...>: rejection must be structural, not name-based.
func TestReservedTailSplitRejectsImpossibleDeclarations(t *testing.T) {
	const block = "<tool_receipt_ref mode=\"full\"/>"
	for _, tc := range []struct {
		name     string
		full     string
		declared int
		want     int
	}{
		{name: "zero", full: "body\n", declared: 0, want: -1},
		{name: "negative", full: "body\n", declared: -8, want: -1},
		{name: "no block in text", full: "body without any marker at all\n", declared: 24, want: -1},
		{name: "block not line initial", full: "body " + block, declared: len(block) + 5, want: -1},
		{name: "block not closed", full: "body\n<tool_receipt_ref mode=\"full\">\n", declared: 34, want: -1},
		{name: "size describes other text", full: "body\n" + block, declared: len(block) + 400, want: -1},
		{name: "whole text is block", full: block, declared: len(block), want: 0},
		{name: "exact declaration", full: "body\n" + block, declared: len(block), want: 5},
		// The producer measures before the render layer trims the trailing
		// newline, so the declaration runs one byte past the block.
		{name: "trailing newline drift", full: "body\n" + block, declared: len(block) + 1, want: 5},
	} {
		if got := reservedTailSplit(tc.full, tc.declared); got != tc.want {
			t.Fatalf("%s: reservedTailSplit = %d, want %d", tc.name, got, tc.want)
		}
	}
}

// rendered trims the outermost whitespace of the final body (pre-existing
// contract), so byte-exact assertions compare the trimmed forms: the block's
// own lines must survive intact, which is what the exemption is for.
func hasTail(rendered string) bool {
	return strings.HasSuffix(strings.TrimSpace(rendered), strings.TrimSpace(reservedTailFixture))
}

func tailOf(text string) string {
	if index := strings.Index(text, "<lsp_"); index >= 0 {
		return text[index:]
	}
	return text
}

func headOf(text string) string {
	if index := strings.Index(text, "<lsp_"); index >= 0 {
		return text[:index]
	}
	return text
}

// The default notice says the omitted bytes went "from the end" and tells the
// model to re-issue a narrower call to reach them. Once the tail is re-attached
// both claims are false, and a notice that contradicts the bytes printed around
// it is worse than no notice at all.
func TestPreservedTailNoticeDoesNotClaimTheEndWasOmitted(t *testing.T) {
	body := strings.Repeat("receipt line for a very large edit result\n", 2*1024)
	full := body + reservedTailFixture

	rendered := RenderToolResultContentForModel(full, "", reservedTailEnvelope(len(reservedTailFixture)))

	if strings.Contains(rendered, "from the end]") {
		t.Fatalf("a preserved tail must not reuse the dropped-tail notice, got:\n%s", headOf(rendered))
	}
	if !strings.Contains(rendered, "omitted from the middle of this window") {
		t.Fatalf("the fold must state that the loss is the middle, got:\n%s", headOf(rendered))
	}
	if !strings.Contains(rendered, "the reserved block below this notice is complete") {
		t.Fatalf("the model must be told the block below is whole, got:\n%s", headOf(rendered))
	}
	// Re-issue advice is wrong for a receipt: the tool already wrote the file, so
	// running it again produces nothing new.
	if strings.Contains(rendered, "re-issue the same call with a narrower window") {
		t.Fatalf("re-issue advice contradicts a preserved tail, got:\n%s", headOf(rendered))
	}
	// The notice is reserved before the head is cut, so the window still fits.
	if limit := modelToolTextByteBudget + len(reservedTailFixture) + 2; len(rendered) > limit {
		t.Fatalf("rendered %d bytes exceeds budget+reserved tail (%d)", len(rendered), limit)
	}
}

// A tool that already owns its window (skip_render_truncation) never reaches the
// fold, so it needs no exemption. This is the bash case, and it must stay a
// pass-through: re-folding a window the tool says it owns is the double-charge
// the whole opt-out exists to prevent.
func TestOwnedWindowToolKeepsTailWithoutExemption(t *testing.T) {
	full := strings.Repeat("shell output line\n", 2*1024) + reservedTailFixture
	envelope := &Envelope{ToolName: "bash", ToolCallID: "call-owned", Metadata: map[string]interface{}{
		toolresult.MetadataKey:                     toolresult.KindText,
		toolresult.MetadataSkipRenderTruncationKey: true,
		toolresult.MetadataReservedTailBytesKey:    len(reservedTailFixture),
		toolresult.MetadataModelVisibleBudgetKey:   32 * 1024,
	}}

	rendered := RenderToolResultContentForModel(full, "", envelope)

	// The outermost trim of the final body is pre-existing contract behavior;
	// what matters here is that no fold ran and no byte was dropped.
	if strings.TrimSpace(rendered) != strings.TrimSpace(full) {
		t.Fatalf("an owned window must pass through untouched (%d -> %d bytes)", len(full), len(rendered))
	}
}

// The exemption is invisible in the truncation counter: those bytes are folded
// like any other head bytes. Without a separate signal, "the block never reached
// the model" and "the block was there and survived" look identical, so a
// regression that stops honoring declarations cannot be detected from metrics.
func TestReservedTailOutcomeIsCounted(t *testing.T) {
	prev := observability.GlobalMetrics
	observability.GlobalMetrics = observability.NewRegistry()
	t.Cleanup(func() { observability.GlobalMetrics = prev })

	body := strings.Repeat("receipt line for a very large edit result\n", 2*1024)

	// 1. Declared and honored.
	RenderToolResultContentForModel(body+reservedTailFixture, "", reservedTailEnvelope(len(reservedTailFixture)))
	// 2. Declared but unhonorable: the size lands on receipt bytes, not a block.
	RenderToolResultContentForModel(body+"tail that is not a delimited block at all\n", "", reservedTailEnvelope(32))
	// 3. Nothing declared: the common case must not read as a defect.
	RenderToolResultContentForModel(body, "", &Envelope{ToolName: "edit", ToolCallID: "call-n"})

	reserved := observability.SnapshotToolEfficiency().ArtifactFlow.ReservedTail
	for outcome, want := range map[string]float64{
		observability.ReservedTailPreserved:  1,
		observability.ReservedTailRejected:   1,
		observability.ReservedTailUndeclared: 1,
	} {
		if reserved[outcome] != want {
			t.Fatalf("reserved_tail[%s] = %v, want %v (full: %v)", outcome, reserved[outcome], want, reserved)
		}
	}
}
