package agent

import runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"

// ToolExecutionPolicy 约束 agent 在 act 阶段允许执行的工具。
type ToolExecutionPolicy = runtimepolicy.ToolExecutionPolicy

// NewToolExecutionPolicy 创建工具执行策略。
func NewToolExecutionPolicy(allowedTools []string, readOnly bool) *ToolExecutionPolicy {
	return runtimepolicy.NewToolExecutionPolicy(allowedTools, readOnly)
}

func CapabilitiesForTask(role string, readOnly bool, toolNames, writePaths []string) []runtimepolicy.Capability {
	return runtimepolicy.CapabilitiesForTask(role, readOnly, toolNames, writePaths)
}

func isWriteLikeToolName(toolName string) bool {
	return runtimepolicy.IsWriteLikeToolName(toolName)
}
