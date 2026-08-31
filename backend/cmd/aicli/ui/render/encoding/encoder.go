package encoding

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/markdown"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	runtimetoolresult "github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// 单流 delta 乱序重排缓冲上限（与旧终端 orderAssistantDelta 同规格）：
// 超过上限后该流标记 tainted，丢弃后续乱序 delta（对齐既有生产行为）。
const (
	assistantStreamPendingLimit     = 128
	assistantStreamPendingByteLimit = 1 << 20
)

// StreamCoalescedFromKey is the payload field written by the bridge when it
// folds contiguous sequence deltas into one event. The visible "sequence"
// field is the interval end; this field carries the interval start so
// downstream ordering can advance past the whole folded range.
const StreamCoalescedFromKey = "_coalesced_sequence_from"

// ReasoningStreamDeltaKey preserves the semantic mode of a reasoning event
// after the bridge coalesces several typed ReasoningBlock payloads into one
// top-level text payload. Chunk boundaries are transport boundaries only; this
// marker tells every downstream consumer to append the bytes exactly instead
// of interpreting the folded text as an authoritative snapshot.
const ReasoningStreamDeltaKey = "_reasoning_stream_delta"

// ReasoningOperation is the normalized semantic operation carried by an
// assistant.reasoning event. Transport chunking is never inferred from prose:
// append means byte-for-byte continuation, replace means an authoritative
// snapshot for the same canonical request.
type ReasoningOperation uint8

const (
	ReasoningOperationReplace ReasoningOperation = iota
	ReasoningOperationAppend
)

// ReasoningOperationForEvent is the single protocol classifier shared by the
// queue coalescer, bridge compatibility state, and EventEncoder. Streamable is
// deliberately not a signal: it describes provider/UI capability, not whether
// this particular payload is a delta.
func ReasoningOperationForEvent(ev runtimeevents.Event) ReasoningOperation {
	for _, key := range []string{"mode", "operation"} {
		switch strings.ToLower(strings.TrimSpace(payloadString(ev.Payload[key], ""))) {
		case "replace", "snapshot":
			return ReasoningOperationReplace
		case "append", "delta":
			return ReasoningOperationAppend
		}
	}
	if marked, ok := ev.Payload[ReasoningStreamDeltaKey].(bool); ok && marked {
		return ReasoningOperationAppend
	}
	if block := runtimetypes.ReasoningBlockFromMap(ev.Payload["reasoning"]); block != nil &&
		strings.EqualFold(strings.TrimSpace(block.Format), "stream_delta") {
		return ReasoningOperationAppend
	}
	return ReasoningOperationReplace
}

// EventEncoder 是统一渲染编码器，对应 Codex ThreadHistoryBuilder：
// 所有上游事件经 Encode 转换为 RenderModel 的 append/upsert/remove 操作。
//
// 并发：非线程安全。由事件消费侧（chatRuntimeEventBridge 的单 goroutine
// 事件循环）独占调用。
type EventEncoder struct {
	model                  *RenderModel
	nextItemID             uint64 // item-{n} 单调分配
	nextSeq                uint64 // 提交序号单调分配
	clock                  uint64 // 编码器时钟（每事件 +1）
	revisions              map[string]uint64
	assistantBy            map[string]*Item                 // canonical request key -> 当前 assistant item
	assistantTombstones    map[string]struct{}              // canonical request key -> 已移除的空 assistant 终态
	reasoningBy            map[string]*Item                 // canonical request key -> 当前 reasoning item
	reasoningBarriers      map[string]bool                  // request key -> assistant native-history ordering fence
	requestAliases         map[string]string                // transport/logical alias -> canonical request key
	latestRequestByScope   map[string]string                // turn/logical-turn/trace scope -> 最新 request key
	requestFinished        map[string]bool                  // request key -> 已收到 request finished
	requestFailureBy       map[string]*Item                 // request key -> 已提交的可见 failure cell
	toolByID               map[string]*Item                 // payload tool_call_id -> tool_call item
	toolOutputBy           map[string]map[string]struct{}   // callID -> 已提交 output 文本（幂等）
	priorityBy             map[string]*priorityPromptState  // approval/question request key -> delayed transcript state
	streamOrder            map[string]*assistantStreamOrder // canonical request key -> delta 有序提交状态
	reasoningOrder         map[string]*assistantStreamOrder // canonical request key -> reasoning delta 有序提交状态
	orderingBarrierEnabled bool                             // production bridge reserves a hidden reasoning predecessor
	stats                  Stats
}

// EnableReasoningOrderingBarrier enables the physical ordering barrier used by
// the live bridge. Direct encoder consumers retain the historical compact model
// unless they explicitly opt in, which keeps replay fixtures and compatibility
// callers free of an implementation-only placeholder.
func (e *EventEncoder) EnableReasoningOrderingBarrier(enabled bool) {
	if e != nil {
		e.orderingBarrierEnabled = enabled
	}
}

// assistantStreamOrder 维护单条 assistant 流的 delta 有序提交状态
// （对齐旧终端 orderAssistantDelta：sequence 从 1 开始，乱序缓存，
// 连续补拼；超限 tainted 丢弃后续）。
type assistantStreamOrder struct {
	nextSeq     uint64
	pending     map[uint64]assistantPendingDelta
	pendingText int
	tainted     bool
}

type assistantPendingDelta struct {
	text   string
	endSeq uint64
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
		model:                &RenderModel{},
		revisions:            make(map[string]uint64),
		assistantBy:          make(map[string]*Item),
		assistantTombstones:  make(map[string]struct{}),
		reasoningBy:          make(map[string]*Item),
		reasoningBarriers:    make(map[string]bool),
		requestAliases:       make(map[string]string),
		latestRequestByScope: make(map[string]string),
		requestFinished:      make(map[string]bool),
		requestFailureBy:     make(map[string]*Item),
		toolByID:             make(map[string]*Item),
		toolOutputBy:         make(map[string]map[string]struct{}),
		priorityBy:           make(map[string]*priorityPromptState),
		streamOrder:          make(map[string]*assistantStreamOrder),
		reasoningOrder:       make(map[string]*assistantStreamOrder),
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
	return e.SubmitAssistantWithBoundaryGroup(text, "")
}

// SubmitAssistantWithBoundaryGroup is the key-aware form used when a caller
// reconstructs or directly injects a response with an exact request identity.
// The key controls spacing only; it never becomes CauseID/tool ownership.
func (e *EventEncoder) SubmitAssistantWithBoundaryGroup(text, boundaryGroupKey string) *ChangeSet {
	if e == nil {
		return nil
	}
	e.clock++
	e.stats.EncodeCount++
	cs := &ChangeSet{}
	it := e.appendItem(KindAssistant, "", text)
	it.BoundaryGroupKey = boundaryGroupKey
	setAssistantPresentation(it)
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
	return e.SubmitCommandDocument(text, render.Document{})
}

// SubmitCommandDocument retains a command's structured render IR while Head
// remains the stable plain projection used by logs and text-parity checks.
func (e *EventEncoder) SubmitCommandDocument(text string, document render.Document) *ChangeSet {
	if e == nil {
		return nil
	}
	e.clock++
	e.stats.EncodeCount++
	cs := &ChangeSet{}
	it := e.appendItem(KindCommand, "", text)
	if len(document.Blocks) > 0 {
		it.Presentation = Presentation{Kind: PresentationDocument, Document: document.Clone()}
	}
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
	return e.SubmitSupplementWithBoundaryGroup(text, "")
}

