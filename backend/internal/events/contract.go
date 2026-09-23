package events

import "strings"

// 运行时事件「交付契约」单一真源（方案 docs/plan/sse-live-event-channel-optimization-plan.md
// §4 Batch 2 的注册表雏形）。
//
// 背景：同一份运行时事实经四条路径抵达前端，每条路径原先各持一份手写白名单：
//
//	A 会话事件库落盘（api/skills 的 isPersistedRuntimeEventType）
//	B live-only SSE 旁路（api/skills 的 isSessionLiveOnlyRuntimeEvent）
//	C chat SSE 帧桥（api/skills 的 DeliveryChannelsFor 的 chat_bridge 分支）
//	D 回合末尾巴帧（api/skills 的 DeliveryChannelsFor 的 tail_only 分支）
//
// 四份清单漂移时，症状只是「前端没反应」，无法与「本来就没有事件」区分。本文件把
// 「类型 → 通道」收敛成一张声明式注册表，api/skills 侧四个判定全部改为从注册表派生；
// 契约漂移由 contract_test.go 的 AST 门禁挡住（新增 chat 事件常量未登记即失败）。
//
// 硬约束：本文件**不得 import 任何内部包**（尤其 internal/chat）。chat/actor.go 等已
// import internal/events，此处再反向 import 即构成 import cycle；因此类型名一律用
// 字符串字面量，并由门禁测试断言「字面量 ↔ 各包常量」双向一致（含本文件 import
// 清单必须为空这一结构性断言）。新增类型时改这里，不要改 api/skills 的白名单。

// ChannelSet 是「chat 侧交付通道」的位集合。多通道可叠加（例如 tool.requested 既
// 经 A 落盘、又经 C 实时下帧）。
type ChannelSet uint8

const (
	// ChannelSessionStore（A）：落盘到会话事件库 ⇒ 实时（长轮询/SSE 回放）与回放都能看到。
	ChannelSessionStore ChannelSet = 1 << iota
	// ChannelLiveOnly（B）：仅实时转发，不落盘（刷新即丢）。
	ChannelLiveOnly
	// ChannelChatBridge（C）：chat SSE 帧桥（工具生命周期在对话流里实时建行）。
	ChannelChatBridge
	// ChannelTailOnly（D）：仅回合末尾巴补发。
	ChannelTailOnly
)

// 通道的稳定字符串名（与 api/skills/runtime_event_delivery.go 的快照键、以及前端
// 生成物共用；改名等于改契约，须同步方案文档与前端生成物）。
const (
	ChannelNameSessionStore = "session_store"
	ChannelNameLiveOnly     = "live_only"
	ChannelNameChatBridge   = "chat_bridge"
	ChannelNameTailOnly     = "tail_only"
)

// ChatSSEEventPrefix 是 chat SSE 帧事件类型的前缀（"chat.sse."）。它不产生注册表条目
// （帧名 = 前缀 + kind，是前缀派生而非封闭枚举），但前后端都要用它识别帧事件：后端
// api/skills 用它下帧，前端用它把帧名映射成轨迹 kind。值必须与
// api/skills/trajectory_events.go 的 chatSSEStreamEventPrefix 一致——由 AST 门禁断言
// （本文件不得 import 内部包，故只留字面量 + 门禁，与类型名同策略）。
const ChatSSEEventPrefix = "chat.sse."

// Contract 声明一个 runtime 事件类型在 chat 侧交付链路上的归属。
//
// Channels == 0 表示「已登记、但当前没有任何 chat 侧交付通道」：这是显式声明而非
// 遗漏——正是 P0-2 要求「未命中必须可观测」的那一类，也是门禁要求新增类型必须
// 表态的落点（想不出通道就写 0，而不是不登记）。
type Contract struct {
	// Type 是总线上的精确类型名（TrimSpace 后精确匹配；不折叠大小写）。
	Type string
	// Channels 是交付通道位集合。
	Channels ChannelSet
	// ProvenanceBearing 表示该类型可能承载 provenance 信号（类型维度）。
	// 载荷维度（source_refs / profile_source_refs）由 api/skills 侧补充判定。
	ProvenanceBearing bool
	// PersistCritical（P1.5/D3）表示该类型是关键事件：批量落盘桥接命中时必须
	// 立即触发 flush（绕过 25ms 间隔等待），把崩溃窗口压到「当前批内更早的
	// 事件」而非「≤batchSize 条任意事件」。仅对 ChannelSessionStore 类型有意义。
	PersistCritical bool
}

