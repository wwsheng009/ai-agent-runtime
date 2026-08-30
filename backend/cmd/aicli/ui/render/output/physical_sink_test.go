package output

import (
	"context"
	"errors"
	"io"

	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ============================================================================
// 11.1 PhysicalSink tests（fake writer 全表归一化）
// ============================================================================

// fakePhysicalWriter 记录每次 Write 的行为。
type fakePhysicalWriter struct {
	mu    sync.Mutex
	n     []int
	errs  []error
	calls int
	block chan struct{} // 非 nil 时 Write 阻塞直到关闭
	// callHook 在每次调用前执行（可切换行为）
	numWritten int
}

func newFakePhysicalWriter() *fakePhysicalWriter {
	return &fakePhysicalWriter{}
}

// with 追加一次行为：n 与 err 按调用序。
func (w *fakePhysicalWriter) with(n int, err error) *fakePhysicalWriter {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.n = append(w.n, n)
	w.errs = append(w.errs, err)
	return w
}

func (w *fakePhysicalWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.calls++ // 进入 Write 即计数（block 前），供测试观察进行中的调用
	if w.block != nil {
		w.mu.Unlock()
		<-w.block
		w.mu.Lock()
	}
	idx := w.calls - 1
	var n int
	if idx < len(w.n) {
		n = w.n[idx]
	} else {
		n = len(p)
	}
	var err error
	if idx < len(w.errs) {
		err = w.errs[idx]
	}
	w.numWritten += n
	w.mu.Unlock()
	return n, err
}

func (w *fakePhysicalWriter) callsCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.calls
}

// fakeAborter 记录 abort 请求。
type fakeAborter struct {
	mu      sync.Mutex
	calls   int
	aborted bool
	err     error
}

func (a *fakeAborter) AbortTerminalWrite() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	a.aborted = true
	return a.err
}

func (a *fakeAborter) callCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

// gateRecorder 记录 gate 包裹次数。
type gateRecorder struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (g *gateRecorder) WithTerminalWrite(fn func() error) error {
	g.mu.Lock()
	g.calls++
	g.mu.Unlock()
	if g.err != nil {
		return g.err
	}
	return fn()
}

func physicalSink(writer io.Writer, opts PhysicalSinkOptions) *PhysicalSink {
	return NewPhysicalSink(TargetDescriptor{
		SinkID:             "physical-test",
		Class:              TargetClassPhysical,
		ProjectionTargetID: "pt-physical",
	}, writer, opts)
}

func physicalBatch(seq uint64, kind TransactionKind, bytes []byte) RenderBatch {
	return RenderBatch{
		RenderIntent: RenderIntent{
			IntentID: "ph",
			Kind:     kind,
			Source:   "physical-test",
			Cause:    "test",
			Bytes:    bytes,
		},
		SessionID: "ses",
		Sequence:  seq,
		BatchID:   "ph-batch",
	}
}

