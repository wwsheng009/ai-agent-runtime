package skills

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

// Batch 3 · 传输增强的回归护栏（方案 §4 Batch 3 表 / §7 验收）：
//  1. 相邻增量合帧：同类型 + 同 turn/stream + 连续 seq 才折叠，跳号/换段/换槽位不折叠；
//  2. B 通道 latest-wins：同合并键只留最新值，键数超限的丢弃必须可观测；
//  3. 连接参数：tail=N 只发尾窗、与 after 互斥，retry_ms / keepalive_ms 有界校验；
//  4. 连接级指标并入 runtime_event_delivery 快照（只增不改）。

const batch3SessionID = "session-runtime-stream-batch3"

// batch3Delta 造一条打字机增量事件（持久化 seq 由 EventStore 注入 payload）。
func batch3Delta(text string) runtimeevents.Event {
	return runtimeevents.Event{
		Type:      chat.EventAssistantDelta,
		SessionID: batch3SessionID,
		TraceID:   "trace-batch3",
		Payload: map[string]interface{}{
			"turn_id":   "turn-batch3",
			"stream_id": "stream-batch3",
			"delta":     text,
		},
	}
}

// batch3DeltaWithSeq 造一条自带 seq 的增量事件（合帧单元测试不经过 store）。
func batch3DeltaWithSeq(text string, seq int64) runtimeevents.Event {
	event := batch3Delta(text)
	event.Payload["seq"] = seq
	return event
}

// TestCoalesceRuntimeEventPageFoldsConsecutiveDeltas 覆盖最小区间：「Hel」「lo」
// 「 world」三段连续 seq 折叠成一帧，文本拼接，coalesced_from 保留下界、payload.seq
// 前进到区间末行（客户端游标不变量）。
func TestCoalesceRuntimeEventPageFoldsConsecutiveDeltas(t *testing.T) {
	resetRuntimeEventStreamMetricsForTest()
	defer resetRuntimeEventStreamMetricsForTest()

	merged := coalesceRuntimeEventPage([]runtimeevents.Event{
		batch3DeltaWithSeq("Hel", 1),
		batch3DeltaWithSeq("lo", 2),
		batch3DeltaWithSeq(" world", 3),
	}, true)

	require.Len(t, merged, 1)
	assert.Equal(t, "Hello world", merged[0].Payload["delta"])
	assert.Equal(t, int64(1), merged[0].Payload[streamCoalesceFromKey], "coalesced_from 必须是区间下界")
	assert.Equal(t, 3, merged[0].Payload[streamCoalesceCountKey])
	assert.Equal(t, int64(3), merged[0].Payload["seq"], "合帧后游标必须停在区间末行")

	snapshot := runtimeEventStreamMetricsSnapshot()
	assert.Equal(t, uint64(1), snapshot["coalesced_frames"])
	assert.Equal(t, uint64(2), snapshot["coalesced_rows"])
}

// TestCoalesceRuntimeEventPageRespectsGapsAndSegments 覆盖三类「不许折叠」：
// 连续 seq 中间被别的帧打断、seq 跳号（区间不完整）、换 turn（另一段流）。
func TestCoalesceRuntimeEventPageRespectsGapsAndSegments(t *testing.T) {
	otherTurn := batch3DeltaWithSeq("de", 6)
	otherTurn.Payload["turn_id"] = "turn-other"

	merged := coalesceRuntimeEventPage([]runtimeevents.Event{
		batch3DeltaWithSeq("a", 1),
		batch3DeltaWithSeq("b", 2), // 与上一条折叠：ab（count=2）
		{Type: chat.EventToolFinished, SessionID: batch3SessionID, Payload: map[string]interface{}{"seq": int64(3), "tool_call_id": "call-1"}},
		batch3DeltaWithSeq("c", 4),
		batch3DeltaWithSeq("d", 6), // 跳号 5：区间不完整，不折叠
		otherTurn,                  // 换 turn：另一段流，不折叠
	}, true)

	require.Len(t, merged, 5)
	assert.Equal(t, "ab", merged[0].Payload["delta"])
	assert.Equal(t, 2, merged[0].Payload[streamCoalesceCountKey])
	assert.Equal(t, chat.EventToolFinished, merged[1].Type, "非增量帧必须原样保留")
	assert.Equal(t, "c", merged[2].Payload["delta"])
	assert.NotContains(t, merged[2].Payload, streamCoalesceCountKey)
	assert.Equal(t, "d", merged[3].Payload["delta"], "跳号后必须自成新帧")
	assert.Equal(t, "de", merged[4].Payload["delta"], "换 turn 后必须自成新帧")
}

