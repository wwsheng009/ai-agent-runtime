package output

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ============================================================================
// RenderTestFixture（11.2；Phase 0 交付物）
// ============================================================================

// RenderTestFixture 组合一个 gateway + primary + capture mirror + virtual sink
// + fake clock，供 unified renderer 测试使用。所有组件在 t.Cleanup 中关闭。
type RenderTestFixture struct {
	T       *testing.T
	Clock   *FakeClock
	Gateway *RenderOutputGateway
	Primary *MemorySink
	Capture *CaptureSink
	Virtual *VirtualTerminalSink
	Session string
	Start   time.Time
}

type FixtureOption func(*fixtureConfig)

type fixtureConfig struct {
	primaryProjection string
	captureProjection string
	virtualProjection string
	withCapture       bool
	stream            []byte
	withVirtual       bool
	mirrors           []RenderMirror
}

// WithPrimaryCapture 让 primary 兼作 capture class（Phase 2 语义前瞻）。
func WithPrimaryCapture(projectionTargetID string) FixtureOption {
	return func(c *fixtureConfig) {
		c.primaryProjection = projectionTargetID
		c.withCapture = true
	}
}

// WithProjectionTargetID 指定 primary projection target id。
func WithProjectionTargetID(id string) FixtureOption {
	return func(c *fixtureConfig) {
		c.primaryProjection = id
	}
}

// WithPrimaryStream 提供模仿真实屏幕的初始 stream。
func WithPrimaryStream(s []byte) FixtureOption {
	return func(c *fixtureConfig) {
		c.stream = s
	}
}

// WithVirtualTerminal 启用 virtual mirror sink。
func WithVirtualTerminal(projectionTargetID string) FixtureOption {
	return func(c *fixtureConfig) {
		c.withVirtual = true
		c.virtualProjection = projectionTargetID
	}
}

// WithCaptureMirror 添加 capture mirror。
func WithCaptureMirror(projectionTargetID string) FixtureOption {
	return func(c *fixtureConfig) {
		c.withCapture = true
		c.captureProjection = projectionTargetID
	}
}

// WithMirror 添加任意 mirror。
func WithMirror(m RenderMirror) FixtureOption {
	return func(c *fixtureConfig) {
		c.mirrors = append(c.mirrors, m)
	}
}

// NewRenderTestFixture 构造 fixture；t.Cleanup 自动 Close。
func NewRenderTestFixture(t *testing.T, opts ...FixtureOption) *RenderTestFixture {
	t.Helper()
	cfg := fixtureConfig{primaryProjection: "primary-test"}
	for _, o := range opts {
		o(&cfg)
	}
	start := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	clock := NewFakeClock(start)
	sess := "test-session-" + randomID("s")

	primaryDesc := TargetDescriptor{
		SinkID:             "memory-primary",
		Class:              TargetClassPhysical,
		ProjectionTargetID: cfg.primaryProjection,
	}
	if cfg.withVirtual {
		primaryDesc.Class = TargetClassVirtual
	}
	primary := NewMemorySink(primaryDesc)

	route := RenderRouteConfig{
		Primary:            primary,
		PrimaryOwnership:   SinkOwned,
		ProjectionTargetID: cfg.primaryProjection,
	}
	var capture *CaptureSink
	if cfg.withCapture {
		capture = NewCaptureSink("capture-test", CaptureOptions{
			MaxEntries:   64,
			MaxBytes:     1 << 16,
			StorePayload: true,
		})
		route.Mirrors = append(route.Mirrors, RenderMirror{
			Sink:      capture,
			Policy:    MirrorBestEffort,
			ApplyMode: MirrorApplyBytes,
			Ownership: SinkOwned,
			Timeout:   1 * time.Second,
		})
	}
	var virtual *VirtualTerminalSink
	if cfg.withVirtual {
		virtual = NewVirtualTerminalSink("virtual-test", newFakeEmulator(80, 24), VirtualSinkOptions{})
		route.Mirrors = append(route.Mirrors, RenderMirror{
			Sink:      virtual,
			Policy:    MirrorBestEffort,
			ApplyMode: MirrorApplyBytes,
			Ownership: SinkOwned,
			Timeout:   1 * time.Second,
		})
	}
	route.Mirrors = append(route.Mirrors, cfg.mirrors...)

	gw, err := NewRenderOutputGateway(sess, RenderGatewayOptions{
		Clock:                 clock,
		CloseTimeout:          2 * time.Second,
		ReconfigureTimeout:    2 * time.Second,
		MaxIntentBytes:        1 << 20,
		MirrorQueueCapacity:   128,
		DeliveryJournalLimit:  JournalLimit{MaxItems: 128, MaxBytes: 1 << 20},
		EventJournalLimit:     JournalLimit{MaxItems: 256, MaxBytes: 1 << 20},
		MaxSubscriptions:      16,
		MaxSubscriptionBuffer: 64,
	}, route)
	if err != nil {
		t.Fatalf("NewRenderOutputGateway: %v", err)
	}
	gw.Run()
	f := &RenderTestFixture{
		T:       t,
		Clock:   clock,
		Gateway: gw,
		Primary: primary,
		Capture: capture,
		Virtual: virtual,
		Session: sess,
		Start:   start,
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = f.Gateway.Close(ctx)
	})
	return f
}

// Port 返回 bound submit port（Phase 1 TerminalSessionWithOutput 使用）。
func (f *RenderTestFixture) Port() RenderSubmitPort { return f.Gateway }

// SubmitIntent 便捷方法：提交一笔 intent 并返回 receipt。
func (f *RenderTestFixture) SubmitIntent(t *testing.T, kind TransactionKind, bytes []byte) OutputReceipt {
	t.Helper()
	return f.Gateway.Submit(context.Background(), RenderIntent{
		IntentID: randomID("int"),
		Kind:     kind,
		Source:   "fixture",
		Cause:    "test",
		Bytes:    bytes,
	})
}

// Advance 推进 fake clock（元数据断言用）。
func (f *RenderTestFixture) Advance(d time.Duration) { f.Clock.Advance(d) }

// ============================================================================
// 标准契约测试（11.1 Gateway contract tests；对所有标准 sink 运行）
// ============================================================================

// gatewayOptions 是契约测试的标准配置。
func gatewayOptions() RenderGatewayOptions {
	return RenderGatewayOptions{
		Clock:                 NewFakeClock(time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)),
		CloseTimeout:          3 * time.Second,
		ReconfigureTimeout:    3 * time.Second,
		MaxIntentBytes:        1 << 20,
		MirrorQueueCapacity:   16,
		DeliveryJournalLimit:  JournalLimit{MaxItems: 64, MaxBytes: 1 << 20},
		EventJournalLimit:     JournalLimit{MaxItems: 64, MaxBytes: 1 << 20},
		MaxSubscriptions:      8,
		MaxSubscriptionBuffer: 32,
	}
}

func mustGateway(t *testing.T, primary RenderOutputSink, mirrors ...RenderMirror) *RenderOutputGateway {
	t.Helper()
	desc := primary.Descriptor()
	route := RenderRouteConfig{
		Primary:            primary,
		PrimaryOwnership:   SinkOwned,
		ProjectionTargetID: desc.ProjectionTargetID,
		Mirrors:            mirrors,
	}
	gw, err := NewRenderOutputGateway("contract-session-"+randomID("s"), gatewayOptions(), route)
	if err != nil {
		t.Fatalf("NewRenderOutputGateway: %v", err)
	}
	gw.Run()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = gw.Close(ctx)
	})
	return gw
}

func memoryPrimary(t *testing.T) *MemorySink {
	t.Helper()
	return NewMemorySink(TargetDescriptor{
		SinkID:             "memory-primary",
		Class:              TargetClassPhysical,
		ProjectionTargetID: "pt-primary",
	})
}

func captureMirror(t *testing.T) *CaptureSink {
	t.Helper()
	return NewCaptureSink("pt-capture", CaptureOptions{
		MaxEntries:   32,
		MaxBytes:     1 << 16,
		StorePayload: true,
	})
}

// submitOK 提交并断言 accepted + committed。
func submitOK(t *testing.T, gw *RenderOutputGateway, kind TransactionKind, bytes []byte) OutputReceipt {
	t.Helper()
	r := gw.Submit(context.Background(), RenderIntent{
		IntentID: randomID("int"),
		Kind:     kind,
		Source:   "contract-test",
		Cause:    "submitOK",
		Bytes:    bytes,
	})
	if r.Admission.Decision != AdmissionAccepted {
		t.Fatalf("expected accepted, got %s (%s)", r.Admission.Decision, r.Admission.Message)
	}
	if r.Primary == nil {
		t.Fatalf("expected non-nil primary receipt")
	}
	if r.Primary.Status != DeliveryCommitted {
		t.Fatalf("expected committed, got %s", r.Primary.Status)
	}
	return r
}

