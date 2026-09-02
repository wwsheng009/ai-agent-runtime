package output

import (
	"context"
	"testing"
	"time"
)

// ============================================================================
// P1-A2 故障注入与恢复矩阵（场景文档 §A2）
// ============================================================================
//
// 核心守护语义（计划 §6.2）：gateway 对一次 Submit 恰好执行一次 sink 调用，
// 绝不静默 retry；UnknownPartial 的恢复必须是调用方发起的新 intent + 新
// BatchID，并通过 ParentBatchID/Cause 在 journal 中保留完整恢复链。
//
// 矩阵覆盖：none / reject / zero / partial(short) / error_committed(full+error)
// / panic / block。

// TestA2FaultInjectionMatrix 对每种 FaultKind 验证：
//   - gateway 表面化预期 DeliveryStatus/ErrorClass；
//   - 无静默 retry：一次 Submit 恰好一次 sink 调用；
//   - UnknownPartial 用例：恢复批次 BatchID 为新值、ParentBatchID/Cause 指向
//     原始批次，journal 中原始记录保持 non-committed；
//   - 故障后 gateway 仍可用，sequence 单调。
func TestA2FaultInjectionMatrix(t *testing.T) {
	tests := []struct {
		name         string
		fault        FaultKind
		wantStatus   DeliveryStatus
		wantErrClass DeliveryErrorClass
		isPartial    bool // UnknownPartial → 可恢复（断言恢复链）
	}{
		{"none", FaultNone, DeliveryCommitted, DeliveryErrorNone, false},
		{"reject", FaultReject, DeliveryRejected, DeliveryErrorSink, false},
		// zero：无 err 的 zero-byte failure 由 normalizer 补齐稳定 class sink。
		{"zero", FaultZero, DeliveryFailedZeroBytes, DeliveryErrorSink, false},
		{"partial", FaultPartial, DeliveryUnknownPartial, DeliveryErrorSink, true},
		// error_committed：声称 committed 但带 err 的非法组合被保守归一化为
		// unknown_partial（FaultSink 注释：非法，归一化 unknown）→ 可恢复。
		{"error_committed", FaultErrorCommitted, DeliveryUnknownPartial, DeliveryErrorSink, true},
		{"panic", FaultPanic, DeliveryUnknownPartial, DeliveryErrorSink, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fault := NewFaultSink(TargetDescriptor{
				SinkID:             "fault-" + tt.name,
				Class:              TargetClassPhysical,
				ProjectionTargetID: "pt-primary",
			})
			fault.SetKind(tt.fault)
			gw := mustGateway(t, fault)

			r := gw.Submit(context.Background(), RenderIntent{
				IntentID: randomID("int"),
				Kind:     TransactionFrame,
				Source:   "a2-matrix",
				Cause:    "a2-fault-test",
				Bytes:    []byte("fault-test-payload"),
			})
			if r.Admission.Decision != AdmissionAccepted {
				t.Fatalf("admission: %+v", r.Admission)
			}
			if r.Primary == nil {
				t.Fatal("expected non-nil primary receipt")
			}
			if r.Primary.Status != tt.wantStatus {
				t.Fatalf("primary status = %s, want %s", r.Primary.Status, tt.wantStatus)
			}
			if r.Primary.ErrorClass != tt.wantErrClass {
				t.Fatalf("error class = %s, want %s", r.Primary.ErrorClass, tt.wantErrClass)
			}
			// 无静默 retry：一次 Submit 恰好一次 sink 调用。
			if calls := fault.DrainCalls(); calls != 1 {
				t.Fatalf("sink calls = %d, want 1 (no silent retry)", calls)
			}

			if tt.isPartial {
				assertRecoveryChain(t, gw, fault, r)
			}

			// 故障后 gateway 仍可用（先清除 fault kind）。
			fault.SetKind(FaultNone)
			r3 := submitOK(t, gw, TransactionFrame, []byte("post-fault"))
			if r3.Sequence <= r.Sequence {
				t.Fatalf("sequence not monotonic after fault: %d <= %d", r3.Sequence, r.Sequence)
			}
		})
	}
}

