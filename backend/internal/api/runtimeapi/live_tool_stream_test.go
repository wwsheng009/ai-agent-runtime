package runtimeapi

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type liveToolFrame struct {
	event   string
	payload map[string]interface{}
}

func collectLiveToolFrames(t *testing.T, bus *runtimeevents.Bus, sessionID string, tracker *liveToolStreamTracker) (*[]liveToolFrame, func()) {
	t.Helper()
	frames := &[]liveToolFrame{}
	unsubscribe := subscribeLiveToolStream(bus, sessionID, tracker, func(eventName string, payload map[string]interface{}) {
		*frames = append(*frames, liveToolFrame{event: eventName, payload: payload})
	})
	return frames, unsubscribe
}

// 修复目标 1：agent loop 的工具生命周期运行事件必须在执行当下就变成 chat SSE 帧，
// 且行 id 用真实 provider call id（前端工具行据此立即建行）。
func TestSubscribeLiveToolStreamEmitsFramesWithProviderCallID(t *testing.T) {
	bus := runtimeevents.NewBusWithRetention(16)
	tracker := newLiveToolStreamTracker()
	frames, unsubscribe := collectLiveToolFrames(t, bus, "session-1", tracker)
	defer unsubscribe()

	bus.Publish(runtimeevents.Event{
		Type:      runtimeEventToolRequested,
		SessionID: "session-1",
		ToolName:  "view",
		Payload: map[string]interface{}{
			"tool_call_id": "call_00_A",
			"logical_tool": "view",
			"step":         1,
			"arg_preview":  `{"file_path":"a.go"}`,
			"file_path":    "a.go",
		},
	})

	require.Len(t, *frames, 2, "tool.requested 应展开为 tool_call + tool_start 两帧")
	assert.Equal(t, "tool_call", (*frames)[0].event)
	assert.Equal(t, "tool_start", (*frames)[1].event)
	for _, frame := range *frames {
		toolCall, ok := frame.payload["tool_call"].(map[string]interface{})
		require.True(t, ok)
		assert.Equal(t, "call_00_A", toolCall["id"], "实时帧必须用真实 provider call id 建行")
		tool, ok := frame.payload["tool"].(map[string]interface{})
		require.True(t, ok)
		assert.Equal(t, "view", tool["name"])
		assert.Equal(t, "a.go", tool["file_path"], "定位字段要随帧下发，供前端提取工具卡明细")
		metadata, ok := frame.payload["metadata"].(map[string]interface{})
		require.True(t, ok)
		assert.Equal(t, true, metadata["live"])
		assert.Equal(t, "call_00_A", metadata["provider_tool_call_id"])
	}
	assert.Equal(t, "tool_call", (*frames)[0].payload["type"])
	assert.Equal(t, "tool_start", (*frames)[1].payload["type"])

	bus.Publish(runtimeevents.Event{
		Type:      runtimeEventToolCompleted,
		SessionID: "session-1",
		ToolName:  "view",
		Payload: map[string]interface{}{
			"tool_call_id": "call_00_A",
			"logical_tool": "view",
			"step":         1,
			"summary":      "read 12 lines",
		},
	})

	require.Len(t, *frames, 3)
	end := (*frames)[2]
	assert.Equal(t, "tool_end", end.event)
	tool, ok := end.payload["tool"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "call_00_A", tool["id"])
	// status 与回合末尾巴保持同一约定（事件名，前端按帧类型得到行状态）。
	assert.Equal(t, "tool_end", tool["status"])
	assert.Equal(t, "read 12 lines", tool["content"])
	assert.Equal(t, "read 12 lines", end.payload["content"])
}

