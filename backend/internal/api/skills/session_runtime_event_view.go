package skills

import (
	"strings"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

func buildSessionRuntimeEventView(event runtimeevents.Event) map[string]interface{} {
	view := map[string]interface{}{
		"type":       event.Type,
		"trace_id":   event.TraceID,
		"agent_name": event.AgentName,
		"session_id": event.SessionID,
		"tool_name":  event.ToolName,
		"payload":    event.Payload,
		"timestamp":  event.Timestamp,
	}
	// Batch 1（P1-1）：provenance 只在承载事件上下发，且省略零值字段。
	// 此前每帧无条件计算并下发 8 键（单帧约 215B 字面量），而实测绝大多数事件
	// 与 provenance 无关；该字段不落盘，纯属每次传输的重复计算与重复编码。
	if provenance, ok := summarizeRuntimeEventProvenanceIfBearing(event); ok {
		view["provenance"] = provenance
	}
	return view
}

func buildSessionRuntimeEventViews(events []runtimeevents.Event) []map[string]interface{} {
	views := make([]map[string]interface{}, 0, len(events))
	for _, event := range events {
		views = append(views, buildSessionRuntimeEventView(event))
	}
	return views
}

func summarizeSingleRuntimeEventProvenance(event runtimeevents.Event) map[string]interface{} {
	summary := runtimeevents.ProvenanceView{
		ProfileResourceKinds: make(map[string]int),
	}
	runtimeevents.ApplyProvenanceEventForAPI(&summary, event)
	return buildProvenanceSummaryFromView(summary)
}

// runtimeEventBearsProvenance 判定事件是否可能携带 provenance 信号：
// 类型白名单（profile 注入 / recall）或载荷自带来源引用（source_refs /
// profile_source_refs，如 checkpoint_created 以来源反哺统计的事件）。
// 判据与 runtimeevents.applyProvenanceEvent 的读取口径对齐，避免漏掉承载事件。
func runtimeEventBearsProvenance(event runtimeevents.Event) bool {
	switch strings.TrimSpace(event.Type) {
	case "context.profile.injected", "recall.performed":
		return true
	}
	for _, key := range []string{"source_refs", "profile_source_refs"} {
		if _, ok := event.Payload[key]; ok {
			return true
		}
	}
	return false
}

// summarizeRuntimeEventProvenanceIfBearing 只在事件承载 provenance 时计算摘要，
// 并省略全零结果（ok=false 表示该帧不带 provenance 字段）。
func summarizeRuntimeEventProvenanceIfBearing(event runtimeevents.Event) (map[string]interface{}, bool) {
	if !runtimeEventBearsProvenance(event) {
		return nil, false
	}
	summary := summarizeSingleRuntimeEventProvenance(event)
	for key, value := range summary {
		if provenanceValueIsZero(value) {
			delete(summary, key)
		}
	}
	if len(summary) == 0 {
		return nil, false
	}
	return summary, true
}

// provenanceValueIsZero 判定 provenance 摘要中的零值（0 / 空切片 / 空 map），
// 用于在 wire 上省略无信息字段。
func provenanceValueIsZero(value interface{}) bool {
	switch typed := value.(type) {
	case int:
		return typed == 0
	case []string:
		return len(typed) == 0
	case map[string]int:
		return len(typed) == 0
	default:
		return value == nil
	}
}

