package encoding

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// 单流 delta 乱序重排缓冲上限（与旧终端 orderAssistantDelta 同规格）：
// 超过上限后该流标记 tainted，丢弃后续乱序 delta（对齐既有生产行为）。
const (
	assistantStreamPendingLimit     = 128
	assistantStreamPendingByteLimit = 1 << 20
)

// EventEncoder 是统一渲染编码器，对应 Codex ThreadHistoryBuilder：
// 所有上游事件经 Encode 转换为 RenderModel 的 append/upsert/remove 操作。
//
// 并发：非线程安全。由事件消费侧（chatRuntimeEventBridge 的单 goroutine
// 事件循环）独占调用。
type EventEncoder struct {
	model        *RenderModel
	nextItemID   uint64 // item-{n} 单调分配
	nextSeq      uint64 // 提交序号单调分配
	clock        uint64 // 编码器时钟（每事件 +1）
	revisions    map[string]uint64
	assistantBy  map[string]*Item                 // streamKey(turnID/streamID) -> 当前 assistant item
	reasoningBy  map[string]*Item                 // streamKey -> 当前 reasoning item（独立于 assistant）
	toolByID     map[string]*Item                 // payload tool_call_id -> tool_call item
	toolOutputBy map[string]map[string]struct{}   // callID -> 已提交 output 文本（幂等）
	priorityBy   map[string]*priorityPromptState  // approval/question request key -> delayed transcript state
	streamOrder  map[string]*assistantStreamOrder // streamKey -> delta 有序提交状态
	stats        Stats
}

// assistantStreamOrder 维护单条 assistant 流的 delta 有序提交状态
// （对齐旧终端 orderAssistantDelta：sequence 从 1 开始，乱序缓存，
// 连续补拼；超限 tainted 丢弃后续）。
type assistantStreamOrder struct {
	nextSeq     uint64
	pending     map[uint64]string
	pendingText int
	tainted     bool
}

// priorityPromptState tracks the synchronous interaction identity separately
// from retained transcript items. A request is not durable terminal content:
// only the completed prompt transcript may enter RenderModel, at the position
// where the legacy history block is actually committed.
type priorityPromptState struct {
	requested bool
	resolved  bool
	item      *Item
}

// NewEventEncoder 创建空编码器。
func NewEventEncoder() *EventEncoder {
	return &EventEncoder{
		model:        &RenderModel{},
		revisions:    make(map[string]uint64),
		assistantBy:  make(map[string]*Item),
		reasoningBy:  make(map[string]*Item),
		toolByID:     make(map[string]*Item),
		toolOutputBy: make(map[string]map[string]struct{}),
		priorityBy:   make(map[string]*priorityPromptState),
		streamOrder:  make(map[string]*assistantStreamOrder),
	}
}

// Encode 处理单个上游事件，返回增量变更集。事件类型未映射时按
// KindSystem append 兜底（不丢信息），并计入 UnknownCount。
func (e *EventEncoder) Encode(ev runtimeevents.Event) *ChangeSet {
	if e == nil {
		return nil
	}
	e.clock++
	e.stats.EncodeCount++
	cs := &ChangeSet{}
	e.apply(e.classify(ev), ev, cs)
	if e.model.Tail == nil {
		e.model.Tail = &Tail{}
	}
	if len(e.model.Items) > 0 {
		last := e.model.Items[len(e.model.Items)-1]
		e.model.Tail.ItemID = last.ID
		e.model.Tail.Seq = last.Seq
	} else {
		e.model.Tail.ItemID = ""
		e.model.Tail.Seq = 0
	}
	cs.Tail = e.model.Tail
	return cs
}

// SubmitUserInput 把用户输入消息提交为终态 user 块（KindUser），返回
// 增量变更集。用户输入没有 runtime 事件类型（事件流无 user 事件，见
// 切片 10 注记），由渲染层在 coordinator 用户输入提交点直连注入，作为
// 会话 transcript 的数据面内容；与 P4 交互输出锚点机制无关（/debug、
// /model 等交互输出仍只以 Tail 为锚点，不进入编码器因果链）。
//
// 语义与 Encode 对齐：时钟递增、统计计入、Tail 更新；块为一次性终态
// （StatusCompleted，append 即终态提交，INV-SCENE-04），与切片 7 parity
// 基线（手工构造的 KindUser completed item）一致。
func (e *EventEncoder) SubmitUserInput(text string) *ChangeSet {
	if e == nil {
		return nil
	}
	e.clock++
	e.stats.EncodeCount++
	cs := &ChangeSet{}
	it := e.appendItem(KindUser, "", text)
	it.Status = StatusCompleted
	e.change(cs, OpAppend, it)
	if e.model.Tail == nil {
		e.model.Tail = &Tail{}
	}
	if len(e.model.Items) > 0 {
		last := e.model.Items[len(e.model.Items)-1]
		e.model.Tail.ItemID = last.ID
		e.model.Tail.Seq = last.Seq
	} else {
		e.model.Tail.ItemID = ""
		e.model.Tail.Seq = 0
	}
	cs.Tail = e.model.Tail
	return cs
}

// SubmitAssistant 把没有 runtime assistant 终态事件的本地最终回复提交为
// completed assistant 块。正常 runtime assistant 仍必须经 Encode，调用方
// 不得把同一回复同时走两个入口。
func (e *EventEncoder) SubmitAssistant(text string) *ChangeSet {
	if e == nil {
		return nil
	}
	e.clock++
	e.stats.EncodeCount++
	cs := &ChangeSet{}
	it := e.appendItem(KindAssistant, "", text)
	it.Status = StatusCompleted
	e.change(cs, OpAppend, it)
	if e.model.Tail == nil {
		e.model.Tail = &Tail{}
	}
	if len(e.model.Items) > 0 {
		last := e.model.Items[len(e.model.Items)-1]
		e.model.Tail.ItemID = last.ID
		e.model.Tail.Seq = last.Seq
	} else {
		e.model.Tail.ItemID = ""
		e.model.Tail.Seq = 0
	}
	cs.Tail = e.model.Tail
	return cs
}

