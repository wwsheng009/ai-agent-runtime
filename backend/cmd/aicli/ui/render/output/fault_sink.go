package output

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// ============================================================================
// FaultSink：确定性故障注入（contract tests 用）
// ============================================================================

// FaultKind 描述注入的故障行为。
type FaultKind string

const (
	FaultNone           FaultKind = ""
	FaultReject         FaultKind = "reject"          // DeliveryRejected, zero proof, 不写
	FaultZero           FaultKind = "zero"            // zero proof 成功返回
	FaultPartial        FaultKind = "partial"         // accepted < attempted, unknown
	FaultErrorCommitted FaultKind = "error_committed" // 声称 committed 但 Err != nil（非法，归一化 unknown）
	FaultPanic          FaultKind = "panic"           // callback panic（gateway 捕获）
	FaultBlock          FaultKind = "block"           // callback 阻塞直到 release
)

// FaultSink 是确定性故障注入 sink。序列缓存放行调用方定义每次调用的行为，
// 默认应用当前 FaultKind。
type FaultSink struct {
	desc        TargetDescriptor
	mu          sync.Mutex
	kind        FaultKind
	seq         []FaultKind // 按调用次数索引；耗尽后回落到 kind
	calls       atomic.Uint64
	blocked     chan struct{}
	blockedOnce sync.Once
	pauseCh     chan struct{} // 非 nil 时每个 callback 等待
}

// NewFaultSink 创建 fault sink。
func NewFaultSink(desc TargetDescriptor) *FaultSink {
	return &FaultSink{desc: desc, kind: FaultNone, blocked: make(chan struct{})}
}

func (f *FaultSink) Descriptor() TargetDescriptor { return f.desc }

// SetKind 设置默认故障类型。
func (f *FaultSink) SetKind(k FaultKind) {
	f.mu.Lock()
	f.kind = k
	f.mu.Unlock()
}

// AddSequence 追加指定调用次的故障类型。
func (f *FaultSink) AddSequence(kinds ...FaultKind) {
	f.mu.Lock()
	f.seq = append(f.seq, kinds...)
	f.mu.Unlock()
}

// Release 解除 FaultBlock 阻塞。
func (f *FaultSink) Release() {
	f.blockedOnce.Do(func() {
		if f.blocked != nil {
			close(f.blocked)
		}
	})
}

// Pause 使后续每个 callback 在调用前等待（便于测试观察 entry 状态）。
func (f *FaultSink) Pause() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pauseCh == nil {
		f.pauseCh = make(chan struct{})
	}
}

// Resume 恢复。
func (f *FaultSink) Resume() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pauseCh != nil {
		close(f.pauseCh)
		f.pauseCh = nil
	}
}

func (f *FaultSink) nextKind() FaultKind {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := f.calls.Add(1)
	if n <= uint64(len(f.seq)) {
		return f.seq[n-1]
	}
	return f.kind
}

// DrainCalls 返回已调用次数（测试断言）。
func (f *FaultSink) DrainCalls() uint64 { return f.calls.Load() }

// blockKind 在 callback 内阻塞，直到 release。
func (f *FaultSink) block() {
	f.mu.Lock()
	ch := f.blocked
	pause := f.pauseCh
	f.mu.Unlock()
	if ch != nil {
		<-ch
	}
	if pause != nil {
		<-pause
	}
}

