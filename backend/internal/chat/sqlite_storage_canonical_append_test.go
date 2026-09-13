package chat

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// rawCanonicalContents returns the stored canonical rows as plain strings,
// bypassing every read-path normalization, so tests can assert on what was
// actually written (ordering, skipped rows, duplicates).
func rawCanonicalContents(t *testing.T, store *SQLiteSessionStorage, sessionID string) []string {
	t.Helper()
	rows, err := store.db.QueryContext(context.Background(), `
		SELECT payload_json FROM session_messages WHERE session_id = ? ORDER BY seq ASC
	`, sessionID)
	require.NoError(t, err)
	defer rows.Close()
	var contents []string
	for rows.Next() {
		var payload []byte
		require.NoError(t, rows.Scan(&payload))
		var message types.Message
		require.NoError(t, json.Unmarshal(payload, &message))
		contents = append(contents, message.Role+":"+message.Content)
	}
	require.NoError(t, rows.Err())
	return contents
}

// renderedContents is the transcript as the resume/history read paths present it.
func renderedContents(t *testing.T, store *SQLiteSessionStorage, sessionID string) []string {
	t.Helper()
	messages, err := store.GetRecentMessages(context.Background(), sessionID, 64)
	require.NoError(t, err)
	contents := make([]string, 0, len(messages))
	for _, message := range messages {
		contents = append(contents, message.Role+":"+message.Content)
	}
	return contents
}

// streamedContents is the transcript as canonical stream visitors (exports,
// projection rebuilds) receive it.
func streamedContents(t *testing.T, store *SQLiteSessionStorage, sessionID string) []string {
	t.Helper()
	contents := make([]string, 0, 8)
	require.NoError(t, store.StreamMessages(context.Background(), sessionID, func(_ int, message types.Message) error {
		contents = append(contents, message.Role+":"+message.Content)
		return nil
	}))
	return contents
}

func requireNoDuplicateCanonicalIdentities(t *testing.T, store *SQLiteSessionStorage, sessionID string) {
	t.Helper()
	rows, err := store.db.QueryContext(context.Background(), `
		SELECT payload_json FROM session_messages WHERE session_id = ? ORDER BY seq ASC
	`, sessionID)
	require.NoError(t, err)
	defer rows.Close()
	seen := make(map[string]struct{})
	for rows.Next() {
		var payload []byte
		require.NoError(t, rows.Scan(&payload))
		var message types.Message
		require.NoError(t, json.Unmarshal(payload, &message))
		id := types.MessageID(message)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			t.Fatalf("canonical transcript stores message id %s twice: %#v", id, message)
		}
		seen[id] = struct{}{}
	}
	require.NoError(t, rows.Err())
}

// TestSQLiteSessionStorageUpdateAlignsCanonicalAppendByIdentity pins the
// observed corruption: the agent loop persists a history with the system
// message stripped, so the length arithmetic in updateSessionTx appended "the
// last N messages" and silently skipped the assistant tool-call row while later
// re-inserting messages that were already stored (duplicated assistant turns on
// resume).
func TestSQLiteSessionStorageUpdateAlignsCanonicalAppendByIdentity(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, nil)

	session := NewSession("canonical-append-user")
	session.AddMessage(*types.NewSystemMessage("system prompt"))
	session.AddMessage(*types.NewUserMessage("ls"))
	require.NoError(t, store.Save(ctx, session))

	// Actor run: the loop hands back the system-stripped transcript of the turn
	// it just executed (user → assistant(tool_call) → tool → assistant).
	loaded, err := store.Load(ctx, session.ID)
	require.NoError(t, err)
	toolCallAssistant := *types.NewAssistantMessage("")
	toolCallAssistant.ToolCalls = []types.ToolCall{{
		ID:   "call_1",
		Type: "function",
		Name: "shell",
		Args: map[string]interface{}{"command": "ls"},
	}}
	loaded.ReplaceHistory([]types.Message{
		loaded.History[1], // user "ls" (same message id as the stored row)
		toolCallAssistant,
		*types.NewToolMessage("call_1", "目录输出"),
		*types.NewAssistantMessage("当前目录内容: ..."),
	})
	require.NoError(t, store.Update(ctx, loaded))

	canonical := rawCanonicalContents(t, store, session.ID)
	require.Equal(t, []string{
		"system:system prompt",
		"user:ls",
		"assistant:",
		"tool:目录输出",
		"assistant:当前目录内容: ...",
	}, canonical, "the canonical append must keep every new message exactly once")
	require.Equal(t, canonical, renderedContents(t, store, session.ID))

	loadedCount, err := store.MessageCount(ctx, session.ID)
	require.NoError(t, err)
	require.Equal(t, len(canonical), loadedCount)
	requireNoDuplicateCanonicalIdentities(t, store, session.ID)
}

