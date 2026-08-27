package chat

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func newTestSQLiteSessionStorage(t *testing.T, mutate func(*PersistentSessionStorageConfig)) *SQLiteSessionStorage {
	t.Helper()
	dir := t.TempDir()
	cfg := DefaultPersistentSessionStorageConfig(dir)
	cfg.Path = filepath.Join(dir, "sessions.sqlite")
	cfg.ImportLegacyJSON = false
	if mutate != nil {
		mutate(&cfg)
	}
	store, err := NewSQLiteSessionStorage(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.CloseStorage()) })
	return store
}

func TestSQLiteSessionStorageSchemaVersionFastPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.sqlite")

	cfg := DefaultPersistentSessionStorageConfig(dir)
	cfg.Path = path
	cfg.ImportLegacyJSON = false
	first, err := NewSQLiteSessionStorage(cfg)
	require.NoError(t, err)
	require.NoError(t, first.CloseStorage())

	db, err := sql.Open("sqlite3", path)
	require.NoError(t, err)
	var version int
	require.NoError(t, db.QueryRow(`PRAGMA user_version`).Scan(&version))
	require.Equal(t, sqliteSessionSchemaVersion, version)
	require.NoError(t, db.Close())

	// Reopen should skip migration work and still serve sessions.
	second, err := NewSQLiteSessionStorage(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, second.CloseStorage()) })

	session := NewSession("fast-path-user")
	require.NoError(t, second.Save(context.Background(), session))
	loaded, err := second.Load(context.Background(), session.ID)
	require.NoError(t, err)
	require.Equal(t, session.ID, loaded.ID)
}

func TestSQLiteSessionStorageSkipsLegacyImportWhenNoJSON(t *testing.T) {
	dir := t.TempDir()
	// Directory contains only non-JSON entries.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("no sessions"), 0o644))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "nested"), 0o755))

	mayContain, err := sessionDirMayContainLegacyJSON(dir)
	require.NoError(t, err)
	require.False(t, mayContain)

	// With a leftover JSON file the preflight must detect it.
	legacy := NewSession("legacy-detect")
	legacy.ID = "session-legacy-detect"
	payload, err := json.Marshal(legacy)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, legacy.ID+".json"), payload, 0o644))

	mayContain, err = sessionDirMayContainLegacyJSON(dir)
	require.NoError(t, err)
	require.True(t, mayContain)
}

func TestSQLiteSessionStorageKeepsCanonicalTranscriptAcrossProjectionReplacement(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, func(cfg *PersistentSessionStorageConfig) {
		cfg.HotHistoryMessages = 4
	})
	session := NewSession("sqlite-user")
	require.NoError(t, store.Save(ctx, session))

	for index := 0; index < 10; index++ {
		message := *types.NewUserMessage("message-" + string(rune('a'+index)))
		require.NoError(t, store.AddMessageWithLimit(ctx, session.ID, message, 4))
	}

	loaded, err := store.Load(ctx, session.ID)
	require.NoError(t, err)
	require.LessOrEqual(t, len(loaded.History), 4)
	require.Equal(t, 10, loaded.CanonicalMessageCount)

	summary := *types.NewUserMessage("compacted summary")
	summary.Metadata["context_stage"] = "compaction"
	loaded.ReplaceHistory([]types.Message{summary, *types.NewUserMessage("recent projection")})
	require.NoError(t, store.Update(ctx, loaded))

	afterCompact, err := store.Load(ctx, session.ID)
	require.NoError(t, err)
	require.Equal(t, 10, afterCompact.CanonicalMessageCount)
	require.Equal(t, "compacted summary", afterCompact.History[0].Content)

	canonical, err := store.GetRecentMessages(ctx, session.ID, 20)
	require.NoError(t, err)
	require.Len(t, canonical, 10)
	require.Equal(t, "message-j", canonical[len(canonical)-1].Content)

	afterCompact.AddMessage(*types.NewAssistantMessage("new answer"))
	require.NoError(t, store.Update(ctx, afterCompact))
	canonical, err = store.GetRecentMessages(ctx, session.ID, 20)
	require.NoError(t, err)
	require.Len(t, canonical, 11)
	require.Equal(t, "new answer", canonical[len(canonical)-1].Content)
}

