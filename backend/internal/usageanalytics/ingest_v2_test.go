package usageanalytics

import (
	"path/filepath"
	"testing"
	"time"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// TestCollectorIngestsSchemaV2Events 锁定方案 §4 批次 1.2：tool.* /
// subagent.completed / session_start / session_end 落三张新表。
func TestCollectorIngestsSchemaV2Events(t *testing.T) {
	store, err := Open(Config{Path: filepath.Join(t.TempDir(), "usage_analytics.sqlite")})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = store.Close() }()

	bus := runtimeevents.NewBus()
	collector := newCollector(store, nil, nil)
	collector.subscribe(bus)
	defer collector.close()

	started := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	publish := func(eventType string, payload map[string]interface{}, at time.Time) {
		bus.Publish(runtimeevents.Event{
			Type:      eventType,
			SessionID: "session-v2",
			Payload:   payload,
			Timestamp: at,
		})
	}

	publish(EventSessionStart, map[string]interface{}{
		"session_id": "session-v2",
		"turn_id":    "turn-1",
		"trace_id":   "trace-1",
	}, started)

	publish(EventToolRequested, map[string]interface{}{
		"tool_call_id": "call-1",
		"logical_tool": "shell",
		"step":         1,
		"trace_id":     "trace-1",
		"turn_id":      "turn-1",
	}, started.Add(time.Second))
	publish(EventToolCompleted, map[string]interface{}{
		"tool_call_id": "call-1",
		"logical_tool": "shell",
		"step":         1,
		"trace_id":     "trace-1",
		"turn_id":      "turn-1",
		"ok":           false,
		"outcome":      "failed",
		"error_code":   "TOOL_TIMEOUT",
		"retryable":    true,
		"duration_ms":  120,
	}, started.Add(2*time.Second))

	// 同名工具失败后重试成功 → 该回合应计 recovered_tool_error_count=1。
	publish(EventToolRequested, map[string]interface{}{
		"tool_call_id": "call-2",
		"logical_tool": "shell",
		"step":         2,
		"trace_id":     "trace-1",
		"turn_id":      "turn-1",
	}, started.Add(3*time.Second))
	publish(EventToolCompleted, map[string]interface{}{
		"tool_call_id": "call-2",
		"logical_tool": "shell",
		"step":         2,
		"trace_id":     "trace-1",
		"turn_id":      "turn-1",
		"ok":           true,
		"outcome":      "success",
		"empty_result": true,
		"duration_ms":  30,
	}, started.Add(4*time.Second))

	// 生产者①（scheduler 风格）：success=false + error_code。
	publish(EventSubagentCompleted, map[string]interface{}{
		"agent_id":          "child-1",
		"session_id":        "child-1",
		"parent_session_id": "session-v2",
		"role":              "researcher",
		"success":           false,
		"error_code":        "UPSTREAM_RATE_LIMITED",
		"attempt":           1,
		"duration_ms":       500,
	}, started.Add(5*time.Second))

	// 生产者②（agent_controller 风格）：同一子代理仅 status，按 §5.2 合并为一行。
	publish(EventSubagentCompleted, map[string]interface{}{
		"agent_id":           "child-1",
		"parent_session_id":  "session-v2",
		"status":             "idle",
		"control_action":     "close",
		"attempt":            2,
		"max_attempts":       2,
		"duration_ms":        800,
		"usage_total_tokens": 42,
	}, started.Add(6*time.Second))

	publish(EventSessionEnd, map[string]interface{}{
		"turn_id":                 "turn-1",
		"trace_id":                "trace-1",
		"success":                 true,
		"steps":                   3,
		"duration":                int64(1500),
		"usage_prompt_tokens":     int64(100),
		"usage_completion_tokens": int64(20),
		"usage_total_tokens":      int64(120),
	}, started.Add(7*time.Second))

	// --- usage_tool_calls ---
	assertRowCount(t, store, `SELECT COUNT(*) FROM usage_tool_calls`, 2)
	failed := queryRow(t, store, `SELECT outcome, ok, empty_result, error_code, retryable, duration_ms FROM usage_tool_calls WHERE tool_call_id='call-1'`)
	if failed[0] != "failed" || failed[1] != int64(0) || failed[2] != int64(0) || failed[3] != "TOOL_TIMEOUT" || failed[4] != int64(1) || failed[5] != int64(120) {
		t.Fatalf("失败工具行不符: %v", failed)
	}
	succeeded := queryRow(t, store, `SELECT outcome, ok, empty_result FROM usage_tool_calls WHERE tool_call_id='call-2'`)
	if succeeded[0] != "success" || succeeded[1] != int64(1) || succeeded[2] != int64(1) {
		t.Fatalf("成功工具行不符: %v", succeeded)
	}

	// --- usage_subagents（幂等合并成一行）---
	assertRowCount(t, store, `SELECT COUNT(*) FROM usage_subagents`, 1)
	subagent := queryRow(t, store, `SELECT success, completion_reason, failure_category, attempt, max_attempts, usage_total_tokens, source FROM usage_subagents WHERE subagent_id='child-1'`)
	if subagent[0] != int64(1) {
		t.Fatalf("后写入的 status=idle 应合并为 success=1，实际 %v", subagent)
	}
	if subagent[2] != "" {
		t.Fatalf("成功后 failure_category 应为空，实际 %v", subagent[2])
	}
	if subagent[3] != int64(2) || subagent[4] != int64(2) {
		t.Fatalf("attempt/max_attempts 应取最大值：%v", subagent)
	}
	if subagent[5] != int64(42) || subagent[6] != SubagentSourceAgentController {
		t.Fatalf("usage_total_tokens/source 合并不符: %v", subagent)
	}

	// --- usage_turns ---
	assertRowCount(t, store, `SELECT COUNT(*) FROM usage_turns`, 1)
	turn := queryRow(t, store, `SELECT success, steps, tool_error_count, recovered_tool_error_count, unrecovered_tool_error_count, total_tokens FROM usage_turns WHERE session_id='session-v2' AND turn_id='turn-1'`)
	if turn[0] != int64(1) || turn[1] != int64(3) {
		t.Fatalf("回合终值不符: %v", turn)
	}
	if turn[2] != int64(1) || turn[3] != int64(1) || turn[4] != int64(0) {
		t.Fatalf("工具失败/恢复计数不符: %v", turn)
	}
	if turn[5] != int64(120) {
		t.Fatalf("total_tokens 期望 120，实际 %v", turn[5])
	}
}

