package chat

import (
	"context"
	"strings"
	"time"

	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// markTurnFinalizing records the durable FINALIZING phase before the sequence
// gate runs. It is not a terminal transition: parked/finalized/canceled phases
// are left untouched, and the caller still applies the final phase after the
// gate. Keeping this as a separate state write makes "request finished but the
// logical turn is still finalizing" observable to hosts and debug surfaces.
func (a *SessionActor) markTurnFinalizing(ctx context.Context, turnID string) bool {
	turnID = strings.TrimSpace(turnID)
	if a == nil || turnID == "" {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	applied := false
	_ = a.updateStateConvergent(ctx, func(state *RuntimeState) error {
		if state.CurrentTurnID != turnID {
			return nil
		}
		switch state.TurnPhase {
		case ParentTurnPhaseParked, ParentTurnPhaseFinalized, ParentTurnPhaseCanceled:
			return nil
		}
		state.TurnPhase = ParentTurnPhaseFinalizing
		state.UpdatedAt = time.Now().UTC()
		applied = true
		return nil
	})
	return applied
}

// annotateLogicalTurnPayload makes "request finished" distinguishable from
// "logical parent turn finalized" on the client-visible terminal events.
// assistant_message/session_end are also published for a parked run, so clients
// must not infer that the logical turn is complete from those events alone.
func annotateLogicalTurnPayload(payload map[string]interface{}, episodeID string, phase ParentTurnPhase) {
	if payload == nil {
		return
	}
	if episodeID = strings.TrimSpace(episodeID); episodeID != "" {
		payload["episode_id"] = episodeID
	}
	payload["turn_phase"] = string(phase)
	payload["turn_parked"] = phase == ParentTurnPhaseParked
	payload["final"] = phase == ParentTurnPhaseFinalized || phase == ParentTurnPhaseCanceled
}

// continuationKind classifies a run's entry point without changing the legacy
// "resume" flag semantics (which means an explicit user/session resume).
func continuationKind(prompt string, resume bool) string {
	switch {
	case resume:
		return "user_resume"
	case runtimetypes.IsControlPlanePromptText(prompt):
		return "supervision_wake"
	default:
		return "user"
	}
}
