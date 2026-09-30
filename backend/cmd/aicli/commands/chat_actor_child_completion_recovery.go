package commands

import (
	"context"
	"math"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// 2026-09-30 现场（W6=session_20260930105807_4OT9C2lq）：子会话终态 → 父会话
// 完成投影的订阅是**进程内存态**，只在 spawn 时登记（subscribeLocalAgentCompletion
// 的调用点）。宿主重启后，被 resume/恢复续跑的子代理再次结束时，新进程里没有任何
// 监听者：mailbox `subagent.completed`、supervision 通知、wake 全部不产生，主 agent
// 永久 idle，只能等下一次自然 turn 的 preflight digest 或人工 /supervision wake。
// batch（spawn_subagents）有启动重放（localSubagentBatchStartupReplay），轻量子会话
// 没有 —— 本文件补上这两条收敛：
//
//  1. rebindLocalChildCompletionSubscriptions：宿主启动 + 子会话 resume 时为
//     「属于本宿主根会话树、且未终态」的子会话重建进程内完成订阅（未来不再丢）。
//  2. replayLocalChildCompletions：宿主启动时扫描子会话尾部事件，把「已有
//     session_end/session_interrupted 但父会话尚无对应完成投影」的子会话补投影
//     一次（过去/重启期间丢掉的终态不再依赖内存订阅；ProjectAgentCompletion 内部
//     同时收敛子会话的 execution run 与 run 级告警，覆盖 P1）。
//
// 重放的安全性依赖三处既有幂等：registry close 只动 active 行、completion 投影按
// subject 版本 upsert、mailbox 以确定性 message id 去重；父侧 subagent.completed
// 镜像事件由本文件的「已投影」闸门保证不重复追加。

const (
	// localChildCompletionReplayWindow 限制重放窗口：重启/崩溃窗口以小时计，
	// 超出窗口的历史终态不再补投影（避免启动期把陈年子代理全部翻出来）。
	localChildCompletionReplayWindow = 48 * time.Hour
	// localChildCompletionReplayScan 是子会话尾部事件扫描深度：终态事件总是
	// 会话的最后几条，400 条足以跨过一次 turn 的收尾写入。
	localChildCompletionReplayScan = 400
	// localChildCompletionMirrorScan 是父会话尾部扫描深度，用于判定"该终态
	// 是否已经投影过"（镜像事件带 agent_id + source_event_trace_id）。
	localChildCompletionMirrorScan = 200
	// localChildCompletionCandidateLimit 限制单次启动扫描的子行数量。
	localChildCompletionCandidateLimit = 200
	// localSubagentCompletionMirrorEventType 是父侧完成镜像事件的稳定类型名
	// （与 subscribeLocalAgentCompletion 的镜像一致）。
	localSubagentCompletionMirrorEventType = "subagent.completed"
)

// localChildCompletionTailEventStore 是 EventStore 的尾部读取扩展（内置的
// InMemoryRuntimeStore 与 SQLiteRuntimeStore 都已实现，见 chat_agent_transcript.go
// 的同名口径）：判定"最后一次终态"要的是最近一页事件，而不是会话开头的事件。
type localChildCompletionTailEventStore interface {
	ListEventsBefore(ctx context.Context, sessionID string, beforeSeq int64, limit int) ([]runtimeevents.Event, error)
}

func (h *localChatRuntimeHost) localChildCompletionRootSessionID() string {
	if h == nil || h.BaseSession == nil || h.BaseSession.RuntimeSession == nil {
		return ""
	}
	return strings.TrimSpace(h.BaseSession.RuntimeSession.ID)
}

// localChildParentSessionID 解析子会话的父会话 id（spawn 时写入 context 并随
// SessionStore 持久化；根会话没有该键）。
func localChildParentSessionID(session *runtimechat.Session) string {
	if session == nil {
		return ""
	}
	if value, ok := session.GetContext(toolbroker.AgentSessionContextParentSessionID); ok {
		if text, ok := value.(string); ok {
			return strings.TrimSpace(text)
		}
	}
	return ""
}

// localChildRootSessionID 解析子会话所属的根会话 id；缺失时回落到父会话 id
// （与 localAgentRootSessionID 的回落链同源）。
func localChildRootSessionID(session *runtimechat.Session, parentSessionID string) string {
	if session == nil {
		return strings.TrimSpace(parentSessionID)
	}
	if value, ok := session.GetContext(toolbroker.AgentSessionContextRootSessionID); ok {
		if text, ok := value.(string); ok {
			if trimmed := strings.TrimSpace(text); trimmed != "" {
				return trimmed
			}
		}
	}
	return strings.TrimSpace(parentSessionID)
}

// localChildCompletionCandidates 列出本宿主根会话树下的子代理行。包含终态行：
// 重启 sweep 可能已把仍在运行的子行标 stale/closed，行状态不能当作存活判据
// （2026-09-30 现场的 W6 行就是 closed 但在跑）。
func (h *localChatRuntimeHost) localChildCompletionCandidates(ctx context.Context) []agentcontrol.AgentRecord {
	if h == nil || h.AgentRegistryStore == nil {
		return nil
	}
	rootSessionID := h.localChildCompletionRootSessionID()
	if rootSessionID == "" {
		return nil
	}
	records, err := h.AgentRegistryStore.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{
		RootSessionID: rootSessionID,
		IncludeClosed: true,
		Limit:         localChildCompletionCandidateLimit,
	})
	if err != nil {
		return nil
	}
	candidates := make([]agentcontrol.AgentRecord, 0, len(records))
	for _, record := range records {
		record = record.Normalize()
		if record.SessionID == "" || record.AgentPath == "" {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(record.SessionID), rootSessionID) {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(record.AgentID), "root:"+rootSessionID) {
			continue
		}
		candidates = append(candidates, record)
	}
	return candidates
}