// SubmitCommand 把本地命令执行结果提交为终态 command 块（KindCommand）。
// 命令执行没有 runtime 事件类型（与用户输入同理，见 SubmitUserInput 注记；
// 设计文档 §1.3 行 9/10），由渲染层在命令结果 cell 提交点直连注入，作为
// 会话 transcript 的数据面内容。块为一次性终态（StatusCompleted，append
// 即终态提交），与切片 7 parity 基线一致。
func (e *EventEncoder) SubmitCommand(text string) *ChangeSet {
	if e == nil {
		return nil
	}
	e.clock++
	e.stats.EncodeCount++
	cs := &ChangeSet{}
	it := e.appendItem(KindCommand, "", text)
	it.Status = StatusCompleted
	e.change(cs, OpAppend, it)
	if e.model.Tail == nil {
		e.model.Tail = &Tail{}
	}
	if len(e.model.Items) > 0 {
		last := e.model.Items[len(e.model.Items)-1]
		e.model.Tail.ItemID = last.ID
		e.model.Tail.Seq = last.Seq
	} else {
		e.model.Tail.ItemID = ""
		e.model.Tail.Seq = 0
	}
	cs.Tail = e.model.Tail
	return cs
}

// SubmitError 把操作错误提交为终态 system 块（KindSystem：会话/诊断事件，
// 设计文档 §1.3 行 11）。本地命令/工具错误没有 runtime 事件类型，由渲染
// 层在错误块提交点直连注入；assistant 流内错误仍走 EventAssistantMessage
// 终态路径（行 4），不在此 API 范围。
func (e *EventEncoder) SubmitError(text string) *ChangeSet {
	if e == nil {
		return nil
	}
	e.clock++
	e.stats.EncodeCount++
	cs := &ChangeSet{}
	it := e.appendItem(KindSystem, "", text)
	it.Status = StatusCompleted
	e.change(cs, OpAppend, it)
	if e.model.Tail == nil {
		e.model.Tail = &Tail{}
	}
	if len(e.model.Items) > 0 {
		last := e.model.Items[len(e.model.Items)-1]
		e.model.Tail.ItemID = last.ID
		e.model.Tail.Seq = last.Seq
	} else {
		e.model.Tail.ItemID = ""
		e.model.Tail.Seq = 0
	}
	cs.Tail = e.model.Tail
	return cs
}

// SubmitSupplement 把没有对应 runtime 事件的本地补充提交为独立终态
// supplement 块。它与 RenderAsyncLine 的 legacy surface 投影一一对应，
// 但不冒充 system/error 语义；调用方必须只在该内容尚未由 Encode 映射时
// 使用，避免 runtime event + direct supplement 双重注入。
func (e *EventEncoder) SubmitSupplement(text string) *ChangeSet {
	if e == nil {
		return nil
	}
	e.clock++
	e.stats.EncodeCount++
	cs := &ChangeSet{}
	it := e.appendItem(KindSupplement, "", text)
	it.Status = StatusCompleted
	e.change(cs, OpAppend, it)
	if e.model.Tail == nil {
		e.model.Tail = &Tail{}
	}
	if len(e.model.Items) > 0 {
		last := e.model.Items[len(e.model.Items)-1]
		e.model.Tail.ItemID = last.ID
		e.model.Tail.Seq = last.Seq
	} else {
		e.model.Tail.ItemID = ""
		e.model.Tail.Seq = 0
	}
	cs.Tail = e.model.Tail
	return cs
}

// SubmitPriorityPromptTranscript commits the one retained semantic item for a
// previously observed approval/question request. The request itself is only
// pending interaction identity and never occupies a RenderModel position.
// This makes the completed transcript appear after any local hint that was
// physically committed while stdin was pending. A resolved event may arrive
// first; it records lifecycle state but does not discard a late transcript.
func (e *EventEncoder) SubmitPriorityPromptTranscript(eventType, requestKey, text string) *ChangeSet {
	if e == nil || strings.TrimSpace(requestKey) == "" || strings.TrimSpace(text) == "" {
		return nil
	}
	key := PriorityPromptKey(eventType, requestKey)
	if key == "" {
		return nil
	}
	state := e.priorityBy[key]
	if state == nil || !state.requested || state.item != nil {
		if state != nil && state.item != nil {
			e.stats.DuplicateCount++
		}
		return nil
	}

	e.clock++
	e.stats.EncodeCount++
	cs := &ChangeSet{}
	it := e.appendItem(KindPriorityPrompt, "", text)
	it.Status = StatusCompleted
	state.item = it
	e.change(cs, OpAppend, it)
	if e.model.Tail == nil {
		e.model.Tail = &Tail{}
	}
	e.model.Tail.ItemID = it.ID
	e.model.Tail.Seq = it.Seq
	cs.Tail = e.model.Tail
	return cs
}

// SubmitToolCall 提交一个没有 runtime 事件外层包装的工具请求。
//
// chat-core 的 legacy tool loop 直接产生 ChatEvent；调用方仍必须提供
// 稳定 toolCallID，才能让请求、进度和结果归并到同一个 mutable tool cell。
// 该 helper 与 Encode(runtimeevents.Event{Type:"tool.requested"}) 完全同义，
// 只是把 direct producer 的语义约束收口在编码器 API 内。
func (e *EventEncoder) SubmitToolCall(toolCallID, toolName string, args map[string]interface{}) *ChangeSet {
	if e == nil || strings.TrimSpace(toolCallID) == "" || strings.TrimSpace(toolName) == "" {
		return nil
	}
	payload := make(map[string]interface{}, len(args)+2)
	for key, value := range args {
		if strings.TrimSpace(key) == "" {
			continue
		}
		payload[key] = value
	}
	// Call identity and readable name are protocol-owned fields: direct
	// argument maps must never override them, otherwise live/replay could map
	// the same tool result to different chains.
	payload["tool_call_id"] = strings.TrimSpace(toolCallID)
	payload["tool_name"] = strings.TrimSpace(toolName)
	return e.Encode(runtimeevents.Event{Type: "tool.requested", ToolName: toolName, Payload: payload})
}

// SubmitToolProgress 更新一个 direct tool call 的 source。没有稳定身份或
// 未知 call 时编码器会按既有 legacy 规则降级为 system，避免错误绑定。
func (e *EventEncoder) SubmitToolProgress(toolCallID, toolName, progress string) *ChangeSet {
	if e == nil || strings.TrimSpace(toolCallID) == "" || strings.TrimSpace(progress) == "" {
		return nil
	}
	return e.Encode(runtimeevents.Event{
		Type:     "tool.progress",
		ToolName: toolName,
		Payload: map[string]interface{}{
			"tool_call_id": strings.TrimSpace(toolCallID),
			"tool_name":    strings.TrimSpace(toolName),
			"progress":     progress,
		},
	})
}

