package runtimeapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

// TestTrajectoryEmitterWriteAmplificationBaseline 固化 Batch 1 的写入放大基线：
// 模拟一轮典型流式回合（reasoning + N 个 chunk + 同源 observation + tool_end +
// done（内嵌 result）+ result），断言去重名单与「落盘字节 / wire 字节」占比。
//
// 该测试是「构造性双写」回归护栏：若未来重新落盘 chunk/reasoning/同源
// observation，或取消 done.result 裁剪，占比会回升、断言失败。
func TestTrajectoryEmitterWriteAmplificationBaseline(t *testing.T) {
	store := chat.NewInMemoryRuntimeStore(64)
	handler := &Handler{sessionEventStore: store}

	rec := httptest.NewRecorder()
	emitter := handler.newTrajectoryEmitter(rec, &chat.Session{ID: "sess-amplification"})

	bigResult := strings.Repeat("r", 4096)
	emitter.Emit("reasoning", map[string]interface{}{"content": strings.Repeat("t", 512)})
	for i := 0; i < 12; i++ {
		emitter.Emit("chunk", map[string]interface{}{"type": "text", "content": strings.Repeat("c", 256)})
	}
	emitter.Emit("observation", map[string]interface{}{
		"tool":    "execute_shell_command",
		"step":    "1",
		"content": strings.Repeat("o", 512),
	})
	emitter.Emit("tool_end", map[string]interface{}{
		"tool":   "execute_shell_command",
		"status": "completed",
		"output": strings.Repeat("e", 256),
	})
	emitter.Emit("done", map[string]interface{}{
		"status":  "completed",
		"content": "final",
		"result":  map[string]interface{}{"content": bigResult},
	})
	emitter.Emit("result", map[string]interface{}{"content": bigResult})

	wireBytes := int64(len(rec.Body.Bytes()))
	require.NotZero(t, wireBytes)
	wireFrames := parseSSETestFrames(t, rec.Body.String())
	require.NotEmpty(t, wireFrames)

	events, err := store.ListEvents(context.Background(), "sess-amplification", 0, 0)
	require.NoError(t, err)
	require.NotEmpty(t, events)

	storedBytes := int64(0)
	storedTypes := make([]string, 0, len(events))
	for _, event := range events {
		encoded, err := json.Marshal(event.Payload)
		require.NoError(t, err)
		storedBytes += int64(len(encoded))
		storedTypes = append(storedTypes, event.Type)
	}

	// 去重名单：chunk/reasoning/同源 observation 一律不落盘。
	assert.NotContains(t, storedTypes, chatSSEStreamEventPrefix+"chunk")
	assert.NotContains(t, storedTypes, chatSSEStreamEventPrefix+"reasoning")
	assert.NotContains(t, storedTypes, chatSSEStreamEventPrefix+"observation")
	// 回放内容源与工具事件仍落盘。
	assert.Contains(t, storedTypes, chatSSEStreamEventPrefix+"tool_end")
	assert.Contains(t, storedTypes, chatSSEStreamEventPrefix+"done")
	assert.Contains(t, storedTypes, chatSSEStreamEventPrefix+"result")

	// 写入放大护栏：落盘字节应显著低于 wire 字节（本用例约 1/3）。
	ratio := float64(storedBytes) / float64(wireBytes)
	assert.Lessf(t, ratio, 0.55, "stored/wire byte ratio %.2f exceeds Batch 1 baseline (>=45%% reduction)", ratio)
	// 行数护栏（方案 §7）：同一 mock 回合落盘行数相对 wire 帧数下降 >=50%。
	assert.LessOrEqualf(t, len(events)*2, len(wireFrames),
		"stored rows %d must be <= half of wire frames %d", len(events), len(wireFrames))
	t.Logf("wire=%dB/%dframes stored=%dB/%drows ratio=%.2f",
		wireBytes, len(wireFrames), storedBytes, len(events), ratio)
}
