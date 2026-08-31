package output

import (
	"context"
	"errors"
	"testing"
	"time"
)

// ============================================================================
// Phase 4 parity 测试
// ============================================================================

// TestParityBatchWire：wire bytes 完全一致 → pass；差异 → fail + 差异报告。
func TestParityBatchWire(t *testing.T) {
	expected := []byte("same-content-123")
	res := CheckBatchParity(1, "b1", expected, []byte("same-content-123"))
	if !res.Pass || res.Skipped {
		t.Fatalf("identical wire must pass: %+v", res)
	}
	res = CheckBatchParity(2, "b2", expected, []byte("same-content-456"))
	if res.Pass {
		t.Fatal("different wire must fail")
	}
	if len(res.Mismatches) == 0 {
		t.Fatal("mismatch details required")
	}
	// 长度不同。
	res = CheckBatchParity(3, "b3", []byte("abcdef"), []byte("abcdefg"))
	if res.Pass || res.ExpectedLen != 6 || res.ActualLen != 7 {
		t.Fatalf("length mismatch: %+v", res)
	}
}

// TestParitySemanticUnavailableSkipped：semantic 缺失/schema 不同 →
// skipped-with-reason，不判 success。
func TestParitySemanticUnavailableSkipped(t *testing.T) {
	res := CheckSemanticParity(1, "b1", nil, &SemanticPayload{SchemaVersion: 1, PlainText: "x"})
	if !res.Skipped || res.Pass {
		t.Fatalf("physical nil must skip: %+v", res)
	}
	res = CheckSemanticParity(2, "b2", &SemanticPayload{SchemaVersion: 1, PlainText: "x"}, nil)
	if !res.Skipped {
		t.Fatalf("capture nil must skip: %+v", res)
	}
	// schema 不同 → skip。
	res = CheckSemanticParity(3, "b3",
		&SemanticPayload{SchemaVersion: 1, PlainText: "x"},
		&SemanticPayload{SchemaVersion: 2, PlainText: "x"})
	if !res.Skipped || res.SkipReason == "" {
		t.Fatalf("schema mismatch must skip with reason: %+v", res)
	}
	// 相同 schema：文本一致 → pass。
	res = CheckSemanticParity(4, "b4",
		&SemanticPayload{SchemaVersion: 1, PlainText: "hello"},
		&SemanticPayload{SchemaVersion: 1, PlainText: "hello"})
	if res.Skipped || !res.Pass {
		t.Fatalf("same semantic must pass: %+v", res)
	}
	// 文本不同 → fail。
	res = CheckSemanticParity(5, "b5",
		&SemanticPayload{SchemaVersion: 1, PlainText: "hello"},
		&SemanticPayload{SchemaVersion: 1, PlainText: "world"})
	if res.Pass {
		t.Fatalf("different semantic must fail: %+v", res)
	}
}

// TestParityVirtualProjection：rows+scrollback 一致 → pass；unknown → skip；
// 差异 → fail。
func TestParityVirtualProjection(t *testing.T) {
	base := VirtualProjectionSnapshot{
		SchemaVersion: SchemaVersion,
		Width:         20,
		Height:        6,
		Rows:          []string{"row-a", "row-b", "row-c"},
		Scrollback:    []string{"scroll-1"},
		Validity:      ProjectionValid,
	}
	res := CheckVirtualProjectionParity(1, "b1", base, base)
	if !res.Pass || res.Skipped {
		t.Fatalf("same projection must pass: %+v", res)
	}
	// unknown → skip。
	unk := base
	unk.Validity = ProjectionUnknown
	res = CheckVirtualProjectionParity(2, "b2", base, unk)
	if !res.Skipped {
		t.Fatalf("unknown projection must skip: %+v", res)
	}
	// 行内容差异 → fail。
	diff := base
	diff.Rows = []string{"row-a", "row-X", "row-c"}
	res = CheckVirtualProjectionParity(3, "b3", base, diff)
	if res.Pass || len(res.Mismatches) == 0 {
		t.Fatalf("projection diff must fail: %+v", res)
	}
}