// runtimeEventContracts 是封闭注册表：新增 runtime 事件类型必须在此表态。
// 顺序按家族分组（A 落盘 / B live-only / C 帧桥 / D 尾巴 / 无通道），
// 对外导出（RegisteredEventTypes）按字典序排序，保证代码生成物稳定。
var runtimeEventContracts = []Contract{
	// ---- A 通道：落盘（原 isPersistedRuntimeEventType 清单）----
	{Type: "tool.requested", Channels: ChannelSessionStore | ChannelChatBridge},
	{Type: "tool.completed", Channels: ChannelSessionStore | ChannelChatBridge, PersistCritical: true},
	{Type: "context.profile.injected", Channels: ChannelSessionStore, ProvenanceBearing: true},
	{Type: "recall.performed", Channels: ChannelSessionStore, ProvenanceBearing: true},
	{Type: "checkpoint_created", Channels: ChannelSessionStore, PersistCritical: true},
	{Type: "approval_requested", Channels: ChannelSessionStore, PersistCritical: true},
	{Type: "approval_resolved", Channels: ChannelSessionStore, PersistCritical: true},
	{Type: "agent.reclaimed", Channels: ChannelSessionStore},
	{Type: "session_compact_started", Channels: ChannelSessionStore},
	{Type: "session_compact_completed", Channels: ChannelSessionStore},
	{Type: "session_compact_skipped", Channels: ChannelSessionStore},
	{Type: "session_compact_failed", Channels: ChannelSessionStore},
	{Type: "session_start", Channels: ChannelSessionStore, PersistCritical: true},
	{Type: "session_end", Channels: ChannelSessionStore, PersistCritical: true},
	{Type: "session_interrupted", Channels: ChannelSessionStore, PersistCritical: true},
	{Type: "context_reconciled", Channels: ChannelSessionStore},
	// 方案B：增量打字机事件落盘，供 runtime/stream 长轮询实时消费。
	{Type: "assistant_delta", Channels: ChannelSessionStore},
	{Type: "assistant_reasoning", Channels: ChannelSessionStore},
	{Type: "assistant.reasoning", Channels: ChannelSessionStore},
	{Type: "assistant.image_progress", Channels: ChannelSessionStore},

	// ---- A 通道：主 Agent 动态 route 治理事件（方案 §6.2；MG3 的修复）----
	// 全部走 A 通道：成本归因、失败/降级路径与「还原到基线」的证据都必须在事后
	// 查得到，否则「主 Agent 路由可审计」这一治理价值不成立。代价是每 turn 多出
	// 1–3 行事件（相对 assistant_delta 量级可忽略，记入方案 §10 风险 R2）。
	{Type: EventMainAgentRouteApplied, Channels: ChannelSessionStore},
	{Type: EventMainAgentRoutePredictionInvalid, Channels: ChannelSessionStore},
	{Type: EventMainAgentRoutePredictionUnresolvable, Channels: ChannelSessionStore},
	{Type: EventMainAgentRouteDisabledForTurn, Channels: ChannelSessionStore},
	{Type: EventMainAgentRouteCostGuardTripped, Channels: ChannelSessionStore, PersistCritical: true},
	{Type: EventMainAgentRouteCleared, Channels: ChannelSessionStore},

	// ---- A 通道：会话级路由管理（方案 §7.1）----
	// 会话级覆盖的写入/清除是治理动作：三层（session/workspace/config）落点与
	// revision 必须在会话事件流里可反查，否则「为什么这个会话换了模型」在事后
	// 无从追责。事件本身只是失效信号 + 摘要，权威投影仍是 GET /routing（防漂移）。
	{Type: EventSessionRoutingChanged, Channels: ChannelSessionStore},

	// ---- A 通道：在途健康门禁已泄漏的信号（方案 §6.2 第 4 步补登记）----
	// 两者此前以裸字面量发射且未登记 ⇒ ChannelsFor 返回 0 ⇒ 不落盘、不下帧、
	// 不进尾巴帧，「唯一对外信号完全不可见」。登记后它们是主 Agent 健康维度
	// 切换与 prompt cache 熔断的证据链。两者都是边沿触发（只在状态真正翻转时
	// 发一次），落盘量有天然上界。
	{Type: EventLLMProviderHealthOpened, Channels: ChannelSessionStore},
	{Type: EventLLMPromptCacheBreakerTripped, Channels: ChannelSessionStore},

	// ---- B 通道：live-only（不落盘，刷新即丢）----
	{Type: "tool.progress", Channels: ChannelLiveOnly},
	{Type: "subagent.progress", Channels: ChannelLiveOnly},
	{Type: "subagent.batch.progress", Channels: ChannelLiveOnly},

	// ---- D 通道：仅回合末尾巴补发（实时通道与事件库都没有它们）----
	{Type: "subagent.batch.started", Channels: ChannelTailOnly},
	{Type: "subagent.batch.completed", Channels: ChannelTailOnly},
	{Type: "subagent.task.started", Channels: ChannelTailOnly},
	{Type: "subagent.task.completed", Channels: ChannelTailOnly},
	{Type: "subagent.started", Channels: ChannelTailOnly},
	// subagent.completed 是**摘要落盘**：agent-controller 在子会话终态时把
	// 生命周期摘要（agent_id/path/status/usage，无子正文）直接 AppendEvent 写进
	// 父会话库（session_runtime_support.go），同时经总线发布供尾巴帧消费。
	// 因此它既是 D 通道（尾巴）也是 A 通道（事件库）——落盘由生产者完成，
	// A 通道桥靠 ProducerPersistedEvent 跳过，避免同一摘要写两遍。
	{Type: "subagent.completed", Channels: ChannelSessionStore | ChannelTailOnly},

	// ---- A 通道：子代理路由审计与批次失败态终态（SA-G1/SA-G2 的修复）----
	// 新增类型（常量见 subagent_audit_events.go，发射点只引用常量）：
	//   subagent.route.resolved：开工时刻的路由决策，归属**父会话**落盘，让取消/
	//   超时/孤儿/崩溃的子代理也能反查「当时被路由到哪个模型」；
	//   4 个批次失败态此前未登记 ⇒ ChannelsFor 返回 0 ⇒ 不下帧、不落盘、不进尾巴帧，
	//   被取消或超时的批次在 chat 侧完全没有终态信号（§3.5 实测 15 个 timed_out 批次）。
	// 失败态比成功态更重（成功态有 subagent.completed 与批次账本兜底），故走 A+D；
	// 与 batch.started/completed 的纯 D 不一致是**已记录的取舍**（方案 §9 R3）。
	{Type: EventSubagentRouteResolved, Channels: ChannelSessionStore | ChannelTailOnly},
	{Type: EventSubagentBatchFailed, Channels: ChannelSessionStore | ChannelTailOnly},
	{Type: EventSubagentBatchCanceled, Channels: ChannelSessionStore | ChannelTailOnly},
	{Type: EventSubagentBatchTimedOut, Channels: ChannelSessionStore | ChannelTailOnly},
	{Type: EventSubagentBatchOrphaned, Channels: ChannelSessionStore | ChannelTailOnly},

	// ---- 已登记、当前无 chat 侧通道 ----
	// chat/events.go 常量（其中 tool_started/tool_finished 是 tool.requested/
	// tool.completed 落库时的映射结果，作为「入站类型」没有自己的通道）。
	{Type: "assistant_message"},
	{Type: "llm_request_started"},
	{Type: "llm_request_finished"},
	{Type: "tool_started"},
	{Type: "tool_finished"},
	{Type: "tool_receipt_recorded"},
	{Type: "tool_receipt_replayed"},
	{Type: "question_asked"},
	{Type: "question_answered"},
	{Type: "rewind_started"},
	{Type: "rewind_finished"},
	{Type: "backtrack_started"},
	{Type: "backtrack_finished"},
	{Type: "job_started"},
	{Type: "job_output"},
	{Type: "job_cancelled"},
	{Type: "job_finished"},
	{Type: "mailbox_received"},
	// 总线真实别名 / 宿主侧事件（runtimeobserve 已知目录的来源 4/5）。
	{Type: "assistant.delta"},
	{Type: "tool.reduced"},
	{Type: "tool.denied"},
	{Type: "tool.malformed_arguments.guardrail_hit"},
	{Type: "tool.malformed_arguments.recovered"},
	{Type: "session.checkpoint_persist_error"},

	// ---- 已登记、当前无 chat 侧通道：SA-G2 扫描门禁收编的历史裸字面量 ----
	// 这些类型由 internal/agent 的 emitRuntimeEvent("<literal>") 真实发射，此前
	// 既不在注册表也不在 runtimeobserve 目录 ⇒ 与「完全未知类型」不可区分（三分法
	// 失效）。登记为 0 通道 = 显式表态「当前无 chat 侧交付通道」，**不改变任何投递
	// 行为**；是否给某条通道需单独评审（方案 §6.1 第 6 条的说明）。
	{Type: "agent.turn.started"},                   // loop.go
	{Type: "agent.turn.finished"},                  // loop.go
	{Type: "completion.requirement_recovery"},      // loop.go
	{Type: "context.preflight.started"},            // loop.go
	{Type: "context.preflight.compacted"},          // loop.go
	{Type: "context.preflight.failed"},             // loop.go
	{Type: "context.tool_schema.compacted"},        // loop.go
	{Type: "context.tool_schema.frozen"},           // loop.go
	{Type: "hooks.stop_blocked"},                   // loop.go
	{Type: "llm.request.started"},                  // loop.go（点分隔形；下划线形见上）
	{Type: "llm.request.finished"},                 // loop.go（点分隔形；下划线形见上）
	{Type: "llm.retry"},                            // loop.go
	{Type: "llm.retry.aggregated"},                 // loop.go
	{Type: "llm.max_output_tokens.escalated"},      // loop.go
	{Type: "llm.prompt_cache.backoff_applied"},     // loop.go
	{Type: "llm.reasoning_only.guardrail_hit"},     // loop.go
	{Type: "llm.reasoning_only.recovered"},         // loop.go
	{Type: "patch.decision"},                       // orchestrator.go
	{Type: "patch.applied"},                        // scheduler.go
	{Type: "subagent.batch.created"},               // loop.go（批次创建里程碑）
	{Type: "subagent.batch.circuit_open"},          // scheduler.go（熔断治理信号）
	{Type: "subagent.denied"},                      // scheduler.go
	{Type: "subagent.requires_write"},              // loop.go
	{Type: "tool_loop.exploration_stall_observed"}, // loop.go
	{Type: "tool_loop.repeated_prompt_observed"},   // loop.go
}