// TestGatewayStampImmutability：intent 被深拷贝并由 gateway 盖章，
// producer 不能伪造 sequence/route epoch/target ID。
func TestGatewayStampImmutability(t *testing.T) {
	gw := mustGateway(t, memoryPrimary(t))
	intent := RenderIntent{
		IntentID: "int-1",
		Kind:     TransactionFrame,
		Source:   "test",
		Cause:    "stamp",
		Bytes:    []byte("hello"),
	}
	r := gw.Submit(context.Background(), intent)
	if r.Primary == nil || r.Primary.Status != DeliveryCommitted {
		t.Fatalf("unexpected receipt: %+v", r)
	}
	if r.Sequence != 1 {
		t.Fatalf("expected sequence 1, got %d", r.Sequence)
	}
	if r.RouteEpoch != 1 {
		t.Fatalf("expected route epoch 1, got %d", r.RouteEpoch)
	}
	if r.ProjectionTargetID != "pt-primary" {
		t.Fatalf("expected stamped target id, got %q", r.ProjectionTargetID)
	}
	// producer 无法篡改 batch。
	intent.Kind = TransactionBell
	intent.Bytes = []byte("tampered")
	batches := gw.RecentDeliveries(10)
	if len(batches) == 0 {
		t.Fatal("expected at least one delivery record")
	}
	if batches[0].Batch.Kind != TransactionFrame {
		t.Fatalf("record kind changed by producer mutation: %s", batches[0].Batch.Kind)
	}
	if string(batches[0].Batch.BytesHash) != "" && batches[0].Batch.BytesLength != 5 {
		t.Fatalf("record bytes length changed: %d", batches[0].Batch.BytesLength)
	}
	// sequence 单调不复用。
	r2 := submitOK(t, gw, TransactionFrame, []byte("again"))
	if r2.Sequence <= r.Sequence {
		t.Fatalf("sequence not monotonic: %d <= %d", r2.Sequence, r.Sequence)
	}
}

// TestGatewayPrimaryIdentityUsesFrozenDescriptor verifies that SinkID remains
// distinct from ProjectionTargetID and that primary lifecycle events use the
// same invocation identity as the returned receipt.
func TestGatewayPrimaryIdentityUsesFrozenDescriptor(t *testing.T) {
	gw := mustGateway(t, memoryPrimary(t))
	r := submitOK(t, gw, TransactionFrame, []byte("identity"))
	if r.Primary.SinkID != "memory-primary" {
		t.Fatalf("primary sink id: got %q want %q", r.Primary.SinkID, "memory-primary")
	}
	if r.Primary.ProjectionTargetID != "pt-primary" {
		t.Fatalf("primary projection target: got %q want %q",
			r.Primary.ProjectionTargetID, "pt-primary")
	}
	if r.Primary.InvocationID == 0 {
		t.Fatal("primary invocation id must be non-zero")
	}

	var started, completed uint64
	for _, ev := range gw.RecentEvents(64) {
		if ev.BatchID != r.BatchID {
			continue
		}
		switch ev.Kind {
		case EventPrimaryStarted:
			started = ev.InvocationID
		case EventPrimaryCompleted:
			completed = ev.InvocationID
		}
	}
	if started != r.Primary.InvocationID || completed != r.Primary.InvocationID {
		t.Fatalf("invocation identity mismatch: started=%d completed=%d receipt=%d",
			started, completed, r.Primary.InvocationID)
	}
}

// TestGatewayMissingCapturedPrimaryFailsClosed exercises the defensive path
// directly: a corrupted admitted batch must yield a target-level rejection,
// not panic while dereferencing a nil sink.
func TestGatewayMissingCapturedPrimaryFailsClosed(t *testing.T) {
	gw := mustGateway(t, memoryPrimary(t))
	mirror := NewMemorySink(TargetDescriptor{
		SinkID:             "frozen-mirror",
		Class:              TargetClassCapture,
		ProjectionTargetID: "pt-frozen-mirror",
	})
	frozenMirrorSlot := newMirrorSlot(gw, 0, RenderMirror{
		Sink:      mirror,
		Policy:    MirrorBestEffort,
		ApplyMode: MirrorApplyBytes,
		Ownership: SinkBorrowed,
		Timeout:   time.Second,
	})
	var typedNil *MemorySink
	cases := []struct {
		name string
		sink RenderOutputSink
	}{
		{name: "nil"},
		{name: "typed nil", sink: typedNil},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seq := uint64(i + 1)
			batch := RenderBatch{
				RenderIntent: RenderIntent{
					IntentID: "missing-primary",
					Kind:     TransactionFrame,
					Bytes:    []byte("x"),
				},
				SessionID:             gw.sessID,
				Sequence:              seq,
				BatchID:               fmt.Sprintf("missing-primary-batch-%d", seq),
				RouteEpoch:            1,
				ProjectionTargetID:    "pt-primary",
				ProjectionTargetClass: TargetClassPhysical,
				PreparedAt:            gw.clock.Now(),
				primarySink:           tc.sink,
				primaryDesc: TargetDescriptor{
					SinkID:             "memory-primary",
					Class:              TargetClassPhysical,
					ProjectionTargetID: "pt-primary",
				},
				mirrorSlots: []*mirrorSlot{frozenMirrorSlot},
			}
			gw.mu.Lock()
			gw.sequence = batch.Sequence
			gw.pendingStamps++
			gw.stats.admissionAccepted++
			gw.mu.Unlock()
			gw.initRecordSlot(batch, len(batch.mirrorSlots))

			r := gw.deliver(context.Background(), batch)
			if r.Primary == nil {
				t.Fatal("admitted batch must return a primary receipt")
			}
			if r.Primary.Status != DeliveryRejected ||
				r.Primary.ErrorClass != DeliveryErrorAbandoned {
				t.Fatalf("missing primary did not fail closed: %+v", r.Primary)
			}
			if !r.Primary.Synthetic || r.Primary.CallbackReturned ||
				r.Primary.InvocationID != 0 || r.TargetInvoked {
				t.Fatalf("missing primary callback facts are inconsistent: %+v", r)
			}
			if r.Primary.SinkID != "memory-primary" {
				t.Fatalf("defensive receipt lost frozen sink identity: %+v", r.Primary)
			}
			if len(r.MirrorAdmissions) != 1 || r.MirrorAdmissions[0].Scheduled ||
				r.MirrorAdmissions[0].ErrorClass != DeliveryErrorAbandoned {
				t.Fatalf("frozen mirror did not reach terminal skip: %+v", r.MirrorAdmissions)
			}
			for _, ev := range gw.RecentEvents(64) {
				if ev.BatchID == r.BatchID && ev.Kind == EventPrimaryStarted {
					t.Fatalf("uninvoked primary published started event: %+v", ev)
				}
			}
			var found *DeliveryRecord
			for _, rec := range gw.RecentDeliveries(10) {
				if rec.Batch.BatchID == r.BatchID {
					cp := rec
					found = &cp
					break
				}
			}
			if found == nil || found.Output.Primary == nil ||
				!found.Output.Primary.Synthetic || len(found.Mirrors) != 1 {
				t.Fatalf("synthetic outcome was not completely sealed: %+v", found)
			}
		})
	}
	if got := len(mirror.SnapshotBatches()); got != 0 {
		t.Fatalf("fail-closed path invoked frozen mirror %d times", got)
	}
}

// TestGatewayPreAdmissionRejection：pre-admission rejection 使用
// Primary=nil、Sequence=0；target-level 才用非空 primary。
func TestGatewayPreAdmissionRejection(t *testing.T) {
	gw := mustGateway(t, memoryPrimary(t))
	writes := gw.RecentDeliveries(10)
	_ = writes

	// 空 bytes。
	r := gw.Submit(context.Background(), RenderIntent{
		IntentID: "empty",
		Kind:     TransactionFrame,
		Bytes:    nil,
	})
	if r.Admission.Decision != AdmissionRejected {
		t.Fatalf("expected rejected, got %s", r.Admission.Decision)
	}
	if r.Primary != nil || r.Sequence != 0 {
		t.Fatalf("pre-admission rejection must have Primary=nil, Sequence=0; got %+v", r)
	}

	// 无 kind。
	r = gw.Submit(context.Background(), RenderIntent{
		IntentID: "nokind",
		Bytes:    []byte("x"),
	})
	if r.Admission.Decision != AdmissionRejected || r.Primary != nil {
		t.Fatalf("expected rejected for missing kind: %+v", r)
	}

	// 超 size。
	big := make([]byte, gatewayOptions().MaxIntentBytes+1)
	r = gw.Submit(context.Background(), RenderIntent{
		IntentID: "big",
		Kind:     TransactionFrame,
		Bytes:    big,
	})
	if r.Admission.Decision != AdmissionRejected || r.Admission.ErrorClass != DeliveryErrorOversized {
		t.Fatalf("expected oversized rejection: %+v", r.Admission)
	}

	// history epoch 不允许的 kind。
	ep := uint64(3)
	r = gw.Submit(context.Background(), RenderIntent{
		IntentID:     "hx",
		Kind:         TransactionFrame,
		Bytes:        []byte("x"),
		HistoryEpoch: &ep,
	})
	if r.Admission.Decision != AdmissionRejected {
		t.Fatalf("expected rejection for history epoch on non-history kind: %+v", r)
	}
}

