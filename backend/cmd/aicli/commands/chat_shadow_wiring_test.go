package commands

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/knowledge"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
	"github.com/wwsheng009/ai-agent-runtime/internal/usageledger"
)

// ADR-0008 §8.1 三入口之一（aicli：chat_actor_host 与 agent_stdio 共用
// applyLocalChatToolObservation）：shadow 观察器必须把 grep 的 file-level 两列
// 经真实账本写入；mode=off / 账本未启用时 hook 保持 nil（零写入）。
func TestApplyLocalChatToolObservation_WritesFileLevelColumns(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	cfg := knowledge.DefaultConfig().WithWorkspace(root)
	cfg.Mode = knowledge.ModeShadow
	act, err := knowledge.Activate(ctx, cfg, root, knowledge.ActivationOptions{SkipInitialIndex: true})
	require.NoError(t, err)
	require.NotNil(t, act)
	t.Cleanup(func() { _ = act.Close() })

	store, err := usageledger.NewSQLiteStore(&usageledger.Config{
		Driver: "sqlite",
		DSN:    filepath.Join(t.TempDir(), "ledger.db"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	session := &ChatSession{Knowledge: act}
	host := &localChatRuntimeHost{ledgerSvc: usageledger.NewService(store)}
	config := &agent.LoopReActConfig{}
	applyLocalChatToolObservation(config, session, host, knowledge.ObservationSourceMainSession, "")
	require.NotNil(t, config.OnToolObserved)

	config.OnToolObserved(ctx, "sess-cli", runtimetypes.ToolCall{
		Name: "grep",
		Args: map[string]interface{}{"pattern": "Foo"},
	}, "backend/a.go:3:func Foo\n", "")

	rows, err := store.ListExplorationAttribution(ctx, time.Time{}, 10)
	require.NoError(t, err)
	require.Len(t, rows, 1, "CLI 入口 live 写入一行")
	require.Equal(t, "sess-cli", rows[0].SessionID)
	require.Equal(t, 1, rows[0].BaselineFilesN, "ADR-0008 §4：baseline 文件集合落库")
	require.Zero(t, rows[0].OverlapFilesN, "空索引无候选 → 文件交集为 0")
	require.False(t, rows[0].Usable, "file_coverage = 0 < α 时不可用")

	// 账本未启用（host.ledgerSvc == nil）→ shadow sink 为 nil（exploration_attribution
	// 零写入）；但 W2 起探索记忆采集器不依赖账本，hook 仍须接线。
	offHost := &localChatRuntimeHost{}
	offConfig := &agent.LoopReActConfig{}
	applyLocalChatToolObservation(offConfig, session, offHost, knowledge.ObservationSourceMainSession, "")
	require.NotNil(t, offConfig.OnToolObserved, "探索记忆采集器不依赖账本，必须独立接线")

	// mode=off（Knowledge == nil）→ 同样零接线。
	offConfig = &agent.LoopReActConfig{}
	applyLocalChatToolObservation(offConfig, &ChatSession{}, host, knowledge.ObservationSourceMainSession, "")
	require.Nil(t, offConfig.OnToolObserved, "知识层 off 时不得接线")
}
