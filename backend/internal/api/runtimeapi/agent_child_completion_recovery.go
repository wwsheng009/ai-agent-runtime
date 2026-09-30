package runtimeapi

import (
	"context"
	"math"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// 2026-09-30 现场（CLI 侧 W6=session_20260930105807_4OT9C2lq 同源缺口）：子会话
// 终态 → 父会话完成投影的订阅是**进程内存态**，只在 spawn 时登记
//（session_runtime_support.go 的 subscribeAgentCompletion）。宿主重启后，被
// resume/恢复续跑的子代理再次结束时，新进程里没有任何监听者：mailbox
// `subagent.completed`、supervision 通知、wake 全部不产生，父会话永久 idle。
//
// 本文件是 CLI 宿主 chat_actor_child_completion_recovery.go 的 API 对等实现，
// 挂在物化路径（materializeAgentControlAgentProjections）上，每个 root 每进程
// 收敛一次：
//   - 重建完成订阅（P0-A）：之后的终态由实时订阅处理；
//   - 补投影（P0-B）：把重启窗口内丢失的终态补一次 mailbox + 通知 + run 收敛。
//
// 与 CLI 侧的差异：API 宿主服务多个 root，闸门按 root 记录；重放不追加父侧
// `subagent.completed` 镜像（那是实时通道的职责），父会话通过 mailbox 事件与
// supervision digest 拿到同一事实。幂等判据用 supervision 通知（subject=child）
// 的存在性，避免重复投影。

const (
	// apiChildCompletionReplayWindow 限制重放窗口（与 CLI 侧同口径）。
	apiChildCompletionReplayWindow = 48 * time.Hour
	// apiChildCompletionReplayScan 是子会话尾部事件扫描深度。
	apiChildCompletionReplayScan = 400
	// apiChildCompletionCandidateLimit 限制单次收敛扫描的子行数量。
	apiChildCompletionCandidateLimit = 200
)

// apiChildCompletionTailEventStore 是 chat.EventStore 的尾部读取扩展
// （SQLiteRuntimeStore / InMemoryRuntimeStore 均已实现，见 session_runtime_handlers.go
// 的同名口径）。
type apiChildCompletionTailEventStore interface {
	ListEventsBefore(ctx context.Context, sessionID string, beforeSeq int64, limit int) ([]runtimeevents.Event, error)
}

// recoverAgentChildCompletions 是 API 宿主的 P0-A/P0-B 收敛入口。它在物化
// （列表刷新 / 周期对账）时调用，对尚未收敛过的 root 做一次有界重建 + 补投影；
// 后续重复调用是空操作。
func (h *Handler) recoverAgentChildCompletions(ctx context.Context, store agentcontrol.AgentRegistryStore) {
	if h == nil || store == nil || h.sessionManager == nil || h.sessionManager.GetStorage() == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	tail, _ := h.getSessionEventStore().(apiChildCompletionTailEventStore)
	if tail == nil {
		// 无尾部读取能力的存储无法判定"最后一次终态"：宁缺勿错，不重放。
		return
	}
	records, err := store.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{
		IncludeClosed: true,
		Limit:         apiChildCompletionCandidateLimit,
	})
	if err != nil {
		return
	}
	byRoot := make(map[string][]agentcontrol.AgentRecord)
	for _, record := range records {
		record = record.Normalize()
		if record.RootSessionID == "" || record.SessionID == "" || record.AgentPath == "" || record.Depth <= 0 {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(record.SessionID), strings.TrimSpace(record.RootSessionID)) {
			continue
		}
		byRoot[record.RootSessionID] = append(byRoot[record.RootSessionID], record)
	}
	controller := &sessionAgentController{handler: h}
	for _, children := range byRoot {
		for _, record := range children {
			childSessionID := strings.TrimSpace(record.SessionID)
			if !h.claimAgentChildCompletionRecovery(childSessionID) {
				continue
			}
			if !h.recoverAgentChildCompletion(ctx, controller, tail, record) {
				// 子会话还没落盘（行先于会话写入）：释放名额，下一次物化重试。
				h.releaseAgentChildCompletionRecovery(childSessionID)
			}
		}
	}
}

// claimAgentChildCompletionRecovery 领取一个子会话的"每进程一次"重放名额。
func (h *Handler) claimAgentChildCompletionRecovery(childSessionID string) bool {
	childSessionID = strings.TrimSpace(childSessionID)
	if h == nil || childSessionID == "" {
		return false
	}
	h.childCompletionReplayMu.Lock()
	defer h.childCompletionReplayMu.Unlock()
	if h.childCompletionReplayChildren == nil {
		h.childCompletionReplayChildren = make(map[string]struct{})
	}
	if _, ok := h.childCompletionReplayChildren[childSessionID]; ok {
		return false
	}
	h.childCompletionReplayChildren[childSessionID] = struct{}{}
	return true
}

// releaseAgentChildCompletionRecovery 归还未完成的领取名额（会话未落盘时）。
func (h *Handler) releaseAgentChildCompletionRecovery(childSessionID string) {
	childSessionID = strings.TrimSpace(childSessionID)
	if h == nil || childSessionID == "" {
		return
	}
	h.childCompletionReplayMu.Lock()
	delete(h.childCompletionReplayChildren, childSessionID)
	h.childCompletionReplayMu.Unlock()
}