// TestSQLiteSessionStorageUpdateSkipsAlreadyStoredTail reproduces the exact
// shape found in a corrupted session: the caller mirrors the newest assistant
// message a second time (projection lag + system strip) and the next write
// re-inserted that already stored row.
func TestSQLiteSessionStorageUpdateSkipsAlreadyStoredTail(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, nil)

	session := NewSession("canonical-tail-user")
	session.AddMessage(*types.NewSystemMessage("system prompt"))
	session.AddMessage(*types.NewUserMessage("ls"))
	session.AddMessage(*types.NewAssistantMessage("列表结果"))
	require.NoError(t, store.Save(ctx, session))

	loaded, err := store.Load(ctx, session.ID)
	require.NoError(t, err)

	// Drifted in-memory view: system stripped and the last assistant message
	// mirrored twice, followed by the next user prompt.
	drifted := make([]types.Message, 0, len(loaded.History)+1)
	drifted = append(drifted, loaded.History[1:]...)
	drifted = append(drifted, loaded.History[len(loaded.History)-1])
	drifted = append(drifted, *types.NewUserMessage("pwd"))
	loaded.ReplaceHistory(drifted)

	require.NoError(t, store.Update(ctx, loaded))

	canonical := rawCanonicalContents(t, store, session.ID)
	require.Equal(t, []string{
		"system:system prompt",
		"user:ls",
		"assistant:列表结果",
		"user:pwd",
	}, canonical, "an already stored message must never be appended twice")
	require.Equal(t, canonical, renderedContents(t, store, session.ID))
	requireNoDuplicateCanonicalIdentities(t, store, session.ID)
}

// TestSQLiteSessionStorageUpdateKeepsRepeatedContentWithDistinctIdentities
// guards the other direction: identical content sent twice is a legitimate
// transcript, not a duplicate, because the messages carry distinct ids.
func TestSQLiteSessionStorageUpdateKeepsRepeatedContentWithDistinctIdentities(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, nil)

	session := NewSession("canonical-repeat-user")
	session.AddMessage(*types.NewSystemMessage("system prompt"))
	session.AddMessage(*types.NewUserMessage("ok"))
	require.NoError(t, store.Save(ctx, session))

	loaded, err := store.Load(ctx, session.ID)
	require.NoError(t, err)
	loaded.AddMessage(*types.NewUserMessage("ok"))
	loaded.AddMessage(*types.NewAssistantMessage("ok"))
	require.NoError(t, store.Update(ctx, loaded))

	canonical := rawCanonicalContents(t, store, session.ID)
	require.Equal(t, []string{
		"system:system prompt",
		"user:ok",
		"user:ok",
		"assistant:ok",
	}, canonical)
	require.Equal(t, canonical, renderedContents(t, store, session.ID))
	requireNoDuplicateCanonicalIdentities(t, store, session.ID)
}

