package runtimehost

import (
	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// SessionContext 是 actor 构建期从会话元数据派生的执行上下文（纯数据）。
//
// 它把「读会话 metadata → 决定 workspace / profile / 路由 / 子代理标记」这段
// 与 Handler 无关的逻辑收敛到执行核，供 runtimeapi / aicli / scheduled 共用
// （E3.1a；见 docs/automation-tasks/runtimehost-executor-extraction-plan.md）。
type SessionContext struct {
	// WorkspacePath 是会话绑定的工作目录（worktree 覆盖优先）。
	WorkspacePath string
	// ProfileRef / AgentID 是会话 profile 引用，供调用方解析 profile 状态。
	ProfileRef string
	AgentID    string
	// RoutingOverrideRaw 是会话级路由覆盖（JSON 字符串；主方案 §3.4）。
	RoutingOverrideRaw string
	// 子代理标记（agent_type / depth / read_only / completion requirement）。
	ChildAgentType             string
	ChildCompletionRequirement string
	ChildReadOnly              bool
	ChildDepth                 int
	// 模型/提供商/推理强度覆盖。
	RequestedProvider        string
	RequestedModel           string
	RequestedReasoningEffort string
	// StreamRequested / DisableTools 是本回合的执行偏好。
	StreamRequested bool
	DisableTools    bool
	// ObservationTaskID 是观察任务作用域（团队任务激活时存在；空串 = 无任务语义）。
	ObservationTaskID string
}

// ExtractSessionContext 从已加载的会话派生执行上下文。
//
// 纯函数：不读任何进程级状态；nil 会话返回零值。调用方负责先用**带截止时间**的
// 查询加载会话（session_history.sqlite 可能与并发 aicli CLI 进程共享、被写锁阻塞）。
//
// 语义与抽取前 buildSessionActor 内联块逐条对齐（E3.1a：行为零变化）：
//   - WorkspacePath 先取 sessionmeta.WorkspacePath，再被 worktree 路径覆盖；
//   - RequestedModel 先取 agent_type 请求模型，空则回落 sessionmeta.Model；
//   - Bool/Int 仅在元数据存在（ok=true）时才覆盖零值。
func ExtractSessionContext(session *chat.Session) SessionContext {
	if session == nil {
		return SessionContext{}
	}
	ctx := session.Metadata.Context
	getString := func(key string) string { return sessionmeta.String(ctx, key) }

	out := SessionContext{
		ProfileRef:                 getString(sessionmeta.LegacyAPIProfileReference),
		AgentID:                    getString(sessionmeta.ProfileAgent),
		WorkspacePath:              getString(sessionmeta.WorkspacePath),
		RoutingOverrideRaw:         getString(agentconfig.SessionRoutingOverrideContextKey),
		ChildAgentType:             getString(toolbroker.AgentSessionContextAgentType),
		ChildCompletionRequirement: getString(toolbroker.AgentSessionContextCompletionRequirement),
		RequestedProvider:          getString(sessionmeta.ProviderName),
		RequestedModel:             getString(toolbroker.AgentSessionContextRequestedModel),
		RequestedReasoningEffort:   getString(sessionmeta.ReasoningEffort),
		ObservationTaskID:          getString(sessionmeta.ActiveTeamTaskID),
	}
	if worktreePath := getString(toolbroker.AgentSessionContextWorktreePath); worktreePath != "" {
		out.WorkspacePath = worktreePath
	}
	if out.RequestedModel == "" {
		out.RequestedModel = getString(sessionmeta.Model)
	}
	if value, ok := sessionmeta.Bool(ctx, sessionmeta.Stream); ok {
		out.StreamRequested = value
	}
	if value, ok := sessionmeta.Bool(ctx, sessionmeta.DisableTools); ok {
		out.DisableTools = value
	}
	if value, ok := sessionmeta.Bool(ctx, toolbroker.AgentSessionContextReadOnly); ok {
		out.ChildReadOnly = value
	}
	if value, ok := sessionmeta.Int(ctx, toolbroker.AgentSessionContextDepth); ok {
		out.ChildDepth = value
	}
	return out
}
