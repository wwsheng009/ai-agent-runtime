package runtimeobserve

import "strings"

// knownEventTypes 是"产品内已知 runtime 事件类型"的封闭目录。
//
// 语义（docs/plan/ui-event-bridge-drop-hardening.md §6.3 落点 B）：
//   - 命中目录且不在 v1 白名单（projector.go:33 eventAllowlist）→ 计入
//     runtime.filtered_by_type：事件类型有明确产品语义，只因白名单裁剪而丢失；
//   - 未命中目录 → 计入 runtime.unknown_events_dropped，保持异常语义。
//
// 目录是封闭集合，因此 filtered_by_type 的键数天然有上界（即 top-N 语义），
// 快照侧不再二次截断：sum(filtered_by_type) 即"已知被过滤"的总数。
//
// 目录来源（逐条核对代码，禁止凭猜测扩充）：
//  1. 本包 v1 白名单常量（model.go:33-54）。白名单收窄时它们仍按"已知"计数；
//  2. runtime chat 会话事件常量（internal/chat/events.go:5-40）；
//  3. EventBus→SSE 映射表中比来源 1/2 多出的条目
//     （cmd/aicli/commands/web_schema.go:73-111）：
//     llm.request.started（:78）、llm.request.finished（:85）与来源 1 重复，
//     aicli.chat.dynamic_status（web_schema.go:39/:107）、
//     aicli.chat.user_submitted（web_schema.go:55/:108）、
//     aicli.chat.model_selection_changed（web_schema.go:47/:109）、
//     cache_request_finished（internal/cacheanalytics/collector.go:27 → web_schema.go:110）；
//  4. 总线上的真实别名（无具名常量，但有生产 emit 或等价分支证据）：
//     tool.completed（internal/agent/loop.go:2136 等；cmd/aicli/commands/agent_stdio_bridge.go:224
//     与 tool_finished 等价）、tool.requested（internal/agent/loop.go emitRuntimeEvent）、
//     tool.reduced（internal/events/bus.go:1088 统计；agent emitRuntimeEvent）、
//     tool.denied（internal/agent/loop.go:2828）、
//     tool.malformed_arguments.guardrail_hit / tool.malformed_arguments.recovered
//     （internal/agent/loop.go:1285/:1354）、
//     assistant.delta（internal/chat/events.go:8 的点分隔等价形；
//     cmd/aicli/commands/agent_stdio_bridge.go:245 与 encoder.go:2466 均按等价分支处理）。
//  5. 宿主侧持久化事件（internal/chat/actor.go 的中途落库上报）：
//     session.checkpoint_persist_error（actor.go publishSessionCheckpointFailure）。
//  6. live-only 总线类型（不落盘、只走实时旁路，但有真实消费者）：
//     tool.progress（internal/toolprotocol/progress.go:25 EventTypeProgress）、
//     subagent.progress（internal/supervision/subagent_progress.go:21
//     EventTypeSubagentProgress）、subagent.batch.progress
//     （internal/agent/subagent_batch_coordinator.go，M1 的写回失败/降级事件；由
//     internal/events/contract.go 登记为 live-only）。它们不在来源 1-5 的任何
//     清单里，但都是产品事件；复用本目录做交付通道分类时（批次 20 / P0-2）必须
//     先补进来，否则「已知但被通道白名单裁掉」会被误记成「完全未知」，三分法失效。
//  7. 交付契约注册表登记的 chat 侧产品事件（internal/events/contract.go，Batch 2）：
//     context.profile.injected（internal/contextmgr/manager.go:579）、
//     recall.performed（internal/contextmgr/manager.go:735）、
//     agent.reclaimed（internal/agentcontrol/reclaim_events.go:17）、
//     subagent.batch.started（internal/agent/subagent_batch_coordinator.go:1044、
//     internal/agent/loop.go:2567）、subagent.batch.completed
//     （internal/agent/subagent_batch_coordinator.go:1771、loop.go:2604）、
//     subagent.started（internal/agent/scheduler.go:417）、subagent.completed
//     （internal/agent/scheduler.go:454/:507）、subagent.task.started /
//     subagent.task.completed（internal/agent/subagent_batch_coordinator.go，
//     登记为 tail-only）。这些类型此前只出现在交付侧白名单里、不在本目录内，
//     导致「已知但被通道裁掉」与「完全未知」三分法对它们失效（由
//     internal/events/contract_test.go 的注册表 ↔ 目录双向一致门禁暴露）。
//
// 匹配规则与 Projector 保持一致：TrimSpace 后精确匹配；仅大小写不同按未知处理，
// 以保留异常语义（见 normalizeEventType）。
var knownEventTypes = buildKnownEventTypes()

