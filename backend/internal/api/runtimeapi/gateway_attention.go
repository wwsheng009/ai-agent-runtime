package runtimeapi

import (
	"context"
	"strings"
	"time"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	gatewayagent "github.com/wwsheng009/ai-agent-runtime/internal/gateway/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"
	"github.com/wwsheng009/ai-agent-runtime/internal/runtimeobserve"
)

// 本文件把 runtime-server 的运行时事件映射到共享会话状态核心
// （internal/gateway/agent.SessionStateStore，与 aicli 同一实现）：
// 状态词表、attention↔state 耦合、busy/终态（completed/interrupted/error）
// 转换与变更检测都归核心所有，这里只负责事件 → 核心调用的映射与推送时机。

// gatewayStateStore lazily creates and returns the shared session-state store.
func (h *Handler) gatewayStateStore() *gatewayagent.SessionStateStore {
	if h == nil {
		return nil
	}
	h.gatewayAttentionMu.Lock()
	defer h.gatewayAttentionMu.Unlock()
	if h.gatewaySessionStates == nil {
		h.gatewaySessionStates = gatewayagent.NewSessionStateStore()
	}
	return h.gatewaySessionStates
}

// gatewayTailThrottle lazily creates the shared live-tail throttle (1s
// cadence + duplicate suppression, same implementation as aicli).
func (h *Handler) gatewayTailThrottle() *gatewayagent.TailThrottle {
	if h == nil {
		return nil
	}
	h.gatewayAttentionMu.Lock()
	defer h.gatewayAttentionMu.Unlock()
	if h.gatewayTailThrottleInst == nil {
		h.gatewayTailThrottleInst = gatewayagent.NewTailThrottle(time.Second)
	}
	return h.gatewayTailThrottleInst
}

// EnableGatewayAttention subscribes to the runtime event bus and maintains the
// per-session state projection that GatewaySessions reports to the gateway.
// It is idempotent and safe to call when no gateway controller is attached.
func (h *Handler) EnableGatewayAttention() {
	if h == nil {
		return
	}
	h.gatewayAttentionOnce.Do(func() {
		bus := h.getRuntimeEventBus()
		if bus == nil {
			return
		}
		bus.Subscribe("", h.observeGatewayAttention)
	})
}