func TestGatewayRejectsTypedNilRouteSinks(t *testing.T) {
	var typedNil *MemorySink
	validPrimary := memoryPrimary(t)
	cases := []struct {
		name  string
		route RenderRouteConfig
	}{
		{
			name: "primary",
			route: RenderRouteConfig{
				Primary:            typedNil,
				PrimaryOwnership:   SinkBorrowed,
				ProjectionTargetID: "pt-primary",
			},
		},
		{
			name: "mirror",
			route: RenderRouteConfig{
				Primary:            validPrimary,
				PrimaryOwnership:   SinkBorrowed,
				ProjectionTargetID: validPrimary.Descriptor().ProjectionTargetID,
				Mirrors: []RenderMirror{{
					Sink:      typedNil,
					Policy:    MirrorBestEffort,
					ApplyMode: MirrorApplyMetadataOnly,
					Ownership: SinkBorrowed,
					Timeout:   time.Second,
				}},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewRenderOutputGateway(
				"typed-nil-"+tc.name, gatewayOptions(), tc.route,
			); err == nil || ClassOf(err) != DeliveryErrorInvalid {
				t.Fatalf("typed-nil %s must be rejected as invalid, got %v", tc.name, err)
			}
		})
	}
}

func TestGatewayNilControlContexts(t *testing.T) {
	gw := mustGateway(t, memoryPrimary(t))
	if err := gw.WaitIdle(nil); err != nil {
		t.Fatalf("WaitIdle(nil): %v", err)
	}
	if err := gw.Drain(nil); err != nil {
		t.Fatalf("Drain(nil): %v", err)
	}
	if err := gw.Close(nil); err != nil {
		t.Fatalf("Close(nil): %v", err)
	}
}

func TestGatewaySnapshotCountsUnsealedRecords(t *testing.T) {
	gw := mustGateway(t, memoryPrimary(t))
	batch := RenderBatch{SessionID: gw.sessID, Sequence: 1, BatchID: "unsealed"}
	gw.initRecordSlot(batch, 0)
	t.Cleanup(func() {
		gw.recordsMu.Lock()
		delete(gw.recordSlots, batch.Sequence)
		gw.recordsMu.Unlock()
	})
	if got := gw.Snapshot().DeliveryRecordsUnsealed; got != 1 {
		t.Fatalf("unsealed delivery records=%d, want 1", got)
	}
}

// TestGatewayStateMachineClose：关闭后提交返回 rejected/closed 且 Sequence=0。
func TestGatewayStateMachineClose(t *testing.T) {
	gw := mustGateway(t, memoryPrimary(t))
	submitOK(t, gw, TransactionFrame, []byte("first"))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := gw.Close(ctx); err != nil {
		t.Fatalf("close: %v", err)
	}
	if gw.stateOf() != GatewayClosed {
		t.Fatalf("expected closed state, got %s", gw.stateOf())
	}
	r := gw.Submit(context.Background(), RenderIntent{
		IntentID: "after-close",
		Kind:     TransactionFrame,
		Bytes:    []byte("late"),
	})
	if r.Admission.Decision != AdmissionRejected || r.Admission.ErrorClass != DeliveryErrorClosed {
		t.Fatalf("expected closed rejection, got %+v", r.Admission)
	}
	if r.Primary != nil || r.Sequence != 0 {
		t.Fatalf("closed rejection must have Primary=nil, Sequence=0")
	}
}

// TestGatewayCloseIdempotent：Close 幂等，不并发关闭同一 sink 两次。
func TestGatewayCloseIdempotent(t *testing.T) {
	primary := memoryPrimary(t)
	gw := mustGateway(t, primary)
	ctx := context.Background()
	if err := gw.Close(ctx); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := gw.Close(ctx); err != nil {
		t.Fatalf("second close should be idempotent: %v", err)
	}
	snap := primary.Snapshot()
	if snap.State != SinkLifecycleClosed {
		t.Fatalf("primary should be closed (owned), got %s", snap.State)
	}
}

// TestGatewayMirrorOutcomeAware：三种 mirror policy × 四种 primary outcome，
// 断言 admission 的 effective mode / scheduled / skip。
func TestGatewayMirrorOutcomeAware(t *testing.T) {
	// primary 用 FaultSink 以便制造多种 outcome。
	fault := NewFaultSink(TargetDescriptor{
		SinkID:             "fault-primary",
		Class:              TargetClassPhysical,
		ProjectionTargetID: "pt-primary",
	})
	m := NewMemorySink(TargetDescriptor{
		SinkID:             "memory-mirror",
		Class:              TargetClassCapture,
		ProjectionTargetID: "pt-mirror",
	})
	gw := mustGateway(t, fault, RenderMirror{
		Sink:      m,
		Policy:    MirrorCommittedOnly,
		ApplyMode: MirrorApplyBytes,
		Ownership: SinkOwned,
		Timeout:   1 * time.Second,
	})

	cases := []struct {
		name   string
		fault  FaultKind
		expect bool // committed_only 是否 schedule
	}{
		{"committed", FaultNone, true},
		{"zero", FaultZero, false},
		{"deferred", FaultReject, false},
		{"unknown", FaultPartial, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fault.SetKind(tc.fault)
			r := gw.Submit(context.Background(), RenderIntent{
				IntentID: randomID("m"),
				Kind:     TransactionFrame,
				Bytes:    []byte("case-" + tc.name),
			})
			if r.Admission.Decision != AdmissionAccepted {
				t.Fatalf("admission: %+v", r.Admission)
			}
			if len(r.MirrorAdmissions) != 1 {
				t.Fatalf("expected 1 mirror admission, got %d", len(r.MirrorAdmissions))
			}
			ad := r.MirrorAdmissions[0]
			if ad.Scheduled != tc.expect {
				t.Fatalf("mirror scheduled=%v, want %v (primary status %s)", ad.Scheduled, tc.expect, r.Primary.Status)
			}
			if !tc.expect && ad.SkipReason != MirrorSkipPrimaryNotCommitted {
				t.Fatalf("expected skip reason primary_not_committed, got %q", ad.SkipReason)
			}
			if tc.expect && ad.EffectiveApplyMode != MirrorApplyBytes {
				t.Fatalf("expected effective mode bytes, got %s", ad.EffectiveApplyMode)
			}
		})
	}
}

// TestGatewaySerialBoundary：primary callback 阻塞时第二个 submit 不进入 sink
// （serial slot），释放后按序继续。
func TestGatewaySerialBoundary(t *testing.T) {
	blocker := NewFaultSink(TargetDescriptor{
		SinkID:             "blocker",
		Class:              TargetClassPhysical,
		ProjectionTargetID: "pt-primary",
	})
	blocker.SetKind(FaultBlock)
	gw := mustGateway(t, blocker)

	done := make(chan OutputReceipt, 1)
	go func() {
		done <- gw.Submit(context.Background(), RenderIntent{
			IntentID: "b1",
			Kind:     TransactionFrame,
			Bytes:    []byte("blocked"),
		})
	}()
	// 等第一个进入 callback。
	deadline := time.Now().Add(2 * time.Second)
	for blocker.DrainCalls() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if blocker.DrainCalls() == 0 {
		t.Fatal("first submit never reached sink")
	}
	// 第二个 submit 必须等待（serial）。
	second := make(chan OutputReceipt, 1)
	go func() {
		second <- gw.Submit(context.Background(), RenderIntent{
			IntentID: "b2",
			Kind:     TransactionFrame,
			Bytes:    []byte("queued"),
		})
	}()
	time.Sleep(50 * time.Millisecond)
	if n := blocker.DrainCalls(); n != 1 {
		t.Fatalf("second submit entered sink while first blocked: calls=%d", n)
	}
	blocker.Release()
	<-done
	<-second
}

// TestGatewayWaitIdleVsDrain：WaitIdle 不等待 mirror，Drain 等待全部。
func TestGatewayWaitIdleVsDrain(t *testing.T) {
	gw := mustGateway(t, memoryPrimary(t))
	submitOK(t, gw, TransactionFrame, []byte("a"))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := gw.WaitIdle(ctx); err != nil {
		t.Fatalf("wait idle: %v", err)
	}
	if err := gw.Drain(ctx); err != nil {
		t.Fatalf("drain: %v", err)
	}
}

// TestGatewayFaultPanicRecovery：sink panic 被 gateway 捕获为 unknown。
func TestGatewayFaultPanicRecovery(t *testing.T) {
	fault := NewFaultSink(TargetDescriptor{
		SinkID:             "panic-primary",
		Class:              TargetClassPhysical,
		ProjectionTargetID: "pt-primary",
	})
	fault.SetKind(FaultPanic)
	gw := mustGateway(t, fault)
	r := gw.Submit(context.Background(), RenderIntent{
		IntentID: "panic",
		Kind:     TransactionFrame,
		Bytes:    []byte("boom"),
	})
	if r.Admission.Decision != AdmissionAccepted {
		t.Fatalf("admission: %+v", r.Admission)
	}
	if r.Primary == nil {
		t.Fatal("expected non-nil primary after panic guard")
	}
	if r.Primary.Status != DeliveryUnknownPartial {
		t.Fatalf("expected unknown_partial after panic, got %s", r.Primary.Status)
	}
	if r.Primary.ErrorClass != DeliveryErrorSink {
		t.Fatalf("expected sink error class, got %s", r.Primary.ErrorClass)
	}
	// gateway 仍可用（先恢复 fault kind）。
	fault.SetKind(FaultNone)
	r2 := submitOK(t, gw, TransactionBell, []byte("recovered"))
	if r2.Sequence <= r.Sequence {
		t.Fatalf("sequence not monotonic after panic: %d <= %d", r2.Sequence, r.Sequence)
	}
}