// TestMergeCoalescibleRuntimeEventReplacesOnSnapshot 覆盖「权威快照」语义：
// mode=replace 的帧已含此前正文，合并时替换而不是累加，但行数仍计入 coalesced_count。
func TestMergeCoalescibleRuntimeEventReplacesOnSnapshot(t *testing.T) {
	last := batch3DeltaWithSeq("a", 5)
	incoming := batch3DeltaWithSeq("abc", 6)
	incoming.Payload["mode"] = "replace"

	merged, ok := mergeCoalescibleRuntimeEvent(last, incoming)
	require.True(t, ok)
	assert.Equal(t, "abc", merged.Payload["delta"], "替换语义下不得叠加旧文本")
	assert.Equal(t, int64(5), merged.Payload[streamCoalesceFromKey])
	assert.Equal(t, 2, merged.Payload[streamCoalesceCountKey])
}

// TestStreamLiveQueueLatestWinsKeepsNewestValuePerKey 覆盖 latest-wins 的核心语义：
// 同一 tool_call_id 的连续进度帧只保留最新值，合并次数可增量回传。
func TestStreamLiveQueueLatestWinsKeepsNewestValuePerKey(t *testing.T) {
	queue := newBatch3LiveQueue(8)

	queue.publish(batch3ToolProgress("call-1", 10))
	queue.publish(batch3ToolProgress("call-1", 20))
	queue.publish(batch3ToolProgress("call-1", 30))
	queue.publish(batch3ToolProgress("call-2", 7))

	batch := queue.takePending()
	require.Len(t, batch, 2, "两个合并键各一帧")
	assert.Equal(t, "call-1", batch[0].Payload["tool_call_id"])
	assert.Equal(t, 30.0, batch[0].Payload["percent"], "同键只保留最新值")
	assert.Equal(t, 3, batch[0].Payload[streamCoalesceCountKey])
	assert.Equal(t, "call-2", batch[1].Payload["tool_call_id"])
	assert.NotContains(t, batch[1].Payload, streamCoalesceCountKey, "未发生合并的帧不带合并计数")

	mergedDelta, droppedDelta, droppedTotal := queue.statsSinceLastReport()
	assert.Equal(t, uint64(2), mergedDelta)
	assert.Equal(t, uint64(0), droppedDelta, "latest-wins 路径不应计丢弃")
	assert.Equal(t, uint64(0), droppedTotal)
	assert.Nil(t, queue.takePending(), "取走后队列必须为空")
}

// TestStreamLiveQueueDropsBeyondKeyLimitAreObservable 覆盖内存上界：键数超限时丢弃
// 新键，且丢弃同时进入连接级指标与 runtime_event_delivery 快照（§7 验收：丢弃必须可观测）。
func TestStreamLiveQueueDropsBeyondKeyLimitAreObservable(t *testing.T) {
	resetRuntimeEventStreamMetricsForTest()
	resetRuntimeEventDeliveryCountersForTest()
	defer func() {
		resetRuntimeEventStreamMetricsForTest()
		resetRuntimeEventDeliveryCountersForTest()
	}()

	queue := newBatch3LiveQueue(1)
	queue.publish(batch3ToolProgress("call-1", 10))
	queue.publish(batch3ToolProgress("call-2", 20))

	_, droppedDelta, droppedTotal := queue.statsSinceLastReport()
	assert.Equal(t, uint64(1), droppedDelta)
	assert.Equal(t, uint64(1), droppedTotal)

	stream, ok := SnapshotRuntimeEventDelivery()["stream"].(map[string]interface{})
	require.True(t, ok, "快照必须带 stream 子对象")
	assert.Equal(t, uint64(1), stream["dropped_live"])
}

// TestStreamSessionRuntimeEventsTailCoalesceAndRetryHint 覆盖连接参数联动（真 TCP）：
// tail=3 只回放尾窗三条、coalesce=1 折叠成一帧、retry_ms 下发 SSE `retry:` 提示帧，
// 连接级指标同时被记到（frames/bytes/dump/retry/coalesced）。
func TestStreamSessionRuntimeEventsTailCoalesceAndRetryHint(t *testing.T) {
	resetRuntimeEventStreamMetricsForTest()
	defer resetRuntimeEventStreamMetricsForTest()

	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	runtimeStore := chat.NewInMemoryRuntimeStore(64)
	handler.sessionRuntimeStore = runtimeStore
	handler.sessionEventStore = runtimeStore

	for index, text := range []string{"a", "b", "c", "d", "e"} {
		_, err := runtimeStore.AppendEvent(context.Background(), batch3Delta(text))
		require.NoErrorf(t, err, "造第 %d 条增量失败", index+1)
	}

	router := mux.NewRouter()
	router.HandleFunc("/api/runtime/sessions/{id}/runtime/stream", handler.StreamSessionRuntimeEvents).Methods(http.MethodGet)
	server := httptest.NewServer(router)
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	streamURL := server.URL + "/api/runtime/sessions/" + batch3SessionID + "/runtime/stream?tail=3&coalesce=1&retry_ms=2500&poll_ms=5000"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, streamURL, nil)
	require.NoError(t, err)
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	body := readBatch3StreamUntil(t, resp.Body, func(seen string) bool {
		return strings.Contains(seen, "retry: 2500") && strings.Contains(seen, `"coalesced_count":3`)
	})

	assert.Contains(t, body, ": open")
	assert.Contains(t, body, "retry: 2500")
	assert.Contains(t, body, `"delta":"cde"`, "尾窗三条必须折叠成一帧")
	assert.Contains(t, body, `"coalesced_from":3`)
	assert.NotContains(t, body, `"delta":"ab"`, "tail=3 不得回放更早的事件")

	metrics := runtimeEventStreamMetricsSnapshot()
	assert.GreaterOrEqual(t, metrics["frames_sent"].(uint64), uint64(1))
	assert.Greater(t, metrics["bytes_sent"].(uint64), uint64(0))
	assert.GreaterOrEqual(t, metrics["dump_pages"].(uint64), uint64(1))
	assert.GreaterOrEqual(t, metrics["retry_count"].(uint64), uint64(1), "下发 retry 帧必须计数")
	assert.GreaterOrEqual(t, metrics["coalesced_frames"].(uint64), uint64(1))
	assert.GreaterOrEqual(t, metrics["coalesced_rows"].(uint64), uint64(2))

	cancel()
	require.Eventually(t, func() bool {
		return runtimeEventStreamMetricsSnapshot()["active_connections"].(int64) == 0
	}, 3*time.Second, 10*time.Millisecond, "断连后 active_connections 必须回落")
}