// TestParityVirtualProjectionScrollback：scrollback 参与比较。
func TestParityVirtualProjectionScrollback(t *testing.T) {
	base := VirtualProjectionSnapshot{
		SchemaVersion: SchemaVersion,
		Width:         20,
		Height:        6,
		Rows:          []string{"r"},
		Validity:      ProjectionValid,
	}
	diff := base
	diff.Scrollback = []string{"extra-scroll"}
	res := CheckVirtualProjectionParity(1, "b1", base, diff)
	if res.Pass {
		t.Fatalf("scrollback diff must fail: %+v", res)
	}
}

// TestParityBatchMismatchLimit：差异报告被限制在前 N 个。
func TestParityBatchMismatchLimit(t *testing.T) {
	e := make([]byte, 64)
	a := make([]byte, 64)
	for i := range e {
		e[i] = byte('a' + i%26)
		a[i] = byte('z')
	}
	res := CheckBatchParity(1, "b1", e, a)
	if res.Pass {
		t.Fatal("must fail")
	}
	if len(res.Mismatches) > MaxParityMismatchReports {
		t.Fatalf("mismatch report limit: %d", len(res.Mismatches))
	}
}

// TestParitySemanticSummaryHash：SummaryHash 不同 → fail。
func TestParitySemanticSummaryHash(t *testing.T) {
	res := CheckSemanticParity(1, "b1",
		&SemanticPayload{SchemaVersion: 1, PlainText: "same", SummaryHash: "h1"},
		&SemanticPayload{SchemaVersion: 1, PlainText: "same", SummaryHash: "h2"})
	if res.Pass {
		t.Fatalf("hash diff must fail: %+v", res)
	}
}

// parityDoubleRunFixture：physical primary + capture mirror 双跑 fixture。
type parityDoubleRunFixture struct {
	Gateway *RenderOutputGateway
	Sink    *MemorySink
	Capture *CaptureSink
}

func newParityDoubleRunFixture(t *testing.T) *parityDoubleRunFixture {
	t.Helper()
	sink := NewMemorySink(TargetDescriptor{
		SinkID:             "double-primary",
		Class:              TargetClassPhysical,
		ProjectionTargetID: "pt-primary",
	})
	capture := NewCaptureSink("pt-capture", CaptureOptions{
		MaxEntries:   64,
		MaxBytes:     1 << 20,
		StorePayload: true,
	})
	gw, err := NewRenderOutputGateway("double-"+randomID("s"), gatewayOptions(), RenderRouteConfig{
		Primary:            sink,
		PrimaryOwnership:   SinkOwned,
		ProjectionTargetID: "pt-primary",
		Mirrors: []RenderMirror{{
			Sink:      capture,
			Policy:    MirrorBestEffort,
			ApplyMode: MirrorApplyBytes,
			Ownership: SinkOwned,
			Timeout:   time.Second,
		}},
	})
	if err != nil {
		t.Fatalf("gateway: %v", err)
	}
	gw.Run()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = gw.Close(ctx)
	})
	return &parityDoubleRunFixture{Gateway: gw, Sink: sink, Capture: capture}
}

