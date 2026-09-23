package events

// 会话级路由管理事件名常量（方案 docs/plan/session-scoped-agent-routing-management-plan-20260922.md §7.1）。
//
// 与姊妹文件 main_agent_routing.go / subagent_audit_events.go 同构：
//
//  1. 类型名在这里定义一次，发射点（internal/api/skills/session_routing_handlers.go）
//     只引用常量，不写裸字面量；
//  2. contract.go 的 runtimeEventContracts 登记同一常量（同包引用），通道由注册表决定；
//  3. contract_test.go 断言「常量 ↔ 注册表 ↔ runtimeobserve 已知目录」三方一致。
//
// 本文件不得 import 任何内部包（与 contract.go 同约束）。
const (
	// EventSessionRoutingChanged：会话级路由覆盖被写入/清除后发布的失效信号 + 摘要。
	//
	// 语义边界（§7.1）：事件**不是**权威数据源，只是「路由已变更、请重新读取」的
	// 提示；客户端一律以 GET /api/runtime/sessions/{id}/routing 的投影为准，避免
	// payload 与投影漂移。顺序固定为：写入落盘 → 发布本事件 → 失效 actor（§4.5），
	// 因此收到事件后进行中的 turn 仍可能使用旧路由（effective_from=next_turn）。
	//
	// 通道选择 A（ChannelSessionStore）：路由覆盖的写入与清除是会话级治理动作，
	// 事后必须能反查「谁在什么时候把哪一层改成了什么」（与主 Agent route 治理事件
	// 同一取舍，代价记入方案 §10 风险 R2）。
	EventSessionRoutingChanged = "session.routing_changed"
)