// deliverFault 应用故障并返回结果。
func (f *FaultSink) deliverFault(batchLen int) SinkDeliveryResult {
	k := f.nextKind()
	switch k {
	case FaultReject:
		return SinkDeliveryResult{
			Status:         DeliveryRejected,
			Certainty:      WriteCertaintyZero,
			ErrorClass:     DeliveryErrorSink,
			AttemptedBytes: batchLen,
			AcceptedBytes:  0,
			Err:            NewClassifiedError(DeliveryErrorSink, "fault: reject"),
		}
	case FaultZero:
		return SinkDeliveryResult{
			Status:         DeliveryFailedZeroBytes,
			Certainty:      WriteCertaintyZero,
			ErrorClass:     DeliveryErrorNone,
			AttemptedBytes: batchLen,
			AcceptedBytes:  0,
		}
	case FaultPartial:
		n := batchLen / 2
		return SinkDeliveryResult{
			Status:                  DeliveryUnknownPartial,
			Certainty:               WriteCertaintyUnknown,
			ErrorClass:              DeliveryErrorSink,
			AttemptedBytes:          batchLen,
			AcceptedBytes:           n,
			MayHavePartiallyWritten: true,
			Err:                     NewClassifiedError(DeliveryErrorSink, "fault: partial write"),
		}
	case FaultErrorCommitted:
		return SinkDeliveryResult{
			Status:                  DeliveryCommitted,
			Certainty:               WriteCertaintyFull,
			ErrorClass:              DeliveryErrorSink, // 非法：committed 带 error
			AttemptedBytes:          batchLen,
			AcceptedBytes:           batchLen,
			MayHavePartiallyWritten: false,
			Err:                     NewClassifiedError(DeliveryErrorSink, "fault: committed with error"),
		}
	case FaultPanic:
		panic("fault: panic injected")
	case FaultBlock:
		f.block()
		return SinkDeliveryResult{
			Status:         DeliveryFailedZeroBytes,
			Certainty:      WriteCertaintyZero,
			ErrorClass:     DeliveryErrorCanceledBeforeIO,
			AttemptedBytes: batchLen,
			AcceptedBytes:  0,
		}
	default:
		return SinkDeliveryResult{
			Status:         DeliveryCommitted,
			Certainty:      WriteCertaintyFull,
			ErrorClass:     DeliveryErrorNone,
			AttemptedBytes: batchLen,
			AcceptedBytes:  batchLen,
		}
	}
}

// Submit 是 primary 入口。
func (f *FaultSink) Submit(_ context.Context, batch RenderBatch) SinkDeliveryResult {
	return f.deliverFault(len(batch.Bytes))
}

// SubmitMirror 是 mirror 入口。
func (f *FaultSink) SubmitMirror(_ context.Context, env MirrorEnvelope) MirrorSinkResult {
	r := f.deliverFault(len(env.Bytes))
	return MirrorSinkResult{
		Status:         mirrorStatusFrom(r),
		ErrorClass:     r.ErrorClass,
		Err:            r.Err,
		AttemptedBytes: r.AttemptedBytes,
		AcceptedBytes:  r.AcceptedBytes,
		Certainty:      r.Certainty,
	}
}

func mirrorStatusFrom(r SinkDeliveryResult) MirrorDeliveryStatus {
	switch r.Status {
	case DeliveryCommitted:
		return MirrorApplied
	case DeliveryFailedZeroBytes, DeliveryDeferred, DeliveryRejected:
		return MirrorSkipped
	default:
		return MirrorFailed
	}
}

func (f *FaultSink) Snapshot() SinkSnapshot {
	return SinkSnapshot{
		Descriptor: f.desc,
		State:      SinkLifecycleOpen,
		WriteCount: f.calls.Load(),
	}
}

func (f *FaultSink) Abort(AbortProof) error { return nil }
func (f *FaultSink) Close(context.Context) error {
	f.Release()
	return nil
}

// FaultTickerSink 是带自动 release 延迟的 fault sink（Phase 0 可选辅助）。
type FaultTickerSink struct {
	*FaultSink
	delay    time.Duration
	released chan struct{}
}

// NewFaultTickerSink 创建 delay 后自动 Release 的 fault sink。
func NewFaultTickerSink(desc TargetDescriptor, delay time.Duration) *FaultTickerSink {
	f := NewFaultSink(desc)
	s := &FaultTickerSink{FaultSink: f, delay: delay, released: make(chan struct{})}
	time.AfterFunc(delay, func() { s.Release() })
	return s
}