// TestP4DoubleRunParity：physical primary 与 capture mirror 同一 route 双跑，
// wire parity pass；console（memory sink）保留完整输出。
func TestP4DoubleRunParity(t *testing.T) {
	fx := newParityDoubleRunFixture(t)
	payload := []byte("\x1b[1;24r\x1b[Hhello world\x1b[0m")
	r := fx.Gateway.Submit(context.Background(), RenderIntent{
		IntentID: "p4-1",
		Kind:     TransactionFrame,
		Source:   "parity-test",
		Cause:    "double-run",
		Bytes:    payload,
	})
	if r.Admission.Decision != AdmissionAccepted || r.Primary == nil || r.Primary.Status != DeliveryCommitted {
		t.Fatalf("primary: %+v", r)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := fx.Gateway.Drain(ctx); err != nil {
		t.Fatalf("drain: %v", err)
	}
	// console 保留完整输出。
	batches := fx.Sink.SnapshotBatches()
	if len(batches) != 1 || string(batches[0].Bytes) != string(payload) {
		t.Fatalf("console output: %+v", batches)
	}
	// capture 记录同一 wire bytes。
	entries := fx.Capture.Entries()
	if len(entries) != 1 {
		t.Fatalf("capture entries: %d", len(entries))
	}
	if entries[0].Mode != RecordedFullAvailable {
		t.Fatalf("capture mode: %s", entries[0].Mode)
	}
	got, cls := fx.Capture.Payload(entries[0].CaptureEntryID)
	if cls != CapturePayloadErrorNone || string(got) != string(payload) {
		t.Fatalf("capture payload: %q class=%s", got, cls)
	}
	// parity：physical batch bytes 与 capture bytes 一致。
	res := CheckBatchParity(r.Sequence, r.BatchID, payload, got)
	if !res.Pass {
		t.Fatalf("wire parity failed: %+v", res)
	}
}

// TestP4CaptureFullDoesNotBlockPrimary：capture mirror 满/慢不影响 physical
// receipt（mirror 是异步有界队列）。
func TestP4CaptureFullDoesNotBlockPrimary(t *testing.T) {
	// 小容量 capture：很快填满。
	sink := NewMemorySink(TargetDescriptor{
		SinkID:             "full-primary",
		Class:              TargetClassPhysical,
		ProjectionTargetID: "pt-primary",
	})
	capture := NewCaptureSink("pt-capture", CaptureOptions{
		MaxEntries:   2,
		MaxBytes:     64,
		StorePayload: true,
	})
	gw, err := NewRenderOutputGateway("full-"+randomID("s"), gatewayOptions(), RenderRouteConfig{
		Primary:            sink,
		PrimaryOwnership:   SinkOwned,
		ProjectionTargetID: "pt-primary",
		Mirrors: []RenderMirror{{
			Sink:      capture,
			Policy:    MirrorBestEffort,
			ApplyMode: MirrorApplyBytes,
			Ownership: SinkOwned,
			Timeout:   time.Second,
		}},
	})
	if err != nil {
		t.Fatalf("gateway: %v", err)
	}
	gw.Run()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = gw.Close(ctx)
	})
	// 连续提交 20 笔大 batch：capture 必然溢出，但每笔 primary 仍 committed。
	deadline := time.Now().Add(2 * time.Second)
	for i := 0; i < 20 && time.Now().Before(deadline); i++ {
		r := gw.Submit(context.Background(), RenderIntent{
			IntentID: "f1",
			Kind:     TransactionFrame,
			Source:   "full-test",
			Bytes:    []byte("0123456789ABCDEF"),
		})
		if r.Admission.Decision != AdmissionAccepted || r.Primary == nil ||
			r.Primary.Status != DeliveryCommitted {
			t.Fatalf("primary blocked by capture: %+v", r)
		}
	}
	// capture 有 drop 计数，但 primary 全 committed。
	if n := len(sink.SnapshotBatches()); n != 20 {
		t.Fatalf("primary batches: %d", n)
	}
	drainCtx, drainCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer drainCancel()
	if err := gw.Drain(drainCtx); err != nil {
		t.Fatalf("drain capture mirror: %v", err)
	}
	snap := capture.CaptureSnapshot()
	if snap.DroppedBatches == 0 {
		t.Fatal("expected capture drops with tiny capacity")
	}
}

// TestP4SemanticParity：semantic payload 相同 → pass；不可用 → skip。
func TestP4SemanticParity(t *testing.T) {
	res := CheckSemanticParity(1, "b1",
		&SemanticPayload{SchemaVersion: 1, PlainText: "hello\nworld"},
		&SemanticPayload{SchemaVersion: 1, PlainText: "hello\nworld"})
	if !res.Pass || res.Skipped {
		t.Fatalf("semantic parity: %+v", res)
	}
	res = CheckSemanticParity(2, "b2", nil, nil)
	if !res.Skipped {
		t.Fatalf("both nil must skip: %+v", res)
	}
}

