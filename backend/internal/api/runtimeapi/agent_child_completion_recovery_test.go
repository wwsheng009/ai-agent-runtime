package runtimeapi

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// 2026-09-30 现场（CLI 侧 W6 同源缺口）API 对等回归：完成订阅是进程内存态，
// 宿主重启后必须重建（P0-A）并把丢失的终态补投影一次（P0-B）。本用例钉住：
//   - 有终态、无通知的子会话被补投影（通知落库）且每进程每 root 只做一次；
//   - 无终态的子会话在收敛后重新订阅，下一次 session_end 必须被实时投影。

func seedAPIRecoveryChild(t *testing.T, handler *Handler, sessionManager *chat.SessionManager, runtimeStore *chat.InMemoryRuntimeStore, registryStore agentcontrol.AgentRegistryStore, rootID, childID string, terminalAt *time.Time, rowStatus string) {
	t.Helper()
	ctx := context.Background()
	child := &chat.Session{ID: childID, UserID: "agent", State: chat.StateActive}
	child.SetContext(toolbroker.AgentSessionContextParentSessionID, rootID)
	child.SetContext(toolbroker.AgentSessionContextRootSessionID, rootID)
	child.SetContext(toolbroker.AgentSessionContextPath, "/root/"+childID)
	child.SetContext(toolbroker.AgentSessionContextDepth, 1)
	child.SetContext(toolbroker.AgentSessionContextAgentType, "general")
	require.NoError(t, sessionManager.GetStorage().Save(ctx, child))

	updatedAt := time.Now().UTC()
	if terminalAt != nil {
		updatedAt = *terminalAt
	}
	require.NoError(t, runtimeStore.SaveState(ctx, &chat.RuntimeState{
		SessionID: childID,
		Status:    chat.SessionIdle,
		UpdatedAt: updatedAt,
	}))
	if terminalAt != nil {
		_, err := runtimeStore.AppendEvent(ctx, runtimeevents.Event{
			Type:      chat.EventSessionEnd,
			SessionID: childID,
			TraceID:   "trace-" + childID,
			Timestamp: *terminalAt,
			Payload:   map[string]interface{}{"status": "idle", "success": true},
		})
		require.NoError(t, err)
	}
	_, err := registryStore.UpsertAgentControlAgent(ctx, agentcontrol.AgentRecord{
		AgentID:         childID,
		RootSessionID:   rootID,
		ParentAgentID:   "root:" + rootID,
		ParentSessionID: rootID,
		SessionID:       childID,
		AgentPath:       "/root/" + childID,
		Depth:           1,
		Status:          rowStatus,
	})
	require.NoError(t, err)
}

func listAPIChildCompletionNotifications(t *testing.T, store *supervision.SQLiteSupervisionStore, rootID, childID string) []supervision.Notification {
	t.Helper()
	notifications, err := store.ListNotifications(context.Background(), supervision.NotificationFilter{
		RootScopeID:     rootID,
		SubjectKind:     supervision.SubjectAgentSession,
		SubjectID:       childID,
		IncludeResolved: true,
	})
	require.NoError(t, err)
	return notifications
}

func TestRecoverAgentChildCompletionsReplaysAndRebinds(t *testing.T) {
	handler, store, _ := newAPIWakeTestHandler(t, "api-child-completion-recovery")
	ctx := context.Background()
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	defer sessionManager.Stop()
	handler.SetSessionManager(sessionManager)

	runtimeStore := chat.NewInMemoryRuntimeStore(64)
	handler.sessionEventStore = runtimeStore
	handler.sessionRuntimeStore = runtimeStore

	registryStore, err := agentcontrol.NewSQLiteGlobalAgentRegistryStore(&agentcontrol.GlobalAgentStoreConfig{
		Path: filepath.Join(t.TempDir(), "agents.sqlite"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = registryStore.Close() })
	handler.SetAgentControlAgentStore(registryStore)

	const rootID = "api-root-recovery"
	const replayedChild = "api-child-replayed"
	const reboundChild = "api-child-rebound"
	terminalAt := time.Now().UTC().Add(-3 * time.Minute)
	seedAPIRecoveryChild(t, handler, sessionManager, runtimeStore, registryStore, rootID, replayedChild, &terminalAt, agentcontrol.AgentStatusClosed)
	seedAPIRecoveryChild(t, handler, sessionManager, runtimeStore, registryStore, rootID, reboundChild, nil, agentcontrol.AgentStatusActive)

	handler.recoverAgentChildCompletions(ctx, registryStore)

	require.NotEmpty(t, listAPIChildCompletionNotifications(t, store, rootID, replayedChild),
		"丢失的终态必须补投影出 supervision 通知")
	require.Empty(t, listAPIChildCompletionNotifications(t, store, rootID, reboundChild),
		"没有终态的子会话不得被凭空补投影")

	// 每进程每 root 一次：重复调用不再重放。
	before := len(listAPIChildCompletionNotifications(t, store, rootID, replayedChild))
	handler.recoverAgentChildCompletions(ctx, registryStore)
	require.Len(t, listAPIChildCompletionNotifications(t, store, rootID, replayedChild), before)

	// P0-A：重建订阅后，子会话的下一次终态必须被实时投影。
	// （隔离断言：父会话 id 必须能从子会话上下文解析——重建路径的第一个前置。）
	childSession, err := sessionManager.GetStorage().Load(ctx, reboundChild)
	require.NoError(t, err)
	require.NotNil(t, childSession)
	require.Equal(t, rootID, apiChildParentSessionID(childSession), "父会话 id 必须能从子会话上下文解析")
	handler.getRuntimeEventBus().Publish(runtimeevents.Event{
		Type:      chat.EventSessionEnd,
		SessionID: reboundChild,
		TraceID:   "trace-" + reboundChild + "-new",
		Timestamp: time.Now().UTC(),
		Payload:   map[string]interface{}{"status": "idle", "success": true},
	})
	rows, err := registryStore.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{AgentID: reboundChild, IncludeClosed: true})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, agentcontrol.AgentStatusClosed, rows[0].Status,
		"处理器第一步（registry close）必须先跑起来：否则说明订阅没有重建")
	require.NotEmpty(t, listAPIChildCompletionNotifications(t, store, rootID, reboundChild),
		"重建订阅后子会话终态必须重新走完整投影")
}
