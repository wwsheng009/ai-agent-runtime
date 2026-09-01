package output

import (
	"context"
	"io"
	"os"
	"sync"
	"time"
)

// ============================================================================
// FileSink（终端输出落盘：primary 或 mirror）
// ============================================================================

// FileSinkOptions 是 FileSink 的可选配置。
type FileSinkOptions struct {
	// SyncEveryWrite 为 true 时，每次成功写入后立即调用底层 Sync
	// （*os.File 的 fsync），保证字节落盘后才返回 committed。默认 false：
	// 只在 Close/Flush 时同步一次，吞吐更高但断电/崩溃时可能丢最后一批
	// 内核缓冲数据。
	SyncEveryWrite bool
	// SyncOnClose 为 true 时，Close 前先 Sync 一次。默认 true。
	SyncOnClose bool
}

// FileSink 把渲染批次字节（wire 层 ANSI）写入一个文件，同时实现
// RenderOutputSink（primary）与 RenderMirrorSink（mirror），可直接挂到
// RenderOutputGateway 的 primary 或 mirror 位置：
//
//   - primary：终端输出直接落盘（非交互模式、回放录制）；
//   - mirror：物理 console 不变，同一批次并行写入文件（会话录屏/审计）。
//
// 写入归一化复用 PhysicalSink（一次 batch 一次底层 Write、不自行重试、
// short write / error 如实分类），区别在于 FileSink 拥有文件生命周期
// （打开/同步/关闭）并额外支持 mirror 入口与 FlushableSink。
//
// 纯文本导出不在本 sink 职责内：FileSink 只负责 wire 字节；可读文本应通过
// VirtualTerminalSink.Projection().Rows 组装后另行落盘。
type FileSink struct {
	desc   TargetDescriptor
	writer io.Writer
	closer io.Closer
	syncr  interface{ Sync() error }
	opts   FileSinkOptions

	phys *PhysicalSink

	mu         sync.Mutex
	state      SinkLifecycleState
	lastSeq    uint64
	lastErr    error
	lastClass  DeliveryErrorClass
	lastSeenAt time.Time
}

// NewFileSink 打开 path（O_CREATE|O_WRONLY|O_APPEND）并创建 sink。
// desc.Class 建议为 TargetClassCapture（文件不是物理 console 投影事实）
// 或 TargetClassPhysical（当文件自身就是 primary 目标、无 console 时）。
func NewFileSink(desc TargetDescriptor, path string, opts FileSinkOptions) (*FileSink, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	return newFileSink(desc, f, f, f, opts), nil
}

// NewFileSinkWriter 用调用方提供的 io.Writer 构造 sink（测试/管道/轮转
// wrapper 用）。若 w 实现 io.Closer，Close 时会一并关闭；若实现
// Sync() error，同步选项生效。
func NewFileSinkWriter(desc TargetDescriptor, w io.Writer, opts FileSinkOptions) *FileSink {
	if w == nil {
		w = io.Discard
	}
	var closer io.Closer
	if c, ok := w.(io.Closer); ok {
		closer = c
	}
	var syncr interface{ Sync() error }
	if s, ok := w.(interface{ Sync() error }); ok {
		syncr = s
	}
	return newFileSink(desc, w, closer, syncr, opts)
}

func newFileSink(desc TargetDescriptor, w io.Writer, closer io.Closer, syncr interface{ Sync() error }, opts FileSinkOptions) *FileSink {
	// SyncOnClose 默认 true（见 normalizeFileSinkOptions）。
	opts = normalizeFileSinkOptions(opts)
	return &FileSink{
		desc:   desc,
		writer: w,
		closer: closer,
		syncr:  syncr,
		opts:   opts,
		phys:   NewPhysicalSink(desc, w, PhysicalSinkOptions{}),
		state:  SinkLifecycleOpen,
	}
}

// normalizeFileSinkOptions 处理 SyncOnClose 的默认值。由于零值 bool 无法
// 区分"未设置"与"显式 false"，这里约定：SyncOnClose 默认 true。
func normalizeFileSinkOptions(opts FileSinkOptions) FileSinkOptions {
	if !opts.SyncEveryWrite && !opts.SyncOnClose {
		opts.SyncOnClose = true
	}
	return opts
}

func (f *FileSink) Descriptor() TargetDescriptor { return f.desc }

