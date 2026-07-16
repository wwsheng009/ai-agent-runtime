package llm

import "strings"

const (
	MetadataKeyInternalOperation = "internal_operation"
	MetadataKeyDisableTools      = "disable_tools"
	MetadataKeyDisableMetaTools  = "disable_meta_tools"
	MetadataKeyParallelToolCalls = "parallel_tool_calls"
)

func metadataDisablesTools(metadata map[string]interface{}) bool {
	if metadataBool(metadata[MetadataKeyDisableTools]) {
		return true
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
		"unrecognized request argument", "not supported", "not allowed",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}
