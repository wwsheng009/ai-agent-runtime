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
		wantCalls     int
	}{
		{"full write", len(payload), nil, DeliveryCommitted, WriteCertaintyFull, DeliveryErrorNone, len(payload), 1},
		{"zero byte error", 0, errors.New("boom"), DeliveryFailedZeroBytes, WriteCertaintyZero, DeliveryErrorSink, 0, 1},
		{"zero nil contract break", 0, nil, DeliveryFailedZeroBytes, WriteCertaintyZero, DeliveryErrorWriterContract, 0, 1},
		// 短写（err==nil）自动补全：第一次写一半，第二次写剩余，最终 Committed。
		{"short nil completes", len(payload) / 2, nil, DeliveryCommitted, WriteCertaintyFull, DeliveryErrorNone, len(payload), 2},
		// 短写带错误：不补全（错误表明写入被中断），保持 UnknownPartial。
		{"short error", len(payload) / 2, errors.New("short"), DeliveryUnknownPartial, WriteCertaintyUnknown, DeliveryErrorSink, len(payload) / 2, 1},
		{"full with error", len(payload), errors.New("late"), DeliveryUnknownPartial, WriteCertaintyUnknown, DeliveryErrorSink, len(payload), 1},
		{"negative n", -1, nil, DeliveryUnknownPartial, WriteCertaintyUnknown, DeliveryErrorWriterContract, 0, 1},
		{"oversized n", len(payload) + 10, nil, DeliveryUnknownPartial, WriteCertaintyUnknown, DeliveryErrorWriterContract, 0, 1},
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
			if writer.callsCount() != tc.wantCalls {
				t.Fatalf("writer calls: got %d want %d", writer.callsCount(), tc.wantCalls)
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
	// "part" 短写后自动补全成功，计入 Committed 而非 Partial。
	if m.Committed != 2 || m.Zero != 1 || m.Partial != 0 {
		t.Fatalf("metrics: committed=%d zero=%d partial=%d", m.Committed, m.Zero, m.Partial)
	}
	if m.LastSeq != 3 {
		t.Fatalf("last seq: %d", m.LastSeq)
	}
	// "x" 批次零写失败贡献 0 accepted；"part" 补全后全部接受。
	if m.Attempted != uint64(len("ok")+1+len("part")) || m.Accepted != uint64(len("ok")+len("part")) {
		t.Fatalf("bytes: attempted=%d accepted=%d", m.Attempted, m.Accepted)
	}
	snap := sink.Snapshot()
	if snap.State != SinkLifecycleOpen || snap.Descriptor.SinkID != "physical-test" {
		t.Fatalf("snapshot: %+v", snap)
	}
}

// TestPhysicalSinkShortWriteCompletion：短写（0<n<len 且 err==nil）自动补全
// 剩余字节，全部写完判 Committed；补全中断（错误/零进度/abort）按已写部分
// 归一化，且不无限循环。
func TestPhysicalSinkShortWriteCompletion(t *testing.T) {
	t.Run("multi chunk completion", func(t *testing.T) {
		// 每次只写 1 字节，最终全部写完。
		writer := &chunkWriter{chunk: 1}
		sink := physicalSink(writer, PhysicalSinkOptions{})
		payload := []byte("terminal-frame-bytes")
		res := sink.Submit(context.Background(), physicalBatch(1, TransactionFrame, payload))
		if res.Status != DeliveryCommitted || res.Certainty != WriteCertaintyFull {
			t.Fatalf("multi chunk: got %s/%s", res.Status, res.Certainty)
		}
		if res.AcceptedBytes != len(payload) || res.AttemptedBytes != len(payload) {
			t.Fatalf("multi chunk bytes: accepted=%d attempted=%d", res.AcceptedBytes, res.AttemptedBytes)
		}
		if writer.calls != len(payload) {
			t.Fatalf("writer calls: %d want %d", writer.calls, len(payload))
		}
		if string(writer.written) != string(payload) {
			t.Fatalf("bytes corrupted: %q", writer.written)
		}
	})

	t.Run("completion interrupted by error", func(t *testing.T) {
		// 第一次短写成功，第二次出错：不无限循环，UnknownPartial + 已写字节。
		writer := newFakePhysicalWriter().
			with(2, nil).
			with(0, errors.New("pipe broken"))
		sink := physicalSink(writer, PhysicalSinkOptions{})
		payload := []byte("abcdef")
		res := sink.Submit(context.Background(), physicalBatch(1, TransactionFrame, payload))
		if res.Status != DeliveryUnknownPartial || res.Certainty != WriteCertaintyUnknown {
			t.Fatalf("interrupted: got %s/%s", res.Status, res.Certainty)
		}
		if res.ErrorClass != DeliveryErrorSink {
			t.Fatalf("interrupted class: %s", res.ErrorClass)
		}
		if res.AcceptedBytes != 2 || res.AttemptedBytes != len(payload) {
			t.Fatalf("interrupted bytes: accepted=%d attempted=%d", res.AcceptedBytes, res.AttemptedBytes)
		}
		if writer.callsCount() != 2 {
			t.Fatalf("writer calls: %d", writer.callsCount())
		}
	})

	t.Run("completion zero progress stops", func(t *testing.T) {
		// 补全中途零进度（n==0,nil）：ErrNoProgress，不无限循环。
		writer := newFakePhysicalWriter().
			with(3, nil).
			with(0, nil)
		sink := physicalSink(writer, PhysicalSinkOptions{})
		payload := []byte("abcdef")
		res := sink.Submit(context.Background(), physicalBatch(1, TransactionFrame, payload))
		if res.Status != DeliveryUnknownPartial || res.ErrorClass != DeliveryErrorWriterContract {
			t.Fatalf("zero progress: got %s/%s", res.Status, res.ErrorClass)
		}
		if res.AcceptedBytes != 3 {
			t.Fatalf("zero progress accepted: %d", res.AcceptedBytes)
		}
		if writer.callsCount() != 2 {
			t.Fatalf("writer calls: %d (must not loop)", writer.callsCount())
		}
	})

	t.Run("completion abort stops", func(t *testing.T) {
		// 第一次短写返回时触发 abort（模拟 abort 发生在 writer invocation
		// 期间）：补全前检查到 aborted，不再向 writer 继续写。
		aborter := &fakeAborter{}
		w := &abortDuringWriteWriter{}
		sink := physicalSink(w, PhysicalSinkOptions{Aborter: aborter})
		w.sink = sink
		payload := []byte("abcdef")
		res := sink.Submit(context.Background(), physicalBatch(1, TransactionFrame, payload))
		if res.Status != DeliveryUnknownPartial || res.ErrorClass != DeliveryErrorCanceledAfterStart {
			t.Fatalf("aborted completion: got %s/%s", res.Status, res.ErrorClass)
		}
		if res.AcceptedBytes != 3 {
			t.Fatalf("aborted accepted: %d", res.AcceptedBytes)
		}
		if w.calls != 1 {
			t.Fatalf("writer calls: %d (must stop after abort)", w.calls)
		}
	})

	t.Run("gate wraps whole completion", func(t *testing.T) {
		// 补全循环在单次 gate 包裹内完成：gate.calls==1，writer 多次调用。
		writer := &chunkWriter{chunk: 2}
		gate := &gateRecorder{}
		sink := physicalSink(writer, PhysicalSinkOptions{Gate: gate})
		res := sink.Submit(context.Background(), physicalBatch(1, TransactionFrame, []byte("abcd")))
		if res.Status != DeliveryCommitted {
			t.Fatalf("gate completion: %s", res.Status)
		}
		if gate.calls != 1 {
			t.Fatalf("gate calls: %d want 1", gate.calls)
		}
		if writer.calls != 2 {
			t.Fatalf("writer calls: %d want 2", writer.calls)
		}
	})
}

// chunkWriter 每次最多写 chunk 字节（模拟终端/管道短写）。
type chunkWriter struct {
	chunk   int
	written []byte
	calls   int
}

func (w *chunkWriter) Write(p []byte) (int, error) {
	w.calls++
	n := w.chunk
	if n > len(p) {
		n = len(p)
	}
	w.written = append(w.written, p[:n]...)
	return n, nil
}

// abortDuringWriteWriter 在第一次 Write 返回前触发 sink.Abort（模拟 abort
// 发生在 writer invocation 期间），并始终短写一半。
type abortDuringWriteWriter struct {
	sink  *PhysicalSink
	calls int
}

func (w *abortDuringWriteWriter) Write(p []byte) (int, error) {
	w.calls++
	if w.calls == 1 && w.sink != nil {
		_ = w.sink.Abort(AbortProofRequested)
	}
	return len(p) / 2, nil
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
