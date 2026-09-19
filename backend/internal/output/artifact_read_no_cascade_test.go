package output

import (
	"context"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/artifact"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

// Regression: artifact_read results used to be re-archived by the gateway and
// wrapped with another "Full raw output artifact_id" pointer, so every
// dereference hop grew a new pointer line pointing at the previous pointer.
func TestGateway_SkipsReArchivingArtifactReadWindows(t *testing.T) {
	store, err := artifact.NewStore(nil)
	if err != nil {
		t.Fatalf("create artifact store: %v", err)
	}
	defer func() { _ = store.Close() }()

	sourceID := "art_00000000000000000000000000000001"
	envelope, err := NewGateway(store).Process(context.Background(), RawToolResult{
		SessionID:  "session-1",
		ToolName:   "artifact_read",
		ToolCallID: "call-read-window",
		Content:    "artifact " + sourceID + " | window=[0,32) | eof=true\n\nraw window bytes",
		Metadata: map[string]interface{}{
			artifactSourceIDMetadataKey: sourceID,
			toolresult.MetadataKey:      toolresult.KindText,
		},
	})
	if err != nil {
		t.Fatalf("process artifact_read output: %v", err)
	}
	if len(envelope.ArtifactIDs) != 0 {
		t.Fatalf("expected no re-archive for artifact_read window, got ids %v", envelope.ArtifactIDs)
	}
	if _, ok := envelope.Metadata["artifact_id"]; ok {
		t.Fatalf("expected no artifact_id metadata on artifact_read window, got %#v", envelope.Metadata["artifact_id"])
	}
}

// Regression: small successful tool results with a record-id pointer used to
// always append the notice, so dereferencing a pointer produced text that
// itself ends in a new pointer. Notice must only survive when the body was
// truncated, the result failed, or the notice points at an on-disk artifact.
func TestRenderToolResultContentForModel_NoIDNoticeOnUntruncatedSuccess(t *testing.T) {
	const id = "art_00000000000000000000000000000002"
	envelope := &Envelope{
		ArtifactIDs: []string{id},
		Metadata: map[string]interface{}{
			"artifact_id":          id,
			"raw_bytes":            42,
			toolresult.MetadataKey: toolresult.KindText,
		},
	}

	got := RenderToolResultContentForModel("small complete output", "", envelope)
	if strings.Contains(got, "Full raw output artifact_id") {
		t.Fatalf("untruncated success must not carry a record-id pointer, got %q", got)
	}
	if !strings.Contains(got, "small complete output") {
		t.Fatalf("expected full body, got %q", got)
	}

	// Failed results keep the pointer (it doubles as the recovery hint).
	failed := RenderToolResultContentForModel("stderr line", "exit status 1", envelope)
	if !strings.Contains(failed, "Full raw output artifact_id: "+id) {
		t.Fatalf("failed result must keep the pointer, got %q", failed)
	}

	// Truncated results keep the pointer.
	big := RenderToolResultContentForModel(strings.Repeat("x", modelToolTextByteBudget+len(id)+64), "", envelope)
	if !strings.Contains(big, "Full raw output artifact_id: "+id) {
		t.Fatalf("truncated result must keep the pointer, got %q", big)
	}

	// Path notices stay unconditional for small results.
	pathEnvelope := &Envelope{
		Metadata: map[string]interface{}{
			"raw_output_artifact_path": `C:\temp\shell-output\toolkit\git_456.txt`,
			toolresult.MetadataKey:     toolresult.KindText,
		},
	}
	withPath := RenderToolResultContentForModel("short output", "", pathEnvelope)
	if !strings.Contains(withPath, "Full raw output artifact: ") {
		t.Fatalf("path notice must stay unconditional, got %q", withPath)
	}
}
