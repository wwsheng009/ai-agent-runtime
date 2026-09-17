package usageanalytics

import (
	"path/filepath"
	"testing"
	"time"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
)

// seedV2Fixture 写入一组确定性的 schema v2 事件（工具失败→重试成功、子代理失败/成功）。
func seedV2Fixture(t *testing.T, store *Store, sessionID string) {
	t.Helper()
	bus := runtimeevents.NewBus()
	collector := newCollector(store, nil, nil)
	collector.subscribe(bus)
	defer collector.close()

	started := time.Date(2026, 9, 17, 8, 0, 0, 0, time.UTC)
	publish := func(eventType string, payload map[string]interface{}, at time.Time) {
		bus.Publish(runtimeevents.Event{Type: eventType, SessionID: sessionID, Payload: payload, Timestamp: at})
	}

	publish(EventSessionStart, map[string]interface{}{"session_id": sessionID, "turn_id": "turn-1"}, started)
	// 一条 LLM 请求，使 buildTurns 能派生 turn-1（usage_turns 的列由它按 turn_id 关联）。
	publish(EventLLMRequestStarted, map[string]interface{}{
		"llm_request_id": "req-1", "trace_id": "trace-1", "turn_id": "turn-1", "step": 1,
		"provider": "test-provider", "model": "test-model",
	}, started.Add(500*time.Millisecond))
	publish(EventLLMRequestFinished, map[string]interface{}{
		"llm_request_id": "req-1", "trace_id": "trace-1", "turn_id": "turn-1", "step": 1,
		"success": true, "duration_ms": 120,
	}, started.Add(1500*time.Millisecond))
	// 工具：失败 → 成功（恢复）；再一条确定性失败。
	publish(EventToolRequested, map[string]interface{}{"tool_call_id": "c-1", "logical_tool": "view", "turn_id": "turn-1", "step": 1}, started.Add(time.Second))
	publish(EventToolCompleted, map[string]interface{}{
		"tool_call_id": "c-1", "logical_tool": "view", "turn_id": "turn-1", "step": 1,
		"ok": false, "outcome": "failed", "error_code": "TOOL_TIMEOUT", "retryable": true, "duration_ms": 100,
	}, started.Add(2*time.Second))
	publish(EventToolRequested, map[string]interface{}{"tool_call_id": "c-2", "logical_tool": "view", "turn_id": "turn-1", "step": 2}, started.Add(3*time.Second))
	publish(EventToolCompleted, map[string]interface{}{
		"tool_call_id": "c-2", "logical_tool": "view", "turn_id": "turn-1", "step": 2,
		"ok": true, "outcome": "success", "duration_ms": 300,
	}, started.Add(4*time.Second))
	publish(EventToolRequested, map[string]interface{}{"tool_call_id": "c-3", "logical_tool": "shell", "turn_id": "turn-1", "step": 3}, started.Add(5*time.Second))
	publish(EventToolCompleted, map[string]interface{}{
		"tool_call_id": "c-3", "logical_tool": "shell", "turn_id": "turn-1", "step": 3,
		"ok": false, "outcome": "failed", "error_code": "TOOL_PERMISSION_DENIED", "retryable": false, "duration_ms": 50,
	}, started.Add(6*time.Second))

	// 子代理：一个失败（rate_limited），一个成功。
	publish(EventSubagentCompleted, map[string]interface{}{
		"subagent_id": "sa-1", "parent_session_id": sessionID, "role": "researcher", "read_only": true,
		"success": false, "error_code": "UPSTREAM_RATE_LIMITED", "attempt": 2, "max_attempts": 2, "source": "scheduler",
	}, started.Add(7*time.Second))
	publish(EventSubagentCompleted, map[string]interface{}{
		"subagent_id": "sa-2", "parent_session_id": sessionID, "role": "writer",
		"success": true, "attempt": 1, "max_attempts": 1, "usage_total_tokens": 77, "source": "scheduler",
	}, started.Add(8*time.Second))

	publish(EventSessionEnd, map[string]interface{}{
		"turn_id": "turn-1", "success": true, "steps": 3, "duration": int64(9000),
		"usage_total_tokens": int64(200),
	}, started.Add(9*time.Second))
}

