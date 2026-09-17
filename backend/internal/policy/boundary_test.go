package policy

import (
	"strings"
	"testing"
)

func TestRenderReadOnlyBoundaryBlock(t *testing.T) {
	if got := RenderReadOnlyBoundaryBlock(BoundaryManifest{ReadOnly: false}); got != "" {
		t.Fatalf("non read-only manifest should render empty, got %q", got)
	}
	if got := RenderReadOnlyBoundaryBlock(BoundaryManifest{}); got != "" {
		t.Fatalf("zero-value manifest should render empty, got %q", got)
	}
	block := RenderReadOnlyBoundaryBlock(BoundaryManifest{
		ReadOnly:       true,
		Source:         "parent_tool_execution_policy",
		RemovedTools:   []string{"write", "apply_patch"},
		PermissionMode: "read_only",
	})
	for _, want := range []string{
		"parent_tool_execution_policy",
		"write, apply_patch",
		"return the exact required change",
		"read_only=false",
	} {
		if !strings.Contains(block, want) {
			t.Fatalf("boundary block missing %q:\n%s", want, block)
		}
	}
}

func TestReadOnlyEscalationPathTextSingleSource(t *testing.T) {
	// 出路文案是三处消费方（子代理 prompt / 父 spawn 报告 / 拒绝指导）的唯一
	// 来源，必须包含"交给父代理"与"重派 read_only=false"两个要点。
	if !strings.Contains(ReadOnlyEscalationPathText, "read_only=false") {
		t.Fatalf("escalation path must mention read_only=false, got: %s", ReadOnlyEscalationPathText)
	}
	if !strings.Contains(ReadOnlyEscalationPathText, "parent") {
		t.Fatalf("escalation path must mention parent, got: %s", ReadOnlyEscalationPathText)
	}
}
