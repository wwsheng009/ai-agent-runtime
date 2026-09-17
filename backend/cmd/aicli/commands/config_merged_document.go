package commands

import (
	"encoding/json"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

// mergedConfigDocumentKey is the payload key under which the CLI exposes the
// shared layered/merged effective config document.
const mergedConfigDocumentKey = "mergedDocument"

// attachMergedConfigDocument mirrors the same layered/merged effective config
// document that the runtime-server serves over GET /api/runtime/config/document
// into the CLI's effective-config payload, so that the CLI and the web surface
// describe one identical effective config.
//
// The document is round-tripped through JSON so the attached value is
// wire-identical to what the server emits for the same struct.
func attachMergedConfigDocument(payload interface{}) error {
	doc, ok := payload.(map[string]interface{})
	if !ok || doc == nil {
		return nil
	}

	merged, err := config.LoadMergedRuntimeConfigDocument()
	if err != nil {
		return err
	}
	if merged == nil {
		return nil
	}

	raw, err := json.Marshal(merged)
	if err != nil {
		return err
	}

	var attached interface{}
	if err := json.Unmarshal(raw, &attached); err != nil {
		return err
	}

	doc[mergedConfigDocumentKey] = attached
	return nil
}