// TestToolCompletedWithoutDispositionFallsBackToErrorText 锁定兼容分支：
// 老事件没有 outcome/ok 时按 error 文本判定（不留 NULL 造成"永远在途"）。
func TestToolCompletedWithoutDispositionFallsBackToErrorText(t *testing.T) {
	store, err := Open(Config{Path: filepath.Join(t.TempDir(), "usage_analytics.sqlite")})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = store.Close() }()

	collector := newCollector(store, nil, nil)
	collector.onToolCompleted(runtimeevents.Event{
		Type:      EventToolCompleted,
		SessionID: "session-legacy",
		Payload: map[string]interface{}{
			"tool_call_id": "call-legacy",
			"logical_tool": "view",
			"error":        "path not found",
			"turn_id":      "turn-legacy",
		},
	})
	row := queryRow(t, store, `SELECT outcome, ok FROM usage_tool_calls WHERE tool_call_id='call-legacy'`)
	if row[0] != "failed" || row[1] != int64(0) {
		t.Fatalf("老事件失败工具应落 failed/ok=0，实际 %v", row)
	}
}

func assertRowCount(t *testing.T, store *Store, query string, want int64) {
	t.Helper()
	row := queryRow(t, store, query)
	if row[0] != want {
		t.Fatalf("%s 期望 %d，实际 %d", query, want, row[0])
	}
}

func queryRow(t *testing.T, store *Store, query string) []interface{} {
	t.Helper()
	rows, ok, err := store.query(query)
	if err != nil || !ok {
		t.Fatalf("query %q: ok=%v err=%v", query, ok, err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatalf("query %q 无结果行", query)
	}
	columns, err := rows.Columns()
	if err != nil {
		t.Fatalf("columns: %v", err)
	}
	values := make([]interface{}, len(columns))
	pointers := make([]interface{}, len(columns))
	for i := range values {
		pointers[i] = &values[i]
	}
	if err := rows.Scan(pointers...); err != nil {
		t.Fatalf("scan: %v", err)
	}
	normalized := make([]interface{}, len(values))
	for i, value := range values {
		if raw, isBytes := value.([]byte); isBytes {
			normalized[i] = string(raw)
			continue
		}
		normalized[i] = value
	}
	return normalized
}
