package output

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"
)

// ============================================================================
// PhysicalSink（7.1）
// ============================================================================

// PhysicalWriteAborter 与 ui.TerminalWriteAborter 结构同构（鸭子类型），
// 避免 output -> ui 依赖。AbortTerminalWrite 不得在持业务锁时调用。
type PhysicalWriteAborter interface {
	AbortTerminalWrite() error
}

// PhysicalWriteGate 允许注入共享终端写锁（如 renderengine 的
// WithTerminalWriteLock），sink 在锁内完成一次底层 Write。gate 非重入。
type PhysicalWriteGate interface {
	WithTerminalWrite(func() error) error
}

// PhysicalWriteGateFunc 是 gate 的函数形态。
type PhysicalWriteGateFunc func(func() error) error

// WithTerminalWrite 实现 PhysicalWriteGate。
func (f PhysicalWriteGateFunc) WithTerminalWrite(fn func() error) error { return f(fn) }

// PhysicalSinkOptions 是 PhysicalSink 的可选配置。
type PhysicalSinkOptions struct {
	// Aborter 提供 bounded shutdown；nil 时 AbortSupported=false。
	Aborter PhysicalWriteAborter
	// Gate 包裹一次底层 Write（共享终端写锁）；nil 时不加锁。
	Gate PhysicalWriteGate
}

// PhysicalSink 将现有 io.Writer、可选的 TerminalWriteAborter 和短写分类
// 封装为标准 sink。职责：
//   - 对一次 RenderBatch.Bytes 只执行一次底层提交，不自行 retry；
//   - 提供 Abort（presenter bounded shutdown 调用）；
//   - 不持有业务层锁，不回调 reducer。
//
// 普通 io.Writer 没有可靠的取消/终止协议时，AbortSupported=false 且
// AbortProof 保持 AbortProofNone/AbortProofUnavailable；即使包装器让调用方
// 返回，也不能把"底层 syscall 可能稍后完成"当作 AbortProofTerminated。
type PhysicalSink struct {
	desc    TargetDescriptor
	writer  io.Writer
	aborter PhysicalWriteAborter
	gate    PhysicalWriteGate

	mu         sync.Mutex
	state      SinkLifecycleState
	aborted    bool
	abortProof AbortProof
	lastSeenAt time.Time

	attempted uint64 // 尝试字节总数
	accepted  uint64 // 已证明接受的字节总数
	committed uint64 // full proof 计数
	zero      uint64 // failed_zero_bytes 计数
	partial   uint64 // unknown partial 计数
	rejected  uint64
	canceled  uint64 // cancellation error class 计数
	lastSeq   uint64
	lastErr   error
	lastClass DeliveryErrorClass
}

// NewPhysicalSink 创建物理 sink；desc.Class 应为 physical。
func NewPhysicalSink(desc TargetDescriptor, writer io.Writer, opts PhysicalSinkOptions) *PhysicalSink {
	if writer == nil {
		writer = io.Discard
	}
	return &PhysicalSink{
		desc:    desc,
		writer:  writer,
		aborter: opts.Aborter,
		gate:    opts.Gate,
		state:   SinkLifecycleOpen,
	}
}

func (p *PhysicalSink) Descriptor() TargetDescriptor { return p.desc }

// Snapshot 返回可观察快照（detached）。
func (p *PhysicalSink) Snapshot() SinkSnapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	return SinkSnapshot{
		Descriptor:    p.desc,
		State:         p.state,
		WriteCount:    p.committed + p.zero + p.partial + p.rejected,
		RetainedBytes: int(p.accepted),
		LastResult: SinkDeliveryResult{
			Status:         DeliveryUnknownPartial,
			Certainty:      WriteCertaintyUnknown,
			ErrorClass:     p.lastClass,
			AttemptedBytes: int(p.attempted),
			AcceptedBytes:  int(p.accepted),
			Err:            p.lastErr,
		},
		LastError:  p.lastErr,
		Err:        p.lastErr,
		LastSeenAt: p.lastSeenAt,
	}
}

// stateSnapshot 返回当前 lifecycle state（加锁读取）。
func (p *PhysicalSink) stateSnapshot() SinkLifecycleState {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state
}

