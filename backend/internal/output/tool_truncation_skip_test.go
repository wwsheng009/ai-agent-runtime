package output

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

// TestSkipsRenderTruncationCanonicalKey pins that the literal key the render
// layer honors is exactly the exported constant. A rename or typo on either
// side must fail loudly instead of silently re-enabling double truncation.
func TestSkipsRenderTruncationCanonicalKey(t *testing.T) {
	meta := map[string]interface{}{toolresult.MetadataSkipRenderTruncationKey: true}
	if !toolresult.SkipsRenderTruncation(meta) {
		t.Fatalf("expected %q to be honored via canonical key", toolresult.MetadataSkipRenderTruncationKey)
	}
	if !toolTruncatedUpstream(meta) {
		t.Fatalf("expected render layer to treat %q as upstream-truncated", toolresult.MetadataSkipRenderTruncationKey)
	}
}

// TestSkipRenderTruncationBeatsInferredVocabulary pins precedence: an explicit
// opt-out wins even when the inferred vocabulary would otherwise report no
// truncation (is_truncated=false), so controlled tools keep ownership of their
// own window.
func TestSkipRenderTruncationBeatsInferredVocabulary(t *testing.T) {
	meta := map[string]interface{}{
		toolresult.MetadataSkipRenderTruncationKey: true,
		"is_truncated": false,
	}
	if !toolTruncatedUpstream(meta) {
		t.Fatalf("expected explicit %q to win over is_truncated=false", toolresult.MetadataSkipRenderTruncationKey)
	}
}

// TestViewStyleMetadataIsNotDoubleTruncated guards the view tool shape: a
// truncated view result marks is_truncated itself and must be seen as
// upstream-truncated so L4 does not fold the body a second time.
func TestViewStyleMetadataIsNotDoubleTruncated(t *testing.T) {
	meta := map[string]interface{}{
		"is_truncated":          true,
		"total_lines":           1200,
		"suggested_next_offset": 400,
	}
	if !toolTruncatedUpstream(meta) {
		t.Fatal("expected is_truncated=true view metadata to be treated as upstream-truncated")
	}
}

// TestSkipRenderTruncationKeepsOversizedToolPayloadVerbatim pins the point of
// the opt-out: a tool that owns its own budget (view/grep/glob/ls/fetch/
// artifact_read) publishes a payload larger than the render-layer budget, and
// the render layer must pass it through exactly as produced — no head/tail
// fold, no duplicated "middle omitted" marker contradicting the tool's own
// continuation guidance.
func TestSkipRenderTruncationKeepsOversizedToolPayloadVerbatim(t *testing.T) {
	content := strings.TrimSpace(strings.Repeat("file_1.go:1:match line\n", modelToolTextByteBudget/8))
	if len(content) <= modelToolTextByteBudget {
		t.Fatalf("precondition: payload must exceed the render budget, got %d bytes", len(content))
	}
	envelope := &Envelope{
		ToolCallID: "call-skip-render-truncation",
		Metadata: map[string]interface{}{
			toolresult.MetadataSkipRenderTruncationKey: true,
			"truncated": true,
		},
	}

	got := renderToolTextForModelHistory(content, "", envelope)
	if got != content {
		t.Fatalf("stamped payload must pass through verbatim: got %d bytes, want %d", len(got), len(content))
	}
	if strings.Contains(got, "omitted") {
		t.Fatalf("render layer folded a stamped payload: %q", got[:200])
	}
}
