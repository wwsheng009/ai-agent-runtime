package toolbroker

import (
	"encoding/json"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

// UnmarshalJSON accepts both the documented snake_case field and the legacy
// camelCase alias for direct HTTP/API spawn-agent calls. Tool broker calls parse
// map arguments separately, but both paths share NormalizeOrdinarySpawnAgentArgs.
func (args *SpawnAgentArgs) UnmarshalJSON(data []byte) error {
	type spawnAgentArgsJSON SpawnAgentArgs
	var decoded spawnAgentArgsJSON
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	alias := struct {
		CompletionRequirementAlias string `json:"completionRequirement,omitempty"`
	}{}
	if err := json.Unmarshal(data, &alias); err != nil {
		return err
	}
	result := SpawnAgentArgs(decoded)
	if strings.TrimSpace(result.CompletionRequirement) == "" {
		result.CompletionRequirement = strings.TrimSpace(alias.CompletionRequirementAlias)
	}
	*args = result
	return nil
}

// NormalizeOrdinarySpawnAgentArgs applies the completion contract shared by the
// broker and direct local/API controllers. complete_task (including historical
// aliases and agentdef defaults) is rejected before any ordinary child session
// is created.
func NormalizeOrdinarySpawnAgentArgs(args SpawnAgentArgs) (SpawnAgentArgs, error) {
	if strings.TrimSpace(args.CompletionRequirement) == "" && strings.TrimSpace(args.AgentType) != "" {
		requirement, err := resolveSpawnAgentCompletionRequirement(args.AgentType)
		if err != nil {
			return args, err
		}
		args.CompletionRequirement = requirement
	}
	requirement, err := normalizeSpawnAgentCompletionRequirement(args.CompletionRequirement)
	if err != nil {
		return args, err
	}
	args.CompletionRequirement = requirement
	return args, nil
}

// SpawnAgentRunMetaFromContext constructs run metadata from the child session's
// own persisted route context. Spawn/follow-up/resume paths must not recover a
// parent Team worker completion contract from ambient context.
func SpawnAgentRunMetaFromContext(session agentcontrol.ContextGetter) *team.RunMeta {
	return SpawnAgentRunMeta(SpawnAgentArgs{
		PermissionMode:        agentcontrol.ContextString(session, AgentSessionContextPermissionMode),
		CompletionRequirement: agentcontrol.ContextString(session, AgentSessionContextCompletionRequirement),
	})
}