// SubmitToolResult 提交 direct tool call 的唯一终态结果。output/error 会
// 由同一 tool-chain cell 合并；重复同文本结果由编码器幂等去重。
func (e *EventEncoder) SubmitToolResult(toolCallID, toolName, output, toolErr string, success bool) *ChangeSet {
	if e == nil || strings.TrimSpace(toolCallID) == "" {
		return nil
	}
	payload := map[string]interface{}{
		"tool_call_id": strings.TrimSpace(toolCallID),
		"tool_name":    strings.TrimSpace(toolName),
		"success":      success,
	}
	if strings.TrimSpace(output) != "" {
		payload["output"] = output
	}
	if strings.TrimSpace(toolErr) != "" {
		payload["error"] = toolErr
	}
	typeName := "tool.completed"
	if !success {
		typeName = "tool.failed"
	}
	return e.Encode(runtimeevents.Event{Type: typeName, ToolName: toolName, Payload: payload})
}

// SubmitToolResultDisplay finalizes a direct tool chain from its canonical
// transcript projection. Some legacy chat-core tools only expose a completed
// display block rather than a structured raw result. Keeping that one display
// head on the tool-call item preserves exact Scene/legacy/replay parity without
// pretending that it is a separate supplement cell.
func (e *EventEncoder) SubmitToolResultDisplay(toolCallID, display string) *ChangeSet {
	if e == nil || strings.TrimSpace(toolCallID) == "" || strings.TrimSpace(display) == "" {
		return nil
	}
	return e.Encode(runtimeevents.Event{
		Type: "tool.completed",
		Payload: map[string]interface{}{
			"tool_call_id": strings.TrimSpace(toolCallID),
			"display_head": display,
		},
	})
}

// SubmitUserInteraction 把 /debug、/model 等用户交互输出提交为终态
// user_interaction 块（KindUserInteraction，设计文档 §1.3 行 12）。
//
// 与 SubmitUserInput 的差异：交互输出不进入编码器因果链（不分配 CauseID），
// 以触发时刻捕获的模型尾部锚点（anchor *Tail，见 render-model-spec §1.4/
// §2）为界插入渲染序列——插入到 anchor.ItemID 之后而非模型末尾；模型在
// 触发时刻与提交时刻之间继续增长不影响输出位置。锚点为空或指向已不存在
// 的 Item 时退化为 append 到末尾（§4.1 幂等哲学：目标缺失退化为 append）。
//
// 交互输出不推进 Tail（Tail 由正常事件与用户/命令/错误注入推进，交互块
// 不参与因果链）；cs.Tail 返回插入前的模型尾部。
func (e *EventEncoder) SubmitUserInteraction(text string, anchor *Tail) *ChangeSet {
	if e == nil {
		return nil
	}
	e.clock++
	e.stats.EncodeCount++
	cs := &ChangeSet{}
	it, afterID := e.insertItemAfter(anchor, KindUserInteraction, text)
	it.Status = StatusCompleted
	e.changeAfter(cs, OpAppend, it, afterID)
	cs.Tail = e.model.Tail
	return cs
}

// Snapshot 返回当前模型深拷贝（渲染层只读消费）。
func (e *EventEncoder) Snapshot() *RenderModel {
	if e == nil {
		return nil
	}
	return e.model.Clone()
}

// Tail 返回当前模型尾部指针（用户交互锚点）。
func (e *EventEncoder) Tail() *Tail {
	if e == nil || e.model == nil || e.model.Tail == nil {
		return nil
	}
	t := *e.model.Tail
	return &t
}

// Stats 返回运行统计（双跑模式审计）。
func (e *EventEncoder) Stats() Stats {
	if e == nil {
		return Stats{}
	}
	return e.stats
}

// Replay 按给定顺序重放事件，幂等重建模型（等价
// build_turns_from_rollout_items）。重放前模型必须为空或调用 Reset。
func (e *EventEncoder) Replay(events []runtimeevents.Event) (*RenderModel, error) {
	if e == nil {
		return nil, fmt.Errorf("nil encoder")
	}
	for _, ev := range events {
		e.Encode(ev)
	}
	return e.Snapshot(), nil
}

// Reset 清空模型与全部状态（重放前调用）。
func (e *EventEncoder) Reset() {
	if e == nil {
		return
	}
	e.model = &RenderModel{}
	e.nextItemID = 0
	e.nextSeq = 0
	e.clock = 0
	e.revisions = make(map[string]uint64)
	e.assistantBy = make(map[string]*Item)
	e.reasoningBy = make(map[string]*Item)
	e.toolByID = make(map[string]*Item)
	e.toolOutputBy = make(map[string]map[string]struct{})
	e.priorityBy = make(map[string]*priorityPromptState)
	e.streamOrder = make(map[string]*assistantStreamOrder)
	e.stats = Stats{}
}

// classify 把事件类型映射为编码操作（append/upsert/remove），并解析出
// 目标 Item 的身份与内容。对应设计文档 §3 事件→操作映射表。
func (e *EventEncoder) classify(ev runtimeevents.Event) op {
	// A terminal session event is intentionally visually silent, but it is not
	// semantically inert: a provider failure, context cancellation, or user
	// interrupt can end a run without an assistant.message / llm.finished.
	// Close every open streamed item in place so the Scene never retains a
	// mutable tail after the owning run is gone.
	if ev.Type == runtimechat.EventSessionEnd || ev.Type == runtimechat.EventSessionInterrupted {
		return opSessionEnd
	}
	if isSilentSystemEventType(ev.Type) {
		return opNone
	}
	switch ev.Type {
	case runtimechat.EventSessionCompactStarted,
		runtimechat.EventSessionCompactCompleted,
		runtimechat.EventSessionCompactFailed:
		return opSystem

	case runtimechat.EventApprovalRequested, runtimechat.EventQuestionAsked:
		return opPriorityRequested

	case runtimechat.EventApprovalResolved, runtimechat.EventQuestionAnswered:
		return opPriorityResolved

	case runtimechat.EventAssistantReasoning, "assistant.reasoning":
		// The local ReAct loop emits the dotted event name while the chat
		// actor emits EventAssistantReasoning. Both carry the same typed
		// reasoning payload and must produce one KindReasoning item rather
		// than a fallback system row containing only "assistant.reasoning".
		return opReasoning

	case runtimechat.EventAssistantDelta:
		return opAssistantDelta

	case runtimechat.EventAssistantMessage:
		return opAssistantFinal

	case runtimechat.EventLLMRequestStarted:
		return opLLMStarted

	case runtimechat.EventLLMRequestFinished:
		return opLLMFinished

	case runtimechat.EventToolStarted,
		runtimechat.EventToolReceiptRecorded:
		return opToolStarted

	case runtimechat.EventToolFinished:
		return opToolFinished

	// Legacy runtime tool events carry the same call identity as the typed
	// chatcore events. When that identity is present they can therefore share
	// the mutable tool-cell lifecycle instead of becoming unrelated system
	// cells. The apply methods retain a system fallback for incomplete legacy
	// payloads so information is never silently discarded.
	case "tool.requested":
		return opToolStarted
	case "tool.progress":
		return opToolProgress
	case "tool.completed", "tool.failed", "tool.cancelled", "tool.canceled":
		return opToolFinished

	case runtimechat.EventCheckpointCreated,
		runtimechat.EventRewindStarted,
		runtimechat.EventRewindFinished,
		runtimechat.EventBacktrackStarted,
		runtimechat.EventBacktrackFinished,
		runtimechat.EventJobStarted,
		runtimechat.EventJobOutput,
		runtimechat.EventJobCancelled,
		runtimechat.EventJobFinished,
		runtimechat.EventMailboxReceived,
		runtimechat.EventToolReceiptReplayed:
		return opSystem

	default:
		if isKnownLegacyEventType(ev.Type) {
			return opSystem
		}
		e.stats.UnknownCount++
		return opSystem
	}
}

