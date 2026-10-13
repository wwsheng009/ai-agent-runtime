package chat

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// TestUpdateWithCompactionReplacementKeepsReplacementProjection 复现 2026-10-13
// 事故：压缩替换（摘要 + 保留尾巴）被存储层当作"过期窗口"，从 append-only 的
// canonical 转录重建投影，把压缩前的消息带回投影，下一次 Load/恢复就会重新
// 发送全量历史（实际 1.1M tokens > 模型 1M 窗口 → 上游 400）。
// MarkHistoryReplaced 必须让替换被无条件写入投影，且是一次性标志。
func TestUpdateWithCompactionReplacementKeepsReplacementProjection(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, nil)

	session := NewSession("compaction-user")
	session.ReplaceHistory([]types.Message{
		*types.NewUserMessage("u1"),
		*types.NewAssistantMessage("a1"),
	})
	require.NoError(t, store.Save(ctx, session))

	// canonical 最新消息变成 u2：此后 "a1" 仍存在于 canonical，但不再是最后
	// 一条 —— 旧的启发式（incomingHistoryReachesNewest）因此会把替换判成
	// 过期窗口并从 canonical 全量重建。
	appended, err := store.Load(ctx, session.ID)
	require.NoError(t, err)
	appended.AddMessage(*types.NewUserMessage("u2"))
	require.NoError(t, store.Update(ctx, appended))

	// 负例（既有语义）：不带标记的短历史仍按"过期窗口"回退 canonical 全量。
	stale, err := store.Load(ctx, session.ID)
	require.NoError(t, err)
	stale.ReplaceHistory([]types.Message{*types.NewAssistantMessage("a1")})
	require.NoError(t, store.Update(ctx, stale))
	staleLoaded, err := store.Load(ctx, session.ID)
	require.NoError(t, err)
	require.Len(t, staleLoaded.History, 3, "stale window must still rebuild from canonical")
	require.Equal(t, "u2", staleLoaded.History[len(staleLoaded.History)-1].Content)

	// 正例：压缩替换 + 标记 → 投影必须等于替换本身，不含压缩前的 u1/u2。
	replacement := []types.Message{
		*types.NewUserMessage("Compacted context from earlier turns: summary"),
		*types.NewAssistantMessage("a1"),
	}
	loaded, err := store.Load(ctx, session.ID)
	require.NoError(t, err)
	loaded.ReplaceHistory(replacement)
	loaded.MarkHistoryReplaced()
	require.NoError(t, store.Update(ctx, loaded))
	require.False(t, loaded.HistoryReplaced, "replacement flag must be one-shot")

	got, err := store.Load(ctx, session.ID)
	require.NoError(t, err)
	require.Len(t, got.History, 2, "compaction replacement must not be canonical-rebuilt")
	require.Equal(t, "Compacted context from earlier turns: summary", got.History[0].Content)
	require.Equal(t, "a1", got.History[1].Content)
}
