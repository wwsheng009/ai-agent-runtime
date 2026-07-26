package toolprotocol

import (
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

// ErrorCode is a stable, vendor-neutral tool failure code on the wire.
type ErrorCode string

// Common wire error codes (aligned with runtimeerrors / toolresult classification).
const (
	ErrorCodeInvalidArgs   ErrorCode = "tool_invalid_args"
	ErrorCodeTimeout       ErrorCode = "tool_timeout"
	ErrorCodePermission    ErrorCode = "agent_permission"
	ErrorCodePathNotFound  ErrorCode = "tool_path_not_found"
	ErrorCodeExecution     ErrorCode = "tool_execution"
	ErrorCodeCanceled      ErrorCode = "agent_run_canceled"
	ErrorCodeStaleContext  ErrorCode = "tool_stale_context"
	ErrorCodeSpawnDepth    ErrorCode = "agent_spawn_depth_limit"
	ErrorCodeUnknown       ErrorCode = "tool_unknown"
)

// Error is the portable error wire object for tool results and notifications.
type Error struct {
	Code       ErrorCode              `json:"code,omitempty"`
	Message    string                 `json:"message,omitempty"`
	Retryable  bool                   `json:"retryable,omitempty"`
	NextAction string                 `json:"next_action,omitempty"`
	Data       map[string]interface{} `json:"data,omitempty"`
}

// NormalizeErrorCode trims and lowercases a code; unknown non-empty values are kept.
func NormalizeErrorCode(value string) ErrorCode {
	trimmed := strings.ToLower(strings.TrimSpace(value))
	if trimmed == "" {
		return ""
	}
	return ErrorCode(trimmed)
}

// ErrorFromDiagnostic builds a wire Error from toolresult.Diagnostic + message.
func ErrorFromDiagnostic(diag toolresult.Diagnostic, message string) *Error {
	if diag.OK && strings.TrimSpace(message) == "" {
		return nil
	}
	code := NormalizeErrorCode(diag.ErrorCode)
	if code == "" && strings.TrimSpace(message) != "" {
		code = ErrorCodeExecution
	}
	if code == "" && !diag.OK {
		code = ErrorCodeUnknown
	}
	if code == "" {
		return nil
	}
	err := &Error{
		Code:       code,
		Message:    strings.TrimSpace(message),
		Retryable:  diag.Retryable,
		NextAction: strings.TrimSpace(diag.NextAction),
	}
	if err.Message == "" && !diag.OK {
		err.Message = "tool execution failed"
	}
	return err
}
