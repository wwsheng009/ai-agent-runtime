package runtimeapi

import (
	"strings"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/runtimeobserve"
)

// gatewaySessionState is the runtime-server's per-session attention projection
// reported to the gateway (docs/gateway/08-notifications.md §3).
type gatewaySessionState struct {
	attention string
	busy      bool
}

// Gateway attention values (wire.SessionSummary.Attention).
const (
	gatewayAttentionCompleted = "completed"
	gatewayAttentionApproval  = "approval"
	gatewayAttentionQuestion  = "question"
)

// EnableGatewayAttention subscribes to the runtime event bus and maintains the
// per-session attention projection that GatewaySessions reports to the gateway.
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

// observeGatewayAttention maps runtime events to attention state. Only state
// changes trigger a gateway push, so a burst of deltas stays cheap.
func (h *Handler) observeGatewayAttention(event runtimeevents.Event) {
	sessionID := strings.TrimSpace(event.SessionID)
	if sessionID == "" {
		return
	}
	var changed bool
	switch event.Type {
	case runtimeobserve.EventAgentTurnStarted:
		changed = h.setGatewaySessionState(sessionID, "", true)
	case runtimeobserve.EventAgentTurnDone:
		changed = h.setGatewaySessionState(sessionID, gatewayAttentionCompleted, false)
	case runtimechat.EventApprovalRequested:
		changed = h.setGatewaySessionState(sessionID, gatewayAttentionApproval, true)
	case runtimechat.EventQuestionAsked:
		changed = h.setGatewaySessionState(sessionID, gatewayAttentionQuestion, true)
	case runtimechat.EventApprovalResolved, runtimechat.EventQuestionAnswered:
		changed = h.setGatewaySessionState(sessionID, "", true)
	case runtimechat.EventSessionEnd, runtimechat.EventSessionInterrupted:
		changed = h.clearGatewaySessionState(sessionID)
	}
	if changed {
		h.pushGatewaySessions()
	}
}

// setGatewaySessionState updates attention/busy, reporting whether it changed.
func (h *Handler) setGatewaySessionState(sessionID, attention string, busy bool) bool {
	h.gatewayAttentionMu.Lock()
	defer h.gatewayAttentionMu.Unlock()
	if h.gatewayAttention == nil {
		h.gatewayAttention = make(map[string]gatewaySessionState)
	}
	prev := h.gatewayAttention[sessionID]
	if prev.attention == attention && prev.busy == busy {
		return false
	}
	h.gatewayAttention[sessionID] = gatewaySessionState{attention: attention, busy: busy}
	return true
}

// clearGatewaySessionState drops a session from the projection (session ended).
func (h *Handler) clearGatewaySessionState(sessionID string) bool {
	h.gatewayAttentionMu.Lock()
	defer h.gatewayAttentionMu.Unlock()
	if _, ok := h.gatewayAttention[sessionID]; !ok {
		return false
	}
	delete(h.gatewayAttention, sessionID)
	return true
}

// gatewaySessionStateFor returns the projection for sessionID (zero when absent).
func (h *Handler) gatewaySessionStateFor(sessionID string) gatewaySessionState {
	h.gatewayAttentionMu.Lock()
	defer h.gatewayAttentionMu.Unlock()
	return h.gatewayAttention[sessionID]
}
