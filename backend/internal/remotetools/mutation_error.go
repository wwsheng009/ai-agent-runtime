package remotetools

import (
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/mutatecore"
)

// remoteMutationError adapts a mutatecore failure onto the remote tool error
// shape without parsing prose. The stable code and next_action are preserved so
// local and remote surfaces expose the same recovery contract.
func remoteMutationError(err error) *ToolError {
	var toolErr *ToolError
	if asToolError(err, &toolErr) {
		return toolErr
	}
	code := mutatecore.CodeOf(err)
	if code == "" {
		code = CodeInternal
	}
	nextAction := mutatecore.NextActionOf(err)
	if nextAction == "" {
		nextAction = "re-read the target with view, then retry the mutation with current content"
	}
	return &ToolError{
		Code:       code,
		Message:    err.Error(),
		NextAction: nextAction,
		Retriable:  false,
	}
}
