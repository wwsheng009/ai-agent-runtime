package skills

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
)

// P2-9 API 宿主：对账循环的配置口径必须与 CLI 宿主一致（env > runtime config > 默认），
// 否则同一部署下两个宿主会跑出不同节奏的审计。
func TestAgentRegistryReconcileTuningPrecedence(t *testing.T) {
	t.Run("no config falls back to defaults", func(t *testing.T) {
		t.Setenv(apiRegistryReconcileIntervalEnv, "")
		t.Setenv(apiRegistryReconcileModeEnv, "")
		handler := NewHandler(nil, nil, nil)
		mode, interval := handler.agentRegistryReconcileTuning()
		require.Equal(t, agentcontrol.ReconcileModeObserve, mode)
		require.Equal(t, agentcontrol.DefaultReconcileInterval, interval)
	})

	t.Run("runtime config wins over defaults", func(t *testing.T) {
		t.Setenv(apiRegistryReconcileIntervalEnv, "")
		t.Setenv(apiRegistryReconcileModeEnv, "")
		handler := NewHandler(nil, nil, nil)
		cfg := runtimecfg.DefaultRuntimeConfig()
		cfg.Agents.RegistryReconcileInterval = 3 * time.Minute
		cfg.Agents.RegistryReconcileMode = "enforce"
		handler.SetRuntimeConfig(cfg, "")

		mode, interval := handler.agentRegistryReconcileTuning()
		require.Equal(t, agentcontrol.ReconcileModeEnforce, mode)
		require.Equal(t, 3*time.Minute, interval)
	})

	t.Run("env overrides runtime config", func(t *testing.T) {
		t.Setenv(apiRegistryReconcileIntervalEnv, "45m")
		t.Setenv(apiRegistryReconcileModeEnv, "observe")
		handler := NewHandler(nil, nil, nil)
		cfg := runtimecfg.DefaultRuntimeConfig()
		cfg.Agents.RegistryReconcileInterval = 3 * time.Minute
		cfg.Agents.RegistryReconcileMode = "enforce"
		handler.SetRuntimeConfig(cfg, "")

		mode, interval := handler.agentRegistryReconcileTuning()
		require.Equal(t, agentcontrol.ReconcileModeObserve, mode)
		require.Equal(t, 45*time.Minute, interval)
	})

	t.Run("invalid env duration keeps config interval", func(t *testing.T) {
		t.Setenv(apiRegistryReconcileIntervalEnv, "45")
		t.Setenv(apiRegistryReconcileModeEnv, "")
		handler := NewHandler(nil, nil, nil)
		cfg := runtimecfg.DefaultRuntimeConfig()
		cfg.Agents.RegistryReconcileInterval = 3 * time.Minute
		handler.SetRuntimeConfig(cfg, "")

		_, interval := handler.agentRegistryReconcileTuning()
		require.Equal(t, 3*time.Minute, interval)
	})

	t.Run("unknown mode stays observe and floor applies", func(t *testing.T) {
		t.Setenv(apiRegistryReconcileIntervalEnv, "10s")
		t.Setenv(apiRegistryReconcileModeEnv, "aggressive")
		handler := NewHandler(nil, nil, nil)

		mode, interval := handler.agentRegistryReconcileTuning()
		require.Equal(t, agentcontrol.ReconcileModeObserve, mode)
		require.Equal(t, agentcontrol.MinReconcileInterval, interval)
	})

	t.Run("nil handler is safe", func(t *testing.T) {
		var handler *Handler
		mode, interval := handler.agentRegistryReconcileTuning()
		require.Equal(t, agentcontrol.ReconcileModeObserve, mode)
		require.Equal(t, agentcontrol.DefaultReconcileInterval, interval)
		require.Equal(t, "reconcile=not_run", handler.agentRegistryReconcileSummary())
	})
}

// P2-9 API 宿主：enforce 模式下周期性对账应把「会话已不存在」的 active 行收敛掉，
// 并把最近一次对账结果暴露给 HTTP supervision API；未配置 store 时保持 not_run。
func TestAgentRegistryReconcilerConvergesMissingAgentSession(t *testing.T) {
	t.Setenv(apiRegistryReconcileIntervalEnv, "1m")
	t.Setenv(apiRegistryReconcileModeEnv, string(agentcontrol.ReconcileModeEnforce))

	handler := NewHandler(nil, nil, nil)
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	defer sessionManager.Stop()
	defer handler.getSessionHub().StopAll()
	handler.SetSessionManager(sessionManager)

	store, err := agentcontrol.NewSQLiteGlobalAgentRegistryStore(&agentcontrol.GlobalAgentStoreConfig{
		Path: filepath.Join(t.TempDir(), "api-registry-reconcile.db"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	// 未接线 durable store 时不应有对账循环。
	require.Equal(t, "reconcile=not_run", handler.agentRegistryReconcileSummary())
	require.Nil(t, handler.ensureAgentRegistryReconciler())

	ctx := context.Background()
	_, err = store.UpsertAgentControlAgent(ctx, agentcontrol.AgentRecord{
		AgentID:       "api-missing-agent",
		RootSessionID: "api-missing-root",
		SessionID:     "api-missing-session",
		AgentPath:     "/root/missing",
		AgentType:     agentcontrol.AgentTypeChild,
		Status:        agentcontrol.AgentStatusActive,
	})
	require.NoError(t, err)

	// 注入 store 即启动循环，RunLoop 的首次 pass 立刻收敛已存在的漂移。
	handler.SetAgentControlAgentStore(store)
	reconciler := handler.ensureAgentRegistryReconciler()
	require.NotNil(t, reconciler)

	require.Eventually(t, func() bool {
		records, listErr := store.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{
			AgentID:       "api-missing-agent",
			IncludeClosed: true,
		})
		if listErr != nil || len(records) == 0 {
			return false
		}
		return records[0].Closed()
	}, 5*time.Second, 25*time.Millisecond)

	summary := handler.agentRegistryReconcileSummary()
	require.Contains(t, summary, "reconcile=enforce")
	require.Contains(t, summary, "last_reconcile=")

	// 对账必须幂等：同一批漂移已收敛后再次运行不再产生 issue。
	// 首次 pass 可能仍在收尾（Reconciler 拒绝叠加 pass），因此轮询到一次干净的空跑。
	require.Eventually(t, func() bool {
		report, runErr := reconciler.RunOnce(ctx)
		return runErr == nil && report.IssueCount == 0
	}, 5*time.Second, 25*time.Millisecond)

	// 同一 store 重复接线不会重复起循环。
	require.Same(t, reconciler, handler.ensureAgentRegistryReconciler())

	// store 被摘除后循环停止，摘要回到 not_run。
	handler.SetAgentControlAgentStore(nil)
	require.Nil(t, handler.ensureAgentRegistryReconciler())
	require.Equal(t, "reconcile=not_run", handler.agentRegistryReconcileSummary())
}
