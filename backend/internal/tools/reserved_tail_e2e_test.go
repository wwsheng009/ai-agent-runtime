package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/lsp"
	"github.com/wwsheng009/ai-agent-runtime/internal/output"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

// These are the end-to-end guards for the reserved-tail contract: the real
// producer renders a block, the manager declares its measured size, and the
// render layer must keep it. The unit tests in internal/output pin the fold
// against hand-written fixtures; only these prove the fixture shape has not
// drifted from what the producer actually emits. A shape change that breaks the
// delimiter contract would silently turn every one of these windows into a plain
// fold, so they are the tripwire for that.

// renderReservedTail folds a receipt plus a real producer block and returns the
// text the model would see. The receipt is far larger than the render budget,
// which is the only condition under which the fold runs at all.
func renderReservedTail(t *testing.T, receipt, block string) string {
	t.Helper()
	metadata := stampReservedTailBytes(map[string]interface{}{}, len(block))
	return output.RenderToolResultContentForModel(receipt+block, "", &output.Envelope{
		ToolName:   "edit",
		ToolCallID: "call-e2e",
		Metadata:   metadata,
	})
}

func assertTailSurvived(t *testing.T, rendered, block string) {
	t.Helper()
	if !strings.HasSuffix(strings.TrimSpace(rendered), strings.TrimSpace(block)) {
		t.Fatalf("the producer's block must survive the fold verbatim.\nblock:\n%s\nrendered tail:\n%s",
			block, rendered[maxInt(0, len(rendered)-600):])
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

const oversizedReceipt = "receipt line for a very large edit result\n" +
	"receipt line for a very large edit result\n" +
	"receipt line for a very large edit result\n" +
	"receipt line for a very large edit result\n" +
	"receipt line for a very large edit result\n"

// A populated diagnostics block: header, body lines, closing tag.
func TestRealDiagnosticsBlockSurvivesTheFold(t *testing.T) {
	block := lsp.RenderDiagnostics([]lsp.Diagnostic{{
		Severity: lsp.SeverityError,
		Message:  "undefined: os",
		Server:   "gopls",
		Range:    lsp.CanonicalRange{Start: lsp.CanonicalPos{Line: 2, Column: 8}},
	}}, lsp.RenderOptions{File: "main.go", Servers: []string{"gopls"}}).Text

	if !strings.HasSuffix(strings.TrimSpace(block), "</lsp_diagnostics>") {
		t.Fatalf("fixture assumption broken: block must be closed, got %q", block)
	}
	assertTailSurvived(t, renderReservedTail(t, strings.Repeat(oversizedReceipt, 512), block), block)
}

// The self-closing "checked, no problems" marker is the one producer shape with
// no closing tag, so it is the only output that exercises the /> branch of the
// delimiter contract.
func TestRealEmptyDiagnosticsMarkerSurvivesTheFold(t *testing.T) {
	block := lsp.RenderDiagnostics(nil, lsp.RenderOptions{
		File: "main.go", Servers: []string{"gopls"}, EmptyCompact: true,
	}).Text

	if !strings.HasSuffix(strings.TrimSpace(block), `"/>`) {
		t.Fatalf("fixture assumption broken: compact marker must self-close, got %q", block)
	}
	assertTailSurvived(t, renderReservedTail(t, strings.Repeat(oversizedReceipt, 512), block), block)
}

// A diagnostic message containing markup must not break the contract: the block
// is delimited by its own tags, and message text is not markup. This is the case
// a naive "the tail is XML" reading would get wrong.
func TestRealDiagnosticsBlockWithMarkupInMessageSurvivesTheFold(t *testing.T) {
	block := lsp.RenderDiagnostics([]lsp.Diagnostic{{
		Severity: lsp.SeverityError,
		Message:  `expected "<int>" but found <string>`,
		Server:   "gopls",
		Range:    lsp.CanonicalRange{Start: lsp.CanonicalPos{Line: 2, Column: 8}},
	}}, lsp.RenderOptions{File: "main.go", Servers: []string{"gopls"}}).Text

	assertTailSurvived(t, renderReservedTail(t, strings.Repeat(oversizedReceipt, 512), block), block)
}

// Degraded blocks are single-line: the opening tag, the reason and the closing
// tag share one line. The tail must still be anchored at its line start, so this
// guards the anchor rather than just the closer. It also drives the real
// manager, so the declared size is measured exactly the way production measures
// it.
func TestRealDegradedBlockSurvivesTheFold(t *testing.T) {
	for _, mode := range []lsp.DegradeMode{lsp.DegradeHint, lsp.DegradeError} {
		t.Run(string(mode), func(t *testing.T) {
			root := t.TempDir()
			target := filepath.Join(root, "main.go")
			if err := os.WriteFile(target, []byte("package main\n"), 0o644); err != nil {
				t.Fatalf("write target: %v", err)
			}
			manager := managerWithMutatingStub(t, strings.Repeat(oversizedReceipt, 512), target)
			manager.lspBridge = bridgeWithUnstartableServer(t, root, mode)

			appended, metadata, err := manager.ExecuteWithMeta(context.Background(), "edit", map[string]interface{}{})
			if err != nil {
				t.Fatalf("ExecuteWithMeta failed: %v", err)
			}
			reserved := toolresult.ReservedTailBytes(metadata)
			if reserved == 0 {
				t.Fatalf("a degraded block is still an appended block and must be declared, got %#v", metadata)
			}

			block := appended[len(appended)-reserved:]
			assertTailSurvived(t, output.RenderToolResultContentForModel(appended, "", &output.Envelope{
				ToolName:   "edit",
				ToolCallID: "call-degraded",
				Metadata:   metadata,
			}), block)
		})
	}
}

// Without a declaration the same real block IS dropped. This is the negative
// control for the two tests above: if it ever starts passing, they prove
// nothing about the exemption.
//
// The assertion is on the block's structural marker, not on the error text. The
// fold separately promotes the earliest failure line out of the tail it drops
// (firstFailureLine), so "undefined: os" is expected to survive in the header -
// that rescue is a different feature. What must NOT survive is the block itself:
// the model has no second call to recover the rest of the diagnostics from.
func TestRealDiagnosticsBlockIsFoldedAwayWithoutDeclaration(t *testing.T) {
	block := lsp.RenderDiagnostics([]lsp.Diagnostic{{
		Severity: lsp.SeverityError,
		Message:  "undefined: os",
		Server:   "gopls",
		Range:    lsp.CanonicalRange{Start: lsp.CanonicalPos{Line: 2, Column: 8}},
	}}, lsp.RenderOptions{File: "main.go", Servers: []string{"gopls"}}).Text

	rendered := output.RenderToolResultContentForModel(
		strings.Repeat(oversizedReceipt, 512)+block, "", &output.Envelope{
			ToolName:   "edit",
			ToolCallID: "call-undeclared",
			Metadata:   map[string]interface{}{},
		})

	if strings.Contains(rendered, "<lsp_diagnostics") {
		t.Fatalf("an undeclared block must be folded away, got:\n%s", rendered)
	}
}
