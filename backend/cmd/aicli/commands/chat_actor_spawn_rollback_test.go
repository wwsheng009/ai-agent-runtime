package commands

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	runtimellm "github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// spawnSnapshotFailureStore 把「快照读回失败」这一个失败点从本地宿主的其它读路径里隔离
// 出来：`Spawn` 全程会多次读取会话（父会话探测、子会话可寻址性检查、物化投影），如果在存储
// 层无条件失败，错误会落在预约之前的步骤上，用例就测不到 P0-3 的补偿分支。
//
// 触发条件是两个与「已经走到快照阶段」等价的运行期事实：子会话 actor 已经建出来（`Spawn`
// 在预约之后才 `GetOrCreate`），且注入开关仍打开。满足时 `Load` 返回注入错误，不满足时原样
// 委托给真实存储。用例断言前调用 `disarm()`，避免注入器干扰断言自己的读取。
type spawnSnapshotFailureStore struct {
	runtimechat.SessionStorage
	childID  string
	armed    atomic.Bool
	actorUp  func() bool
	injected error
}

func (s *spawnSnapshotFailureStore) Load(ctx context.Context, sessionID string) (*runtimechat.Session, error) {
	if s != nil && s.childID != "" && s.armed.Load() && strings.TrimSpace(sessionID) == s.childID {
		if s.actorUp == nil || s.actorUp() {
			return nil, s.injected
		}
	}
	return s.SessionStorage.Load(ctx, sessionID)
}

func (s *spawnSnapshotFailureStore) disarm() {
	if s != nil {
		s.armed.Store(false)
	}
}

// newLocalSpawnRollbackTestHost 组装 P0-3 的本地宿主回滚用例所需的宿主：会话管理器 +
// 真实 durable registry（SQLite）+ 可注入的会话存储。team store 保持健康——失败点由
// spawnSnapshotFailureStore 精确落在快照读回上，与 API 宿主用例
// （session_agent_spawn_snapshot_rollback_test.go）覆盖同一分支。
func newLocalSpawnRollbackTestHost(t *testing.T, ctx context.Context, childID string) (*localChatRuntimeHost, *localActorRegistry, string, *spawnSnapshotFailureStore) {
	t.Helper()

	manager, userID, _, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	t.Cleanup(manager.Stop)

	rootSession, err := manager.Create(ctx, userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}

	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = teamStore.Close() })

	llmRuntime := runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{})
	host := newLocalOrchestrationTestHost(t, manager, userID, llmRuntime, teamStore)
	host.RuntimeConfig = runtimecfg.DefaultRuntimeConfig()
	host.RuntimeConfig.Agents.MaxThreads = 1
	host.BaseSession = &ChatSession{
		RuntimeSession:   rootSession,
		SessionUserID:    userID,
		LocalRuntimeHost: host,
	}
	t.Cleanup(host.SessionHub.StopAll)

	store, err := agentcontrol.NewSQLiteGlobalAgentRegistryStore(&agentcontrol.GlobalAgentStoreConfig{
		Path: filepath.Join(t.TempDir(), "agents.db"),
	})
	if err != nil {
		t.Fatalf("NewSQLiteGlobalAgentRegistryStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	host.AgentRegistryStore = store

	injected := &spawnSnapshotFailureStore{
		SessionStorage: host.SessionStore,
		childID:        childID,
		actorUp: func() bool {
			_, ok := host.SessionHub.Get(childID)
			return ok
		},
		injected: fmt.Errorf("injected snapshot read failure"),
	}
	injected.armed.Store(true)
	t.Cleanup(injected.disarm)
	host.SessionStore = injected

	return host, host.ActorRegistry, rootSession.ID, injected
}

// assertLocalSpawnReservationRolledBack 断言一次失败 spawn 的 durable 预约已收敛：
//  1. 半成品子会话已删除；
//  2. registry 行保留可审计，但状态为 stale 终态（Closed）；
//  3. active 集合只剩 root 行，失败预约不占 agents.maxThreads 额度。
func assertLocalSpawnReservationRolledBack(t *testing.T, ctx context.Context, host *localChatRuntimeHost, rootSessionID, childID string) {
	t.Helper()

	if _, err := host.SessionStore.Load(ctx, childID); err == nil {
		t.Fatalf("rolled back spawn must delete the half-built child session")
	}

	records, err := host.AgentRegistryStore.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{
		RootSessionID: rootSessionID,
		IncludeClosed: true,
	})
	if err != nil {
		t.Fatalf("ListAgentControlAgents(IncludeClosed): %v", err)
	}
	var rollbackRecord *agentcontrol.AgentRecord
	for index := range records {
		if records[index].SessionID == childID {
			rollbackRecord = &records[index]
		}
	}
	if rollbackRecord == nil {
		t.Fatalf("rolled back spawn must keep an auditable registry row")
	}
	if rollbackRecord.Status != agentcontrol.AgentStatusStale || !rollbackRecord.Closed() {
		t.Fatalf("rolled back reservation must be terminal, got status=%q closed=%v",
			rollbackRecord.Status, rollbackRecord.Closed())
	}

	active, err := host.AgentRegistryStore.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{RootSessionID: rootSessionID})
	if err != nil {
		t.Fatalf("ListAgentControlAgents(active): %v", err)
	}
	if len(active) != 1 || active[0].SessionID != rootSessionID {
		t.Fatalf("only the root row may stay active after a rolled back spawn, got %+v", active)
	}
}

