package commands

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimeserver "github.com/wwsheng009/ai-agent-runtime/internal/runtimeserver"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// 2026-09-30 现场（W6=session_20260930105807_4OT9C2lq）回归：
// 宿主重启后，spawn 期登记的完成订阅随旧进程消失；被 resume 续跑的子代理结束时
// 没有任何监听者，mailbox / supervision 通知 / wake 全部不产生，主 agent 永久
// idle。本文件的用例钉住两条收敛：启动重放补投影（P0-B）与订阅重建（P0-A）。

const (
	recoveryTestRoot  = "session-root-recovery"
	recoveryTestChild = "session-child-recovery"
)

func newLocalChildCompletionRecoveryFixture(t *testing.T) (*localChatRuntimeHost, *runtimechat.InMemoryRuntimeStore, *supervision.SQLiteSupervisionStore) {
	t.Helper()
	store, err := supervision.NewSQLiteSupervisionStore(&supervision.StoreConfig{
		DSN: "file:" + t.Name() + "?mode=memory&cache=shared",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	scheduler := supervision.NewWakeScheduler(store, supervision.WakeSchedulerConfig{})

	runtimeStore := runtimechat.NewInMemoryRuntimeStore(64)
	registryStore, err := agentcontrol.NewSQLiteGlobalAgentRegistryStore(&agentcontrol.GlobalAgentStoreConfig{
		Path: filepath.Join(t.TempDir(), "agents.sqlite"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = registryStore.Close() })

	host := &localChatRuntimeHost{
		EventBus:           runtimeevents.NewBusWithRetention(32),
		EventStore:         runtimeStore,
		RuntimeStore:       runtimeStore,
		SessionStore:       runtimechat.NewInMemoryStorage(),
		AgentRegistryStore: registryStore,
		BaseSession: &ChatSession{
			RuntimeSession: &runtimechat.Session{ID: recoveryTestRoot},
		},
		supervisionWake: &supervision.WakeConsumer{
			Wakes: scheduler,
			Runnable: func(ctx context.Context, rootScopeID, parentSessionID, parentTeamID string) bool {
				return true
			},
			Deliver: func(ctx context.Context, parentSessionID, rootScopeID string, digest *supervision.Digest, wakeIDs []string) error {
				return nil
			},
		},
	}
	host.ActorRegistry = newLocalActorRegistry(host)
	host.Supervision = &runtimeserver.SupervisionControlPlane{Store: store, Wakes: scheduler}
	t.Cleanup(host.Close)
	return host, runtimeStore, store
}

// seedRecoveredChildTerminal 造出"重启前子代理已结束、但完成投影丢失"的现场：
// 子会话 + 终态 runtime state + session_end 事件 + registry 子行（行状态按真机
// 取 closed —— 重启 sweep 会把仍在跑的 child 行标死，行状态不能当存活判据）。
func seedRecoveredChildTerminal(t *testing.T, host *localChatRuntimeHost, runtimeStore *runtimechat.InMemoryRuntimeStore, terminalAt time.Time, traceID string) {
	t.Helper()
	ctx := context.Background()
	child := &runtimechat.Session{ID: recoveryTestChild, UserID: "agent", State: runtimechat.StateActive}
	child.SetContext(toolbroker.AgentSessionContextParentSessionID, recoveryTestRoot)
	child.SetContext(toolbroker.AgentSessionContextRootSessionID, recoveryTestRoot)
	child.SetContext(toolbroker.AgentSessionContextPath, "/root/"+recoveryTestChild)
	child.SetContext(toolbroker.AgentSessionContextDepth, 1)
	child.SetContext(toolbroker.AgentSessionContextAgentType, "general")
	require.NoError(t, host.SessionStore.Save(ctx, child))
	require.NoError(t, runtimeStore.SaveState(ctx, &runtimechat.RuntimeState{
		SessionID: recoveryTestChild,
		Status:    runtimechat.SessionIdle,
		UpdatedAt: terminalAt,
	}))
	_, err := runtimeStore.AppendEvent(ctx, runtimeevents.Event{
		Type:      runtimechat.EventSessionEnd,
		SessionID: recoveryTestChild,
		TraceID:   traceID,
		Timestamp: terminalAt,
		Payload:   map[string]interface{}{"status": "idle", "success": true, "duration": 4321},
	})
	require.NoError(t, err)
	_, err = host.AgentRegistryStore.UpsertAgentControlAgent(ctx, agentcontrol.AgentRecord{
		AgentID:         recoveryTestChild,
		RootSessionID:   recoveryTestRoot,
		ParentAgentID:   "root:" + recoveryTestRoot,
		ParentSessionID: recoveryTestRoot,
		SessionID:       recoveryTestChild,
		AgentPath:       "/root/" + recoveryTestChild,
		Depth:           1,
		Status:          agentcontrol.AgentStatusClosed,
	})
	require.NoError(t, err)
}

func localChildCompletionMirrorCount(t *testing.T, runtimeStore *runtimechat.InMemoryRuntimeStore, parentSessionID string) int {
	t.Helper()
	events, err := runtimeStore.ListEvents(context.Background(), parentSessionID, 0, 100)
	require.NoError(t, err)
	count := 0
	for _, event := range events {
		if event.Type == localSubagentCompletionMirrorEventType {
			count++
			require.Equal(t, recoveryTestChild, event.Payload["agent_id"])
		}
	}
	return count
}

// TestReplayLocalChildCompletionsProjectsMissedTerminal 钉住 P0-B：重启后补投影
// 必须产生父侧完成镜像 + supervision 通知，且重复重放幂等（不重复追加镜像）。
func TestReplayLocalChildCompletionsProjectsMissedTerminal(t *testing.T) {
	host, runtimeStore, store := newLocalChildCompletionRecoveryFixture(t)
	ctx := context.Background()
	terminalAt := time.Now().UTC().Add(-5 * time.Minute)
	seedRecoveredChildTerminal(t, host, runtimeStore, terminalAt, "trace-child-recovery")

	require.Equal(t, 1, host.replayLocalChildCompletions(ctx, false), "丢失的终态必须补投影一次")
	require.Equal(t, 1, localChildCompletionMirrorCount(t, runtimeStore, recoveryTestRoot))

	notifications, err := store.ListNotifications(ctx, supervision.NotificationFilter{
		RootScopeID:     recoveryTestRoot,
		SubjectKind:     supervision.SubjectAgentSession,
		SubjectID:       recoveryTestChild,
		IncludeResolved: true,
	})
	require.NoError(t, err)
	require.NotEmpty(t, notifications, "完成投影必须落下 supervision 通知")

	// 幂等：第二次重放不得重复投影（父侧镜像事件是"已投影"判据）。
	require.Equal(t, 0, host.replayLocalChildCompletions(ctx, false))
	require.Equal(t, 1, localChildCompletionMirrorCount(t, runtimeStore, recoveryTestRoot))
}

// TestReplayLocalChildCompletionsSkipsChildThatMovedOn 钉住重放的保守边界：
// 终态之后子会话又有新动作（已被 resume 跑起新一轮）时不得补一条"已完成"。
func TestReplayLocalChildCompletionsSkipsChildThatMovedOn(t *testing.T) {
	host, runtimeStore, _ := newLocalChildCompletionRecoveryFixture(t)
	ctx := context.Background()
	seedRecoveredChildTerminal(t, host, runtimeStore, time.Now().UTC().Add(-10*time.Minute), "trace-child-old")
	require.NoError(t, runtimeStore.SaveState(ctx, &runtimechat.RuntimeState{
		SessionID: recoveryTestChild,
		Status:    runtimechat.SessionRunning,
		UpdatedAt: time.Now().UTC(),
	}))

	require.Equal(t, 0, host.replayLocalChildCompletions(ctx, false))
	require.Equal(t, 0, localChildCompletionMirrorCount(t, runtimeStore, recoveryTestRoot))
}

// TestRebindLocalChildCompletionSubscriptionsCatchesNextTerminal 钉住 P0-A：
// 重建订阅后，子会话的下一次终态事件必须重新走完整投影（重启后不再丢）。
func TestRebindLocalChildCompletionSubscriptionsCatchesNextTerminal(t *testing.T) {
	host, runtimeStore, store := newLocalChildCompletionRecoveryFixture(t)
	ctx := context.Background()
	seedRecoveredChildTerminal(t, host, runtimeStore, time.Now().UTC().Add(-10*time.Minute), "trace-child-old")

	require.Equal(t, 1, host.rebindLocalChildCompletionSubscriptions(ctx))

	host.EventBus.Publish(runtimeevents.Event{
		Type:      runtimechat.EventSessionEnd,
		SessionID: recoveryTestChild,
		TraceID:   "trace-child-new",
		Timestamp: time.Now().UTC(),
		Payload:   map[string]interface{}{"status": "idle", "success": true},
	})

	require.Equal(t, 1, localChildCompletionMirrorCount(t, runtimeStore, recoveryTestRoot))
	notifications, err := store.ListNotifications(ctx, supervision.NotificationFilter{
		RootScopeID:     recoveryTestRoot,
		SubjectKind:     supervision.SubjectAgentSession,
		SubjectID:       recoveryTestChild,
		IncludeResolved: true,
	})
	require.NoError(t, err)
	require.NotEmpty(t, notifications)
}
