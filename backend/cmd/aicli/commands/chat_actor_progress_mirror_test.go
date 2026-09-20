package commands

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolprotocol"
)

// P0-1c（M7）：CLI 宿主进度镜像对等的独立用例。覆盖：
//   - 重复帧在 2s 窗口内合并、state 变化立即透传（与 API 宿主同实现）；
//   - 镜像事件只走 live 通道，A 通道桥在场也不落 event store；
//   - per-host 长生命周期 mirror 同时服务订阅与 BatchProgressSource.Messages；
//   - 子会话终态 Forget + host.Close() 兜底释放订阅，不残留投递。
const (
	localMirrorTestParent = "parent-mirror"
	localMirrorTestChild  = "child-mirror"
)

func newLocalProgressMirrorTestHost(t *testing.T) (*localChatRuntimeHost, *runtimeevents.Bus, runtimechat.EventStore) {
	t.Helper()
	bus := runtimeevents.NewBusWithRetention(64)
	store := runtimechat.NewInMemoryRuntimeStore(64)
	host := &localChatRuntimeHost{EventBus: bus, EventStore: store}
	t.Cleanup(host.Close)
	return host, bus, store
}

func newLocalMirrorTestChild(t *testing.T, childID string) *runtimechat.Session {
	t.Helper()
	child := &runtimechat.Session{ID: childID, UserID: "agent"}
	child.SetContext(toolbroker.AgentSessionContextParentSessionID, localMirrorTestParent)
	child.SetContext(toolbroker.AgentSessionContextPath, "/root/"+childID)
	child.SetContext(toolbroker.AgentSessionContextDepth, 1)
	child.SetContext(toolbroker.AgentSessionContextAgentType, "worker")
	return child
}

func publishLocalMirrorProgress(bus *runtimeevents.Bus, childID, state, message string) {
	bus.Publish(runtimeevents.Event{
		Type:      toolprotocol.EventTypeProgress,
		SessionID: childID,
		TraceID:   "trace-" + childID,
		ToolName:  "shell",
		Timestamp: time.Now().UTC(),
		Payload: map[string]interface{}{
			"tool_call_id": "call-1",
			"tool_name":    "shell",
			"kind":         state,
			"message":      message,
		},
	})
}

func collectLocalMirrorProgress(bus *runtimeevents.Bus) (func() []runtimeevents.Event, func()) {
	var mu sync.Mutex
	var frames []runtimeevents.Event
	unsubscribe := bus.SubscribeCancelable(supervision.EventTypeSubagentProgress, func(event runtimeevents.Event) {
		mu.Lock()
		frames = append(frames, event)
		mu.Unlock()
	})
	snapshot := func() []runtimeevents.Event {
		mu.Lock()
		defer mu.Unlock()
		return append([]runtimeevents.Event(nil), frames...)
	}
	return snapshot, unsubscribe
}

func TestLocalProgressMirrorMergesRepeatedFramesAndKeepsStateChanges(t *testing.T) {
	host, bus, store := newLocalProgressMirrorTestHost(t)
	// A 通道桥必须在场：即使落盘桥订阅了整条总线，live-only 的镜像也不得落库。
	host.bindRuntimeEventPersistence()
	registry := newLocalActorRegistry(host)
	registry.subscribeLocalAgentCompletion(localMirrorTestParent, newLocalMirrorTestChild(t, localMirrorTestChild))

	mirrors, unsubscribe := collectLocalMirrorProgress(bus)
	defer unsubscribe()

	publishLocalMirrorProgress(bus, localMirrorTestChild, "progress", "step 1")
	require.Len(t, mirrors(), 1, "首帧必须透传")

	publishLocalMirrorProgress(bus, localMirrorTestChild, "progress", "step 1")
	require.Len(t, mirrors(), 1, "窗口内同 state/message 的重复帧只发一次")

	publishLocalMirrorProgress(bus, localMirrorTestChild, "progress", "step 2")
	require.Len(t, mirrors(), 1, "窗口内同 state 的新 message 仍被合并（leading-edge throttle）")

	publishLocalMirrorProgress(bus, localMirrorTestChild, "completed", "done")
	frames := mirrors()
	require.Len(t, frames, 2, "state 变化不受窗口抑制，立即透传")
	require.Equal(t, localMirrorTestParent, frames[1].SessionID, "镜像发到父会话通道")
	require.Equal(t, supervision.EventTypeSubagentProgress, frames[1].Type)
	require.Equal(t, localMirrorTestChild, frames[1].Payload["agent_id"])
	require.Equal(t, "completed", frames[1].Payload["state"])
	require.Equal(t, toolprotocol.EventTypeProgress, frames[1].Payload["source_event_type"])
	require.Equal(t, "/root/"+localMirrorTestChild, frames[1].Payload["path"])
	require.Equal(t, 1, frames[1].Payload["depth"])
	require.Equal(t, "worker", frames[1].Payload["agent_type"])

	// live-only：父/子会话事件库都不得出现任何事件（含镜像与 tool.progress）。
	for _, sessionID := range []string{localMirrorTestParent, localMirrorTestChild} {
		events, err := store.ListEvents(context.Background(), sessionID, 0, 0)
		require.NoError(t, err)
		require.Empty(t, events, "live-only 镜像禁止写入 event store: %s", sessionID)
	}
}

