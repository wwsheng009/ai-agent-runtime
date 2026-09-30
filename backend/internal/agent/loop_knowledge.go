package agent

// W6：知识层复用的写意图判定——`contextmgr.BuildInput.KnowledgeWrite` 的置位口。
//
// 04 §4.4：同任务内"涉及写操作"的复用阈值 ≥0.90 且强制一次验证读取；跨任务
// 复用恒为 ≥0.90 + 强制验证（W3/W4 已在 Planner 侧落地）。agent 循环是知道
// 当前目标与工具边界的调用方，因此在这里把写意图透传给 contextmgr
// （W5 已预留 `BuildInput.KnowledgeWrite` 通道，此前无调用方置位）。
//
// 判定方向（保守优先，G2 `unsafe_reuse_count=0`）：
//   - 只读策略（只读子代理 / 只读会话）→ false：该 run 无写能力，写阈值只会
//     压缩只读复用空间，不提升安全性；
//   - 强写意图短语（goalHasWriteIntent，M5 的既有启发式）→ true；
//   - 只读核验措辞（goalHasReadOnlyIntent）→ false；
//   - 其余（无法判定）→ true：写语义更严（≥0.90 + 强制验证），误报只降低
//     复用率，不产生不安全复用；W7 用实测校准（04 §7.6）。
func (loop *ReActLoop) knowledgeWriteIntent(goal string) bool {
	if loop == nil || loop.agent == nil {
		// 无法判定 → 保守按写语义（更严阈值 + 强制验证）。
		return true
	}
	if agentIsReadOnly(loop.agent) {
		return false
	}
	if goalHasWriteIntent(goal) {
		return true
	}
	return !goalHasReadOnlyIntent(goal)
}

// agentIsReadOnly 报告 agent 是否处于只读边界（只读子代理 / 只读会话策略）。
//
// 只读标志来自工具执行策略：子代理路径由 `SubagentTask.ReadOnly` /
// agentdef sandbox 折叠到 `Options["read_only"]` 与策略的 ReadOnly（W6 核实
// 既有最小枚举，无需新增字段）。
func agentIsReadOnly(agent *Agent) bool {
	if agent == nil {
		return false
	}
	policy := agent.GetToolExecutionPolicy()
	return policy != nil && policy.ReadOnly
}
