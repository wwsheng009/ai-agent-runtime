package commands

import (
	"context"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

// localSuspensionUnavailableReasonInFlight 是「回合结束但仍有在途子会话、且没有
// 任何挂起记录」的 I9 降级原因。它复用既有 EventSubagentSuspensionUnavailable
// 事件（TUI 已渲染 "[subagents] suspension unavailable: <reason>"），让不可挂起
// 也不是静默失败。
const localSuspensionUnavailableReasonInFlight = "child agent sessions still running; durable turn parking unavailable for this turn"

// localAgentObligationSpawnNextAction 是 spawn_agent 成功登记挂起义务后给模型的
// 可见语义：wait_agent 会把它当 agent_session 义务行报告，直接收尾也不会丢工作
// （宿主挂起该 turn 并在子代理终态事件上自动续跑）。模型不知道这层语义时，常见
// 误用是重复派发同一份工作或空转轮询。
const localAgentObligationSpawnNextAction = "obligation_registered: this child session is now a durable turn obligation; wait_agent reports it as an agent_session row (pending until the child reaches a terminal state) — ending the turn is safe: the host parks the turn and auto-resumes it on the child's terminal event. Do not spawn the same work again."

// localAgentSessionObligationResolver judges the durable state of one child
// agent session from the same supervision notification plane that drives the
// auto-wake/resume path: ProjectAgentCompletion writes an
// agent_completed/agent_failed/agent_interrupted lifecycle row per child whose
// SupervisionState is terminated. Reading that row back (including resolved
// rows) keeps settle/restart semantics aligned with the control plane that
// actually decides whether the child is done — the agent registry identity row
// alone stays active until an explicit close/reclaim, so it cannot answer
// "did the child finish".
type localAgentSessionObligationResolver struct {
	store supervision.Store
}

func (r localAgentSessionObligationResolver) AgentSessionTerminal(ctx context.Context, sessionID string) (bool, bool, error) {
	sessionID = strings.TrimSpace(sessionID)
	if r.store == nil || sessionID == "" {
		return false, false, nil
	}
	rows, err := r.store.ListNotifications(ctx, supervision.NotificationFilter{
		SubjectKind:     supervision.SubjectAgentSession,
		SubjectID:       sessionID,
		IncludeResolved: true,
		Limit:           32,
	})
	if err != nil {
		return false, false, err
	}
	if len(rows) == 0 {
		// No durable lifecycle row yet: the child has not reached a terminal
		// projection (a missing row is never evidence of completion).
		return false, false, nil
	}
	for _, row := range rows {
		if row.SupervisionState == supervision.SupervisionTerminated {
			return true, true, nil
		}
	}
	return false, true, nil
}

// agentSessionObligationResolver returns the durable child-session reader for
// this host, or nil when the supervision control plane is not wired (in which
// case child-session obligations are never parked: a record that could never be
// settled must not be written).
func (h *localChatRuntimeHost) agentSessionObligationResolver() subagentbatch.AgentSessionObligationResolver {
	if h == nil || h.Supervision == nil || h.Supervision.Store == nil {
		return nil
	}
	return localAgentSessionObligationResolver{store: h.Supervision.Store}
}

// parkLocalAgentChildObligation records that the current parent turn handed
// work to one live spawn_agent child session and parks the turn on it (§6.12),
// mirroring parkBackgroundTurn for subagent batches. The obligation id is
// prefix-encoded (agent_session:<session_id>) so it shares the existing
// obligation_ids_json representation and needs no schema migration.
//
// Best-effort by contract: a missing durable batch store, a missing
// child-session resolver or a write error simply leaves the turn unparked (the
// child-completion wake still fires on the normal projection path, and the
// turn-end binder projects the degraded signal). It never fails the spawn; the
// bool return reports whether the obligation is durably recorded (used to give
// the model the wait/finalize semantics only when they actually hold).
func (h *localChatRuntimeHost) parkLocalAgentChildObligation(ctx context.Context, parentSessionID, childSessionID string) bool {
	if h == nil {
		return false
	}
	parentSessionID = strings.TrimSpace(parentSessionID)
	childSessionID = strings.TrimSpace(childSessionID)
	if parentSessionID == "" || childSessionID == "" {
		return false
	}
	if h.SubagentBatches == nil || !h.SubagentBatches.IsDurable() {
		return false
	}
	if h.agentSessionObligationResolver() == nil {
		// Without a durable judge the record could never settle; do not write
		// an unparkable suspension (I9 conservative direction).
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	turnID := strings.TrimSpace(agent.TurnIDFromContext(ctx))
	if turnID == "" {
		turnID = h.localTurnIDForSession(ctx, parentSessionID)
	}
	if turnID == "" {
		return false
	}
	record, _, err := h.SubagentBatches.GetTurnSuspension(ctx, parentSessionID, turnID)
	if err != nil {
		return false
	}
	if record == nil {
		record = &subagentbatch.TurnSuspension{TurnID: turnID, SessionID: parentSessionID}
	}
	// 记录在本 turn 已有内容（批次派发先挂起）时，loop.parkBackgroundTurn 已经
	// 播报过 turn.suspended；宿主侧只追加义务、不重复播报。空记录（理论上只可能
	// 出现在手写/历史数据）仍按首次挂起播报。
	firstPark := len(record.ObligationIDs) == 0 && len(record.ResumeQueue) == 0
	record.RootScopeID = firstNonEmptyChatValue(record.RootScopeID, parentSessionID)
	obligationID := subagentbatch.AgentSessionObligationID(childSessionID)
	for _, existing := range record.ObligationIDs {
		if existing == obligationID {
			return true
		}
	}
	record.ObligationIDs = append(record.ObligationIDs, obligationID)
	if record.ParkedAt.IsZero() {
		record.ParkedAt = time.Now().UTC()
	}
	if err := h.SubagentBatches.ParkTurnSuspension(ctx, record); err != nil {
		return false
	}
	if firstPark {
		h.announceLocalTurnSuspended(record)
	}
	return true
}

// localTurnIDForSession reads the durable turn identity of a session that is
// currently running (CurrentTurnID) or parked (SuspendedTurnID).
func (h *localChatRuntimeHost) localTurnIDForSession(ctx context.Context, sessionID string) string {
	if h == nil || h.RuntimeStore == nil {
		return ""
	}
	state, err := h.RuntimeStore.LoadState(ctx, sessionID)
	if err != nil || state == nil {
		return ""
	}
	if turnID := strings.TrimSpace(state.CurrentTurnID); turnID != "" {
		return turnID
	}
	return strings.TrimSpace(state.SuspendedTurnID)
}

// announceLocalTurnSuspended publishes one §6.8 turn.suspended edge for a
// host-side park. The payload mirrors loop.parkBackgroundTurn's keys plus
// agent_session_ids; turn_id is part of the dedup identity so consecutive
// suspensions of different turns cannot collapse into one UI line.
func (h *localChatRuntimeHost) announceLocalTurnSuspended(record *subagentbatch.TurnSuspension) {
	if h == nil || h.EventBus == nil || record == nil {
		return
	}
	if !h.claimParkedTurnEdge("suspended", record.SessionID, record.TurnID) {
		return
	}
	payload := map[string]interface{}{
		"turn_id":            record.TurnID,
		"session_id":         record.SessionID,
		"obligation_count":   len(record.ObligationIDs),
		"resume_queue_count": len(record.ResumeQueue),
		"parked_at":          record.ParkedAt.UTC().Format(time.RFC3339Nano),
	}
	if sessionIDs := record.ObligationAgentSessionIDs(); len(sessionIDs) > 0 {
		payload["agent_session_ids"] = sessionIDs
	}
	h.EventBus.Publish(runtimeevents.Event{
		Type:      runtimeevents.EventTurnSuspended,
		SessionID: record.SessionID,
		Payload:   payload,
	})
}

// claimParkedTurnEdge dedupes host-side park/in-flight announcements per
// (kind, session, turn). Returns true exactly once per key.
func (h *localChatRuntimeHost) claimParkedTurnEdge(kind, sessionID, turnID string) bool {
	if h == nil {
		return false
	}
	key := strings.TrimSpace(kind) + "|" + strings.TrimSpace(sessionID) + "|" + strings.TrimSpace(turnID)
	h.parkedTurnEdgeMu.Lock()
	defer h.parkedTurnEdgeMu.Unlock()
	if h.parkedTurnEdges == nil {
		h.parkedTurnEdges = make(map[string]bool)
	}
	if h.parkedTurnEdges[key] {
		return false
	}
	h.parkedTurnEdges[key] = true
	return true
}

// inFlightLocalChildSessions lists live spawn_agent children of one root
// session: active identity rows (the registry filter already excludes closed
// rows) whose child session has no terminal lifecycle projection yet. The
// resolver check keeps finished-but-unclosed children out of the list.
func (h *localChatRuntimeHost) inFlightLocalChildSessions(ctx context.Context, sessionID string) []string {
	if h == nil || h.AgentRegistryStore == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	records, err := h.AgentRegistryStore.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{RootSessionID: sessionID})
	if err != nil {
		return nil
	}
	resolver := h.agentSessionObligationResolver()
	seen := make(map[string]struct{}, len(records))
	children := make([]string, 0, len(records))
	for _, record := range records {
		childSessionID := strings.TrimSpace(record.SessionID)
		if childSessionID == "" || childSessionID == sessionID {
			continue
		}
		// spawn_agent 显式指定 agent_type 时，记录里存的是定义名（general/
		// explore/plan…），只有未指定才回落为 "child"——按 "child" 白名单过滤会
		// 漏掉最常见的带定义子代理（真机 2026-09-30：I9 兜底 0 事件）。改用树
		// 结构判据：depth<=0 排除 root 自身，root/team teammate 不是本会话的
		// 轻量子代理。
		if record.Depth <= 0 {
			continue
		}
		switch {
		case strings.EqualFold(strings.TrimSpace(record.AgentType), agentcontrol.AgentTypeRoot),
			strings.EqualFold(strings.TrimSpace(record.AgentType), agentcontrol.AgentTypeTeamTeammate):
			continue
		}
		if root := strings.TrimSpace(record.RootSessionID); root != "" && !strings.EqualFold(root, sessionID) {
			continue
		}
		if _, exists := seen[childSessionID]; exists {
			continue
		}
		if resolver != nil {
			if terminal, found, err := resolver.AgentSessionTerminal(ctx, childSessionID); err == nil && found && terminal {
				continue
			}
		}
		seen[childSessionID] = struct{}{}
		children = append(children, childSessionID)
	}
	return children
}

// signalLocalInFlightChildSessions projects the I9 degraded signal when a
// parent turn ends with live child sessions but no parked-turn record (parking
// unavailable / disabled / raced). Without it the session would show a plain
// "Worked for …" completion while work is still owed. Published only once per
// (session, turn).
func (h *localChatRuntimeHost) signalLocalInFlightChildSessions(ctx context.Context, sessionID, turnID string) {
	if h == nil || h.EventBus == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	turnID = strings.TrimSpace(turnID)
	if h.SubagentBatches != nil && turnID != "" {
		if record, ok, err := h.SubagentBatches.GetTurnSuspension(ctx, sessionID, turnID); err == nil && ok && record != nil {
			// The managed path already expressed the waiting state; no degraded
			// signal is needed.
			return
		}
	}
	children := h.inFlightLocalChildSessions(ctx, sessionID)
	if len(children) == 0 {
		return
	}
	if !h.claimParkedTurnEdge("inflight", sessionID, turnID) {
		return
	}
	h.EventBus.Publish(runtimeevents.Event{
		Type:      runtimeevents.EventSubagentSuspensionUnavailable,
		SessionID: sessionID,
		Payload: map[string]interface{}{
			"parent_session_id": sessionID,
			"root_scope_id":     sessionID,
			"turn_id":           turnID,
			"reason":            localSuspensionUnavailableReasonInFlight,
			"severity":          "warning",
			"child_count":       len(children),
			"child_session_ids": children,
		},
	})
}

// bindLocalInFlightChildSignal subscribes the root session's turn-end event:
// a turn that ends with live child sessions and no parked record must not look
// like a plain completion. This is independent of supervision.turn_end_check
// (that switch only controls whether pending wakes are drained at turn end, not
// whether waiting work is observable).
func (h *localChatRuntimeHost) bindLocalInFlightChildSignal() {
	if h == nil || h.EventBus == nil || h.BaseSession == nil || h.BaseSession.RuntimeSession == nil {
		return
	}
	rootSessionID := strings.TrimSpace(h.BaseSession.RuntimeSession.ID)
	if rootSessionID == "" {
		return
	}
	h.EventBus.SubscribeCancelable(runtimechat.EventSessionEnd, func(event runtimeevents.Event) {
		if event.Type != runtimechat.EventSessionEnd {
			return
		}
		if !strings.EqualFold(strings.TrimSpace(event.SessionID), rootSessionID) {
			return
		}
		turnID := strings.TrimSpace(payloadStringValue(event.Payload["turn_id"]))
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		h.signalLocalInFlightChildSessions(ctx, rootSessionID, turnID)
	})
}
