package lsp

import (
	"context"
	"testing"
	"time"
)

func TestMetricsSnapshotAggregates(t *testing.T) {
	m := NewMetrics()
	base := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	m.Observe(RequestRecord{Time: base, Trigger: "inline", Server: "gopls", Outcome: "injected", DurationMS: 10, DiagCount: 2, AppendedBytes: 100})
	m.Observe(RequestRecord{Time: base.Add(time.Second), Trigger: "inline", Outcome: "injected", DurationMS: 20, AppendedBytes: 50})
	m.Observe(RequestRecord{Time: base.Add(2 * time.Second), Trigger: "tool", Outcome: "degraded_no_fresh", DurationMS: 30, AppendedBytes: 30})
	m.Observe(RequestRecord{Time: base.Add(3 * time.Second), Trigger: "inline", Outcome: "no_server", DurationMS: 0})
	m.Observe(RequestRecord{Time: base.Add(4 * time.Second), Trigger: "inline", Outcome: "clean", DurationMS: 5, AppendedBytes: 20})

	snap := m.Snapshot()
	if snap.Requests != 5 || snap.InlineAttempts != 4 || snap.ToolRequests != 1 {
		t.Fatalf("requests/inline/tool = %d/%d/%d, want 5/4/1", snap.Requests, snap.InlineAttempts, snap.ToolRequests)
	}
	if snap.Injected != 2 || snap.Clean != 1 || snap.NoServer != 1 || snap.Degraded != 1 {
		t.Fatalf("outcome buckets = injected:%d clean:%d no_server:%d degraded:%d, want 2/1/1/1",
			snap.Injected, snap.Clean, snap.NoServer, snap.Degraded)
	}
	if snap.DiagHit != 1 {
		t.Fatalf("diag_hit = %d, want 1（只有 diag_count>0 的 injected 计入）", snap.DiagHit)
	}
	if snap.DiagHitRatio != 0.5 || snap.FallbackRatio != 0.2 {
		t.Fatalf("ratios = diag_hit:%v fallback:%v, want 0.5/0.2", snap.DiagHitRatio, snap.FallbackRatio)
	}
	if snap.WaitLatencyP50MS != 10 || snap.WaitLatencyP95MS != 30 || snap.LatencySamples != 5 {
		t.Fatalf("latency = p50:%d p95:%d samples:%d, want 10/30/5",
			snap.WaitLatencyP50MS, snap.WaitLatencyP95MS, snap.LatencySamples)
	}
	if snap.DiagCount != 2 || snap.AppendedBytes != 200 {
		t.Fatalf("totals = diag:%d bytes:%d, want 2/200", snap.DiagCount, snap.AppendedBytes)
	}
	if snap.LastRequestAt == nil || !snap.LastRequestAt.Equal(base.Add(4*time.Second)) {
		t.Fatalf("last_request_at = %v, want %v", snap.LastRequestAt, base.Add(4*time.Second))
	}
	if len(snap.RecentRequests) != 5 || snap.RecentRequests[0].Outcome != "clean" {
		t.Fatalf("recent requests 必须最新在前，got %#v", snap.RecentRequests)
	}
	if snap.ByOutcome["injected"] != 2 {
		t.Fatalf("by_outcome = %v, want injected=2", snap.ByOutcome)
	}
}

func TestMetricsRecentCapKeepsNewest(t *testing.T) {
	m := NewMetrics()
	total := metricsRecentCap + 6
	for i := 0; i < total; i++ {
		m.Observe(RequestRecord{Outcome: "clean", DurationMS: int64(i)})
	}
	snap := m.Snapshot()
	if snap.Requests != total {
		t.Fatalf("requests = %d, want %d（累计计数不受环形容量影响）", snap.Requests, total)
	}
	if len(snap.RecentRequests) != metricsRecentCap {
		t.Fatalf("recent 容量 = %d, want %d", len(snap.RecentRequests), metricsRecentCap)
	}
	if snap.RecentRequests[0].DurationMS != int64(total-1) {
		t.Fatalf("recent[0] = %d, want %d（最新在前）", snap.RecentRequests[0].DurationMS, total-1)
	}
}

func TestClassifyOutcome(t *testing.T) {
	cases := []struct {
		name    string
		outcome Outcome
		want    string
	}{
		{"no server", Outcome{}, "no_server"},
		{"wait timeout", Outcome{Handled: true, Degraded: true, Reason: "no fresh diagnostics within 1s"}, "degraded_no_fresh"},
		{"budget exhausted", Outcome{Handled: true, Degraded: true, Reason: "diagnostics wait budget exhausted"}, "degraded_no_fresh"},
		{"read error", Outcome{Handled: true, Degraded: true, Reason: "read file: boom"}, "degraded_read_error"},
		{"other degrade", Outcome{Handled: true, Degraded: true, Reason: "unknown"}, "degraded"},
		{"clean", Outcome{Handled: true, Fresh: true}, "clean"},
		{"injected", Outcome{Handled: true, Fresh: true, Items: make([]Diagnostic, 1)}, "injected"},
	}
	for _, tc := range cases {
		if got := classifyOutcome(tc.outcome); got != tc.want {
			t.Fatalf("%s: classifyOutcome = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestBridgeObserveRequestEmitsAndAggregates(t *testing.T) {
	var events []Event
	bridge := NewBridgeWithOptions(Config{Enabled: true}, t.TempDir(), BridgeOptions{
		Observer:             func(event Event) { events = append(events, event) },
		SessionIDFromContext: func(ctx context.Context) string { return "session-1" },
	})
	bridge.observeRequest(context.Background(), "inline", "/tmp/a.go",
		Outcome{Handled: true, Fresh: true, Items: make([]Diagnostic, 3), Servers: []string{"gopls"}},
		42, 15*time.Millisecond)

	if len(events) != 1 {
		t.Fatalf("observer events = %d, want 1", len(events))
	}
	event := events[0]
	if event.Kind != EventRequest || event.Trigger != "inline" || event.Outcome != "injected" {
		t.Fatalf("event = %#v, want request.finished/inline/injected", event)
	}
	if event.DurationMS != 15 || event.DiagCount != 3 || event.AppendedBytes != 42 || event.Server != "gopls" {
		t.Fatalf("event scalars = %#v, want duration=15 diag=3 bytes=42 server=gopls", event)
	}
	if event.SessionID != "session-1" {
		t.Fatalf("event.SessionID = %q, want session-1（从执行 ctx 解析）", event.SessionID)
	}
	snap := bridge.MetricsSnapshot()
	if snap.Requests != 1 || snap.Injected != 1 || snap.DiagHit != 1 || snap.AppendedBytes != 42 {
		t.Fatalf("snapshot = %#v, want requests=1 injected=1 diag_hit=1 bytes=42", snap)
	}
}
