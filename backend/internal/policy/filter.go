package policy

import (
	"sort"

	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

// FilterToolNames filters and sorts tool names allowed by policy.
func FilterToolNames(names []string, policy *ToolExecutionPolicy) []string {
	if len(names) == 0 {
		return nil
	}
	filtered := make([]string, 0, len(names))
	for _, name := range names {
		if policy == nil || policy.AllowsDefinition(name) {
			filtered = append(filtered, name)
		}
	}
	sort.Strings(filtered)
	return filtered
}

// FilterToolInfos filters tool definitions allowed by policy, preserving order.
func FilterToolInfos(tools []skill.ToolInfo, policy *ToolExecutionPolicy) []skill.ToolInfo {
	if len(tools) == 0 {
		return nil
	}
	filtered := make([]skill.ToolInfo, 0, len(tools))
	for _, tool := range tools {
		if policy == nil || policy.AllowToolInfo(tool) == nil {
			filtered = append(filtered, tool)
		}
	}
	return filtered
}
