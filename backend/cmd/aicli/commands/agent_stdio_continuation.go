package commands

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/acp"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

const (
	// acpTurnMetaKey namespaces the runtime's optional ACP metadata. ACP
	// clients that do not understand it ignore the field.
	acpTurnMetaKey = "ai-agent-runtime"

	// ACP deliberately has no non-terminal PromptResponse stop reason. A parked
	// logical turn ends the current session/prompt request, and the next
	// explicit session/prompt continues the same turn/episode when durable
	// state still has a parked record.
	acpContinuationExplicitPrompt = "explicit_prompt"
	acpContinuationNewEpisode     = "new_episode"
	acpContinuationInFlight       = "in_flight"
	acpContinuationUnknown        = "unknown"
)

// acpTurnMeta is the host-neutral turn lifecycle projection attached to an ACP
// PromptResponse. It is intentionally additive metadata, not a new stop reason:
// EndTurn still means "this session/prompt request finished".
type acpTurnMeta struct {
	TurnID          string `json:"turn_id,omitempty"`
	EpisodeID       string `json:"episode_id,omitempty"`
	TurnPhase       string `json:"turn_phase,omitempty"`
	Parked          bool   `json:"parked"`
	Final           bool   `json:"final"`
	Continuation    string `json:"continuation,omitempty"`
	ResumeTurnID    string `json:"resume_turn_id,omitempty"`
	ResumeEpisodeID string `json:"resume_episode_id,omitempty"`
}

// acpPromptResponseWithTurnMeta is the single response constructor for ACP
// prompt returns. It keeps StopReason transport-compatible and carries the
// logical turn identity through the ACP _meta extension.
func acpPromptResponseWithTurnMeta(ctx context.Context, chat *ChatSession, stopReason string) acp.PromptResponse {
	return acp.PromptResponse{
		StopReason: stopReason,
		Meta:       acpPromptTurnMeta(ctx, chat),
	}
}

// acpPromptTurnMeta reads the durable runtime state after a prompt returns. A
// parked state is explicitly marked non-final and names the turn/episode that a
// later explicit prompt will resume. Unreadable state degrades to no metadata
// rather than guessing an identity.
func acpPromptTurnMeta(ctx context.Context, chat *ChatSession) map[string]json.RawMessage {
	state := acpRuntimeState(ctx, chat)
	if state == nil {
		return nil
	}
	meta := acpTurnMetaFromState(state)
	raw, err := json.Marshal(meta)
	if err != nil {
		return nil
	}
	return map[string]json.RawMessage{acpTurnMetaKey: raw}
}

func acpRuntimeState(ctx context.Context, chat *ChatSession) *runtimechat.RuntimeState {
	if chat == nil || chat.LocalRuntimeHost == nil || chat.LocalRuntimeHost.RuntimeStore == nil ||
		chat.RuntimeSession == nil {
		return nil
	}
	sessionID := strings.TrimSpace(chat.RuntimeSession.ID)
	if sessionID == "" {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	state, err := chat.LocalRuntimeHost.RuntimeStore.LoadState(ctx, sessionID)
	if err != nil || state == nil {
		return nil
	}
	return state
}

func acpTurnMetaFromState(state *runtimechat.RuntimeState) acpTurnMeta {
	phase := state.TurnPhase
	if phase == "" {
		phase = acpInferTurnPhase(state)
	}
	meta := acpTurnMeta{
		TurnPhase:    string(phase),
		Continuation: acpContinuationUnknown,
	}
	switch phase {
	case runtimechat.ParentTurnPhaseParked:
		meta.TurnID = strings.TrimSpace(state.SuspendedTurnID)
		meta.EpisodeID = strings.TrimSpace(state.SuspendedEpisodeID)
		meta.Parked = true
		meta.Continuation = acpContinuationExplicitPrompt
		meta.ResumeTurnID = meta.TurnID
		meta.ResumeEpisodeID = meta.EpisodeID
	case runtimechat.ParentTurnPhaseFinalized, runtimechat.ParentTurnPhaseCanceled:
		meta.TurnID = firstNonEmptyACPValue(state.FinalizedTurnID, state.CurrentTurnID)
		meta.EpisodeID = firstNonEmptyACPValue(state.FinalizedEpisodeID, state.CurrentEpisodeID)
		meta.Final = true
		meta.Continuation = acpContinuationNewEpisode
	case runtimechat.ParentTurnPhaseRunning, runtimechat.ParentTurnPhaseFinalizing:
		meta.TurnID = strings.TrimSpace(state.CurrentTurnID)
		meta.EpisodeID = strings.TrimSpace(state.CurrentEpisodeID)
		meta.Continuation = acpContinuationInFlight
	default:
		meta.TurnID = firstNonEmptyACPValue(state.SuspendedTurnID, state.CurrentTurnID, state.FinalizedTurnID)
		meta.EpisodeID = firstNonEmptyACPValue(state.SuspendedEpisodeID, state.CurrentEpisodeID, state.FinalizedEpisodeID)
	}
	return meta
}

func acpInferTurnPhase(state *runtimechat.RuntimeState) runtimechat.ParentTurnPhase {
	switch {
	case state == nil:
		return ""
	case strings.TrimSpace(state.SuspendedTurnID) != "":
		return runtimechat.ParentTurnPhaseParked
	case strings.TrimSpace(state.CurrentTurnID) != "":
		return runtimechat.ParentTurnPhaseRunning
	case strings.TrimSpace(state.FinalizedTurnID) != "":
		return runtimechat.ParentTurnPhaseFinalized
	default:
		return ""
	}
}

func firstNonEmptyACPValue(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
