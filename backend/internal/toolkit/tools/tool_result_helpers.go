package tools

import (
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

// toolResultFailure builds a failed ToolResult with optional structured next_action.
func toolResultFailure(err error, nextAction string) *toolkit.ToolResult {
	result := &toolkit.ToolResult{
		Success:    false,
		OutputKind: toolresult.KindText,
		Error:      err,
	}
	if nextAction != "" {
		result.Metadata = map[string]interface{}{
			toolresult.MetadataNextActionKey: nextAction,
		}
	}
	return result
}