// 修复目标 2：重复的 tool.requested（审批回执路径 approved_tool.go 会再发一次）
// 不得重复发帧、更不得把后续工具的观测序号挤偏；其他会话的事件必须被忽略。
func TestSubscribeLiveToolStreamIgnoresDuplicatesAndOtherSessions(t *testing.T) {
	bus := runtimeevents.NewBusWithRetention(16)
	tracker := newLiveToolStreamTracker()
	frames, unsubscribe := collectLiveToolFrames(t, bus, "session-1", tracker)
	defer unsubscribe()

	requested := func(sessionID, callID string, step int) runtimeevents.Event {
		return runtimeevents.Event{
			Type:      runtimeEventToolRequested,
			SessionID: sessionID,
			Payload: map[string]interface{}{
				"tool_call_id": callID,
				"logical_tool": "bash",
				"step":         step,
			},
		}
	}

	bus.Publish(requested("session-2", "call_other", 1))
	assert.Empty(t, *frames, "其他会话的事件不得混入本会话的 SSE 流")

	bus.Publish(requested("session-1", "call_00_B", 1))
	bus.Publish(requested("session-1", "call_00_B", 1))
	assert.Len(t, *frames, 2, "同一次调用的重复请求事件只发一次帧")

	bus.Publish(requested("session-1", "call_00_C", 1))
	require.Len(t, *frames, 4)

	// 序号必须按「每个 step 首次出现」推进：第二个工具应落在 tool_1 上，
	// 与 internal/agent/loop.go observe() 的 `step_%d_tool_%d` 对齐。
	assert.Equal(t, "call_00_C", (*frames)[2].payload["tool_call"].(map[string]interface{})["id"])
	_, rowID, fresh := tracker.recordRequest("", "bash", 1, -1)
	assert.True(t, fresh)
	assert.Equal(t, "observation_step_1_tool_2", rowID, "provider 缺 id 时退回与观测命名一致的占位 id")
}

// 修复目标 3：回合末证据尾巴对已实时下发的工具只补权威 tool_end，且行 id 改写为
// 真实 call id（前端 upsert 合并为一行）；未实时下发的工具保持原有三段式。
func TestBuildObservedToolEventPayloadsWithLiveRewritesStreamedRows(t *testing.T) {
	tracker := newLiveToolStreamTracker()
	_, rowID, fresh := tracker.recordRequest("call_00_A", "view", 1, -1)
	require.True(t, fresh)
	require.Equal(t, "call_00_A", rowID)

	resultPayload := map[string]interface{}{
		"observations": []types.Observation{
			{Step: "step_1_tool_0", Tool: "view", Output: "file content", Success: true},
			{Step: "step_1_tool_1", Tool: "bash", Output: "ok", Success: true},
		},
	}

	events := buildObservedToolEventPayloadsWithLive(resultPayload, tracker)
	require.Len(t, events, 4, "实时工具 1 条 tool_end + 未实时工具 3 段")

	assert.Equal(t, "tool_end", events[0].Event)
	assert.Equal(t, "call_00_A", events[0].Payload["tool_call"].(map[string]interface{})["id"])
	assert.Equal(t, "file content", events[0].Payload["content"])
	assert.Equal(t, true, events[0].Payload["metadata"].(map[string]interface{})["live"])

	assert.Equal(t, []string{"tool_call", "tool_start", "tool_end"}, []string{events[1].Event, events[2].Event, events[3].Event})
	for _, event := range events[1:] {
		assert.Equal(t, "observation_step_1_tool_1", event.Payload["tool_call"].(map[string]interface{})["id"])
	}
}

// 回归：无追踪器（静态结果路径等）时保持修复前的三段式行为。
func TestBuildObservedToolEventPayloadsWithoutLiveKeepsLegacyShape(t *testing.T) {
	resultPayload := map[string]interface{}{
		"observations": []types.Observation{
			{Step: "step_1_tool_0", Tool: "view", Output: "file content", Success: true},
		},
	}

	events := buildObservedToolEventPayloads(resultPayload)
	require.Len(t, events, 3)
	assert.Equal(t, []string{"tool_call", "tool_start", "tool_end"}, []string{events[0].Event, events[1].Event, events[2].Event})
	assert.Equal(t, "observation_step_1_tool_0", events[0].Payload["tool_call"].(map[string]interface{})["id"])

	nilTracker := buildObservedToolEventPayloadsWithLive(resultPayload, nil)
	require.Len(t, nilTracker, 3)
	assert.Equal(t, events[0].Payload["tool_call"], nilTracker[0].Payload["tool_call"])
}