// Submit 把 batch 字节写入文件一次（复用 PhysicalSink 归一化）。closed
// 状态返回 Rejected/closed。
func (f *FileSink) Submit(ctx context.Context, batch RenderBatch) SinkDeliveryResult {
	f.mu.Lock()
	if f.state != SinkLifecycleOpen {
		f.mu.Unlock()
		return SinkDeliveryResult{
			Status:         DeliveryRejected,
			Certainty:      WriteCertaintyZero,
			ErrorClass:     DeliveryErrorClosed,
			AttemptedBytes: len(batch.Bytes),
			Err:            NewClassifiedError(DeliveryErrorClosed, "file sink is closed"),
		}
	}
	f.mu.Unlock()

	res := f.phys.Submit(ctx, batch)
	if res.Status == DeliveryCommitted && f.opts.SyncEveryWrite && f.syncr != nil {
		if err := f.syncr.Sync(); err != nil {
			// 字节已写入但未证明落盘：降级为 unknown partial，不伪装
			// committed 持久化事实。
			res = SinkDeliveryResult{
				Status:                  DeliveryUnknownPartial,
				Certainty:               WriteCertaintyUnknown,
				ErrorClass:              DeliveryErrorSink,
				AttemptedBytes:          res.AttemptedBytes,
				AcceptedBytes:           res.AcceptedBytes,
				MayHavePartiallyWritten: true,
				Err:                     err,
			}
		}
	}
	f.record(res, batch.Sequence)
	return res
}

// SubmitMirror 是 mirror 入口；归一化与 primary 相同，identity 由 gateway
// scheduler 盖章汇总。
func (f *FileSink) SubmitMirror(ctx context.Context, env MirrorEnvelope) MirrorSinkResult {
	r := f.Submit(ctx, env.RenderBatch)
	return MirrorSinkResult{
		Status:         mirrorStatusFrom(r),
		ErrorClass:     r.ErrorClass,
		Err:            r.Err,
		AttemptedBytes: r.AttemptedBytes,
		AcceptedBytes:  r.AcceptedBytes,
		Certainty:      r.Certainty,
		Target: &TargetReceipt{
			SessionID:          env.SessionID,
			Sequence:           env.Sequence,
			BatchID:            env.BatchID,
			RouteEpoch:         env.RouteEpoch,
			SinkID:             f.desc.SinkID,
			TargetClass:        f.desc.Class,
			ProjectionTargetID: f.desc.ProjectionTargetID,
			SinkDeliveryResult: r,
			CallbackReturned:   true,
		},
	}
}

func (f *FileSink) record(res SinkDeliveryResult, seq uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastSeq = seq
	f.lastErr = res.Err
	f.lastClass = res.ErrorClass
	f.lastSeenAt = time.Now()
}

// Snapshot 返回可观察快照（detached）；统计透传底层 PhysicalSink。
func (f *FileSink) Snapshot() SinkSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	ps := f.phys.Snapshot()
	ps.Descriptor = f.desc
	ps.State = f.state
	ps.LastSequence = f.lastSeq
	ps.LastErrorClass = f.lastClass
	ps.LastError = f.lastErr
	ps.Err = f.lastErr
	ps.LastSeenAt = f.lastSeenAt
	return ps
}

// Abort 文件写入无平台级终止协议；返回 nil。
func (f *FileSink) Abort(AbortProof) error { return nil }

// Flush 同步底层文件（实现 FlushableSink）；无 sync 能力时为空操作。
func (f *FileSink) Flush(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.state != SinkLifecycleOpen || f.syncr == nil {
		return nil
	}
	return f.syncr.Sync()
}

// Close 先同步（默认）再关闭文件，并置 closed。
func (f *FileSink) Close(ctx context.Context) error {
	f.mu.Lock()
	if f.state == SinkLifecycleClosed {
		f.mu.Unlock()
		return nil
	}
	f.state = SinkLifecycleClosed
	syncr := f.syncr
	closer := f.closer
	doSync := f.opts.SyncOnClose
	f.mu.Unlock()

	var firstErr error
	if doSync && syncr != nil {
		if err := syncr.Sync(); err != nil {
			firstErr = err
		}
	}
	_ = f.phys.Close(ctx)
	if closer != nil {
		if err := closer.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
