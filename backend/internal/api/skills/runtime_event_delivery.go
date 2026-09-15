package skills

import (
	"strings"
	"sync"

	"github.com/wwsheng009/ai-agent-runtime/internal/runtimeobserve"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolprotocol"
)

// 运行时事件「交付通道」可观测（批次 20 / P0-2）。
//
// 背景：同一份运行时事实经四条独立路径抵达前端，每条路径各持一份手写白名单：
//
//	A 会话事件库落盘（shouldPersistRuntimeSessionEvent）——决定「实时 + 回放」是否可见；
//	B live-only SSE 旁路（isSessionLiveOnlyRuntimeEvent）——只放 2 类且不落盘；
//	C chat SSE 帧桥（live_tool_stream.go）——工具生命周期实时建行；
//	D 回合末尾巴帧（buildObservedToolEventPayloadsWithLive）——迟到但完整。
//
// 四条路径原先都不上报「未命中」，于是白名单漏掉一类事件时，前端的表现只是
// 「没有反应」，无法与「本来就没有事件」区分。本文件做两件事：
//
//  1. DeliveryChannelsFor：把「类型 → 通道」收敛成一处可测的判定，并复用
//     runtimeobserve 的已知类型目录区分「已知但被通道裁掉」与「完全未知」；
//  2. 计数：A 记丢弃（按原因 + 类型 + 已知性三分），B 记实际转发量，读侧经
//     runtimeStatusSnapshot 的 runtime_event_delivery 键暴露（不新增端点、
//     不改事件流本身）。
const (
	// A：落盘 ⇒ 实时（长轮询）与回放都能看到。
	runtimeEventDeliverySessionStore = "session_store"
	// B：仅实时转发，不落盘（刷新即丢）。
	runtimeEventDeliveryLiveOnly = "live_only"
	// C：chat SSE 帧桥（工具生命周期在对话流里实时建行）。
	runtimeEventDeliveryChatBridge = "chat_bridge"
	// D：仅回合末尾巴补发。
	runtimeEventDeliveryTailOnly = "tail_only"
)

// 丢弃原因（reason 维度）。
const (
	runtimeEventDeliveryNoSessionID      = "no_session_id"
	runtimeEventDeliveryTypeNotPersisted = "type_not_persisted"
)

// 计数上界与溢出桶：类型名来自运行时事件，属于外部输入，必须有界。
const (
	maxRuntimeEventDeliveryTypes = 64
	runtimeEventDeliveryOverflow = "_other"
)

// DeliveryChannelsFor 返回事件类型当前的 chat 侧交付通道集合（可多通道：例如
// tool.requested 既经 A 落盘、又经 C 实时下帧）。返回空集合表示该类型目前
// 没有任何对话侧交付路径——正是 P0-2 要求「未命中必须可观测」的那一类。
func DeliveryChannelsFor(eventType string) []string {
	normalized := strings.TrimSpace(eventType)
	channels := make([]string, 0, 2)
	if isPersistedRuntimeEventType(normalized) {
		channels = append(channels, runtimeEventDeliverySessionStore)
	}
	switch normalized {
	case toolprotocol.EventTypeProgress, supervision.EventTypeSubagentProgress:
		channels = append(channels, runtimeEventDeliveryLiveOnly)
	}
	switch normalized {
	case runtimeEventToolRequested, runtimeEventToolCompleted:
		channels = append(channels, runtimeEventDeliveryChatBridge)
	}
	switch normalized {
	// 子代理批量事件只在回合末经端点补发帧进入对话流（handler.go 收尾段），
	// 实时通道与事件库都没有它们。
	case "subagent.batch.started", "subagent.batch.completed",
		"subagent.started", "subagent.completed":
		channels = append(channels, runtimeEventDeliveryTailOnly)
	}
	return channels
}

type runtimeEventDeliveryCounters struct {
	mu sync.Mutex
	// droppedByReason：reason -> 类型 -> 计数（A 通道未命中）。
	droppedByReason map[string]map[string]uint64
	droppedKnown    uint64
	droppedUnknown  uint64
	// liveForwarded：类型 -> 计数（B 通道实际转发量）。
	liveForwarded map[string]uint64
	overflow      uint64
}

