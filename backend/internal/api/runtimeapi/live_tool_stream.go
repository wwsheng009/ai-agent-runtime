package runtimeapi

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
)

// agent loop 发射的工具生命周期运行事件名（internal/agent/loop.go 的
// emitRuntimeEvent("tool.requested" / "tool.completed")）。与
// shouldPersistRuntimeSessionEvent 的过滤集合同名，但这里的用途不同：那边把
// 事件落进会话事件存储（tool_started/tool_finished 供回放），这里把它们实时
// 翻译成 chat SSE 帧，让「推理结束 → 工具执行」这段窗口在对话流里立刻可见。
const (
	runtimeEventToolRequested = "tool.requested"
	runtimeEventToolCompleted = "tool.completed"
)

// liveToolStreamTracker 记录本轮 ReAct 运行中已经实时下发过帧的工具调用。
//
// 键沿用观察记录的步标签：internal/agent/loop.go 的 observe() 用
// `step_%d_tool_%d`（循环步号 + 该步 toolResults 下标）给每条观测命名，回合末
// 的证据尾巴据此生成 `observation_step_N_tool_M` 形式的占位 id。追踪器在
// tool.requested 到达时按同一规则发号，于是：
//
//   - 实时帧用**真实 provider call id**（`call_00_...`）建行，前端的工具行
//     在工具真正开始执行时就出现；
//   - 回合末的证据尾巴按同一把键查到真实 id 并改写占位 id，前端按 id upsert
//     合并成同一行——既不重复建行，又能用观测里的完整 arguments/output 覆盖
//     实时帧的预览值（因此尾巴对已实时下发的工具只补一条 tool_end）。
type liveToolStreamTracker struct {
	mu sync.Mutex
	// ordinals 记录每个循环步里已发出的工具序号（0 基），与 observe() 的 i 对齐。
	ordinals map[int]int
	// idByObservationKey 把 `step_N_tool_M` 映射到实时帧使用的行 id。
	idByObservationKey map[string]string
	// nameByObservationKey 记录每个观测键对应的逻辑工具名：尾巴改写行 id 前用它
	// 做一次一致性校验（见 rowIDForObservationKey）。
	nameByObservationKey map[string]string
	// keyByProviderCallID 用于识别同一次调用的重复请求事件（例如走审批的
	// approved_tool.go 会再发一次 tool.requested），避免重复占号并把序号挤偏。
	keyByProviderCallID map[string]string
	// endedRowIDs 记录已经下发过 tool_end 的行，重复的完成事件不再重发帧。
	endedRowIDs map[string]struct{}
}

func newLiveToolStreamTracker() *liveToolStreamTracker {
	return &liveToolStreamTracker{
		ordinals:             make(map[int]int),
		idByObservationKey:   make(map[string]string),
		nameByObservationKey: make(map[string]string),
		keyByProviderCallID:  make(map[string]string),
		endedRowIDs:          make(map[string]struct{}),
	}
}

// markEnded 登记一次完成帧下发；false 表示该行的 tool_end 已经发过。
func (t *liveToolStreamTracker) markEnded(rowID string) bool {
	if t == nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, exists := t.endedRowIDs[rowID]; exists {
		return false
	}
	t.endedRowIDs[rowID] = struct{}{}
	return true
}

