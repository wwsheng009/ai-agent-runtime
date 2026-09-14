package chat

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// seedBacktrackSession stores a four-turn (8 message) transcript and returns it.
func seedBacktrackSession(t *testing.T, store *SQLiteSessionStorage) *Session {
	t.Helper()
	session := NewSession("backtrack-truncate-user")
	for i := 1; i <= 4; i++ {
		session.AddMessage(*types.NewUserMessage(fmt.Sprintf("user turn %d", i)))
		session.AddMessage(*types.NewAssistantMessage(fmt.Sprintf("assistant turn %d", i)))
	}
	require.NoError(t, store.Save(context.Background(), session))
	return session
}

// A confirmed backtrack must physically drop the removed turns from the
// canonical transcript. Regression: the projection was rewritten but the rows
// stayed in session_messages, so GET /history and every later Load returned the
// "removed" messages and the UI looked unchanged.
func TestSQLiteStorageBacktrackTruncatesCanonicalTranscript(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, nil)
	session := seedBacktrackSession(t, store)

	loaded, err := store.Load(ctx, session.ID)
	require.NoError(t, err)
	require.Len(t, loaded.History, 8)

	// Actor-side backtrack: keep the first two turns, drop the rest.
	prefix := append([]types.Message(nil), loaded.History[:4]...)
	loaded.ReplaceHistory(prefix)
	loaded.MarkHistoryTruncated()
	loaded.SetHeadOffset(0)
	require.NoError(t, store.Update(ctx, loaded))

	// GET /history reads the canonical transcript.
	page, err := store.GetMessagePage(ctx, session.ID, 0, 0)
	require.NoError(t, err)
	require.Len(t, page.Messages, 4, "被回溯删除的消息不得再从 canonical 记录返回")
	require.Equal(t, "user turn 1", page.Messages[0].Content)
	require.Equal(t, "assistant turn 2", page.Messages[3].Content)

	// Resume/Load must agree with the paged history.
	resumed, err := store.Load(ctx, session.ID)
	require.NoError(t, err)
	require.Len(t, resumed.History, 4)
	require.Equal(t, 4, resumed.CanonicalMessageCount)
	require.Equal(t, 4, resumed.Metadata.TotalTurns)

	// The flag is one-shot: it must not survive the successful persist.
	require.False(t, loaded.HistoryTruncated)

	// Appending after the rewind continues from the truncated transcript.
	resumed.AddMessage(*types.NewUserMessage("user turn 3"))
	require.NoError(t, store.Update(ctx, resumed))
	page, err = store.GetMessagePage(ctx, session.ID, 0, 0)
	require.NoError(t, err)
	require.Len(t, page.Messages, 5)
}

// A shorter history without the explicit rewind marker is a stale window (an
// exit sync racing a concurrent persist) and must never delete canonical rows.
func TestSQLiteStorageStaleShorterHistoryKeepsCanonicalTranscript(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, nil)
	session := seedBacktrackSession(t, store)

	loaded, err := store.Load(ctx, session.ID)
	require.NoError(t, err)
	stale := append([]types.Message(nil), loaded.History[:4]...)
	loaded.ReplaceHistory(stale)
	loaded.SetHeadOffset(0)
	require.NoError(t, store.Update(ctx, loaded))

	page, err := store.GetMessagePage(ctx, session.ID, 0, 0)
	require.NoError(t, err)
	require.Len(t, page.Messages, 8, "未标记的更短历史必须继续被视为陈旧窗口")
}

// Even when a rewind is declared, a replacement that is not an exact prefix of
// the stored transcript (compaction summary, rewritten middle) keeps the
// canonical rows and only rewrites the prompt projection.
func TestSQLiteStorageNonPrefixReplacementKeepsCanonicalTranscript(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, nil)
	session := seedBacktrackSession(t, store)

	loaded, err := store.Load(ctx, session.ID)
	require.NoError(t, err)

	summary := []types.Message{
		*types.NewSystemMessage("compact summary"),
		*types.NewAssistantMessage("assistant turn 4"),
	}
	loaded.ReplaceHistory(summary)
	loaded.MarkHistoryTruncated()
	require.NoError(t, store.Update(ctx, loaded))

	page, err := store.GetMessagePage(ctx, session.ID, 0, 0)
	require.NoError(t, err)
	require.Len(t, page.Messages, 8, "非前缀替换不得删除 canonical 行")

	resumed, err := store.Load(ctx, session.ID)
	require.NoError(t, err)
	require.Len(t, resumed.History, 2, "压缩后的投影仍按调用方替换结果生效")
}
