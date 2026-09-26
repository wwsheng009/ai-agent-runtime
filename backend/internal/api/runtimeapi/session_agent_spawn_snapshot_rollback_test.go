package runtimeapi

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	runtimeagent "github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// TestSessionAgentController_SpawnRollsBackReservationWhenSnapshotFails 覆盖 N1 的
// 最后一条未补偿路径：durable 预约已成功、且**没有**排队任何 prompt 时，状态快照读回
// 失败。此时没有任何 run 会把子会话推向终态，残留的 active 行会永久占用
// agents.maxThreads（与 N1 同类僵尸行），控制器必须走 rollbackSpawnFailure。
//
// 故障注入方式：把 team store 换成一个已关闭的 SQLite 库。`snapshot` 里的
// `enrichAgentTeamProjection`（`team.FindTeammateBySession` → `Store.ListTeams`）
// 是预约之后、队列之前唯一会读它的调用，因此错误稳定落在快照阶段；这也是该阶段真实
// 可能出现的失败（team 库不可用）。对照 rollback 用例：那里用无工厂的 hub 让
// `GetOrCreate` 先失败，覆盖的是另一个分支。
func TestSessionAgentController_SpawnRollsBackReservationWhenSnapshotFails(t *testing.T) {
	ctx := context.Background()
	const childID = "api-snapshot-fail-child"

	storage := chat.NewInMemoryStorage()
	sessionManager := chat.NewSessionManager(storage, nil)
	defer sessionManager.Stop()

	runtimeStore := chat.NewInMemoryRuntimeStore(64)
	apiAgent := runtimeagent.NewAgent(&runtimeagent.Config{
		Name:  "spawn-snapshot-test",
		Model: "test-model",
	}, nil)

	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetSessionManager(sessionManager)
	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.Agents.MaxThreads = 1
	handler.SetRuntimeConfig(cfg, "")

	store, err := agentcontrol.NewSQLiteGlobalAgentRegistryStore(&agentcontrol.GlobalAgentStoreConfig{
		Path: t.TempDir() + "/agents.db",
	})
	require.NoError(t, err)
	// 先停掉周期对账循环再关库：否则后台 pass 会拿着已关闭的 *sql.DB 继续投影。
	t.Cleanup(func() { _ = store.Close() })
	handler.SetAgentControlAgentStore(store)
	t.Cleanup(func() {
		handler.agentControlReconcileMu.Lock()
		if handler.agentControlReconcilerStop != nil {
			handler.agentControlReconcilerStop()
			handler.agentControlReconcilerStop = nil
		}
		handler.agentControlReconcileMu.Unlock()
	})

	// 不可打开的 team store：懒加载的 SQLite 库在首次真实操作时才打开，用「目录当
	// 库文件」的路径保证打开失败，失败点因此稳定落在 `snapshot` 的 team 投影读上。
	// 先探测一次，避免注入失效时用例变成一个静默的空跑。
	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: t.TempDir()})
	require.NoError(t, err)
	_, err = teamStore.ListTeams(ctx, team.TeamFilter{})
	require.Error(t, err, "team store must be unusable so the snapshot projection read fails")
	handler.teamStore = teamStore

	// 本用例要走到预约之后的快照读回，actor 必须能真正创建出来。
	handler.sessionHub = chat.NewSessionHub(func(sessionID string) (*chat.SessionActor, error) {
		return chat.NewSessionActor(sessionID, chat.SessionActorConfig{
			Agent:        apiAgent,
			SessionStore: storage,
			StateStore:   runtimeStore,
			EventStore:   runtimeStore,
		})
	})
	defer handler.sessionHub.StopAll()

	rootSession, err := sessionManager.Create(ctx, "user-session-spawn-snapshot")
	require.NoError(t, err)

	controller := handler.getAgentSessionController()
	require.NotNil(t, controller)
	// 不带 message：没有 prompt 被排队，因此没有 run 会收敛这笔预约。
	_, err = controller.Spawn(ctx, rootSession.ID, toolbroker.SpawnAgentArgs{ID: childID})
	require.Error(t, err)
	require.Contains(t, err.Error(), "snapshot child session")

	// 半成品会话必须被删除。
	_, err = sessionManager.Get(ctx, childID)
	require.Error(t, err)

	// 预留必须回滚：行保留可审计，但已终态。
	records, err := store.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{
		RootSessionID: rootSession.ID,
		IncludeClosed: true,
	})
	require.NoError(t, err)
	var rollbackRecord *agentcontrol.AgentRecord
	for index := range records {
		if records[index].SessionID == childID {
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