// TestCaptureSinkBounded：capture 超限只淘汰旧条目，不阻塞。
func TestCaptureSinkBounded(t *testing.T) {
	c := NewCaptureSink("pt-capture", CaptureOptions{
		MaxEntries:   4,
		MaxBytes:     1 << 20,
		StorePayload: true,
	})
	for i := 0; i < 10; i++ {
		env := MirrorEnvelope{
			MirrorEntryRef: MirrorEntryRef{
				EntryID:     randomID("e"),
				MirrorIndex: 0,
				SinkID:      "capture",
			},
			RenderBatch: RenderBatch{
				RenderIntent: RenderIntent{
					IntentID: randomID("i"),
					Kind:     TransactionFrame,
					Bytes:    []byte("payload-"),
				},
				BatchID: randomID("b"),
			},
		}
		res := c.SubmitMirror(context.Background(), env)
		if res.Status != MirrorApplied {
			t.Fatalf("capture submit: %s", res.Status)
		}
	}
	entries := c.Entries()
	if len(entries) > 4 {
		t.Fatalf("capture exceeded max entries: %d", len(entries))
	}
	// payload 可回读。
	if entries[len(entries)-1].CaptureEntryID == "" {
		t.Fatal("entry id empty")
	}
	p, cls := c.Payload(entries[len(entries)-1].CaptureEntryID)
	if cls != CapturePayloadErrorNone || len(p) == 0 {
		t.Fatalf("payload readback: class=%s len=%d", cls, len(p))
	}
}

// TestFakeClockDeterministic：fake clock 控制 journal 时间，不依赖 sleep。
func TestFakeClockDeterministic(t *testing.T) {
	start := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	clk := NewFakeClock(start)
	if !clk.Now().Equal(start) {
		t.Fatalf("clock start: %v", clk.Now())
	}
	tm := clk.NewTimer(5 * time.Second)
	select {
	case <-tm.C():
		t.Fatal("timer fired before advance")
	default:
	}
	clk.Advance(5 * time.Second)
	select {
	case <-tm.C():
	default:
		t.Fatal("timer did not fire after advance")
	}
	if !clk.Now().Equal(start.Add(5 * time.Second)) {
		t.Fatalf("clock now after advance: %v", clk.Now())
	}
}

// TestEventHubSubscription：订阅收到事件、关闭无 panic。
func TestEventHubSubscription(t *testing.T) {
	gw := mustGateway(t, memoryPrimary(t))
	sub, err := gw.Subscribe(16)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	submitOK(t, gw, TransactionFrame, []byte("eventful"))
	events := gw.RecentEvents(64)
	if len(events) == 0 {
		t.Fatal("no events recorded")
	}
	sub.Close()
	// 关闭后 publish 不 panic。
	submitOK(t, gw, TransactionFrame, []byte("after-close-sub"))
}

// TestGatewaySnapshotEquations：snapshot 的累计等式成立。
func TestGatewaySnapshotEquations(t *testing.T) {
	gw := mustGateway(t, memoryPrimary(t))
	for i := 0; i < 3; i++ {
		submitOK(t, gw, TransactionFrame, []byte("x"))
	}
	snap := gw.Snapshot()
	if snap.AdmissionAccepted != 3 {
		t.Fatalf("admission accepted: %d", snap.AdmissionAccepted)
	}
	if snap.PrimaryCommitted != 3 {
		t.Fatalf("primary committed: %d", snap.PrimaryCommitted)
	}
	if snap.LastSequence != 3 {
		t.Fatalf("last sequence: %d", snap.LastSequence)
	}
	if snap.SchemaVersion != SchemaVersion {
		t.Fatalf("schema version: %d", snap.SchemaVersion)
	}
	recs := gw.RecentDeliveries(10)
	if len(recs) != 3 {
		t.Fatalf("expected 3 sealed records, got %d", len(recs))
	}
	for _, r := range recs {
		if r.SchemaVersion != SchemaVersion {
			t.Fatalf("record schema: %d", r.SchemaVersion)
		}
		if r.Output.Primary == nil || r.Output.Primary.Status != DeliveryCommitted {
			t.Fatalf("record primary: %+v", r.Output.Primary)
		}
	}
}

// TestMemorySinkDeepCopy：sink 收到的 batch 是深拷贝，修改不影响 gateway。
func TestMemorySinkDeepCopy(t *testing.T) {
	primary := memoryPrimary(t)
	gw := mustGateway(t, primary)
	original := []byte("deep-copy-payload")
	submitOK(t, gw, TransactionFrame, original)
	batches := primary.SnapshotBatches()
	if len(batches) != 1 {
		t.Fatalf("expected 1 batch, got %d", len(batches))
	}
	batches[0].Bytes[0] = 'X'
	// gateway 侧无影响：新提交与旧不同。
	submitOK(t, gw, TransactionFrame, []byte("other"))
	batches2 := primary.SnapshotBatches()
	if len(batches2) != 2 {
		t.Fatalf("expected 2 batches, got %d", len(batches2))
	}
	if string(batches2[0].Bytes) != "deep-copy-payload" {
		t.Fatalf("sink mutation leaked: %q", batches2[0].Bytes)
	}
}

// TestMirrorSinkResultContract：mirror receipt 不改变 primary status。
func TestMirrorSinkResultContract(t *testing.T) {
	capture := captureMirror(t)
	res := capture.SubmitMirror(context.Background(), MirrorEnvelope{
		MirrorEntryRef: MirrorEntryRef{EntryID: "e1", MirrorIndex: 0, SinkID: "capture"},
		RenderBatch: RenderBatch{
			RenderIntent: RenderIntent{Kind: TransactionFrame, Bytes: []byte("m")},
			BatchID:      "b1",
		},
	})
	if res.Target == nil {
		t.Fatal("mirror result must carry target receipt")
	}
	if res.Status != MirrorApplied {
		t.Fatalf("mirror status: %s", res.Status)
	}
}

// TestNormalizeSinkResultContract：zero/full 证明判定与非法提升禁止
// （review 指出的盲区：committed 带 Err 不得被提升为 clean full；
// attempted>0 的 zero proof 必须保留 zero 而非降级 unknown）。
func TestNormalizeSinkResultContract(t *testing.T) {
	cases := []struct {
		name   string
		in     SinkDeliveryResult
		status DeliveryStatus
		class  DeliveryErrorClass
	}{
		{
			name: "discard zero proof with attempted>0",
			in: SinkDeliveryResult{
				Status: DeliveryFailedZeroBytes, Certainty: WriteCertaintyZero,
				ErrorClass: DeliveryErrorNone, AttemptedBytes: 5, AcceptedBytes: 0,
			},
			status: DeliveryFailedZeroBytes, class: DeliveryErrorSink,
		},
		{
			name: "fault reject zero proof",
			in: SinkDeliveryResult{
				Status: DeliveryRejected, Certainty: WriteCertaintyZero,
				ErrorClass: DeliveryErrorSink, AttemptedBytes: 10, AcceptedBytes: 0,
				Err: NewClassifiedError(DeliveryErrorSink, "rejected"),
			},
			status: DeliveryRejected, class: DeliveryErrorSink,
		},
		{
			name: "committed with err must downgrade",
			in: SinkDeliveryResult{
				Status: DeliveryCommitted, Certainty: WriteCertaintyFull,
				ErrorClass: DeliveryErrorSink, AttemptedBytes: 5, AcceptedBytes: 5,
				Err: NewClassifiedError(DeliveryErrorSink, "committed with err"),
			},
			status: DeliveryUnknownPartial, class: DeliveryErrorSink,
		},
		{
			name: "clean full committed",
			in: SinkDeliveryResult{
				Status: DeliveryCommitted, Certainty: WriteCertaintyFull,
				ErrorClass: DeliveryErrorNone, AttemptedBytes: 5, AcceptedBytes: 5,
			},
			status: DeliveryCommitted, class: DeliveryErrorNone,
		},
		{
			name: "partial unknown",
			in: SinkDeliveryResult{
				Status: DeliveryUnknownPartial, Certainty: WriteCertaintyUnknown,
				ErrorClass: DeliveryErrorSink, AttemptedBytes: 5, AcceptedBytes: 2,
				Err: NewClassifiedError(DeliveryErrorSink, "partial"),
			},
			status: DeliveryUnknownPartial, class: DeliveryErrorSink,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := normalizeSinkResult(tc.in, 5)
			if out.Status != tc.status {
				t.Fatalf("status: got %s want %s", out.Status, tc.status)
			}
			if out.ErrorClass != tc.class {
				t.Fatalf("class: got %s want %s", out.ErrorClass, tc.class)
			}
			// zero proof 必须保持 attempted 计数（attempted 是计数不是证明）。
			if tc.status == DeliveryFailedZeroBytes || tc.status == DeliveryRejected {
				if out.AttemptedBytes != 5 || out.AcceptedBytes != 0 {
					t.Fatalf("zero proof counts: attempted=%d accepted=%d", out.AttemptedBytes, out.AcceptedBytes)
				}
			}
		})
	}
}