// Abort 请求底层 writer 终止。只有对应平台 API 明确保证不再写才报告
// terminated proof；这里只透传调用结果，proof 由调用方语义决定。
//
// 接线契约：Aborter.AbortTerminalWrite 回调内不得反向调用本 gateway 的
// Close/Submit（closeOnce 不可重入；abort 必须在锁外、单向快路径）。
func (p *PhysicalSink) Abort(proof AbortProof) error {
	p.mu.Lock()
	p.aborted = true
	if proof != AbortProofNone {
		p.abortProof = proof
	}
	aborter := p.aborter
	p.mu.Unlock()
	if aborter == nil {
		return nil
	}
	return aborter.AbortTerminalWrite()
}

// Close 置 closed；底层 writer 不再复用（拘于 aborter 实际是否可终止，
// 状态仍如实呈现 closed）。
func (p *PhysicalSink) Close(_ context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.state = SinkLifecycleClosed
	return nil
}

// AbortSupported 报告当前底层 writer 是否可终止。
func (p *PhysicalSink) AbortSupported() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.aborter != nil
}

// AbortState 返回 abort 状态（诊断用）；未 abort 时 proof 归一化为
// AbortProofNone。
func (p *PhysicalSink) AbortState() (aborted bool, proof AbortProof) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.aborted {
		return false, AbortProofNone
	}
	if p.abortProof == "" {
		return true, AbortProofRequested
	}
	return true, p.abortProof
}

// recordLocked 更新统计；调用方持 p.mu。
func (p *PhysicalSink) recordLocked(seq uint64, res SinkDeliveryResult) {
	p.lastSeq = seq
	p.lastErr = res.Err
	p.lastClass = res.ErrorClass
	p.lastSeenAt = time.Now()
	p.attempted += uint64(res.AttemptedBytes)
	p.accepted += uint64(res.AcceptedBytes)
	switch res.Status {
	case DeliveryCommitted:
		p.committed++
	case DeliveryFailedZeroBytes:
		p.zero++
	case DeliveryUnknownPartial:
		p.partial++
	case DeliveryDeferred, DeliveryRejected:
		p.rejected++
	}
	switch res.ErrorClass {
	case DeliveryErrorCanceledBeforeIO, DeliveryErrorCanceledAfterStart:
		p.canceled++
	}
}