// TestStreamSessionRuntimeEventsBatch3ParameterValidation 覆盖连接参数校验：
// tail 与 after 互斥、越界与非法值一律 400（而不是静默忽略后走旧路径）。
func TestStreamSessionRuntimeEventsBatch3ParameterValidation(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	runtimeStore := chat.NewInMemoryRuntimeStore(8)
	handler.sessionRuntimeStore = runtimeStore
	handler.sessionEventStore = runtimeStore

	router := mux.NewRouter()
	router.HandleFunc("/api/runtime/sessions/{id}/runtime/stream", handler.StreamSessionRuntimeEvents).Methods(http.MethodGet)

	cases := []struct {
		name  string
		query string
	}{
		{"tail 与 after 互斥", "tail=10&after=3"},
		{"tail 越界", fmt.Sprintf("tail=%d", streamTailMax+1)},
		{"tail 非数字", "tail=abc"},
		{"tail 为 0", "tail=0"},
		{"retry_ms 过小", "retry_ms=1"},
		{"retry_ms 过大", "retry_ms=60001"},
		{"keepalive_ms 过小", "keepalive_ms=10"},
		{"keepalive_ms 过大", "keepalive_ms=300001"},
		{"flush_ms 非法", "flush_ms=abc"},
		{"flush_ms 过大", "flush_ms=1001"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet,
				"/api/runtime/sessions/"+batch3SessionID+"/runtime/stream?"+tc.query, nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			assert.Equalf(t, http.StatusBadRequest, rec.Code, "响应体：%s", rec.Body.String())
		})
	}
}

// newBatch3LiveQueue 造一个不启动投递协程的队列，让合并断言完全确定（无消费者竞态）。
func newBatch3LiveQueue(limit int) *streamLiveQueue {
	return &streamLiveQueue{
		out:     make(chan runtimeevents.Event, 4),
		notify:  make(chan struct{}, 1),
		stop:    make(chan struct{}),
		pending: map[string]runtimeevents.Event{},
		limit:   limit,
	}
}

// batch3ToolProgress 造一条 live-only 工具进度帧（携带 tool_call_id 合并键）。
func batch3ToolProgress(toolCallID string, percent float64) runtimeevents.Event {
	return runtimeevents.Event{
		Type:      "tool.progress",
		SessionID: batch3SessionID,
		Payload: map[string]interface{}{
			"tool_call_id": toolCallID,
			"percent":      percent,
		},
	}
}

// readBatch3StreamUntil 以谓词为终点从 SSE 流里读字节：读循环在独立 goroutine，
// 断言在主 goroutine，避免 ResponseRecorder 式的读写竞态。
func readBatch3StreamUntil(t *testing.T, body io.Reader, predicate func(string) bool) string {
	t.Helper()
	type outcome struct {
		text string
		err  error
	}
	results := make(chan outcome, 1)
	go func() {
		buf := make([]byte, 512)
		var seen strings.Builder
		for !predicate(seen.String()) {
			n, readErr := body.Read(buf)
			if n > 0 {
				seen.Write(buf[:n])
			}
			if readErr != nil {
				results <- outcome{text: seen.String(), err: readErr}
				return
			}
		}
		results <- outcome{text: seen.String()}
	}()

	select {
	case result := <-results:
		require.Truef(t, predicate(result.text), "SSE 流未满足预期即结束（err=%v）：%s", result.err, result.text)
		return result.text
	case <-time.After(5 * time.Second):
		t.Fatal("5s 内未收到预期的 SSE 帧")
		return ""
	}
}