func TestSQLiteSessionStorageListsMetadataWithoutLoadingHistory(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, nil)
	session := NewSession("preview-user")
	session.UpdateTitle("bounded listing")
	require.NoError(t, store.Save(ctx, session))
	require.NoError(t, store.AddMessage(ctx, session.ID, *types.NewUserMessage("hello")))

	sessions, err := store.List(ctx, "preview-user")
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	require.False(t, sessions[0].HistoryLoaded)
	require.Empty(t, sessions[0].History)
	require.Equal(t, 1, sessions[0].MessageCount())

	previews, err := store.ListPreviews(ctx, "preview-user", 10, 0)
	require.NoError(t, err)
	require.Len(t, previews, 1)
	require.Equal(t, 1, previews[0].MessageCount)
	_, err = store.db.ExecContext(ctx, `UPDATE sessions SET metadata_json = ? WHERE id = ?`, []byte(`{`), session.ID)
	require.NoError(t, err)
	previews, err = store.ListPreviews(ctx, "preview-user", 10, 0)
	require.NoError(t, err, "preview listing must not decode the complete metadata context")
	require.Len(t, previews, 1)
}

func TestSQLiteSessionStorageExternalizesLargeCanonicalMessagesAndBoundsHotCopy(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, func(cfg *PersistentSessionStorageConfig) {
		cfg.HotHistoryBytes = 4 * 1024
		cfg.MaxHotMessageBytes = 1024
		cfg.MaxInlineMessageBytes = 2048
	})
	session := NewSession("artifact-user")
	require.NoError(t, store.Save(ctx, session))
	fullContent := strings.Repeat("0123456789", 2000)
	require.NoError(t, store.AddMessage(ctx, session.ID, *types.NewToolMessage("call-large", fullContent)))

	loaded, err := store.Load(ctx, session.ID)
	require.NoError(t, err)
	require.Len(t, loaded.History, 1)
	require.Less(t, len(loaded.History[0].Content), len(fullContent))
	require.True(t, loaded.History[0].Metadata.GetBool("session_storage_truncated", false))

	canonical, err := store.GetRecentMessages(ctx, session.ID, 1)
	require.NoError(t, err)
	require.Len(t, canonical, 1)
	require.Equal(t, fullContent, canonical[0].Content)

	var inline []byte
	var artifactPath string
	require.NoError(t, store.db.QueryRowContext(ctx, `
		SELECT payload_json, artifact_path FROM session_messages
		WHERE session_id = ? AND seq = 1
	`, session.ID).Scan(&inline, &artifactPath))
	require.Empty(t, inline)
	require.NotEmpty(t, artifactPath)
	_, err = os.Stat(filepath.Join(store.cfg.Dir, artifactPath))
	require.NoError(t, err)
}

func TestSQLiteSessionStorageStreamsExternalCanonicalJSON(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, func(cfg *PersistentSessionStorageConfig) {
		cfg.MaxInlineMessageBytes = 1024
	})
	session := NewSession("raw-stream-user")
	require.NoError(t, store.Save(ctx, session))
	content := strings.Repeat("stream-payload-", 64*1024)
	message := *types.NewToolMessage("stream-call", content)
	message.ContentParts = []types.ContentPart{{Type: types.ContentPartText, Text: "part"}}
	require.NoError(t, store.AddMessage(ctx, session.ID, message))

	visited := 0
	err := store.StreamMessageJSON(ctx, session.ID, func(sequence int, info CanonicalMessageInfo, payload io.Reader) error {
		visited++
		require.Equal(t, 1, sequence)
		require.True(t, info.StatsKnown)
		require.True(t, info.ToolResult)
		require.Equal(t, 1, info.ContentPartCount)
		written, err := io.Copy(io.Discard, payload)
		require.Greater(t, written, int64(len(content)))
		return err
	})
	require.NoError(t, err)
	require.Equal(t, 1, visited)
}

