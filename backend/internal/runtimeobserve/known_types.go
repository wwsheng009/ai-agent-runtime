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
//     tool.reduced（internal/events/bus.go:1088 统计；agent emitRuntimeEvent）。
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
		"tool.completed", // internal/agent/loop.go:2136 等；agent_stdio_bridge.go:224 ≡ tool_finished
		"tool.requested", // internal/agent/loop.go emitRuntimeEvent
		"tool.reduced",   // internal/events/bus.go:1088；agent emitRuntimeEvent
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

// maxFilteredByTypeEntries 是 filtered_by_type 的内部计数上界（top-N 兜底）。
// 目录是封闭集合（本文来源，62 项），正常永远触发不到；此上限只作为
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