// SubmitSupplementWithBoundaryGroup is reserved for reconstructed reasoning
// with an exact request identity. Ordinary notices remain ungrouped.
func (e *EventEncoder) SubmitSupplementWithBoundaryGroup(text, boundaryGroupKey string) *ChangeSet {
	if e == nil {
		return nil
	}
	e.clock++
	e.stats.EncodeCount++
	cs := &ChangeSet{}
	it := e.appendItem(KindSupplement, "", text)
	it.BoundaryGroupKey = boundaryGroupKey
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

// SubmitReasoningWithBoundaryGroup 提交重建的终态 reasoning cell（resume /
// 历史种子路径）。Head 与持久化转录一样只保存原始推理正文；reasoning 的
// 开始/结束 divider 是由 Scene kind + terminal status 派生的展示 chrome，
// 不能进入语义文本、流 offset 或快照去重身份。
func (e *EventEncoder) SubmitReasoningWithBoundaryGroup(text, boundaryGroupKey string) *ChangeSet {
	if e == nil {
		return nil
	}
	e.clock++
	e.stats.EncodeCount++
	cs := &ChangeSet{}
	it := e.appendItem(KindReasoning, "", text)
	it.BoundaryGroupKey = boundaryGroupKey
	it.Status = StatusCompleted
	setReasoningPresentation(it)
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
	return e.SubmitUserInteractionDocument(text, render.Document{}, anchor)
}

func (e *EventEncoder) SubmitUserInteractionDocument(text string, document render.Document, anchor *Tail) *ChangeSet {
	if e == nil {
		return nil
	}
	e.clock++
	e.stats.EncodeCount++
	cs := &ChangeSet{}
	it, afterID := e.insertItemAfter(anchor, KindUserInteraction, text)
	if len(document.Blocks) > 0 {
		it.Presentation = Presentation{Kind: PresentationDocument, Document: document.Clone()}
	}
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
	e.assistantTombstones = make(map[string]struct{})
	e.reasoningBy = make(map[string]*Item)
	e.reasoningBarriers = make(map[string]bool)
	e.requestAliases = make(map[string]string)
	e.latestRequestByScope = make(map[string]string)
	e.requestFinished = make(map[string]bool)
	e.requestFailureBy = make(map[string]*Item)
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

	case runtimechat.EventAssistantDelta, "assistant.delta":
		return opAssistantDelta

	case runtimechat.EventAssistantMessage, "assistant.message":
		return opAssistantFinal

	case runtimechat.EventLLMRequestStarted, "llm.request.started":
		return opLLMStarted

	case runtimechat.EventLLMRequestFinished, "llm.request.finished":
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

	case "llm.retry":
		// 重试是过程状态：重试信息由 bridge 渲染在动态数据状态区域
		// （handleEvent 的 RefreshStatus），不产生 transcript/system cell。
		return opNone

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
// context.tool_schema.frozen / tool.reduced 等）。编码器对静默事件不产生
// 任何 Item 与变更（EncodeCount 仍计入：事件被消费；UnknownCount 不计：
// 属已知类型），与旧路径可见行为严格一致（checkTextParity 亦依赖该对齐）。
//
// tool.reduced 是 reducer 在 tool.completed 之后补充的遥测事件（payload
// 仅为 reducer 名、artifact 计数等元信息，无 tool 输出内容），对应的
// mutable tool cell 已由 tool.requested/tool.completed 完成终态；若将其
// 渲染为 system cell，会把字面 "tool.reduced" 泄漏到可见 transcript。
//
// 注意：llm.retry / response.* 等在旧路径有（或部分有）可见 timeline
// 输出，不在此列；typed/dotted request lifecycle is classified above and
// mutates stream state without creating a system cell.
func isSilentSystemEventType(eventType string) bool {
	switch eventType {
	case runtimechat.EventSessionStart,
		runtimechat.EventSessionEnd,
		runtimechat.EventSessionInterrupted,
		runtimechat.EventSessionCompactSkipped,
		runtimechat.EventContextReconciled,
		"planning.started",
		"subagent.batch.started",
		"subagent.started",
		"task.started",
		"team.task.started",
		"context.tool_schema.frozen",
		"tool.reduced":
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
	opLLMStarted                  // LLM 请求开始：登记流身份，不创建可见占位
	opLLMFinished                 // LLM 请求结束：成功等待 final，失败终结并呈现错误
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

// insertItemBefore 把新 Item 插入到 anchorID 对应 Item 之前（reasoning
// 需要排在 assistant final 之前，见 applyReasoning）。anchor 不存在时退化
// 为 append。Seq 仍为单调创建序号（渲染顺序 = 模型数组顺序）。
func (e *EventEncoder) insertItemBefore(anchorID string, kind ItemKind, head string) *Item {
	it := &Item{
		ID:      e.nextID(),
		Seq:     e.nextSeqValue(),
		Kind:    kind,
		Status:  StatusPending,
		Head:    head,
		Created: e.clock,
		Updated: e.clock,
	}
	if anchorID != "" {
		for i, existing := range e.model.Items {
			if existing.ID == anchorID {
				rest := make([]*Item, len(e.model.Items)-i)
				copy(rest, e.model.Items[i:])
				e.model.Items = append(e.model.Items[:i], it)
				e.model.Items = append(e.model.Items, rest...)
				e.revisions[it.ID] = 1
				e.stats.AppendCount++
				return it
			}
		}
	}
	e.model.Items = append(e.model.Items, it)
	e.revisions[it.ID] = 1
	e.stats.AppendCount++
	return it
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

// correctTerminalReasoningItem is the sole encoder escape hatch from the
// ordinary terminal-item immutability rule. assistant.message can carry the
// authoritative reasoning snapshot after an assistant delta already closed the
// reasoning predecessor. That snapshot corrects the same item in place; it
// never reopens it and never applies to another item kind.
func (e *EventEncoder) correctTerminalReasoningItem(id string, mutate func(*Item) bool) (*Item, bool) {
	for _, it := range e.model.Items {
		if it.ID != id {
			continue
		}
		if it.Kind != KindReasoning || !it.Status.Terminal() {
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
	e.stats.DuplicateCount++
	return nil, false
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
	cs.Changes = append(cs.Changes, ItemChange{Op: o, Item: cloneItem(it), Revision: rev})
}

// changeAfter 与 change 同语义，额外携带锚定插入目标（AfterID 非空时
// 渲染层在目标 cell 之后插入，见 ItemChange.AfterID）。
func (e *EventEncoder) changeAfter(cs *ChangeSet, o Op, it *Item, afterID string) {
	rev := e.revisions[it.ID]
	cs.Changes = append(cs.Changes, ItemChange{Op: o, Item: cloneItem(it), Revision: rev, AfterID: afterID})
}

func (e *EventEncoder) changeBefore(cs *ChangeSet, o Op, it *Item, beforeID string) {
	rev := e.revisions[it.ID]
	cs.Changes = append(cs.Changes, ItemChange{Op: o, Item: cloneItem(it), Revision: rev, BeforeID: beforeID})
}

func cloneItem(item *Item) *Item {
	if item == nil {
		return nil
	}
	clone := *item
	clone.Presentation = item.Presentation.Clone()
	return &clone
}

// ---- 具体操作 ----

func (e *EventEncoder) applySystem(ev runtimeevents.Event, cs *ChangeSet) {
	head := systemHead(ev)
	it := e.appendItem(KindSystem, "", head)
	if looksLikeDiffPresentation(head) {
		it.Presentation.Kind = PresentationDiffSupplement
		it.Presentation.DiffLabel = diffPresentationLabel(toolCallName(ev), head)
	}
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
	key := e.resolveAssistantRequestKey(ev)
	if text == "" {
		return
	}
	if assistant := e.assistantBy[key]; assistant != nil && assistant.Status.Terminal() {
		// assistant.message is the terminal ownership boundary for the whole
		// response. A late reasoning event must not reopen or mutate its already
		// committed predecessor.
		e.stats.DuplicateCount++
		return
	}
	if streamDelta {
		var ready bool
		text, ready = e.orderReasoningDelta(key, text, ev.Payload)
		if !ready || text == "" {
			return
		}
	} else {
		// An authoritative snapshot supersedes all transport ordering state.
		delete(e.reasoningOrder, key)
	}
	// reasoning 拥有独立索引：绝不允许覆盖 assistant Item 的内容或状态
	// （render-model-spec：reasoning 是独立 Kind，与 assistant 并存）。
	if it := e.reasoningBy[key]; it != nil {
		if it.Status.Terminal() {
			if streamDelta {
				e.stats.DuplicateCount++
				return
			}
			// A transport boundary may have closed reasoning before its
			// authoritative snapshot arrived. Replace source in place while
			// preserving terminal status; never reopen the cell.
			e.applyFinalReasoningSnapshot(key, text, cs)
			return
		}
		u, changed := e.upsertItem(it.ID, KindReasoning, func(t *Item) bool {
			t.BoundaryGroupKey = key
			nextHead := text
			if streamDelta {
				// Ordered deltas are byte-faithful appends. A provider may split
				// inside a word, punctuation token, code span, or UTF-8 rune
				// boundary represented by separate valid strings; the renderer
				// must never infer whitespace from that transport seam.
				nextHead = t.Head + text
			}
			if t.Head == nextHead {
				return false
			}
			t.Head = nextHead
			t.Status = StatusRunning
			setReasoningPresentation(t)
			return true
		})
		if changed {
			e.change(cs, OpUpsert, u)
		}
		e.removeReasoningBarrier(key, cs)
		return
	}
	var it *Item
	if assistant := e.assistantBy[key]; assistant != nil {
		it = e.insertItemBefore(assistant.ID, KindReasoning, text)
		it.BoundaryGroupKey = key
		setReasoningPresentation(it)
		e.changeBefore(cs, OpAppend, it, assistant.ID)
	} else {
		it = e.appendItem(KindReasoning, "", text)
		it.BoundaryGroupKey = key
		setReasoningPresentation(it)
		e.change(cs, OpAppend, it)
	}
	e.reasoningBy[key] = it
	e.removeReasoningBarrier(key, cs)
}

// setReasoningPresentation keeps reasoning source-faithful. Reasoning prose is
// not assistant Markdown: provider newlines and markdown-looking bytes remain
// literal, while dividers and terminal wrapping are derived by the UI.
func setReasoningPresentation(it *Item) bool {
	if it == nil {
		return false
	}
	if it.Presentation.Kind == PresentationPlain && len(it.Presentation.Document.Blocks) == 0 {
		return false
	}
	it.Presentation = Presentation{Kind: PresentationPlain}
	return true
}

// markReasoningBarrier 创建空 reasoning 占位 cell：native-history ordering
// fence 的语义位置。
func (e *EventEncoder) markReasoningBarrier(key string, assistant *Item, cs *ChangeSet) {
	if e == nil || key == "" || assistant == nil || e.reasoningBy[key] != nil || cs == nil {
		return
	}
	e.reasoningBarriers[key] = true
	assistant.HistoryCommitBlocked = true
	// 空 reasoning 占位 cell：native-history ordering fence 的语义位置。
	// 迟到的 reasoning（assistant.reasoning 事件）经 reasoningBy 命中并
	// 填充此占位；authoritative final 提交时占位即最终 reasoning 内容。
	// 占位插在 assistant 之前（模型数组 insertItemBefore + Scene 锚定
	// InsertCellBefore），保证 Scene 渲染顺序 reasoning -> assistant。
	placeholder := e.insertItemBefore(assistant.ID, KindReasoning, "")
	placeholder.BoundaryGroupKey = key
	e.reasoningBy[key] = placeholder
	e.changeBefore(cs, OpAppend, placeholder, assistant.ID)
}

func (e *EventEncoder) removeReasoningBarrier(key string, cs *ChangeSet) bool {
	if e == nil || cs == nil || key == "" || !e.reasoningBarriers[key] {
		return false
	}
	delete(e.reasoningBarriers, key)
	assistant := e.assistantBy[key]
	if assistant == nil || !assistant.HistoryCommitBlocked {
		return false
	}
	u, changed := e.upsertItem(assistant.ID, KindAssistant, func(t *Item) bool {
		if !t.HistoryCommitBlocked {
			return false
		}
		t.HistoryCommitBlocked = false
		return true
	})
	if changed {
		e.change(cs, OpUpsert, u)
	}
	// 空占位（barrier 的语义位置）从未被迟到的 reasoning 填充：它不是持久
	// 内容，barrier 解除时必须移除，否则会成为空白 supplement cell。
	if placeholder := e.reasoningBy[key]; placeholder != nil &&
		placeholder.Kind == KindReasoning && placeholder.Head == "" {
		delete(e.reasoningBy, key)
		e.removeItem(placeholder.ID, cs)
	}
	return changed
}

// reasoningText accepts both the compact encoder payload and the typed nested
// ReasoningBlock emitted by the local agent loop. The latter is the production
// shape for "assistant.reasoning"; treating it as a system event previously
// leaked only the event type into the TUI and discarded its visible thought.
func reasoningText(ev runtimeevents.Event) (text string, streamDelta bool) {
	text = payloadString(ev.Payload["text"], payloadString(ev.Payload["summary"], ""))
	streamDelta = ReasoningOperationForEvent(ev) == ReasoningOperationAppend
	if block := runtimetypes.ReasoningBlockFromMap(ev.Payload["reasoning"]); block != nil {
		if display := block.RawDisplayText(); display != "" {
			text = display
		}
	}
	return text, streamDelta
}

// appendReasoningDelta deliberately performs no content-based reconciliation.
// Ordered deltas append exactly; authoritative snapshots are handled by the
// non-delta branch in applyReasoning.
func appendReasoningDelta(existing, incoming string) string {
	return existing + incoming
}

// orderReasoningDelta gives reasoning the same explicit identity/ordering
// contract as assistant text. It never examines text equality: duplicate,
// replayed, out-of-order, and coalesced events are decided only by sequence
// metadata within the canonical request.
func (e *EventEncoder) orderReasoningDelta(key, text string, payload map[string]interface{}) (string, bool) {
	seq, hasSeq := assistantSequence(payload)
	if !hasSeq || seq == 0 {
		// Compatibility for legacy providers without ordering metadata. Preserve
		// bytes and arrival order; do not guess replay from repeated prose.
		return text, true
	}
	order := e.reasoningOrder[key]
	if order == nil {
		order = &assistantStreamOrder{nextSeq: 1, pending: make(map[uint64]assistantPendingDelta)}
		e.reasoningOrder[key] = order
	}
	if order.tainted {
		e.stats.OutOfOrderCount++
		return "", false
	}
	if seq < order.nextSeq {
		e.stats.DuplicateCount++
		return "", false
	}
	coalescedFrom, coalesced := assistantCoalescedFrom(payload)
	if coalesced && coalescedFrom < order.nextSeq {
		e.stats.DuplicateCount++
		return "", false
	}
	if seq > order.nextSeq && !(coalesced && coalescedFrom == order.nextSeq) {
		cacheKey := seq
		if coalesced {
			cacheKey = coalescedFrom
		}
		if _, duplicate := order.pending[cacheKey]; duplicate {
			e.stats.DuplicateCount++
			return "", false
		}
		order.pending[cacheKey] = assistantPendingDelta{text: text, endSeq: seq}
		order.pendingText += len(text)
		e.stats.OutOfOrderCount++
		if len(order.pending) >= assistantStreamPendingLimit ||
			order.pendingText > assistantStreamPendingByteLimit {
			order.pending = make(map[uint64]assistantPendingDelta)
			order.pendingText = 0
			order.tainted = true
		}
		return "", false
	}
	head := text
	if coalesced && coalescedFrom == order.nextSeq && seq >= coalescedFrom {
		order.nextSeq = seq + 1
	} else {
		order.nextSeq++
	}
	for {
		pending, ok := order.pending[order.nextSeq]
		if !ok {
			break
		}
		head += pending.text
		order.pendingText -= len(pending.text)
		delete(order.pending, order.nextSeq)
		order.nextSeq = pending.endSeq + 1
	}
	if order.pendingText < 0 {
		order.pendingText = 0
	}
	return head, true
}

func (e *EventEncoder) applyLLMStarted(ev runtimeevents.Event, _ *ChangeSet) {
	// Request-start is lifecycle metadata, not transcript content. In
	// particular it must not reserve an empty assistant position before the
	// first reasoning delta. The first non-empty assistant delta/final creates
	// the visible item at its true canonical position.
	e.beginAssistantRequest(ev)
}

func (e *EventEncoder) applyAssistantDelta(ev runtimeevents.Event, cs *ChangeSet) {
	key := e.resolveAssistantRequestKey(ev)
	delta := payloadString(ev.Payload["delta"], "")
	if delta == "" {
		delta = payloadString(ev.Payload["content"], "")
	}
	if delta == "" {
		return
	}
	if _, retired := e.assistantTombstones[key]; retired {
		// An empty assistant was deliberately removed from the retained model.
		// Keep its request identity retired so a late delta cannot resurrect a
		// ghost assistant cell.
		e.stats.DuplicateCount++
		return
	}
	if current := e.assistantBy[key]; current != nil && current.Status.Terminal() {
		// A delta arriving after the authoritative final belongs to a retired
		// request. Do not create a fresh ordering barrier (or a duplicate
		// assistant cell) for it.
		e.stats.DuplicateCount++
		return
	}
	seq, hasSeq := assistantSequence(ev.Payload)
	it := e.assistantBy[key]
	if it == nil {
		// The first visible delta creates the assistant after any completed
		// reasoning item. Finalize a pending reasoning first so the change
		// order mirrors the semantic render order (reasoning completed upsert
		// precedes the assistant append); otherwise the Scene mapper would
		// materialize the assistant cell before the reasoning cell.
		if e.reasoningBy[key] != nil && !e.reasoningBarriers[key] {
			e.finalizeReasoningBeforeAssistant(key, cs)
		}
		it = e.appendItem(KindAssistant, "", "")
		it.BoundaryGroupKey = key
		setAssistantPresentation(it)
		e.assistantBy[key] = it
		e.change(cs, OpAppend, it)
	}
	if e.reasoningBy[key] != nil && !e.reasoningBarriers[key] {
		e.finalizeReasoningBeforeAssistant(key, cs)
	}
	if it.Status.Terminal() {
		// final 后到达的 delta：丢弃（对齐旧终端 HasRenderedAssistantFinal）。
		e.stats.DuplicateCount++
		return
	}
	// legacy 路径：无 sequence 身份时按到达顺序直接拼接（兼容旧 provider）。
	if !hasSeq || seq == 0 {
		u, changed := e.upsertItem(it.ID, KindAssistant, func(t *Item) bool {
			if t.Head == "" {
				t.Head = delta
			} else {
				t.Head += delta
			}
			t.Status = StatusRunning
			setAssistantPresentation(t)
			return true
		})
		if changed {
			e.change(cs, OpUpsert, u)
		}
		return
	}
	order := e.streamOrder[key]
	if order == nil {
		order = &assistantStreamOrder{nextSeq: 1, pending: make(map[uint64]assistantPendingDelta)}
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
	coalescedFrom, coalesced := assistantCoalescedFrom(ev.Payload)
	if coalesced && coalescedFrom < order.nextSeq {
		// The merged interval overlaps already committed content; skip the
		// whole block instead of partially re-applying its text.
		e.stats.DuplicateCount++
		return
	}
	if seq > order.nextSeq {
		if !(coalesced && coalescedFrom == order.nextSeq) {
			cacheKey := seq
			if coalesced {
				cacheKey = coalescedFrom
			}
			if _, dup := order.pending[cacheKey]; dup {
				e.stats.DuplicateCount++
				return
			}
			order.pending[cacheKey] = assistantPendingDelta{text: delta, endSeq: seq}
			order.pendingText += len(delta)
			e.stats.OutOfOrderCount++
			if len(order.pending) >= assistantStreamPendingLimit ||
				order.pendingText > assistantStreamPendingByteLimit {
				order.pending = make(map[uint64]assistantPendingDelta)
				order.pendingText = 0
				order.tainted = true
			}
			return
		}
	}
	// seq == nextSeq：提交本段并连续补拼 pending（有序拼接，不丢信息）。
	head := delta
	if coalesced && coalescedFrom == order.nextSeq && seq >= coalescedFrom {
		order.nextSeq = seq + 1
	} else {
		order.nextSeq++
	}
	for {
		p, ok := order.pending[order.nextSeq]
		if !ok {
			break
		}
		head += p.text
		delete(order.pending, order.nextSeq)
		order.nextSeq = p.endSeq + 1
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
		setAssistantPresentation(t)
		return true
	})
	if changed {
		e.change(cs, OpUpsert, u)
	}
}

func (e *EventEncoder) applyAssistantFinal(ev runtimeevents.Event, cs *ChangeSet) {
	key := e.resolveAssistantRequestKey(ev)
	if _, retired := e.assistantTombstones[key]; retired {
		// A removed empty assistant is terminal too. In particular, a late
		// non-empty authoritative snapshot must not recreate the cell.
		e.stats.DuplicateCount++
		return
	}
	if assistant := e.assistantBy[key]; assistant != nil && assistant.Status.Terminal() {
		// The first assistant.message owns the terminal snapshot for this exact
		// request. A retransmission must not apply a conflicting embedded
		// reasoning snapshot before the generic terminal upsert guard runs.
		e.stats.DuplicateCount++
		return
	}
	if block := runtimetypes.ReasoningBlockFromMap(ev.Payload["reasoning"]); block != nil {
		e.applyFinalReasoningSnapshot(key, block.RawDisplayText(), cs)
	}
	text := payloadString(ev.Payload["content"], payloadString(ev.Payload["message"], ""))
	e.removeReasoningBarrier(key, cs)
	e.finalizeReasoningBeforeAssistant(key, cs)
	it := e.assistantBy[key]
	if it == nil {
		if strings.TrimSpace(text) == "" {
			// 孤儿 final 没有可见内容（reasoning-only turn 的 assistant 块常为空）：
			// 不落空 assistant 项，否则渲染层会把它画成孤立一行的 "• "，
			// 表现为 "end reasoning" 分隔线之后、tool 结果之前的一行空输出。
			e.retireAssistant(key)
			return
		}
		it = e.appendItem(KindAssistant, "", text)
		it.BoundaryGroupKey = key
		setAssistantPresentation(it)
		it.Status = StatusCompleted // 孤儿 final：直接终态（不保持 pending）
		e.assistantBy[key] = it
		e.change(cs, OpAppend, it)
		return
	}
	// 流结束：补拼乱序缓冲中尚未提交的 delta（不丢信息）。
	e.flushAssistantStream(key, it, cs)
	if e.dropEmptyAssistant(key, text, cs) {
		return
	}
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
		if setAssistantPresentation(t) {
			changed = true
		}
		return changed
	})
	if changed {
		e.change(cs, OpUpsert, u)
	}
}

// applyFinalReasoningSnapshot applies reasoning carried by assistant.message to
// the existing canonical reasoning item before that item is finalized. The
// snapshot is authoritative source, not a new presentation block: identity and
// placement stay unchanged, ordering state is retired, and an already-terminal
// item is never regressed to running.
func (e *EventEncoder) applyFinalReasoningSnapshot(key, text string, cs *ChangeSet) {
	if e == nil || cs == nil || key == "" {
		return
	}
	delete(e.reasoningOrder, key)
	if text == "" {
		return
	}
	if it := e.reasoningBy[key]; it != nil {
		update := func(t *Item) bool {
			changed := false
			t.BoundaryGroupKey = key
			if t.Head != text {
				t.Head = text
				changed = true
			}
			if setReasoningPresentation(t) {
				changed = true
			}
			return changed
		}
		var (
			u       *Item
			changed bool
		)
		if it.Status.Terminal() {
			u, changed = e.correctTerminalReasoningItem(it.ID, update)
		} else {
			u, changed = e.upsertItem(it.ID, KindReasoning, update)
		}
		if changed {
			op := OpUpsert
			if u.Status.Terminal() {
				op = OpCorrectReasoning
			}
			e.change(cs, op, u)
		}
		e.removeReasoningBarrier(key, cs)
		return
	}

	var it *Item
	if assistant := e.assistantBy[key]; assistant != nil {
		it = e.insertItemBefore(assistant.ID, KindReasoning, text)
		it.BoundaryGroupKey = key
		setReasoningPresentation(it)
		e.changeBefore(cs, OpAppend, it, assistant.ID)
	} else {
		it = e.appendItem(KindReasoning, "", text)
		it.BoundaryGroupKey = key
		setReasoningPresentation(it)
		e.change(cs, OpAppend, it)
	}
	e.reasoningBy[key] = it
	e.removeReasoningBarrier(key, cs)
}

// dropEmptyAssistant 在提交终态前检查 assistant 项是否没有任何可见内容：
// Head 与传入的终态文本均为空/纯空白。此时项不是内容（reasoning-only
// turn 的 assistant 块、纯空白 delta 流），直接移除而不是提交成终态 cell
// —— 与 removeReasoningBarrier 的空占位清理同源，否则渲染层会把空项画成
// 孤立一行的 "• "，出现在 "end reasoning" 分隔线与后续 tool 结果之间。
// 返回 true 表示已移除，调用方应跳过终态 upsert。
func (e *EventEncoder) dropEmptyAssistant(key, text string, cs *ChangeSet) bool {
	it := e.assistantBy[key]
	if it == nil || it.Status.Terminal() {
		return false
	}
	if strings.TrimSpace(it.Head) != "" || strings.TrimSpace(text) != "" {
		return false
	}
	e.retireAssistant(key)
	delete(e.assistantBy, key)
	e.removeItem(it.ID, cs)
	return true
}

func (e *EventEncoder) retireAssistant(key string) {
	if e == nil || key == "" {
		return
	}
	if e.assistantTombstones == nil {
		e.assistantTombstones = make(map[string]struct{})
	}
	e.assistantTombstones[key] = struct{}{}
}

func (e *EventEncoder) applyLLMFinished(ev runtimeevents.Event, cs *ChangeSet) {
	key := e.resolveAssistantRequestKey(ev)
	if key != "" {
		e.requestFinished[key] = true
	}

	if llmRequestFailed(ev) {
		// A failed request has no authoritative assistant.message to wait for.
		// Preserve any partial body, close it as failed, then append one readable
		// semantic error. The raw lifecycle event name is never transcript data.
		e.finalizeRequestStream(key, StatusFailed, cs)
		e.appendLLMRequestFailure(key, ev, cs)
		return
	}

	// Success is only a transport boundary. Production emits the authoritative
	// assistant.message after llm.request.finished, and that final snapshot may
	// omit step while retaining stream_id. Flush buffered deltas and close the
	// reasoning predecessor, but keep assistant mutable until assistant.message
	// (or the session/EndRun fallback) commits it.
	// native-history ordering fence：request finished 后、authoritative final
	// 未到、reasoning 也未到——此时可能收到迟到的 reasoning（assistant.
	// reasoning 事件）。在 assistant 前保留一个空 reasoning 占位 cell（经
	// reasoningBy 命中并在 reasoning 到达时填充），并阻塞 assistant 提交
	// native history；assistant.message 提交时解除。
	if e.orderingBarrierEnabled {
		if assistant := e.assistantBy[key]; assistant != nil && !assistant.Status.Terminal() &&
			e.reasoningBy[key] == nil && e.requestFinished[key] {
			e.markReasoningBarrier(key, assistant, cs)
		}
	}
	if !e.reasoningBarriers[key] {
		e.finalizeReasoning(key, StatusCompleted, cs)
	}
	if it := e.assistantBy[key]; it != nil && !it.Status.Terminal() {
		e.flushAssistantStream(key, it, cs)
	}
}

func (e *EventEncoder) finalizeReasoningBeforeAssistant(key string, cs *ChangeSet) {
	e.finalizeReasoning(key, StatusCompleted, cs)
}

func (e *EventEncoder) finalizeReasoning(key string, status ItemStatus, cs *ChangeSet) {
	if e == nil || cs == nil {
		return
	}
	if !status.Terminal() {
		status = StatusCompleted
	}
	r := e.reasoningBy[key]
	if r == nil || r.Status.Terminal() {
		return
	}
	if u, changed := e.upsertItem(r.ID, KindReasoning, func(t *Item) bool {
		changed := false
		if t.Status != status {
			t.Status = status
			changed = true
		}
		// Finalization changes lifecycle only. Opening/closing divider rows are
		// presentation chrome derived from KindReasoning and terminal status.
		return changed
	}); changed {
		e.change(cs, OpUpsert, u)
	}
}

func (e *EventEncoder) finalizeRequestStream(key string, status ItemStatus, cs *ChangeSet) {
	if e == nil || cs == nil {
		return
	}
	if !status.Terminal() {
		status = StatusCompleted
	}
	// Keep transcript order deterministic: the reasoning predecessor reaches
	// terminal state before the assistant partial and its following error cell.
	if e.removeReasoningBarrier(key, cs) {
		// An empty barrier is an ordering aid only and must never become a
		// durable blank transcript cell on request failure.
	} else {
		e.finalizeReasoning(key, status, cs)
	}
	it := e.assistantBy[key]
	if it == nil || it.Status.Terminal() {
		return
	}
	e.flushAssistantStream(key, it, cs)
	if e.dropEmptyAssistant(key, "", cs) {
		return
	}
	if u, changed := e.upsertItem(it.ID, KindAssistant, func(t *Item) bool {
		t.Status = status
		return true
	}); changed {
		e.change(cs, OpUpsert, u)
	}
}

func (e *EventEncoder) appendLLMRequestFailure(key string, ev runtimeevents.Event, cs *ChangeSet) {
	if e == nil || cs == nil {
		return
	}
	if key != "" {
		if e.requestFailureBy[key] != nil {
			e.stats.DuplicateCount++
			return
		}
	}
	it := e.appendItem(KindSystem, "", llmRequestFailureHead(ev))
	it.Status = StatusCompleted
	e.change(cs, OpAppend, it)
	if key != "" {
		e.requestFailureBy[key] = it
	}
}

func llmRequestFailed(ev runtimeevents.Event) bool {
	if ev.Payload == nil {
		return false
	}
	if value, reported := ev.Payload["success"]; reported && !payloadBoolValue(value) {
		return true
	}
	return strings.TrimSpace(payloadString(ev.Payload["error"], "")) != ""
}

func llmRequestFailureHead(ev runtimeevents.Event) string {
	payload := ev.Payload
	title := "model error"
	attributes := make([]string, 0, 2)
	if code := strings.TrimSpace(payloadString(payload["error_code"], "")); code != "" {
		attributes = append(attributes, code)
	}
	if value, reported := payload["retryable"]; reported {
		attributes = append(attributes, "retryable="+strconv.FormatBool(payloadBoolValue(value)))
	}
	if len(attributes) > 0 {
		title += " [" + strings.Join(attributes, ", ") + "]"
	}
	if message := strings.TrimSpace(payloadString(payload["error"], "")); message != "" {
		title += " " + message
	}
	if nextAction := strings.TrimSpace(payloadString(payload["next_action"], "")); nextAction != "" {
		title += "\n[action] " + nextAction
	}
	return title
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
		e.removeReasoningBarrier(key, cs)
		e.flushAssistantStream(key, it, cs)
		if e.dropEmptyAssistant(key, "", cs) {
			continue
		}
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

	toolIDs := make([]string, 0, len(e.toolByID))
	for callID := range e.toolByID {
		toolIDs = append(toolIDs, callID)
	}
	sort.Strings(toolIDs)
	for _, callID := range toolIDs {
		it := e.toolByID[callID]
		if it == nil || it.Status.Terminal() {
			continue
		}
		u, changed := e.upsertItem(it.ID, KindToolCall, func(item *Item) bool {
			if item.Status.Terminal() {
				return false
			}
			item.Status = status
			item.Head = finalizeToolHeadAtRunEnd(item.Head, status)
			return true
		})
		if changed {
			e.change(cs, OpUpsert, u)
		}
	}
}

// finalizeToolHeadAtRunEnd 将 run 结束时仍未收到 tool.completed/failed 事件的
// 工具行从执行中态（"• Running ..."）转为终态标题（"• Completed ..."），避免
// 状态栏/transcript 在 turn 完成后残留 running 图标。与 applyToolFinished 的
// 区别：finalizeOpenStreams 没有事件 payload，无法附加 duration/backend 后缀，
// 因此只重建首行并保留 progress 累积的细节行；非 "• Running ..." 形态的 head
// （如直接以 display_head 建立的 head）保持原样，仅推进状态机。
func finalizeToolHeadAtRunEnd(head string, status ItemStatus) string {
	if head == "" {
		return head
	}
	label := "Completed"
	switch status {
	case StatusCanceled:
		label = "Canceled"
	case StatusFailed:
		label = "Failed"
	}
	first, rest := head, ""
	if newline := strings.IndexByte(first, '\n'); newline >= 0 {
		first, rest = first[:newline], first[newline:]
	}
	trimmed := strings.TrimSpace(first)
	if !strings.HasPrefix(trimmed, "• Running ") {
		return head
	}
	display := strings.TrimSpace(strings.TrimPrefix(trimmed, "• Running "))
	if display == "" {
		return head
	}
	return "• " + label + " " + display + rest
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
	name := payloadString(ev.Payload["tool_name"], payloadString(ev.Payload["logical_tool"], ev.ToolName))
	if callID == "" || name == "" {
		// A mutable tool cell must have both a stable call identity and a
		// readable semantic head. Without either, later progress/final events
		// cannot be attached safely, so preserve the event as a system fallback.
		e.applySystem(ev, cs)
		return
	}
	// A ReAct request that returns tool calls normally has no authoritative
	// assistant.message for this intermediate step. llm.request.finished is
	// only a transport boundary, so the assistant cell is otherwise left
	// mutable forever and becomes the first history barrier for every later
	// round. Close that request before mounting the tool cell; the final
	// assistant.message of the whole turn is correlated to its own step key.
	e.finalizeAssistantRequestBeforeTool(ev, cs)
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
	head := toolCallDisplayHead(ev)
	if head == "" {
		head = name
	}
	// 执行中 head 带 "• Running " 前缀（对齐旧 chat_tool_rendering 的
	// Running 行）：transcript 里工具开始执行即有可见状态；完成后由
	// applyToolFinished 替换首行为 "• Completed/Failed ..."。
	if !strings.HasPrefix(head, "• ") {
		head = "• Running " + head
	}
	it := e.appendItem(KindToolCall, "", head)
	if callID != "" {
		e.toolByID[callID] = it
	}
	e.change(cs, OpAppend, it)
}

func (e *EventEncoder) finalizeAssistantRequestBeforeTool(ev runtimeevents.Event, cs *ChangeSet) {
	if e == nil || cs == nil {
		return
	}
	key := e.resolveAssistantRequestKey(ev)
	if key == "" || !e.requestFinished[key] {
		return
	}
	if assistant := e.assistantBy[key]; assistant != nil && !assistant.Status.Terminal() {
		e.finalizeRequestStream(key, StatusCompleted, cs)
		return
	}
	if reasoning := e.reasoningBy[key]; reasoning != nil && !reasoning.Status.Terminal() {
		e.finalizeReasoning(key, StatusCompleted, cs)
	}
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
		// progress 基于 started 建立的 display head（首行）追加，避免被
		// payload tool_name 重置而丢失命令/参数摘要细节。
		next := t.Head
		if newline := strings.IndexByte(next, '\n'); newline >= 0 {
			next = next[:newline]
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
			} else if title := toolCallCompletedTitle(ev, t.Head); title != "" {
				// 无 display_head（direct legacy 投影）时恢复旧渲染的调用后
				// 标题（• Completed/Failed <display>[ via <backend>][ in <dur>]），
				// 首行替换为标题，保留 progress 阶段累积的细节行（不丢信息）。
				if newline := strings.IndexByte(t.Head, '\n'); newline >= 0 {
					t.Head = title + t.Head[newline:]
				} else {
					t.Head = title
				}
			}
			if looksLikeDiffPresentation(t.Head) {
				t.Presentation.Kind = PresentationDiffSupplement
				t.Presentation.DiffLabel = diffPresentationLabel(toolCallName(ev), t.Head)
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
	// 普通文本工具输出树形化（中间行竖线 "│"、末行收尾符号 "└"），
	// 与 legacy 工具块结果形态一致；diff 与 markdown 输出保留自身结构。
	treeText := output
	if !looksLikeDiffPresentation(output) && !markdown.LooksLikeMarkdown(output) {
		treeText = indentToolOutputTree(output)
	}
	out := e.appendItem(KindToolOutput, cause, treeText)
	if looksLikeDiffPresentation(output) {
		out.Presentation.Kind = PresentationDiffSupplement
		out.Presentation.DiffLabel = diffPresentationLabel(toolCallName(ev), output)
	}
	out.Status = StatusCompleted // 工具输出一次性完成（终态语义）
	e.change(cs, OpAppend, out)
}

// indentToolOutputTree 把工具输出文本行树形化：从第一行到倒数第二行用竖线
// "│" 前缀、最后一行用收尾符号 "└"（对齐 legacy 工具块树形结果块与期望的
// "● Read(...) / └ 27 lines" 形态）。单行输出保持原样。
func indentToolOutputTree(output string) string {
	lines := strings.Split(output, "\n")
	if len(lines) <= 1 {
		return output
	}
	var b strings.Builder
	b.Grow(len(output) + 6*len(lines))
	for i, line := range lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		marker := "│"
		if i == len(lines)-1 {
			marker = "└"
		}
		b.WriteString("  ")
		b.WriteString(marker)
		b.WriteString("  ")
		b.WriteString(line)
	}
	return b.String()
}

// looksLikeDiffPresentation recognizes the canonical textual formats accepted
// by ui/diff.RenderText. It deliberately records only semantic intent; width,
// syntax theme and terminal color depth are resolved later by layout.
func looksLikeDiffPresentation(text string) bool {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false
	}
	for _, line := range strings.Split(trimmed, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "• Edited ") || strings.HasPrefix(line, "• Diff ") {
			return true
		}
	}
	if strings.HasPrefix(trimmed, "diff --git ") || strings.Contains(trimmed, "\ndiff --git ") {
		return true
	}
	return strings.Contains("\n"+trimmed, "\n--- ") &&
		strings.Contains("\n"+trimmed, "\n+++ ") &&
		strings.Contains("\n"+trimmed, "\n@@ ")
}

// diffPresentationLabel returns the header verb the layout layer must use
// when projecting text marked as PresentationDiffSupplement. Supplement
// text ("• Edited "/"• Diff " prefixes) already carries its own label —
// ui/diff.ParseSupplementBlocks overrides the header — so only raw unified
// diffs need the encoder's annotation. Without it, a read-only `git diff`
// tool output would render under the diff renderer's default "Edited" verb.
func diffPresentationLabel(toolName, text string) string {
	normalized := strings.ReplaceAll(text, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	for _, line := range strings.Split(strings.TrimSpace(normalized), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "• Edited ") || strings.HasPrefix(line, "• Diff ") {
			return ""
		}
	}
	return diffHeaderLabelForTool(toolName)
}

// diffHeaderLabelForTool mirrors the legacy renderer's verb choice
// (commands.diffSupplementLabel): editing tools annotate "Edited", every
// other tool — shell git diff, read-only view — annotates "Diff". An empty
// name keeps the legacy compatibility default "Edited".
func diffHeaderLabelForTool(toolName string) string {
	name := strings.ToLower(strings.TrimSpace(toolName))
	if name == "" {
		return "Edited"
	}
	switch name {
	case "edit", "apply", "apply_patch", "patch", "multiedit", "write", "append_write":
		return "Edited"
	}
	if strings.HasSuffix(name, "/edit") || strings.HasSuffix(name, "/apply_patch") ||
		strings.HasSuffix(name, ".edit") || strings.HasSuffix(name, ".edit_file") ||
		strings.HasSuffix(name, ".apply_patch") || strings.HasSuffix(name, ".apply") {
		return "Edited"
	}
	return "Diff"
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
		tail += order.pending[s].text
	}
	order.pending = make(map[uint64]assistantPendingDelta)
	order.pendingText = 0
	if tail == "" {
		return
	}
	u, changed := e.upsertItem(it.ID, KindAssistant, func(t *Item) bool {
		t.Head += tail
		setAssistantPresentation(t)
		return true
	})
	if changed {
		e.change(cs, OpUpsert, u)
	}
}

// setAssistantPresentation keeps the semantic source and its renderer contract
// aligned throughout streaming and finalization. Ordinary multi-line prose is
// source-preserving plain text; only actual Markdown enters the structured
// renderer. Mixing those contracts would make an acknowledged active prefix
// differ from the finalized cell and cause the same message to be handed off
// to native history twice.
func setAssistantPresentation(it *Item) bool {
	if it == nil {
		return false
	}
	next := PresentationPlain
	if markdown.LooksLikeMarkdown(it.Head) {
		next = PresentationAssistantMarkdown
	}
	if it.Presentation.Kind == next && len(it.Presentation.Document.Blocks) == 0 {
		return false
	}
	it.Presentation = Presentation{Kind: next}
	return true
}

// ---- 辅助 ----

// assistantRequestIdentity describes every identity shape emitted for one LLM
// request. Production reasoning has turn+step but no stream_id; deltas and the
// request lifecycle have both; assistant_message has turn+stream_id but no
// step. A stateless key cannot join all three shapes without either splitting
// one response or merging multiple ReAct steps.
type assistantRequestIdentity struct {
	scopes    []string
	step      string
	streamID  string
	requestID string
}

const anonymousAssistantRequestScope = "\x00anonymous-assistant-request"

func (id assistantRequestIdentity) anonymous() bool {
	return len(id.scopes) == 0 && id.step == "" && id.streamID == "" && id.requestID == ""
}

func assistantRequestIdentityFromEvent(ev runtimeevents.Event) assistantRequestIdentity {
	id := assistantRequestIdentity{
		step:      strings.TrimSpace(payloadString(ev.Payload["step"], "")),
		streamID:  strings.TrimSpace(payloadString(ev.Payload["stream_id"], "")),
		requestID: strings.TrimSpace(payloadString(ev.Payload["llm_request_id"], "")),
	}
	for _, scope := range []string{
		payloadString(ev.Payload["turn_id"], ""),
		payloadString(ev.Payload["logical_turn_id"], ""),
		payloadString(ev.Payload["trace_id"], ""),
		ev.TraceID,
	} {
		id.scopes = appendUniqueIdentity(id.scopes, scope)
	}
	return id
}

func appendUniqueIdentity(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func (id assistantRequestIdentity) strongAliases() []string {
	aliases := make([]string, 0, 2)
	if id.requestID != "" {
		aliases = append(aliases, "request:"+id.requestID)
	}
	if id.streamID != "" {
		aliases = append(aliases, "stream:"+id.streamID)
	}
	return aliases
}

func (id assistantRequestIdentity) stepAliases() []string {
	if id.step == "" {
		return nil
	}
	aliases := make([]string, 0, len(id.scopes))
	for _, scope := range id.scopes {
		aliases = append(aliases, "step:"+scope+"/"+id.step)
	}
	return aliases
}

func (id assistantRequestIdentity) allAliases() []string {
	return append(id.strongAliases(), id.stepAliases()...)
}

func (id assistantRequestIdentity) newCanonicalKey(clock uint64) string {
	if steps := id.stepAliases(); len(steps) > 0 {
		return steps[0]
	}
	if strong := id.strongAliases(); len(strong) > 0 {
		return strong[0]
	}
	if len(id.scopes) > 0 {
		return "scope:" + id.scopes[0]
	}
	return "anonymous:" + strconv.FormatUint(clock, 10)
}

func (e *EventEncoder) resolveAssistantRequestKey(ev runtimeevents.Event) string {
	id := assistantRequestIdentityFromEvent(ev)
	if id.anonymous() {
		key := e.latestRequestByScope[anonymousAssistantRequestScope]
		if key != "" && anonymousEventStartsNewRequest(ev, e.assistantBy[key], e.reasoningBy[key]) {
			key = ""
		}
		if key == "" {
			key = id.newCanonicalKey(e.clock)
			e.latestRequestByScope[anonymousAssistantRequestScope] = key
		}
		return key
	}
	key := e.lookupAssistantRequestAlias(id.strongAliases())
	if key == "" {
		key = e.lookupAssistantRequestAlias(id.stepAliases())
	}
	// A final snapshot is allowed to omit both step and a previously observed
	// stream alias. The latest request in the same logical turn is then the only
	// sound association; step-bearing events never use this fallback.
	if key == "" && id.step == "" {
		for _, scope := range id.scopes {
			if latest := e.latestRequestByScope[scope]; latest != "" {
				key = latest
				break
			}
		}
	}
	if key == "" {
		key = id.newCanonicalKey(e.clock)
	}
	e.bindAssistantRequestAliases(key, id.allAliases(), false)
	e.rememberLatestAssistantRequest(id, key, false)
	return key
}

func (e *EventEncoder) beginAssistantRequest(ev runtimeevents.Event) string {
	id := assistantRequestIdentityFromEvent(ev)
	if id.anonymous() {
		key := e.latestRequestByScope[anonymousAssistantRequestScope]
		if key == "" || anonymousRequestTerminal(e.assistantBy[key], e.reasoningBy[key]) ||
			e.assistantTombstoned(key) {
			key = id.newCanonicalKey(e.clock)
			e.latestRequestByScope[anonymousAssistantRequestScope] = key
		}
		e.requestFinished[key] = false
		return key
	}
	strongKey := e.lookupAssistantRequestAlias(id.strongAliases())
	if strongKey != "" && !e.assistantTombstoned(strongKey) {
		key := strongKey
		e.bindAssistantRequestAliases(key, id.stepAliases(), false)
		e.rememberLatestAssistantRequest(id, key, true)
		return key
	}

	stepKey := e.lookupAssistantRequestAlias(id.stepAliases())
	if stepKey != "" && !e.requestFinished[stepKey] && !e.assistantTombstoned(stepKey) {
		e.bindAssistantRequestAliases(stepKey, id.strongAliases(), false)
		e.rememberLatestAssistantRequest(id, stepKey, true)
		return stepKey
	}

	key := id.newCanonicalKey(e.clock)
	if stepKey != "" {
		// A new transport/request identity for an already-finished step is a
		// retry, not a continuation of its terminal cell. Prefer a strong alias
		// as the new canonical key and repoint only the logical step aliases.
		if strong := id.strongAliases(); len(strong) > 0 {
			key = strong[0]
		} else {
			key += "#" + strconv.FormatUint(e.clock, 10)
		}
	}
	// A retry may reuse a logical step or transport alias after an empty
	// assistant was retired. Never let the new request inherit that tombstone's
	// canonical key; repoint the old strong alias to the fresh generation.
	if e.assistantTombstoned(key) {
		key += "#" + strconv.FormatUint(e.clock, 10)
	}
	e.bindAssistantRequestAliases(key, id.strongAliases(), strongKey != "")
	e.bindAssistantRequestAliases(key, id.stepAliases(), stepKey != "")
	e.requestFinished[key] = false
	e.rememberLatestAssistantRequest(id, key, true)
	return key
}

func anonymousRequestTerminal(assistant, reasoning *Item) bool {
	return (assistant != nil && assistant.Status.Terminal()) ||
		(assistant == nil && reasoning != nil && reasoning.Status.Terminal())
}

func (e *EventEncoder) assistantTombstoned(key string) bool {
	if e == nil || key == "" {
		return false
	}
	_, ok := e.assistantTombstones[key]
	return ok
}

func anonymousEventStartsNewRequest(ev runtimeevents.Event, assistant, reasoning *Item) bool {
	if !anonymousRequestTerminal(assistant, reasoning) {
		return false
	}
	return ev.Type == runtimechat.EventAssistantDelta || ev.Type == "assistant.delta" ||
		ev.Type == runtimechat.EventAssistantReasoning || ev.Type == "assistant.reasoning" ||
		ev.Type == runtimechat.EventAssistantMessage || ev.Type == "assistant.message"
}

func (e *EventEncoder) lookupAssistantRequestAlias(aliases []string) string {
	if e == nil {
		return ""
	}
	for _, alias := range aliases {
		if key := e.requestAliases[alias]; key != "" {
			return key
		}
	}
	return ""
}

func (e *EventEncoder) bindAssistantRequestAliases(key string, aliases []string, replace bool) {
	if e == nil || key == "" {
		return
	}
	if e.requestAliases == nil {
		e.requestAliases = make(map[string]string)
	}
	for _, alias := range aliases {
		if alias == "" {
			continue
		}
		if current := e.requestAliases[alias]; current == "" || current == key || replace {
			e.requestAliases[alias] = key
		}
	}
}

func (e *EventEncoder) rememberLatestAssistantRequest(id assistantRequestIdentity, key string, force bool) {
	if e == nil || key == "" {
		return
	}
	for _, scope := range id.scopes {
		if force || id.step != "" || e.latestRequestByScope[scope] == "" {
			e.latestRequestByScope[scope] = key
		}
	}
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
	// render_output 优先：edit/apply_patch 等工具携带完整 fenced diff，
	// summary/summary_lines 只是 3 行截断预览，不能作为终态输出。
	for _, key := range []string{"render_output", "output", "result", "summary"} {
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

// toolCallDisplayHead 构建工具调用期间（Running/调用前）tool cell 的可读
// 语义头。对齐旧 compactToolDisplayTextWithSource 的文本格式：
//   - shell 类工具显示命令文本（command_text，其次 arg_preview 的 command= 摘要）
//   - 其他工具显示 "工具名 + arg_preview"
//   - 无细节时退化为工具名
//   - 带 [meta]/[mcp]/[broker] 来源前缀
func toolCallDisplayHead(ev runtimeevents.Event) string {
	name := toolCallName(ev)
	if name == "" {
		return ""
	}
	commandText := chatToolDisplaySegment(payloadString(ev.Payload["command_text"], ""))
	argPreview := chatToolDisplaySegment(payloadString(ev.Payload["arg_preview"], ""))
	display := ""
	if runtimepolicy.IsShellLikeToolName(name) {
		if command := firstNonEmptyChatToolSegment(commandText, chatToolCommandFromPreview(argPreview)); command != "" {
			display = command
		}
	} else if argPreview != "" {
		display = name + " " + argPreview
	}
	if display == "" {
		display = name
	}
	display = truncateChatToolText(display, 200)
	if source := payloadString(ev.Payload[runtimetoolresult.SourceKey], ""); source != "" {
		if prefix := chatToolSourcePrefix(source); prefix != "" {
			return prefix + display
		}
	}
	return display
}

// toolCallCompletedTitle 构建工具调用完成后的 tool cell 终态标题（调用后
// 渲染）。对齐旧 compactToolCompletionTitle："• Completed <display>
// [via <backend>][ in <duration>]"，error 非空时为 "• Failed"。
// display 优先取 head 首行（started 建立的调用前摘要，含命令/参数细节），
// 无 head 时回退从 payload 重建。无 display 时返回空，保持既有 head。
func toolCallCompletedTitle(ev runtimeevents.Event, head string) string {
	display := ""
	if head != "" {
		display = head
		if newline := strings.IndexByte(display, '\n'); newline >= 0 {
			display = display[:newline]
		}
		// started 建立的 head 首行带 "• Running " 前缀，构造终态标题前剥掉，
		// 避免产出 "• Completed • Running ..."。
		display = strings.TrimSpace(strings.TrimPrefix(display, "• Running "))
	}
	if display == "" {
		display = toolCallDisplayHead(ev)
	}
	if display == "" {
		return ""
	}
	status := "Completed"
	if strings.TrimSpace(payloadString(ev.Payload["error"], "")) != "" {
		status = "Failed"
	}
	title := "• " + status + " " + display
	if backend := chatToolBackendSuffix(ev); backend != "" {
		title += backend
	}
	if durationSuffix := chatToolDurationSuffix(ev); durationSuffix != "" {
		title += durationSuffix
	}
	return title
}

// toolCallName 优先 payload 的 tool_name（chat actor 通道），其次
// logical_tool（agent runtime 通道），最后事件携带的 ToolName。
func toolCallName(ev runtimeevents.Event) string {
	return payloadString(ev.Payload["tool_name"], payloadString(ev.Payload["logical_tool"], ev.ToolName))
}

func chatToolDisplaySegment(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
}

func chatToolCommandFromPreview(argPreview string) string {
	argPreview = strings.TrimSpace(argPreview)
	if !strings.HasPrefix(argPreview, "command=") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(argPreview, "command="))
}

func firstNonEmptyChatToolSegment(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func truncateChatToolText(text string, limit int) string {
	if limit <= 0 || len([]rune(text)) <= limit {
		return text
	}
	return strings.TrimSpace(string([]rune(text)[:limit])) + "..."
}

func chatToolSourcePrefix(source string) string {
	switch runtimetoolresult.NormalizeSource(source) {
	case runtimetoolresult.SourceMeta:
		return "[meta] "
	case runtimetoolresult.SourceMCP:
		return "[mcp] "
	case runtimetoolresult.SourceBroker:
		return "[broker] "
	default:
		return ""
	}
}

func chatToolBackendSuffix(ev runtimeevents.Event) string {
	backend := payloadString(ev.Payload["execution_backend"], "")
	if backend == "" {
		backend = payloadString(ev.Payload["engine"], "")
	}
	backend = chatToolDisplaySegment(backend)
	if backend == "" {
		return ""
	}
	return " via " + truncateChatToolText(backend, 40)
}

func chatToolDurationSuffix(ev runtimeevents.Event) string {
	durationMs := payloadInt64Value(ev.Payload["duration_ms"])
	if durationMs <= 0 {
		return ""
	}
	return " in " + (time.Duration(durationMs) * time.Millisecond).String()
}

func payloadInt64Value(value interface{}) int64 {
	switch typed := value.(type) {
	case int:
		return int64(typed)
	case int64:
		return typed
	case int32:
		return int64(typed)
	case uint64:
		return int64(typed)
	case uint32:
		return int64(typed)
	case float64:
		return int64(typed)
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			return parsed
		}
	}
	return 0
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
	return assistantSequenceValue(payload["sequence"])
}

func assistantSequenceValue(value interface{}) (uint64, bool) {
	switch value := value.(type) {
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

func assistantCoalescedFrom(payload map[string]interface{}) (uint64, bool) {
	if payload == nil {
		return 0, false
	}
	from, ok := assistantSequenceValue(payload[StreamCoalescedFromKey])
	return from, ok && from > 0
}