func TestSQLiteSessionStorageMarksMigratedCanonicalStatsUnknown(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, nil)
	session := NewSession("legacy-stats-user")
	require.NoError(t, store.Save(ctx, session))
	require.NoError(t, store.AddMessage(ctx, session.ID, *types.NewAssistantMessage("legacy row")))
	_, err := store.db.ExecContext(ctx, `
		UPDATE session_messages
		SET role = '', tool_call_count = -1, tool_result = -1, content_part_count = -1
		WHERE session_id = ?
	`, session.ID)
	require.NoError(t, err)

	err = store.StreamMessageJSON(ctx, session.ID, func(_ int, info CanonicalMessageInfo, payload io.Reader) error {
		require.False(t, info.StatsKnown)
		var message types.Message
		require.NoError(t, json.NewDecoder(payload).Decode(&message))
		require.Equal(t, "assistant", message.Role)
		return nil
	})
	require.NoError(t, err)
}

func TestSQLiteSessionStorageSnapshotIncludesCommittedWALContents(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, nil)
	session := NewSession("snapshot-user")
	require.NoError(t, store.Save(ctx, session))
	require.NoError(t, store.AddMessage(ctx, session.ID, *types.NewUserMessage("before snapshot")))

	snapshotPath := filepath.Join(t.TempDir(), "session-snapshot.sqlite")
	require.NoError(t, store.Snapshot(ctx, snapshotPath))
	require.NoError(t, store.AddMessage(ctx, session.ID, *types.NewUserMessage("after snapshot")))

	snapshotDB, err := sql.Open("sqlite3", snapshotPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, snapshotDB.Close()) })
	var messageCount, rowCount int
	require.NoError(t, snapshotDB.QueryRowContext(ctx,
		`SELECT message_count FROM sessions WHERE id = ?`, session.ID).Scan(&messageCount))
	require.NoError(t, snapshotDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM session_messages WHERE session_id = ?`, session.ID).Scan(&rowCount))
	require.Equal(t, 1, messageCount)
	require.Equal(t, 1, rowCount)
}

func TestSQLiteSessionStorageSnapshotsOnlyRequestedSession(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, nil)
	first := NewSession("snapshot-scope-user")
	second := NewSession("snapshot-scope-user")
	require.NoError(t, store.Save(ctx, first))
	require.NoError(t, store.Save(ctx, second))
	require.NoError(t, store.AddMessage(ctx, first.ID, *types.NewUserMessage("included")))
	require.NoError(t, store.AddMessage(ctx, second.ID, *types.NewUserMessage("excluded")))

	snapshotPath := filepath.Join(t.TempDir(), "single-session.sqlite")
	require.NoError(t, store.SnapshotSession(ctx, first.ID, snapshotPath))
	database, err := sql.Open("sqlite3", snapshotPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	var sessions, messages int
	require.NoError(t, database.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions`).Scan(&sessions))
	require.NoError(t, database.QueryRowContext(ctx, `SELECT COUNT(*) FROM session_messages`).Scan(&messages))
	require.Equal(t, 1, sessions)
	require.Equal(t, 1, messages)
	var storedID string
	require.NoError(t, database.QueryRowContext(ctx, `SELECT id FROM sessions`).Scan(&storedID))
	require.Equal(t, first.ID, storedID)
}

func TestSQLiteSessionStorageRemovesNewArtifactWhenTransactionRollsBack(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, func(cfg *PersistentSessionStorageConfig) {
		cfg.MaxInlineMessageBytes = 1024
	})
	session := NewSession("rollback-user")
	require.NoError(t, store.Save(ctx, session))
	loaded, err := store.Load(ctx, session.ID)
	require.NoError(t, err)
	loaded.AddMessage(*types.NewToolMessage("large-call", strings.Repeat("x", 4096)))
	cycle := map[string]interface{}{}
	cycle["self"] = cycle
	loaded.Metadata.Context["invalid_cycle"] = cycle

	require.Error(t, store.Update(ctx, loaded))
	count, err := store.MessageCount(ctx, session.ID)
	require.NoError(t, err)
	require.Zero(t, count)
	artifactDir := filepath.Join(store.cfg.Dir, "session-artifacts", session.ID)
	entries, readErr := os.ReadDir(artifactDir)
	if readErr != nil {
		require.True(t, os.IsNotExist(readErr))
	} else {
		require.Empty(t, entries)
	}
}