// TestGatewayMirrorOutcomeAware 的补充断言：以 FaultReject 提交时 primary
// 必须是非 nil 的 target-level rejection（而不是 pre-admission）。
func TestTargetLevelRejectionReceipt(t *testing.T) {
	fault := NewFaultSink(TargetDescriptor{
		SinkID:             "reject-primary",
		Class:              TargetClassPhysical,
		ProjectionTargetID: "pt-primary",
	})
	fault.SetKind(FaultReject)
	gw := mustGateway(t, fault)
	r := gw.Submit(context.Background(), RenderIntent{
		IntentID: "tr",
		Kind:     TransactionFrame,
		Bytes:    []byte("data"),
	})
	if r.Admission.Decision != AdmissionAccepted {
		t.Fatalf("admission must be accepted: %+v", r.Admission)
	}
	if r.Sequence == 0 {
		t.Fatal("target-level rejection must have non-zero sequence")
	}
	if r.Primary == nil {
		t.Fatal("target-level rejection must have non-nil primary")
	}
	if r.Primary.Status != DeliveryRejected {
		t.Fatalf("expected rejected, got %s", r.Primary.Status)
	}
	if r.Primary.Certainty != WriteCertaintyZero {
		t.Fatalf("expected zero certainty, got %s", r.Primary.Certainty)
	}
	// rejected 的 mirror（committed_only）必须 skip。
	if len(r.MirrorAdmissions) > 0 && r.MirrorAdmissions[0].Scheduled {
		t.Fatalf("committed_only mirror must skip on rejected primary: %+v", r.MirrorAdmissions[0])
	}
}

// TestGatewayCloseDrainsQueuedBatches：Close 排空期间，已盖章但仍在 serial
// 排队的 batch 必须继续执行，不能被静默丢弃（closeCutoff 排空契约）。
func TestGatewayCloseDrainsQueuedBatches(t *testing.T) {
	primary := memoryPrimary(t)
	gw := mustGateway(t, primary)
	gw.Run()

	// 并发提交 5 笔；sink 立即完成，serial 逐个执行。
	var wg sync.WaitGroup
	receipts := make([]OutputReceipt, 5)
	for i := range receipts {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			receipts[idx] = gw.Submit(context.Background(), RenderIntent{
				IntentID: randomID("cd"),
				Kind:     TransactionFrame,
				Bytes:    []byte("q"),
			})
		}(i)
	}
	// 等所有盖章完成（serial 有 5 笔在队列/执行中）。
	deadline := time.Now().Add(3 * time.Second)
	for {
		gw.mu.Lock()
		seq := gw.sequence
		gw.mu.Unlock()
		if seq >= 5 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if gw.sequenceLocked() < 5 {
		t.Fatalf("expected 5 stamped sequences, got %d", gw.sequenceLocked())
	}
	// 在 ongoing 队列非空时执行 Close（Close 应排空剩余 batch 而非丢弃）。
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := gw.Close(ctx); err != nil {
		t.Fatalf("close: %v", err)
	}
	wg.Wait()
	// 断言：全部 5 笔 accepted 且 primary 非 nil（执行过 sink），
	// 不允许任何一笔在盖章后被静默 rejected/abandoned。
	for i, r := range receipts {
		if r.Admission.Decision != AdmissionAccepted {
			t.Fatalf("receipt %d admission: %+v (must not be rejected after stamping)", i, r.Admission)
		}
		if r.Primary == nil {
			t.Fatalf("receipt %d primary must be non-nil (drained to sink)", i)
		}
	}
	// sink 收到了全部 5 笔。
	batches := primary.SnapshotBatches()
	if len(batches) != 5 {
		t.Fatalf("expected 5 drained batches, got %d", len(batches))
	}
}

// TestGatewayCloseAbortDrainsQueue：caller 取消只停止该 caller 的等待；shared
// CloseTimeout 到期后，排队 batch 以 synthetic abandoned 收敛，不执行 sink。
func TestGatewayCloseAbortDrainsQueue(t *testing.T) {
	blocker := NewFaultSink(TargetDescriptor{
		SinkID:             "block-abort",
		Class:              TargetClassPhysical,
		ProjectionTargetID: "pt-primary",
	})
	blocker.SetKind(FaultBlock)
	gw := mustGateway(t, blocker)
	gw.Run()

	// 第一笔进入 blocking callback。
	first := make(chan OutputReceipt, 1)
	go func() {
		first <- gw.Submit(context.Background(), RenderIntent{
			IntentID: "ab1",
			Kind:     TransactionFrame,
			Bytes:    []byte("blocked"),
		})
	}()
	// 等到第一笔已开始。
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
			IntentID: "ab2",
			Kind:     TransactionFrame,
			Bytes:    []byte("queued"),
		})
	}()
	time.Sleep(50 * time.Millisecond)

	// Close 且立即超时（借助极短 deadline ctx 或直接让 sink 卡住触发 deviate）。
	ctx, cancel := context.WithCancel(context.Background())
	closeDone := make(chan error, 1)
	go func() { closeDone <- gw.Close(ctx) }()
	// 取消 context 只结束该 caller 的等待，不能取消 shared close。
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
	// shared close 继续；推进注入时钟触发其独立 CloseTimeout。
	clock, ok := gw.clock.(*FakeClock)
	if !ok {
		t.Fatalf("gateway clock is %T, want *FakeClock", gw.clock)
	}
	clock.Advance(gw.opts.CloseTimeout)
	// 排队中的第二笔必须返回 synthetic abandoned，不永久阻塞。
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
	// 放行第一笔，Close 完成。
	blocker.Release()
	select {
	case <-first:
	case <-time.After(3 * time.Second):
		t.Fatal("first submit did not return")
	}
}

// TestGatewayCloseAbortsBlockedPhysicalWriter：in-flight callback 卡在底层
// 阻塞 writer 时，Close deviate（CloseTimeout 到期）必须调用 primary.Abort
// （转发 aborter），让被阻塞的 Submit 以 canceled 返回——否则上层的
// Leased 写永久卡死。
func TestGatewayCloseAbortsBlockedPhysicalWriter(t *testing.T) {
	block := make(chan struct{})
	w := &blockingPhysicalWriter{block: block}
	aborter := &recordingAborter{}
	phys := NewPhysicalSink(TargetDescriptor{
		SinkID:             "block-writer",
		Class:              TargetClassPhysical,
		ProjectionTargetID: "pt-blocked",
	}, w, PhysicalSinkOptions{Aborter: aborter})

	// 自定义 gateway：CloseTimeout 很短（200ms），让 deadline deviate
	// 确定性触发（不依赖 ctx 到期与 closedCh 的竞争）。
	opts := gatewayOptions()
	opts.Clock = SystemClock{}
	opts.CloseTimeout = 200 * time.Millisecond
	gw, err := NewRenderOutputGateway("blocked-writer-"+randomID("s"), opts, RenderRouteConfig{
		Primary:            phys,
		PrimaryOwnership:   SinkOwned,
		ProjectionTargetID: "pt-blocked",
	})
	if err != nil {
		t.Fatalf("gateway: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = gw.Close(ctx)
	})

	// 第一笔进入阻塞 writer。
	first := make(chan OutputReceipt, 1)
	go func() {
		first <- gw.Submit(context.Background(), RenderIntent{
			IntentID: "bw1",
			Kind:     TransactionFrame,
			Bytes:    []byte("blocked-write"),
		})
	}()
	// 等 writer 已开始（block 中）。
	deadline := time.Now().Add(2 * time.Second)
	for !w.started() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !w.started() {
		t.Fatal("submit never reached writer")
	}

	// Close：drainLoop 等到 CloseTimeout 到期 → deviate → 必须调用
	// primary.Abort → aborter 被调用；without terminated proof the shared
	// operation memoizes Abandoned rather than claiming a clean close.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := gw.Close(ctx); ClassOf(err) != DeliveryErrorAbandoned {
		t.Fatalf("close without termination proof must abandon: %v", err)
	}
	if gw.stateOf() != GatewayAbandoned {
		t.Fatalf("gateway claimed terminal state %s without termination proof", gw.stateOf())
	}
	if !aborter.wasCalled() {
		t.Fatal("gateway close-deviate must call primary.Abort to interrupt blocked writer")
	}
	// 释放 writer（模拟 abort 后底层 syscall 最终返回），第一笔 Submit 返回
	// 且不 panic/泄漏。
	close(block)
	select {
	case r := <-first:
		if r.Primary != nil && r.Primary.Status == DeliveryCommitted {
			// 底层最终写完（fault 注入返回 full）——允许，但若 aborted
			// 已生效应为 canceled；两种都是 bounded 返回。
		}
	case <-time.After(3 * time.Second):
		t.Fatal("blocked submit did not return after writer release")
	}
}