// assertRecoveryChain 对一次 UnknownPartial 原始批次执行恢复演练：
//  1. 清除 fault（模拟调用方确认底层已恢复）；
//  2. 以新 intent + ParentBatchID/Cause 提交恢复批次；
//  3. 断言恢复批次 BatchID 为新值、Primary committed；
//  4. 断言 journal 中恢复记录 ParentBatchID 指向原始批次、Cause 正确；
//  5. 断言原始记录未进入 Committed（bytes 不成为已提交事实）。
func assertRecoveryChain(t *testing.T, gw *RenderOutputGateway, fault *FaultSink, orig OutputReceipt) {
	t.Helper()
	if orig.Primary == nil || orig.Primary.Status != DeliveryUnknownPartial {
		t.Fatalf("assertRecoveryChain requires UnknownPartial original, got %+v", orig.Primary)
	}
	originalBatchID := orig.BatchID
	if originalBatchID == "" {
		t.Fatal("original batch has empty BatchID")
	}

	// 恢复前确认故障仍可复现；清除 fault 后恢复才可能 committed。
	fault.SetKind(FaultNone)

	r2 := gw.Submit(context.Background(), RenderIntent{
		IntentID:      randomID("rec"),
		ParentBatchID: originalBatchID,
		Kind:          TransactionFrame,
		Source:        "a2-matrix",
		Cause:         "recovery",
		Bytes:         []byte("recovery-payload"),
	})
	if r2.Admission.Decision != AdmissionAccepted {
		t.Fatalf("recovery admission: %+v", r2.Admission)
	}
	if r2.Primary == nil || r2.Primary.Status != DeliveryCommitted {
		t.Fatalf("recovery primary: %+v", r2.Primary)
	}
	// 恢复批次必须使用新 BatchID，绝不复用原始批次 identity。
	if r2.BatchID == originalBatchID {
		t.Fatal("recovery batch reused the original BatchID")
	}
	if r2.BatchID == "" {
		t.Fatal("recovery batch has empty BatchID")
	}

	// journal 可完整还原恢复链。
	recs := gw.RecentDeliveries(10)
	var origRec, recovRec *DeliveryRecord
	for i := range recs {
		switch recs[i].Batch.BatchID {
		case originalBatchID:
			origRec = &recs[i]
		case r2.BatchID:
			recovRec = &recs[i]
		}
	}
	if origRec == nil {
		t.Fatal("original batch not found in journal")
	}
	if recovRec == nil {
		t.Fatal("recovery batch not found in journal")
	}
	if recovRec.Batch.ParentBatchID != originalBatchID {
		t.Fatalf("recovery ParentBatchID = %q, want %q", recovRec.Batch.ParentBatchID, originalBatchID)
	}
	if recovRec.Batch.Cause != "recovery" {
		t.Fatalf("recovery Cause = %q, want %q", recovRec.Batch.Cause, "recovery")
	}
	// 原始 partial 的 bytes 不进入 Committed 事实。
	if origRec.Output.Primary == nil {
		t.Fatal("original journal record missing primary receipt")
	}
	if origRec.Output.Primary.Status == DeliveryCommitted {
		t.Fatal("original partial batch must not be recorded as committed")
	}
	// 恢复批次确认为 committed。
	if recovRec.Output.Primary == nil || recovRec.Output.Primary.Status != DeliveryCommitted {
		t.Fatalf("recovery record primary: %+v", recovRec.Output.Primary)
	}
	// 恢复批次自身不再携带 parent（单跳链：recov → orig）。
	if origRec.Batch.ParentBatchID != "" {
		t.Fatalf("original ParentBatchID should be empty, got %q", origRec.Batch.ParentBatchID)
	}
}