func TestSQLiteSessionStorageEnforcesHardHotMessageLimit(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, func(cfg *PersistentSessionStorageConfig) {
		cfg.MaxHotMessageBytes = 4096
		cfg.HotHistoryBytes = 8192
	})
	session := NewSession("hard-limit-user")
	require.NoError(t, store.Save(ctx, session))
	message := *types.NewAssistantMessage(strings.Repeat("answer", 4000))
	message.Metadata["large_metadata"] = strings.Repeat("metadata", 4000)
	for index := 0; index < 64; index++ {
		message.ToolCalls = append(message.ToolCalls, types.ToolCall{
			ID:   fmt.Sprintf("call-%d", index),
			Name: "large_tool",
			Args: map[string]interface{}{"payload": strings.Repeat("argument", 1000)},
		})
	}
	require.NoError(t, store.AddMessage(ctx, session.ID, message))

	var storedBytes int
	require.NoError(t, store.db.QueryRowContext(ctx, `
		SELECT byte_count FROM session_prompt_messages WHERE session_id = ?
	`, session.ID).Scan(&storedBytes))
	require.LessOrEqual(t, storedBytes, store.cfg.MaxHotMessageBytes)
	loaded, err := store.Load(ctx, session.ID)
	require.NoError(t, err)
	require.Len(t, loaded.History, 1)
	require.True(t, loaded.History[0].Metadata.GetBool("session_storage_truncated", false))
}

func TestSQLiteSessionStoragePreservesCustomToolTransportWhenTruncated(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, func(cfg *PersistentSessionStorageConfig) {
		cfg.MaxHotMessageBytes = 4096
		cfg.HotHistoryBytes = 8192
	})
	session := NewSession("custom-tool-transport-user")
	require.NoError(t, store.Save(ctx, session))

	raw := strings.Repeat("*** Begin Patch\n+line\n*** End Patch\n", 300)
	message := *types.NewAssistantMessage("")
	message.ToolCalls = []types.ToolCall{{
		ID: "call-patch", Type: "custom_tool_call", Name: "apply_patch",
		Args: map[string]interface{}{"patch": raw}, RawInput: raw,
	}}
	require.NoError(t, store.AddMessage(ctx, session.ID, message))

	loaded, err := store.Load(ctx, session.ID)
	require.NoError(t, err)
	require.Len(t, loaded.History, 1)
	require.Len(t, loaded.History[0].ToolCalls, 1)
	stored := loaded.History[0].ToolCalls[0]
	require.Equal(t, "custom_tool_call", stored.Type)
	require.NotEmpty(t, stored.RawInput)
	require.Less(t, len(stored.RawInput), len(raw))
	require.Equal(t, true, stored.Args["_session_storage_omitted"])

	canonical, err := store.GetRecentMessages(ctx, session.ID, 1)
	require.NoError(t, err)
	require.Len(t, canonical, 1)
	require.Equal(t, raw, canonical[0].ToolCalls[0].RawInput)
}

func TestSQLiteSessionStoragePreservesFrozenContextMetadataWhenTruncated(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, func(cfg *PersistentSessionStorageConfig) {
		cfg.MaxHotMessageBytes = 4096
		cfg.HotHistoryBytes = 8192
	})
	session := NewSession("snapshot-metadata-user")
	require.NoError(t, store.Save(ctx, session))

	message := *types.NewAssistantMessage(strings.Repeat("frozen context ", 1000))
	message.Metadata["context_stage"] = "recall"
	message.Metadata["context_snapshot"] = true
	message.Metadata["context_turn_id"] = "turn-cache-prefix"
	message.Metadata["large_metadata"] = strings.Repeat("metadata", 4000)
	require.NoError(t, store.AddMessage(ctx, session.ID, message))

	loaded, err := store.Load(ctx, session.ID)
	require.NoError(t, err)
	require.Len(t, loaded.History, 1)
	stored := loaded.History[0]
	require.True(t, stored.Metadata.GetBool("session_storage_truncated", false))
	require.Equal(t, "recall", stored.Metadata.GetString("context_stage", ""))
	require.True(t, stored.Metadata.GetBool("context_snapshot", false))
	require.Equal(t, "turn-cache-prefix", stored.Metadata.GetString("context_turn_id", ""))
}