var runtimeEventContractByType = buildRuntimeEventContractIndex()

func buildRuntimeEventContractIndex() map[string]Contract {
	index := make(map[string]Contract, len(runtimeEventContracts))
	for _, contract := range runtimeEventContracts {
		index[contract.Type] = contract
	}
	return index
}

// normalizeContractType 只做 TrimSpace，与 runtimeobserve 的归一化口径一致
// （不折叠大小写，保留「仅大小写不同 ⇒ 未知类型」的异常语义）。
func normalizeContractType(eventType string) string {
	return strings.TrimSpace(eventType)
}

// ContractFor 返回类型的契约声明；ok=false 表示该类型未登记。
func ContractFor(eventType string) (Contract, bool) {
	contract, ok := runtimeEventContractByType[normalizeContractType(eventType)]
	return contract, ok
}

// IsRegisteredEventType 判断类型是否已在注册表登记（不代表它有交付通道）。
func IsRegisteredEventType(eventType string) bool {
	_, ok := ContractFor(eventType)
	return ok
}

// ChannelsFor 返回类型的 chat 侧交付通道集合；未登记类型返回 0。
func ChannelsFor(eventType string) ChannelSet {
	contract, ok := ContractFor(eventType)
	if !ok {
		return 0
	}
	return contract.Channels
}

