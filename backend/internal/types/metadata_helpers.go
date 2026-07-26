package types

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	// ToolMetadataSupportsParallelKey marks whether a tool explicitly supports
	// parallel execution in its definition metadata.
	ToolMetadataSupportsParallelKey = "supports_parallel"
	// ToolMetadataRetryClassKey declares the side-effect retry contract exposed
	// to schedulers and approval policy.
	ToolMetadataRetryClassKey = "retry_class"

	// Tool taxonomy metadata keys (Iteration A permission/parallel productization).
	ToolMetadataKindKey        = "tool_kind"
	ToolMetadataReadOnlyKey    = "read_only"
	ToolMetadataMutatesFSKey   = "mutates_fs"
	ToolMetadataRequiresNetKey = "requires_net"

	ToolKindRead    = "read"
	ToolKindSearch  = "search"
	ToolKindEdit    = "edit"
	ToolKindExec    = "exec"
	ToolKindNetwork = "network"
	ToolKindControl = "control"

	ToolRetryClassNever                  = "never"
	ToolRetryClassSafe                   = "safe"
	ToolRetryClassIdempotencyKeyRequired = "idempotency_key_required"
	ToolRetryClassCompensatable          = "compensatable"
)

// BoolMetadataValue extracts a boolean metadata value from a generic tool
// metadata map. The second return value reports whether the key existed and
// could be parsed.
func BoolMetadataValue(metadata map[string]interface{}, key string) (bool, bool) {
	if len(metadata) == 0 {
		return false, false
	}
	raw, ok := metadata[key]
	if !ok || raw == nil {
		return false, false
	}
	switch typed := raw.(type) {
	case bool:
		return typed, true
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return false, false
		}
		value, err := strconv.ParseBool(trimmed)
		if err != nil {
			return false, false
		}
		return value, true
	case fmt.Stringer:
		trimmed := strings.TrimSpace(typed.String())
		if trimmed == "" {
			return false, false
		}
		value, err := strconv.ParseBool(trimmed)
		if err != nil {
			return false, false
		}
		return value, true
	default:
		trimmed := strings.ToLower(strings.TrimSpace(fmt.Sprint(raw)))
		if trimmed == "" {
			return false, false
		}
		value, err := strconv.ParseBool(trimmed)
		if err != nil {
			return false, false
		}
		return value, true
	}
}
