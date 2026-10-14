package toolbroker

import (
	"sort"
	"strings"
)

// MergeAgentStatusResults merges two agent-status views into one deduplicated,
// stably sorted list. The primary view wins on identity collisions
// (session_id / id / path, case-insensitive); extra rows fill the remaining
// slots. This is the single merge口径 shared by the local chat host and the
// HTTP host so "which rows does an agent list show" cannot drift between them.
func MergeAgentStatusResults(primary, extra []AgentStatusResult) []AgentStatusResult {
	if len(extra) == 0 {
		return primary
	}
	key := func(agent AgentStatusResult) string {
		return strings.ToLower(firstNonEmptyToolValue(agent.SessionID, agent.ID, agent.Path))
	}
	seen := make(map[string]struct{}, len(primary)+len(extra))
	merged := make([]AgentStatusResult, 0, len(primary)+len(extra))
	for _, agent := range primary {
		if k := key(agent); k != "" {
			seen[k] = struct{}{}
		}
		merged = append(merged, agent)
	}
	for _, agent := range extra {
		if k := key(agent); k != "" {
			if _, exists := seen[k]; exists {
				continue
			}
			seen[k] = struct{}{}
		}
		merged = append(merged, agent)
	}
	sort.SliceStable(merged, func(i, j int) bool {
		left := firstNonEmptyToolValue(merged[i].Path, merged[i].SessionID, merged[i].ID)
		right := firstNonEmptyToolValue(merged[j].Path, merged[j].SessionID, merged[j].ID)
		return left < right
	})
	return merged
}

// PathPrefixCoversWholeAgentTree reports whether a subagent_list path_prefix
// filter selects the caller's whole tree ("" or "/root"). Batch-task ledger
// rows carry no agent path — they are only included when the filter does not
// narrow to a subtree, because a synthesized path would feed fake positions to
// path-based target resolution.
func PathPrefixCoversWholeAgentTree(pathPrefix string) bool {
	trimmed := strings.Trim(strings.TrimSpace(pathPrefix), "/")
	return trimmed == "" || trimmed == "root"
}