// knownLegacyEventTypePrefixes 是 agent/skills 层直接 emit、未经 chatcore
// 类型转换的事件类型前缀（如 tool.requested、subagent.started、llm.retry、
// team.task.completed、context.preflight.started、patch.applied）。这些事件
// 经事件总线全量进入编码器，作为已知呈现事件参与渲染模型，不属"未知类型"。
//
// 大多数 legacy 事件仍归 system；带稳定 tool_call_id 的 tool.requested /
// tool.progress / tool.completed（以及失败、取消变体）例外，按与 chatcore
// tool_started/tool_finished 相同的 mutable tool-cell 生命周期编码。缺少
// call identity 的 legacy tool 事件仍由 applyTool* 保守降级为 system，避免
// 把不完整事件绑定到错误工具或与 typed 事件产生重复链。
var knownLegacyEventTypePrefixes = []string{
	"assistant.",
	"context.preflight.",
	"llm.",
	"patch.",
	"planning.",
	"response.",
	"subagent.",
	"team.",
	"tool.",
}

// isKnownLegacyEventType 判断事件类型是否属于已知 legacy 前缀族。
func isKnownLegacyEventType(eventType string) bool {
	for _, prefix := range knownLegacyEventTypePrefixes {
		if strings.HasPrefix(eventType, prefix) {
			return true
		}
	}
	return false
}

// isSilentSystemEventType 判断事件是否属于"仅内部生命周期/遥测"类型。
// 旧终端 timeline 渲染器对这些事件返回空行（或 DebugOnly 默认隐藏），
// 可见 transcript 从不显示它们；Scene 投影若为它们创建 system cell，会
// 以 "❌ <type>" 形式把内部事件泄漏到可见输出（真实会话中的
// session_start / session_compact_skipped / llm.request.started /
// context.tool_schema.frozen 等）。编码器对静默事件不产生任何 Item 与
// 变更（EncodeCount 仍计入：事件被消费；UnknownCount 不计：属已知类型），
// 与旧路径可见行为严格一致（checkTextParity 亦依赖该对齐）。
//
// 注意：llm.retry / llm.request.finished / response.* 等在旧路径有（或
// 部分有）可见 timeline 输出，不在此列；其 Scene 内容对齐属 P3 映射项。
func isSilentSystemEventType(eventType string) bool {
	switch eventType {
	case runtimechat.EventSessionStart,
		runtimechat.EventSessionEnd,
		runtimechat.EventSessionInterrupted,
		runtimechat.EventSessionCompactSkipped,
		runtimechat.EventContextReconciled,
		"llm.request.started",
		"planning.started",
		"subagent.batch.started",
		"subagent.started",
		"task.started",
		"team.task.started",
		"context.tool_schema.frozen":
		return true
	}
	return false
}

// op 是 classify 的内部操作枚举。
type op int

const (
	opNone              op = iota // 静默事件：不产生任何变更（内部生命周期/遥测）
	opSystem                      // 系统/会话事件：append 独立 system 块
	opPriorityRequested           // approval/question：登记 pending prompt identity，不产生 Scene item
	opPriorityResolved            // approval/question 终态：记录 resolution，保留晚到 transcript identity
	opReasoning                   // reasoning：append/upsert 当前 assistant 下
	opAssistantDelta              // assistant delta：upsert 当前流
	opAssistantFinal              // assistant 完成：upsert 终态
	opLLMStarted                  // LLM 请求开始：append assistant pending
	opLLMFinished                 // LLM 请求结束：upsert 终态
	opSessionEnd                  // 会话终止：收尾所有未完成的流式项（不产生 system 行）
	opToolStarted                 // 工具调用发起：append tool_call（分配 CauseID）
	opToolProgress                // 工具运行进度：upsert 同一 mutable tool_call
	opToolFinished                // 工具完成：upsert tool_call 终态 + append tool_output
)

// apply 执行一次编码操作。
func (e *EventEncoder) apply(o op, ev runtimeevents.Event, cs *ChangeSet) {
	switch o {
	case opNone:
		// 静默：内部生命周期/遥测事件不产生变更（旧路径同样零可见输出）。
	case opSystem:
		e.applySystem(ev, cs)
	case opPriorityRequested:
		e.applyPriorityRequested(ev, cs)
	case opPriorityResolved:
		e.applyPriorityResolved(ev, cs)
	case opReasoning:
		e.applyReasoning(ev, cs)
	case opAssistantDelta:
		e.applyAssistantDelta(ev, cs)
	case opAssistantFinal:
		e.applyAssistantFinal(ev, cs)
	case opLLMStarted:
		e.applyLLMStarted(ev, cs)
	case opLLMFinished:
		e.applyLLMFinished(ev, cs)
	case opSessionEnd:
		e.applySessionEnd(ev, cs)
	case opToolStarted:
		e.applyToolStarted(ev, cs)
	case opToolProgress:
		e.applyToolProgress(ev, cs)
	case opToolFinished:
		e.applyToolFinished(ev, cs)
	}
}

func (e *EventEncoder) nextID() string {
	e.nextItemID++
	return fmt.Sprintf("item-%d", e.nextItemID)
}

func (e *EventEncoder) nextSeqValue() uint64 {
	e.nextSeq++
	return e.nextSeq
}

func (e *EventEncoder) updateTail(cs *ChangeSet) {
	if e == nil || cs == nil {
		return
	}
	if e.model.Tail == nil {
		e.model.Tail = &Tail{}
	}
	if len(e.model.Items) > 0 {
		last := e.model.Items[len(e.model.Items)-1]
		e.model.Tail.ItemID = last.ID
		e.model.Tail.Seq = last.Seq
	} else {
		e.model.Tail.ItemID = ""
		e.model.Tail.Seq = 0
	}
	cs.Tail = e.model.Tail
}