func TestLocalProgressMirrorCarriesParentToolCallID(t *testing.T) {
	host, bus, _ := newLocalProgressMirrorTestHost(t)
	registry := newLocalActorRegistry(host)
	child := newLocalMirrorTestChild(t, localMirrorTestChild)
	// 父侧工具调用归位（D）：broker 注入 → Spawn 落上下文 → 镜像回填。
	child.SetContext(toolbroker.AgentSessionContextParentToolCallID, "call-spawn-7")
	registry.subscribeLocalAgentCompletion(localMirrorTestParent, child)

	mirrors, unsubscribe := collectLocalMirrorProgress(bus)
	defer unsubscribe()

	publishLocalMirrorProgress(bus, localMirrorTestChild, "progress", "step 1")
	frames := mirrors()
	require.Len(t, frames, 1)
	require.Equal(t, "call-spawn-7", frames[0].Payload["parent_tool_call_id"])
}

func TestLocalBatchProgressSourceEnrichesLastMessageFromHostMirror(t *testing.T) {
	host, bus, _ := newLocalProgressMirrorTestHost(t)
	host.SubagentBatches = newTestSubagentBatchStore(t)
	registry := newLocalActorRegistry(host)
	registry.subscribeLocalAgentCompletion(localMirrorTestParent, newLocalMirrorTestChild(t, localMirrorTestChild))

	now := time.Now().UTC()
	created, err := host.SubagentBatches.CreateBatch(context.Background(), &subagentbatch.SubagentBatch{
		BatchID:         "batch-mirror",
		RootScopeID:     localMirrorTestParent,
		ParentSessionID: localMirrorTestParent,
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
		Status:          subagentbatch.BatchRunning,
		TaskCount:       1,
		RunningCount:    1,
		HeartbeatAt:     now,
		CreatedAt:       now,
		UpdatedAt:       now,
		Version:         1,
	}, []subagentbatch.SubagentTaskRecord{
		{TaskID: "task-mirror", ChildSessionID: localMirrorTestChild, Status: subagentbatch.TaskRunning, OrderIndex: 1, UpdatedAt: now, Version: 1},
	})
	require.NoError(t, err)
	require.True(t, created)

	publishLocalMirrorProgress(bus, localMirrorTestChild, "progress", "bash: go test ./cmd/aicli/...")

	source := host.newLocalBatchProgressSource()
	require.NotNil(t, source)
	batchSource, ok := source.(*supervision.BatchProgressSource)
	require.True(t, ok)
	require.NotNil(t, batchSource.Messages, "CLI digest 必须带 last_message 富化")
	messages, ok := batchSource.Messages.(supervision.MirrorProgressMessages)
	require.True(t, ok)
	require.Same(t, host.subagentProgressMirror(), messages.Mirror, "mirror 必须是 per-host 长生命周期实例，不是每次调用新建")

	groups, err := source.ListProgress(context.Background(), supervision.ProgressRequest{ParentSessionID: localMirrorTestParent})
	require.NoError(t, err)
	require.Len(t, groups, 1)
	require.Len(t, groups[0].RunningTasks, 1)
	require.Equal(t, localMirrorTestChild, groups[0].RunningTasks[0].ChildSessionID)
	require.Equal(t, "bash: go test ./cmd/aicli/...", groups[0].RunningTasks[0].LastMessage)
}

func TestLocalProgressMirrorForgetsOnSessionEndAndHostClose(t *testing.T) {
	host, bus, _ := newLocalProgressMirrorTestHost(t)
	registry := newLocalActorRegistry(host)
	registry.subscribeLocalAgentCompletion(localMirrorTestParent, newLocalMirrorTestChild(t, localMirrorTestChild))

	mirrors, unsubscribe := collectLocalMirrorProgress(bus)
	defer unsubscribe()

	publishLocalMirrorProgress(bus, localMirrorTestChild, "progress", "step 1")
	require.Len(t, mirrors(), 1)
	require.Equal(t, 1, host.subagentProgressMirror().Pending(), "完成前保留该子会话的节流状态")

	bus.Publish(runtimeevents.Event{
		Type:      runtimechat.EventSessionEnd,
		SessionID: localMirrorTestChild,
		Payload:   map[string]interface{}{"success": true},
	})
	require.Zero(t, host.subagentProgressMirror().Pending(), "子会话完成必须 Forget 节流状态")

	host.childEventUnsubsMu.Lock()
	remaining := len(host.childEventUnsubs)
	host.childEventUnsubsMu.Unlock()
	require.Zero(t, remaining, "session_end 释放 per-child 订阅")

	// host.Close() 兜底：一个从未收到 session_end 的子会话订阅也必须被释放。
	registry.subscribeLocalAgentCompletion(localMirrorTestParent, newLocalMirrorTestChild(t, "child-mirror-open"))
	host.childEventUnsubsMu.Lock()
	tracked := len(host.childEventUnsubs)
	host.childEventUnsubsMu.Unlock()
	require.Equal(t, 1, tracked)

	host.Close()
	host.childEventUnsubsMu.Lock()
	remaining = len(host.childEventUnsubs)
	host.childEventUnsubsMu.Unlock()
	require.Zero(t, remaining, "Close 必须释放未收敛的子会话订阅")

	publishLocalMirrorProgress(bus, "child-mirror-open", "progress", "step 1")
	require.Len(t, mirrors(), 1, "Close 之后不再有镜像投递（订阅已释放）")
}
