package toolresult

import (
	"strings"

	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
)

const (
	MetadataOKKey         = "ok"
	MetadataErrorCodeKey  = "error_code"
	MetadataRetryableKey  = "retryable"
	MetadataNextActionKey = "next_action"
	MetadataToolNameKey   = "tool_name"
	MetadataToolCallIDKey = "tool_call_id"
)

// Diagnostic is the stable execution contract attached to every tool result.
// OK describes the tool invocation itself; a successful status query may still
// report an underlying job error in its result payload and metadata.
type Diagnostic struct {
	OK         bool   `json:"ok"`
	ToolName   string `json:"tool_name,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	ErrorCode  string `json:"error_code,omitempty"`
	Retryable  bool   `json:"retryable,omitempty"`
	NextAction string `json:"next_action,omitempty"`
}

// Diagnose builds an actionable tool invocation diagnostic from the execution
// error and any structured metadata supplied by the tool runtime.
func Diagnose(toolName, toolCallID, toolErr string, metadata map[string]interface{}) Diagnostic {
	diagnostic := Diagnostic{
		OK:         strings.TrimSpace(toolErr) == "",
		ToolName:   strings.TrimSpace(toolName),
		ToolCallID: strings.TrimSpace(toolCallID),
	}
	if diagnostic.OK {
		return diagnostic
	}

	structuredCode := strings.TrimSpace(diagnosticString(metadata, MetadataErrorCodeKey))
	if knownRuntimeErrorCode(structuredCode) {
		diagnostic.ErrorCode = structuredCode
	} else {
		diagnostic.ErrorCode = classifyToolErrorCode(toolErr)
	}
	if retryable, ok := diagnosticBool(metadata, MetadataRetryableKey); ok {
		diagnostic.Retryable = retryable
	} else {
		diagnostic.Retryable = retryableToolErrorCode(diagnostic.ErrorCode)
	}
	diagnostic.NextAction = strings.TrimSpace(diagnosticTopLevelString(metadata, MetadataNextActionKey))
	if diagnostic.NextAction == "" {
		diagnostic.NextAction = nextActionForToolError(diagnostic.ErrorCode)
	}
	return diagnostic
}

// ApplyDiagnosticMetadata promotes the invocation diagnostic to the top-level
// envelope metadata consumed by events, observations, persistence, and UIs.
func ApplyDiagnosticMetadata(metadata map[string]interface{}, diagnostic Diagnostic) {
	if metadata == nil {
		return
	}
	metadata[MetadataOKKey] = diagnostic.OK
	if diagnostic.ToolName != "" {
		metadata[MetadataToolNameKey] = diagnostic.ToolName
	}
	if diagnostic.ToolCallID != "" {
		metadata[MetadataToolCallIDKey] = diagnostic.ToolCallID
	}
	if diagnostic.OK {
		return
	}
	metadata[MetadataErrorCodeKey] = diagnostic.ErrorCode
	metadata[MetadataRetryableKey] = diagnostic.Retryable
	metadata[MetadataNextActionKey] = diagnostic.NextAction
}

func diagnosticString(metadata map[string]interface{}, key string) string {
	if len(metadata) == 0 {
		return ""
	}
	if value, ok := metadata[key].(string); ok && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	if nested, ok := metadata["tool_metadata"].(map[string]interface{}); ok {
		if value, ok := nested[key].(string); ok {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func diagnosticTopLevelString(metadata map[string]interface{}, key string) string {
	if len(metadata) == 0 {
		return ""
	}
	value, _ := metadata[key].(string)
	return strings.TrimSpace(value)
}

func diagnosticBool(metadata map[string]interface{}, key string) (bool, bool) {
	if len(metadata) == 0 {
		return false, false
	}
	if value, ok := metadata[key].(bool); ok {
		return value, true
	}
	if nested, ok := metadata["tool_metadata"].(map[string]interface{}); ok {
		if value, ok := nested[key].(bool); ok {
			return value, true
		}
	}
	return false, false
}

func classifyToolErrorCode(message string) string {
	message = strings.TrimSpace(message)
	if code := bracketedRuntimeErrorCode(message); code != "" {
		return code
	}
	lower := strings.ToLower(message)
	switch {
	case strings.Contains(lower, "background job") && strings.Contains(lower, "not found"),
		strings.Contains(lower, "job_id") && strings.Contains(lower, "not found"):
		return string(runtimeerrors.ErrJobNotFound)
	case strings.Contains(lower, "tool not found"), strings.Contains(lower, "unknown tool"):
		return string(runtimeerrors.ErrToolNotFound)
	case strings.Contains(lower, "rate limit"), strings.Contains(lower, "http 429"),
		strings.Contains(lower, "status 429"):
		return string(runtimeerrors.ErrAPIRateLimit)
	case strings.Contains(lower, "unauthorized"), strings.Contains(lower, "forbidden"),
		strings.Contains(lower, "http 401"), strings.Contains(lower, "http 403"),
		strings.Contains(lower, "status 401"), strings.Contains(lower, "status 403"):
		return string(runtimeerrors.ErrAPIUnauthorized)
	case strings.Contains(lower, "connection refused"), strings.Contains(lower, "connection reset"),
		strings.Contains(lower, "network unavailable"), strings.Contains(lower, "no such host"):
		return string(runtimeerrors.ErrNetworkUnavailable)
	case strings.Contains(lower, "http 500"), strings.Contains(lower, "http 502"),
		strings.Contains(lower, "http 503"), strings.Contains(lower, "http 504"),
		strings.Contains(lower, "status 500"), strings.Contains(lower, "status 502"),
		strings.Contains(lower, "status 503"), strings.Contains(lower, "status 504"):
		return string(runtimeerrors.ErrAPIServerError)
	case strings.Contains(lower, "permission denied"), strings.Contains(lower, "access denied"),
		strings.Contains(lower, "not allowed"), strings.Contains(lower, "read-only"),
		strings.Contains(lower, "operation not permitted"), strings.Contains(lower, "denied by policy"),
		strings.Contains(lower, "hook blocked"):
		return string(runtimeerrors.ErrAgentPermission)
	case strings.Contains(lower, "path not found"), strings.Contains(lower, "file not found"),
		strings.Contains(lower, "no such file or directory"), strings.Contains(lower, "cannot find the path specified"),
		strings.Contains(lower, "cannot find the file specified"):
		return string(runtimeerrors.ErrToolPathNotFound)
	case strings.Contains(lower, "approval") && strings.Contains(lower, "expired"):
		return string(runtimeerrors.ErrApprovalExpired)
	case strings.Contains(lower, "deadline exceeded"), strings.Contains(lower, "timed out"),
		strings.Contains(lower, "timeout"):
		return string(runtimeerrors.ErrToolTimeout)
	case strings.Contains(lower, "context canceled"), strings.Contains(lower, "context cancelled"),
		strings.Contains(lower, "run canceled"), strings.Contains(lower, "run cancelled"):
		return string(runtimeerrors.ErrAgentRunCanceled)
	case strings.Contains(lower, "invalid argument"), strings.Contains(lower, "invalid args"),
		strings.Contains(lower, "missing required"), strings.Contains(lower, " is required"),
		strings.Contains(lower, "cannot unmarshal"), strings.Contains(lower, "unexpected end of json"),
		strings.Contains(lower, "failed to parse arguments"), strings.Contains(lower, "unknown field"):
		return string(runtimeerrors.ErrToolInvalidArgs)
	default:
		return string(runtimeerrors.ErrToolExecution)
	}
}

func bracketedRuntimeErrorCode(message string) string {
	for start := strings.Index(message, "["); start >= 0; {
		remainder := message[start+1:]
		end := strings.Index(remainder, "]")
		if end < 0 {
			return ""
		}
		candidate := strings.TrimSpace(remainder[:end])
		if knownRuntimeErrorCode(candidate) {
			return candidate
		}
		next := start + end + 2
		if next >= len(message) {
			return ""
		}
		following := strings.Index(message[next:], "[")
		if following < 0 {
			return ""
		}
		start = next + following
	}
	return ""
}

func knownRuntimeErrorCode(code string) bool {
	switch runtimeerrors.ErrorCode(strings.TrimSpace(code)) {
	case runtimeerrors.ErrNetworkTimeout, runtimeerrors.ErrNetworkUnavailable,
		runtimeerrors.ErrAPIRateLimit, runtimeerrors.ErrAPIUnauthorized, runtimeerrors.ErrAPINotFound,
		runtimeerrors.ErrAPIBadRequest, runtimeerrors.ErrAPIServerError,
		runtimeerrors.ErrToolNotFound, runtimeerrors.ErrToolExecution, runtimeerrors.ErrToolTimeout,
		runtimeerrors.ErrWritePrecondition, runtimeerrors.ErrJobNotFound, runtimeerrors.ErrTurnDeadlineExceeded,
		runtimeerrors.ErrAgentRunCanceled, runtimeerrors.ErrApprovalExpired, runtimeerrors.ErrSessionLeaseConflict,
		runtimeerrors.ErrToolInvalidArgs, runtimeerrors.ErrToolPathNotFound, runtimeerrors.ErrToolBrokerFailure,
		runtimeerrors.ErrProcessStartFailed, runtimeerrors.ErrProcessHealthcheck,
		runtimeerrors.ErrAgentMaxSteps, runtimeerrors.ErrAgentPermission, runtimeerrors.ErrContextBudget,
		runtimeerrors.ErrStreamInterrupted, runtimeerrors.ErrUpstreamUnavailable,
		runtimeerrors.ErrMemoryFull, runtimeerrors.ErrWorkflowCycle, runtimeerrors.ErrWorkflowStep,
		runtimeerrors.ErrSkillNotFound, runtimeerrors.ErrSkillLoadFailed, runtimeerrors.ErrInvalidManifest,
		runtimeerrors.ErrToolNotRegistered, runtimeerrors.ErrValidationFailed,
		runtimeerrors.ErrConfigNotFound, runtimeerrors.ErrConfigInvalid:
		return true
	default:
		return false
	}
}

func retryableToolErrorCode(code string) bool {
	switch runtimeerrors.ErrorCode(strings.TrimSpace(code)) {
	case runtimeerrors.ErrNetworkTimeout, runtimeerrors.ErrNetworkUnavailable,
		runtimeerrors.ErrAPIRateLimit, runtimeerrors.ErrAPIServerError,
		runtimeerrors.ErrToolTimeout, runtimeerrors.ErrTurnDeadlineExceeded,
		runtimeerrors.ErrSessionLeaseConflict, runtimeerrors.ErrProcessStartFailed,
		runtimeerrors.ErrStreamInterrupted, runtimeerrors.ErrUpstreamUnavailable:
		return true
	default:
		return false
	}
}

func nextActionForToolError(code string) string {
	switch runtimeerrors.ErrorCode(strings.TrimSpace(code)) {
	case runtimeerrors.ErrToolInvalidArgs, runtimeerrors.ErrAPIBadRequest,
		runtimeerrors.ErrValidationFailed, runtimeerrors.ErrConfigInvalid:
		return "Correct the tool arguments using the current schema, then call it again."
	case runtimeerrors.ErrJobNotFound:
		return "Use the exact job_id returned by background_task; do not guess or synthesize an id."
	case runtimeerrors.ErrToolNotFound, runtimeerrors.ErrToolNotRegistered:
		return "Choose a tool name from the current tool definitions; do not retry the unavailable name."
	case runtimeerrors.ErrToolPathNotFound:
		return "Verify the path and working directory, correct them, then call the tool again."
	case runtimeerrors.ErrAgentPermission, runtimeerrors.ErrAPIUnauthorized, runtimeerrors.ErrApprovalExpired:
		return "Request the required approval or use an allowed tool; do not retry unchanged."
	case runtimeerrors.ErrToolTimeout, runtimeerrors.ErrTurnDeadlineExceeded:
		return "Check whether the operation completed before a bounded retry to avoid duplicate side effects."
	case runtimeerrors.ErrNetworkTimeout, runtimeerrors.ErrNetworkUnavailable,
		runtimeerrors.ErrAPIRateLimit, runtimeerrors.ErrAPIServerError,
		runtimeerrors.ErrSessionLeaseConflict, runtimeerrors.ErrStreamInterrupted,
		runtimeerrors.ErrUpstreamUnavailable:
		return "Retry with bounded backoff; stop after repeated failure and report the blocker."
	case runtimeerrors.ErrProcessStartFailed, runtimeerrors.ErrProcessHealthcheck:
		return "Inspect launch and health-check details, correct the cause, then retry only if side effects are safe."
	case runtimeerrors.ErrWritePrecondition:
		return "Re-read the target state and rebuild the mutation from the latest content."
	case runtimeerrors.ErrContextBudget:
		return "Reduce or compact the input and tool output before continuing."
	case runtimeerrors.ErrAgentRunCanceled:
		return "Do not retry automatically; start a new run only when continuation is still required."
	default:
		return "Inspect the error details, correct the cause, and retry only when the operation is safe."
	}
}
