package agent

import (
	"context"
	stderrors "errors"
	"strings"

	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	"github.com/wwsheng009/ai-agent-runtime/internal/output"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
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
		if !stderrors.As(execErr, &runtimeErr) {
			runtimeErr = classifyGenericToolExecutionError(execErr)
		}
		if runtimeErr != nil {
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

func classifyGenericToolExecutionError(err error) *runtimeerrors.RuntimeError {
	if err == nil {
		return nil
	}
	lower := strings.ToLower(strings.TrimSpace(err.Error()))
	code := runtimeerrors.ErrToolExecution
	switch {
	case stderrors.Is(err, context.Canceled):
		code = runtimeerrors.ErrAgentRunCanceled
	case stderrors.Is(err, context.DeadlineExceeded), strings.Contains(lower, "timed out"), strings.Contains(lower, "timeout"):
		code = runtimeerrors.ErrToolTimeout
	case strings.Contains(lower, "permission denied"), strings.Contains(lower, "access denied"):
		code = runtimeerrors.ErrAgentPermission
	case strings.Contains(lower, "path not found"), strings.Contains(lower, "file not found"), strings.Contains(lower, "no such file or directory"):
		code = runtimeerrors.ErrToolPathNotFound
	case strings.Contains(lower, "invalid argument"), strings.Contains(lower, "invalid args"), strings.Contains(lower, "missing required"), strings.Contains(lower, " is required"):
		code = runtimeerrors.ErrToolInvalidArgs
	case strings.Contains(lower, "spawn depth limit"),
		strings.Contains(lower, "spawn_depth"),
		strings.Contains(err.Error(), "SPAWN_DEPTH_LIMIT"),
		(strings.Contains(lower, "max_depth") && strings.Contains(lower, "spawn")),
		(strings.Contains(lower, "depth limit") && strings.Contains(lower, "spawn")):
		code = runtimeerrors.ErrAgentSpawnDepthLimit
	}
	return runtimeerrors.Wrap(code, "tool execution failed", err)
}

func recordToolFailureMetadata(metadata map[string]interface{}, code runtimeerrors.ErrorCode, message string) {
	if metadata == nil || code == "" {
		return
	}
	metadata["error_code"] = string(code)
	if message = strings.TrimSpace(message); message != "" {
		metadata["error_message"] = message
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

// newRawToolResult builds a gateway input that carries call args so recovery
// contracts can compact-inject attempted_args without tool-name branches.
func newRawToolResult(sessionID string, call types.ToolCall, step int, content interface{}, toolErr string, metadata map[string]interface{}) output.RawToolResult {
	return output.RawToolResult{
		SessionID:  sessionID,
		ToolName:   call.Name,
		ToolCallID: call.ID,
		Step:       step,
		Content:    content,
		Error:      toolErr,
		Metadata:   metadata,
		Args:       call.Args,
	}
}
