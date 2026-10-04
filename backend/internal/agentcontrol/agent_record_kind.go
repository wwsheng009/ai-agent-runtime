package agentcontrol

import "strings"

// AgentRecord identity classification.
//
// AgentType 这个字段被重载了两套互不相容的语义，判身份时不能用它当结构判据：
//   - 结构值：root / child / team_teammate（AgentRecord 自身）
//   - 角色值：spawn_agent 显式指定的 agent 定义名（general/explore/plan…）
//
// 两套值都写进同一个列，所以「值等于某个常量」只能覆盖**恰好没有覆盖语义**的那
// 一小部分行。下面的方法把「结构身份」与「角色」彻底分开，让调用点只依赖不带
// 歧义的字段。

// IsTeamTeammate reports whether this identity row is a spawn_team teammate.
//
// 不要用 `AgentType == AgentTypeTeamTeammate` 判身份：team 身份行的 AgentType
// 写进去的是 `firstNonEmptyString(mate.Profile, AgentTypeTeamTeammate)`
// （runtimeapi/team_handlers.go 的 teammate 投影、aicli 的 chat_actor_registry
// 同源两处），只有队友**没配 profile** 时才等于 "team_teammate"。按该字段判
// 会漏掉绝大多数真实 team 行 —— 与 chat_actor_agent_obligations.go 里记录的
// spawn_agent 同源问题（那边已因此改成树结构判据）。
//
// 这里用不带歧义的结构字段：Workflow / TeamID / TeammateID 是 team 行的专属
// 写入路径，spawn_agent 与 root 行都不会带。最后保留 AgentType 兜底，以识别
// workflow 列写入前就已落库的历史行。
func (r AgentRecord) IsTeamTeammate() bool {
	switch {
	case strings.EqualFold(strings.TrimSpace(r.Workflow), WorkflowSpawnTeam):
		return true
	case strings.TrimSpace(r.TeamID) != "":
		return true
	case strings.TrimSpace(r.TeammateID) != "":
		return true
	case strings.EqualFold(strings.TrimSpace(r.AgentType), AgentTypeTeamTeammate):
		return true
	default:
		return false
	}
}

// IsRootAgent reports whether this identity row is the foreground/root node.
//
// 两种判据并用：root 行的 AgentType 是硬写 AgentTypeRoot（不做 profile 回落的
// 唯一一种行），AgentPath 则是权威的树位置。任一命中即认定。
func (r AgentRecord) IsRootAgent() bool {
	return strings.EqualFold(strings.TrimSpace(r.AgentPath), "/root") ||
		strings.EqualFold(strings.TrimSpace(r.AgentType), AgentTypeRoot)
}
