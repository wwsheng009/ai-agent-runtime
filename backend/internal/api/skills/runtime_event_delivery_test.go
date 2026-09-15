package skills

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// P0-2：交付通道分类与未命中计数（批次 20）。
//
// 这些断言钉住两件事：
//  1. 每条通道的白名单变化必须同步反映在 DeliveryChannelsFor 上（分类是
//     「哪类事件经哪条路到达前端」的唯一可测契约）；
//  2. A/B 两条通道的未命中与转发量必须可观测，且计数器有界、可 JSON 序列化
//     （它挂在 runtimeStatusSnapshot 上）。
func TestDeliveryChannelsForChannelSets(t *testing.T) {
	cases := []struct {
		eventType string
		channels  []string
	}{
		// 工具生命周期：唯一同时走「落盘（A）」与「实时帧桥（C）」的类型。
		{"tool.requested", []string{runtimeEventDeliverySessionStore, runtimeEventDeliveryChatBridge}},
		{"tool.completed", []string{runtimeEventDeliverySessionStore, runtimeEventDeliveryChatBridge}},
		// live-only（B）：不落盘，刷新即丢。
		{"tool.progress", []string{runtimeEventDeliveryLiveOnly}},
		{"subagent.progress", []string{runtimeEventDeliveryLiveOnly}},
		// 回合末尾巴（D）：实时通道与事件库都没有。
		{"subagent.batch.started", []string{runtimeEventDeliveryTailOnly}},
		{"subagent.batch.completed", []string{runtimeEventDeliveryTailOnly}},
		// 仅落盘（A）：由快照重建 UI，没有实时帧。
		{"approval_requested", []string{runtimeEventDeliverySessionStore}},
		{"assistant_delta", []string{runtimeEventDeliverySessionStore}},
		// 完全未知：零通道 —— 这类必须计入 unknown 才有机会被发现。
		{"totally.unknown.event", []string{}},
		{"", []string{}},
	}
	for _, tc := range cases {
		t.Run("type="+tc.eventType, func(t *testing.T) {
			assert.ElementsMatch(t, tc.channels, DeliveryChannelsFor(tc.eventType))
		})
	}
}

func TestDeliveryChannelsForAgreesWithPersistWhitelist(t *testing.T) {
	// 分类只要说 session_store，落盘判定就必须放行（带会话 id）；反之亦然。
	// 这是防止两处白名单漂移的最小一致性断言。
	for _, eventType := range []string{
		"tool.requested", "tool.completed", "context.profile.injected",
		"recall.performed", "checkpoint_created", "approval_requested",
		"approval_resolved", "context_reconciled", "assistant_delta",
		"session_compact_started", "totally.unknown.event", "",
	} {
		t.Run("type="+eventType, func(t *testing.T) {
			declared := channelsContain(DeliveryChannelsFor(eventType), runtimeEventDeliverySessionStore)
			actual := shouldPersistRuntimeSessionEvent(runtimeevents.Event{
				Type:      eventType,
				SessionID: "session-x",
			})
			assert.Equal(t, declared, actual,
				"DeliveryChannelsFor 与 shouldPersistRuntimeSessionEvent 对 %q 的判定必须一致", eventType)
		})
	}
}

func TestRuntimeEventDeliveryDropCountersSplitKnownAndUnknown(t *testing.T) {
	resetRuntimeEventDeliveryCountersForTest()

	// 已知类型但无会话归属 ⇒ known + no_session_id。
	recordRuntimeEventDeliveryDrop("tool.progress", "")
	// 已知类型、有会话归属但不在落盘白名单 ⇒ known + type_not_persisted。
	recordRuntimeEventDeliveryDrop("tool.progress", "session-a")
	// 完全未知类型 ⇒ unknown 语义保持独立。
	recordRuntimeEventDeliveryDrop("not.a.real.event", "session-a")

	snapshot := SnapshotRuntimeEventDelivery()
	assert.Equal(t, uint64(3), snapshot["dropped_total"])
	assert.Equal(t, uint64(2), snapshot["dropped_known"])
	assert.Equal(t, uint64(1), snapshot["dropped_unknown"])

	byReason, ok := snapshot["dropped_by_reason"].(map[string]map[string]uint64)
	require.True(t, ok, "dropped_by_reason 必须保持 reason -> 类型 -> 计数 的形状")
	assert.Equal(t, uint64(1), byReason[runtimeEventDeliveryNoSessionID]["tool.progress"])
	assert.Equal(t, uint64(1), byReason[runtimeEventDeliveryTypeNotPersisted]["tool.progress"])
	assert.Equal(t, uint64(1), byReason[runtimeEventDeliveryTypeNotPersisted]["not.a.real.event"])
}

func TestRuntimeEventDeliveryLiveForwardedCountsPerType(t *testing.T) {
	resetRuntimeEventDeliveryCountersForTest()

	recordRuntimeEventDeliveryLiveForwarded("tool.progress")
	recordRuntimeEventDeliveryLiveForwarded("tool.progress")
	recordRuntimeEventDeliveryLiveForwarded("subagent.progress")

	snapshot := SnapshotRuntimeEventDelivery()
	forwarded, ok := snapshot["live_forwarded"].(map[string]uint64)
	require.True(t, ok, "live_forwarded 必须保持 类型 -> 计数 的形状")
	assert.Equal(t, uint64(2), forwarded["tool.progress"])
	assert.Equal(t, uint64(1), forwarded["subagent.progress"])
	// 转发量与丢弃量分账：B 通道的正向证据不能被 A 的丢弃计数污染。
	assert.Equal(t, uint64(0), snapshot["dropped_total"])
}

func TestRuntimeEventDeliveryCountersAreBoundedAndSerializable(t *testing.T) {
	resetRuntimeEventDeliveryCountersForTest()

	for index := 0; index < maxRuntimeEventDeliveryTypes+10; index++ {
		recordRuntimeEventDeliveryDrop("flood."+strconv.Itoa(index), "")
	}

	snapshot := SnapshotRuntimeEventDelivery()
	byReason := snapshot["dropped_by_reason"].(map[string]map[string]uint64)
	counters := byReason[runtimeEventDeliveryNoSessionID]
	require.NotEmpty(t, counters)
	assert.LessOrEqual(t, len(counters), maxRuntimeEventDeliveryTypes+1,
		"类型维必须封顶（允许 _other 溢出桶）")
	assert.GreaterOrEqual(t, snapshot["overflow"].(uint64), uint64(1),
		"超出上界的类型必须计入 overflow，不能静默丢失")

	_, err := json.Marshal(snapshot)
	require.NoError(t, err, "快照挂在 runtimeStatusSnapshot 上，必须可 JSON 序列化")
}

func channelsContain(channels []string, wanted string) bool {
	for _, channel := range channels {
		if channel == wanted {
			return true
		}
	}
	return false
}