// appendItem 追加新 Item 并记录变更。
func (e *EventEncoder) appendItem(kind ItemKind, causeID, head string) *Item {
	it := &Item{
		ID:      e.nextID(),
		Seq:     e.nextSeqValue(),
		Kind:    kind,
		CauseID: causeID,
		Status:  StatusPending,
		Head:    head,
		Created: e.clock,
		Updated: e.clock,
	}
	e.model.Items = append(e.model.Items, it)
	e.revisions[it.ID] = 1
	e.stats.AppendCount++
	return it
}

// insertItemAfter 构造新 Item 并按锚点插入（Tail 锚定语义，见
// SubmitUserInteraction）。anchor 非空且其 ItemID 存在于模型中时插入到该
// Item 之后，返回 (item, afterID)；否则退化为 append 到末尾，返回
// (item, "")。Seq 仍为单调创建序号（渲染顺序 = 模型数组顺序，Seq 只作
// 幂等/审计标识，不要求与数组位置一致）。
func (e *EventEncoder) insertItemAfter(anchor *Tail, kind ItemKind, head string) (*Item, string) {
	it := &Item{
		ID:      e.nextID(),
		Seq:     e.nextSeqValue(),
		Kind:    kind,
		Status:  StatusPending,
		Head:    head,
		Created: e.clock,
		Updated: e.clock,
	}
	if anchor != nil && anchor.ItemID != "" {
		for i, existing := range e.model.Items {
			if existing.ID == anchor.ItemID {
				rest := make([]*Item, len(e.model.Items)-i-1)
				copy(rest, e.model.Items[i+1:])
				e.model.Items = append(e.model.Items[:i+1], it)
				e.model.Items = append(e.model.Items, rest...)
				e.revisions[it.ID] = 1
				e.stats.AppendCount++
				return it, anchor.ItemID
			}
		}
	}
	e.model.Items = append(e.model.Items, it)
	e.revisions[it.ID] = 1
	e.stats.AppendCount++
	return it, ""
}

// upsertItem 按 ID 更新既有 Item；找不到时退化为 append（乱序免疫）。
// mutate 返回 false 表示内容无变化（幂等跳过，不产生变更）。
// 返回 (Item, changed)：changed=false 时调用方不得追加变更记录。
func (e *EventEncoder) upsertItem(id string, kind ItemKind, mutate func(*Item) bool) (*Item, bool) {
	for _, it := range e.model.Items {
		if it.ID == id {
			// 终态保护：completed/failed/canceled 后拒绝一切 upsert
			// （状态机不变量：终态后仅允许 remove）。
			if it.Status.Terminal() {
				e.stats.DuplicateCount++
				return it, false
			}
			if mutate != nil && !mutate(it) {
				e.stats.DuplicateCount++
				return it, false
			}
			it.Updated = e.clock
			e.revisions[id]++
			e.stats.UpsertCount++
			return it, true
		}
	}
	it := e.appendItem(kind, "", "")
	return it, true
}

// removeItem 按 ID 移除；不存在则忽略（幂等）。
func (e *EventEncoder) removeItem(id string, cs *ChangeSet) {
	for i, it := range e.model.Items {
		if it.ID == id {
			rev := e.revisions[id]
			e.model.Items = append(e.model.Items[:i], e.model.Items[i+1:]...)
			delete(e.revisions, id)
			e.stats.RemoveCount++
			cs.Changes = append(cs.Changes, ItemChange{Op: OpRemove, Item: it, Revision: rev})
			return
		}
	}
	e.stats.DuplicateCount++
}

func (e *EventEncoder) change(cs *ChangeSet, o Op, it *Item) {
	rev := e.revisions[it.ID]
	cp := *it
	cs.Changes = append(cs.Changes, ItemChange{Op: o, Item: &cp, Revision: rev})
}

// changeAfter 与 change 同语义，额外携带锚定插入目标（AfterID 非空时
// 渲染层在目标 cell 之后插入，见 ItemChange.AfterID）。
func (e *EventEncoder) changeAfter(cs *ChangeSet, o Op, it *Item, afterID string) {
	rev := e.revisions[it.ID]
	cp := *it
	cs.Changes = append(cs.Changes, ItemChange{Op: o, Item: &cp, Revision: rev, AfterID: afterID})
}

// ---- 具体操作 ----

func (e *EventEncoder) applySystem(ev runtimeevents.Event, cs *ChangeSet) {
	it := e.appendItem(KindSystem, "", systemHead(ev))
	e.change(cs, OpAppend, it)
}

// PriorityPromptKey returns the canonical key shared by the requested event,
// the synchronous prompt transcript, and the resolved event. Runtime actors
// assign request_id/question_id; the fallback keeps compatibility events
// observable without binding different event types to each other.
func PriorityPromptKey(eventType, requestKey string) string {
	eventType = strings.TrimSpace(eventType)
	requestKey = strings.TrimSpace(requestKey)
	switch eventType {
	case runtimechat.EventApprovalResolved:
		eventType = runtimechat.EventApprovalRequested
	case runtimechat.EventQuestionAnswered:
		eventType = runtimechat.EventQuestionAsked
	}
	if requestKey == "" {
		return ""
	}
	switch eventType {
	case runtimechat.EventApprovalRequested, runtimechat.EventQuestionAsked:
		return eventType + "\x00" + requestKey
	default:
		return ""
	}
}

// PriorityPromptRequestKey derives the request component of a runtime
// approval/question key. It is exported so the bridge can carry the exact
// request identity across the synchronous stdin exception without duplicating
// the fallback algorithm.
func PriorityPromptRequestKey(ev runtimeevents.Event) string {
	requestKey := ""
	switch ev.Type {
	case runtimechat.EventApprovalRequested, runtimechat.EventApprovalResolved:
		requestKey = payloadString(ev.Payload["request_id"], "")
		if requestKey == "" {
			requestKey = strings.Join([]string{
				strings.TrimSpace(ev.SessionID),
				strings.TrimSpace(ev.TraceID),
				payloadString(ev.Payload["tool_name"], ev.ToolName),
				payloadString(ev.Payload["reason"], ""),
			}, "\x1f")
		}
	case runtimechat.EventQuestionAsked, runtimechat.EventQuestionAnswered:
		requestKey = payloadString(ev.Payload["question_id"], "")
		if requestKey == "" {
			requestKey = strings.Join([]string{
				strings.TrimSpace(ev.SessionID),
				strings.TrimSpace(ev.TraceID),
				payloadString(ev.Payload["prompt"], ""),
			}, "\x1f")
		}
	}
	return requestKey
}

func priorityPromptKeyForEvent(ev runtimeevents.Event) string {
	return PriorityPromptKey(ev.Type, PriorityPromptRequestKey(ev))
}