// TestStoreV2StatsQueries 锁定方案 §4 批次 1.3：工具/子代理/失败模式查询输出。
func TestStoreV2StatsQueries(t *testing.T) {
	store, err := Open(Config{Path: filepath.Join(t.TempDir(), "usage_analytics.sqlite")})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = store.Close() }()
	seedV2Fixture(t, store, "session-v2-stats")

	tools, err := store.ToolStats(ToolStatsQuery{SessionID: "session-v2-stats"})
	if err != nil {
		t.Fatalf("ToolStats: %v", err)
	}
	if len(tools.Tools) != 2 {
		t.Fatalf("期望 2 个工具聚合行，实际 %d: %+v", len(tools.Tools), tools.Tools)
	}
	byName := map[string]ToolStat{}
	for _, stat := range tools.Tools {
		byName[stat.ToolName] = stat
	}
	view := byName["view"]
	// 百分位和错误 Top-N 现延迟至 ToolStatsDetail（不参与首屏聚合）。
	if view.Calls != 2 || view.Failures != 1 {
		t.Fatalf("view 聚合不符: %+v", view)
	}
	if view.FailureRate < 0.49 || view.FailureRate > 0.51 {
		t.Fatalf("view 失败率应约 0.5，实际 %v", view.FailureRate)
	}
	// 错误 Top-N 现延迟至 ToolStatsDetail（不参与首屏聚合）。
	if tools.Totals.Calls != 3 || tools.Totals.Failures != 2 {
		t.Fatalf("全局工具合计不符: %+v", tools.Totals)
	}

	subagents, err := store.SubagentStats(SubagentStatsQuery{SessionID: "session-v2-stats"})
	if err != nil {
		t.Fatalf("SubagentStats: %v", err)
	}
	if subagents.Summary.Total != 2 || subagents.Summary.Failed != 1 || subagents.Summary.Succeeded != 1 {
		t.Fatalf("子代理汇总不符: %+v", subagents.Summary)
	}
	if subagents.Summary.FailureCategories[llm.FailureCategoryRateLimited] != 1 {
		t.Fatalf("失败分类分布不符: %+v", subagents.Summary.FailureCategories)
	}
	if subagents.Summary.Sources["scheduler"] != 2 {
		t.Fatalf("来源分布不符: %+v", subagents.Summary.Sources)
	}
	if subagents.Summary.Retried != 1 {
		t.Fatalf("重试计数应为 1: %+v", subagents.Summary)
	}

	failedOnly, err := store.SubagentStats(SubagentStatsQuery{SessionID: "session-v2-stats", FailedOnly: true})
	if err != nil {
		t.Fatalf("SubagentStats(failed_only): %v", err)
	}
	if len(failedOnly.Subagents) != 1 || failedOnly.Subagents[0].SubagentID != "sa-1" {
		t.Fatalf("failed_only 过滤不符: %+v", failedOnly.Subagents)
	}
	if failedOnly.Subagents[0].Success == nil || *failedOnly.Subagents[0].Success {
		t.Fatalf("失败行 success 应为 false：%+v", failedOnly.Subagents[0])
	}

	patterns, err := store.ErrorPatterns(ErrorPatternsQuery{SessionID: "session-v2-stats", Top: 10})
	if err != nil {
		t.Fatalf("ErrorPatterns: %v", err)
	}
	sources := map[string]bool{}
	for _, pattern := range patterns.Patterns {
		sources[pattern.Source] = true
	}
	if !sources["tools"] || !sources["subagents"] {
		t.Fatalf("失败模式应覆盖 tools 与 subagents：%+v", patterns.Patterns)
	}

	empty, err := store.ToolStats(ToolStatsQuery{SessionID: "no-such-session"})
	if err != nil {
		t.Fatalf("空库过滤不得报错: %v", err)
	}
	if len(empty.Tools) != 0 {
		t.Fatalf("空结果应为空数组，实际 %+v", empty.Tools)
	}
}

// TestSessionUsageCarriesV2Dimensions 锁定批次 1.3 的 rollup/诊断扩展：
// 单会话明细包含工具/子代理字段、重试恢复计数与新增诊断码。
func TestSessionUsageCarriesV2Dimensions(t *testing.T) {
	store, err := Open(Config{Path: filepath.Join(t.TempDir(), "usage_analytics.sqlite")})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = store.Close() }()
	seedV2Fixture(t, store, "session-v2-detail")

	detail, err := store.SessionUsage("session-v2-detail")
	if err != nil {
		t.Fatalf("SessionUsage: %v", err)
	}
	rollup := detail.Session
	if rollup.ToolCallsObserved != 3 || rollup.ToolFailures != 2 {
		t.Fatalf("会话 rollup 工具维度不符: %+v", rollup)
	}
	if rollup.SubagentRuns != 2 || rollup.SubagentFailures != 1 {
		t.Fatalf("会话 rollup 子代理维度不符: %+v", rollup)
	}
	if rollup.RetryRecoveredTurns != 1 {
		t.Fatalf("重试恢复回合数应为 1，实际 %d", rollup.RetryRecoveredTurns)
	}
	if len(detail.Tools) != 2 || len(detail.Subagents) != 2 {
		t.Fatalf("明细应包含工具/子代理行：tools=%d subagents=%d", len(detail.Tools), len(detail.Subagents))
	}
	if len(detail.Turns) != 1 {
		t.Fatalf("回合数应为 1，实际 %d", len(detail.Turns))
	}
	turn := detail.Turns[0]
	if turn.ToolErrors != 2 || turn.RecoveredToolErrors != 1 || turn.UnrecoveredToolErrors != 1 {
		t.Fatalf("回合工具失败/恢复计数不符: %+v", turn)
	}

	codes := map[string]bool{}
	for _, diagnostic := range detail.Diagnostics {
		codes[diagnostic.Code] = true
	}
	for _, want := range []string{"tool_failures", "subagent_failures", "retry_recovered"} {
		if !codes[want] {
			t.Fatalf("缺少诊断码 %s：%+v", want, detail.Diagnostics)
		}
	}
}