// rebindLocalChildCompletionSubscriptions 为属于本宿主根会话树的子会话重建完成
// 订阅（幂等：同 id 的旧句柄会被释放后替换）。返回重建数量，供启动日志/测试断言。
func (h *localChatRuntimeHost) rebindLocalChildCompletionSubscriptions(ctx context.Context) int {
	if h == nil || h.ActorRegistry == nil || h.SessionStore == nil {
		return 0
	}
	if ctx == nil {
		ctx = context.Background()
	}
	rootSessionID := h.localChildCompletionRootSessionID()
	if rootSessionID == "" {
		return 0
	}
	rebound := 0
	for _, record := range h.localChildCompletionCandidates(ctx) {
		session, err := h.SessionStore.Load(ctx, strings.TrimSpace(record.SessionID))
		if err != nil || session == nil {
			continue
		}
		if session.State == runtimechat.StateClosed || session.State == runtimechat.StateArchived {
			continue
		}
		parentSessionID := localChildParentSessionID(session)
		if parentSessionID == "" {
			continue
		}
		if !strings.EqualFold(localChildRootSessionID(session, parentSessionID), rootSessionID) {
			// 只重建属于本宿主根会话树的子代理（CLI 单宿主持有整棵树）。
			continue
		}
		h.ActorRegistry.subscribeLocalAgentCompletion(parentSessionID, session)
		rebound++
	}
	return rebound
}