// 回归：并行批次（tool_parallel_scheduler.go）在各自 goroutine 里发
// tool.requested，到达顺序与 results[item.index] 无关。序号必须按事件自带的
// batch_index 发，否则尾巴会把完成帧合并到**别的工具行**上。
func TestBuildObservedToolEventPayloadsWithLiveKeepsParallelRowsAligned(t *testing.T) {
	bus := runtimeevents.NewBusWithRetention(16)
	tracker := newLiveToolStreamTracker()
	_, unsubscribe := collectLiveToolFrames(t, bus, "session-1", tracker)
	defer unsubscribe()

	requested := func(callID, toolName string, batchIndex int) runtimeevents.Event {
		return runtimeevents.Event{
			Type:      runtimeEventToolRequested,
			SessionID: "session-1",
			ToolName:  toolName,
			Payload: map[string]interface{}{
				"tool_call_id": callID,
				"logical_tool": toolName,
				"step":         1,
				"batch_index":  batchIndex,
				"parallel":     true,
			},
		}
	}

	// 故意乱序到达：batch_index=1 的完成事件先被发出。
	bus.Publish(requested("call_00_second", "bash", 1))
	bus.Publish(requested("call_00_first", "view", 0))

	resultPayload := map[string]interface{}{
		"observations": []types.Observation{
			{Step: "step_1_tool_0", Tool: "view", Output: "view output", Success: true},
			{Step: "step_1_tool_1", Tool: "bash", Output: "bash output", Success: true},
		},
	}

	events := buildObservedToolEventPayloadsWithLive(resultPayload, tracker)
	require.Len(t, events, 2, "两个工具都已实时下发，尾巴只补两条 tool_end")
	assert.Equal(t, "call_00_first", events[0].Payload["tool_call"].(map[string]interface{})["id"])
	assert.Equal(t, "view output", events[0].Payload["content"])
	assert.Equal(t, "call_00_second", events[1].Payload["tool_call"].(map[string]interface{})["id"])
	assert.Equal(t, "bash output", events[1].Payload["content"])
}

// 回归：序号漂移的安全网——观测键上的工具名与实时行登记的名字不一致时，尾巴
// 不按 id 改写，退回三段式（宁可多一行，也不把输出合并进别的工具行）。
func TestLiveToolStreamTrackerRejectsMismatchedToolName(t *testing.T) {
	tracker := newLiveToolStreamTracker()
	key, rowID, fresh := tracker.recordRequest("call_00_A", "view", 1, -1)
	require.True(t, fresh)
	require.Equal(t, "step_1_tool_0", key)
	require.Equal(t, "call_00_A", rowID)

	if _, ok := tracker.rowIDForObservationKey(key, "bash"); ok {
		t.Fatal("工具名不一致时不得按 id 改写（会把完成帧合并到别的工具行）")
	}
	if id, ok := tracker.rowIDForObservationKey(key, "VIEW"); !ok || id != "call_00_A" {
		t.Fatalf("同名（大小写无关）或观测未给名字时应放行，got id=%q ok=%v", id, ok)
	}
	if id, ok := tracker.rowIDForObservationKey(key, ""); !ok || id != "call_00_A" {
		t.Fatalf("观测未给工具名时不得误判为漂移，got id=%q ok=%v", id, ok)
	}

	// 名字不一致时尾巴确实退回三段式。
	resultPayload := map[string]interface{}{
		"observations": []types.Observation{
			{Step: key, Tool: "bash", Output: "other output", Success: true},
		},
	}
	events := buildObservedToolEventPayloadsWithLive(resultPayload, tracker)
	require.Len(t, events, 3)
	assert.Equal(t, []string{"tool_call", "tool_start", "tool_end"}, []string{events[0].Event, events[1].Event, events[2].Event})
	assert.Equal(t, "observation_step_1_tool_0", events[0].Payload["tool_call"].(map[string]interface{})["id"])
}

