package agent

import (
	"context"
	stderrors "errors"
	"strings"

	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	"github.com/wwsheng009/ai-agent-runtime/internal/output"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
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
			// Prefer tool-authored structured codes (e.g. STALE_CONTEXT from edit/
			// apply_patch) over generic classification of plain error strings.
			// Generic classification only fills gaps when tools return unstructured errors.
			if structuredErrorCodeFromMeta(toolMetadata) == "" {
				runtimeErr = classifyGenericToolExecutionError(execErr)
			}
		}
		if runtimeErr != nil {
			if toolMetadata == nil {
				toolMetadata = map[string]interface{}{}
			}
			// Never overwrite a more specific tool-authored code with a generic
			// TOOL_EXECUTION classification of the same plain error string.
			mergeRuntimeErrorMetadataPreferExisting(toolMetadata, runtimeErr)
			mergeRuntimeErrorMetadataPreferExisting(metadata, runtimeErr)
		}
	}
	if len(toolMetadata) > 0 && metadata != nil {
		metadata["tool_metadata"] = toolMetadata
		// Promote disposition fields so Diagnose / chat-log export / runtime events
		// see STALE_CONTEXT + next_action without depending on nested dig alone.
		promoteToolDispositionMetadata(metadata, toolMetadata)
	}
	if execErr != nil {
		result.Error = execErr.Error()
	}
}

// structuredErrorCodeFromMeta returns a known runtime error code authored by the
// tool (top-level or nested), or empty when absent/unknown.
func structuredErrorCodeFromMeta(meta map[string]interface{}) string {
	if len(meta) == 0 {
		return ""
	}
	if code, ok := meta["error_code"].(string); ok {
		code = strings.TrimSpace(code)
		if code != "" && knownToolOutcomeErrorCode(code) {
			return code
		}
	}
	return ""
}

func knownToolOutcomeErrorCode(code string) bool {
	switch runtimeerrors.ErrorCode(strings.TrimSpace(code)) {
	case runtimeerrors.ErrToolStaleContext,
		runtimeerrors.ErrToolShellCompat,
		runtimeerrors.ErrAgentSpawnDepthLimit,
		runtimeerrors.ErrToolPathNotFound,
		runtimeerrors.ErrToolInvalidArgs,
		runtimeerrors.ErrToolTimeout,
		runtimeerrors.ErrTurnDeadlineExceeded,
		runtimeerrors.ErrAgentRunCanceled,
		runtimeerrors.ErrAgentPermission,
		runtimeerrors.ErrJobNotFound,
		runtimeerrors.ErrProcessStartFailed,
		runtimeerrors.ErrProcessHealthcheck,
		runtimeerrors.ErrWritePrecondition,
		runtimeerrors.ErrToolExecution,
		runtimeerrors.ErrToolBrokerFailure,
		runtimeerrors.ErrNetworkUnavailable,
		runtimeerrors.ErrNetworkTimeout,
		runtimeerrors.ErrAPIRateLimit,
		runtimeerrors.ErrAPIServerError:
		return true
	default:
		return false
	}
}

// promoteToolDispositionMetadata copies tool-authored recovery fields to the
// top-level metadata map when missing, without overwriting existing values.
func promoteToolDispositionMetadata(dst, src map[string]interface{}) {
	if dst == nil || len(src) == 0 {
		return
	}
	for _, key := range []string{
		"error_code",
		"next_action",
		"retryable",
		"failure_class",
		"file_path",
		"suggested_view_offset",
		"suggested_view_limit",
		"current_snippet",
		"current_snippet_start_line",
		"path_auto_healed",
		"original_path",
		"resolved_path",
		toolresult.MetadataOutcomeKey,
		toolresult.MetadataEmptyResultKey,
		toolresult.MetadataPartialFailureKey,
		toolresult.MetadataFailedItemsKey,
		toolresult.MetadataAttemptedArgsKey,
		toolresult.MetadataPathCandidatesKey,
	} {
		if _, exists := dst[key]; exists {
			continue
		}
		if value, ok := src[key]; ok && value != nil {
			dst[key] = value
		}
	}
}

// mergeRuntimeErrorMetadataPreferExisting copies runtime error fields only when
// the destination does not already carry a non-empty value for that key.
func mergeRuntimeErrorMetadataPreferExisting(target map[string]interface{}, runtimeErr *runtimeerrors.RuntimeError) {
	if target == nil || runtimeErr == nil {
		return
	}
	if existing := structuredErrorCodeFromMeta(target); existing == "" {
		target["error_code"] = string(runtimeErr.Code)
	}
	if _, ok := target["error_message"]; !ok && runtimeErr.Message != "" {
		target["error_message"] = runtimeErr.Message
	}
	for key, value := range runtimeErr.GetContext() {
		if _, exists := target[key]; exists || value == nil {
			continue
		}
		target[key] = value
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
	case strings.Contains(err.Error(), "old_string 未在文件中找到"),
		strings.Contains(err.Error(), "无法定位 hunk"),
		strings.Contains(lower, "stale_context"),
		strings.Contains(lower, "stale old_string"),
		strings.Contains(lower, "stale @@"),
		(strings.Contains(lower, "old_string") && (strings.Contains(lower, "not found") || strings.Contains(lower, "not present"))):
		code = runtimeerrors.ErrToolStaleContext
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
