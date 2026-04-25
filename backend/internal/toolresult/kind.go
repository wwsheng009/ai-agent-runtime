package toolresult

import "strings"

const MetadataKey = "output_kind"
const SourceKey = "tool_source"

const (
	KindText       = "text"
	KindStructured = "structured"
	KindBinary     = "binary"
	KindEmpty      = "empty"
)

const (
	SourceMeta    = "meta"
	SourceToolkit = "toolkit"
	SourceMCP     = "mcp"
	SourceBroker  = "broker"
)

func NormalizeKind(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case KindText:
		return KindText
	case KindStructured:
		return KindStructured
	case KindBinary:
		return KindBinary
	case KindEmpty:
		return KindEmpty
	default:
		return ""
	}
}

func KindFromMetadata(metadata map[string]interface{}) string {
	if len(metadata) == 0 {
		return ""
	}
	if value, ok := metadata[MetadataKey].(string); ok {
		if kind := NormalizeKind(value); kind != "" {
			return kind
		}
	}
	if raw, ok := metadata["tool_metadata"].(map[string]interface{}); ok {
		if value, ok := raw[MetadataKey].(string); ok {
			return NormalizeKind(value)
		}
	}
	return ""
}

func WithKind(metadata map[string]interface{}, kind string) map[string]interface{} {
	kind = NormalizeKind(kind)
	if kind == "" {
		return cloneMap(metadata)
	}
	cloned := cloneMap(metadata)
	if cloned == nil {
		cloned = map[string]interface{}{}
	}
	cloned[MetadataKey] = kind
	return cloned
}

func NormalizeSource(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case SourceMeta:
		return SourceMeta
	case SourceToolkit:
		return SourceToolkit
	case SourceMCP:
		return SourceMCP
	case SourceBroker:
		return SourceBroker
	default:
		return ""
	}
}

func SourceFromMetadata(metadata map[string]interface{}) string {
	if len(metadata) == 0 {
		return ""
	}
	if value, ok := metadata[SourceKey].(string); ok {
		if source := NormalizeSource(value); source != "" {
			return source
		}
	}
	if raw, ok := metadata["tool_metadata"].(map[string]interface{}); ok {
		if value, ok := raw[SourceKey].(string); ok {
			return NormalizeSource(value)
		}
	}
	return ""
}

func WithSource(metadata map[string]interface{}, source string) map[string]interface{} {
	source = NormalizeSource(source)
	if source == "" {
		return cloneMap(metadata)
	}
	cloned := cloneMap(metadata)
	if cloned == nil {
		cloned = map[string]interface{}{}
	}
	cloned[SourceKey] = source
	return cloned
}

func cloneMap(input map[string]interface{}) map[string]interface{} {
	if len(input) == 0 {
		return nil
	}
	cloned := make(map[string]interface{}, len(input))
	for key, value := range input {
		cloned[key] = value
	}
	return cloned
}
