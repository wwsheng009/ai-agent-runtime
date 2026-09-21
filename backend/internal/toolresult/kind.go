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

// MetadataSkipRenderTruncationKey is the declared opt-out for render-layer (L4)
// truncation management.
//
// A tool that sets this key to true on its result metadata states that it
// already folded its own payload, published its own continuation metadata
// (offset/limit/eof/artifact_id) and owns the final shape of the body. The
// render layer must therefore leave the body untouched: folding it again would
// charge the byte budget twice, emit a duplicate "middle omitted" marker and
// contradict the tool's own continuation notice.
//
// The flag may be set either flat on the result metadata or nested inside
// "tool_metadata".
const MetadataSkipRenderTruncationKey = "skip_render_truncation"

// SkipsRenderTruncation reports whether metadata declares that the producing
// tool manages truncation itself, so the render layer must not fold the body
// again. Both a flat key and the nested tool_metadata map are honored.
func SkipsRenderTruncation(metadata map[string]interface{}) bool {
	if len(metadata) == 0 {
		return false
	}
	if truthyMetadataFlag(metadata, MetadataSkipRenderTruncationKey) {
		return true
	}
	if nested, ok := metadata["tool_metadata"].(map[string]interface{}); ok {
		return truthyMetadataFlag(nested, MetadataSkipRenderTruncationKey)
	}
	return false
}

// MetadataModelVisibleBudgetKey lets a tool declare the model-visible byte
// budget for its own payload WITHOUT folding it.
//
// This is the middle ground between "render layer owns the budget" (no key: the
// body is folded at the global model-visible budget) and
// MetadataSkipRenderTruncationKey ("the tool already folded its own body").
//
// A tool that sets this key keeps its payload intact - so the archive holds the
// full output and artifact_read can page through it - while the render layer
// folds head-only at the declared budget and appends the raw-output pointer for
// the omitted tail. Shell output uses this: the capture limit bounds memory
// (256 KiB), but the model window is the shell's own budget, not the global
// backstop.
//
// The key may be set flat on the result metadata or nested inside
// "tool_metadata".
const MetadataModelVisibleBudgetKey = "model_visible_budget_bytes"

// ModelVisibleBudgetBytes returns the declared model-visible byte budget, or 0
// when the tool did not declare one. Both a flat key and the nested
// tool_metadata map are honored.
func ModelVisibleBudgetBytes(metadata map[string]interface{}) int {
	if len(metadata) == 0 {
		return 0
	}
	if budget := metadataIntValue(metadata[MetadataModelVisibleBudgetKey]); budget > 0 {
		return budget
	}
	if nested, ok := metadata["tool_metadata"].(map[string]interface{}); ok {
		if budget := metadataIntValue(nested[MetadataModelVisibleBudgetKey]); budget > 0 {
			return budget
		}
	}
	return 0
}

func metadataIntValue(value interface{}) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int32:
		return int(typed)
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	default:
		return 0
	}
}

func truthyMetadataFlag(metadata map[string]interface{}, key string) bool {
	if metadata == nil {
		return false
	}
	switch value := metadata[key].(type) {
	case bool:
		return value
	case string:
		return value == "1" || value == "true" || value == "yes" || value == "on"
	case int:
		return value != 0
	case float64:
		return value != 0
	default:
		return false
	}
}
