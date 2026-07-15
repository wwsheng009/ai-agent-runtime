package agent

import (
	stderrors "errors"

	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
)

// recordToolExecutionOutcome preserves tool output/metadata even when the tool
// also returns an execution error. This keeps stderr-like diagnostic text
// available to runtime events, chat history, and model-visible tool messages.
func recordToolExecutionOutcome(result *toolExecutionResult, metadata map[string]interface{}, rawOutput interface{}, rawMeta map[string]interface{}, execErr error) {
	if result == nil {
		return
	}
	result.Output = rawOutput
	toolMetadata := cloneInterfaceMap(rawMeta)
	if execErr != nil {
		var runtimeErr *runtimeerrors.RuntimeError
		if stderrors.As(execErr, &runtimeErr) {
			if toolMetadata == nil {
				toolMetadata = map[string]interface{}{}
			}
			copyRuntimeErrorMetadata(toolMetadata, runtimeErr)
			copyRuntimeErrorMetadata(metadata, runtimeErr)
		}
	}
	if len(toolMetadata) > 0 && metadata != nil {
		metadata["tool_metadata"] = toolMetadata
	}
	if execErr != nil {
		result.Error = execErr.Error()
	}
}

func copyRuntimeErrorMetadata(target map[string]interface{}, runtimeErr *runtimeerrors.RuntimeError) {
	if target == nil || runtimeErr == nil {
		return
	}
	target["error_code"] = string(runtimeErr.Code)
	if runtimeErr.Message != "" {
		target["error_message"] = runtimeErr.Message
	}
	for key, value := range runtimeErr.GetContext() {
		target[key] = value
	}
}