// IsPersistedEventType 判断类型是否经 A 通道落盘（原 api/skills 白名单语义）。
func IsPersistedEventType(eventType string) bool {
	return ChannelsFor(eventType)&ChannelSessionStore != 0
}

// IsPersistCriticalEventType 判断类型是否关键事件（P1.5/D3）：批量落盘桥接
// 命中时立即触发 flush。未登记类型返回 false（批量缓冲对未登记类型不启用
// 关键路径——落盘判定 IsPersistedEventType 已先把它们挡在外面）。
func IsPersistCriticalEventType(eventType string) bool {
	contract, ok := ContractFor(eventType)
	if !ok {
		return false
	}
	return contract.PersistCritical && contract.Channels&ChannelSessionStore != 0
}

// ProducerPersistedEvent 判断事件是否**已由生产者写入会话事件库**：生产者
// AppendEvent 成功后会回填 payload["seq"]，随后才把同一事件发布到总线。
//
// A 通道（总线 → 事件库）据此跳过，避免「生产者已写 + 桥再写」的双份行。
// 当前唯一生产者是 agent-controller 的 subagent.completed 摘要行
// （api/skills/session_runtime_support.go）；CLI 本地 A 通道桥自始就有同样的
// 判据（cmd/aicli/commands/chat_actor_host.go），本函数把它收敛成单一实现。
// 判定刻意保持宽松（键存在即可，不校验数值）：与 CLI 侧既有行为一致，且 seq
// 只可能由 AppendEvent 回填路径写入。
func ProducerPersistedEvent(event Event) bool {
	if event.Payload == nil {
		return false
	}
	if _, persisted := event.Payload["seq"]; persisted {
		return true
	}
	return false
}