// TestP4VirtualProjectionParity：virtual mirror 投影与期望行一致。
func TestP4VirtualProjectionParity(t *testing.T) {
	emu := newFakeEmulator(20, 6)
	v := NewVirtualTerminalSink("pt-virtual", emu, VirtualSinkOptions{})
	_ = v.SubmitMirror(context.Background(), virtualEnv("e1", "b1", 1, MirrorApplyBytes, false, "line-one\nline-two"))
	proj := v.Projection()
	want := VirtualProjectionSnapshot{
		SchemaVersion: SchemaVersion,
		Width:         20,
		Height:        6,
		Rows:          []string{"line-one", "line-two"},
		Validity:      ProjectionValid,
	}
	res := CheckVirtualProjectionParity(1, "b1", want, proj)
	if !res.Pass {
		t.Fatalf("virtual parity: %+v", res)
	}
}

// TestP4MirrorTimeoutSealedImmutable：mirror callback 超过 timeout 后 entry
// 被看门狗封存（MirrorFailed+DeliveryErrorTimeout）；late return 只进诊断
// 不改写 entry。
func TestP4MirrorTimeoutSealedImmutable(t *testing.T) {
	blockSink := &blockingMirrorSink{release: make(chan struct{})}
	opts := gatewayOptions()
	opts.Clock = SystemClock{}
	gw, err := NewRenderOutputGateway("mt-"+randomID("s"), opts, RenderRouteConfig{
		Primary:            NewDiscardSink("pt-primary"),
		PrimaryOwnership:   SinkOwned,
		ProjectionTargetID: "pt-primary",
		Mirrors: []RenderMirror{{
			Sink:      blockSink,
			Policy:    MirrorBestEffort,
			ApplyMode: MirrorApplyBytes,
			Ownership: SinkBorrowed, // borrowed：gateway 不 Close，watchdog 负责
			Timeout:   50 * time.Millisecond,
		}},
	})
	if err != nil {
		t.Fatalf("gateway: %v", err)
	}
	gw.Run()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = gw.Close(ctx)
	})
	// 提交一笔（mirror callback 阻塞）。
	r := gw.Submit(context.Background(), RenderIntent{
		IntentID: "mt-1",
		Kind:     TransactionFrame,
		Bytes:    []byte("data"),
	})
	if r.Admission.Decision != AdmissionAccepted {
		t.Fatalf("admission: %+v", r.Admission)
	}
	// 等待 watchdog 触发 timeout seal（真实时钟 50ms timeout；sleep 让
	// watchdog goroutine 先执行）。
	time.Sleep(150 * time.Millisecond)
	// 释放 callback（late return）。
	close(blockSink.release)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := gw.Drain(ctx); err != nil {
		t.Fatalf("drain: %v", err)
	}
	// snapshot：entry 以 timeout/failed 记录，late 计数增长。
	snap := gw.Snapshot()
	if len(snap.Mirrors) != 1 {
		t.Fatalf("mirror snapshot: %d", len(snap.Mirrors))
	}
	ms := snap.Mirrors[0]
	if ms.TimedOut < 1 {
		t.Fatalf("expected timed-out mirror entry, got TimedOut=%d", ms.TimedOut)
	}
	if ms.Failed < 1 {
		t.Fatalf("expected failed (timeout) entry, got Failed=%d", ms.Failed)
	}
}

// blockingMirrorSink 阻塞 SubmitMirror 直到 release，之后返回 applied。
type blockingMirrorSink struct {
	release chan struct{}
	calls   int
}

func (b *blockingMirrorSink) Descriptor() TargetDescriptor {
	return TargetDescriptor{SinkID: "block-mirror", Class: TargetClassVirtual, ProjectionTargetID: "pt-block-mirror"}
}

func (b *blockingMirrorSink) SubmitMirror(_ context.Context, _ MirrorEnvelope) MirrorSinkResult {
	b.calls++
	<-b.release
	return MirrorSinkResult{Status: MirrorApplied, ErrorClass: DeliveryErrorNone}
}

