package events

// 子代理路由审计与批次终态事件名常量。
//
// 背景（方案 docs/plan/task-difficulty-routing-audit-hardening-plan-20260921.md
// §5.1/§5.2 的主措施）：发射点**不得**使用裸字面量。病因是「门禁覆盖不到
// internal/agent 里的裸字面量」——只要事件名以字面量散落在发射点，注册表与
// 发射点之间就没有任何机械约束，新增事件漏登记时症状只是「查不到账 / 前端没
// 反应」。因此：
//
//  1. 类型名在这里定义一次，发射点（internal/agent/scheduler.go、
//     internal/agent/subagent_batch_coordinator.go）只引用常量；
//  2. contract.go 的 runtimeEventContracts 登记同一批类型（同包常量不违反
//     「contract.go 不得 import 内部包」的硬约束），通道由注册表决定；
//  3. contract_test.go 断言「常量 ↔ 注册表 ↔ runtimeobserve 已知目录」三方一致，
//     并有扫描型测试禁止新的裸字面量发射点。
//
// 与姊妹方案 main_agent_routing.go 同构。本文件不得 import 任何内部包。
const (
	// EventSubagentRouteResolved：子代理开工时刻的路由决策审计（G1 的修复）。
	//
	// 归属**父会话**（与 subagent.started 挂子会话不同，这是本事件的关键点）：
	// 取消 / 超时 / 孤儿 / 进程崩溃的子代理，其「当时被路由到哪个模型」必须在
	// 父会话事件流里可反查——D 通道的 subagent.started 既不落盘、又被子会话
	// primary-session 过滤挡掉，两个通道都到不了父会话。
	//
	// 载荷经 mergeRouteAuditPayload + 截断（goal / difficulty_rationale ≤256 字符，
	// 整行 ≤2 KB），每次 attempt 发一行并带 attempt 序号以保留重试轨迹。
	EventSubagentRouteResolved = "subagent.route.resolved"

	// 批次失败态终态（G2 的修复）。
	//
	// 未登记时 ChannelsFor 返回 0 ⇒ 不下帧、不落盘、不进尾巴帧：一个被取消或
	// 超时的批次在 chat 侧没有任何可观测的终态事件（§3.5 实测 15 个 timed_out /
	// 42 个 failed 批次属此态）。登记后走 A+D：失败态需要事后追责与统计，而成功
	// 态有 subagent.completed 与批次账本兜底。
	EventSubagentBatchFailed   = "subagent.batch.failed"
	EventSubagentBatchCanceled = "subagent.batch.canceled"
	EventSubagentBatchTimedOut = "subagent.batch.timed_out"
	EventSubagentBatchOrphaned = "subagent.batch.orphaned"
)
