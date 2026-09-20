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
	// ToolMetadataEmptyReplayCacheKey controls whether successful empty results
	// may open the run-scoped same-arguments negative cache. It defaults to true;
	// volatile polling/state tools should explicitly set it to false.
	ToolMetadataEmptyReplayCacheKey = "empty_replay_cache"
	// ToolMetadataPathPreflightKey is the per-tool opt-in/opt-out for the generic
	// read-path existence preflight. Tools whose path arguments are write targets
	// that legitimately do not exist yet (e.g. enter_plan_mode's plan_path) must
	// set it to false so the preflight cannot deny a valid call.
	ToolMetadataPathPreflightKey = "path_preflight"

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
// ToolMetadataSkipRenderTruncationKey is the tool-authoring surface for the
// render-layer (L4) truncation opt-out.
//
// Set it to true on a tool result's metadata (or inside "tool_metadata") when
// the tool truncates its own output and therefore manages its own paging window
// (offset/limit/eof/artifact_id). The render layer then treats the body as
// final and does not fold it a second time.
const ToolMetadataSkipRenderTruncationKey = "skip_render_truncation"

// SkipRenderTruncationMetadata returns the metadata fragment a tool embeds into
// its ToolResult to declare that it manages its own truncation and must be
// exempt from render-layer (L4) folding.
func SkipRenderTruncationMetadata() map[string]interface{} {
	return map[string]interface{}{ToolMetadataSkipRenderTruncationKey: true}
}

// ToolManagesOwnTruncation reports whether metadata declares the render-layer
// truncation opt-out, either flat or nested inside "tool_metadata".
func ToolManagesOwnTruncation(metadata map[string]interface{}) bool {
	if len(metadata) == 0 {
		return false
	}
	if flag, ok := BoolMetadataValue(metadata, ToolMetadataSkipRenderTruncationKey); ok {
		return flag
	}
	if nested, ok := metadata["tool_metadata"].(map[string]interface{}); ok {
		if flag, ok := BoolMetadataValue(nested, ToolMetadataSkipRenderTruncationKey); ok {
			return flag
		}
	}
	return false
}

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