// observeGatewayAttention maps runtime events to the shared session-state
// projection. Only real changes trigger a gateway push, so a burst of deltas
// stays cheap.
func (h *Handler) observeGatewayAttention(event runtimeevents.Event) {
	sessionID := strings.TrimSpace(event.SessionID)
	if sessionID == "" {
		return
	}
	store := h.gatewayStateStore()
	if store == nil {
		return
	}
	var changed bool
	var notifyKind string
	switch event.Type {
	case runtimeobserve.EventAgentTurnStarted:
		h.clearGatewayTail(sessionID)
		changed = store.BeginTurn(sessionID, time.Now())
	case "assistant_delta", "assistant.delta":
		// Live output: accumulate the running text and push a bounded tail
		// through the shared throttle (≤1/s, unchanged never pushed).
		changed = h.appendGatewayTail(sessionID, gatewayEventTextDelta(event))
	case runtimeobserve.EventToolStarted:
		// Fine-grained activity: the console shows "Running <tool>" while the
		// call is in flight. The event timestamp anchors the elapsed clock.
		changed = store.SetSurface(sessionID, wire.SessionStateRunning,
			"Running "+gatewayToolDisplayName(event), event.Timestamp, time.Now())
	case runtimeobserve.EventToolFinished, runtimeobserve.EventToolFailed:
		// The tool line is stale once the call returns: fall back to the
		// generic running state; the next phase (llm request / turn end)
		// updates it again.
		changed = store.SetSurface(sessionID, wire.SessionStateRunning, "", event.Timestamp, time.Now())
	case runtimeobserve.EventLLMRequestStart, runtimeobserve.EventLLMAttemptStart:
		changed = store.SetSurface(sessionID, wire.SessionStateAnswering,
			"Generating response", event.Timestamp, time.Now())
	case runtimeobserve.EventLLMRetry:
		changed = store.SetSurface(sessionID, wire.SessionStateRunning,
			"Retrying", event.Timestamp, time.Now())
	case runtimeobserve.EventAgentTurnDone:
		h.clearGatewayTail(sessionID)
		if h.sessionHasParkedTurnRecord(sessionID) {
			// §6.12 托管挂起：run 结束 ≠ 托管 turn 结束（与 CLI 宿主同口径）。
			// 仍有 durable 挂起记录时投影 waiting 而不是 completed——否则控制台
			// 显示"已完成/待处理"并推送"回合完成"，与仍在等待子代理的事实冲突
			// （docs/gateway/08-notifications.md §3）。
			changed = h.markGatewayParkedTurn(sessionID)
			notifyKind = wire.NotifyKindWaiting
			break
		}
		changed = store.EndTurn(sessionID, wire.SessionStateCompleted)
		notifyKind = wire.NotifyKindTurnCompleted
	case runtimechat.EventApprovalRequested:
		changed = store.SetAttention(sessionID, gatewayagent.AttentionApproval)
		notifyKind = wire.NotifyKindApproval
	case runtimechat.EventQuestionAsked:
		changed = store.SetAttention(sessionID, gatewayagent.AttentionQuestion)
		notifyKind = wire.NotifyKindQuestion
	case runtimechat.EventApprovalResolved, runtimechat.EventQuestionAnswered:
		changed = store.SetAttention(sessionID, "")
	case runtimeevents.EventTurnSuspended:
		// docs/gateway/08-notifications.md §3：托管挂起是持久注意力状态
		// （attention=waiting + 等待子任务文案）。
		changed = h.markGatewayParkedTurn(sessionID)
	case runtimeevents.EventTurnResumed:
		// 恢复清除挂起注意力；只清 waiting 自己，审批/提问徽标不受影响。
		changed = h.clearGatewayParkedAttention(sessionID)
	case runtimechat.EventSessionInterrupted:
		// User cancel: keep the row visible with an interrupted state instead
		// of silently clearing it (the operator can still see what happened).
		h.clearGatewayTail(sessionID)
		changed = store.EndTurn(sessionID, wire.SessionStateInterrupted)
	case runtimechat.EventSessionEnd:
		h.clearGatewayTail(sessionID)
		changed = store.Clear(sessionID)
	}
	if changed {
		h.pushGatewaySessions()
		if notifyKind != "" {
			h.notifyGateway(wire.Notify{
				SessionID: sessionID,
				Kind:      notifyKind,
				Priority:  notifyPriorityForKind(notifyKind),
				TS:        time.Now().UTC(),
			})
		}
	}
}

// markGatewayParkedTurn projects a parked managed turn onto the gateway axes
// (docs/gateway/08-notifications.md §3: attention=waiting plus the shared
// "Waiting for subagents" activity line). It reports whether anything changed.
func (h *Handler) markGatewayParkedTurn(sessionID string) bool {
	if h == nil {
		return false
	}
	store := h.gatewayStateStore()
	if store == nil {
		return false
	}
	now := time.Now()
	changed := store.SetAttention(sessionID, gatewayagent.AttentionWaiting)
	if store.SetSurface(sessionID, wire.SessionStateRunning, gatewayagent.StatusTextWaitingSubagents, now, now) {
		changed = true
	}
	return changed
}

// clearGatewayParkedAttention clears the waiting badge only when it is the one
// currently set: approval/question badges own the attention axis.
func (h *Handler) clearGatewayParkedAttention(sessionID string) bool {
	if h == nil {
		return false
	}
	store := h.gatewayStateStore()
	if store == nil {
		return false
	}
	if store.Get(sessionID).Attention != gatewayagent.AttentionWaiting {
		return false
	}
	return store.SetAttention(sessionID, "")
}

// sessionHasParkedTurnRecord reports whether the durable batch control plane
// still holds a parked turn for the session (§6.12) — the same source of truth
// the CLI host uses for its completed-freeze decision. Fail-open: a missing
// store or a read error answers false, degrading to the previous behavior.
func (h *Handler) sessionHasParkedTurnRecord(sessionID string) bool {
	if h == nil {
		return false
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return false
	}
	batches := h.peekSubagentBatchStore()
	if batches == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	records, err := batches.ListTurnSuspensions(ctx, sessionID)
	if err != nil {
		return false
	}
	return len(records) > 0
}

