package runtimeapi

import (
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

// buildSessionRuntimeReplayEventView 标记「回放帧」：runtime/stream 的初始 dump 与
// 断点补齐都从 EventStore 读，属于历史回放；总线直投的实时帧走
// buildSessionRuntimeLiveEventView（带 live:true）。两者互斥，客户端据此可以区分
// 「历史补齐」与「刚刚发生」，不必再用 isResponding 之类的状态猜测（Batch 4）。
//
// 注意：不回改 buildSessionRuntimeEventView —— 窗口读取（/runtime/events）与轨迹
// 恢复复用同一构造器，它们不是 runtime/stream 意义上的回放，语义不能混。
func buildSessionRuntimeReplayEventView(event runtimeevents.Event) map[string]interface{} {
	view := buildSessionRuntimeEventView(event)
	view["replay"] = true
	return view
}

func summarizeSingleRuntimeEventProvenance(event runtimeevents.Event) map[string]interface{} {
	summary := runtimeevents.ProvenanceView{
		ProfileResourceKinds: make(map[string]int),
	}
	runtimeevents.ApplyProvenanceEventForAPI(&summary, event)
	return buildProvenanceSummaryFromView(summary)
}

// runtimeEventBearsProvenance 判定事件是否可能携带 provenance 信号：
// 类型维度（注册表的 ProvenanceBearing：profile 注入 / recall）或载荷自带来源引用
// （source_refs / profile_source_refs，如 checkpoint_created 以来源反哺统计的事件）。
// 判据与 runtimeevents.applyProvenanceEvent 的读取口径对齐，避免漏掉承载事件；
// 类型清单收敛在 internal/events/contract.go（Batch 2），此处不再手写。
func runtimeEventBearsProvenance(event runtimeevents.Event) bool {
	if runtimeevents.IsProvenanceBearingEventType(event.Type) {
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
