package runtimeapi

import (
	"context"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
)

// stopSessionForTerminalState is the single close/delete cleanup path for the
// HTTP host. It retires a parked interaction before stopping the live actor,
// then falls back to the durable runtime row when no actor exists (restart or
// children that never materialized in-process).
func (h *Handler) stopSessionForTerminalState(parent context.Context, sessionID, reason string) {
	sessionID = strings.TrimSpace(sessionID)
	if h == nil || sessionID == "" {
		return
	}
	ctx := parent
	if ctx == nil {
		ctx = context.Background()
	}
	retiredLive := false
	if hub := h.peekSessionHub(); hub != nil {
		if actor, ok := hub.Get(sessionID); ok && actor != nil {
			if err := actor.RetirePendingInteraction(ctx, reason); err != nil {
				logger.Warnf("session close: retire pending interaction %s: %v", sessionID, err)
			} else {
				retiredLive = true
			}
			// StopAsync is safe on the actor's own run goroutine and cancels the
			// active run immediately; the loop drains in the background.
			hub.StopAsync(sessionID)
		}
	}
	if !retiredLive {
		h.retireDurablePendingInteraction(ctx, sessionID, reason)
	}
	if h.sessionManager != nil {
		if session, err := h.sessionManager.Get(ctx, sessionID); err == nil && session != nil {
			if controller := h.getAgentSessionController(); controller != nil {
				_ = controller.cleanupAPISpawnIsolation(ctx, session)
			}
		}
	}
}

// retireDurablePendingInteraction closes a persisted approval/question that has
// no live waiter and publishes the terminal events. The bus bridge persists the
// events, keeping notification state and the event stream aligned.
func (h *Handler) retireDurablePendingInteraction(ctx context.Context, sessionID, reason string) {
	store := h.getSessionRuntimeStore()
	if store == nil {
		return
	}
	events, _, err := chat.RetireDurablePendingInteraction(ctx, store, sessionID, reason)
	if err != nil {
		logger.Warnf("session close: retire durable pending interaction %s: %v", sessionID, err)
		return
	}
	h.publishTerminalPendingEvents(events)
}

// expireDurableApprovalIfNeeded lazily reconciles a restored approval whose
// window elapsed while no actor owned the session. It is called from status
// snapshots (including AgentControl cleanup) and before resume.
func (h *Handler) expireDurableApprovalIfNeeded(ctx context.Context, sessionID string, state *chat.RuntimeState) (*chat.RuntimeState, bool) {
	if h == nil || state == nil || state.PendingApproval == nil {
		return state, false
	}
	store := h.getSessionRuntimeStore()
	if store == nil {
		return state, false
	}
	events, changed, err := chat.ExpireDurableApproval(ctx, store, sessionID, time.Now().UTC())
	if err != nil {
		logger.Warnf("session approval sweeper: expire %s: %v", sessionID, err)
		return state, false
	}
	if !changed {
		return state, false
	}
	h.publishTerminalPendingEvents(events)
	reloaded, loadErr := store.LoadState(ctx, sessionID)
	if loadErr != nil || reloaded == nil {
		return state, true
	}
	return reloaded, true
}

func (h *Handler) publishTerminalPendingEvents(events []runtimeevents.Event) {
	if h == nil || len(events) == 0 {
		return
	}
	bus := h.getRuntimeEventBus()
	if bus == nil {
		return
	}
	for _, event := range events {
		event.SessionID = strings.TrimSpace(event.SessionID)
		bus.Publish(event)
	}
}