// TestPhysicalSinkNormalizeTable：7.1 全表。
func TestPhysicalSinkNormalizeTable(t *testing.T) {
	payload := []byte("hello-terminal")
	cases := []struct {
		name          string
		outcomeN      int
		outcomeErr    error
		wantStatus    DeliveryStatus
		wantCertainty WriteCertainty
		wantClass     DeliveryErrorClass
		wantAccepted  int
	}{
		{"full write", len(payload), nil, DeliveryCommitted, WriteCertaintyFull, DeliveryErrorNone, len(payload)},
		{"zero byte error", 0, errors.New("boom"), DeliveryFailedZeroBytes, WriteCertaintyZero, DeliveryErrorSink, 0},
		{"zero nil contract break", 0, nil, DeliveryFailedZeroBytes, WriteCertaintyZero, DeliveryErrorWriterContract, 0},
		{"short nil", len(payload) / 2, nil, DeliveryUnknownPartial, WriteCertaintyUnknown, DeliveryErrorSink, len(payload) / 2},
		{"short error", len(payload) / 2, errors.New("short"), DeliveryUnknownPartial, WriteCertaintyUnknown, DeliveryErrorSink, len(payload) / 2},
		{"full with error", len(payload), errors.New("late"), DeliveryUnknownPartial, WriteCertaintyUnknown, DeliveryErrorSink, len(payload)},
		{"negative n", -1, nil, DeliveryUnknownPartial, WriteCertaintyUnknown, DeliveryErrorWriterContract, 0},
		{"oversized n", len(payload) + 10, nil, DeliveryUnknownPartial, WriteCertaintyUnknown, DeliveryErrorWriterContract, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writer := newFakePhysicalWriter().with(tc.outcomeN, tc.outcomeErr)
			sink := physicalSink(writer, PhysicalSinkOptions{})
			res := sink.Submit(context.Background(), physicalBatch(1, TransactionFrame, payload))
			if res.Status != tc.wantStatus {
				t.Fatalf("status: got %s want %s", res.Status, tc.wantStatus)
			}
			if res.Certainty != tc.wantCertainty {
				t.Fatalf("certainty: got %s want %s", res.Certainty, tc.wantCertainty)
			}
			if res.ErrorClass != tc.wantClass {
				t.Fatalf("error class: got %s want %s", res.ErrorClass, tc.wantClass)
			}
			if res.AcceptedBytes != tc.wantAccepted {
				t.Fatalf("accepted: got %d want %d", res.AcceptedBytes, tc.wantAccepted)
			}
			if res.AttemptedBytes != len(payload) {
				t.Fatalf("attempted: got %d want %d", res.AttemptedBytes, len(payload))
			}
			if writer.callsCount() != 1 {
				t.Fatalf("writer must be called exactly once per batch, got %d", writer.callsCount())
			}
		})
	}
}

// TestPhysicalSinkZeroLengthBarrier：合法 zero-length TransactionContextBarrier
// 不调用 writer 但生成 committed receipt；其他 kind 空 bytes 防御性拒绝。
func TestPhysicalSinkZeroLengthBarrier(t *testing.T) {
	writer := newFakePhysicalWriter()
	sink := physicalSink(writer, PhysicalSinkOptions{})
	// barrier：不调用 writer，committed/full。
	res := sink.Submit(context.Background(), physicalBatch(1, TransactionContextBarrier, nil))
	if res.Status != DeliveryCommitted || res.Certainty != WriteCertaintyFull {
		t.Fatalf("barrier: got %s/%s", res.Status, res.Certainty)
	}
	if writer.callsCount() != 0 {
		t.Fatalf("barrier must not invoke writer, got %d calls", writer.callsCount())
	}
	// 其他 kind 空 bytes：防御性 rejected/invalid。
	res = sink.Submit(context.Background(), physicalBatch(2, TransactionFrame, nil))
	if res.Status != DeliveryRejected || res.ErrorClass != DeliveryErrorInvalid {
		t.Fatalf("empty non-barrier: got %s/%s", res.Status, res.ErrorClass)
	}
}