func buildKnownEventTypes() map[string]bool {
	out := make(map[string]bool, 64)
	add := func(types ...string) {
		for _, eventType := range types {
			out[eventType] = true
		}
	}

	// 来源 1：v1 白名单常量（model.go:33-54）。
	add(
		EventRuntimeStarted,
		EventRuntimeReady,
		EventRuntimeShutdown,
		EventSessionStarted,
		EventSessionState,
		EventSessionFinished,
		EventAgentTurnStarted,
		EventAgentTurnDone,
		EventLLMRequestStart,
		EventLLMRequestDone,
		EventLLMAttemptStart,
		EventLLMAttemptDone,
		EventLLMRetry,
		EventLLMStreamSummary,
		EventUsageUpdated,
		EventToolStarted,
		EventToolFinished,
		EventToolFailed,
		EventToolProgress,
		EventSkillInvoked,
		EventRendererChanged,
		EventObservationGap,
		EventResyncRequired,
	)

	// 来源 2：internal/chat/events.go 的会话事件常量（行号见注释）。
	add(
		"session_start",             // events.go:5
		"session_end",               // :6
		"session_interrupted",       // :7
		"assistant_delta",           // :8
		"assistant_reasoning",       // :9
		"assistant.reasoning",       // :13（canonical 别名）
		"assistant.image_progress",  // :14
		"assistant_message",         // :15
		"llm_request_started",       // :16
		"llm_request_finished",      // :17
		"tool_started",              // :18
		"tool_finished",             // :19
		"tool_receipt_recorded",     // :20
		"tool_receipt_replayed",     // :21
		"approval_requested",        // :22
		"approval_resolved",         // :23
		"question_asked",            // :24
		"question_answered",         // :25
		"checkpoint_created",        // :26
		"session_compact_started",   // :27
		"session_compact_completed", // :28
		"session_compact_skipped",   // :29
		"session_compact_failed",    // :30
		"context_reconciled",        // :31
		"rewind_started",            // :32
		"rewind_finished",           // :33
		"backtrack_started",         // :34
		"backtrack_finished",        // :35
		"job_started",               // :36
		"job_output",                // :37
		"job_cancelled",             // :38
		"job_finished",              // :39
		"mailbox_received",          // :40
	)

	// 来源 3：web_schema.go 映射表中额外的总线事件名。
	add(
		"aicli.chat.dynamic_status",          // web_schema.go:39 / :107
		"aicli.chat.user_submitted",          // web_schema.go:55 / :108
		"aicli.chat.model_selection_changed", // web_schema.go:47 / :109
		"cache_request_finished",             // cacheanalytics/collector.go:27 → web_schema.go:110
	)

	// 来源 4：总线真实别名（见文件头说明）。
	add(
		"tool.completed",                         // internal/agent/loop.go:2136 等；agent_stdio_bridge.go:224 ≡ tool_finished
		"tool.requested",                         // internal/agent/loop.go emitRuntimeEvent
		"tool.reduced",                           // internal/events/bus.go:1088；agent emitRuntimeEvent
		"tool.denied",                            // internal/agent/loop.go:2828
		"tool.malformed_arguments.guardrail_hit", // internal/agent/loop.go:1285
		"tool.malformed_arguments.recovered",     // internal/agent/loop.go:1354
		"assistant.delta",                        // assistant_delta 的点分隔等价形（总线上两种都在用）
	)

	// 来源 5：宿主侧持久化事件（见文件头说明）。
	add(
		"session.checkpoint_persist_error", // internal/chat/actor.go publishSessionCheckpointFailure
	)

	// 来源 6：live-only 总线类型（见文件头说明）。这两类不落盘，只经
	// sessionLiveOnlyRuntimeEventTypes 走实时旁路，刷新即丢；它们是产品事件，
	// 但此前不在任何清单里。
	add(
		"tool.progress",           // toolprotocol/progress.go:25：工具中途进度
		"subagent.progress",       // supervision/subagent_progress.go:21：子代理进度（父流镜像）
		"subagent.batch.progress", // agent/subagent_batch_coordinator.go：批次进度写回失败/降级（events/contract.go 登记为 live-only）
	)

	// 来源 7：交付契约注册表登记的 chat 侧产品事件（见文件头说明）。它们经 A 通道
	// 落盘或经 D 通道尾巴补发，但此前不在本目录内。
	add(
		"context.profile.injected", // contextmgr/manager.go:579
		"recall.performed",         // contextmgr/manager.go:735
		"agent.reclaimed",          // agentcontrol/reclaim_events.go:17
		"subagent.batch.started",   // agent/subagent_batch_coordinator.go:1044
		"subagent.batch.completed", // agent/subagent_batch_coordinator.go:1771
		"subagent.started",         // agent/scheduler.go:417
		"subagent.completed",       // agent/scheduler.go:454
		"subagent.task.started",    // agent/subagent_batch_coordinator.go:1184（events/contract.go 登记为 tail-only）
		"subagent.task.completed",  // agent/subagent_batch_coordinator.go:1399（events/contract.go 登记为 tail-only）
	)

	return out
}