func (e *EventEncoder) applyPriorityRequested(ev runtimeevents.Event, _ *ChangeSet) {
	key := priorityPromptKeyForEvent(ev)
	if key == "" {
		return
	}
	state := e.priorityBy[key]
	if state == nil {
		state = &priorityPromptState{}
		e.priorityBy[key] = state
	}
	if state.requested {
		e.stats.DuplicateCount++
		return
	}
	state.requested = true
}

func (e *EventEncoder) applyPriorityResolved(ev runtimeevents.Event, _ *ChangeSet) {
	key := priorityPromptKeyForEvent(ev)
	if key == "" {
		return
	}
	state := e.priorityBy[key]
	if state == nil {
		state = &priorityPromptState{}
		e.priorityBy[key] = state
	}
	if state.resolved {
		e.stats.DuplicateCount++
		return
	}
	state.resolved = true
}

func (e *EventEncoder) applyReasoning(ev runtimeevents.Event, cs *ChangeSet) {
	text, streamDelta := reasoningText(ev)
	key := reasoningKey(ev)
	// reasoning 拥有独立索引：绝不允许覆盖 assistant Item 的内容或状态
	// （render-model-spec：reasoning 是独立 Kind，与 assistant 并存）。
	if it := e.reasoningBy[key]; it != nil {
		u, changed := e.upsertItem(it.ID, KindReasoning, func(t *Item) bool {
			next := text
			if streamDelta {
				next = appendReasoningDelta(t.Head, text)
			}
			if t.Head == next {
				return false
			}
			t.Head = next
			t.Status = StatusRunning
			return true
		})
		if changed {
			e.change(cs, OpUpsert, u)
		}
		return
	}
	it := e.appendItem(KindReasoning, "", text)
	e.reasoningBy[key] = it
	e.change(cs, OpAppend, it)
}

// reasoningText accepts both the compact encoder payload and the typed nested
// ReasoningBlock emitted by the local agent loop. The latter is the production
// shape for "assistant.reasoning"; treating it as a system event previously
// leaked only the event type into the TUI and discarded its visible thought.
func reasoningText(ev runtimeevents.Event) (text string, streamDelta bool) {
	text = payloadString(ev.Payload["text"], payloadString(ev.Payload["summary"], ""))
	if block := runtimetypes.ReasoningBlockFromMap(ev.Payload["reasoning"]); block != nil {
		if display := block.RawDisplayText(); display != "" {
			text = display
		}
		streamDelta = strings.EqualFold(strings.TrimSpace(block.Format), "stream_delta")
	}
	return text, streamDelta
}

// appendReasoningDelta accepts both provider delta chunks and providers which
// repeat a full snapshot in each event. It is deliberately local to the
// encoder: the semantic Scene must receive the same monotonic visible body as
// the live reasoning presenter.
func appendReasoningDelta(existing, incoming string) string {
	if incoming == "" || incoming == existing {
		return existing
	}
	if existing == "" {
		return incoming
	}
	if strings.HasPrefix(incoming, existing) {
		return incoming
	}
	return existing + incoming
}

func (e *EventEncoder) applyLLMStarted(ev runtimeevents.Event, cs *ChangeSet) {
	key := streamKey(ev)
	// delta 先于 llm_started 到达时已创建块：复用而非再 append，
	// 保证"同一流 = 一个 assistant Item"的模型不变量。
	if it := e.assistantBy[key]; it != nil {
		if !it.Status.Terminal() {
			e.stats.DuplicateCount++
			return
		}
		// 终态后同流重新启动（重试/新请求）：允许新建块。
	}
	it := e.appendItem(KindAssistant, "", "")
	e.assistantBy[key] = it
	e.change(cs, OpAppend, it)
}

func (e *EventEncoder) applyAssistantDelta(ev runtimeevents.Event, cs *ChangeSet) {
	key := streamKey(ev)
	delta := payloadString(ev.Payload["delta"], "")
	if delta == "" {
		delta = payloadString(ev.Payload["content"], "")
	}
	seq, hasSeq := assistantSequence(ev.Payload)
	it := e.assistantBy[key]
	if it == nil {
		// 缺失起点（delta 先于 llm_started）：创建空 assistant 块，
		// 后续 llm_started 复用；继续走重排逻辑，本段 delta 同样入序。
		it = e.appendItem(KindAssistant, "", "")
		e.assistantBy[key] = it
		e.change(cs, OpAppend, it)
	}
	if it.Status.Terminal() {
		// final 后到达的 delta：丢弃（对齐旧终端 HasRenderedAssistantFinal）。
		e.stats.DuplicateCount++
		return
	}
	// legacy 路径：无 sequence 身份时按到达顺序直接拼接（兼容旧 provider）。
	if !hasSeq || seq == 0 {
		if delta == "" {
			return
		}
		u, changed := e.upsertItem(it.ID, KindAssistant, func(t *Item) bool {
			if t.Head == "" {
				t.Head = delta
			} else {
				t.Head += delta
			}
			t.Status = StatusRunning
			return true
		})
		if changed {
			e.change(cs, OpUpsert, u)
		}
		return
	}
	order := e.streamOrder[key]
	if order == nil {
		order = &assistantStreamOrder{nextSeq: 1, pending: make(map[uint64]string)}
		e.streamOrder[key] = order
	}
	if order.tainted {
		// 超限后该流丢弃后续乱序 delta（对齐旧终端 tainted 语义）。
		e.stats.OutOfOrderCount++
		return
	}
	if seq < order.nextSeq {
		// 已提交过的旧 sequence：重复/迟到，幂等跳过。
		e.stats.DuplicateCount++
		return
	}
	if seq > order.nextSeq {
		if _, dup := order.pending[seq]; dup {
			e.stats.DuplicateCount++
			return
		}
		order.pending[seq] = delta
		order.pendingText += len(delta)
		e.stats.OutOfOrderCount++
		if len(order.pending) >= assistantStreamPendingLimit ||
			order.pendingText > assistantStreamPendingByteLimit {
			order.pending = make(map[uint64]string)
			order.pendingText = 0
			order.tainted = true
		}
		return
	}
	// seq == nextSeq：提交本段并连续补拼 pending（有序拼接，不丢信息）。
	head := delta
	order.nextSeq++
	for {
		p, ok := order.pending[order.nextSeq]
		if !ok {
			break
		}
		head += p
		delete(order.pending, order.nextSeq)
		order.nextSeq++
	}
	if head == "" {
		return
	}
	u, changed := e.upsertItem(it.ID, KindAssistant, func(t *Item) bool {
		if t.Head == "" {
			t.Head = head
		} else {
			t.Head += head
		}
		t.Status = StatusRunning
		return true
	})
	if changed {
		e.change(cs, OpUpsert, u)
	}
}