// recordRequest 登记一次工具调用并返回它在观测命名空间里的键与行 id。
// fresh=false 表示这次调用此前已经登记过（重复事件，调用方不应重复发帧）。
//
// preferredIndex 是事件自带的批量下标（并行调度器的 `batch_index`，与
// results[item.index] / observe() 的下标同源）；<0 表示事件没带。并行批次的事件
// 由 goroutine 发出，**到达顺序不可依赖**，因此序号优先取它，缺席或该位已被占用
// 时才退化为「本 step 的下一个空位」。
func (t *liveToolStreamTracker) recordRequest(providerCallID, toolName string, step, preferredIndex int) (key, rowID string, fresh bool) {
	if t == nil {
		return "", "", false
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	providerCallID = strings.TrimSpace(providerCallID)
	if providerCallID != "" {
		if existing, ok := t.keyByProviderCallID[providerCallID]; ok {
			return existing, t.idByObservationKey[existing], false
		}
	}

	index := t.ordinals[step]
	if preferredIndex >= 0 && !t.keyTakenLocked(step, preferredIndex) {
		index = preferredIndex
	}
	for t.keyTakenLocked(step, index) {
		index++
	}
	if index >= t.ordinals[step] {
		t.ordinals[step] = index + 1
	}

	key = observationKeyForStep(step, index)

	rowID = providerCallID
	if rowID == "" {
		// provider 未给真实 id 时退回占位 id：与证据尾巴的命名一致，
		// 前端仍能把两条通道合并成一行。
		rowID = "observation_" + key
	}
	t.idByObservationKey[key] = rowID
	if name := strings.TrimSpace(toolName); name != "" {
		t.nameByObservationKey[key] = name
	}
	if providerCallID != "" {
		t.keyByProviderCallID[providerCallID] = key
	}
	return key, rowID, true
}

// keyTakenLocked 判断某个观测键是否已经登记；调用方必须已持有 t.mu。
func (t *liveToolStreamTracker) keyTakenLocked(step, index int) bool {
	if index < 0 {
		return false
	}
	_, exists := t.idByObservationKey[observationKeyForStep(step, index)]
	return exists
}

// rowIDForObservationKey 返回观测键对应的行 id。
//
// ok=false 的两种情形：该工具没有实时下发过帧；或键上的工具名与观测工具名明显
// 不一致——序号一旦漂移（并行批次乱序、观测缺失等），按 id 改写会把完成帧的
// 输出合并到**别的工具行**上，这里宁可回退成修复前的三段式（多一行），也不做
// 错误合并。名字对不上时只影响呈现，不影响工具真实执行结果。
func (t *liveToolStreamTracker) rowIDForObservationKey(key, toolName string) (string, bool) {
	if t == nil {
		return "", false
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return "", false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	id, ok := t.idByObservationKey[key]
	if !ok {
		return "", false
	}
	if expected := t.nameByObservationKey[key]; expected != "" {
		if actual := strings.TrimSpace(toolName); actual != "" && !strings.EqualFold(actual, expected) {
			return "", false
		}
	}
	return id, true
}

// streamedCount 返回已实时下发的工具调用数（测试与日志用）。
func (t *liveToolStreamTracker) streamedCount() int {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.idByObservationKey)
}

// observationKeyForStep 复刻 internal/agent/loop.go observe() 的观测命名：
// step 为循环步号，index 为该步 toolResults 的下标（0 基）。
func observationKeyForStep(step, index int) string {
	return fmt.Sprintf("step_%d_tool_%d", step, index)
}

// isLiveToolLifecycleEvent 判定事件是否为「本轮次、本会话」的工具生命周期事件。
// sessionID 为空时退化为只按类型判定（无会话的临时运行）。
func isLiveToolLifecycleEvent(event runtimeevents.Event, sessionID string) bool {
	switch strings.TrimSpace(event.Type) {
	case runtimeEventToolRequested, runtimeEventToolCompleted:
	default:
		return false
	}
	if sessionID == "" {
		return true
	}
	return strings.TrimSpace(event.SessionID) == sessionID
}

// buildLiveToolFrames 把一条工具生命周期运行事件翻译成 chat SSE 帧。
//
// 载荷形状与回合末证据尾巴的 buildStaticToolEventPayload 完全一致（前端对两条
// 通道用同一个 upsert），差别只在 id：实时帧优先用真实 provider call id。
// tool.requested 展开为 tool_call + tool_start 两帧（与尾巴的三段式对齐），
// tool.completed 只发 tool_end。
func buildLiveToolFrames(event runtimeevents.Event, rowID string, index int) []staticToolEvent {
	payload := event.Payload
	if payload == nil {
		payload = map[string]interface{}{}
	}
	toolName := firstNonEmptyString(
		payloadStringValue(payload, "logical_tool"),
		payloadStringValue(payload, "tool_name"),
		strings.TrimSpace(event.ToolName),
	)

	switch strings.TrimSpace(event.Type) {
	case runtimeEventToolRequested:
		return []staticToolEvent{
			{Event: streamEventName(llm.EventTypeToolCall), Payload: buildLiveToolEventPayload(llm.EventTypeToolCall, rowID, toolName, payload, index)},
			{Event: streamEventName(llm.EventTypeToolStart), Payload: buildLiveToolEventPayload(llm.EventTypeToolStart, rowID, toolName, payload, index+1)},
		}
	case runtimeEventToolCompleted:
		return []staticToolEvent{
			{Event: streamEventName(llm.EventTypeToolEnd), Payload: buildLiveToolEventPayload(llm.EventTypeToolEnd, rowID, toolName, payload, index)},
		}
	default:
		return nil
	}
}

func buildLiveToolEventPayload(eventType llm.StreamEventType, rowID, toolName string, source map[string]interface{}, index int) map[string]interface{} {
	metadata := map[string]interface{}{"live": true}
	if step, ok := payloadIntValue(source, "step"); ok {
		metadata["step"] = step
	}
	if traceID := payloadStringValue(source, "trace_id"); traceID != "" {
		metadata["trace_id"] = traceID
	}
	providerCallID := payloadStringValue(source, "tool_call_id")
	if providerCallID != "" {
		// 真实 provider call id 始终随 metadata 下发：前端/离线分析要按模型
		// 原始 id 对齐 tool_started/tool_finished 时不必依赖行 id。
		metadata["provider_tool_call_id"] = providerCallID
	}
	if errMessage := payloadStringValue(source, "error"); errMessage != "" {
		metadata["error"] = errMessage
	}

	// 实时帧只带请求侧预览（arg_preview）与定位字段；完整 arguments/output 由
	// 回合末尾巴的 tool_end 覆盖（前端按 id upsert 合并）。
	content := ""
	if eventType == llm.EventTypeToolEnd {
		content = firstNonEmptyString(
			payloadStringValue(source, "summary"),
			payloadStringValue(source, "render_output"),
			payloadStringValue(source, "content"),
		)
	}
	argsValue := firstNonEmptyString(
		payloadStringValue(source, "arg_preview"),
		payloadStringValue(source, "command_text"),
	)

	toolCall := map[string]interface{}{
		"id":   rowID,
		"name": toolName,
	}
	if argsValue != "" {
		toolCall["arguments"] = argsValue
	}

	toolPayload := map[string]interface{}{
		"id":      rowID,
		"name":    toolName,
		"status":  string(eventType),
		"content": content,
	}
	if argsValue != "" {
		toolPayload["args"] = argsValue
	}
	for _, key := range []string{"directory", "file_path", "command_text", "arg_preview", "summary"} {
		if value, ok := source[key]; ok && value != nil {
			toolPayload[key] = value
		}
	}

	payload := map[string]interface{}{
		"index":     index + 1,
		"type":      string(eventType),
		"content":   content,
		"metadata":  metadata,
		"tool_call": toolCall,
		"tool":      toolPayload,
	}
	if eventType == llm.EventTypeToolCall {
		payload["delta"] = toolCall
	}
	attachToolEntity(payload, providerCallID)
	return payload
}

// attachToolEntity 把权威行身份随帧下发（P1-2，批次 20）。
//
// 前端此前只能从 `tool_call_id` 反推身份，且「没有 id」这件事只能靠 reducer
// 自己推断：于是一次调用的两次到达在降级数据下静默分成两行，没有任何信号说明
// 身份是猜的。这里在有**真实 provider call id** 时显式下发
// `entity: {kind:"tool", id:<call_id>}`；没有 id 时保持不下发——兜底身份由前端
// 按 seq 派生并标 `degraded`（frontend/src/lib/trajectory/entity-identity.ts）。
//
// 只增一个键，既有载荷形状不变（前端读侧对缺席完全兼容）。
func attachToolEntity(payload map[string]interface{}, callID string) {
	writeToolEntity(payload, callID, false)
}

// attachDegradedToolEntity 标记「身份是合成的」：id 在本地通道内自洽（同一工具
// 的多帧仍合并成一行），但与权威帧的真实 provider call id **无法合并**。典型
// 场景是回合末证据尾巴退回三段式时用的 `observation_<step>`（批次 18 的保守
// 退化）。把这件事写进载荷，前端才能把「同一次调用两行」显示成已知降级，
// 而不是让用户自己发现重复行。
func attachDegradedToolEntity(payload map[string]interface{}, callID string) {
	writeToolEntity(payload, callID, true)
}

func writeToolEntity(payload map[string]interface{}, callID string, degraded bool) {
	if payload == nil {
		return
	}
	trimmed := strings.TrimSpace(callID)
	if trimmed == "" {
		return
	}
	entity := map[string]interface{}{
		"kind": "tool",
		"id":   trimmed,
	}
	if degraded {
		entity["degraded"] = true
	}
	payload["entity"] = entity
}

// subscribeLiveToolStream 在 ReAct 运行期间把工具生命周期运行事件实时翻译成
// chat SSE 帧（tool_call/tool_start/tool_end），返回退订函数。
//
// emit 由调用方负责串行化（与 streamSink 共用同一把锁），因为总线派发在独立
// goroutine 上执行，而 SSE 写入/持久化不可并发。
func subscribeLiveToolStream(
	bus *runtimeevents.Bus,
	execSessionID string,
	tracker *liveToolStreamTracker,
	emit func(eventName string, payload map[string]interface{}),
) func() {
	if bus == nil || tracker == nil || emit == nil {
		return func() {}
	}
	index := 0
	return bus.SubscribeCancelable("", func(event runtimeevents.Event) {
		if !isLiveToolLifecycleEvent(event, execSessionID) {
			return
		}
		providerCallID := payloadStringValue(event.Payload, "tool_call_id")
		logicalTool := firstNonEmptyString(
			payloadStringValue(event.Payload, "logical_tool"),
			payloadStringValue(event.Payload, "tool_name"),
			strings.TrimSpace(event.ToolName),
		)
		step, _ := payloadIntValue(event.Payload, "step")
		// 并行调度器的 tool.requested 带 batch_index（= results/toolResults 下标）：
		// 事件在各自 goroutine 里发出，到达顺序不可依赖，按它发号才能与 observe()
		// 的 `step_%d_tool_%d` 对齐。
		batchIndex := -1
		if parsed, ok := payloadIntValue(event.Payload, "batch_index"); ok {
			batchIndex = parsed
		}
		_, rowID, fresh := tracker.recordRequest(providerCallID, logicalTool, step, batchIndex)
		switch strings.TrimSpace(event.Type) {
		case runtimeEventToolRequested:
			// 同一次调用的重复请求事件（审批回执等）不重发帧：重复占号会把
			// 后续工具的观测键整段挤偏，导致尾巴改写错行。
			if !fresh {
				return
			}
		case runtimeEventToolCompleted:
			// 完成帧对同一次调用是另一种事件：即便请求侧此前已登记（fresh=false）
			// 也必须下发；只对重复的完成事件去重。
			if !tracker.markEnded(rowID) {
				return
			}
		}
		for _, frame := range buildLiveToolFrames(event, rowID, index) {
			index++
			emit(frame.Event, frame.Payload)
		}
	})
}

func payloadStringValue(payload map[string]interface{}, key string) string {
	if payload == nil {
		return ""
	}
	switch value := payload[key].(type) {
	case string:
		return strings.TrimSpace(value)
	case []byte:
		return strings.TrimSpace(string(value))
	case nil:
		return ""
	default:
		return ""
	}
}

func payloadIntValue(payload map[string]interface{}, key string) (int, bool) {
	if payload == nil {
		return 0, false
	}
	switch value := payload[key].(type) {
	case int:
		return value, true
	case int32:
		return int(value), true
	case int64:
		return int(value), true
	case float64:
		return int(value), true
	case json.Number:
		if parsed, err := value.Int64(); err == nil {
			return int(parsed), true
		}
	}
	return 0, false
}