// TestGatewayCloseSealsBlockedPrimaryBeforePublishingTerminalState verifies the
// lifecycle-finalizer contract for an uninterruptible primary callback.  Close
// must fix and seal one synthetic abandoned outcome before it returns; the
// callback's eventual return is diagnostic-only and cannot rewrite that
// receipt or append a second delivery record.
func TestGatewayCloseSealsBlockedPrimaryBeforePublishingTerminalState(t *testing.T) {
	block := make(chan struct{})
	w := &blockingPhysicalWriter{block: block}
	aborter := &recordingAborter{}
	phys := NewPhysicalSink(TargetDescriptor{
		SinkID:             "late-writer",
		Class:              TargetClassPhysical,
		ProjectionTargetID: "pt-late",
	}, w, PhysicalSinkOptions{Aborter: aborter})

	opts := gatewayOptions()
	opts.Clock = SystemClock{}
	opts.CloseTimeout = 100 * time.Millisecond
	gw, err := NewRenderOutputGateway("late-close-"+randomID("s"), opts, RenderRouteConfig{
		Primary:            phys,
		PrimaryOwnership:   SinkOwned,
		ProjectionTargetID: "pt-late",
	})
	if err != nil {
		t.Fatalf("gateway: %v", err)
	}

	submitDone := make(chan OutputReceipt, 1)
	go func() {
		submitDone <- gw.Submit(context.Background(), RenderIntent{
			IntentID: "late-primary",
			Kind:     TransactionFrame,
			Bytes:    []byte("blocked"),
		})
	}()
	deadline := time.Now().Add(2 * time.Second)
	for !w.started() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !w.started() {
		t.Fatal("submit never reached writer")
	}

	closeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := gw.Close(closeCtx); ClassOf(err) != DeliveryErrorAbandoned {
		t.Fatalf("close without termination proof must abandon: %v", err)
	}

	snap := gw.Snapshot()
	if snap.State != GatewayAbandoned || snap.DeliveryRecordsUnsealed != 0 ||
		snap.DeliveryRecordsSealed != 1 {
		t.Fatalf("terminal close exposed incomplete record state: %+v", snap)
	}
	before := gw.RecentDeliveries(8)
	if len(before) != 1 || before[0].Output.Primary == nil {
		t.Fatalf("blocked primary was not sealed exactly once: %+v", before)
	}
	primary := before[0].Output.Primary
	if !primary.Synthetic || primary.CallbackReturned || !before[0].Output.TargetInvoked ||
		primary.InvocationID == 0 || primary.ErrorClass != DeliveryErrorAbandoned ||
		!primary.FinishedAt.IsZero() {
		t.Fatalf("synthetic blocked-primary facts are inconsistent: %+v", before[0])
	}

	close(block)
	var returned OutputReceipt
	select {
	case returned = <-submitDone:
	case <-time.After(3 * time.Second):
		t.Fatal("late primary callback did not return")
	}
	if returned.Primary == nil || !returned.Primary.Synthetic ||
		returned.Primary.ErrorClass != DeliveryErrorAbandoned {
		t.Fatalf("submit did not observe the fixed synthetic outcome: %+v", returned)
	}

	after := gw.RecentDeliveries(8)
	if len(after) != 1 || after[0].RecordID != before[0].RecordID ||
		after[0].Output.Primary == nil ||
		after[0].Output.Primary.OutcomeFixedAt != primary.OutcomeFixedAt ||
		after[0].Output.Primary.Status != primary.Status {
		t.Fatalf("late callback rewrote or duplicated sealed record: before=%+v after=%+v", before, after)
	}
	lateCount := 0
	for _, ev := range gw.RecentEvents(128) {
		if ev.BatchID == returned.BatchID && ev.Kind == EventPrimaryLateCompletion {
			lateCount++
		}
	}
	if lateCount != 1 {
		t.Fatalf("late callback diagnostics=%d, want exactly one", lateCount)
	}
}

// blockingPhysicalWriter 首次 Write 阻塞直到 block 关闭，随后返回 full。
type blockingPhysicalWriter struct {
	block       chan struct{}
	startedFlag atomic.Bool
}

func (w *blockingPhysicalWriter) started() bool { return w.startedFlag.Load() }

func (w *blockingPhysicalWriter) Write(p []byte) (int, error) {
	w.startedFlag.Store(true)
	<-w.block
	return len(p), nil
}

// recordingAborter 记录 abort 调用。
type recordingAborter struct {
	mu     sync.Mutex
	called bool
}

func (a *recordingAborter) AbortTerminalWrite() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.called = true
	return nil
}

func (a *recordingAborter) wasCalled() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.called
}

// TestCaptureSinkBudgetDegrade：payload 预算不足时降级为 hash-only，
// 不声明 full proof。
func TestCaptureSinkBudgetDegrade(t *testing.T) {
	c := NewCaptureSink("pt-capture", CaptureOptions{
		MaxEntries:   4,
		MaxBytes:     16, // 预算极小
		StorePayload: true,
	})
	big := bytes.Repeat([]byte("x"), 64)
	env := MirrorEnvelope{
		MirrorEntryRef: MirrorEntryRef{EntryID: "e1", MirrorIndex: 0, SinkID: "capture"},
		RenderBatch: RenderBatch{
			RenderIntent: RenderIntent{Kind: TransactionFrame, Bytes: big},
			BatchID:      "b1",
		},
	}
	res := c.SubmitMirror(context.Background(), env)
	if res.Status != MirrorApplied {
		t.Fatalf("capture should accept metadata even over budget: %s", res.Status)
	}
	if res.AcceptedBytes != len(big) {
		t.Fatalf("capture accepts the batch record: accepted=%d want=%d", res.AcceptedBytes, len(big))
	}
	entries := c.Entries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Mode != RecordedHashOnly {
		t.Fatalf("over-budget entry must degrade to hash_only, got %s", entries[0].Mode)
	}
	// payload 不可回读（hash-only）。
	if _, cls := c.Payload(entries[0].CaptureEntryID); cls != CapturePayloadErrorNotFound {
		t.Fatalf("hash-only payload must not be readable, got class %s", cls)
	}
	// avatar proof 与降级一致：attempted==accepted（整批被记录），hash-only。
	if res.Target == nil || res.Target.Status != DeliveryCommitted {
		t.Fatalf("avatar must be committed (record accepted): %+v", res.Target)
	}
}

// TestGatewaySnapshotMirrors：Snapshot.Mirrors 填各 slot 快照。
func TestGatewaySnapshotMirrors(t *testing.T) {
	capture := captureMirror(t)
	gw := mustGateway(t, memoryPrimary(t), RenderMirror{
		Sink:      capture,
		Policy:    MirrorBestEffort,
		ApplyMode: MirrorApplyBytes,
		Ownership: SinkOwned,
		Timeout:   1 * time.Second,
	})
	submitOK(t, gw, TransactionFrame, []byte("with-mirror"))
	// 等 mirror worker 完成 entry seal。
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := gw.Drain(ctx); err != nil {
		t.Fatalf("drain: %v", err)
	}
	snap := gw.Snapshot()
	if len(snap.Mirrors) != 1 {
		t.Fatalf("expected 1 mirror snapshot, got %d", len(snap.Mirrors))
	}
	m := snap.Mirrors[0]
	if m.MirrorIndex != 0 || m.Sink.Descriptor.ProjectionTargetID != "pt-capture" {
		t.Fatalf("mirror snapshot identity: %+v", m)
	}
	if m.Applied != 1 {
		t.Fatalf("expected 1 applied mirror entry, got %d", m.Applied)
	}
}

// TestDeliveryRecordWaitsForMirrors verifies that the journal contains the
// final per-mirror outcome rather than the incomplete primary-only receipt.
func TestDeliveryRecordWaitsForMirrors(t *testing.T) {
	capture := captureMirror(t)
	gw := mustGateway(t, memoryPrimary(t), RenderMirror{
		Sink:      capture,
		Policy:    MirrorBestEffort,
		ApplyMode: MirrorApplyBytes,
		Ownership: SinkOwned,
		Timeout:   time.Second,
	})
	r := submitOK(t, gw, TransactionFrame, []byte("record"))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := gw.Drain(ctx); err != nil {
		t.Fatalf("drain: %v", err)
	}
	var found *DeliveryRecord
	for _, candidate := range gw.RecentDeliveries(10) {
		if candidate.Batch.BatchID == r.BatchID {
			cp := candidate
			found = &cp
			break
		}
	}
	if found == nil {
		t.Fatalf("missing delivery record for batch %s", r.BatchID)
	}
	if len(found.Mirrors) != 1 {
		t.Fatalf("expected one final mirror receipt, got %d", len(found.Mirrors))
	}
	if !found.Mirrors[0].Scheduled || found.Mirrors[0].Status != MirrorApplied {
		t.Fatalf("unexpected final mirror receipt: %+v", found.Mirrors[0])
	}
}

