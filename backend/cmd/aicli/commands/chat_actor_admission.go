package commands

import (
	"context"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

// newLocalResumeAdmission builds the CLI host's view of the shared admission
// decision service. It is intentionally a thin adapter: the decision logic lives
// in supervision and is shared byte-for-byte with the API host.
func (h *localChatRuntimeHost) newLocalResumeAdmission() *supervision.ResumeAdmissionService {
	if h == nil || h.Supervision == nil || h.Supervision.Store == nil {
		return nil
	}
	var suspensions subagentbatch.BatchStore
	if h.SubagentBatches != nil {
		suspensions = h.SubagentBatches
	}
	return supervision.NewResumeAdmissionService(supervision.ResumeAdmissionConfig{
		Suspensions:    suspensions,
		ParentStates:   supervision.ParentTurnSnapshotFunc(h.localParentTurnSnapshot),
		AgentSessions:  h.agentSessionObligationResolver(),
		Teams:          h.teamObligationResolver(),
		BackgroundJobs: h.backgroundJobObligationResolver(),
		Claims:         h.Supervision.Store,
	})
}

// localParentTurnSnapshot maps the CLI's durable RuntimeState to the neutral
// admission snapshot. The phase is authoritative when written by the actor; an
// older state without TurnPhase is inferred from the suspension/finalized ids.
func (h *localChatRuntimeHost) localParentTurnSnapshot(ctx context.Context, sessionID string) (*supervision.ParentTurnSnapshot, error) {
	if h == nil || h.RuntimeStore == nil {
		return nil, nil
	}
	state, err := h.RuntimeStore.LoadState(ctx, sessionID)
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
