package commands

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// P4（2026-10-14）：journal 落盘侧的 delta 合并。三条契约：
//  1. 长流（累计 ≥ 阈值）把同流多个 append 增量合成一行，文本拼接无损，
//     别名键（text/content/delta）统一为拼接文本，保留末条 sequence；
//  2. 小流（< 地板值）保持逐条原样落盘，行为与旧实现逐字节一致；
//  3. 快照（mode=replace）与注入记录写入前先按原序落盘缓冲，保证全序。
//
// 断言经 eventLogSinkOverride 在进程内截获行流：进程级 journal 写者由并行
// 测试共享，文件级断言会被其它测试干扰（2026-10-14 整包复跑实测抖动）。

func deltaMergeTestEvent(seq int, text string) runtimeevents.Event {
	return runtimeevents.Event{
		Type:      "assistant.reasoning",
		SessionID: "sess-delta-merge",
		TraceID:   "trace-delta-merge",
		Payload: map[string]interface{}{
			"mode":           "append",
			"step":           1,
			"sequence":       seq,
			"stream_id":      "stream-delta-merge",
			"llm_request_id": "llm-delta-merge",
			"turn_id":        "turn-delta-merge",
			"text":           text,
			"content":        text,
			"delta":          text,
		},
	}
}

func newDeltaMergeCaptureBridge() (*chatRuntimeEventBridge, *[][]byte) {
	bridge := newChatRuntimeEventBridge(&ChatSession{})
	lines := &[][]byte{}
	bridge.eventLogSinkOverride = func(line []byte) {
		*lines = append(*lines, append([]byte(nil), line...))
	}
	return bridge, lines
}

func decodeDeltaMergeLine(t *testing.T, line []byte) runtimeevents.Event {
	t.Helper()
	var decoded struct {
		Type    string                 `json:"type"`
		Payload map[string]interface{} `json:"payload"`
	}
	require.NoError(t, json.Unmarshal(line, &decoded))
	return runtimeevents.Event{Type: decoded.Type, Payload: decoded.Payload}
}

func TestChatEventLogDeltaMergeCompactsLongStream(t *testing.T) {
	bridge, lines := newDeltaMergeCaptureBridge()

	const chunk = "0123456789" // 10B
	const count = 200          // 2000B，远大于 256B 阈值
	for i := 1; i <= count; i++ {
		bridge.appendEventLog(deltaMergeTestEvent(i, chunk))
	}
	// 阈值合并已发生：26 条/行 × 7 行 = 182 条，尾段 18 条留在缓冲。
	merged, pending := bridge.eventLogDeltaMergeStats()
	require.Equal(t, 1, pending, "未达阈值的尾段应当留在缓冲")
	require.Equal(t, uint64(175), merged)

	bridge.flushStreamDeltaLog() // 尾段（180B ≥ 地板）合成第 8 行
	mergedAfterFlush, pendingAfterFlush := bridge.eventLogDeltaMergeStats()
	require.Zero(t, pendingAfterFlush)
	require.Equal(t, uint64(192), mergedAfterFlush)

	require.Len(t, *lines, 8, "200 条增量应当被合并为 8 行")
	var got strings.Builder
	mergedRows := 0
	lastSeq := 0
	for _, line := range *lines {
		event := decodeDeltaMergeLine(t, line)
		require.Equal(t, "assistant.reasoning", event.Type)
		require.Equal(t, "append", event.Payload["mode"])
		text, _ := event.Payload["text"].(string)
		got.WriteString(text)
		if _, ok := event.Payload["merged_chunks"]; ok {
			mergedRows++
		}
		if seq, ok := event.Payload["sequence"].(float64); ok {
			lastSeq = int(seq)
		}
		// P1-4：合并行只保留规范文本键（reasoning→text），正文只落一份。
		require.NotContains(t, event.Payload, "delta")
		require.NotContains(t, event.Payload, "content")
		require.Equal(t, text, streamEventText(event))
	}
	require.Equal(t, strings.Repeat(chunk, count), got.String(), "合并后的重放文本必须与原始拼接一致")
	require.Equal(t, 8, mergedRows)
	require.Equal(t, count, lastSeq, "末行保留末条 sequence")
}

func TestChatEventLogDeltaMergeSmallStreamKeepsRawLines(t *testing.T) {
	bridge, lines := newDeltaMergeCaptureBridge()

	for i := 1; i <= 3; i++ {
		bridge.appendEventLog(deltaMergeTestEvent(i, "short"))
	}
	bridge.submitUserInput("hello") // 同步注入：flush 缓冲并按原序落盘

	require.Len(t, *lines, 4, "小流必须保持逐条落盘（3 增量 + 1 注入）")
	require.Contains(t, string((*lines)[0]), `"sequence":1`)
	require.Contains(t, string((*lines)[1]), `"sequence":2`)
	require.Contains(t, string((*lines)[2]), `"sequence":3`)
	require.Contains(t, string((*lines)[3]), `"user_input":"hello"`)
	for i := 0; i < 3; i++ {
		require.NotContains(t, string((*lines)[i]), "merged_chunks")
		// P1-4：小流的原始行同样只保留规范键（reasoning→text）。
		require.Contains(t, string((*lines)[i]), `"text":"short"`)
		require.NotContains(t, string((*lines)[i]), `"content"`)
		require.NotContains(t, string((*lines)[i]), `"delta"`)
	}
	merged, pending := bridge.eventLogDeltaMergeStats()
	require.Zero(t, merged)
	require.Zero(t, pending)
}

func TestChatEventLogDeltaMergeFlushesBeforeSnapshot(t *testing.T) {
	bridge, lines := newDeltaMergeCaptureBridge()

	bridge.appendEventLog(deltaMergeTestEvent(1, "abc")) // 单条缓冲
	replace := deltaMergeTestEvent(2, "")
	replace.Payload["mode"] = "replace"
	replace.Payload["text"] = "abc"
	bridge.appendEventLog(replace)

	require.Len(t, *lines, 2)
	require.Contains(t, string((*lines)[0]), `"mode":"append"`)
	require.Contains(t, string((*lines)[1]), `"mode":"replace"`)
}
