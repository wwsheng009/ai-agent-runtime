package agent

import runtimepolicy "github.com/ai-gateway/ai-agent-runtime/internal/policy"

// ToolExecutionPolicy 约束 agent 在 act 阶段允许执行的工具。
type ToolExecutionPolicy = runtimepolicy.ToolExecutionPolicy

// NewToolExecutionPolicy 创建工具执行策略。
func NewToolExecutionPolicy(allowedTools []string, readOnly bool) *ToolExecutionPolicy {
	return runtimepolicy.NewToolExecutionPolicy(allowedTools, readOnly)
}

func isWriteLikeToolName(toolName string) bool {
	return runtimepolicy.IsWriteLikeToolName(toolName)
}