// recoverAgentChildCompletion 收敛一个子会话：重建完成订阅（P0-A）+ 把丢失的
// 终态补投影一次（P0-B）。返回 false 表示子会话尚未落盘（调用方归还名额）。
func (h *Handler) recoverAgentChildCompletion(ctx context.Context, controller *sessionAgentController, tail apiChildCompletionTailEventStore, record agentcontrol.AgentRecord) bool {
	if h == nil || controller == nil || tail == nil || h.sessionManager == nil {
		return true
	}
	childSessionID := strings.TrimSpace(record.SessionID)
	if childSessionID == "" {
		return true
	}
	session, err := h.sessionManager.GetStorage().Load(ctx, childSessionID)
	if err != nil || session == nil {
		return false
	}
	parentSessionID := apiChildParentSessionID(session)
	if parentSessionID == "" {
		return true
	}
	// P0-A：重建订阅（终态在会话结束/归档前都要能收到）。
	if session.State != chat.StateClosed && session.State != chat.StateArchived {
		controller.subscribeAgentCompletion(parentSessionID, session)
	}
	// P0-B：补投影。
	terminal, ok := apiChildLastTerminalEvent(ctx, tail, childSessionID)
	if !ok {
		return true
	}
	if !terminal.Timestamp.IsZero() && time.Since(terminal.Timestamp) > apiChildCompletionReplayWindow {
		return true
	}
	// 终态之后子会话又有新动作（例如已被 resume 并跑起新一轮）：该终态属于历史，
	// 不重放——避免给正在运行的子代理补一条"已完成"。
	if store := h.getSessionRuntimeStore(); store != nil {
		if state, stateErr := store.LoadState(ctx, childSessionID); stateErr == nil && state != nil {
			if !state.UpdatedAt.IsZero() && !terminal.Timestamp.IsZero() && state.UpdatedAt.After(terminal.Timestamp) {
				return true
			}
		}
	}
	if h.agentChildCompletionAlreadyProjected(ctx, childSessionID, terminal) {
		return true
	}
	status := agentCompletionStatus(terminal)
	eventType := strings.TrimSpace(terminal.Type)
	if eventType == "" {
		eventType = chat.EventSessionEnd
	}
	// 通知 + run 终态收敛 + wake 排程/投递（projectAgentCompletion 内部完成）。
	controller.projectAgentCompletion(ctx, parentSessionID, childSessionID, status, eventType)
	payload := map[string]interface{}{
		"agent_id":              childSessionID,
		"session_id":            childSessionID,
		"parent_session_id":     parentSessionID,
		"path":                  apiAgentSessionPath(session),
		"source_event_type":     eventType,
		"source_event_trace_id": strings.TrimSpace(terminal.TraceID),
		"status":                status,
		"source":                "agent_controller",
	}
	if !terminal.Timestamp.IsZero() {
		payload["source_event_timestamp"] = terminal.Timestamp.UTC().Format(time.RFC3339Nano)
	}
	copyAgentCompletionPayload(payload, terminal.Payload)
	childType := ""
	if value, ok := session.GetContext(toolbroker.AgentSessionContextAgentType); ok {
		if text, ok := value.(string); ok {
			childType = strings.TrimSpace(text)
		}
	}
	_, _ = controller.deliverSubagentCompletionMailbox(ctx, parentSessionID, childSessionID, apiAgentSessionPath(session), childType, eventType, payload)
	return true
}

// agentChildCompletionAlreadyProjected 判定该终态是否已经投影过：完成投影的
// durable 证据是 supervision 通知（subject=child，事件类型属于完成族），重放
// 只在"通知不存在或早于本次终态"时进行。
func (h *Handler) agentChildCompletionAlreadyProjected(ctx context.Context, childSessionID string, terminal runtimeevents.Event) bool {
	store := h.getSupervisionStore()
	if store == nil {
		return false
	}
	notifications, err := store.ListNotifications(ctx, supervision.NotificationFilter{
		SubjectKind:     supervision.SubjectAgentSession,
		SubjectID:       childSessionID,
		IncludeResolved: true,
	})
	if err != nil {
		return false
	}
	for _, notification := range notifications {
		switch strings.TrimSpace(notification.EventType) {
		case "agent_completed", "agent_failed", "agent_interrupted":
		default:
			continue
		}
		if terminal.Timestamp.IsZero() || notification.CreatedAt.IsZero() || !notification.CreatedAt.Before(terminal.Timestamp) {
			return true
		}
	}
	return false
}

// apiChildLastTerminalEvent 在子会话尾部事件里找最近一次终态。
func apiChildLastTerminalEvent(ctx context.Context, tail apiChildCompletionTailEventStore, childSessionID string) (runtimeevents.Event, bool) {
	events, err := tail.ListEventsBefore(ctx, childSessionID, math.MaxInt64, apiChildCompletionReplayScan)
	if err != nil {
		return runtimeevents.Event{}, false
	}
	for index := len(events) - 1; index >= 0; index-- {
		switch strings.TrimSpace(events[index].Type) {
		case chat.EventSessionEnd, chat.EventSessionInterrupted:
			return events[index], true
		}
	}
	return runtimeevents.Event{}, false
}

// apiChildParentSessionID 解析子会话的父会话 id（spawn 时写入 context 并随
// session storage 持久化；根会话没有该键）。
func apiChildParentSessionID(session *chat.Session) string {
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