// TestMirrorScheduleDropClosed：gateway 关闭后 mirror admission drop 分类
// 为 closed 而非 queue_full。
func TestMirrorScheduleDropClosed(t *testing.T) {
	capture := captureMirror(t)
	gw := mustGateway(t, memoryPrimary(t), RenderMirror{
		Sink:      capture,
		Policy:    MirrorBestEffort,
		ApplyMode: MirrorApplyBytes,
		Ownership: SinkOwned,
		Timeout:   1 * time.Second,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := gw.Close(ctx); err != nil {
		t.Fatalf("close: %v", err)
	}
	// close 后提交被 pre-admission 拒绝，不会到达 mirror admission；
	// 这里验证 closed() 分类方法与 enqueue 在关闭态失败的行为。
	r := gw.Submit(context.Background(), RenderIntent{
		IntentID: "post-close",
		Kind:     TransactionFrame,
		Bytes:    []byte("x"),
	})
	if r.Admission.Decision != AdmissionRejected || r.Admission.ErrorClass != DeliveryErrorClosed {
		t.Fatalf("expected closed rejection, got %+v", r.Admission)
	}
}

// TestGatewayCloseDrainWaiterRace：覆盖 A 完成→Broadcast→Close 抢锁时 B
// 仍在 cond 中等待的窗口——drainLoop 必须等 waiters==0 才关闭 sink。
func TestGatewayCloseDrainWaiterRace(t *testing.T) {
	blocker := NewFaultSink(TargetDescriptor{
		SinkID:             "race-primary",
		Class:              TargetClassPhysical,
		ProjectionTargetID: "pt-primary",
	})
	// 序列：第一笔 FaultBlock 阻塞，第二笔 normal。
	blocker.AddSequence(FaultBlock, FaultNone)
	gw := mustGateway(t, blocker)

	done := make(chan OutputReceipt, 2)
	go func() {
		done <- gw.Submit(context.Background(), RenderIntent{
			IntentID: "r1",
			Kind:     TransactionFrame,
			Bytes:    []byte("a"),
		})
	}()
	deadline := time.Now().Add(2 * time.Second)
	for blocker.DrainCalls() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if blocker.DrainCalls() == 0 {
		t.Fatal("first submit never reached sink")
	}
	// 第二笔在 cond 中排队。
	go func() {
		done <- gw.Submit(context.Background(), RenderIntent{
			IntentID: "r2",
			Kind:     TransactionFrame,
			Bytes:    []byte("b"),
		})
	}()
	// 确认 B 已排队：等 primaryWaiters==1。
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
	// Close 与 Release 并发：模拟 A 返回瞬间 Close 抢锁的场景。
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	closeDone := make(chan error, 1)
	go func() { closeDone <- gw.Close(ctx) }()
	// 短暂等待让 Close 进入 drainLoop 轮询，再释放 A。
	time.Sleep(20 * time.Millisecond)
	blocker.Release()
	// B 必须被排空到 sink（parent 不关闭），而不是在已关闭 sink 上执行或丢弃。
	var r1, r2 OutputReceipt
	select {
	case r1 = <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("submit 1 did not return")
	}
	select {
	case r2 = <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("submit 2 did not return (waiters race)")
	}
	select {
	case <-closeDone:
	case <-time.After(3 * time.Second):
		t.Fatal("close did not finish")
	}
	if r1.Admission.Decision != AdmissionAccepted || r2.Admission.Decision != AdmissionAccepted {
		t.Fatalf("both must be accepted: r1=%+v r2=%+v", r1.Admission, r2.Admission)
	}
	if r2.Primary == nil {
		t.Fatalf("waiter batch must be drained to sink, not dropped: %+v", r2)
	}
}

// TestReconfigureSkeleton：Abort 只固定 rollback disposition，Commit 是唯一
// 解除 admission barrier 的 finalizer。
func TestReconfigureSkeleton(t *testing.T) {
	gw := mustGateway(t, memoryPrimary(t))
	ctx := context.Background()
	candidate := memoryPrimary(t)
	plan, err := gw.BeginReconfigure(ctx, RenderRouteConfig{
		Primary:            candidate,
		PrimaryOwnership:   SinkBorrowed,
		ProjectionTargetID: candidate.Descriptor().ProjectionTargetID,
	})
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if gw.stateOf() != GatewayReconfiguring {
		t.Fatalf("expected reconfiguring, got %s", gw.stateOf())
	}
	// reconfiguring 期间不开放 admission。
	r := gw.Submit(context.Background(), RenderIntent{
		IntentID: "during-reconfig",
		Kind:     TransactionFrame,
		Bytes:    []byte("x"),
	})
	if r.Admission.Decision != AdmissionRejected {
		t.Fatalf("expected rejection during reconfigure, got %+v", r.Admission)
	}
	// Abort 恢复 open；错误 token 拒绝。
	if err := gw.AbortReconfigure(ctx, "wrong"); err == nil {
		t.Fatal("abort with wrong token must fail")
	}
	if err := gw.AbortReconfigure(ctx, plan.Token); err != nil {
		t.Fatalf("abort: %v", err)
	}
	if gw.stateOf() != GatewayReconfiguring {
		t.Fatalf("abort must retain barrier until commit, got %s", gw.stateOf())
	}
	if err := gw.CommitReconfigure(ctx, plan.Token); err != nil {
		t.Fatalf("rollback commit: %v", err)
	}
	if gw.stateOf() != GatewayOpen {
		t.Fatalf("expected open after rollback finalizer, got %s", gw.stateOf())
	}
}

// TestReconfigureInstallsFrozenMirrorRoute verifies that commit replaces the
// complete route (primary plus mirror runners), starts the new runners, and
// retires/closes owned resources from the old route outside the control lock.
func TestReconfigureInstallsFrozenMirrorRoute(t *testing.T) {
	oldPrimary := NewMemorySink(TargetDescriptor{
		SinkID:             "reconfig-old-primary",
		Class:              TargetClassPhysical,
		ProjectionTargetID: "pt-reconfig-old",
	})
	oldMirror := NewMemorySink(TargetDescriptor{
		SinkID:             "reconfig-old-mirror",
		Class:              TargetClassCapture,
		ProjectionTargetID: "pt-reconfig-old-mirror",
	})
	gw := mustGateway(t, oldPrimary, RenderMirror{
		Sink:      oldMirror,
		Policy:    MirrorBestEffort,
		ApplyMode: MirrorApplyBytes,
		Ownership: SinkOwned,
		Timeout:   time.Second,
	})
	gw.Run()
	before := submitOK(t, gw, TransactionFrame, []byte("before"))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := gw.Drain(ctx); err != nil {
		t.Fatalf("drain old route: %v", err)
	}
	if len(oldMirror.SnapshotBatches()) != 1 {
		t.Fatal("old mirror did not receive pre-switch batch")
	}

	newPrimary := NewMemorySink(TargetDescriptor{
		SinkID:             "reconfig-new-primary",
		Class:              TargetClassPhysical,
		ProjectionTargetID: "pt-reconfig-new",
	})
	newMirror := NewMemorySink(TargetDescriptor{
		SinkID:             "reconfig-new-mirror",
		Class:              TargetClassCapture,
		ProjectionTargetID: "pt-reconfig-new-mirror",
	})
	plan, err := gw.BeginReconfigure(ctx, RenderRouteConfig{
		Primary:            newPrimary,
		PrimaryOwnership:   SinkOwned,
		ProjectionTargetID: newPrimary.Descriptor().ProjectionTargetID,
		Mirrors: []RenderMirror{{
			Sink:      newMirror,
			Policy:    MirrorBestEffort,
			ApplyMode: MirrorApplyBytes,
			Ownership: SinkOwned,
			Timeout:   time.Second,
		}},
	})
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if plan.ReconfigureCutoffSequence != before.Sequence {
		t.Fatalf("cutoff=%d want %d", plan.ReconfigureCutoffSequence, before.Sequence)
	}
	if err := gw.CommitReconfigure(ctx, plan.Token); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if got := gw.Snapshot().RouteEpoch; got != plan.NewRouteEpoch {
		t.Fatalf("route epoch=%d want %d", got, plan.NewRouteEpoch)
	}
	if oldPrimary.Snapshot().State != SinkLifecycleClosed ||
		oldMirror.Snapshot().State != SinkLifecycleClosed {
		t.Fatalf("old owned route not retired: primary=%s mirror=%s",
			oldPrimary.Snapshot().State, oldMirror.Snapshot().State)
	}

	after := submitOK(t, gw, TransactionFrame, []byte("after"))
	if after.RouteEpoch != plan.NewRouteEpoch || after.Primary.SinkID != "reconfig-new-primary" {
		t.Fatalf("post-switch receipt used stale route: %+v", after)
	}
	if err := gw.Drain(ctx); err != nil {
		t.Fatalf("drain new route: %v", err)
	}
	if len(newPrimary.SnapshotBatches()) != 1 || len(newMirror.SnapshotBatches()) != 1 {
		t.Fatalf("new route deliveries: primary=%d mirror=%d",
			len(newPrimary.SnapshotBatches()), len(newMirror.SnapshotBatches()))
	}
	if len(oldMirror.SnapshotBatches()) != 1 {
		t.Fatalf("retired mirror received post-switch batch: %d", len(oldMirror.SnapshotBatches()))
	}
}

// TestReconfigureRollbackRetainsOldRouteAndDoesNotReuseEpoch verifies candidate
// ownership cleanup, reuse safety, and the monotonic reservation of route
// epochs even when a candidate is rolled back.
func TestReconfigureRollbackRetainsOldRouteAndDoesNotReuseEpoch(t *testing.T) {
	oldPrimary := NewMemorySink(TargetDescriptor{
		SinkID:             "rollback-old-primary",
		Class:              TargetClassPhysical,
		ProjectionTargetID: "pt-rollback-old",
	})
	gw := mustGateway(t, oldPrimary)
	gw.Run()
	candidateMirror := NewMemorySink(TargetDescriptor{
		SinkID:             "rollback-candidate-mirror",
		Class:              TargetClassCapture,
		ProjectionTargetID: "pt-rollback-candidate-mirror",
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	plan, err := gw.BeginReconfigure(ctx, RenderRouteConfig{
		// Reusing the installed primary must not let rollback close it, even
		// though the candidate declaration also says owned.
		Primary:            oldPrimary,
		PrimaryOwnership:   SinkOwned,
		ProjectionTargetID: oldPrimary.Descriptor().ProjectionTargetID,
		Mirrors: []RenderMirror{{
			Sink:      candidateMirror,
			Policy:    MirrorBestEffort,
			ApplyMode: MirrorApplyMetadataOnly,
			Ownership: SinkOwned,
			Timeout:   time.Second,
		}},
	})
	if err != nil {
		t.Fatalf("begin rollback: %v", err)
	}
	if err := gw.AbortReconfigure(ctx, plan.Token); err != nil {
		t.Fatalf("abort: %v", err)
	}
	if err := gw.CommitReconfigure(ctx, plan.Token); err != nil {
		t.Fatalf("rollback finalizer: %v", err)
	}
	if oldPrimary.Snapshot().State != SinkLifecycleOpen {
		t.Fatalf("rollback closed retained primary: %s", oldPrimary.Snapshot().State)
	}
	if candidateMirror.Snapshot().State != SinkLifecycleClosed {
		t.Fatalf("rollback did not close candidate mirror: %s", candidateMirror.Snapshot().State)
	}
	if got := submitOK(t, gw, TransactionFrame, []byte("still-old")); got.Primary.SinkID != oldPrimary.Descriptor().SinkID {
		t.Fatalf("rollback did not retain old route: %+v", got.Primary)
	}

	nextPrimary := NewMemorySink(TargetDescriptor{
		SinkID:             "rollback-next-primary",
		Class:              TargetClassPhysical,
		ProjectionTargetID: "pt-rollback-next",
	})
	nextPlan, err := gw.BeginReconfigure(ctx, RenderRouteConfig{
		Primary:            nextPrimary,
		PrimaryOwnership:   SinkBorrowed,
		ProjectionTargetID: nextPrimary.Descriptor().ProjectionTargetID,
	})
	if err != nil {
		t.Fatalf("second begin: %v", err)
	}
	if nextPlan.NewRouteEpoch <= plan.NewRouteEpoch {
		t.Fatalf("aborted epoch was reused: first=%d next=%d",
			plan.NewRouteEpoch, nextPlan.NewRouteEpoch)
	}
	if err := gw.AbortReconfigure(ctx, nextPlan.Token); err != nil {
		t.Fatalf("second abort: %v", err)
	}
	if err := gw.CommitReconfigure(ctx, nextPlan.Token); err != nil {
		t.Fatalf("second rollback finalizer: %v", err)
	}
}

// TestReconfigureConcurrentCommitJoinsSharedFinalizer verifies that duplicate
// callers observe the same memoized terminal operation rather than closing or
// installing resources twice.
func TestReconfigureConcurrentCommitJoinsSharedFinalizer(t *testing.T) {
	gw := mustGateway(t, memoryPrimary(t))
	candidate := NewMemorySink(TargetDescriptor{
		SinkID:             "concurrent-commit-primary",
		Class:              TargetClassPhysical,
		ProjectionTargetID: "pt-concurrent-commit",
	})
	plan, err := gw.BeginReconfigure(context.Background(), RenderRouteConfig{
		Primary:            candidate,
		PrimaryOwnership:   SinkBorrowed,
		ProjectionTargetID: candidate.Descriptor().ProjectionTargetID,
	})
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			results <- gw.CommitReconfigure(context.Background(), plan.Token)
		}()
	}
	close(start)
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Fatalf("commit caller %d: %v", i, err)
		}
	}
	if err := gw.CommitReconfigure(context.Background(), plan.Token); err != nil {
		t.Fatalf("memoized commit: %v", err)
	}
	if gw.stateOf() != GatewayOpen || gw.Snapshot().RouteEpoch != plan.NewRouteEpoch {
		t.Fatalf("final route not installed exactly once: state=%s epoch=%d",
			gw.stateOf(), gw.Snapshot().RouteEpoch)
	}
}