func (b *blockingMirrorSink) Snapshot() SinkSnapshot {
	return SinkSnapshot{Descriptor: b.Descriptor(), State: SinkLifecycleOpen}
}

func (b *blockingMirrorSink) Abort(AbortProof) error { return nil }
func (b *blockingMirrorSink) Close(context.Context) error {
	select {
	case <-b.release:
	default:
		close(b.release)
	}
	return nil
}

// TestP4MirrorLateReturnOnlyDiagnostic：timeout seal 后 late return 不改写
// entry/record（Status 保持 timeout/failed）。
func TestP4MirrorLateReturnOnlyDiagnostic(t *testing.T) {
	blockSink := &blockingMirrorSink{release: make(chan struct{})}
	opts := gatewayOptions()
	opts.Clock = SystemClock{}
	gw, err := NewRenderOutputGateway("lr-"+randomID("s"), opts, RenderRouteConfig{
		Primary:            NewDiscardSink("pt-primary"),
		PrimaryOwnership:   SinkOwned,
		ProjectionTargetID: "pt-primary",
		Mirrors: []RenderMirror{{
			Sink:      blockSink,
			Policy:    MirrorBestEffort,
			ApplyMode: MirrorApplyBytes,
			Ownership: SinkBorrowed,
			Timeout:   50 * time.Millisecond,
		}},
	})
	if err != nil {
		t.Fatalf("gateway: %v", err)
	}
	gw.Run()
	r := gw.Submit(context.Background(), RenderIntent{
		IntentID: "lr-1",
		Kind:     TransactionFrame,
		Bytes:    []byte("data"),
	})
	if r.Admission.Decision != AdmissionAccepted {
		t.Fatalf("admission: %+v", r.Admission)
	}
	time.Sleep(150 * time.Millisecond) // watchdog seal timeout
	close(blockSink.release)           // late return（applied）
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := gw.Drain(ctx); err != nil {
		t.Fatalf("drain: %v", err)
	}
	// record 中 mirror entry 保持 timeout/failed，不因 late applied 改写。
	records := gw.RecentDeliveries(10)
	found := false
	for _, rec := range records {
		if rec.Batch.BatchID == r.BatchID {
			found = true
			if len(rec.Mirrors) != 1 {
				t.Fatalf("mirror records: %d", len(rec.Mirrors))
			}
			m := rec.Mirrors[0]
			if m.Status != MirrorFailed || m.ErrorClass != DeliveryErrorTimeout {
				t.Fatalf("late return must not rewrite sealed entry: %+v", m)
			}
			break
		}
	}
	if !found {
		t.Fatal("record missing")
	}
}