// TestLocalActorRegistrySpawnRollsBackReservationWhenSnapshotFails 覆盖 P0-3 在本地宿主上
// 最后一条未补偿路径：durable 预约已成功、且**没有**排队任何 prompt 时，状态快照读回失败。
// 此时没有任何 run 会把子会话推向终态，残留的 active 行会永久占用 agents.maxThreads
// （与 N1 同类僵尸行），本地宿主必须与 API 宿主同口径地走 rollbackSpawnFailure。
func TestLocalActorRegistrySpawnRollsBackReservationWhenSnapshotFails(t *testing.T) {
	ctx := context.Background()
	const childID = "local-snapshot-fail-child"

	host, registry, rootSessionID, injector := newLocalSpawnRollbackTestHost(t, ctx, childID)

	// 不带 message：没有 prompt 被排队，因此没有 run 会收敛这笔预约。
	_, err := registry.Spawn(ctx, rootSessionID, toolbroker.SpawnAgentArgs{ID: childID})
	injector.disarm()
	if err == nil {
		t.Fatalf("expected snapshot failure to fail the spawn")
	}
	if !strings.Contains(err.Error(), "snapshot child session") {
		t.Fatalf("expected wrapped snapshot failure, got %v", err)
	}

	assertLocalSpawnReservationRolledBack(t, ctx, host, rootSessionID, childID)
}

// TestLocalActorRegistrySpawnKeepsReservationWhenSnapshotFailsAfterQueueing 覆盖同一分支的
// 另一半口径：prompt 已排队时快照读回失败**不**回滚——此时删除子会话等于杀掉一个活着的
// agent，只上报诊断原因，让完成回调收敛预约。
func TestLocalActorRegistrySpawnKeepsReservationWhenSnapshotFailsAfterQueueing(t *testing.T) {
	ctx := context.Background()
	const childID = "local-snapshot-queued-child"

	host, registry, rootSessionID, injector := newLocalSpawnRollbackTestHost(t, ctx, childID)

	_, err := registry.Spawn(ctx, rootSessionID, toolbroker.SpawnAgentArgs{
		ID:      childID,
		Message: "review the diff",
	})
	injector.disarm()
	if err == nil {
		t.Fatalf("expected snapshot failure to fail the spawn")
	}
	// 未被包装成回滚错误 = 没有走 rollbackSpawnFailure。
	if strings.Contains(err.Error(), "snapshot child session") {
		t.Fatalf("queued spawn must not be rolled back, got %v", err)
	}

	// 子会话必须仍然存在：排队中的 agent 不能被当作半成品删除。
	if _, err := host.SessionStore.Load(ctx, childID); err != nil {
		t.Fatalf("queued child session must survive a snapshot read failure, got %v", err)
	}
}

// TestLocalActorRegistrySpawnAtThreadLimitLeavesNoReservationRow 覆盖 P0-3 的配额侧不变量：
// 名额已满时被闸门拒绝的 spawn 不能落任何 registry 行（既不是 active，也不是可审计的
// "半笔"预约——它从未生效），已占额度的子会话也不受影响。全并发版本见 store 级的
// `agentcontrol/concurrency_stress_test.go`；本用例锁的是宿主侧单笔口径。
func TestLocalActorRegistrySpawnAtThreadLimitLeavesNoReservationRow(t *testing.T) {
	ctx := context.Background()
	const firstChildID = "local-quota-child"
	const rejectedChildID = "local-quota-rejected-child"

	host, registry, rootSessionID, injector := newLocalSpawnRollbackTestHost(t, ctx, "")
	// 本用例不注入失败：被拒的原因是名额，而不是快照读回。
	injector.disarm()

	if _, err := registry.Spawn(ctx, rootSessionID, toolbroker.SpawnAgentArgs{ID: firstChildID}); err != nil {
		t.Fatalf("first spawn must succeed: %v", err)
	}

	if _, err := registry.Spawn(ctx, rootSessionID, toolbroker.SpawnAgentArgs{ID: rejectedChildID}); err == nil {
		t.Fatalf("second spawn must be rejected by agents.maxThreads=1")
	}

	// 被拒的 spawn 连半成品会话都不该留下。
	if _, err := host.SessionStore.Load(ctx, rejectedChildID); err == nil {
		t.Fatalf("rejected spawn must not leave a half-built child session")
	}

	records, err := host.AgentRegistryStore.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{
		RootSessionID: rootSessionID,
		IncludeClosed: true,
	})
	if err != nil {
		t.Fatalf("ListAgentControlAgents(IncludeClosed): %v", err)
	}
	for index := range records {
		if records[index].SessionID == rejectedChildID {
			t.Fatalf("a spawn rejected before the reservation must not leave any registry row, got %+v", records[index])
		}
	}

	// 已占额度的子会话保持 active，配额没有被这次拒绝影响。
	active, err := host.AgentRegistryStore.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{RootSessionID: rootSessionID})
	if err != nil {
		t.Fatalf("ListAgentControlAgents(active): %v", err)
	}
	if len(active) != 2 {
		t.Fatalf("expected root + the admitted child to stay active, got %+v", active)
	}
	seen := map[string]bool{}
	for _, record := range active {
		seen[record.SessionID] = true
	}
	if !seen[rootSessionID] || !seen[firstChildID] {
		t.Fatalf("active set must be root + %s, got %+v", firstChildID, active)
	}
}
