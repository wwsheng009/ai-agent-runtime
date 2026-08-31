package output

import (
	"context"
	"testing"
	"time"
)

// TestObservabilityEventPublishing：10.2 事件发布点回归——
//   - 普通提交封存 record 后发布 EventBatchCompleted（带 RecordID）；
//   - BeginReconfigure 发布 EventGatewayStateChanged(Open→Reconfiguring)；
//   - Abort+Commit(rollback) 发布 (Reconfiguring→Open)；
//   - Commit(install-new) 发布 EventRouteChanged（新旧 target/epoch）；
//   - Close 发布 (Closing→Closed)。
//
// 断言事件可见性而非精确时序（hub journal 有界，RecentEvents 返回）。
func TestObservabilityEventPublishing(t *testing.T) {
	gw := mustGateway(t, memoryPrimary(t))
	ctx := context.Background()

	// 1. 普通提交 → EventBatchCompleted。
	r := gw.Submit(ctx, RenderIntent{
		IntentID: "obs-1",
		Kind:     TransactionFrame,
		Bytes:    []byte("obs"),
	})
	if r.Admission.Decision != AdmissionAccepted {
		t.Fatalf("admission: %+v", r.Admission)
	}
	drainSync(t, gw)
	if !hasEvent(t, gw, func(ev OutputEvent) bool {
		return ev.Kind == EventBatchCompleted && ev.BatchID == r.BatchID && ev.RecordID != ""
	}) {
		t.Fatal("EventBatchCompleted not published for sealed record")
	}

	// 2. Begin → (Open→Reconfiguring)。
	plan, err := gw.BeginReconfigure(ctx, RenderRouteConfig{
		Primary:            NewDiscardSink("pt-obs-candidate"),
		PrimaryOwnership:   SinkOwned,
		ProjectionTargetID: "pt-obs-candidate",
	})
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if !hasEvent(t, gw, func(ev OutputEvent) bool {
		return ev.Kind == EventGatewayStateChanged &&
			ev.PreviousGatewayState == GatewayOpen && ev.GatewayState == GatewayReconfiguring
	}) {
		t.Fatal("EventGatewayStateChanged(Open->Reconfiguring) not published on Begin")
	}

	// 3. Abort + Commit(rollback) → (Reconfiguring→Open)。
	// 先记录 install-new 前的 route 事件计数基线，再验证 rollback 收口。
	if err := gw.AbortReconfigure(ctx, plan.Token); err != nil {
		t.Fatalf("abort: %v", err)
	}
	if err := gw.CommitReconfigure(ctx, plan.Token); err != nil {
		t.Fatalf("commit rollback: %v", err)
	}
	if !hasEvent(t, gw, func(ev OutputEvent) bool {
		return ev.Kind == EventGatewayStateChanged &&
			ev.PreviousGatewayState == GatewayReconfiguring && ev.GatewayState == GatewayOpen
	}) {
		t.Fatal("EventGatewayStateChanged(Reconfiguring->Open) not published on commit")
	}

	// 4. Begin + Commit(install-new) → EventRouteChanged。
	plan2, err := gw.BeginReconfigure(ctx, RenderRouteConfig{
		Primary:            NewDiscardSink("pt-obs-candidate2"),
		PrimaryOwnership:   SinkOwned,
		ProjectionTargetID: "pt-obs-candidate2",
	})
	if err != nil {
		t.Fatalf("begin2: %v", err)
	}
	if err := gw.CommitReconfigure(ctx, plan2.Token); err != nil {
		t.Fatalf("commit install-new: %v", err)
	}
	if !hasEvent(t, gw, func(ev OutputEvent) bool {
		return ev.Kind == EventRouteChanged &&
			ev.ProjectionTargetID == "pt-obs-candidate2" &&
			ev.RouteEpoch == plan2.NewRouteEpoch
	}) {
		t.Fatal("EventRouteChanged not published on install-new commit")
	}

	// 5. Close → (Closing→Closed)。
	closeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := gw.Close(closeCtx); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !hasEvent(t, gw, func(ev OutputEvent) bool {
		return ev.Kind == EventGatewayStateChanged &&
			ev.PreviousGatewayState == GatewayClosing && ev.GatewayState == GatewayClosed
	}) {
		t.Fatal("EventGatewayStateChanged(Closing->Closed) not published on close")
	}
}

// hasEvent 轮询 RecentEvents 查找匹配事件（发布与 memoized done 是同一
// finalizer goroutine 的顺序动作，调用方可能先于发布返回；给 2s 窗口）。
func hasEvent(t *testing.T, gw *RenderOutputGateway, match func(OutputEvent) bool) bool {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, ev := range gw.RecentEvents(512) {
			if match(ev) {
				return true
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	return false
}

// drainSync 提交后同步等待 record 封存（确保 EventBatchCompleted 已发布）。
func drainSync(t *testing.T, gw *RenderOutputGateway) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := gw.Drain(ctx); err != nil {
		t.Fatalf("drain: %v", err)
	}
}
