package remotetools

// The refusal helpers are the contract that lets a caller branch on WHY a tool
// said no, instead of matching prose. Text stays complete for the model; the code
// and failure_class ride along as metadata, same keys as the local surface.

import (
	"errors"
	"strings"
	"testing"
)

func TestEnvdToolErrorPublishesTheCode(t *testing.T) {
	refusal := envdToolError(newToolError(CodePathDenied, "use a path inside the configured workspace",
		"path %q is outside the allowed roots", "/etc/passwd"))
	if refusal.Success {
		t.Fatal("a refusal must not report success")
	}
	if refusal.Error == nil {
		t.Fatal("the MCP layer needs Error to render IsError")
	}
	if refusal.Content == "" {
		t.Fatal("the model-visible text is empty")
	}
	if refusal.Metadata["error_code"] != CodePathDenied {
		t.Fatalf("metadata = %v, want error_code %s", refusal.Metadata, CodePathDenied)
	}
	// Text keeps the CODE: message (next: …) shape the local surface prints.
	if !strings.Contains(refusal.Content, "PATH_DENIED") || !strings.Contains(refusal.Content, "outside the allowed roots") {
		t.Fatalf("text = %q, want the local code-prefixed rendering", refusal.Content)
	}
}

func TestEnvdToolFallsBackForPlainErrors(t *testing.T) {
	refusal := envdToolError(errors.New("dial tcp: connection refused"))
	if refusal.Success {
		t.Fatal("plain errors must still refuse")
	}
	if _, ok := refusal.Metadata["error_code"]; ok {
		t.Fatalf("a non-ToolError must not invent a code: %v", refusal.Metadata)
	}
	if !strings.Contains(refusal.Content, "connection refused") {
		t.Fatalf("text = %q", refusal.Content)
	}
}

func TestEnvdFailureCarriesStructuredFields(t *testing.T) {
	refusal := envdFailureMeta("INVALID_ARGUMENT", map[string]interface{}{
		"failure_class": "ambiguous_edit",
		"occurrences":   2,
	}, "ambiguous edit: old_string 命中 %d 处", 2)
	if refusal.Metadata["failure_class"] != "ambiguous_edit" {
		t.Fatalf("metadata = %v", refusal.Metadata)
	}
	if refusal.Metadata["error_code"] != "INVALID_ARGUMENT" {
		t.Fatalf("metadata lost the code: %v", refusal.Metadata)
	}
	if refusal.Metadata["occurrences"] != 2 {
		t.Fatalf("occurrences = %v", refusal.Metadata["occurrences"])
	}
}
