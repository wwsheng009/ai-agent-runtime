package llm

import "strings"

const (
	MetadataKeyInternalOperation = "internal_operation"
	MetadataKeyDisableTools      = "disable_tools"
	MetadataKeyDisableMetaTools  = "disable_meta_tools"
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