func (e *EventEncoder) applyAssistantFinal(ev runtimeevents.Event, cs *ChangeSet) {
	key := streamKey(ev)
	text := payloadString(ev.Payload["content"], payloadString(ev.Payload["message"], ""))
	it := e.assistantBy[key]
	if it == nil {
		it = e.appendItem(KindAssistant, "", text)
		it.Status = StatusCompleted // 孤儿 final：直接终态（不保持 pending）
		e.assistantBy[key] = it
		e.change(cs, OpAppend, it)
		return
	}
	// 流结束：补拼乱序缓冲中尚未提交的 delta（不丢信息）。
	e.flushAssistantStream(key, it, cs)
	u, changed := e.upsertItem(it.ID, KindAssistant, func(t *Item) bool {
		changed := false
		if text != "" && t.Head != text {
			t.Head = text
			changed = true
		}
		if t.Status != StatusCompleted {
			t.Status = StatusCompleted
			changed = true
		}
		return changed
	})
	if changed {
		e.change(cs, OpUpsert, u)
	}
}

func (e *EventEncoder) applyLLMFinished(ev runtimeevents.Event, cs *ChangeSet) {
	key := streamKey(ev)
	it := e.assistantBy[key]
	if it != nil {
		// 流结束：补拼乱序缓冲中尚未提交的 delta（幂等：空则无操作）。
		e.flushAssistantStream(key, it, cs)
		u, changed := e.upsertItem(it.ID, KindAssistant, func(t *Item) bool {
			if t.Status.Terminal() {
				return false
			}
			t.Status = StatusCompleted
			return true
		})
		if changed {
			e.change(cs, OpUpsert, u)
		}
	}
	// LLM 请求结束同时终结同流 reasoning 块（无专门 reasoning final 事件）。
	if r := e.reasoningBy[key]; r != nil {
		if u, changed := e.upsertItem(r.ID, KindReasoning, func(t *Item) bool {
			if t.Status.Terminal() {
				return false
			}
			t.Status = StatusCompleted
			return true
		}); changed {
			e.change(cs, OpUpsert, u)
		}
	}
}

// FinalizeOpenStreams closes every mutable assistant/reasoning item without
// appending another transcript entry. It is the terminal boundary for runs
// whose provider never emits a per-request completion event. The caller owns
// encoder serialization and may invoke it repeatedly; terminal items are
// skipped, so it is idempotent.
func (e *EventEncoder) FinalizeOpenStreams(status ItemStatus) *ChangeSet {
	if e == nil {
		return nil
	}
	e.clock++
	e.stats.EncodeCount++
	cs := &ChangeSet{}
	e.finalizeOpenStreams(status, cs)
	e.updateTail(cs)
	return cs
}

// applySessionEnd is deliberately silent in the visible RenderModel. It only
// resolves mutable stream ownership, preserving received partial content in
// the existing cell rather than re-emitting it through a fallback renderer.
func (e *EventEncoder) applySessionEnd(ev runtimeevents.Event, cs *ChangeSet) {
	e.finalizeOpenStreams(sessionEndItemStatus(ev), cs)
}

func (e *EventEncoder) finalizeOpenStreams(status ItemStatus, cs *ChangeSet) {
	if e == nil || cs == nil {
		return
	}
	if !status.Terminal() {
		status = StatusCompleted
	}
	assistantKeys := make([]string, 0, len(e.assistantBy))
	for key := range e.assistantBy {
		assistantKeys = append(assistantKeys, key)
	}
	sort.Strings(assistantKeys)
	for _, key := range assistantKeys {
		it := e.assistantBy[key]
		if it == nil || it.Status.Terminal() {
			continue
		}
		e.flushAssistantStream(key, it, cs)
		u, changed := e.upsertItem(it.ID, KindAssistant, func(item *Item) bool {
			if item.Status.Terminal() {
				return false
			}
			item.Status = status
			return true
		})
		if changed {
			e.change(cs, OpUpsert, u)
		}
	}

	reasoningKeys := make([]string, 0, len(e.reasoningBy))
	for key := range e.reasoningBy {
		reasoningKeys = append(reasoningKeys, key)
	}
	sort.Strings(reasoningKeys)
	for _, key := range reasoningKeys {
		it := e.reasoningBy[key]
		if it == nil || it.Status.Terminal() {
			continue
		}
		u, changed := e.upsertItem(it.ID, KindReasoning, func(item *Item) bool {
			if item.Status.Terminal() {
				return false
			}
			item.Status = status
			return true
		})
		if changed {
			e.change(cs, OpUpsert, u)
		}
	}
}

func sessionEndItemStatus(ev runtimeevents.Event) ItemStatus {
	if ev.Type == runtimechat.EventSessionInterrupted {
		return StatusCanceled
	}
	if ev.Payload != nil {
		if _, reported := ev.Payload["success"]; reported && !payloadBoolValue(ev.Payload["success"]) {
			return StatusFailed
		}
		if strings.TrimSpace(payloadString(ev.Payload["error"], "")) != "" {
			return StatusFailed
		}
	}
	return StatusCompleted
}

func (e *EventEncoder) applyToolStarted(ev runtimeevents.Event, cs *ChangeSet) {
	callID := toolCallID(ev)
	name := payloadString(ev.Payload["tool_name"], ev.ToolName)
	if callID == "" || name == "" {
		// A mutable tool cell must have both a stable call identity and a
		// readable semantic head. Without either, later progress/final events
		// cannot be attached safely, so preserve the event as a system fallback.
		e.applySystem(ev, cs)
		return
	}
	if callID != "" {
		if it := e.toolByID[callID]; it != nil {
			if !it.Status.Terminal() {
				// 重复 started（同 callID 且未完成）：幂等跳过，不新建 ToolCall。
				e.stats.DuplicateCount++
				return
			}
			// 已完成调用后同 callID 重新发起：视为新调用，允许新建。
		}
	}
	it := e.appendItem(KindToolCall, "", name)
	if callID != "" {
		e.toolByID[callID] = it
	}
	e.change(cs, OpAppend, it)
}

func (e *EventEncoder) applyToolProgress(ev runtimeevents.Event, cs *ChangeSet) {
	callID := toolCallID(ev)
	it := e.toolByID[callID]
	if callID == "" || it == nil || it.Status.Terminal() {
		e.applySystem(ev, cs)
		return
	}
	detail := toolProgressText(ev)
	if detail == "" {
		return
	}
	u, changed := e.upsertItem(it.ID, KindToolCall, func(t *Item) bool {
		name := payloadString(ev.Payload["tool_name"], ev.ToolName)
		next := name
		if next == "" {
			next = t.Head
			if newline := strings.IndexByte(next, '\n'); newline >= 0 {
				next = next[:newline]
			}
		}
		next += "\n" + detail
		if t.Head == next {
			return false
		}
		t.Head = next
		t.Status = StatusRunning
		return true
	})
	if changed {
		e.change(cs, OpUpsert, u)
	}
}