// TestReconfigureCutoffNoBatchSmuggling：切换在 quiescent boundary 完成，
// cutoff 内的 accepted batch 全部按旧 route 登记执行，新 route 安装前
// 不接受任何新 batch（不偷渡旧 batch、不建立隐式 pending queue）。
func TestReconfigureCutoffNoBatchSmuggling(t *testing.T) {
	oldSink := memoryPrimary(t)
	gw := mustGateway(t, oldSink)
	ctx := context.Background()
	// 提交一笔 accepted batch（cutoff 前登记）。
	r := submitOK(t, gw, TransactionFrame, []byte("pre-switch"))
	// Begin：cutoff = 当前 sequence。
	plan, err := gw.BeginReconfigure(ctx, RenderRouteConfig{
		Primary:            NewDiscardSink("pt-secondary"),
		PrimaryOwnership:   SinkOwned,
		ProjectionTargetID: "pt-secondary",
	})
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if plan.ReconfigureCutoffSequence != r.Sequence {
		t.Fatalf("cutoff=%d want %d", plan.ReconfigureCutoffSequence, r.Sequence)
	}
	// Reconfiguring 期间 Submit 全部被拒（不隐式排队）。
	for i := 0; i < 3; i++ {
		rej := gw.Submit(context.Background(), RenderIntent{
			IntentID: "smuggle",
			Kind:     TransactionFrame,
			Bytes:    []byte("post-switch"),
		})
		if rej.Admission.Decision != AdmissionRejected {
			t.Fatalf("submit during reconfigure must be rejected: %+v", rej)
		}
		if rej.Primary != nil || rej.Sequence != 0 {
			t.Fatalf("rejected during reconfigure must be pre-admission: %+v", rej)
		}
	}
	// 旧 route primary 只收到 pre-switch 一笔（无被偷渡的新 batch）。
	batches := oldSink.SnapshotBatches()
	if len(batches) != 1 || string(batches[0].Bytes) != "pre-switch" {
		t.Fatalf("old route received smuggled batches: %+v", batches)
	}
	// Commit 安装新 route。
	if err := gw.CommitReconfigure(ctx, plan.Token); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if gw.stateOf() != GatewayOpen {
		t.Fatalf("state: %s", gw.stateOf())
	}
	// 新 route 接受新 batch（到 discard）。
	r2 := gw.Submit(context.Background(), RenderIntent{
		IntentID: "after-switch",
		Kind:     TransactionFrame,
		Bytes:    []byte("post-commit"),
	})
	if r2.Admission.Decision != AdmissionAccepted || r2.RouteEpoch != plan.NewRouteEpoch {
		t.Fatalf("post-commit submit wrong epoch: %+v", r2)
	}
}

// TestReconfigureAbortKeepsAdmissionBarrierUntilFinalize：abort 后、commit
// finalize 前 Submit 持续被拒；finalize 后才恢复。
func TestReconfigureAbortKeepsAdmissionBarrierUntilFinalize(t *testing.T) {
	gw := mustGateway(t, memoryPrimary(t))
	ctx := context.Background()
	plan, err := gw.BeginReconfigure(ctx, RenderRouteConfig{
		Primary:            NewDiscardSink("pt-secondary"),
		PrimaryOwnership:   SinkOwned,
		ProjectionTargetID: "pt-secondary",
	})
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	// abort 固定 rollback，但不开放 admission。
	if err := gw.AbortReconfigure(ctx, plan.Token); err != nil {
		t.Fatalf("abort: %v", err)
	}
	for i := 0; i < 2; i++ {
		rej := gw.Submit(context.Background(), RenderIntent{
			IntentID: "barrier",
			Kind:     TransactionFrame,
			Bytes:    []byte("x"),
		})
		if rej.Admission.Decision != AdmissionRejected {
			t.Fatalf("submit after abort must stay rejected: %+v", rej)
		}
	}
	// finalize（rollback-old commit）后恢复。
	if err := gw.CommitReconfigure(ctx, plan.Token); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	r := gw.Submit(context.Background(), RenderIntent{
		IntentID: "open-again",
		Kind:     TransactionFrame,
		Bytes:    []byte("y"),
	})
	if r.Admission.Decision != AdmissionAccepted {
		t.Fatalf("submit after finalize must be accepted: %+v", r)
	}
}
