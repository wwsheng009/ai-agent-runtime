package usageanalytics

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// TestTurnToolStatsTracksFailureStreakAndLastSuccess 钉住 §3.1 的进程内累计：
// 失败累加、成功清零，flush 给出回合内最长连续失败与最后一次成功工具名。
func TestTurnToolStatsTracksFailureStreakAndLastSuccess(t *testing.T) {
	stats := newTurnToolStats()
	stats.record("read_file", true)
	stats.record("apply_patch", false)
	stats.record("apply_patch", false)
	stats.record("apply_patch", false)
	stats.record("apply_patch", true) // 恢复：清零连续计数
	stats.record("shell", false)
	stats.record("shell", false)
	stats.record("read_file", true)

	failed, recovered, unrecovered, streak, lastSuccess := stats.flush()
	if failed != 5 || recovered != 1 || unrecovered != 1 {
		t.Fatalf("failed/recovered/unrecovered=%d/%d/%d want 5/1/1", failed, recovered, unrecovered)
	}
	if streak != 3 {
		t.Fatalf("最长连续失败=%d want 3（成功清零后重开一段 2 次）", streak)
	}
	if lastSuccess != "read_file" {
		t.Fatalf("最后成功工具=%q want read_file", lastSuccess)
	}
}

// TestSessionUsageCarriesToolFailureCascade 是 §3.1 的端到端回归：级联经
// 事件采集 → usage_turns 持久化 → 会话明细 / rollup 呈现。
func TestSessionUsageCarriesToolFailureCascade(t *testing.T) {
	store, err := Open(Config{Path: filepath.Join(t.TempDir(), "usage_analytics.sqlite")})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = store.Close() }()

	bus := runtimeevents.NewBus()
	collector := newCollector(store, nil, nil)
	collector.subscribe(bus)
	defer collector.close()

	started := time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)
	publish := func(eventType string, payload map[string]interface{}, at time.Time) {
		bus.Publish(runtimeevents.Event{Type: eventType, SessionID: "session-cascade", Payload: payload, Timestamp: at})
	}
	publish(EventSessionStart, map[string]interface{}{
		"session_id": "session-cascade",
		"turn_id":    "turn-1",
		"trace_id":   "trace-1",
	}, started)
	// 一条 LLM 请求，使 buildTurns 能派生 turn-1（usage_turns 的级联列由它
	// 按 turn_id 关联）。
	publish(EventLLMRequestStarted, map[string]interface{}{
		"llm_request_id": "req-1", "trace_id": "trace-1", "turn_id": "turn-1", "step": 1,
		"provider": "test-provider", "model": "test-model",
	}, started.Add(500*time.Millisecond))
	publish(EventLLMRequestFinished, map[string]interface{}{
		"llm_request_id": "req-1", "trace_id": "trace-1", "turn_id": "turn-1", "step": 1,
		"success": true, "duration_ms": 120,
	}, started.Add(1500*time.Millisecond))

	step := 0
	toolResult := func(name string, ok bool, at time.Time) {
		step++
		callID := fmt.Sprintf("call-%d", step)
		publish(EventToolRequested, map[string]interface{}{
			"tool_call_id": callID,
			"logical_tool": name,
			"step":         step,
			"trace_id":     "trace-1",
			"turn_id":      "turn-1",
		}, at)
		payload := map[string]interface{}{
			"tool_call_id": callID,
			"logical_tool": name,
			"step":         step,
			"trace_id":     "trace-1",
			"turn_id":      "turn-1",
			"ok":           ok,
			"duration_ms":  10,
		}
		if ok {
			payload["outcome"] = "success"
		} else {
			payload["outcome"] = "failed"
			payload["error_code"] = "TOOL_TIMEOUT"
		}
		publish(EventToolCompleted, payload, at.Add(time.Second))
	}

	base := started.Add(time.Minute)
	toolResult("read_file", true, base)
	toolResult("apply_patch", false, base.Add(2*time.Second))
	toolResult("apply_patch", false, base.Add(4*time.Second))
	toolResult("apply_patch", false, base.Add(6*time.Second))
	toolResult("shell", true, base.Add(8*time.Second))
	publish(EventSessionEnd, map[string]interface{}{
		"session_id": "session-cascade",
		"turn_id":    "turn-1",
		"success":    true,
		"duration":   9000,
	}, base.Add(10*time.Second))

	detail, err := store.SessionUsage("session-cascade")
	if err != nil {
		t.Fatalf("SessionUsage: %v", err)
	}
	if len(detail.Turns) != 1 {
		t.Fatalf("回合数应为 1，实际 %d", len(detail.Turns))
	}
	turn := detail.Turns[0]
	if turn.ToolFailureStreak != 3 {
		t.Fatalf("回合最长连续失败=%d want 3", turn.ToolFailureStreak)
	}
	if turn.LastToolSuccess != "shell" {
		t.Fatalf("最后成功工具=%q want shell", turn.LastToolSuccess)
	}
	if detail.Session.MaxToolFailureStreak != 3 {
		t.Fatalf("rollup 最长连续失败=%d want 3", detail.Session.MaxToolFailureStreak)
	}
}