var runtimeEventDelivery = &runtimeEventDeliveryCounters{
	droppedByReason: make(map[string]map[string]uint64, 2),
	liveForwarded:   make(map[string]uint64, 4),
}

func (c *runtimeEventDeliveryCounters) recordDrop(reason, eventType string, known bool) {
	if c == nil {
		return
	}
	key := strings.TrimSpace(eventType)
	if key == "" {
		key = runtimeEventDeliveryOverflow
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if known {
		c.droppedKnown++
	} else {
		c.droppedUnknown++
	}
	counters := c.droppedByReason[reason]
	if counters == nil {
		counters = make(map[string]uint64, 8)
		c.droppedByReason[reason] = counters
	}
	if _, exists := counters[key]; !exists && len(counters) >= maxRuntimeEventDeliveryTypes {
		c.overflow++
		key = runtimeEventDeliveryOverflow
	}
	counters[key]++
}

func (c *runtimeEventDeliveryCounters) recordLiveForwarded(eventType string) {
	if c == nil {
		return
	}
	key := strings.TrimSpace(eventType)
	if key == "" {
		key = runtimeEventDeliveryOverflow
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.liveForwarded[key]; !exists && len(c.liveForwarded) >= maxRuntimeEventDeliveryTypes {
		c.overflow++
		key = runtimeEventDeliveryOverflow
	}
	c.liveForwarded[key]++
}

// recordRuntimeEventDeliveryDrop 由 A 通道（总线 → 会话事件库）在过滤掉事件时
// 调用。已知性与 runtimeobserve 的三分法同源：命中已知目录 ⇒ 该类型有产品语义，
// 只是被白名单裁掉；未命中 ⇒ 保持异常语义（可能是拼写错误或未登记的新事件）。
func recordRuntimeEventDeliveryDrop(eventType, sessionID string) {
	reason := runtimeEventDeliveryTypeNotPersisted
	if strings.TrimSpace(sessionID) == "" {
		reason = runtimeEventDeliveryNoSessionID
	}
	runtimeEventDelivery.recordDrop(reason, eventType, runtimeobserve.IsKnownEventType(eventType))
}

// recordRuntimeEventDeliveryLiveForwarded 由 B 通道（live-only 旁路）在真正把帧
// 写进订阅通道时调用。该通道不落盘，因此这里是「这类事件到底有没有实时送达」
// 的唯一正向证据。
func recordRuntimeEventDeliveryLiveForwarded(eventType string) {
	runtimeEventDelivery.recordLiveForwarded(eventType)
}

// SnapshotRuntimeEventDelivery 返回计数快照（runtimeStatusSnapshot 的
// runtime_event_delivery 键；JSON 友好，可直接序列化）。
func SnapshotRuntimeEventDelivery() map[string]interface{} {
	c := runtimeEventDelivery
	c.mu.Lock()
	defer c.mu.Unlock()

	droppedByReason := make(map[string]map[string]uint64, len(c.droppedByReason))
	for reason, counters := range c.droppedByReason {
		cloned := make(map[string]uint64, len(counters))
		for key, value := range counters {
			cloned[key] = value
		}
		droppedByReason[reason] = cloned
	}
	liveForwarded := make(map[string]uint64, len(c.liveForwarded))
	for key, value := range c.liveForwarded {
		liveForwarded[key] = value
	}

	return map[string]interface{}{
		"dropped_total":     c.droppedKnown + c.droppedUnknown,
		"dropped_known":     c.droppedKnown,
		"dropped_unknown":   c.droppedUnknown,
		"dropped_by_reason": droppedByReason,
		"live_forwarded":    liveForwarded,
		"overflow":          c.overflow,
		"channels": []string{
			runtimeEventDeliverySessionStore,
			runtimeEventDeliveryLiveOnly,
			runtimeEventDeliveryChatBridge,
			runtimeEventDeliveryTailOnly,
		},
	}
}

// resetRuntimeEventDeliveryCountersForTest 只供本包测试隔离使用。
func resetRuntimeEventDeliveryCountersForTest() {
	c := runtimeEventDelivery
	c.mu.Lock()
	defer c.mu.Unlock()
	c.droppedByReason = make(map[string]map[string]uint64, 2)
	c.liveForwarded = make(map[string]uint64, 4)
	c.droppedKnown = 0
	c.droppedUnknown = 0
	c.overflow = 0
}