// markGatewaySubagentFailure projects a failed / timed-out / orphaned child
// batch onto the parent session's attention axis and pushes the high-priority
// error notification (docs/gateway/08-notifications.md §3 的失败行)。父会话稍后
// 会被 wake 唤醒处理，但控制台在此之前就应看到 error 徽标；父会话下一轮
// BeginTurn 会清除它。
func (h *Handler) markGatewaySubagentFailure(parentSessionID string) bool {
	if h == nil {
		return false
	}
	parentSessionID = strings.TrimSpace(parentSessionID)
	if parentSessionID == "" {
		return false
	}
	store := h.gatewayStateStore()
	if store == nil {
		return false
	}
	changed := store.SetAttention(parentSessionID, gatewayagent.AttentionError)
	if changed {
		h.pushGatewaySessions()
	}
	h.notifyGateway(wire.Notify{
		SessionID: parentSessionID,
		Kind:      wire.NotifyKindError,
		Priority:  wire.NotifyPriorityHigh,
		TS:        time.Now().UTC(),
	})
	return changed
}

// gatewayTailBufferRunes bounds the accumulated delta window before the
// shared 512-rune display truncation applies.
const gatewayTailBufferRunes = 2048

// appendGatewayTail accumulates assistant text deltas for the session and
// pushes the display tail through the shared store + throttle. Returns true
// when a push is warranted.
func (h *Handler) appendGatewayTail(sessionID, delta string) bool {
	if h == nil || sessionID == "" || delta == "" {
		return false
	}
	store := h.gatewayStateStore()
	throttle := h.gatewayTailThrottle()
	if store == nil || throttle == nil {
		return false
	}
	h.gatewayAttentionMu.Lock()
	if h.gatewayTailBuffer == nil {
		h.gatewayTailBuffer = make(map[string]string)
	}
	buf := h.gatewayTailBuffer[sessionID] + delta
	if runes := []rune(buf); len(runes) > gatewayTailBufferRunes {
		buf = string(runes[len(runes)-gatewayTailBufferRunes:])
	}
	h.gatewayTailBuffer[sessionID] = buf
	h.gatewayAttentionMu.Unlock()
	now := time.Now()
	// P1-b presence gating: once the gateway advertises watch control, only
	// watched sessions push tails (pre-gating gateways keep pushing).
	if ctl := h.gatewayController; ctl != nil {
		if gated, ok := ctl.(interface{ TailWanted(string) bool }); ok && !gated.TailWanted(sessionID) {
			return false
		}
	}
	if !throttle.Allow(sessionID, buf, now) {
		return false
	}
	return store.SetTail(sessionID, buf, now)
}

// clearGatewayTail drops a session's delta buffer and throttle state
// (turn start/end, interrupt, session end).
func (h *Handler) clearGatewayTail(sessionID string) {
	if h == nil || sessionID == "" {
		return
	}
	h.gatewayAttentionMu.Lock()
	delete(h.gatewayTailBuffer, sessionID)
	h.gatewayAttentionMu.Unlock()
	if t := h.gatewayTailThrottle(); t != nil {
		t.Forget(sessionID)
	}
}

// gatewayEventTextDelta extracts the running text from a stream delta event;
// producers use different key spellings across hosts, so be tolerant.
func gatewayEventTextDelta(event runtimeevents.Event) string {
	for _, key := range []string{"delta", "text", "content"} {
		if v, ok := event.Payload[key].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// gatewayToolDisplayName resolves the tool name for the activity line: the
// event field first, then the payload keys producers use.
func gatewayToolDisplayName(event runtimeevents.Event) string {
	if name := strings.TrimSpace(event.ToolName); name != "" {
		return name
	}
	for _, key := range []string{"tool_name", "name"} {
		if v, ok := event.Payload[key].(string); ok {
			if name := strings.TrimSpace(v); name != "" {
				return name
			}
		}
	}
	return "tool"
}

// notifyGateway emits a NOTIFY event (docs/gateway/08-notifications.md §4.1) when
// the attached controller supports it. Best-effort: an older controller without
// SendNotify simply receives the SESSION_UPDATE attention state instead.
func (h *Handler) notifyGateway(n wire.Notify) {
	if h == nil || h.gatewayController == nil {
		return
	}
	if notifier, ok := h.gatewayController.(interface{ SendNotify(wire.Notify) }); ok {
		notifier.SendNotify(n)
	}
}

// notifyPriorityForKind marks the states that must pierce quiet hours.
func notifyPriorityForKind(kind string) string {
	switch kind {
	case wire.NotifyKindApproval, wire.NotifyKindQuestion, wire.NotifyKindError:
		return wire.NotifyPriorityHigh
	default:
		return wire.NotifyPriorityNormal
	}
}
