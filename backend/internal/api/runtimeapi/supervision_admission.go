package runtimeapi

import (
	"context"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

// newAPIResumeAdmission builds the API host's adapter around the same
// supervision.ResumeAdmissionService used by the CLI host. Decision logic stays
// host-neutral; only the durable runtime-state read differs.
func (h *Handler) newAPIResumeAdmission() *supervision.ResumeAdmissionService {
	if h == nil || h.getSupervisionStore() == nil {
		return nil
	}
	return supervision.NewResumeAdmissionService(supervision.ResumeAdmissionConfig{
		Suspensions:    h.peekSubagentBatchStore(),
		ParentStates:   supervision.ParentTurnSnapshotFunc(h.apiParentTurnSnapshot),
		AgentSessions:  h.agentSessionObligationResolver(),
		Teams:          h.teamObligationResolver(),
		BackgroundJobs: h.backgroundJobObligationResolver(),
		Claims:         h.getSupervisionStore(),
	})
}

func (h *Handler) apiParentTurnSnapshot(ctx context.Context, sessionID string) (*supervision.ParentTurnSnapshot, error) {
	store := h.getSessionRuntimeStore()
	if store == nil {
		return nil, nil
	}
	state, err := store.LoadState(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if state == nil {
		return nil, nil
	}
	phase := supervision.ParentTurnState(strings.TrimSpace(string(state.TurnPhase)))
	if phase == "" {
		switch {
		case strings.TrimSpace(state.SuspendedTurnID) != "":
			phase = supervision.ParentTurnParked
		case strings.TrimSpace(state.FinalizedTurnID) != "":
			phase = supervision.ParentTurnFinalized
		case strings.TrimSpace(state.CurrentTurnID) != "":
			phase = supervision.ParentTurnRunning
		default:
			phase = supervision.ParentTurnUnknown
		}
	}
	return &supervision.ParentTurnSnapshot{
		SessionID:          state.SessionID,
		Status:             string(state.Status),
		CurrentTurnID:      state.CurrentTurnID,
		CurrentEpisodeID:   state.CurrentEpisodeID,
		SuspendedTurnID:    state.SuspendedTurnID,
		SuspendedEpisodeID: state.SuspendedEpisodeID,
		FinalizedTurnID:    state.FinalizedTurnID,
		FinalizedEpisodeID: state.FinalizedEpisodeID,
		Phase:              phase,
	}, nil
}