// normalizeEventType 与 Projector 的归一化保持一致（只做 TrimSpace，不折叠大小写）。
func normalizeEventType(eventType string) string {
	return strings.TrimSpace(eventType)
}

// isKnownEventType 判断事件类型是否在产品已知目录内（不代表在白名单内）。
func isKnownEventType(eventType string) bool {
	normalized := normalizeEventType(eventType)
	if normalized == "" {
		return false
	}
	return knownEventTypes[normalized]
}

// IsKnownEventType 暴露「产品内已知 runtime 事件类型」判定，供其它交付通道
// （chat 会话事件库白名单等）复用同一份三分法目录：命中目录但被通道白名单裁掉
// 与「完全未知类型」必须在指标里可区分，否则新事件类型被静默丢弃时无从发现
// （批次 20 / P0-2）。语义与 Projector 内的判定完全一致（只 TrimSpace、不折叠
// 大小写）。
func IsKnownEventType(eventType string) bool {
	return isKnownEventType(eventType)
}

// maxFilteredByTypeEntries 是 filtered_by_type 的内部计数上界（top-N 兜底）。
// 目录是封闭集合（来源见文件头），正常永远触发不到；此上限只作为
// "目录被误扩成通配/前缀匹配"时的内存与快照体积保险，超出部分记入溢出桶。
// 不变量由 TestKnownEventTypeCatalogInvariants 断言。
const maxFilteredByTypeEntries = 128

// filteredOverflowKey 承载超出 maxFilteredByTypeEntries 的计数（防御性兜底）。
const filteredOverflowKey = "_other"

// recordFilteredByTypeLocked 在 stateMu 保护下累计"已知但被白名单过滤"的类型计数。
// 调用方必须持有 c.stateMu。
func (c *Collector) recordFilteredByTypeLocked(eventType string) {
	key := normalizeEventType(eventType)
	if key == "" {
		key = filteredOverflowKey
	}
	counters := c.state.Runtime.FilteredByType
	if counters == nil {
		counters = make(map[string]uint64, 8)
		c.state.Runtime.FilteredByType = counters
	}
	if _, exists := counters[key]; !exists && len(counters) >= maxFilteredByTypeEntries {
		key = filteredOverflowKey
	}
	counters[key]++
}