func (e *EventEncoder) applyToolFinished(ev runtimeevents.Event, cs *ChangeSet) {
	callID := toolCallID(ev)
	it := e.toolByID[callID]
	if callID == "" || it == nil {
		e.applySystem(ev, cs)
		return
	}
	displayHead := payloadString(ev.Payload["display_head"], "")
	if it != nil {
		u, changed := e.upsertItem(it.ID, KindToolCall, func(t *Item) bool {
			if t.Status.Terminal() {
				return false
			}
			if displayHead != "" {
				t.Head = displayHead
			}
			t.Status = StatusCompleted
			return true
		})
		if changed {
			e.change(cs, OpUpsert, u)
		}
	}
	// A direct legacy tool has already normalized its final response into one
	// display head. Do not append raw output a second time: that would both
	// change the final cell text and create a duplicate visible result on replay.
	if displayHead != "" {
		return
	}
	output := toolFinishedText(ev)
	if output == "" {
		return
	}
	cause := it.ID
	// 幂等：同 callID 同文本的重复 output 只提交一次（重放/重复 final 场景）。
	if e.toolOutputBy == nil {
		e.toolOutputBy = make(map[string]map[string]struct{})
	}
	seen := e.toolOutputBy[callID]
	if seen == nil {
		seen = make(map[string]struct{})
		e.toolOutputBy[callID] = seen
	}
	if _, dup := seen[output]; dup {
		e.stats.DuplicateCount++
		return
	}
	seen[output] = struct{}{}
	out := e.appendItem(KindToolOutput, cause, output)
	out.Status = StatusCompleted // 工具输出一次性完成（终态语义）
	e.change(cs, OpAppend, out)
}

// flushAssistantStream 在流结束事件（assistant final / llm finished）时
// 把乱序缓冲中尚未提交的 delta 按 sequence 排序补拼进 item（不丢信息）。
// 幂等：无缓冲或无 pending 时无操作。
func (e *EventEncoder) flushAssistantStream(key string, it *Item, cs *ChangeSet) {
	order := e.streamOrder[key]
	if order == nil || order.tainted || len(order.pending) == 0 {
		return
	}
	seqs := make([]uint64, 0, len(order.pending))
	for s := range order.pending {
		seqs = append(seqs, s)
	}
	sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })
	tail := ""
	for _, s := range seqs {
		tail += order.pending[s]
	}
	order.pending = make(map[uint64]string)
	order.pendingText = 0
	if tail == "" {
		return
	}
	u, changed := e.upsertItem(it.ID, KindAssistant, func(t *Item) bool {
		t.Head += tail
		return true
	})
	if changed {
		e.change(cs, OpUpsert, u)
	}
}

// ---- 辅助 ----

// streamKey 构造 assistant 流身份键（turnID/streamID），与
// assistantEventIdentity 的语义一致。
func streamKey(ev runtimeevents.Event) string {
	turnID := payloadString(ev.Payload["turn_id"], "")
	streamID := payloadString(ev.Payload["stream_id"], "")
	if streamID == "" {
		streamID = payloadString(ev.Payload["trace_id"], "")
	}
	if turnID == "" && streamID == "" {
		return ""
	}
	return turnID + "/" + streamID
}

// reasoningKey 与 streamKey 同源（reasoning 挂到当前 assistant 流）。
func reasoningKey(ev runtimeevents.Event) string {
	return streamKey(ev)
}

func toolCallID(ev runtimeevents.Event) string {
	return payloadString(ev.Payload["tool_call_id"], "")
}

func toolProgressText(ev runtimeevents.Event) string {
	for _, key := range []string{"message", "progress", "detail", "status"} {
		if value := payloadString(ev.Payload[key], ""); value != "" {
			return value
		}
	}
	return ""
}

func toolFinishedText(ev runtimeevents.Event) string {
	for _, key := range []string{"output", "result", "summary"} {
		if value := payloadString(ev.Payload[key], ""); value != "" {
			return value
		}
	}
	if lines := payloadStringSlice(ev.Payload["summary_lines"]); len(lines) > 0 {
		return strings.Join(lines, "\n")
	}
	for _, key := range []string{"error", "reason"} {
		if value := payloadString(ev.Payload[key], ""); value != "" {
			return value
		}
	}
	switch ev.Type {
	case "tool.failed":
		return "failed"
	case "tool.cancelled", "tool.canceled":
		return "cancelled"
	default:
		return ""
	}
}

func payloadBoolValue(value interface{}) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(typed))
		return err == nil && parsed
	case float64:
		return typed != 0
	case int:
		return typed != 0
	case int64:
		return typed != 0
	default:
		return false
	}
}

func payloadStringSlice(value interface{}) []string {
	switch values := value.(type) {
	case []string:
		return append([]string(nil), values...)
	case []interface{}:
		result := make([]string, 0, len(values))
		for _, item := range values {
			if text := payloadString(item, ""); text != "" {
				result = append(result, text)
			}
		}
		return result
	default:
		return nil
	}
}

func systemHead(ev runtimeevents.Event) string {
	head := payloadString(ev.Payload["message"], "")
	if head != "" {
		return head
	}
	head = payloadString(ev.Payload["summary"], "")
	if head != "" {
		return head
	}
	return ev.Type
}

func payloadString(v interface{}, fallback string) string {
	switch t := v.(type) {
	case nil:
		return fallback
	case string:
		if t == "" {
			return fallback
		}
		return t
	case json.Number:
		return t.String()
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	default:
		if b, err := json.Marshal(t); err == nil {
			return string(b)
		}
		return fallback
	}
}

// assistantSequence 读取 payload["sequence"]（与 commands 包
// assistantEventSequence 同语义）。
func assistantSequence(payload map[string]interface{}) (uint64, bool) {
	if payload == nil {
		return 0, false
	}
	switch value := payload["sequence"].(type) {
	case uint64:
		return value, true
	case uint:
		return uint64(value), true
	case int:
		if value < 0 {
			return 0, true
		}
		return uint64(value), true
	case int64:
		if value < 0 {
			return 0, true
		}
		return uint64(value), true
	case float64:
		if value < 0 {
			return 0, true
		}
		return uint64(value), true
	case json.Number:
		parsed, err := strconv.ParseUint(string(value), 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}
