package memoryruntime

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

func TestBacktrackThenAppendExtractsNewCanonicalBranch(t *testing.T) {
	ctx := context.Background()
	base := &fakeSource{sessions: []SessionRef{{
		ID: "s1", SessionGeneration: 1, WorkspacePath: "/workspace", State: "closed", CanonicalCount: 304,
	}}}
	source := &fakeWindowSource{fakeSource: base, window: Transcript{
		SessionID: "s1", Revision: 304,
		Messages: []TranscriptMessage{
			{ID: "new-301", Seq: 301, Role: "user", Content: "new question"},
			{ID: "new-302", Seq: 302, Role: "assistant", Content: "new answer"},
			{ID: "new-303", Seq: 303, Role: "user", Content: "new decision"},
			{ID: "new-304", Seq: 304, Role: "assistant", Content: "use the new branch"},
		},
	}}
	extractor := &fakeExtractor{}
	rt, store := newRuntimeForTest(t, "shadow", base, extractor)
	rt.Source = source
	rt.Config.TurnDigestEnabled = true
	rt.Config.MaxSourceMessages = 4
	recordInvalidationDigest(t, store, "s1", "kept", "old-299", "old-300", 300)
	old := recordInvalidationDigest(t, store, "s1", "removed", "old-499", "old-500", 500)
	_, err := store.MarkTurnDigestsProcessed(ctx, "s1", old.DigestSeq, "")
	require.NoError(t, err)
	invalidated, err := rt.InvalidateFromRuntimeEvent(ctx, chat.EventBacktrackFinished, "s1", map[string]interface{}{
		"mode":                           string(chat.BacktrackModeConversation),
		"session_generation":             int64(1),
		"canonical_cutoff_seq":           300,
		"canonical_message_count_before": 500,
		"removed_message_count":          200,
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, invalidated.Digests)
	recordInvalidationDigest(t, store, "s1", "new", "new-301", "new-304", 304)

	result, err := rt.RunPendingTurnDigests(ctx, TriggerSessionEnd)
	require.NoError(t, err)
	require.Empty(t, result.Errors)
	require.Equal(t, 1, result.Processed)
	require.Equal(t, 1, extractor.calls)
	require.Equal(t, 1, source.windowCalls)
	latest, err := store.GetTurnDigestByTurn(ctx, "s1", "new")
	require.NoError(t, err)
	require.Equal(t, memorystore.TurnDigestDone, latest.Status)
	old, err = store.GetTurnDigestByTurn(ctx, "s1", "removed")
	require.NoError(t, err)
	require.Equal(t, memorystore.TurnDigestInvalidated, old.Status)
}