// replayLocalChildCompletions 把「重启期间丢失的子会话终态」补投影一次。
//
// drainWake 与 batch 启动重放同款语义：交互式会话只补账本（mailbox + 通知），
// 不自动 drain wake，避免启动期偷跑一个隐藏 turn 顶掉首个 composer；headless
// 场景立即投递。返回补投影数量。
func (h *localChatRuntimeHost) replayLocalChildCompletions(ctx context.Context, drainWake bool) int {
	if h == nil || h.ActorRegistry == nil || h.SessionStore == nil || h.EventStore == nil {
		return 0
	}
	if ctx == nil {
		ctx = context.Background()
	}
	tail, _ := h.EventStore.(localChildCompletionTailEventStore)
	if tail == nil {
		// 无尾部读取能力的存储无法判定"最后一次终态"：宁缺勿错，不重放。
		return 0
	}
	rootSessionID := h.localChildCompletionRootSessionID()
	if rootSessionID == "" {
		return 0
	}
	now := time.Now().UTC()
	replayed := 0
	for _, record := range h.localChildCompletionCandidates(ctx) {
		childSessionID := strings.TrimSpace(record.SessionID)
		session, err := h.SessionStore.Load(ctx, childSessionID)
		if err != nil || session == nil {
			continue
		}
		parentSessionID := localChildParentSessionID(session)
		if parentSessionID == "" {
			continue
		}
		rootScopeID := localChildRootSessionID(session, parentSessionID)
		if !strings.EqualFold(rootScopeID, rootSessionID) {
			continue
		}
		terminal, ok := localChildLastTerminalEvent(ctx, tail, childSessionID)
		if !ok {
			continue
		}
		if !terminal.Timestamp.IsZero() && now.Sub(terminal.Timestamp) > localChildCompletionReplayWindow {
			continue
		}
		// 终态之后子会话又有新动作（例如已被 resume 并跑起新一轮）：该终态属于
		// 历史，不重放——避免给正在运行的子代理补一条"已完成"。
		if h.RuntimeStore != nil {
			if state, stateErr := h.RuntimeStore.LoadState(ctx, childSessionID); stateErr == nil && state != nil {
				if !state.UpdatedAt.IsZero() && !terminal.Timestamp.IsZero() && state.UpdatedAt.After(terminal.Timestamp) {
					continue
				}
			}
		}
		if h.localChildCompletionAlreadyProjected(ctx, tail, parentSessionID, childSessionID, strings.TrimSpace(terminal.TraceID)) {
			continue
		}
		h.ActorRegistry.handleLocalChildTerminal(ctx, parentSessionID, session, terminal, localChildEventMetaFor(session), true)
		replayed++
		if drainWake {
			if err := h.wakeSupervisedParent(ctx, parentSessionID, rootScopeID); err != nil {
				// busy / 预算耗尽都是 durable 控制面的正常结果：wake 保持 pending，
				// 由下一次 turn-end（P0-C）或自然 turn 的 preflight 排空。
				continue
			}
		}
	}
	return replayed
}

// localChildLastTerminalEvent 在子会话尾部事件里找最近一次终态（session_end /
// session_interrupted）。返回的事件带 payload.seq（读取层注入），因此重放生成的
// mailbox delivery key 与实时路径同源、可跨重启去重。
func localChildLastTerminalEvent(ctx context.Context, tail localChildCompletionTailEventStore, childSessionID string) (runtimeevents.Event, bool) {
	events, err := tail.ListEventsBefore(ctx, childSessionID, math.MaxInt64, localChildCompletionReplayScan)
	if err != nil {
		return runtimeevents.Event{}, false
	}
	for index := len(events) - 1; index >= 0; index-- {
		switch strings.TrimSpace(events[index].Type) {
		case runtimechat.EventSessionEnd, runtimechat.EventSessionInterrupted:
			return events[index], true
		}
	}
	return runtimeevents.Event{}, false
}

// localChildCompletionAlreadyProjected 判定父会话是否已经收到过该终态的完成投影：
// 完成投影的三个产物在同一处理器里写出，父侧 subagent.completed 镜像带
// agent_id + source_event_trace_id，是最便宜且稳定的判据（避免重放重复追加镜像
// 事件污染父会话 transcript）。
func (h *localChatRuntimeHost) localChildCompletionAlreadyProjected(ctx context.Context, tail localChildCompletionTailEventStore, parentSessionID, childSessionID, traceID string) bool {
	events, err := tail.ListEventsBefore(ctx, parentSessionID, math.MaxInt64, localChildCompletionMirrorScan)
	if err != nil {
		return false
	}
	for index := len(events) - 1; index >= 0; index-- {
		event := events[index]
		if strings.TrimSpace(event.Type) != localSubagentCompletionMirrorEventType {
			continue
		}
		if !strings.EqualFold(localChildPayloadString(event.Payload, "agent_id"), childSessionID) {
			continue
		}
		if traceID != "" && !strings.EqualFold(localChildPayloadString(event.Payload, "source_event_trace_id"), traceID) {
			continue
		}
		return true
	}
	return false
}

func localChildPayloadString(payload map[string]interface{}, key string) string {
	if payload == nil {
		return ""
	}
	value, ok := payload[key]
	if !ok || value == nil {
		return ""
	}
	text, _ := value.(string)
	return strings.TrimSpace(text)
}
