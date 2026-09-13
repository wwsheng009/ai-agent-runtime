package skills

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// TestSessionAgentController_SpawnRollsBackReservationOnActorFailure 是 P2-10 的
// “spawn 失败回滚”故障注入用例：配额预留成功之后，只要子会话 actor 创建失败，控制器必须
//  1. 回滚 durable 预留（行落在 stale 终态，而不是留在 active 集合里形成配额僵尸，N1）；
//  2. 删除已保存的子会话，不留半成品；
//  3. 返回带可诊断原因的错误。
//
// 注入方式：把会话 hub 换成一个没有 actor factory 的实例，`GetOrCreate` 必然失败，
// 这是 `Spawn` 中预留成功之后唯一不依赖外部环境即可稳定触发的失败点。
func TestSessionAgentController_SpawnRollsBackReservationOnActorFailure(t *testing.T) {
	ctx := context.Background()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	defer sessionManager.Stop()
	handler.SetSessionManager(sessionManager)
	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.Agents.MaxThreads = 1
	handler.SetRuntimeConfig(cfg, "")
	store, err := agentcontrol.NewSQLiteGlobalAgentRegistryStore(&agentcontrol.GlobalAgentStoreConfig{
		Path: t.TempDir() + "/agents.db",
	})
	require.NoError(t, err)
	defer store.Close()
	handler.SetAgentControlAgentStore(store)

	rootSession, err := sessionManager.Create(ctx, "user-session-spawn-rollback")
	require.NoError(t, err)

	brokenHub := chat.NewSessionHub(nil)
	handler.sessionRuntimeMu.Lock()
	handler.sessionHub = brokenHub
	handler.sessionRuntimeMu.Unlock()
	defer brokenHub.StopAll()

	controller := handler.getAgentSessionController()
	require.NotNil(t, controller)
	_, err = controller.Spawn(ctx, rootSession.ID, toolbroker.SpawnAgentArgs{ID: "api-rollback-child"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "create child session actor")

	// 半成品会话必须被删除。
	_, err = sessionManager.Get(ctx, "api-rollback-child")
	require.Error(t, err)

	// 预留必须回滚：行保留可审计，但状态已终态。
	records, err := store.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{
		RootSessionID: rootSession.ID,
		IncludeClosed: true,
	})
	require.NoError(t, err)
	var rollbackRecord *agentcontrol.AgentRecord
	for index := range records {
		if records[index].SessionID == "api-rollback-child" {
			rollbackRecord = &records[index]
		}
	}
	require.NotNil(t, rollbackRecord, "rolled back spawn must keep an auditable registry row")
	require.Equal(t, agentcontrol.AgentStatusStale, rollbackRecord.Status)
	require.True(t, rollbackRecord.Closed())

	// active 集合只应剩 root 行本身：失败预留不占额度。
	active, err := store.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{RootSessionID: rootSession.ID})
	require.NoError(t, err)
	require.Len(t, active, 1, "only the root row may stay active after a rolled back spawn")
	require.Equal(t, rootSession.ID, active[0].SessionID)
}