// Submit 执行 7.1 的完整 writer 结果归一化；一次 batch 一次底层 Write。
//
// 归一化表（len(p)=len(batch.Bytes)）：
//   - 空 bytes 且 TransactionContextBarrier：Committed/Full，不调用 writer；
//     其他 kind 的空 bytes 不应到达（gateway pre-admission 已拒绝），仍防御
//     返回 Rejected/Zero + Invalid；
//   - n==len, err==nil：Committed/Full；
//   - n==0, err!=nil：FailedZeroBytes/Zero；
//   - n==0, err==nil, len>0：FailedZeroBytes/Zero + WriterContract
//     （转换为 io.ErrNoProgress，不循环）；
//   - 0<n<len（无论 err）：UnknownPartial/Unknown；
//   - n==len, err!=nil：UnknownPartial/Unknown；
//   - n<0 或 n>len：UnknownPartial/Unknown + WriterContract；
//   - ctx 在 writer 调用前已取消：FailedZeroBytes/Zero + CanceledAfterStart
//     （以零写证明为前提）。
func (p *PhysicalSink) Submit(ctx context.Context, batch RenderBatch) SinkDeliveryResult {
	bytes := batch.Bytes
	// 状态检查先行：aborted/closed 的 sink 对任何 batch（含空 barrier）都
	// 不做提交，避免 "closed sink 仍返回 committed" 的不一致。
	if aborted, proof := p.AbortState(); aborted {
		_ = proof
		res := SinkDeliveryResult{
			Status:         DeliveryFailedZeroBytes,
			Certainty:      WriteCertaintyZero,
			ErrorClass:     DeliveryErrorCanceledAfterStart,
			AttemptedBytes: len(bytes),
			AcceptedBytes:  0,
			Err:            NewClassifiedError(DeliveryErrorCanceledAfterStart, "physical sink aborted"),
		}
		p.mu.Lock()
		p.recordLocked(batch.Sequence, res)
		p.mu.Unlock()
		return res
	}
	if s := p.stateSnapshot(); s == SinkLifecycleClosed {
		res := SinkDeliveryResult{
			Status:         DeliveryRejected,
			Certainty:      WriteCertaintyZero,
			ErrorClass:     DeliveryErrorClosed,
			AttemptedBytes: len(bytes),
			AcceptedBytes:  0,
			Err:            NewClassifiedError(DeliveryErrorClosed, "physical sink closed"),
		}
		p.mu.Lock()
		p.recordLocked(batch.Sequence, res)
		p.mu.Unlock()
		return res
	}

	if len(bytes) == 0 {
		if batch.Kind == TransactionContextBarrier {
			// 合法 context-only barrier：不调用 writer，保留 context batch。
			res := SinkDeliveryResult{
				Status:         DeliveryCommitted,
				Certainty:      WriteCertaintyFull,
				ErrorClass:     DeliveryErrorNone,
				AttemptedBytes: 0,
				AcceptedBytes:  0,
			}
			p.mu.Lock()
			p.recordLocked(batch.Sequence, res)
			p.mu.Unlock()
			return res
		}
		// 防御：其他 kind 的空 bytes 不得到达此路径。
		res := SinkDeliveryResult{
			Status:         DeliveryRejected,
			Certainty:      WriteCertaintyZero,
			ErrorClass:     DeliveryErrorInvalid,
			AttemptedBytes: 0,
			AcceptedBytes:  0,
			Err:            NewClassifiedError(DeliveryErrorInvalid, "empty physical batch bytes"),
		}
		p.mu.Lock()
		p.recordLocked(batch.Sequence, res)
		p.mu.Unlock()
		return res
	}

	// writer 调用前取消检查：可证明零写。
	if ctx != nil && ctx.Err() != nil {
		res := SinkDeliveryResult{
			Status:         DeliveryFailedZeroBytes,
			Certainty:      WriteCertaintyZero,
			ErrorClass:     DeliveryErrorCanceledAfterStart,
			AttemptedBytes: len(bytes),
			AcceptedBytes:  0,
			Err:            NewClassifiedError(DeliveryErrorCanceledAfterStart, "canceled before writer invoked"),
		}
		p.mu.Lock()
		p.recordLocked(batch.Sequence, res)
		p.mu.Unlock()
		return res
	}

	// gate 内执行一次底层 Write（gate 自身失败视为 writer 契约破坏）。
	res := SinkDeliveryResult{
		Status:                  DeliveryUnknownPartial,
		Certainty:               WriteCertaintyUnknown,
		ErrorClass:              DeliveryErrorWriterContract,
		AttemptedBytes:          len(bytes),
		AcceptedBytes:           0,
		MayHavePartiallyWritten: true,
		Err:                     NewClassifiedError(DeliveryErrorWriterContract, "physical write gate failed"),
	}
	if p.gate != nil {
		err := p.gate.WithTerminalWrite(func() error {
			res = p.doWrite(batch)
			return nil
		})
		if err != nil {
			res = SinkDeliveryResult{
				Status:                  DeliveryUnknownPartial,
				Certainty:               WriteCertaintyUnknown,
				ErrorClass:              DeliveryErrorWriterContract,
				AttemptedBytes:          len(bytes),
				AcceptedBytes:           0,
				MayHavePartiallyWritten: true,
				Err:                     NewClassifiedError(DeliveryErrorWriterContract, "physical write gate failed: "+err.Error()),
			}
			p.mu.Lock()
			p.recordLocked(batch.Sequence, res)
			p.mu.Unlock()
		}
		return res
	}
	res = p.doWrite(batch)
	return res
}