// TestA2FaultBlockRelease 验证 FaultBlock：Submit 阻塞直到 Release，期间
// 无额外 sink 调用，Release 后按 zero-proof 收敛且 gateway 继续可用。
func TestA2FaultBlockRelease(t *testing.T) {
	fault := NewFaultSink(TargetDescriptor{
		SinkID:             "fault-block",
		Class:              TargetClassPhysical,
		ProjectionTargetID: "pt-primary",
	})
	fault.SetKind(FaultBlock)
	gw := mustGateway(t, fault)

	type submitResult struct {
		r   OutputReceipt
		err error
	}
	done := make(chan submitResult, 1)
	go func() {
		r := gw.Submit(context.Background(), RenderIntent{
			IntentID: randomID("int"),
			Kind:     TransactionFrame,
			Source:   "a2-block",
			Cause:    "a2-block-test",
			Bytes:    []byte("blocked-payload"),
		})
		done <- submitResult{r: r}
	}()

	// 阻塞期间不产生 sink 调用、Submit 未返回。
	select {
	case res := <-done:
		t.Fatalf("submit returned while blocked: %+v", res.r)
	case <-time.After(50 * time.Millisecond):
	}
	if calls := fault.DrainCalls(); calls != 1 {
		t.Fatalf("sink calls while blocked = %d, want 1", calls)
	}

	fault.Release()
	select {
	case res := <-done:
		if res.r.Admission.Decision != AdmissionAccepted {
			t.Fatalf("admission after release: %+v", res.r.Admission)
		}
		if res.r.Primary == nil {
			t.Fatal("expected primary receipt after release")
		}
		if res.r.Primary.Status != DeliveryFailedZeroBytes {
			t.Fatalf("blocked release status = %s, want %s", res.r.Primary.Status, DeliveryFailedZeroBytes)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("submit did not return after release")
	}

	// 故障清除后 gateway 继续可用。
	fault.SetKind(FaultNone)
	submitOK(t, gw, TransactionFrame, []byte("post-block"))
}

// ============================================================================
// P1-E2 Unknown Partial 恢复语义（场景文档 §E2）
// ============================================================================

// TestE2UnknownPartialRecovery 完整演练 UnknownPartial 恢复语义：
//   - 原始批次 UnknownPartial 且不静默 retry（恰好一次 sink 调用）；
//   - 恢复批次 BatchID 为新值、ParentBatchID/Cause 指向原始批次；
//   - 原始 partial bytes 不进入 Committed；
//   - journal 可完整还原恢复链（orig → recov → 独立后续批次）。
func TestE2UnknownPartialRecovery(t *testing.T) {
	fault := NewFaultSink(TargetDescriptor{
		SinkID:             "e2-partial",
		Class:              TargetClassPhysical,
		ProjectionTargetID: "pt-primary",
	})
	fault.SetKind(FaultPartial)
	gw := mustGateway(t, fault)

	// 1. 原始 intent → UnknownPartial。
	r := gw.Submit(context.Background(), RenderIntent{
		IntentID: randomID("orig"),
		Kind:     TransactionFrame,
		Source:   "e2-test",
		Cause:    "e2-original",
		Bytes:    []byte("original-partial-content"),
	})
	if r.Admission.Decision != AdmissionAccepted {
		t.Fatalf("admission: %+v", r.Admission)
	}
	if r.Primary == nil || r.Primary.Status != DeliveryUnknownPartial {
		t.Fatalf("expected UnknownPartial, got %+v", r.Primary)
	}
	if r.Primary.Certainty != WriteCertaintyUnknown {
		t.Fatalf("certainty = %s, want unknown", r.Primary.Certainty)
	}
	// 无静默 retry。
	if calls := fault.DrainCalls(); calls != 1 {
		t.Fatalf("sink calls = %d, want 1 (no silent retry)", calls)
	}
	originalBatchID := r.BatchID

	// 2. 恢复：清除故障，以新 intent + parent/cause 提交恢复批次。
	fault.SetKind(FaultNone)
	r2 := gw.Submit(context.Background(), RenderIntent{
		IntentID:      randomID("recov"),
		ParentBatchID: originalBatchID,
		Kind:          TransactionFrame,
		Source:        "e2-test",
		Cause:         "recovery",
		Bytes:         []byte("recovery-content"),
	})
	if r2.Admission.Decision != AdmissionAccepted {
		t.Fatalf("recovery admission: %+v", r2.Admission)
	}
	if r2.Primary == nil || r2.Primary.Status != DeliveryCommitted {
		t.Fatalf("recovery primary: %+v", r2.Primary)
	}
	if r2.BatchID == originalBatchID {
		t.Fatal("recovery reused original BatchID")
	}

	// 3. 独立后续批次（与恢复链无关联）。
	r3 := submitOK(t, gw, TransactionFrame, []byte("independent-trailer"))

	// 4. journal 完整还原恢复链。
	recs := gw.RecentDeliveries(10)
	var origRec, recovRec, trailRec *DeliveryRecord
	for i := range recs {
		switch recs[i].Batch.BatchID {
		case originalBatchID:
			origRec = &recs[i]
		case r2.BatchID:
			recovRec = &recs[i]
		case r3.BatchID:
			trailRec = &recs[i]
		}
	}
	if origRec == nil {
		t.Fatal("original record not found in journal")
	}
	if recovRec == nil {
		t.Fatal("recovery record not found in journal")
	}
	if trailRec == nil {
		t.Fatal("trailer record not found in journal")
	}

	// 原始记录：partial、parent 为空、元数据保留。
	if origRec.Output.Primary == nil {
		t.Fatal("original record missing primary")
	}
	if origRec.Output.Primary.Status != DeliveryUnknownPartial {
		t.Fatalf("original journal status = %s, want UnknownPartial", origRec.Output.Primary.Status)
	}
	if origRec.Batch.ParentBatchID != "" {
		t.Fatalf("original ParentBatchID = %q, want empty", origRec.Batch.ParentBatchID)
	}
	if origRec.Batch.Cause != "e2-original" {
		t.Fatalf("original Cause = %q, want %q", origRec.Batch.Cause, "e2-original")
	}
	// 原始 partial 的 bytes 不进入 Committed（BytesHash 仍在 metadata 中）。
	if origRec.Output.Primary.Status == DeliveryCommitted {
		t.Fatal("original partial must not be committed")
	}

	// 恢复记录：committed、ParentBatchID/Cause 指向原始批次、TargetClass 不降级。
	if recovRec.Output.Primary == nil || recovRec.Output.Primary.Status != DeliveryCommitted {
		t.Fatalf("recovery journal primary: %+v", recovRec.Output.Primary)
	}
	if recovRec.Batch.ParentBatchID != originalBatchID {
		t.Fatalf("recovery ParentBatchID = %q, want %q", recovRec.Batch.ParentBatchID, originalBatchID)
	}
	if recovRec.Batch.Cause != "recovery" {
		t.Fatalf("recovery Cause = %q, want %q", recovRec.Batch.Cause, "recovery")
	}
	if recovRec.Batch.ProjectionTargetClass != TargetClassPhysical {
		t.Fatalf("recovery target class = %s, want physical (no downgrade)", recovRec.Batch.ProjectionTargetClass)
	}

	// 独立后续批次：parent 为空，与恢复链无关。
	if trailRec.Batch.ParentBatchID != "" {
		t.Fatalf("trailer ParentBatchID = %q, want empty", trailRec.Batch.ParentBatchID)
	}
	if trailRec.Output.Primary == nil || trailRec.Output.Primary.Status != DeliveryCommitted {
		t.Fatalf("trailer journal primary: %+v", trailRec.Output.Primary)
	}
}

// ============================================================================
// P1-E4 阻塞 Writer 关闭与 Abandoned 安全退出（场景文档 §E4）
// ============================================================================
//
// 验收标准：阻塞 writer（永不返回的 FaultBlock callback）场景下：
//   - Close 在 CloseTimeout 超时后 deviate，返回 DeliveryErrorAbandoned
//     （FaultSink.Abort 不提供 terminated proof，无法证明 primary 终止）；
//   - gateway 进入 GatewayAbandoned 且快照 AbandonedReason 非空（可观测）；
//   - 排队批次得到 DeliveryErrorAbandoned，不执行 sink、不伪造 Committed；
//   - 晚到的 callback（Release 后）只发晚期诊断，不重写已封存的 synthetic
//     结果。

// TestE4BlockedWriterAbandonedSafeExit 用 FaultSink+FaultBlock 模拟永不返回
// 的阻塞 writer，验证 Close deviate → Abandoned 全链路。
func TestE4BlockedWriterAbandonedSafeExit(t *testing.T) {
	blocker := NewFaultSink(TargetDescriptor{
		SinkID:             "e4-block-writer",
		Class:              TargetClassPhysical,
		ProjectionTargetID: "pt-e4",
	})
	blocker.SetKind(FaultBlock)
	gw := mustGateway(t, blocker)

	// 第一笔进入阻塞 callback。
	first := make(chan OutputReceipt, 1)
	go func() {
		first <- gw.Submit(context.Background(), RenderIntent{
			IntentID: "e4-1",
			Kind:     TransactionFrame,
			Bytes:    []byte("blocked-write"),
		})
	}()
	deadline := time.Now().Add(2 * time.Second)
	for blocker.DrainCalls() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if blocker.DrainCalls() == 0 {
		t.Fatal("first submit never reached sink")
	}

	// 第二笔排队等待 serial。
	second := make(chan OutputReceipt, 1)
	go func() {
		second <- gw.Submit(context.Background(), RenderIntent{
			IntentID: "e4-2",
			Kind:     TransactionFrame,
			Bytes:    []byte("queued"),
		})
	}()
	dl2 := time.Now().Add(2 * time.Second)
	for {
		gw.mu.Lock()
		w := gw.primaryWaiters
		gw.mu.Unlock()
		if w >= 1 || time.Now().After(dl2) {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}

	// Close：caller ctx 取消只结束该 caller 等待；shared close 继续。
	ctx, cancel := context.WithCancel(context.Background())
	closeDone := make(chan error, 1)
	go func() { closeDone <- gw.Close(ctx) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-closeDone:
		if ClassOf(err) != DeliveryErrorControlCanceled {
			t.Fatalf("caller cancellation class: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("canceled close caller did not return")
	}

	// shared close 继续；推进注入时钟触发其独立 CloseTimeout → deviate。
	clock, ok := gw.clock.(*FakeClock)
	if !ok {
		t.Fatalf("gateway clock is %T, want *FakeClock", gw.clock)
	}
	clock.Advance(gw.opts.CloseTimeout)

	// 排队中的第二笔必须返回 synthetic abandoned，不执行 sink。
	select {
	case r2 := <-second:
		if r2.Primary == nil || !r2.Primary.Synthetic || r2.TargetInvoked {
			t.Fatalf("queued batch must finalize without invoking sink: %+v", r2)
		}
		if r2.Admission.Decision != AdmissionAccepted ||
			r2.Primary.ErrorClass != DeliveryErrorAbandoned {
			t.Fatalf("expected accepted synthetic abandoned outcome, got %+v", r2)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("queued batch blocked forever after close-deviate")
	}
	// 无静默 retry：sink 仍只被第一笔调用一次。
	if calls := blocker.DrainCalls(); calls != 1 {
		t.Fatalf("sink calls = %d, want 1 (queued batch must not execute sink)", calls)
	}

	// 等待 shared close 固定终态 Abandoned（Abort 无 terminated proof）。
	dl3 := time.Now().Add(3 * time.Second)
	for gw.stateOf() != GatewayAbandoned && time.Now().Before(dl3) {
		time.Sleep(5 * time.Millisecond)
	}
	if gw.stateOf() != GatewayAbandoned {
		t.Fatalf("gateway state = %s, want abandoned", gw.stateOf())
	}
	// AbandonedReason 非空：快照可观测（E4 验收标准）。
	snap := gw.Snapshot()
	if snap.AbandonedReason == "" {
		t.Fatal("AbandonedReason must be non-empty after abandon")
	}

	// 放行第一笔（晚到的 callback 返回）：已封存 synthetic abandoned，
	// 晚到返回只发晚期诊断，不重写该 Submit 的收据。
	blocker.Release()
	select {
	case r1 := <-first:
		if r1.Primary == nil || r1.Primary.ErrorClass != DeliveryErrorAbandoned {
			t.Fatalf("blocked submit did not observe fixed abandoned outcome: %+v", r1)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("blocked submit did not return after writer release")
	}

	// 终态快照仍为 Abandoned 且原因保留（不因晚到 callback 回退）。
	snap2 := gw.Snapshot()
	if snap2.State != GatewayAbandoned || snap2.AbandonedReason != snap.AbandonedReason {
		t.Fatalf("terminal snapshot lost abandoned facts: %+v", snap2)
	}
}
