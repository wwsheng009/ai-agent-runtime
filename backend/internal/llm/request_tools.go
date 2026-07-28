package llm

import "strings"

const (
	MetadataKeyInternalOperation = "internal_operation"
	MetadataKeyDisableTools      = "disable_tools"
	MetadataKeyDisableMetaTools  = "disable_meta_tools"
	MetadataKeyDisableRetries    = "disable_retries"
	MetadataKeyParallelToolCalls = "parallel_tool_calls"
)

func metadataDisablesTools(metadata map[string]interface{}) bool {
	// Explicit disable_tools wins so compact can keep the chat tools prefix for
	// prompt-cache reuse while still opting out of tool execution via tool_choice.
	if raw, ok := metadata[MetadataKeyDisableTools]; ok {
		return metadataBool(raw)
	}
	return strings.EqualFold(strings.TrimSpace(stringValue(metadata[MetadataKeyInternalOperation])), "compact")
}

func metadataDisablesMetaTools(metadata map[string]interface{}) bool {
	if metadataDisablesTools(metadata) {
		return true
	}
	return metadataBool(metadata[MetadataKeyDisableMetaTools])
}

func metadataBool(value interface{}) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "1", "true", "yes", "on":
			return true
		default:
			return false
		}
	case int:
		return typed != 0
	case int32:
		return typed != 0
	case int64:
		return typed != 0
	case float32:
		return typed != 0
	case float64:
		return typed != 0
	default:
		return false
	}
}

// IsUnsupportedRequestParameter identifies provider validation errors for one
// optional request field so callers can downgrade without retrying the same body.
func IsUnsupportedRequestParameter(err error, parameter string) bool {
	if err == nil {
		return false
	}
	parameter = strings.ToLower(strings.TrimSpace(parameter))
	message := strings.ToLower(err.Error())
	if parameter == "" || !strings.Contains(message, parameter) {
		return false
	}
	for _, marker := range []string{
		"unsupported parameter", "unknown parameter", "unexpected parameter",
		"unrecognized request argument", "unknown field", "invalid parameter",
		"not supported", "does not support", "not allowed", "not permitted",
		"extra inputs are not permitted", "extra_forbidden",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}
