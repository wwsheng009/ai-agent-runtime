package policy

import "strings"

// ReadOnlyEscalationPathText 是只读边界"写需求出路"的唯一文案来源。三处消费方
// （子代理 system prompt 横幅、父代理 spawn 报告、模型可见的拒绝指导）共用同一
// 份文本，避免像历史上 spawn schema 文案那样出现互相矛盾的漂移副本。
const ReadOnlyEscalationPathText = "If your goal requires file changes, do not attempt them here: return the exact required change to the parent (file, action, content); the parent can apply it or re-spawn you with read_only=false."

// BoundaryManifest 描述一个子代理生效中的能力边界。它由 child_factory 在生成
// 子代理配置时依据 task.ReadOnly / ReadOnlySource / ToolsWhitelist 与继承到的
// 父策略一次计算，再渲染到 prompt、spawn 报告与拒绝指导三处。
type BoundaryManifest struct {
	ReadOnly       bool
	Source         string // explicit | agentdef | parent_tool_execution_policy
	RemovedTools   []string
	ShellReadOnly  bool
	PermissionMode string
}

// RenderReadOnlyBoundaryBlock 渲染子代理 system prompt 的 READ-ONLY BOUNDARY
// 扩展块。非只读清单返回空串，调用方原样拼接。
func RenderReadOnlyBoundaryBlock(m BoundaryManifest) string {
	if !m.ReadOnly {
		return ""
	}
	lines := make([]string, 0, 3)
	if strings.TrimSpace(m.Source) != "" {
		lines = append(lines, "Read-only boundary source: "+strings.TrimSpace(m.Source)+".")
	}
	if len(m.RemovedTools) > 0 {
		lines = append(lines, "Write-like tools removed from this agent: "+strings.Join(m.RemovedTools, ", ")+". They will also be denied at execution.")
	}
	lines = append(lines, ReadOnlyEscalationPathText)
	return strings.Join(lines, "\n")
}