// P1-2（批次 20）：行身份随帧下发。
//
// 前端此前只能从 tool_call_id 反推身份：缺 id 的降级帧与权威帧静默分成两行，
// 没有任何信号说明「身份是猜的」。现在有真实 provider call id 的帧带
// entity{kind,id}，观测合成身份的帧带 entity{...degraded:true}，两者都不与
// 「没有 id」混为一谈（后者不下发 entity，由前端按 seq 兜底并标 degraded）。
func TestLiveToolFramesAttachAuthoritativeEntity(t *testing.T) {
	bus := runtimeevents.NewBusWithRetention(16)
	tracker := newLiveToolStreamTracker()
	frames, unsubscribe := collectLiveToolFrames(t, bus, "session-1", tracker)
	defer unsubscribe()

	bus.Publish(runtimeevents.Event{
		Type:      runtimeEventToolRequested,
		SessionID: "session-1",
		Payload: map[string]interface{}{
			"tool_call_id": "call_00_A",
			"logical_tool": "view",
			"step":         1,
		},
	})

	require.Len(t, *frames, 2)
	for index, frame := range *frames {
		assert.Equal(t,
			map[string]interface{}{"kind": "tool", "id": "call_00_A"},
			frame.payload["entity"],
			"实时帧 %d 必须带权威实体身份", index)
	}
}

// 缺 provider call id 时不得编造身份：entity 键缺席，交给前端按 seq 兜底并标
// degraded（这正是「无法合并」要被看见的那条路径）。
func TestLiveToolFramesOmitEntityWithoutCallID(t *testing.T) {
	bus := runtimeevents.NewBusWithRetention(16)
	tracker := newLiveToolStreamTracker()
	frames, unsubscribe := collectLiveToolFrames(t, bus, "session-1", tracker)
	defer unsubscribe()

	bus.Publish(runtimeevents.Event{
		Type:      runtimeEventToolRequested,
		SessionID: "session-1",
		Payload:   map[string]interface{}{"logical_tool": "view", "step": 1},
	})

	require.NotEmpty(t, *frames)
	for index, frame := range *frames {
		_, exists := frame.payload["entity"]
		assert.False(t, exists, "实时帧 %d 没有权威 id 时不得下发 entity", index)
	}
}

// 回合末证据尾巴：实时登记过的工具补一条权威 tool_end（entity 无 degraded）；
// 退回三段式的工具三帧都标 degraded，把「与实时帧无法合并」写进数据。
func TestObservedToolTailEntityIdentity(t *testing.T) {
	tracker := newLiveToolStreamTracker()
	key, _, fresh := tracker.recordRequest("call_00_live", "view", 1, -1)
	require.True(t, fresh)
	require.Equal(t, "step_1_tool_0", key)

	resultPayload := map[string]interface{}{
		"observations": []types.Observation{
			{Step: key, Tool: "view", Output: "live output", Success: true},
			{Step: "step_1_tool_1", Tool: "bash", Output: "legacy output", Success: true},
		},
	}

	events := buildObservedToolEventPayloadsWithLive(resultPayload, tracker)
	require.Len(t, events, 4, "实时工具只补一条 tool_end，另一个保持三段式")

	assert.Equal(t, "tool_end", events[0].Event)
	assert.Equal(t,
		map[string]interface{}{"kind": "tool", "id": "call_00_live"},
		events[0].Payload["entity"],
		"实时匹配的行 id 是真实 provider call id ⇒ 身份权威")

	assert.Equal(t, []string{"tool_call", "tool_start", "tool_end"},
		[]string{events[1].Event, events[2].Event, events[3].Event})
	for index, frame := range events[1:] {
		assert.Equal(t,
			map[string]interface{}{
				"kind":     "tool",
				"id":       "observation_step_1_tool_1",
				"degraded": true,
			},
			frame.Payload["entity"],
			"三段式第 %d 帧的身份是观测合成的，必须显式 degraded", index)
	}
}
