package chat

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	runtimeagent "github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func seedCompactedBacktrackSession(t *testing.T, store *SQLiteSessionStorage) (*Session, types.Message, []types.Message) {
	t.Helper()
	ctx := context.Background()
	session := NewSession("backtrack-compact-user")
	for i := 1; i <= 4; i++ {
		session.AddMessage(*types.NewUserMessage(fmt.Sprintf("user turn %d", i)))
		session.AddMessage(*types.NewAssistantMessage(fmt.Sprintf("assistant turn %d", i)))
	}
	require.NoError(t, store.Save(ctx, session))

	loaded, err := store.Load(ctx, session.ID)
	require.NoError(t, err)
	retained := append([]types.Message(nil), loaded.History[4:]...)
	summary := *types.NewUserMessage("Compacted context from earlier turns: backtrack fixture")
	summary.Metadata.Set(types.MetadataKeyContextStage, "compaction")
	summary.Metadata.Set("segment_start", 0)
	summary.Metadata.Set("segment_end", len(loaded.History))
	summary.Metadata.Set("replacement_tail_message_id", types.MessageID(retained[len(retained)-1]))
	compacted := append([]types.Message{summary}, retained...)
	loaded.ReplaceHistory(compacted)
	loaded.MarkHistoryReplaced()
	require.NoError(t, store.Update(ctx, loaded))

	reloaded, err := store.Load(ctx, session.ID)
	require.NoError(t, err)
	require.Len(t, reloaded.History, 5)
	anchor := retained[2] // user turn 4
	prefix := append([]types.Message{summary}, retained[:2]...)
	return reloaded, anchor, prefix
}

func TestSQLiteStorageBacktrackAnchorTruncatesCompactedCanonical(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, nil)
	compacted, anchor, prefix := seedCompactedBacktrackSession(t, store)

	priorCount := len(compacted.History)
	compacted.ReplaceHistory(prefix)
	compacted.MarkHistoryTruncatedAt(anchor, false, priorCount+256)
	require.NoError(t, store.Update(ctx, compacted))

	page, err := store.GetMessagePage(ctx, compacted.ID, 0, 0)
	require.NoError(t, err)
	require.Len(t, page.Messages, 6, "the anchor and later canonical rows must be physically removed")
	for _, message := range page.Messages {
		require.NotEqual(t, "user turn 4", message.Content)
		require.NotEqual(t, "assistant turn 4", message.Content)
	}

	resumed, err := store.Load(ctx, compacted.ID)
	require.NoError(t, err)
	require.Len(t, resumed.History, len(prefix), "the truncated projection must not be rebuilt from the stale compacted base")
	require.Equal(t, prefix[0].Content, resumed.History[0].Content)
	require.Equal(t, "user turn 3", resumed.History[1].Content)
	require.Equal(t, "assistant turn 3", resumed.History[2].Content)
	require.Equal(t, 6, resumed.CanonicalMessageCount)
	boundaryValue, ok := resumed.GetContext(ContextCompactBoundary)
	require.True(t, ok)
	boundary, ok := DecodeCompactBoundaryValue(boundaryValue)
	require.True(t, ok)
	require.Equal(t, 6, boundary.SegmentEnd, "the retained compaction summary must not keep covering removed canonical rows")

	// The CLI sync after a backtrack persists the reloaded projection again with
	// no rewind marker. It must stay on the truncated projection instead of
	// rebuilding from the stale compacted base.
	require.NoError(t, store.Update(ctx, resumed))
	resumedAgain, err := store.Load(ctx, compacted.ID)
	require.NoError(t, err)
	require.Len(t, resumedAgain.History, len(prefix))

	resumedAgain.AddMessage(*types.NewUserMessage("after rewind"))
	require.NoError(t, store.Update(ctx, resumedAgain))
	afterAppend, err := store.Load(ctx, compacted.ID)
	require.NoError(t, err)
	require.Len(t, afterAppend.History, len(prefix)+1)
	require.Equal(t, "after rewind", afterAppend.History[len(afterAppend.History)-1].Content)
	page, err = store.GetMessagePage(ctx, compacted.ID, 0, 0)
	require.NoError(t, err)
	require.Len(t, page.Messages, 7)
}

func TestSessionActorBacktrackAfterCompactionTruncatesCanonical(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, nil)
	compacted, anchor, _ := seedCompactedBacktrackSession(t, store)

	actor, err := NewSessionActor(compacted.ID, SessionActorConfig{
		Agent:        runtimeagent.NewAgent(&runtimeagent.Config{Name: "backtrack-compact"}, nil),
		SessionStore: store,
		StateStore:   NewInMemoryRuntimeStore(8),
	})
	require.NoError(t, err)
	t.Cleanup(actor.Stop)

	result, _, err := actor.Backtrack(ctx, BacktrackRequest{
		MessageID: types.MessageID(anchor),
		Mode:      BacktrackModeConversation,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 3, result.TruncatedToMessageCount)

	page, err := store.GetMessagePage(ctx, compacted.ID, 0, 0)
	require.NoError(t, err)
	require.Len(t, page.Messages, 6)
	require.Equal(t, "assistant turn 3", page.Messages[len(page.Messages)-1].Content)

	resumed, err := store.Load(ctx, compacted.ID)
	require.NoError(t, err)
	require.Len(t, resumed.History, 3)
	require.Equal(t, "assistant turn 3", resumed.History[2].Content)
	for _, message := range resumed.History {
		require.NotEqual(t, "user turn 4", message.Content)
		require.NotEqual(t, "assistant turn 4", message.Content)
	}
}

func TestListUserTurnsSkipsCompactionSummary(t *testing.T) {
	summary := *types.NewUserMessage("Compacted context from earlier turns: summary")
	summary.Metadata.Set(types.MetadataKeyContextStage, "compaction")
	turns := ListUserTurns([]types.Message{
		summary,
		*types.NewUserMessage("real turn"),
	})
	require.Len(t, turns, 1)
	require.Equal(t, "real turn", turns[0].Preview)
	require.Equal(t, 1, turns[0].MessageIndex)
}
