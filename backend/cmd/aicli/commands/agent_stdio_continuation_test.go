package commands

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/acp"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

func newACPTurnMetaTestChat(t *testing.T, name string) (*ChatSession, *runtimechat.InMemoryRuntimeStore) {
	t.Helper()
	store := runtimechat.NewInMemoryRuntimeStore(8)
	chat := &ChatSession{
		RuntimeSession:   &runtimechat.Session{ID: name},
		LocalRuntimeHost: &localChatRuntimeHost{RuntimeStore: store},
	}
	return chat, store
}

func decodeACPTurnMeta(t *testing.T, resp acp.PromptResponse) acpTurnMeta {
	t.Helper()
	require.NotNil(t, resp.Meta)
	raw, ok := resp.Meta[acpTurnMetaKey]
	require.True(t, ok, "prompt response must carry the runtime turn metadata")
	var meta acpTurnMeta
	require.NoError(t, json.Unmarshal(raw, &meta))
	return meta
}

func TestACPParkedTurnContinuationIsExplicitPrompt(t *testing.T) {
	ctx := context.Background()
	chat, store := newACPTurnMetaTestChat(t, "sess-acp-parked")
	require.NoError(t, store.SaveState(ctx, &runtimechat.RuntimeState{
		SessionID:          "sess-acp-parked",
		Status:             runtimechat.SessionIdle,
		TurnPhase:          runtimechat.ParentTurnPhaseParked,
		SuspendedTurnID:    "turn-parked",
		SuspendedEpisodeID: "ep-parked",
	}))

	resp := acpPromptResponseWithTurnMeta(ctx, chat, acp.StopReasonEndTurn)
	require.Equal(t, acp.StopReasonEndTurn, resp.StopReason)
	meta := decodeACPTurnMeta(t, resp)
	require.Equal(t, "turn-parked", meta.TurnID)
	require.Equal(t, "ep-parked", meta.EpisodeID)
	require.Equal(t, "turn-parked", meta.ResumeTurnID)
	require.Equal(t, "ep-parked", meta.ResumeEpisodeID)
	require.Equal(t, "parked", meta.TurnPhase)
	require.True(t, meta.Parked)
	require.False(t, meta.Final)
	require.Equal(t, acpContinuationExplicitPrompt, meta.Continuation)

	raw, err := json.Marshal(resp)
	require.NoError(t, err)
	var envelope map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &envelope))
	require.Contains(t, envelope, "_meta")
}

func TestACPFinalizedTurnContinuationIsNewEpisode(t *testing.T) {
	ctx := context.Background()
	chat, store := newACPTurnMetaTestChat(t, "sess-acp-finalized")
	require.NoError(t, store.SaveState(ctx, &runtimechat.RuntimeState{
		SessionID:          "sess-acp-finalized",
		Status:             runtimechat.SessionIdle,
		TurnPhase:          runtimechat.ParentTurnPhaseFinalized,
		FinalizedTurnID:    "turn-final",
		FinalizedEpisodeID: "ep-final",
	}))

	meta := decodeACPTurnMeta(t, acpPromptResponseWithTurnMeta(ctx, chat, acp.StopReasonEndTurn))
	require.Equal(t, "turn-final", meta.TurnID)
	require.Equal(t, "ep-final", meta.EpisodeID)
	require.Equal(t, "finalized", meta.TurnPhase)
	require.False(t, meta.Parked)
	require.True(t, meta.Final)
	require.Equal(t, acpContinuationNewEpisode, meta.Continuation)
}

func TestACPParkedTurnMetaFallsBackToDurableSuspensionFields(t *testing.T) {
	ctx := context.Background()
	chat, store := newACPTurnMetaTestChat(t, "sess-acp-inferred")
	require.NoError(t, store.SaveState(ctx, &runtimechat.RuntimeState{
		SessionID:          "sess-acp-inferred",
		Status:             runtimechat.SessionIdle,
		SuspendedTurnID:    "turn-inferred",
		SuspendedEpisodeID: "ep-inferred",
	}))

	meta := decodeACPTurnMeta(t, acpPromptResponseWithTurnMeta(ctx, chat, acp.StopReasonEndTurn))
	require.Equal(t, "turn-inferred", meta.TurnID)
	require.Equal(t, "ep-inferred", meta.EpisodeID)
	require.Equal(t, "parked", meta.TurnPhase)
	require.True(t, meta.Parked)
	require.Equal(t, acpContinuationExplicitPrompt, meta.Continuation)
}

func TestACPTurnMetaPrefersCurrentTurnOverPreviousFinalization(t *testing.T) {
	ctx := context.Background()
	chat, store := newACPTurnMetaTestChat(t, "sess-acp-current")
	require.NoError(t, store.SaveState(ctx, &runtimechat.RuntimeState{
		SessionID:          "sess-acp-current",
		Status:             runtimechat.SessionRunning,
		CurrentTurnID:      "turn-current",
		CurrentEpisodeID:   "ep-current",
		FinalizedTurnID:    "turn-old",
		FinalizedEpisodeID: "ep-old",
	}))

	meta := decodeACPTurnMeta(t, acpPromptResponseWithTurnMeta(ctx, chat, acp.StopReasonEndTurn))
	require.Equal(t, "turn-current", meta.TurnID)
	require.Equal(t, "ep-current", meta.EpisodeID)
	require.Equal(t, "running", meta.TurnPhase)
	require.False(t, meta.Parked)
	require.False(t, meta.Final)
	require.Equal(t, acpContinuationInFlight, meta.Continuation)
}

func TestACPPromptResponseOmitsMetaWithoutRuntimeState(t *testing.T) {
	ctx := context.Background()
	resp := acpPromptResponseWithTurnMeta(ctx, &ChatSession{}, acp.StopReasonEndTurn)
	require.Equal(t, acp.StopReasonEndTurn, resp.StopReason)
	require.Nil(t, resp.Meta)
}
