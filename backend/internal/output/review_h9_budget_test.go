package output

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

// TestRenderToolResultContractKeepsToolOwnedBodyWindow pins finding H9: when a
// tool declared its own model-visible window, the contract header is
// control-plane text and must not eat into the payload window the tool
// promised. Before this, a batch body that exactly filled the declared window
// lost its tail (a section whose ledger/dedup entries were already committed).
func TestRenderToolResultContractKeepsToolOwnedBodyWindow(t *testing.T) {
	const declared = 32 * 1024
	marker := "TAIL-SECTION-MARKER"
	body := strings.Repeat("x", declared-len(marker)-1) + "\n" + marker
	metadata := map[string]interface{}{
		toolresult.MetadataModelVisibleBudgetKey: declared,
		toolresult.MetadataPartialFailureKey:     true,
		toolresult.MetadataRequestedCountKey:     2,
		toolresult.MetadataFailedCountKey:        1,
	}
	envelope := &Envelope{ToolName: "view", ToolCallID: "call-h9", Metadata: metadata}

	rendered := RenderToolResultContentForModel(body, "", envelope)
	if !strings.Contains(rendered, marker) {
		t.Fatalf("a tool-owned window must reach the model intact (%d bytes rendered)", len(rendered))
	}
	if !strings.Contains(rendered, "\"ok\":true") && !strings.Contains(rendered, "\"partial_failure\":true") {
		t.Fatalf("expected the partial contract header, got %q", rendered[:min(len(rendered), 200)])
	}
	if len(rendered) < declared {
		t.Fatalf("rendered length %d must be at least the declared body window %d", len(rendered), declared)
	}
}

// TestRenderToolResultContractStillBudgetsUndeclaredBodies pins the other half:
// without a declared window the layer backstop (12 KiB) still applies, header
// included.
func TestRenderToolResultContractStillBudgetsUndeclaredBodies(t *testing.T) {
	body := strings.Repeat("y", 40*1024)
	envelope := &Envelope{ToolName: "grep", ToolCallID: "call-h9-default"}
	rendered := RenderToolResultContentForModel(body, "", envelope)
	if len(rendered) > modelToolTextByteBudget+2*1024 {
		t.Fatalf("undeclared body must stay near the layer budget, got %d bytes", len(rendered))
	}
}
