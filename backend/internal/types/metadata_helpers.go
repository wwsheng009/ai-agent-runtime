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

	// ToolMetadataPathRolesKey declares the runtime-side filesystem role of
	// individual path arguments, e.g. {"file_path": "input", "shot.png":
	// "output"}. Roles: input | output | inout | workdir. Only "input" targets
	// must exist before execution; undeclared keys keep the legacy name-based
	// heuristic. This field is authored by tool registration on this host and is
	// never taken from a remote tool server's self-description.
	ToolMetadataPathRolesKey = "path_roles"
	// ToolMetadataFSOwnerKey declares who resolves a tool's filesystem paths:
	// runtime | tool_server | browser | opaque. Only owner=runtime paths are
	// joined to the session workspace and probed on this host; remote/opaque
	// owners (MCP tool servers, browser tools) are never stat'ed locally.
	// Missing metadata defaults to runtime; unknown values fall back to opaque.
	ToolMetadataFSOwnerKey = "fs_owner"

	ToolFSOwnerRuntime    = "runtime"
	ToolFSOwnerToolServer = "tool_server"
	ToolFSOwnerBrowser    = "browser"
	ToolFSOwnerOpaque     = "opaque"

	ToolPathRoleInput   = "input"
	ToolPathRoleOutput  = "output"
	ToolPathRoleInOut   = "inout"
	ToolPathRoleWorkdir = "workdir"
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

// StringMetadataValue extracts a trimmed string metadata value. The second
// return value reports whether the key existed as a non-empty string.
func StringMetadataValue(metadata map[string]interface{}, key string) (string, bool) {
	if len(metadata) == 0 {
		return "", false
	}
	raw, ok := metadata[key]
	if !ok {
		return "", false
	}
	text, ok := raw.(string)
	if !ok {
		return "", false
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", false
	}
	return text, true
}

// FSOwnerFromMetadata resolves the declared filesystem owner for a tool
// definition. Built-in tools default to runtime (the historical behavior);
// unknown or malformed owners fall back to opaque so a typo can never enable
// local path probing.
func FSOwnerFromMetadata(metadata map[string]interface{}) string {
	owner, ok := StringMetadataValue(metadata, ToolMetadataFSOwnerKey)
	if !ok {
		return ToolFSOwnerRuntime
	}
	switch strings.ToLower(owner) {
	case ToolFSOwnerRuntime:
		return ToolFSOwnerRuntime
	case ToolFSOwnerToolServer:
		return ToolFSOwnerToolServer
	case ToolFSOwnerBrowser:
		return ToolFSOwnerBrowser
	case ToolFSOwnerOpaque:
		return ToolFSOwnerOpaque
	default:
		return ToolFSOwnerOpaque
	}
}

// PathRoleFromMetadata resolves the declared role of one path argument.
// Matching is case-insensitive on the argument name. declared=false means the
// key was not listed in path_roles (callers keep the legacy name heuristic);
// declared=true with an empty role means the value was unrecognized, which
// callers must treat conservatively as a non-input target.
func PathRoleFromMetadata(metadata map[string]interface{}, argKey string) (role string, declared bool) {
	if len(metadata) == 0 {
		return "", false
	}
	raw, ok := metadata[ToolMetadataPathRolesKey]
	if !ok {
		return "", false
	}
	roles, ok := raw.(map[string]interface{})
	if !ok || len(roles) == 0 {
		return "", false
	}
	key := strings.ToLower(strings.TrimSpace(argKey))
	if key == "" {
		return "", false
	}
	for name, value := range roles {
		if strings.ToLower(strings.TrimSpace(name)) != key {
			continue
		}
		text, ok := value.(string)
		if !ok {
			return "", true
		}
		switch strings.ToLower(strings.TrimSpace(text)) {
		case ToolPathRoleInput:
			return ToolPathRoleInput, true
		case ToolPathRoleOutput:
			return ToolPathRoleOutput, true
		case ToolPathRoleInOut:
			return ToolPathRoleInOut, true
		case ToolPathRoleWorkdir:
			return ToolPathRoleWorkdir, true
		default:
			return "", true
		}
	}
	return "", false
}