// TestP4PhysicalPartialVirtualUnknown：primary partial（短写）→ virtual
// mirror 标 unknown + NonAuthoritative，不作为 recovery source。
func TestP4PhysicalPartialVirtualUnknown(t *testing.T) {
	shortWriter := &shortWriteWriter{n: 2}
	phys := NewPhysicalSink(TargetDescriptor{
		SinkID:             "short-primary",
		Class:              TargetClassPhysical,
		ProjectionTargetID: "pt-primary",
	}, shortWriter, PhysicalSinkOptions{})
	emu := newFakeEmulator(20, 6)
	virtual := NewVirtualTerminalSink("pt-virtual", emu, VirtualSinkOptions{})
	gw, err := NewRenderOutputGateway("partial-"+randomID("s"), gatewayOptions(), RenderRouteConfig{
		Primary:            phys,
		PrimaryOwnership:   SinkOwned,
		ProjectionTargetID: "pt-primary",
		Mirrors: []RenderMirror{{
			Sink:      virtual,
			Policy:    MirrorBestEffort,
			ApplyMode: MirrorApplyBytes,
			Ownership: SinkOwned,
			Timeout:   time.Second,
		}},
	})
	if err != nil {
		t.Fatalf("gateway: %v", err)
	}
	gw.Run()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = gw.Close(ctx)
	})
	r := gw.Submit(context.Background(), RenderIntent{
		IntentID: "pp-1",
		Kind:     TransactionFrame,
		Bytes:    []byte("partial-content"),
	})
	if r.Primary == nil || r.Primary.Status != DeliveryUnknownPartial {
		t.Fatalf("expected partial primary: %+v", r)
	}
	// best_effort + partial → effective mode 降 metadata_only（非 attempted）。
	for _, ad := range r.MirrorAdmissions {
		if ad.EffectiveApplyMode != MirrorApplyMetadataOnly {
			t.Fatalf("partial primary must downgrade effective mode: %+v", ad)
		}
		if !ad.NonAuthoritative {
			t.Fatalf("partial primary must be non-authoritative: %+v", ad)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := gw.Drain(ctx); err != nil {
		t.Fatalf("drain: %v", err)
	}
	proj := virtual.Projection()
	// metadata_only skip：未应用 bytes——partial 不得作为 committed 投影。
	if proj.Validity == ProjectionValid {
		t.Fatalf("virtual must not project partial primary as valid: %+v", proj)
	}
	for _, row := range proj.Rows {
		if row != "" {
			t.Fatalf("metadata_only must not apply bytes: %+v", proj.Rows)
		}
	}
}

// TestP4PhysicalPartialVirtualAttempted：partial + MirrorAttempted 例外——
// 应用 attempted bytes 但保持 non-authoritative/unknown（7.3 策略 3）。
func TestP4PhysicalPartialVirtualAttempted(t *testing.T) {
	shortWriter := &shortWriteWriter{n: 2}
	phys := NewPhysicalSink(TargetDescriptor{
		SinkID:             "short-attempted",
		Class:              TargetClassPhysical,
		ProjectionTargetID: "pt-primary",
	}, shortWriter, PhysicalSinkOptions{})
	emu := newFakeEmulator(20, 6)
	virtual := NewVirtualTerminalSink("pt-virtual", emu, VirtualSinkOptions{})
	gw, err := NewRenderOutputGateway("attempted-"+randomID("s"), gatewayOptions(), RenderRouteConfig{
		Primary:            phys,
		PrimaryOwnership:   SinkOwned,
		ProjectionTargetID: "pt-primary",
		Mirrors: []RenderMirror{{
			Sink:      virtual,
			Policy:    MirrorAttempted, // 显式 attempted：允许应用 bytes
			ApplyMode: MirrorApplyBytes,
			Ownership: SinkOwned,
			Timeout:   time.Second,
		}},
	})
	if err != nil {
		t.Fatalf("gateway: %v", err)
	}
	gw.Run()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = gw.Close(ctx)
	})
	r := gw.Submit(context.Background(), RenderIntent{
		IntentID: "pa-1",
		Kind:     TransactionFrame,
		Bytes:    []byte("attempted-bytes"),
	})
	if r.Primary == nil || r.Primary.Status != DeliveryUnknownPartial {
		t.Fatalf("expected partial primary: %+v", r)
	}
	// attempted 例外：effective mode 保持 bytes。
	for _, ad := range r.MirrorAdmissions {
		if ad.EffectiveApplyMode != MirrorApplyBytes {
			t.Fatalf("attempted must keep bytes mode: %+v", ad)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := gw.Drain(ctx); err != nil {
		t.Fatalf("drain: %v", err)
	}
	proj := virtual.Projection()
	// nonAuth 应用：即使 bytes 应用了也保持 unknown（不可作 recovery）。
	if proj.NonAuthoritative != true {
		t.Fatalf("attempted virtual must be non-authoritative: %+v", proj)
	}
	if proj.Validity == ProjectionValid {
		t.Fatalf("attempted partial must not be valid: %+v", proj)
	}
}

// shortWriteWriter 声称写入少于请求字节并返回错误：err!=nil 的短写不可
// 补全（错误表明写入被中断），物理 sink 标 unknown partial。无错误的干净
// 短写会被 PhysicalSink 自动补全为 committed，无法构造 partial 场景。
type shortWriteWriter struct{ n int }

func (w *shortWriteWriter) Write(p []byte) (int, error) {
	if w.n > len(p) {
		w.n = len(p)
	}
	return w.n, errors.New("short write")
}
