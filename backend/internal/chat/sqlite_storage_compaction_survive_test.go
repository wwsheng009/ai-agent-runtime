package chat

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// TestCompactionSummarySurvivesFullWindowSync 复现生产事故（2026-10-21）：
//
// 场景：会话已经过多次 compact，session_prompt_messages 里是「一条 compaction
// 摘要 + 保留尾巴」（本测试 21 行）。此时 CLI 把**展示用全量历史**（18k 条，
// 来自 canonical 分页回放）当作模型历史同步回来，Update 落到 default 分支，
// 用整段 canonical 覆盖投影，产生三重后果：
//
//  1. 压缩摘要被冲掉（canonical 里没有摘要行 —— 摘要只存在于投影）→ 模型丢失
//     "更早发生了什么"，且系统会对 18k 条原始消息重新做一次昂贵的压缩；
//  2. 投影从 21 行涨到 200 行，失去所有上限；
//  3. applyStoredPromptProjection 把这份全量投影挂回 session.History —— 这就是
//     生产环境 157 MB 常驻堆（loadCanonicalMessagesTx 在 gc=1 后仍 inuse）的来源。
//
// 本测试锁定「压缩边界」这个契约：已经存在的摘要，不得被一次普通的
// 历史同步抹掉。
func TestCompactionSummarySurvivesFullWindowSync(t *testing.T) {
	ctx := context.Background()
	store := newIdentityFastPathStore(t)

	const total = 200
	const retained = 20
	seedIdentityFastPathSession(t, ctx, store, "sess-compact-overwrite", total)

	// --- 阶段 1：模型驱动压缩（摘要 + 保留尾巴） ---
	loaded, err := store.Load(ctx, "sess-compact-overwrite")
	require.NoError(t, err)
	require.Equal(t, total, len(loaded.History))

	summary := *types.NewSystemMessage("summary of messages 0..179")
	summary.Metadata["context_stage"] = "compaction"
	compacted := make([]types.Message, 0, retained+1)
	compacted = append(compacted, summary)
	compacted = append(compacted, loaded.History[total-retained:]...)

	loaded.ReplaceHistory(compacted)
	loaded.MarkHistoryReplaced()
	require.NoError(t, store.Update(ctx, loaded))

	projection, err := store.loadPromptMessages(ctx, "sess-compact-overwrite")
	require.NoError(t, err)
	require.Equal(t, retained+1, len(projection), "压缩后投影必须是有界的「摘要 + 尾巴」")
	require.Equal(t, "compaction", projection[0].Metadata.GetString("context_stage", ""))

	// --- 阶段 2：CLI 把展示用全量历史同步回来（不带 HistoryReplaced） ---
	canonical, err := store.loadCanonicalMessagesReadOnly(ctx, "sess-compact-overwrite")
	require.NoError(t, err)
	require.Greater(t, len(canonical), retained+1, "canonical 应当仍是完整转录")

	synced := &Session{
		ID:                    "sess-compact-overwrite",
		UserID:                "tester",
		State:                 StateActive,
		CanonicalMessageCount: len(canonical),
		HistoryLoaded:         true,
	}
	synced.ReplaceHistory(canonical)
	require.False(t, synced.HistoryReplaced, "全量同步不是压缩替换：不得走信任调用者的分支")
	require.NoError(t, store.Update(ctx, synced))

	// --- 断言 1：压缩摘要不得丢失 ---
	after, err := store.loadPromptMessages(ctx, "sess-compact-overwrite")
	require.NoError(t, err)
	hasSummary := false
	for index := range after {
		if after[index].Metadata.GetString("context_stage", "") == "compaction" {
			hasSummary = true
			break
		}
	}
	t.Logf("投影规模：压缩后 %d 行（含摘要）→ 全量同步后 %d 行（含摘要=%v）",
		retained+1, len(after), hasSummary)
	t.Logf("回写后 session.History = %d 条；canonical = %d 条", len(synced.History), len(canonical))
	stats := store.CanonicalRebuildStats()
	t.Logf("rebuild stats: bounded=%d fallback=%d full=%d fast=%d",
		stats.BoundedRebuilds, stats.BoundedFallbacks, stats.FullCanonicalLoads, stats.FastIdentityProofs)
	require.True(t, hasSummary,
		"全量窗口同步不得冲掉已存在的 compaction 摘要（否则模型丢失压缩前上下文，且会重复压缩 18k 条）")

	// --- 断言 2：投影必须保持有界 ---
	require.LessOrEqual(t, len(after), retained+1+2,
		"全量窗口同步不得把有界投影放大成整段 canonical（实测事故：724 行 -> 18,528 行）")

	// --- 断言 3：内存不得被钉住 ---
	// applyStoredPromptProjection 会把存储的投影挂回 session.History；
	// 若投影是全量，调用方进程里就常驻了整条转录。
	require.LessOrEqual(t, len(synced.History), retained+1+2,
		"回写后 session.History 必须保持有界，否则整条转录被钉在内存里（实测 157 MB 常驻）")

	// --- 断言 4：canonical 事实来源不得被删改 ---
	var canonicalCount int
	require.NoError(t, store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM session_messages WHERE session_id = ?`,
		"sess-compact-overwrite").Scan(&canonicalCount))
	require.GreaterOrEqual(t, canonicalCount, len(canonical),
		"投影重建不得删除 canonical 行")
}

// TestCompactionSummarySurvivesStaleWindowSync 覆盖同一缺陷的另一个入口：
// 调用方递来的是一段**没有到达最新行**的过期窗口（CLI exit sync 与 actor
// persist 竞争时的典型形状）。这条路径走的是 proven=false 的整段 canonical
// 回退，同样会重建投影。
func TestCompactionSummarySurvivesStaleWindowSync(t *testing.T) {
	ctx := context.Background()
	store := newIdentityFastPathStore(t)

	const total = 120
	const retained = 10
	seedIdentityFastPathSession(t, ctx, store, "sess-compact-stale", total)

	loaded, err := store.Load(ctx, "sess-compact-stale")
	require.NoError(t, err)

	summary := *types.NewSystemMessage("summary of messages 0..109")
	summary.Metadata["context_stage"] = "compaction"
	compacted := make([]types.Message, 0, retained+1)
	compacted = append(compacted, summary)
	compacted = append(compacted, loaded.History[total-retained:]...)

	loaded.ReplaceHistory(compacted)
	loaded.MarkHistoryReplaced()
	require.NoError(t, store.Update(ctx, loaded))

	// 过期窗口：只保留最早的一段历史（结束位置远早于最新行）。
	stale := make([]types.Message, 0, 5)
	for index := 0; index < 5; index++ {
		content := fmt.Sprintf("message-%02d", index)
		if index%2 == 0 {
			stale = append(stale, *types.NewUserMessage(content))
		} else {
			stale = append(stale, *types.NewAssistantMessage(content))
		}
	}
	synced := &Session{
		ID:                    "sess-compact-stale",
		UserID:                "tester",
		State:                 StateActive,
		CanonicalMessageCount: total,
		HistoryLoaded:         true,
	}
	synced.ReplaceHistory(stale)
	require.NoError(t, store.Update(ctx, synced))

	after, err := store.loadPromptMessages(ctx, "sess-compact-stale")
	require.NoError(t, err)

	hasSummary := false
	for index := range after {
		if after[index].Metadata.GetString("context_stage", "") == "compaction" {
			hasSummary = true
			break
		}
	}
	require.True(t, hasSummary, "过期窗口同步同样不得冲掉 compaction 摘要")
	require.LessOrEqual(t, len(after), retained+1+2,
		"过期窗口同步不得把投影放大成整段 canonical")
}

// TestBoundedRebuildStillAppendsNewestTurn 确认「保住压缩边界」没有走向另一个
// 极端：修复不能退化成"永远不追加"。压缩之后新增的轮次必须照常进入投影，
// 否则模型会看不到用户最新说的话。
func TestBoundedRebuildStillAppendsNewestTurn(t *testing.T) {
	ctx := context.Background()
	store := newIdentityFastPathStore(t)

	const total = 60
	const retained = 10
	seedIdentityFastPathSession(t, ctx, store, "sess-append-after-compact", total)

	loaded, err := store.Load(ctx, "sess-append-after-compact")
	require.NoError(t, err)

	summary := *types.NewSystemMessage("summary of messages 0..49")
	summary.Metadata["context_stage"] = "compaction"
	compacted := make([]types.Message, 0, retained+1)
	compacted = append(compacted, summary)
	compacted = append(compacted, loaded.History[total-retained:]...)
	loaded.ReplaceHistory(compacted)
	loaded.MarkHistoryReplaced()
	require.NoError(t, store.Update(ctx, loaded))

	// 压缩后继续对话：追加一轮新的 user/assistant。
	afterCompact, err := store.Load(ctx, "sess-append-after-compact")
	require.NoError(t, err)
	newTurn := []types.Message{
		*types.NewUserMessage("brand new question after compaction"),
		*types.NewAssistantMessage("brand new answer after compaction"),
	}
	afterCompact.AddMessage(newTurn[0])
	afterCompact.AddMessage(newTurn[1])
	require.NoError(t, store.Update(ctx, afterCompact))

	projection, err := store.loadPromptMessages(ctx, "sess-append-after-compact")
	require.NoError(t, err)

	// 摘要仍在。
	require.Equal(t, "compaction", projection[0].Metadata.GetString("context_stage", ""),
		"追加新轮次不得冲掉压缩摘要")
	// 最新一轮必须在投影里。
	require.Equal(t, "brand new question after compaction", projection[len(projection)-2].Content)
	require.Equal(t, "brand new answer after compaction", projection[len(projection)-1].Content)
	// 仍然有界。
	require.LessOrEqual(t, len(projection), retained+3,
		"追加后仍必须保持有界，不得回退成整段转录")
}