// TestPhysicalSinkCancelBeforeWrite：ctx 在 writer 调用前取消 -> zero proof +
// CanceledAfterStart，writer 不被调用。
func TestPhysicalSinkCancelBeforeWrite(t *testing.T) {
	writer := newFakePhysicalWriter()
	sink := physicalSink(writer, PhysicalSinkOptions{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res := sink.Submit(ctx, physicalBatch(1, TransactionFrame, []byte("x")))
	if res.Status != DeliveryFailedZeroBytes || res.Certainty != WriteCertaintyZero {
		t.Fatalf("cancel before write: got %s/%s", res.Status, res.Certainty)
	}
	if res.ErrorClass != DeliveryErrorCanceledAfterStart {
		t.Fatalf("cancel class: got %s", res.ErrorClass)
	}
	if writer.callsCount() != 0 {
		t.Fatalf("canceled before write must not invoke writer, got %d calls", writer.callsCount())
	}
}

// TestPhysicalSinkBlockingWriterAbort：blocking writer + Abort -> bounded
// （Abort 返回后 writer 被标记 aborted，后续 Submit 立即 zero-reject）。
func TestPhysicalSinkBlockingWriterAbort(t *testing.T) {
	writer := newFakePhysicalWriter()
	block := make(chan struct{})
	writer.block = block
	aborter := &fakeAborter{}
	sink := physicalSink(writer, PhysicalSinkOptions{Aborter: aborter})

	// 一次 Submit 卡在 writer。
	done := make(chan SinkDeliveryResult, 1)
	go func() {
		done <- sink.Submit(context.Background(), physicalBatch(1, TransactionFrame, []byte("blocked")))
	}()
	deadline := time.Now().Add(2 * time.Second)
	for writer.callsCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if writer.callsCount() == 0 {
		t.Fatal("submit never reached writer")
	}
	if !sink.AbortSupported() {
		t.Fatal("aborter should be supported")
	}
	if err := sink.Abort(AbortProofRequested); err != nil {
		t.Fatalf("abort: %v", err)
	}
	if aborter.callCount() != 1 {
		t.Fatalf("aborter calls: %d", aborter.callCount())
	}
	// abort 之后的 Submit 立即失败（不进入 writer）。
	res := sink.Submit(context.Background(), physicalBatch(2, TransactionFrame, []byte("after")))
	if res.ErrorClass != DeliveryErrorCanceledAfterStart {
		t.Fatalf("aborted sink: class=%s", res.ErrorClass)
	}
	// 释放 writer 让第一个 submit 返回。
	close(block)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("blocked submit did not return after release")
	}
}

// TestPhysicalSinkAbortedWriteClassification：abort 已在 writer invocation
// 期间发生时，writer 返回的零写归类 CanceledAfterStart/Zero，非零归类
// CanceledAfterStart/UnknownPartial（7.1 cancel/abort-after-start）。
func TestPhysicalSinkAbortedWriteClassification(t *testing.T) {
	// 零写场景：writer 等待 abort 信号后返回 (0, err)。
	zeroWriter := &abortAwareWriter{release: make(chan struct{}), entered: make(chan struct{}, 1)}
	sink := physicalSink(zeroWriter, PhysicalSinkOptions{Aborter: &fakeAborter{}})
	firstDone := make(chan SinkDeliveryResult, 1)
	go func() {
		firstDone <- sink.Submit(context.Background(), physicalBatch(1, TransactionFrame, []byte("blocked-zero")))
	}()
	select {
	case <-zeroWriter.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("writer never entered")
	}
	_ = sink.Abort(AbortProofRequested)
	close(zeroWriter.release)
	select {
	case res := <-firstDone:
		// writer 返回 (0, abortedErr)：aborter 的 abort 是明确"未写"信号，
		// 应为 FailedZeroBytes/Zero + CanceledAfterStart，而不是 Sink。
		if res.Status != DeliveryFailedZeroBytes || res.ErrorClass != DeliveryErrorCanceledAfterStart {
			t.Fatalf("aborted zero write: got %s/%s, want failed_zero_bytes/canceled_after_start",
				res.Status, res.ErrorClass)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("blocked submit did not return")
	}

	// 非零场景：writer 在 abort 后返回部分字节（(n, nil)），应 unknown。
	partWriter := &abortAwareWriter{release: make(chan struct{}), entered: make(chan struct{}, 1), n: 3}
	sink2 := physicalSink(partWriter, PhysicalSinkOptions{Aborter: &fakeAborter{}})
	secondDone := make(chan SinkDeliveryResult, 1)
	go func() {
		secondDone <- sink2.Submit(context.Background(), physicalBatch(1, TransactionFrame, []byte("blocked-part")))
	}()
	select {
	case <-partWriter.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("writer never entered")
	}
	_ = sink2.Abort(AbortProofRequested)
	close(partWriter.release)
	select {
	case res := <-secondDone:
		if res.Status != DeliveryUnknownPartial || res.ErrorClass != DeliveryErrorCanceledAfterStart {
			t.Fatalf("aborted partial write: got %s/%s, want unknown_partial/canceled_after_start",
				res.Status, res.ErrorClass)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("blocked submit did not return")
	}
}

// abortAwareWriter 阻塞 Write 直到 release 关闭；entered 通知已进入。
// n<0 表示返回 (0, error)；n==0 表示返回 (0, nil)。
type abortAwareWriter struct {
	release chan struct{}
	entered chan struct{}
	n       int
}

func (w *abortAwareWriter) Write(p []byte) (int, error) {
	if w.entered != nil {
		select {
		case w.entered <- struct{}{}:
		default:
		}
	}
	<-w.release
	if w.n < 0 {
		return 0, errors.New("aborted write")
	}
	if w.n == 0 {
		return 0, nil
	}
	if w.n > len(p) {
		w.n = len(p)
	}
	return w.n, nil
}

// TestPhysicalSinkClosedBarrier：closed sink 对空 barrier 也返回 rejected/
// closed（不做提交），不返回 committed。
func TestPhysicalSinkClosedBarrier(t *testing.T) {
	writer := newFakePhysicalWriter()
	sink := physicalSink(writer, PhysicalSinkOptions{})
	if err := sink.Close(context.Background()); err != nil {
		t.Fatalf("close: %v", err)
	}
	res := sink.Submit(context.Background(), physicalBatch(1, TransactionContextBarrier, nil))
	if res.Status != DeliveryRejected || res.ErrorClass != DeliveryErrorClosed {
		t.Fatalf("closed sink barrier: got %s/%s, want rejected/closed", res.Status, res.ErrorClass)
	}
	if writer.callsCount() != 0 {
		t.Fatalf("closed sink must not invoke writer")
	}
}

// TestPhysicalSinkCloseRejects：Close 后 Submit 返回 rejected/closed，
// writer 不被调用。
func TestPhysicalSinkCloseRejects(t *testing.T) {
	writer := newFakePhysicalWriter()
	sink := physicalSink(writer, PhysicalSinkOptions{})
	if err := sink.Close(context.Background()); err != nil {
		t.Fatalf("close: %v", err)
	}
	res := sink.Submit(context.Background(), physicalBatch(1, TransactionFrame, []byte("x")))
	if res.Status != DeliveryRejected || res.ErrorClass != DeliveryErrorClosed {
		t.Fatalf("closed sink: got %s/%s", res.Status, res.ErrorClass)
	}
	if writer.callsCount() != 0 {
		t.Fatalf("closed sink must not invoke writer")
	}
	if snap := sink.Snapshot(); snap.State != SinkLifecycleClosed {
		t.Fatalf("snapshot state: %s", snap.State)
	}
}

// TestPhysicalSinkGate：共享写锁 gate 包裹每次 Write。
func TestPhysicalSinkGate(t *testing.T) {
	writer := newFakePhysicalWriter().with(len("x"), nil)
	gate := &gateRecorder{}
	sink := physicalSink(writer, PhysicalSinkOptions{Gate: gate})
	res := sink.Submit(context.Background(), physicalBatch(1, TransactionFrame, []byte("x")))
	if res.Status != DeliveryCommitted {
		t.Fatalf("status: %s", res.Status)
	}
	if gate.calls != 1 {
		t.Fatalf("gate calls: %d", gate.calls)
	}
	// gate 自身失败：writer 契约破坏。
	gate2 := &gateRecorder{err: errors.New("gate fail")}
	sink2 := physicalSink(writer, PhysicalSinkOptions{Gate: gate2})
	res2 := sink2.Submit(context.Background(), physicalBatch(2, TransactionFrame, []byte("y")))
	if res2.ErrorClass != DeliveryErrorWriterContract {
		t.Fatalf("gate failure class: %s", res2.ErrorClass)
	}
}

// TestPhysicalSinkPanicWriter：writer panic -> unknown partial，不击穿。
func TestPhysicalSinkPanicWriter(t *testing.T) {
	panicWriter := &panicWriter{}
	sink := physicalSink(panicWriter, PhysicalSinkOptions{})
	res := sink.Submit(context.Background(), physicalBatch(1, TransactionFrame, []byte("boom")))
	if res.Status != DeliveryUnknownPartial || res.Certainty != WriteCertaintyUnknown {
		t.Fatalf("panic writer: got %s/%s", res.Status, res.Certainty)
	}
	if res.ErrorClass != DeliveryErrorSink {
		t.Fatalf("panic class: %s", res.ErrorClass)
	}
}

type panicWriter struct{}

func (panicWriter) Write([]byte) (int, error) { panic("writer boom") }

// TestPhysicalSinkNoRetry：writer error 不被自动 retry —— 每次 submit 恰好
// 一次 writer 调用。
func TestPhysicalSinkNoRetry(t *testing.T) {
	writer := newFakePhysicalWriter().
		with(0, errors.New("e1")).
		with(len("a"), nil)
	sink := physicalSink(writer, PhysicalSinkOptions{})
	_ = sink.Submit(context.Background(), physicalBatch(1, TransactionFrame, []byte("a")))
	_ = sink.Submit(context.Background(), physicalBatch(2, TransactionFrame, []byte("b")))
	if writer.callsCount() != 2 {
		t.Fatalf("expected 2 calls, got %d", writer.callsCount())
	}
}

// TestPhysicalSinkSnapshotMetrics：Snapshot/Metrics 统计正确。
func TestPhysicalSinkSnapshotMetrics(t *testing.T) {
	writer := newFakePhysicalWriter().
		with(len("ok"), nil).
		with(0, errors.New("zero")).
		with(len("part")/2, nil)
	sink := physicalSink(writer, PhysicalSinkOptions{})
	_ = sink.Submit(context.Background(), physicalBatch(1, TransactionFrame, []byte("ok")))
	_ = sink.Submit(context.Background(), physicalBatch(2, TransactionFrame, []byte("x")))
	_ = sink.Submit(context.Background(), physicalBatch(3, TransactionFrame, []byte("part")))
	m := sink.Metrics()
	if m.Committed != 1 || m.Zero != 1 || m.Partial != 1 {
		t.Fatalf("metrics: committed=%d zero=%d partial=%d", m.Committed, m.Zero, m.Partial)
	}
	if m.LastSeq != 3 {
		t.Fatalf("last seq: %d", m.LastSeq)
	}
	snap := sink.Snapshot()
	if snap.State != SinkLifecycleOpen || snap.Descriptor.SinkID != "physical-test" {
		t.Fatalf("snapshot: %+v", snap)
	}
}

// TestPhysicalSinkConcurrent：并发 Submit 串行通过 gate，无 panic、无 race。
func TestPhysicalSinkConcurrent(t *testing.T) {
	writer := newFakePhysicalWriter()
	for i := 0; i < 100; i++ {
		writer.with(len("c"), nil)
	}
	// 简单串行 gate（模拟慢锁）。
	gate := &serialGate{}
	sink := physicalSink(writer, PhysicalSinkOptions{Gate: gate})
	var wg sync.WaitGroup
	var okCount atomic.Int64
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(seq uint64) {
			defer wg.Done()
			res := sink.Submit(context.Background(), physicalBatch(seq, TransactionFrame, []byte("c")))
			if res.Status == DeliveryCommitted {
				okCount.Add(1)
			}
		}(uint64(i + 1))
	}
	wg.Wait()
	if okCount.Load() != 100 {
		t.Fatalf("committed: %d", okCount.Load())
	}
	if writer.callsCount() != 100 {
		t.Fatalf("writer calls: %d", writer.callsCount())
	}
}

type serialGate struct {
	mu sync.Mutex
}

func (g *serialGate) WithTerminalWrite(fn func() error) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return fn()
}

// TestPhysicalSinkAbortProofReporting：无 aborter 时 AbortSupported=false。
func TestPhysicalSinkAbortProofReporting(t *testing.T) {
	sink := physicalSink(io.Discard, PhysicalSinkOptions{})
	if sink.AbortSupported() {
		t.Fatal("plain writer must not report abort support")
	}
	aborted, proof := sink.AbortState()
	if aborted || proof != AbortProofNone {
		t.Fatalf("initial abort state: aborted=%v proof=%s", aborted, proof)
	}
}
