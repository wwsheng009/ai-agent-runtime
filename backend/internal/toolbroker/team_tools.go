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
// Team-only tools stay hidden until an active team run is bound into ctx. The two
// completion outcome tools are also exposed to a lightweight spawn_agent run
// carrying completion_requirement=complete_task: that contract must never ask a
// worker to call a tool which was removed from its tool surface.
func (b *Broker) DefinitionsForContext(ctx context.Context) []types.ToolDefinition {
	defs := b.Definitions()
	if len(defs) == 0 || hasActiveTeamRun(ctx) {
		return defs
	}
	filtered := make([]types.ToolDefinition, 0, len(defs))
	for _, def := range defs {
		if RequiresActiveTeamRun(def.Name) {
			if !allowsStandaloneCompletionOutcome(ctx, def.Name) {
				continue
			}
			def = standaloneCompletionOutcomeDefinition(def)
		}
		filtered = append(filtered, def)
	}
	return filtered
}

func standaloneCompletionOutcomeDefinition(def types.ToolDefinition) types.ToolDefinition {
	metadata := make(map[string]interface{}, len(def.Metadata)+2)
	for key, value := range def.Metadata {
		metadata[key] = value
	}
	metadata["availability"] = "completion_requirement"
	metadata["completion_scope"] = "agent_session"
	metadata["defer_loading"] = false
	def.Metadata = metadata
	switch normalizeToolName(def.Name) {
	case ToolReportTaskOutcome:
		def.Description = "Report the structured done, failed, blocked, or handoff outcome required by this standalone agent session. No team_id or task_id is needed."
	case ToolBlockCurrentTask:
		def.Description = "Compatibility alias for reporting a blocked or handoff outcome required by this standalone agent session."
	}
	return def
}

func allowsStandaloneCompletionOutcome(ctx context.Context, name string) bool {
	switch normalizeToolName(name) {
	case ToolReportTaskOutcome, ToolBlockCurrentTask:
	default:
		return false
	}
	if ctx == nil {
		return false
	}
	runMeta, ok := team.GetRunMeta(ctx)
	if !ok || runMeta == nil || (runMeta.Team != nil && strings.TrimSpace(runMeta.Team.TeamID) != "") {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(runMeta.CompletionRequirement)) {
	case "complete_task", "complete-task", "completetask":
		return true
	default:
		return false
	}
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
