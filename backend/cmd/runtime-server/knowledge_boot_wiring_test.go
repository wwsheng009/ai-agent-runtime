package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/knowledge"
	"github.com/wwsheng009/ai-agent-runtime/internal/model/entity"
	"github.com/wwsheng009/ai-agent-runtime/internal/usageledger"
)

// ADR-0008 §8.1 三入口之一（runtime-server）：knowledge_boot.go 的落库口在
// 账本可用时必须把 file-level 两列写入；账本不可用时返回 nil（观察器整体
// no-op，mode=off / 未启用零写入，与 CLI 宿主 ledgerAttributionSink 同口径）。
func TestKnowledgeAttributionSinkWritesFileLevelColumns(t *testing.T) {
	require.Nil(t, knowledgeAttributionSink(nil), "账本未启用时 sink 必须为 nil")
	require.Nil(t, knowledgeAttributionSink("not-a-store"), "具体类型不实现接口时 sink 为 nil")

	store, err := usageledger.NewSQLiteStore(&usageledger.Config{
		Driver: "sqlite",
		DSN:    filepath.Join(t.TempDir(), "ledger.db"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	var sink knowledge.AttributionSink = knowledgeAttributionSink(store)
	require.NotNil(t, sink)

	ctx := context.Background()
	require.NoError(t, sink.AppendExplorationAttribution(ctx, &entity.ExplorationAttribution{
		ID: "server-1", Tool: "grep", KnowledgeMode: "shadow",
		BaselineN: 2, BaselineFilesN: 2, CandidateN: 1, OverlapN: 1, OverlapFilesN: 1,
		BaselineTokens: 20, CandidateTokens: 5,
		CreatedAt: time.Now().UTC(),
	}))

	rows, err := store.ListExplorationAttribution(ctx, time.Time{}, 10)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, 2, rows[0].BaselineFilesN, "ADR-0008 §4：baseline 文件集合落库")
	require.Equal(t, 1, rows[0].OverlapFilesN, "ADR-0008 §4：文件交集落库")
}
