package chat

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func newIdentityFastPathStore(t *testing.T) *SQLiteSessionStorage {
	t.Helper()
	dir := t.TempDir()
	cfg := DefaultPersistentSessionStorageConfig(dir)
	cfg.Path = filepath.Join(dir, "sessions.sqlite")
	cfg.ImportLegacyJSON = false
	store, err := NewSQLiteSessionStorage(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.CloseStorage()) })
	return store
}

func seedIdentityFastPathSession(t *testing.T, ctx context.Context, store *SQLiteSessionStorage, sessionID string, count int) {
	t.Helper()
	session := &Session{
		ID:        sessionID,
		UserID:    "tester",
		State:     StateActive,
		CreatedAt: time.Now().UTC(),
	}
	history := make([]types.Message, 0, count)
	for index := 0; index < count; index++ {
		content := fmt.Sprintf("message-%02d", index)
		if index%2 == 0 {
			history = append(history, *types.NewUserMessage(content))
		} else {
			history = append(history, *types.NewAssistantMessage(content))
		}
	}
	session.ReplaceHistory(history)
	require.NoError(t, store.Save(ctx, session))
}

// runDivergentHistoryUpdate 模拟生产中的 default 分支触发形态：加载出的投影
// 在内存里被改写（元数据变化，payload 不再与存储逐字节相等），随后追加新 turn
// 并落库。mutate 是让 prefixMatches=false 的确定性开关。
func runDivergentHistoryUpdate(t *testing.T, ctx context.Context, store *SQLiteSessionStorage, sessionID, marker string, appendNew bool) *Session {
	t.Helper()
	loaded, err := store.Load(ctx, sessionID)
	require.NoError(t, err)
	require.NotEmpty(t, loaded.History)
	loaded.History[0].Metadata = types.NewMetadata().With("p03-divergence", marker)
	if appendNew {
		loaded.AddMessage(*types.NewUserMessage("follow-up question"))
		loaded.AddMessage(*types.NewAssistantMessage("follow-up answer"))
	}
	require.NoError(t, store.Update(ctx, loaded))
	return loaded
}

func projectionContents(messages []types.Message) []string {
	contents := make([]string, 0, len(messages))
	for index := range messages {
		contents = append(contents, messages[index].Role+":"+messages[index].Content)
	}
	return contents
}

// TestUpdateProjectionRebuildFastPathMatchesLegacy 是 P0-3 的核心差分测试：
// 同一段会话变更分别走「强制全量」与「identity_hash 快速路径」，canonical 与
// prompt 投影必须逐字节一致；快速路径不得再触碰 loadCanonicalMessagesTx。
func TestUpdateProjectionRebuildFastPathMatchesLegacy(t *testing.T) {
	ctx := context.Background()
	legacy := newIdentityFastPathStore(t)
	fast := newIdentityFastPathStore(t)
	seedIdentityFastPathSession(t, ctx, legacy, "sess-parity", 12)
	seedIdentityFastPathSession(t, ctx, fast, "sess-parity", 12)

	legacy.forceLegacyProjectionRebuild = true
	runDivergentHistoryUpdate(t, ctx, legacy, "sess-parity", "1", true)
	runDivergentHistoryUpdate(t, ctx, fast, "sess-parity", "1", true)

	legacyStats := legacy.CanonicalRebuildStats()
	require.Equal(t, int64(1), legacyStats.FullCanonicalLoads, "强制全量路径必须只走一次全量解码")
	require.Equal(t, int64(0), legacyStats.FastIdentityProofs)
	require.Equal(t, int64(0), legacyStats.BackfilledRows, "新库的行在写入时已带指纹，无需回填")

	fastStats := fast.CanonicalRebuildStats()
	require.Equal(t, int64(0), fastStats.FullCanonicalLoads, "快速路径不得回退全量解码")
	require.Equal(t, int64(1), fastStats.FastIdentityProofs)

	require.Equal(t, rawCanonicalContents(t, legacy, "sess-parity"), rawCanonicalContents(t, fast, "sess-parity"))

	legacyProjection, err := legacy.loadPromptMessages(ctx, "sess-parity")
	require.NoError(t, err)
	fastProjection, err := fast.loadPromptMessages(ctx, "sess-parity")
	require.NoError(t, err)
	require.Equal(t, projectionContents(legacyProjection), projectionContents(fastProjection))
	require.Equal(t, "follow-up answer", lastMessageContent(fastProjection))
}

