package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// The live persistence path rebuilds messages (streaming assembly, active-turn
// compaction, CLI sync) and mints fresh message ids for identical content. The
// ReAct loop then hands the rebuilt history to Update through ReplaceHistory,
// which does not advance CanonicalMessageCount. Before the substance fallback in
// identityAlignedAppendStart, Update appended nothing in exactly that state: the
// transcript stalled minutes behind the prompt projection and the newest
// messages only stayed readable through the bounded hot window.
func TestUpdateAppendsTailWhenMessageIDsWereReissued(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, nil)
	session := saveSubstanceAnchorSeed(t, store, 150)

	loaded, err := store.Load(ctx, session.ID)
	require.NoError(t, err)
	window := loaded.GetMessages()
	require.NotEmpty(t, window)
	require.Less(t, len(window), 150, "expected a bounded projection window")
	before := canonicalRowCount(t, store, session.ID)

	durable := append(reissueMessageIDs(window),
		*types.NewUserMessage("latest question"),
		*types.NewAssistantMessage("latest answer"))
	loaded.ReplaceHistory(durable)
	require.NoError(t, store.Update(ctx, loaded))

	after := canonicalRowCount(t, store, session.ID)
	require.Equal(t, before+2, after,
		"a rebuilt-history checkpoint must append the new tail instead of silently dropping it")
	require.Equal(t, "latest answer", newestCanonicalContent(t, store, session.ID))
	require.Equal(t, []string{"latest question", "latest answer"},
		renderedTail(t, store, session.ID, 2))

	// A following checkpoint of the very same (reissued) window must not append
	// the window a second time: the transcript already holds every message.
	loaded.ReplaceHistory(reissueMessageIDs(loaded.GetMessages()))
	require.NoError(t, store.Update(ctx, loaded))
	require.Equal(t, after, canonicalRowCount(t, store, session.ID),
		"re-persisting an unchanged reissued window must not duplicate canonical rows")
}

// A turn checkpoint commonly ends on an empty assistant placeholder. Substance
// anchoring must ignore that degenerate message (every transcript holds one) and
// still append the real new messages in front of it.
func TestUpdateAppendsTailEndingInEmptyAssistantPlaceholder(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, nil)
	session := saveSubstanceAnchorSeed(t, store, 150)

	loaded, err := store.Load(ctx, session.ID)
	require.NoError(t, err)
	before := canonicalRowCount(t, store, session.ID)

	durable := append(reissueMessageIDs(loaded.GetMessages()),
		*types.NewUserMessage("placeholder turn question"),
		*types.NewAssistantMessage(""))
	loaded.ReplaceHistory(durable)
	require.NoError(t, store.Update(ctx, loaded))

	require.Equal(t, before+2, canonicalRowCount(t, store, session.ID),
		"an empty trailing placeholder must not suppress the append of new messages")
	require.Equal(t, []string{"placeholder turn question", ""}, renderedTail(t, store, session.ID, 2))
}

// Content that repeats inside a transcript stays a legitimate new turn. The
// counted pass must consume one canonical row per history message, so a replayed
// answer is only treated as stored as often as it really is stored.
func TestUpdateAppendsRepeatedAnswerWhenIDsWereReissued(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, nil)
	session := saveSubstanceAnchorSeed(t, store, 150)

	loaded, err := store.Load(ctx, session.ID)
	require.NoError(t, err)
	window := loaded.GetMessages()
	require.NotEmpty(t, window)
	before := canonicalRowCount(t, store, session.ID)

	replay := *window[len(window)-1].Clone()
	replay.Metadata.Set(types.MetadataKeyMessageID, "msg_replayed_answer")
	durable := append(reissueMessageIDs(window),
		*types.NewUserMessage("repeat that answer please"),
		replay)
	loaded.ReplaceHistory(durable)
	require.NoError(t, store.Update(ctx, loaded))

	require.Equal(t, before+2, canonicalRowCount(t, store, session.ID),
		"a replayed answer must be appended, not collapsed into the stored copy")
	require.Equal(t, "seed assistant 149", newestCanonicalContent(t, store, session.ID))
}

func saveSubstanceAnchorSeed(t *testing.T, store *SQLiteSessionStorage, messages int) *Session {
	t.Helper()
	session := NewSession("substance-anchor-user")
	for index := 0; index < messages; index++ {
		if index%2 == 0 {
			session.AddMessage(*types.NewUserMessage(fmt.Sprintf("seed user %d", index)))
			continue
		}
		session.AddMessage(*types.NewAssistantMessage(fmt.Sprintf("seed assistant %d", index)))
	}
	require.NoError(t, store.Save(context.Background(), session))
	return session
}

// reissueMessageIDs mimics an in-memory copy rebuilt without the stored
// identities: same renderable content, brand-new message ids.
func reissueMessageIDs(messages []types.Message) []types.Message {
	result := make([]types.Message, 0, len(messages))
	for index := range messages {
		clone := *messages[index].Clone()
		if clone.Metadata == nil {
			clone.Metadata = types.NewMetadata()
		}
		clone.Metadata.Set(types.MetadataKeyMessageID, fmt.Sprintf("msg_reissued_%d", index))
		result = append(result, clone)
	}
	return result
}

func canonicalRowCount(t *testing.T, store *SQLiteSessionStorage, sessionID string) int {
	t.Helper()
	var count int
	require.NoError(t, store.db.QueryRow(
		`SELECT COUNT(1) FROM session_messages WHERE session_id = ?`, sessionID,
	).Scan(&count))
	return count
}

func newestCanonicalContent(t *testing.T, store *SQLiteSessionStorage, sessionID string) string {
	t.Helper()
	var payload []byte
	require.NoError(t, store.db.QueryRow(
		`SELECT payload_json FROM session_messages WHERE session_id = ? ORDER BY seq DESC LIMIT 1`, sessionID,
	).Scan(&payload))
	var message types.Message
	require.NoError(t, json.Unmarshal(payload, &message))
	return message.Content
}

// renderedTail restricts the assertion to the newest messages so it stays valid
// while the surrounding projection window is being trimmed.
func renderedTail(t *testing.T, store *SQLiteSessionStorage, sessionID string, tail int) []string {
	t.Helper()
	contents := renderedContents(t, store, sessionID)
	require.GreaterOrEqual(t, len(contents), tail)
	contents = contents[len(contents)-tail:]
	for index := range contents {
		_, content, found := cutStoredContent(contents[index])
		require.True(t, found, "unexpected rendered row %q", contents[index])
		contents[index] = content
	}
	return contents
}

func cutStoredContent(rendered string) (string, string, bool) {
	for index := 0; index < len(rendered); index++ {
		if rendered[index] == ':' {
			return rendered[:index], rendered[index+1:], true
		}
	}
	return rendered, "", false
}