// TestSQLiteSessionStorageReadsCollapseDuplicateIdentities covers repair of the
// sessions already written by the pre-fix append arithmetic: the duplicate rows
// stay in session_messages (they must not be deleted behind the user's back) but
// no reader replays them, and the next write persists a repaired projection.
func TestSQLiteSessionStorageReadsCollapseDuplicateIdentities(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, nil)

	session := NewSession("corrupt-reader-user")
	session.AddMessage(*types.NewSystemMessage("system prompt"))
	session.AddMessage(*types.NewUserMessage("ls"))
	session.AddMessage(*types.NewAssistantMessage("列表结果"))
	require.NoError(t, store.Save(ctx, session))

	// Reproduce a pre-fix write: the newest assistant message is appended a
	// second time with the same message id but a fresh seq/position.
	duplicate := session.History[len(session.History)-1]
	encoded, err := encodeMessages([]types.Message{duplicate})
	require.NoError(t, err)
	require.Len(t, encoded, 1)

	tx, err := store.db.BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, store.insertCanonicalEncodedTx(ctx, tx, session.ID, 4, encoded[0]))
	require.NoError(t, tx.Commit())
	_, err = store.db.ExecContext(ctx, `
		UPDATE sessions SET message_count = 4 WHERE id = ?
	`, session.ID)
	require.NoError(t, err)
	_, err = store.db.ExecContext(ctx, `
		INSERT INTO session_prompt_messages(session_id, position, payload_json, byte_count)
		VALUES (?, 3, ?, ?)
	`, session.ID, encoded[0].payload, encoded[0].size)
	require.NoError(t, err)

	require.Equal(t, []string{
		"system:system prompt",
		"user:ls",
		"assistant:列表结果",
		"assistant:列表结果",
	}, rawCanonicalContents(t, store, session.ID), "the legacy duplicate row is still stored")

	loaded, err := store.Load(ctx, session.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"system:system prompt", "user:ls", "assistant:列表结果"}, renderedContents(t, store, session.ID))
	require.Len(t, loaded.History, 3, "resume must not replay the duplicated row")

	page, err := store.GetMessagePage(ctx, session.ID, 0, 50)
	require.NoError(t, err)
	require.Len(t, page.Messages, 3, "history pages must not replay the duplicated row")
	require.Equal(t, 4, page.Total, "the stored row count itself is left untouched")

	require.Equal(t, []string{
		"system:system prompt",
		"user:ls",
		"assistant:列表结果",
	}, streamedContents(t, store, session.ID), "streamed transcripts must not replay the duplicated row")

	// The next write rebuilds the projection from the normalized canonical
	// transcript, so the stored duplicate disappears for every later reader.
	loaded.AddMessage(*types.NewUserMessage("pwd"))
	require.NoError(t, store.Update(ctx, loaded))

	var projectionRows int
	require.NoError(t, store.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM session_prompt_messages WHERE session_id = ?
	`, session.ID).Scan(&projectionRows))
	require.Equal(t, 4, projectionRows, "the rebuilt projection drops the duplicate row")

	reloaded, err := store.Load(ctx, session.ID)
	require.NoError(t, err)
	require.Equal(t, []string{
		"system:system prompt",
		"user:ls",
		"assistant:列表结果",
		"user:pwd",
	}, renderedContents(t, store, session.ID))
	require.Equal(t, []string{
		"system:system prompt",
		"user:ls",
		"assistant:列表结果",
		"assistant:列表结果",
		"user:pwd",
	}, rawCanonicalContents(t, store, session.ID), "new writes append without duplicating the stored row again")
	require.Len(t, reloaded.History, 4)
}

// TestSQLiteSessionStorageUpdateKeepsRewrittenMessageWithSameIdentity pins the
// boundary of the duplicate guard: a rewritten message (same identity, new
// content, e.g. a retry) is not a duplicate and must stay visible instead of
// being silently dropped or collapsed on read.
func TestSQLiteSessionStorageUpdateKeepsRewrittenMessageWithSameIdentity(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, nil)

	session := NewSession("rewritten-message-user")
	session.AddMessage(*types.NewSystemMessage("system prompt"))
	session.AddMessage(*types.NewUserMessage("ls"))
	require.NoError(t, store.Save(ctx, session))

	loaded, err := store.Load(ctx, session.ID)
	require.NoError(t, err)
	stored := *types.NewAssistantMessage("旧答案")
	loaded.AddMessage(stored)
	require.NoError(t, store.Update(ctx, loaded))

	// Retry: the same message identity is rewritten with the new answer.
	rewritten := stored
	rewritten.Content = "新答案"
	loaded.ReplaceHistory([]types.Message{loaded.History[0], loaded.History[1], rewritten})
	require.NoError(t, store.Update(ctx, loaded))

	require.Equal(t, []string{
		"system:system prompt",
		"user:ls",
		"assistant:旧答案",
		"assistant:新答案",
	}, rawCanonicalContents(t, store, session.ID), "the rewrite must be appended, not dropped")
	require.Equal(t, []string{
		"system:system prompt",
		"user:ls",
		"assistant:旧答案",
		"assistant:新答案",
	}, renderedContents(t, store, session.ID), "a rewritten message is not a duplicate and must render")
	require.Equal(t, []string{
		"system:system prompt",
		"user:ls",
		"assistant:旧答案",
		"assistant:新答案",
	}, streamedContents(t, store, session.ID), "the streamed transcript must keep the rewrite too")
}
