package commands

import (
	"encoding/json"
	"reflect"
	"testing"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

// TestAttachMergedConfigDocumentParityWithRuntimeServer pins the CLI/web
// parity contract: whatever the aicli effective-config payload carries under
// mergedConfigDocumentKey must be structurally identical to the merged layered
// document that the runtime-server serves for the same config inputs.
func TestAttachMergedConfigDocumentParityWithRuntimeServer(t *testing.T) {
	payload := map[string]interface{}{
		"providers": []interface{}{},
	}

	if err := attachMergedConfigDocument(payload); err != nil {
		t.Fatalf("attachMergedConfigDocument: %v", err)
	}

	attached, ok := payload[mergedConfigDocumentKey]
	if !ok {
		t.Fatalf("payload missing %q key; CLI did not surface the merged document", mergedConfigDocumentKey)
	}

	serverDoc, err := config.LoadMergedRuntimeConfigDocument()
	if err != nil {
		t.Fatalf("LoadMergedRuntimeConfigDocument: %v", err)
	}

	serverJSON, err := json.Marshal(serverDoc)
	if err != nil {
		t.Fatalf("marshal runtime-server document: %v", err)
	}
	cliJSON, err := json.Marshal(attached)
	if err != nil {
		t.Fatalf("marshal CLI-attached document: %v", err)
	}

	var want, got interface{}
	if err := json.Unmarshal(serverJSON, &want); err != nil {
		t.Fatalf("unmarshal runtime-server document: %v", err)
	}
	if err := json.Unmarshal(cliJSON, &got); err != nil {
		t.Fatalf("unmarshal CLI-attached document: %v", err)
	}

	if !reflect.DeepEqual(want, got) {
		t.Fatalf("CLI merged document differs from runtime-server document:\nCLI:    %s\nserver: %s", cliJSON, serverJSON)
	}
}

// TestAttachMergedConfigDocumentNilPayload documents the fail-soft contract for
// flag-specific payload shapes (structs/slices) that are not maps.
func TestAttachMergedConfigDocumentNilPayload(t *testing.T) {
	if err := attachMergedConfigDocument(nil); err != nil {
		t.Fatalf("attachMergedConfigDocument(nil): %v", err)
	}
}
