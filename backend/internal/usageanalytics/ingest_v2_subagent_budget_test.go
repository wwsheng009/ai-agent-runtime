package usageanalytics

import (
	"path/filepath"
	"testing"
	"time"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// TestSubagentBudgetOverspendIsVisibleInLedgerAndDiagnostics 是 §4.2 的账本侧
// 回归：子代理任务声明预算后实际用量超预算，必须在 usage_subagents 行、
// SubagentStat 派生标记、会话 rollup 计数与诊断码上直接可见。
func TestSubagentBudgetOverspendIsVisibleInLedgerAndDiagnostics(t *testing.T) {
	store, err := Open(Config{Path: filepath.Join(t.TempDir(), "usage_analytics.sqlite")})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = store.Close() }()

	bus := runtimeevents.NewBus()
	collector := newCollector(store, nil, nil)
	collector.subscribe(bus)
	defer collector.close()

	started := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	// 会话行 + 一条 LLM 请求：SessionUsage/rollup 依赖会话出现在账本里。
	bus.Publish(runtimeevents.Event{
		Type:      EventSessionStart,
		SessionID: "session-budget",
		Payload:   map[string]interface{}{"session_id": "session-budget", "turn_id": "turn-1"},
		Timestamp: started,
	})
	bus.Publish(runtimeevents.Event{
		Type:      EventLLMRequestStarted,
		SessionID: "session-budget",
		Payload: map[string]interface{}{
			"llm_request_id": "req-budget", "turn_id": "turn-1", "step": 1,
			"provider": "test-provider", "model": "test-model",
		},
		Timestamp: started.Add(500 * time.Millisecond),
	})
	bus.Publish(runtimeevents.Event{
		Type:      EventLLMRequestFinished,
		SessionID: "session-budget",
		Payload: map[string]interface{}{
			"llm_request_id": "req-budget", "turn_id": "turn-1", "step": 1,
			"success": true, "duration_ms": 120,
		},
		Timestamp: started.Add(time.Second),
	})
	publish := func(payload map[string]interface{}, at time.Time) {
		bus.Publish(runtimeevents.Event{
			Type:      EventSubagentCompleted,
			SessionID: "session-budget",
			Payload:   payload,
			Timestamp: at,
		})
	}

	// 声明预算 1M 的子代理实际烧掉 6.14M（"6.14M tokens / 32 分钟"事故形态）。
	publish(map[string]interface{}{
		"subagent_id":        "child-budget",
		"parent_session_id":  "session-budget",
		"child_session_id":   "child-budget",
		"role":               "researcher",
		"task_type":          "research",
		"success":            true,
		"completion_reason":  "completed",
		"budget_tokens":      int64(1_000_000),
		"usage_total_tokens": int64(6_140_000),
		"duration_ms":        int64(32 * 60 * 1000),
	}, started)
	// 未声明预算（0 = 不限）的子代理不参与超支判定。
	publish(map[string]interface{}{
		"subagent_id":        "child-nobudget",
		"parent_session_id":  "session-budget",
		"role":               "writer",
		"success":            true,
		"completion_reason":  "completed",
		"usage_total_tokens": int64(9_000_000),
	}, started.Add(time.Second))

	detail, err := store.SessionUsage("session-budget")
	if err != nil {
		t.Fatalf("SessionUsage: %v", err)
	}
	if detail.Session.SubagentBudgetExceeded != 1 {
		t.Fatalf("rollup 超预算子代理数=%d want 1", detail.Session.SubagentBudgetExceeded)
	}
	sawDiagnostic := false
	for _, diagnostic := range detail.Diagnostics {
		if diagnostic.Code == "subagent_budget_exceeded" {
			sawDiagnostic = true
			if diagnostic.Severity != "warning" || diagnostic.Count != 1 {
				t.Fatalf("预算超支诊断不符: %+v", diagnostic)
			}
		}
	}
	if !sawDiagnostic {
		t.Fatalf("缺少 subagent_budget_exceeded 诊断: %+v", detail.Diagnostics)
	}

	stats, err := store.SubagentStats(SubagentStatsQuery{SessionID: "session-budget"})
	if err != nil {
		t.Fatalf("SubagentStats: %v", err)
	}
	byID := map[string]SubagentStat{}
	for _, stat := range stats.Subagents {
		byID[stat.SubagentID] = stat
	}
	exceeded := byID["child-budget"]
	if exceeded.BudgetTokens != 1_000_000 || !exceeded.BudgetExceeded {
		t.Fatalf("超支行不符: %+v", exceeded)
	}
	noBudget := byID["child-nobudget"]
	if noBudget.BudgetExceeded || noBudget.BudgetTokens != 0 {
		t.Fatalf("未声明预算不得判定超支: %+v", noBudget)
	}
}