func TestSQLiteSessionStorageKeepsProjectionBoundedAsTranscriptGrows(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, func(cfg *PersistentSessionStorageConfig) {
		cfg.HotHistoryMessages = 12
		cfg.HotHistoryBytes = 32 * 1024
		cfg.MaxHotMessageBytes = 8 * 1024
	})
	session := NewSession("long-session-user")
	require.NoError(t, store.Save(ctx, session))
	for index := 0; index < 300; index++ {
		require.NoError(t, store.AddMessage(ctx, session.ID,
			*types.NewToolMessage(fmt.Sprintf("call-%d", index), strings.Repeat("result", 500))))
	}

	var count, totalBytes int
	require.NoError(t, store.db.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(byte_count), 0)
		FROM session_prompt_messages WHERE session_id = ?
	`, session.ID).Scan(&count, &totalBytes))
	require.LessOrEqual(t, count, store.cfg.HotHistoryMessages)
	require.LessOrEqual(t, totalBytes, store.cfg.HotHistoryBytes)
	loaded, err := store.Load(ctx, session.ID)
	require.NoError(t, err)
	require.Equal(t, 300, loaded.CanonicalMessageCount)
	require.LessOrEqual(t, len(loaded.History), store.cfg.HotHistoryMessages)
}

func TestSQLiteSessionStorageAppendDoesNotRewriteWholeProjection(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, func(cfg *PersistentSessionStorageConfig) {
		cfg.HotHistoryMessages = 8
		cfg.HotHistoryBytes = 64 * 1024
	})
	session := NewSession("write-amplification-user")
	require.NoError(t, store.Save(ctx, session))
	for index := 0; index < 8; index++ {
		session.AddMessage(*types.NewUserMessage(fmt.Sprintf("message-%d", index)))
		require.NoError(t, store.Update(ctx, session))
	}
	var before int64
	require.NoError(t, store.db.QueryRowContext(ctx, `SELECT total_changes()`).Scan(&before))
	session.AddMessage(*types.NewUserMessage("message-next"))
	require.NoError(t, store.Update(ctx, session))
	var after int64
	require.NoError(t, store.db.QueryRowContext(ctx, `SELECT total_changes()`).Scan(&after))
	require.LessOrEqual(t, after-before, int64(4), "append should insert one canonical row, one prompt row, trim one prompt row, and update metadata")
	require.LessOrEqual(t, len(session.History), store.cfg.HotHistoryMessages)
}

func TestSQLiteSessionStorageMetadataOnlyUpdateReusesHotProjection(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, nil)
	session := NewSession("metadata-memory-user")
	require.NoError(t, store.Save(ctx, session))
	require.NoError(t, store.AddMessage(ctx, session.ID, *types.NewUserMessage(strings.Repeat("history", 1024))))

	loaded, err := store.Load(ctx, session.ID)
	require.NoError(t, err)
	require.Len(t, loaded.History, 1)
	projectionMessage := &loaded.History[0]
	loaded.UpdateTitle("metadata only")
	require.NoError(t, store.Update(ctx, loaded))

	require.Same(t, projectionMessage, &loaded.History[0], "metadata-only saves should not replace the in-memory projection")
	require.Equal(t, "metadata only", loaded.Metadata.Title)
}

func TestSQLiteSessionStorageUpdateKeepsInMemoryProjectionBounded(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, func(cfg *PersistentSessionStorageConfig) {
		cfg.HotHistoryMessages = 10
		cfg.HotHistoryBytes = 64 * 1024
	})
	session := NewSession("live-memory-user")
	require.NoError(t, store.Save(ctx, session))
	for index := 0; index < 250; index++ {
		session.AddMessage(*types.NewToolMessage(fmt.Sprintf("call-%d", index), strings.Repeat("output", 400)))
		require.NoError(t, store.Update(ctx, session))
		require.LessOrEqual(t, len(session.History), store.cfg.HotHistoryMessages)
	}
	require.Equal(t, 250, session.CanonicalMessageCount)
}

func TestSQLiteSessionStorageArchivesIdleSessionsInBatches(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, nil)
	ids := make([]string, 0, 5)
	for index := 0; index < 5; index++ {
		session := NewSession("idle-batch-user")
		require.NoError(t, store.Save(ctx, session))
		ids = append(ids, session.ID)
	}
	oldUpdatedAt := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339Nano)
	for _, id := range ids[:4] {
		_, err := store.db.ExecContext(ctx, `UPDATE sessions SET updated_at = ? WHERE id = ?`, oldUpdatedAt, id)
		require.NoError(t, err)
	}
	_, err := store.db.ExecContext(ctx, `UPDATE sessions SET state = ? WHERE id = ?`, StateClosed, ids[3])
	require.NoError(t, err)

	archived, err := store.ArchiveIdleSessions(ctx, time.Now().Add(-time.Hour), 2)
	require.NoError(t, err)
	require.Equal(t, 3, archived)
	var idleCount, activeCount, closedCount int
	require.NoError(t, store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sessions WHERE state = ?`, StateIdle).Scan(&idleCount))
	require.NoError(t, store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sessions WHERE state = ?`, StateActive).Scan(&activeCount))
	require.NoError(t, store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sessions WHERE state = ?`, StateClosed).Scan(&closedCount))
	require.Equal(t, 3, idleCount)
	require.Equal(t, 1, activeCount)
	require.Equal(t, 1, closedCount)
	stats, err := store.GetStatistics(ctx, "idle-batch-user")
	require.NoError(t, err)
	require.Equal(t, 5, stats.Total)
	require.Equal(t, 3, stats.Idle)
	require.Equal(t, 1, stats.Active)
	require.Equal(t, 1, stats.Closed)
}

