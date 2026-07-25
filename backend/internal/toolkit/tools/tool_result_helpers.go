package tools

import (
	"strings"

	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

// toolResultFailure builds a failed ToolResult with optional structured next_action.
func toolResultFailure(err error, nextAction string) *toolkit.ToolResult {
	return toolResultFailureWithCode(err, "", nextAction, nil)
}

// toolResultFailureWithCode builds a failed ToolResult with optional error_code,
// next_action, and extra metadata fields (file_path, failure_class, etc.).
func toolResultFailureWithCode(err error, errorCode, nextAction string, extra map[string]interface{}) *toolkit.ToolResult {
	result := &toolkit.ToolResult{
		Success:    false,
		OutputKind: toolresult.KindText,
		Error:      err,
	}
	metadata := map[string]interface{}{}
	if code := strings.TrimSpace(errorCode); code != "" {
		metadata[toolresult.MetadataErrorCodeKey] = code
		// Stale edit/patch context, spawn depth limits, and shell dialect preflight
		// failures are never safe to blind-retry with the same payload.
		switch runtimeerrors.ErrorCode(code) {
		case runtimeerrors.ErrToolStaleContext, runtimeerrors.ErrAgentSpawnDepthLimit, runtimeerrors.ErrToolShellCompat:
			metadata[toolresult.MetadataRetryableKey] = false
		}
	}
	if next := strings.TrimSpace(nextAction); next != "" {
		metadata[toolresult.MetadataNextActionKey] = next
	}
	for key, value := range extra {
		if strings.TrimSpace(key) == "" || value == nil {
			continue
		}
		metadata[key] = value
	}
	if len(metadata) > 0 {
		result.Metadata = metadata
	}
	return result
}
