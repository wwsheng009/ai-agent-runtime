package runtimeobserve

import (
	"testing"
	"time"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// TestCollectorTurnMetricsFollowStartedAndFinished 是 PR-4 落点 C 的回归：
// agent.turn.started/finished 必须让 observe 侧同时看到"在跑几轮"和"最近一轮的
// 水位"，而不是只有一个计数。
func TestCollectorTurnMetricsFollowStartedAndFinished(t *testing.T) {
	c := &Collector{}
	startedAt := time.Unix(1700000000, 0).UTC()
	c.updateAggregatesLocked(Event{
		Type:        EventAgentTurnStarted,
		Timestamp:   startedAt,
		Correlation: Correlation{SessionID: "session-a"},
		Payload: map[string]interface{}{
			"step":         0,
			"max_steps":    10,
			"budget_level": "ok",
		},
	})

	if got := c.state.Runtime.RunningTurns; got != 1 {
		t.Fatalf("running_turns=%d want 1", got)
	}
	turn := c.stateLastTurn()
	if turn == nil {
		t.Fatal("last_turn must exist after started")
	}
	if turn.SessionID != "session-a" || turn.MaxSteps != 10 || turn.BudgetLevel != "ok" {
		t.Fatalf("unexpected started watermark: %+v", turn)
	}
	if !turn.StartedAt.Equal(startedAt) {
		t.Fatalf("started_at=%v want %v", turn.StartedAt, startedAt)
	}

	finishedAt := startedAt.Add(90 * time.Second)
	c.updateAggregatesLocked(Event{
		Type:        EventAgentTurnDone,
		Timestamp:   finishedAt,
		Correlation: Correlation{SessionID: "session-a"},
		Payload: map[string]interface{}{
			"step":         4,
			"elapsed_ms":   int64(90000),
			"budget_level": "soft",
			"budget_ratio": 0.84,
		},
	})

	if got := c.state.Runtime.RunningTurns; got != 0 {
		t.Fatalf("running_turns=%d want 0", got)
	}
	rt, _ := c.Stats()
	last := rt.LastTurn
	if last == nil {
		t.Fatal("last_turn must survive finished")
	}
	if last.Step != 4 || last.ElapsedMS != 90000 {
		t.Fatalf("unexpected finished watermark: %+v", last)
	}
	if last.BudgetLevel != "soft" || last.BudgetRatio != 0.84 {
		t.Fatalf("unexpected budget watermark: %+v", last)
	}
	// started 的字段必须保留在合并后的水位里，否则"上限/起点"会丢。
	if last.MaxSteps != 10 || !last.StartedAt.Equal(startedAt) {
		t.Fatalf("started fields lost: %+v", last)
	}
	if !last.FinishedAt.Equal(finishedAt) {
		t.Fatalf("finished_at=%v want %v", last.FinishedAt, finishedAt)
	}

	// 新一轮开始即替换旧轮水位：observe 只保留"最近一轮"，不把上一轮的
	// duration/budget 挂到新轮上。
	c.updateAggregatesLocked(Event{
		Type:        EventAgentTurnStarted,
		Timestamp:   finishedAt.Add(time.Minute),
		Correlation: Correlation{SessionID: "session-b"},
		Payload:     map[string]interface{}{"max_steps": 5, "budget_level": "ok"},
	})
	if last.Step != 4 || last.BudgetLevel != "soft" {
		t.Fatalf("Stats() must return a detached copy, got mutated %+v", last)
	}
	next := c.stateLastTurn()
	if next == nil || next.SessionID != "session-b" || next.Step != 0 || next.ElapsedMS != 0 {
		t.Fatalf("new turn must reset the terminal watermark: %+v", next)
	}
}

// TestCollectorTurnMetricsAcceptFinishedWithoutStarted 覆盖"观测在轮次中途开启"：
// 只看到 finished 时仍要落库终局水位，不能因为缺少 started 就丢掉停止原因。
func TestCollectorTurnMetricsAcceptFinishedWithoutStarted(t *testing.T) {
	c := &Collector{}
	c.updateAggregatesLocked(Event{
		Type:        EventAgentTurnDone,
		Timestamp:   time.Unix(1700000500, 0).UTC(),
		Correlation: Correlation{SessionID: "session-c"},
		Payload: map[string]interface{}{
			"step":         7,
			"elapsed_ms":   int64(1500),
			"budget_level": "hard",
			"budget_ratio": 1.0,
		},
	})
	if got := c.state.Runtime.RunningTurns; got != 0 {
		t.Fatalf("running_turns must not go negative: %d", got)
	}
	rt, _ := c.Stats()
	if rt.LastTurn == nil || rt.LastTurn.SessionID != "session-c" {
		t.Fatalf("finished-only turn must still be recorded: %+v", rt.LastTurn)
	}
	if rt.LastTurn.BudgetLevel != "hard" || rt.LastTurn.BudgetRatio != 1.0 {
		t.Fatalf("unexpected watermark: %+v", rt.LastTurn)
	}
}

// TestProjectorAgentTurnPayloadKeepsWatermarkFields 保证水位字段真的穿过投影
// 白名单（否则 /debug 与 observe 只能看到计数、看不到水位），同时确认正文类
// 字段仍然被丢弃。
func TestProjectorAgentTurnPayloadKeepsWatermarkFields(t *testing.T) {
	p := NewProjector(NewRedactor(nil, "", ""), false, 65536)
	proj, ok := p.ProjectRuntimeEvent(runtimeevents.Event{
		Type:      EventAgentTurnDone,
		Timestamp: time.Unix(1700001000, 0).UTC(),
		SessionID: "session-a",
		TraceID:   "trace-a",
		Payload: map[string]interface{}{
			"step":         4,
			"max_steps":    10,
			"elapsed_ms":   int64(90000),
			"budget_level": "soft",
			"budget_ratio": 0.84,
			"turn_id":      "turn-1",
			"prompt":       "secret prompt body",
		},
	})
	if !ok {
		t.Fatal("agent.turn.finished must stay in the projector allowlist")
	}
	if got := proj.Payload["step"]; got != 4 {
		t.Fatalf("step=%v want 4", got)
	}
	if got := proj.Payload["max_steps"]; got != 10 {
		t.Fatalf("max_steps=%v want 10", got)
	}
	if got := proj.Payload["elapsed_ms"]; got != int64(90000) {
		t.Fatalf("elapsed_ms=%v want 90000", got)
	}
	if got := proj.Payload["budget_level"]; got != "soft" {
		t.Fatalf("budget_level=%v want soft", got)
	}
	if got := proj.Payload["budget_ratio"]; got != 0.84 {
		t.Fatalf("budget_ratio=%v want 0.84", got)
	}
	if _, exists := proj.Payload["prompt"]; exists {
		t.Fatal("prompt body must never survive projection")
	}
	if proj.Correlation.SessionID != "session-a" || proj.Correlation.TraceID != "trace-a" {
		t.Fatalf("correlation lost: %+v", proj.Correlation)
	}
}

// TestCollectorTurnMetricsDedupKeepsSecondTurn 是 2026-10-09 真机回归：同一会话的
// 第二轮 turn.started/finished 曾被 (session, type) 去重键折叠成"重复投递"丢弃，
// 结果是 running_turns 停在 1、last_turn.finished_at 永远为零。本用例走 consume
// 全路径（含去重），锁定不同轮次互不折叠、同事件重复投递仍只计一次。
func TestCollectorTurnMetricsDedupKeepsSecondTurn(t *testing.T) {
	c := &Collector{
		cfg:        Config{Enabled: true, MaxEventBytes: 65536, RetentionEvents: 32},
		projector:  NewProjector(NewRedactor(nil, "", ""), false, 65536),
		dedup:      make(map[uint64]struct{}),
		dedupLimit: 1024,
		nextSeq:    1,
		ring:       make([]ringSlot, 32),
	}
	c.state.Runtime.FilteredByType = map[string]uint64{}
	base := time.Unix(1700002000, 0).UTC()
	events := []runtimeevents.Event{
		{Type: EventAgentTurnStarted, Timestamp: base, SessionID: "session-x", TraceID: "trace-1",
			Payload: map[string]interface{}{"turn_id": "turn-1", "max_steps": 10, "budget_level": "ok"}},
		{Type: EventAgentTurnDone, Timestamp: base.Add(time.Minute), SessionID: "session-x", TraceID: "trace-1",
			Payload: map[string]interface{}{"turn_id": "turn-1", "step": 3, "elapsed_ms": int64(60000)}},
		{Type: EventAgentTurnStarted, Timestamp: base.Add(2 * time.Minute), SessionID: "session-x", TraceID: "trace-2",
			Payload: map[string]interface{}{"turn_id": "turn-2", "max_steps": 10, "budget_level": "ok"}},
		{Type: EventAgentTurnDone, Timestamp: base.Add(3 * time.Minute), SessionID: "session-x", TraceID: "trace-2",
			Payload: map[string]interface{}{"turn_id": "turn-2", "step": 2, "elapsed_ms": int64(60000)}},
	}
	for _, event := range events {
		c.consume(ingressItem{kind: "runtime", event: event})
	}
	if got := c.stateRunningTurns(); got != 0 {
		t.Fatalf("second turn finished must decrement running_turns, got %d", got)
	}
	last := c.stateLastTurn()
	if last == nil || last.FinishedAt.IsZero() {
		t.Fatalf("last_turn must be closed by the second finished event: %+v", last)
	}
	if last.SessionID != "session-x" || last.Step != 2 {
		t.Fatalf("unexpected last turn watermark: %+v", last)
	}
	// 同一事件重复投递仍必须只计一次（去重语义不能被修复破坏）。
	c.consume(ingressItem{kind: "runtime", event: events[3]})
	if got := c.stateRunningTurns(); got != 0 {
		t.Fatalf("duplicate finished must stay deduplicated, running_turns=%d", got)
	}
	if got := c.latestSeq; got != 4 {
		t.Fatalf("duplicate delivery must not append a new ring slot, latest_seq=%d", got)
	}
}
