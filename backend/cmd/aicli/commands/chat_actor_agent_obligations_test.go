package commands

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimellm "github.com/wwsheng009/ai-agent-runtime/internal/llm"
	runtimeserver "github.com/wwsheng009/ai-agent-runtime/internal/runtimeserver"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// newLocalAgentObligationTestHost 组装轻量子代理挂起链路的最小宿主：durable
// batch store（挂起记录）+ durable supervision store（子会话终态判读）+ 真实
// agent registry（在途子会话枚举）+ 事件总线（边沿播报断言）。
func newLocalAgentObligationTestHost(t *testing.T, name string) *localChatRuntimeHost {
	t.Helper()
	host := newLocalSupervisionTestHost(t)

	batchStore, err := subagentbatch.NewSQLiteBatchStore(&subagentbatch.StoreConfig{
		Path: filepath.Join(t.TempDir(), "batches.db"),
	})
	require.NoError(t, err)
	require.True(t, batchStore.IsDurable(), "the park path requires a durable batch store")
	t.Cleanup(func() { _ = batchStore.Close() })

	agentStore, err := agentcontrol.NewSQLiteGlobalAgentRegistryStore(&agentcontrol.GlobalAgentStoreConfig{
		Path: filepath.Join(t.TempDir(), "agents.sqlite"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = agentStore.Close() })

	host.SubagentBatches = batchStore
	host.AgentRegistryStore = agentStore
	host.EventBus = runtimeevents.NewBusWithRetention(32)
	host.RuntimeStore = runtimechat.NewInMemoryRuntimeStore(16)
	host.BaseSession = &ChatSession{RuntimeSession: &runtimechat.Session{ID: "parent-" + name}}
	return host
}

func subscribeRuntimeEvents(t *testing.T, bus *runtimeevents.Bus, eventType string) chan runtimeevents.Event {
	t.Helper()
	events := make(chan runtimeevents.Event, 8)
	unsubscribe := bus.SubscribeCancelable(eventType, func(event runtimeevents.Event) {
		select {
		case events <- event:
		default:
		}
	})
	t.Cleanup(unsubscribe)
	return events
}

// TestLocalAgentSessionObligationResolverReadsTerminalProjection pins the
// durable judge: the same supervision lifecycle row that drives the auto-wake
// (agent_completed / agent_failed, SupervisionState=terminated) is the only
// accepted evidence that a child session is done; an unknown id stays found=false.
func TestLocalAgentSessionObligationResolverReadsTerminalProjection(t *testing.T) {
	host := newLocalAgentObligationTestHost(t, "resolver")
	ctx := context.Background()
	resolver := host.agentSessionObligationResolver()
	require.NotNil(t, resolver)

	terminal, found, err := resolver.AgentSessionTerminal(ctx, "child-unknown")
	require.NoError(t, err)
	require.False(t, terminal)
	require.False(t, found, "a child without a lifecycle projection is never terminal")

	parent := host.BaseSession.RuntimeSession.ID
	_, err = supervision.ProjectAgentCompletion(ctx, host.Supervision.Store, nil, parent, parent, "child-done", "idle", "session_end")
	require.NoError(t, err)
	terminal, found, err = resolver.AgentSessionTerminal(ctx, "child-done")
	require.NoError(t, err)
	require.True(t, found)
	require.True(t, terminal)

	_, err = supervision.ProjectAgentCompletion(ctx, host.Supervision.Store, nil, parent, parent, "child-failed", "failed", "session_end")
	require.NoError(t, err)
	terminal, found, err = resolver.AgentSessionTerminal(ctx, "child-failed")
	require.NoError(t, err)
	require.True(t, found)
	require.True(t, terminal)
}

// TestLocalSpawnParkWritesAgentSessionObligationAndAnnouncesOnce pins the
// dispatch-time park: each queued spawn_agent child becomes one
// agent_session: obligation on the current turn, the first park announces one
// turn.suspended edge, and repeated parks never duplicate the record or the edge.
func TestLocalSpawnParkWritesAgentSessionObligationAndAnnouncesOnce(t *testing.T) {
	host := newLocalAgentObligationTestHost(t, "park")
	ctx := agent.WithTurnID(context.Background(), "turn-park")
	parent := host.BaseSession.RuntimeSession.ID
	events := subscribeRuntimeEvents(t, host.EventBus, runtimeevents.EventTurnSuspended)

	host.parkLocalAgentChildObligation(ctx, parent, "child-a")
	host.parkLocalAgentChildObligation(ctx, parent, "child-b")
	host.parkLocalAgentChildObligation(ctx, parent, "child-a")

	record, ok, err := host.SubagentBatches.GetTurnSuspension(context.Background(), parent, "turn-park")
	require.NoError(t, err)
	require.True(t, ok)
	require.ElementsMatch(t,
		[]string{
			subagentbatch.AgentSessionObligationID("child-a"),
			subagentbatch.AgentSessionObligationID("child-b"),
		},
		record.ObligationIDs)
	require.ElementsMatch(t, []string{"child-a", "child-b"}, record.ObligationAgentSessionIDs())

	select {
	case event := <-events:
		require.Equal(t, runtimeevents.EventTurnSuspended, event.Type)
		require.Equal(t, parent, event.SessionID)
		require.Equal(t, "turn-park", event.Payload["turn_id"])
		require.Equal(t, 1, event.Payload["obligation_count"])
		select {
		case extra := <-events:
			t.Fatalf("second park must not re-announce, got %+v", extra)
		default:
		}
	default:
		t.Fatal("expected one turn.suspended edge for the first host-side park")
	}
}

// TestLocalParkedChildObligationSettlesAfterTerminalProjection pins the other
// half: a turn parked on child sessions may only be cleared once the supervision
// plane reports the child terminal; an unfinished child keeps it parked.
func TestLocalParkedChildObligationSettlesAfterTerminalProjection(t *testing.T) {
	host := newLocalAgentObligationTestHost(t, "settle")
	ctx := agent.WithTurnID(context.Background(), "turn-settle")
	parent := host.BaseSession.RuntimeSession.ID
	host.parkLocalAgentChildObligation(ctx, parent, "child-a")

	record, ok, err := host.SubagentBatches.GetTurnSuspension(context.Background(), parent, "turn-settle")
	require.NoError(t, err)
	require.True(t, ok)
	resolver := host.agentSessionObligationResolver()

	settled, err := subagentbatch.TurnObligationsSettledWith(ctx, host.SubagentBatches, record, resolver)
	require.NoError(t, err)
	require.False(t, settled, "a running child keeps the turn parked")

	_, err = supervision.ProjectAgentCompletion(ctx, host.Supervision.Store, nil, parent, parent, "child-a", "idle", "session_end")
	require.NoError(t, err)
	settled, err = subagentbatch.TurnObligationsSettledWith(ctx, host.SubagentBatches, record, resolver)
	require.NoError(t, err)
	require.True(t, settled, "a terminal child settles the parked turn")
}

// TestLocalInFlightSignalFiresWithoutParkedRecord pins the degraded/observable
// fallback: a turn that ended while a live child remains must project the I9
// signal unless a parked record already expresses the waiting state.
func TestLocalInFlightSignalFiresWithoutParkedRecord(t *testing.T) {
	host := newLocalAgentObligationTestHost(t, "inflight")
	ctx := context.Background()
	parent := host.BaseSession.RuntimeSession.ID

	for _, child := range []string{"child-live", "child-done"} {
		_, err := host.AgentRegistryStore.UpsertAgentControlAgent(ctx, agentcontrol.AgentRecord{
			AgentID:         child,
			RootSessionID:   parent,
			ParentSessionID: parent,
			SessionID:       child,
			AgentPath:       "/root/" + child,
			Depth:           1,
			AgentType:       agentcontrol.AgentTypeChild,
			Status:          agentcontrol.AgentStatusActive,
		})
		require.NoError(t, err)
	}
	_, err := supervision.ProjectAgentCompletion(ctx, host.Supervision.Store, nil, parent, parent, "child-done", "idle", "session_end")
	require.NoError(t, err)

	events := subscribeRuntimeEvents(t, host.EventBus, runtimeevents.EventSubagentSuspensionUnavailable)
	host.signalLocalInFlightChildSessions(ctx, parent, "turn-unparked")

	select {
	case event := <-events:
		require.Equal(t, parent, event.SessionID)
		require.Equal(t, "turn-unparked", event.Payload["turn_id"])
		require.Equal(t, 1, event.Payload["child_count"])
		require.Equal(t, []string{"child-live"}, event.Payload["child_session_ids"])
	default:
		t.Fatal("expected an in-flight degradation signal while a live child remains")
	}
	select {
	case extra := <-events:
		t.Fatalf("the signal is an edge: got %+v", extra)
	default:
	}

	// A parked record already expresses the waiting state: no degraded signal.
	host.parkLocalAgentChildObligation(agent.WithTurnID(context.Background(), "turn-parked"), parent, "child-live")
	host.signalLocalInFlightChildSessions(ctx, parent, "turn-parked")
	select {
	case extra := <-events:
		t.Fatalf("a parked turn must not emit the degraded signal, got %+v", extra)
	default:
	}
}

// TestLocalInFlightSignalCoversAgentDefinitionChildren pins the filter fix for
// the I9 fallback: spawn_agent children with an explicit agent_type store the
// definition name (general/explore/plan…) in AgentRecord.AgentType, so the
// degraded in-flight signal must key on the tree shape instead of the literal
// "child" type — otherwise the safety net stays silent exactly for the common
// case (真机 2026-09-30：全日志 0 条 subagent.suspension_unavailable)。
func TestLocalInFlightSignalCoversAgentDefinitionChildren(t *testing.T) {
	host := newLocalAgentObligationTestHost(t, "inflight-general")
	ctx := context.Background()
	parent := host.BaseSession.RuntimeSession.ID

	for _, record := range []agentcontrol.AgentRecord{
		{
			AgentID:         "child-general",
			RootSessionID:   parent,
			ParentSessionID: parent,
			SessionID:       "child-general",
			AgentPath:       "/root/child-general",
			Depth:           1,
			AgentType:       "general",
			Status:          agentcontrol.AgentStatusActive,
		},
		{
			AgentID:         "teammate",
			RootSessionID:   parent,
			ParentSessionID: parent,
			SessionID:       "teammate",
			AgentPath:       "/root/teammate",
			Depth:           1,
			AgentType:       agentcontrol.AgentTypeTeamTeammate,
			Status:          agentcontrol.AgentStatusActive,
		},
	} {
		_, err := host.AgentRegistryStore.UpsertAgentControlAgent(ctx, record)
		require.NoError(t, err)
	}

	events := subscribeRuntimeEvents(t, host.EventBus, runtimeevents.EventSubagentSuspensionUnavailable)
	host.signalLocalInFlightChildSessions(ctx, parent, "turn-general")

	select {
	case event := <-events:
		require.Equal(t, []string{"child-general"}, event.Payload["child_session_ids"],
			"agent-typed children must be covered; team teammates stay out")
	default:
		t.Fatal("expected the in-flight signal to cover agent-typed children")
	}
}

// TestLocalSpawnParksQueuedChildObligation is the call-site guard: a real
// localActorRegistry.Spawn with a queued prompt must park the parent turn on the
// child session (turn id resolved from the running RuntimeState, because the
// broker call context carries no turn annotation in this fixture).
func TestLocalSpawnParksQueuedChildObligation(t *testing.T) {
	ctx := context.Background()
	manager, userID, _, err := newChatSessionManager(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(manager.Stop)

	rootSession, err := manager.Create(ctx, userID)
	require.NoError(t, err)

	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = teamStore.Close() })

	provider := runtimellm.NewMockProvider("mock", 0)
	provider.SetResponse("hello child", "child finished")
	llmRuntime := runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{
		DefaultProvider: "test-provider",
		DefaultModel:    "test-model",
	})
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))
	require.NoError(t, llmRuntime.RegisterProviderAlias("test-model", "test-provider"))

	host := newLocalOrchestrationTestHost(t, manager, userID, llmRuntime, teamStore)
	host.RuntimeConfig = runtimecfg.DefaultRuntimeConfig()
	host.BaseSession = &ChatSession{RuntimeSession: rootSession, SessionUserID: userID}

	plane, err := runtimeserver.BuildSupervisionControlPlane(t.TempDir(), supervision.Config{}, runtimeserver.SupervisionRuntimeHooks{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = plane.Close() })
	host.Supervision = plane

	batchStore, err := subagentbatch.NewSQLiteBatchStore(&subagentbatch.StoreConfig{Path: filepath.Join(t.TempDir(), "batches.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = batchStore.Close() })
	host.SubagentBatches = batchStore

	require.NoError(t, host.RuntimeStore.SaveState(ctx, &runtimechat.RuntimeState{
		SessionID:     rootSession.ID,
		Status:        runtimechat.SessionRunning,
		CurrentTurnID: "turn-spawn",
	}))

	result, err := host.ActorRegistry.Spawn(ctx, rootSession.ID, toolbroker.SpawnAgentArgs{
		Message:   "hello child",
		AgentType: "general",
	})
	require.NoError(t, err)
	require.True(t, result.Queued)
	require.Contains(t, result.NextAction, "obligation_registered",
		"a durably parked child must tell the model the wait/finalize semantics")

	record, ok, err := host.SubagentBatches.GetTurnSuspension(ctx, rootSession.ID, "turn-spawn")
	require.NoError(t, err)
	require.True(t, ok, "a queued spawn_agent child must park the parent turn")
	require.Equal(t, []string{result.SessionID}, record.ObligationAgentSessionIDs())
}

// TestLocalFollowupTaskParksTriggeredChildObligation is the call-site guard for
// the followup half of §6.12: resume_agent + followup_task on an existing child
// starts a new run without any spawn, so the delivery itself must park the
// caller turn (真机 2026-09-30：该路径无 turn.suspended，回合结束冻结
// "Worked for …" 而子代理仍在跑). The turn id is resolved from the durable
// RuntimeState because this fixture context carries no turn annotation.
func TestLocalFollowupTaskParksTriggeredChildObligation(t *testing.T) {
	ctx := context.Background()
	manager, userID, _, err := newChatSessionManager(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(manager.Stop)

	rootSession, err := manager.Create(ctx, userID)
	require.NoError(t, err)

	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = teamStore.Close() })

	provider := runtimellm.NewMockProvider("mock", 0)
	provider.SetResponse("continue the work", "child finished")
	llmRuntime := runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{
		DefaultProvider: "test-provider",
		DefaultModel:    "test-model",
	})
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))
	require.NoError(t, llmRuntime.RegisterProviderAlias("test-model", "test-provider"))

	host := newLocalOrchestrationTestHost(t, manager, userID, llmRuntime, teamStore)
	host.RuntimeConfig = runtimecfg.DefaultRuntimeConfig()
	host.BaseSession = &ChatSession{RuntimeSession: rootSession, SessionUserID: userID}

	plane, err := runtimeserver.BuildSupervisionControlPlane(t.TempDir(), supervision.Config{}, runtimeserver.SupervisionRuntimeHooks{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = plane.Close() })
	host.Supervision = plane

	batchStore, err := subagentbatch.NewSQLiteBatchStore(&subagentbatch.StoreConfig{Path: filepath.Join(t.TempDir(), "batches.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = batchStore.Close() })
	host.SubagentBatches = batchStore

	agentStore, err := agentcontrol.NewSQLiteGlobalAgentRegistryStore(&agentcontrol.GlobalAgentStoreConfig{Path: filepath.Join(t.TempDir(), "agents.sqlite")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = agentStore.Close() })
	host.AgentRegistryStore = agentStore

	require.NoError(t, host.RuntimeStore.SaveState(ctx, &runtimechat.RuntimeState{
		SessionID:     rootSession.ID,
		Status:        runtimechat.SessionRunning,
		CurrentTurnID: "turn-followup",
	}))

	spawn, err := host.ActorRegistry.Spawn(ctx, rootSession.ID, toolbroker.SpawnAgentArgs{
		ID:        "followup-child",
		AgentType: "general",
	})
	require.NoError(t, err)
	require.False(t, spawn.Queued, "a spawn without a message must not start a run")

	_, parked, err := host.SubagentBatches.GetTurnSuspension(ctx, rootSession.ID, "turn-followup")
	require.NoError(t, err)
	require.False(t, parked, "a message-less spawn must not park the turn")

	result, err := host.ActorRegistry.FollowupTask(ctx, rootSession.ID, toolbroker.AgentMessageArgs{
		Target:  spawn.SessionID,
		Message: "continue the work",
	})
	require.NoError(t, err)
	require.True(t, result.Triggered, "an idle child must be triggered into a new run")

	record, ok, err := host.SubagentBatches.GetTurnSuspension(ctx, rootSession.ID, "turn-followup")
	require.NoError(t, err)
	require.True(t, ok, "a triggered followup to a live child must park the caller turn")
	require.Equal(t, []string{spawn.SessionID}, record.ObligationAgentSessionIDs())
}

// attachSettledTurnWakeFixture wires the durable batch store plus the
// supervision control-plane view the completion bridge reads
// (Supervision.Store / Supervision.Wakes). The wake-consumer fixture keeps its
// store and scheduler local, so the tests that exercise the full bridge have to
// expose them on the host.
func attachSettledTurnWakeFixture(t *testing.T, host *localChatRuntimeHost, store *supervision.SQLiteSupervisionStore, name string) {
	t.Helper()
	batchStore, err := subagentbatch.NewSQLiteBatchStore(&subagentbatch.StoreConfig{
		Path: filepath.Join(t.TempDir(), name+".db"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = batchStore.Close() })
	host.SubagentBatches = batchStore
	host.Supervision = &runtimeserver.SupervisionControlPlane{
		Store: store,
		Wakes: host.supervisionWake.Wakes,
	}
}

// TestProjectLocalAgentCompletionWakesSettledParkedTurn 钉住"全部成功"缺边：
// 成功（info/closed）终态不排生命周期 wake（projection 只在 critical +
// action_required 时排），所以完成桥必须在 durable 账本判定"该挂起 turn 已结清"
// 时自己排一笔结算 wake —— 且只在最后一笔义务终态时排；排出的 wake 经
// wakeSupervisedParent 立即投递，父会话恢复。
func TestProjectLocalAgentCompletionWakesSettledParkedTurn(t *testing.T) {
	host, store, deliveries := newWakeConsumerTestHost(t, "aicli-settled-turn-wake")
	ctx := context.Background()

	attachSettledTurnWakeFixture(t, host, store, "settled-turn-batches")

	require.NoError(t, host.RuntimeStore.SaveState(ctx, &runtimechat.RuntimeState{
		SessionID: "root-session",
		Status:    runtimechat.SessionIdle,
		UpdatedAt: time.Now().UTC(),
	}))

	parkCtx := agent.WithTurnID(ctx, "turn-settled")
	require.True(t, host.parkLocalAgentChildObligation(parkCtx, "root-session", "child-a"))
	require.True(t, host.parkLocalAgentChildObligation(parkCtx, "root-session", "child-b"))

	registry := &localActorRegistry{Host: host}
	registry.projectLocalAgentCompletion(ctx, "root-session", "child-a", "idle", "session_end")

	pending, err := store.ListWakePending(ctx, supervision.WakeFilter{RootScopeID: "root-session", UnclaimedOnly: true})
	require.NoError(t, err)
	require.Empty(t, pending, "一笔终态不能让仍欠 child-b 的 turn 提前醒来")
	require.Equal(t, 0, deliveries.count())

	// 父会话仍在运行：结算 wake 必须保持 durable、可观察，而不是同一次调用里
	// 就被排空。
	require.NoError(t, host.RuntimeStore.SaveState(ctx, &runtimechat.RuntimeState{
		SessionID: "root-session",
		Status:    runtimechat.SessionRunning,
		UpdatedAt: time.Now().UTC(),
	}))
	registry.projectLocalAgentCompletion(ctx, "root-session", "child-b", "idle", "session_end")

	pending, err = store.ListWakePending(ctx, supervision.WakeFilter{RootScopeID: "root-session", UnclaimedOnly: true})
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, supervision.WakeReasonObligationSettled, pending[0].WakeReason)
	require.Equal(t, "turn-settled", pending[0].TurnID)
	require.Equal(t, 0, deliveries.count(), "运行中的父会话让结算 wake 保持 durable")

	// 父会话重新 runnable：下一次 runnable 迁移排空同一笔 wake 并恢复父会话。
	require.NoError(t, host.RuntimeStore.SaveState(ctx, &runtimechat.RuntimeState{
		SessionID: "root-session",
		Status:    runtimechat.SessionIdle,
		UpdatedAt: time.Now().UTC(),
	}))
	require.NoError(t, host.supervisionWake.MaybeWakeParent(ctx, "root-session", "", "root-session"))
	deliveries.wait(t, time.Second)
	require.Equal(t, 1, deliveries.count(), "结算 wake 必须把父会话恢复恰好一次")
	require.Equal(t, "root-session", deliveries.lastParent())
}

// TestProjectLocalAgentCompletionDoesNotDoubleWakeCriticalSettledTurn 钉住镜像
// 规则：终态本身是 critical 时，生命周期投影已经排了 wake，结算路径不得为同一
// turn 再排第二笔（否则一次终态会换出两次恢复）。
func TestProjectLocalAgentCompletionDoesNotDoubleWakeCriticalSettledTurn(t *testing.T) {
	host, store, deliveries := newWakeConsumerTestHost(t, "aicli-settled-turn-critical")
	ctx := context.Background()

	attachSettledTurnWakeFixture(t, host, store, "critical-turn-batches")

	require.NoError(t, host.RuntimeStore.SaveState(ctx, &runtimechat.RuntimeState{
		SessionID: "root-session",
		Status:    runtimechat.SessionIdle,
		UpdatedAt: time.Now().UTC(),
	}))
	require.True(t, host.parkLocalAgentChildObligation(agent.WithTurnID(ctx, "turn-critical"), "root-session", "child-fail"))

	registry := &localActorRegistry{Host: host}
	registry.projectLocalAgentCompletion(ctx, "root-session", "child-fail", "failed", "session_end")

	deliveries.wait(t, time.Second)
	require.Equal(t, 1, deliveries.count(), "critical 终态只允许一次恢复")

	pending, err := store.ListWakePending(ctx, supervision.WakeFilter{RootScopeID: "root-session", UnclaimedOnly: true})
	require.NoError(t, err)
	require.Empty(t, pending, "结算 wake 不得重复 critical 生命周期 wake")
}
