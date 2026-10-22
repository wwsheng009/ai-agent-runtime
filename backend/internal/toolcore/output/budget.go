// Package output owns the model-visible output contract shared by every tool
// surface. It is deliberately independent of local sandbox/session packages so
// the same shaping code can run in the builtin agent and a remote server.
package output

import (
	"encoding/json"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

const (
	// DefaultModelToolTextBudget is the backstop for tools with no declared
	// budget. Keep it aligned with internal/output's default.
	DefaultModelToolTextBudget = 12 * 1024
	// BudgetCeiling is the absolute model-visible ceiling.
	BudgetCeiling = 64 * 1024
	// DefaultRemoteOutputHardCap is the default trust-boundary cap.
	DefaultRemoteOutputHardCap = 64 * 1024
	// MaxMetadataBytes bounds a remote Meta object before parsing.
	MaxMetadataBytes = 64 * 1024

	ViewOutputBudgetBytes         = 32 * 1024
	ViewBatchAggregateBudgetBytes = 64 * 1024
	GrepOutputBudgetBytes         = 32 * 1024
	GlobOutputBudgetBytes         = 16 * 1024
	LsOutputBudgetBytes           = 16 * 1024
	ShellOutputBudgetBytes        = 32 * 1024
	ArtifactOutputBudgetBytes     = 32 * 1024
)

// Metadata keys are centralized here; toolresult owns the existing wire names.
const (
	KeySkipRenderTruncation = toolresult.MetadataSkipRenderTruncationKey
	KeyModelVisibleBudget   = toolresult.MetadataModelVisibleBudgetKey
	KeyReservedTailBytes    = toolresult.MetadataReservedTailBytesKey
	KeyIsTruncated          = "is_truncated"
	KeySuggestedNextOffset  = "suggested_next_offset"
	KeyArtifactRef          = "artifact_ref"
	KeyArtifactID           = "artifact_id"
	KeyOutputKind           = "output_kind"
	KeyErrorCode            = "error_code"
	KeyNextAction           = "next_action"
	KeyRetriable            = "retriable"
	KeyTruncated            = "truncated"
	KeyToolMetadata         = "tool_metadata"
	KeyMetadataRejected     = "remote_metadata_rejected"
)

// ToolBudget returns the declared model-visible window for a tool.
func ToolBudget(tool string) int {
	switch strings.ToLower(strings.TrimSpace(tool)) {
	case "view", "read":
		return ViewOutputBudgetBytes
	case "view_batch":
		return ViewBatchAggregateBudgetBytes
	case "grep", "rg":
		return GrepOutputBudgetBytes
	case "glob":
		return GlobOutputBudgetBytes
	case "ls":
		return LsOutputBudgetBytes
	case "shell", "bash", "execute_shell_command":
		return ShellOutputBudgetBytes
	case "artifact_read":
		return ArtifactOutputBudgetBytes
	default:
		return DefaultModelToolTextBudget
	}
}

// ResolveCap implements the trust-boundary arithmetic from design §9.6.
//
// hardCap = min(toolWindow, remoteHardCap)
// cap     = declared > 0 ? min(declared, hardCap) : hardCap
//
// A remote declaration can only narrow the local window; it can never widen it.
func ResolveCap(tool string, declared, toolWindow, remoteHardCap int) int {
	if toolWindow <= 0 {
		toolWindow = ToolBudget(tool)
	}
	if toolWindow <= 0 {
		toolWindow = DefaultModelToolTextBudget
	}
	if remoteHardCap <= 0 || remoteHardCap > BudgetCeiling {
		remoteHardCap = DefaultRemoteOutputHardCap
	}
	hardCap := toolWindow
	if hardCap > remoteHardCap {
		hardCap = remoteHardCap
	}
	if hardCap > BudgetCeiling {
		hardCap = BudgetCeiling
	}
	if declared > 0 && declared < hardCap {
		return declared
	}
	return hardCap
}

// HardCap returns the non-negotiable local ceiling for a tool.
func HardCap(tool string, toolWindow, remoteHardCap int) int {
	if toolWindow <= 0 {
		toolWindow = ToolBudget(tool)
	}
	if remoteHardCap <= 0 || remoteHardCap > BudgetCeiling {
		remoteHardCap = DefaultRemoteOutputHardCap
	}
	if toolWindow > remoteHardCap {
		return remoteHardCap
	}
	if toolWindow > BudgetCeiling {
		return BudgetCeiling
	}
	return toolWindow
}

// MetadataSize returns the serialized size of metadata, or 0 on failure.
func MetadataSize(metadata map[string]any) int {
	if len(metadata) == 0 {
		return 0
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return 0
	}
	return len(encoded)
}