// IsLiveOnlyEventType 判断类型是否走 B 通道（仅实时、不落盘）。
func IsLiveOnlyEventType(eventType string) bool {
	return ChannelsFor(eventType)&ChannelLiveOnly != 0
}

// IsProvenanceBearingEventType 判断类型是否可能承载 provenance 信号（类型维度）。
func IsProvenanceBearingEventType(eventType string) bool {
	contract, ok := ContractFor(eventType)
	return ok && contract.ProvenanceBearing
}

// RegisteredEventTypes 返回全部已登记类型（字典序，供代码生成与门禁使用）。
func RegisteredEventTypes() []string {
	return TypesWithChannel(0)
}

// TypesWithChannel 返回命中任一给定通道位（channelSet == 0 表示「全部已登记」）的
// 类型列表，字典序排序。
func TypesWithChannel(channelSet ChannelSet) []string {
	types := make([]string, 0, len(runtimeEventContracts))
	for _, contract := range runtimeEventContracts {
		if channelSet != 0 && contract.Channels&channelSet == 0 {
			continue
		}
		types = append(types, contract.Type)
	}
	sortStrings(types)
	return types
}

// LiveOnlyEventTypes 返回 B 通道类型（live 订阅用，字典序稳定）。
func LiveOnlyEventTypes() []string {
	return TypesWithChannel(ChannelLiveOnly)
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