// doWrite 在 gate 锁内执行一次底层 Write 并归一化、记录统计；返回结果。
func (p *PhysicalSink) doWrite(batch RenderBatch) SinkDeliveryResult {
	n, err, panicked := p.invokeWriter(batch.Bytes)
	res := p.normalizeWrite(n, err, panicked, len(batch.Bytes))
	// abort 已请求时，writer 返回的任何零写/错误都归因于 cancel
	// （7.1：writer invocation 开始后 cancel/abort 默认 Unknown，但
	// PhysicalSink 转发的 aborter 明确证明零写时降为 zero）。
	p.mu.Lock()
	aborted := p.aborted
	p.mu.Unlock()
	if aborted {
		if res.Status == DeliveryFailedZeroBytes {
			res.ErrorClass = DeliveryErrorCanceledAfterStart
			res.Err = NewClassifiedError(DeliveryErrorCanceledAfterStart, "write aborted")
		} else {
			res.Status = DeliveryUnknownPartial
			res.Certainty = WriteCertaintyUnknown
			res.ErrorClass = DeliveryErrorCanceledAfterStart
			res.Err = NewClassifiedError(DeliveryErrorCanceledAfterStart, "write aborted during invocation")
		}
	}
	p.mu.Lock()
	p.recordLocked(batch.Sequence, res)
	p.mu.Unlock()
	return res
}
func (p *PhysicalSink) invokeWriter(bytes []byte) (n int, err error, panicked bool) {
	defer func() {
		if r := recover(); r != nil {
			panicked = true
			err = errors.New("physical writer panicked")
		}
	}()
	n, err = p.writer.Write(bytes)
	return n, err, false
}

// normalizeWrite 实现 7.1 归一化表。
func (p *PhysicalSink) normalizeWrite(n int, err error, panicked bool, batchLen int) SinkDeliveryResult {
	if panicked {
		return SinkDeliveryResult{
			Status:                  DeliveryUnknownPartial,
			Certainty:               WriteCertaintyUnknown,
			ErrorClass:              DeliveryErrorSink,
			AttemptedBytes:          batchLen,
			AcceptedBytes:           0,
			MayHavePartiallyWritten: true,
			Err:                     NewClassifiedError(DeliveryErrorSink, "physical writer panicked"),
		}
	}
	if n < 0 || n > batchLen {
		return SinkDeliveryResult{
			Status:                  DeliveryUnknownPartial,
			Certainty:               WriteCertaintyUnknown,
			ErrorClass:              DeliveryErrorWriterContract,
			AttemptedBytes:          batchLen,
			AcceptedBytes:           0,
			MayHavePartiallyWritten: true,
			Err:                     NewClassifiedError(DeliveryErrorWriterContract, "writer returned out-of-range n"),
		}
	}
	if n == batchLen && err == nil {
		return SinkDeliveryResult{
			Status:         DeliveryCommitted,
			Certainty:      WriteCertaintyFull,
			ErrorClass:     DeliveryErrorNone,
			AttemptedBytes: batchLen,
			AcceptedBytes:  batchLen,
		}
	}
	if n == 0 && err != nil {
		return SinkDeliveryResult{
			Status:         DeliveryFailedZeroBytes,
			Certainty:      WriteCertaintyZero,
			ErrorClass:     DeliveryErrorSink,
			AttemptedBytes: batchLen,
			AcceptedBytes:  0,
			Err:            err,
		}
	}
	if n == 0 && err == nil {
		// n==0,nil：转换为 io.ErrNoProgress，不循环。
		return SinkDeliveryResult{
			Status:         DeliveryFailedZeroBytes,
			Certainty:      WriteCertaintyZero,
			ErrorClass:     DeliveryErrorWriterContract,
			AttemptedBytes: batchLen,
			AcceptedBytes:  0,
			Err:            io.ErrNoProgress,
		}
	}
	// 0<n<len（无论 err）或 n==len 且 err!=nil：unknown partial。
	return SinkDeliveryResult{
		Status:                  DeliveryUnknownPartial,
		Certainty:               WriteCertaintyUnknown,
		ErrorClass:              ClassOf(err),
		AttemptedBytes:          batchLen,
		AcceptedBytes:           n,
		MayHavePartiallyWritten: true,
		Err:                     err,
	}
}

// Metrics 返回诊断计数器（attempted/accepted/各类计数/cancel class）。
type PhysicalMetrics struct {
	Attempted uint64
	Accepted  uint64
	Committed uint64
	Zero      uint64
	Partial   uint64
	Rejected  uint64
	Canceled  uint64
	LastSeq   uint64
}

// Metrics 返回统计快照（测试/诊断用）。
func (p *PhysicalSink) Metrics() PhysicalMetrics {
	p.mu.Lock()
	defer p.mu.Unlock()
	return PhysicalMetrics{
		Attempted: p.attempted,
		Accepted:  p.accepted,
		Committed: p.committed,
		Zero:      p.zero,
		Partial:   p.partial,
		Rejected:  p.rejected,
		Canceled:  p.canceled,
		LastSeq:   p.lastSeq,
	}
}