// TestProjectionRebuildBackfillsLegacyRowsAndSelfHeals 复现升级路径：v1 库的
// identity_hash 全为 NULL → 第一次 default 回退完成回填（全量成本只付一次）→
// 之后的 checkpoint 全部走点查快速路径。
func TestProjectionRebuildBackfillsLegacyRowsAndSelfHeals(t *testing.T) {
	ctx := context.Background()
	store := newIdentityFastPathStore(t)
	seedIdentityFastPathSession(t, ctx, store, "sess-legacy", 12)

	// 模拟 v1 数据库：所有历史行没有身份指纹。
	_, err := store.db.ExecContext(ctx, `UPDATE session_messages SET identity_hash = NULL`)
	require.NoError(t, err)

	// 第一轮：不追加新消息（最新行仍是 NULL 行），default 分支回退 + 回填。
	runDivergentHistoryUpdate(t, ctx, store, "sess-legacy", "1", false)
	stats := store.CanonicalRebuildStats()
	require.Equal(t, int64(1), stats.FullCanonicalLoads)
	require.Equal(t, int64(0), stats.FastIdentityProofs)
	require.Equal(t, int64(12), stats.BackfilledRows)

	var missing int
	require.NoError(t, store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM session_messages WHERE session_id = ? AND identity_hash IS NULL`,
		"sess-legacy").Scan(&missing))
	require.Zero(t, missing, "回填后不应再有 NULL 行")

	// 第二轮：最新行进来了，快速路径必须生效，且不再触发全量解码。
	runDivergentHistoryUpdate(t, ctx, store, "sess-legacy", "2", true)
	stats = store.CanonicalRebuildStats()
	require.Equal(t, int64(1), stats.FullCanonicalLoads, "全量解码不应随 checkpoint 重复发生")
	require.Equal(t, int64(1), stats.FastIdentityProofs)

	projection, err := store.loadPromptMessages(ctx, "sess-legacy")
	require.NoError(t, err)
	require.Equal(t, "follow-up answer", lastMessageContent(projection))
}

// TestProjectionRebuildStaleWindowStillUsesFullCanonicalLoad 锁定快速路径的
// 保守边界：入参最新消息存在于 canonical 但不在最新位置（过期窗口）时，必须
// 回退全量路径，由 incomingHistoryReachesNewest 决定 source，绝不用过期窗口
// 覆盖投影、丢掉最新 turn。
func TestProjectionRebuildStaleWindowStillUsesFullCanonicalLoad(t *testing.T) {
	ctx := context.Background()
	store := newIdentityFastPathStore(t)
	seedIdentityFastPathSession(t, ctx, store, "sess-stale-p03", 8)

	loaded, err := store.Load(ctx, "sess-stale-p03")
	require.NoError(t, err)
	require.Equal(t, "message-07", lastMessageContent(loaded.History))

	stale := &Session{
		ID:                    "sess-stale-p03",
		UserID:                "tester",
		State:                 StateActive,
		CanonicalMessageCount: 8,
		HistoryLoaded:         true,
	}
	staleHistory := make([]types.Message, 0, 5)
	for index := 2; index <= 6; index++ {
		content := fmt.Sprintf("message-%02d", index)
		if index%2 == 0 {
			staleHistory = append(staleHistory, *types.NewUserMessage(content))
		} else {
			staleHistory = append(staleHistory, *types.NewAssistantMessage(content))
		}
	}
	stale.ReplaceHistory(staleHistory)
	require.NoError(t, store.Update(ctx, stale))

	stats := store.CanonicalRebuildStats()
	require.Equal(t, int64(1), stats.FullCanonicalLoads, "过期窗口必须回退全量路径")
	require.Equal(t, int64(0), stats.FastIdentityProofs)
	require.Equal(t, int64(0), stats.BackfilledRows)

	after, err := store.Load(ctx, "sess-stale-p03")
	require.NoError(t, err)
	require.Equal(t, "message-07", lastMessageContent(after.History),
		"快速路径的保守性不能破坏 stale sync 保护语义")
}

// TestCanonicalMessageIdentityHashMirrorsIdentityEquality 锁定指纹语义与
// incomingHistoryReachesNewest 的 messageIdentityEqual 完全一致：只看
// role+content；重建副本（id/时间戳/metadata 不同）视为同一条消息；字段边界
// 不允许碰撞。
func TestCanonicalMessageIdentityHashMirrorsIdentityEquality(t *testing.T) {
	base := *types.NewAssistantMessage("answer")
	rebuilt := base
	rebuilt.Metadata = types.NewMetadata().With("rebuilt", "true")
	rebuilt.ToolCalls = []types.ToolCall{{ID: "call-late"}}
	require.True(t, bytes.Equal(canonicalMessageIdentityHash(base), canonicalMessageIdentityHash(rebuilt)),
		"role+content 相同即同一条消息，忽略 metadata/tool_calls/时间戳")

	otherContent := *types.NewAssistantMessage("answer-2")
	require.False(t, bytes.Equal(canonicalMessageIdentityHash(base), canonicalMessageIdentityHash(otherContent)))

	// 长度前缀拼接：("ab","c") 与 ("a","bc") 不能碰撞成同一指纹。
	left := types.Message{Role: "ab", Content: "c"}
	right := types.Message{Role: "a", Content: "bc"}
	require.False(t, bytes.Equal(canonicalMessageIdentityHash(left), canonicalMessageIdentityHash(right)))
}

// TestIncomingHistoryReachesNewestFastProofBranches 覆盖快速证明的三类结论：
// 消息已在最新位置（true）、消息只存在于更早位置（false → 回退）、消息不存在
// （true，保持 honor caller replacement 语义）；未回填的最新行必须保守回退。
func TestIncomingHistoryReachesNewestFastProofBranches(t *testing.T) {
	ctx := context.Background()
	store := newIdentityFastPathStore(t)
	seedIdentityFastPathSession(t, ctx, store, "sess-proof", 6)

	prove := func(history []types.Message) bool {
		t.Helper()
		ctxTx, tx, tracker, err := store.beginWriteTx(ctx)
		require.NoError(t, err)
		defer store.rollbackWriteTx(tx, tracker)
		proven, err := store.incomingHistoryReachesNewestFastTx(ctxTx, tx, "sess-proof", history)
		require.NoError(t, err)
		return proven
	}

	require.True(t, prove([]types.Message{*types.NewAssistantMessage("message-05")}),
		"最新 canonical 行身份一致 → 已到最新")
	require.False(t, prove([]types.Message{*types.NewAssistantMessage("message-03")}),
		"只存在于更早位置 → 过期窗口，必须回退全量")
	require.True(t, prove([]types.Message{*types.NewAssistantMessage("brand new summary")}),
		"canonical 中不存在 → 按新消息（compaction summary 等）处理")

	// 最新行未回填（v1 库升级后的第一次落库）：无法证明，保守回退。
	_, err := store.db.ExecContext(ctx, `
		UPDATE session_messages SET identity_hash = NULL
		WHERE session_id = ? AND seq = (SELECT MAX(seq) FROM session_messages WHERE session_id = ?)
	`, "sess-proof", "sess-proof")
	require.NoError(t, err)
	require.False(t, prove([]types.Message{*types.NewAssistantMessage("message-05")}),
		"最新行无指纹时不得跳过全量路径")
}

// TestSchemaMigrationAddsIdentityHashToLegacyDatabase 锁定 v1 → v2 迁移：既有
// 数据库重新打开时必须补列 + 重建索引，历史行保持可读（指纹为 NULL，等待
// 第一次全量回退时的惰性回填）。
func TestSchemaMigrationAddsIdentityHashToLegacyDatabase(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cfg := DefaultPersistentSessionStorageConfig(dir)
	cfg.Path = filepath.Join(dir, "sessions.sqlite")
	cfg.ImportLegacyJSON = false

	store, err := NewSQLiteSessionStorage(cfg)
	require.NoError(t, err)
	seedIdentityFastPathSession(t, ctx, store, "sess-migrate", 6)

	// 把 schema 降级成 v1 形态：删索引、删列、回退版本号。
	_, err = store.db.ExecContext(ctx, `DROP INDEX IF EXISTS idx_session_messages_identity`)
	require.NoError(t, err)
	_, err = store.db.ExecContext(ctx, `ALTER TABLE session_messages DROP COLUMN identity_hash`)
	require.NoError(t, err)
	_, err = store.db.ExecContext(ctx, `PRAGMA user_version = 1`)
	require.NoError(t, err)
	require.NoError(t, store.CloseStorage())

	reopened, err := NewSQLiteSessionStorage(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.CloseStorage()) })

	var version int
	require.NoError(t, reopened.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version))
	require.Equal(t, sqliteSessionSchemaVersion, version)

	var hasColumn, hasIndex bool
	columnRows, err := reopened.db.QueryContext(ctx, `PRAGMA table_info(session_messages)`)
	require.NoError(t, err)
	for columnRows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue interface{}
		require.NoError(t, columnRows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey))
		if name == "identity_hash" {
			hasColumn = true
		}
	}
	require.NoError(t, columnRows.Err())
	require.NoError(t, columnRows.Close())
	require.True(t, hasColumn, "迁移必须补上 identity_hash 列")

	indexRows, err := reopened.db.QueryContext(ctx, `PRAGMA index_list(session_messages)`)
	require.NoError(t, err)
	for indexRows.Next() {
		var seq, unique, partial int
		var name, origin string
		require.NoError(t, indexRows.Scan(&seq, &name, &unique, &origin, &partial))
		if name == "idx_session_messages_identity" {
			hasIndex = true
		}
	}
	require.NoError(t, indexRows.Err())
	require.NoError(t, indexRows.Close())
	require.True(t, hasIndex, "迁移必须重建 identity_hash 索引")

	var missing int
	require.NoError(t, reopened.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM session_messages WHERE session_id = ? AND identity_hash IS NULL`,
		"sess-migrate").Scan(&missing))
	require.Equal(t, 6, missing, "迁移只补列，历史行等待惰性回填")

	loaded, err := reopened.Load(ctx, "sess-migrate")
	require.NoError(t, err)
	require.NotEmpty(t, loaded.History, "迁移不得影响既有会话读取")
}