func TestSQLiteSessionStorageCleansMoreThanOneExpiredBatch(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, nil)
	for index := 0; index < 130; index++ {
		session := NewSession("cleanup-batch-user")
		require.NoError(t, store.Save(ctx, session))
	}
	_, err := store.db.ExecContext(ctx, `UPDATE sessions SET expires_at = ?`,
		time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano))
	require.NoError(t, err)
	removed, err := store.Cleanup(ctx, time.Now().Add(-24*time.Hour))
	require.NoError(t, err)
	require.Equal(t, 130, removed)
	var remaining int
	require.NoError(t, store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions`).Scan(&remaining))
	require.Zero(t, remaining)
}

func TestSQLiteSessionStorageStreamsLegacyJSONImport(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	legacy := NewSession("legacy-user")
	legacy.ID = "session-legacy-stream"
	legacy.CreatedAt = time.Now().Add(-time.Hour)
	for index := 0; index < 25; index++ {
		legacy.AddMessage(*types.NewUserMessage("legacy-message-" + string(rune('a'+index))))
	}
	legacyPath := filepath.Join(dir, legacy.ID+".json")
	file, err := os.Create(legacyPath)
	require.NoError(t, err)
	require.NoError(t, json.NewEncoder(file).Encode(legacy))
	require.NoError(t, file.Close())

	cfg := DefaultPersistentSessionStorageConfig(dir)
	cfg.Path = filepath.Join(dir, "sessions.sqlite")
	cfg.HotHistoryMessages = 5
	store, err := NewSQLiteSessionStorage(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.CloseStorage()) })

	loaded, err := store.Load(ctx, legacy.ID)
	require.NoError(t, err)
	require.Equal(t, 25, loaded.CanonicalMessageCount)
	require.LessOrEqual(t, len(loaded.History), 5)
	canonical, err := store.GetRecentMessages(ctx, legacy.ID, 100)
	require.NoError(t, err)
	// Explicit history pages are independent from the much smaller runtime window.
	require.Len(t, canonical, 25)
	require.Equal(t, "legacy-message-y", canonical[len(canonical)-1].Content)
	_, err = os.Stat(legacyPath)
	require.NoError(t, err, "legacy JSON remains as a rollback source")
}

func TestSQLiteSessionStoragePagesCanonicalHistoryWithStableCursor(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, func(cfg *PersistentSessionStorageConfig) {
		cfg.HistoryPageMessages = 3
	})
	session := NewSession("page-user")
	require.NoError(t, store.Save(ctx, session))
	for index := 1; index <= 8; index++ {
		require.NoError(t, store.AddMessage(ctx, session.ID, *types.NewUserMessage(fmt.Sprintf("message-%d", index))))
	}

	newest, err := store.GetMessagePage(ctx, session.ID, 0, 3)
	require.NoError(t, err)
	require.Equal(t, 8, newest.Total)
	require.Equal(t, 6, newest.FirstSeq)
	require.Equal(t, 8, newest.LastSeq)
	require.Equal(t, 6, newest.NextBeforeSeq)
	require.True(t, newest.HasMore)
	require.Equal(t, []string{"message-6", "message-7", "message-8"}, messageContents(newest.Messages))

	previous, err := store.GetMessagePage(ctx, session.ID, newest.NextBeforeSeq, 3)
	require.NoError(t, err)
	require.Equal(t, 3, previous.FirstSeq)
	require.Equal(t, 5, previous.LastSeq)
	require.Equal(t, []string{"message-3", "message-4", "message-5"}, messageContents(previous.Messages))

	oldest, err := store.GetMessagePage(ctx, session.ID, previous.NextBeforeSeq, 3)
	require.NoError(t, err)
	require.False(t, oldest.HasMore)
	require.Zero(t, oldest.NextBeforeSeq)
	require.Equal(t, []string{"message-1", "message-2"}, messageContents(oldest.Messages))
}

func messageContents(messages []types.Message) []string {
	contents := make([]string, len(messages))
	for index := range messages {
		contents[index] = messages[index].Content
	}
	return contents
}

func TestSQLiteSessionStorageOpenRetriesOnLockedDatabase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.sqlite")

	cfg := DefaultPersistentSessionStorageConfig(dir)
	cfg.Path = path
	cfg.ImportLegacyJSON = false
	first, err := NewSQLiteSessionStorage(cfg)
	require.NoError(t, err)
	require.NoError(t, first.CloseStorage())

	// Hold a write lock on a rollback-journal database so new readers (the
	// PRAGMA user_version probe during open) hit SQLITE_BUSY until the lock
	// is released, mimicking a concurrent process mid-write.
	locker, err := sql.Open("sqlite3", path)
	require.NoError(t, err)
	_, err = locker.Exec("PRAGMA journal_mode=DELETE")
	require.NoError(t, err)
	_, err = locker.Exec("BEGIN EXCLUSIVE")
	require.NoError(t, err)
	release := make(chan struct{})
	go func() {
		select {
		case <-time.After(600 * time.Millisecond):
		case <-release:
		}
		_, _ = locker.ExecContext(context.Background(), "COMMIT")
		_ = locker.Close()
	}()

	cfg.BusyTimeout = 100 * time.Millisecond
	store, err := NewSQLiteSessionStorage(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.CloseStorage()) })

	session := NewSession("open-retry-user")
	require.NoError(t, store.Save(context.Background(), session))
	loaded, err := store.Load(context.Background(), session.ID)
	require.NoError(t, err)
	require.Equal(t, session.ID, loaded.ID)
}

func TestSQLiteSessionStorageOpenFailsWhenLockPersists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.sqlite")

	cfg := DefaultPersistentSessionStorageConfig(dir)
	cfg.Path = path
	cfg.ImportLegacyJSON = false
	first, err := NewSQLiteSessionStorage(cfg)
	require.NoError(t, err)
	require.NoError(t, first.CloseStorage())

	locker, err := sql.Open("sqlite3", path)
	require.NoError(t, err)
	_, err = locker.Exec("PRAGMA journal_mode=DELETE")
	require.NoError(t, err)
	_, err = locker.Exec("BEGIN EXCLUSIVE")
	require.NoError(t, err)
	defer func() {
		_, _ = locker.ExecContext(context.Background(), "COMMIT")
		_ = locker.Close()
	}()

	cfg.BusyTimeout = 20 * time.Millisecond
	_, err = NewSQLiteSessionStorage(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "database is locked")
	require.Contains(t, err.Error(), path)
}
