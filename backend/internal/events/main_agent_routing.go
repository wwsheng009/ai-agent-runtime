package events

// 主 Agent 动态 route 与 provider 健康维度的事件名常量。
//
// 背景（方案 docs/plan/main-agent-dynamic-provider-model-switching-plan-20260921.md
// §6.2 的主措施）：发射点**不得**使用裸字面量。病因是「门禁覆盖不到 internal/agent
// 里的裸字面量」——只要事件名以字面量散落在发射点，注册表与发射点之间就没有任何
// 机械约束，新增事件漏登记时症状只是「前端没反应」。因此：
//
//  1. 类型名在这里定义一次，发射点（internal/agent/loop.go）只引用常量；
//  2. contract.go 的 runtimeEventContracts 登记同一批类型，通道由注册表决定；
//  3. contract_test.go 断言「常量 ↔ 注册表 ↔ runtimeobserve 已知目录」三方一致。
//
// 本文件不得 import 任何内部包（与 contract.go 同约束）：常量是纯字面量，
// 反向依赖会让 internal/events 与 internal/chat 构成 cycle。
const (
	// EventMainAgentRouteApplied：某 step 实际使用了非基线 route（含 source=baseline
	// 的常规情形，便于统计「本 turn 有多少 step 走了基线」）。
	EventMainAgentRouteApplied = "main_agent.route_applied"
	// EventMainAgentRoutePredictionInvalid：难度上报格式非法 / 难度值非法。
	EventMainAgentRoutePredictionInvalid = "main_agent.route_prediction_invalid"
	// EventMainAgentRoutePredictionUnresolvable：上报的 provider/model 无法解析
	// （配置问题；保持当前 route，turn 不禁用）。
	EventMainAgentRoutePredictionUnresolvable = "main_agent.route_prediction_unresolvable"
	// EventMainAgentRouteDisabledForTurn：连续非法上报达阈值，本 turn 不再接受难度上报。
	EventMainAgentRouteDisabledForTurn = "main_agent.route_disabled_for_turn"
	// EventMainAgentRouteCostGuardTripped：昂贵档位连续驻留达阈值（关键事件，落盘走
	// PersistCritical 以压缩崩溃窗口）。
	EventMainAgentRouteCostGuardTripped = "main_agent.route_cost_guard_tripped"
	// EventMainAgentRouteCleared：turn 结束还原基线（还原证据，与 route_applied 配对）。
	EventMainAgentRouteCleared = "main_agent.route_cleared"
	// EventLLMProviderHealthOpened：provider 级动态健康电路由关闭转为打开（边沿触发）。
	// 它是主 Agent 健康维度切换的唯一信号（§5.10 规则 1/3 的判据来源）。
	EventLLMProviderHealthOpened = "llm.provider.health_opened"
	// EventLLMPromptCacheBreakerTripped：同指纹 prompt cache 熔断跨过阈值（边沿触发）。
	EventLLMPromptCacheBreakerTripped = "llm.prompt_cache.breaker_tripped"
)
