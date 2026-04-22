package toolbroker

import (
	"context"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

const activeTeamRunRequirementText = "Requires an active team run. If no team run exists yet, call spawn_team first and only use this tool after spawn_team succeeds."

// RequiresActiveTeamRun reports whether a broker tool depends on an existing
// active team run rather than creating one.
func RequiresActiveTeamRun(name string) bool {
	switch normalizeToolName(name) {
	case ToolSendTeamMessage, ToolReadMailboxDigest, ToolReadTaskSpec, ToolReadTaskContext, ToolReportTaskOutcome, ToolBlockCurrentTask:
		return true
	default:
		return false
	}
}

func describeRequiresActiveTeamRun(base string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		return activeTeamRunRequirementText
	}
	if strings.HasSuffix(base, ".") {
		return base + " " + activeTeamRunRequirementText
	}
	return base + ". " + activeTeamRunRequirementText
}

func metadataRequiresActiveTeamRun(existing map[string]interface{}) map[string]interface{} {
	merged := make(map[string]interface{}, len(existing)+2)
	for key, value := range existing {
		merged[key] = value
	}
	merged["availability"] = "requires_active_team_run"
	merged["defer_loading"] = true
	return merged
}

// DefinitionsForContext returns broker tools visible for the current request context.
// Team-only tools stay hidden until an active team run is bound into ctx.
func (b *Broker) DefinitionsForContext(ctx context.Context) []types.ToolDefinition {
	defs := b.Definitions()
	if len(defs) == 0 || hasActiveTeamRun(ctx) {
		return defs
	}
	filtered := make([]types.ToolDefinition, 0, len(defs))
	for _, def := range defs {
		if RequiresActiveTeamRun(def.Name) {
			continue
		}
		filtered = append(filtered, def)
	}
	return filtered
}

func hasActiveTeamRun(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	runMeta, ok := team.GetRunMeta(ctx)
	if !ok || runMeta == nil || runMeta.Team == nil {
		return false
	}
	return strings.TrimSpace(runMeta.Team.TeamID) != ""
}
