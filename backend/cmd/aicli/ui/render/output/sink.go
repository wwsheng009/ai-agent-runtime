package output

import (
	"context"
	"sync"
	"time"
)

// TargetClass 是 sink/batch 所属的路由类别；drives authorization 与
// capability 选择，不是 application 语义。primary 表示"当前选定的投影
// 目标"，不保证一定是物理 console；只有 physical 的 committed primary
// receipt 才是物理交付事实。
type TargetClass string

const (
	TargetClassPhysical TargetClass = "physical"
	TargetClassCapture  TargetClass = "capture"
	TargetClassVirtual  TargetClass = "virtual"
	TargetClassDiscard  TargetClass = "discard"
)

// TargetDescriptor 是 identity + capability（continuity）的有效边界。
type TargetDescriptor struct {
	SinkID             string
	Class              TargetClass
	ProjectionTargetID string
	ContinuityID       string // 物理目标/lease 的连续性标识，见 8.4
}

// AbortProof 由 gateway/invocation runner 持有；只有对应 InvocationID 的
// 平台终止证明才允许替代 callback return 释放 serial slot。
type AbortProof string

const (
	AbortProofNone        AbortProof = "none"
	AbortProofRequested   AbortProof = "requested"
	AbortProofTerminated  AbortProof = "terminated"
	AbortProofUnavailable AbortProof = "unavailable"
)

// SinkLifecycleState 是 sink 安装状态机。
type SinkLifecycleState string

const (
	SinkLifecycleOpen      SinkLifecycleState = "open"
	SinkLifecycleClosing   SinkLifecycleState = "closing"
	SinkLifecycleClosed    SinkLifecycleState = "closed"
	SinkLifecycleAbandoned SinkLifecycleState = "abandoned"
)

// SinkSnapshot 是 sink 的可观察快照；所有字段 detached。
type SinkSnapshot struct {
	SchemaVersion          uint32
	SnapshotEpoch          uint64
	Descriptor             TargetDescriptor
	State                  SinkLifecycleState
	AbortSupported         bool
	AbortRequested         bool
	AbortProof             AbortProof
	AbortProofInvocationID uint64
	AbortProofAt           time.Time
	InFlight               int
	InvocationID           uint64
	CallbackCount          uint64
	MirrorCallbacksApplied uint64
	Committed              uint64
	FailedZeroBytes        uint64
	UnknownPartial         uint64
	Deferred               uint64
	Rejected               uint64
	LastSequence           uint64
	LastBatchID            string
	LastErrorClass         DeliveryErrorClass
	LastSafeMessage        string
	OwnerSessionID         string // 打开 connection 的会话；无连接为空
	WriteCount             uint64
	RetainedBytes          int
	LastResult             SinkDeliveryResult
	LastError              error
	Err                    error
	LastSeenAt             time.Time
}

// RenderOutputSink 是 primary target 的 sink 契约。gateway 在 serial slot 内
// 调用 Submit；Snapshot/Abort/Close 是生命周期/控制入口（非 serial）。
//
// sink callback 的契约：
//  1. 返回前不可依赖 future 已经存在；不透明返回值视为未回读（unknown）。
//  2. io.Writer 语义：返回 n 字节数时，前面的字节必须已经同步。
//  3. 不得忽略 io.ErrShortWrite。
//  4. context 取消可以发生；sink 自行决定是否屈服，但返回的
//     SinkDeliveryResult 必须如实反映实际写入。
type RenderOutputSink interface {
	Descriptor() TargetDescriptor
	Submit(ctx context.Context, batch RenderBatch) SinkDeliveryResult
	Snapshot() SinkSnapshot
	Abort(proof AbortProof) error
	Close(ctx context.Context) error
}

// RenderMirrorSink 是 mirror target 的 sink 契约；SubmitMirror 接收入队后的
// MirrorEnvelope（已包含 stamp 与 entry identity）。
type RenderMirrorSink interface {
	Descriptor() TargetDescriptor
	SubmitMirror(ctx context.Context, envelope MirrorEnvelope) MirrorSinkResult
	Snapshot() SinkSnapshot
	Abort(proof AbortProof) error
	Close(ctx context.Context) error
}

// MirrorEnvelope 是 mirror worker 投递的不可变信封。
type MirrorEnvelope struct {
	MirrorEntryRef
	RenderBatch
	Policy             MirrorPolicy
	RequestedApplyMode MirrorApplyMode
	EffectiveApplyMode MirrorApplyMode // gateway admission 计算后的有效 mode
	NonAuthoritative   bool
	Timeout            time.Duration
}

// MirrorSinkResult 是 mirror callback 的 I/O outcome。
type MirrorSinkResult struct {
	Status         MirrorDeliveryStatus
	Target         *TargetReceipt // mirror 自己的 target receipt，非 primary copy
	ErrorClass     DeliveryErrorClass
	Err            error
	SkipReason     MirrorSkipReason
	AttemptedBytes int
	AcceptedBytes  int
	Certainty      WriteCertainty
}

// mirrorSubmitWithPanicGuard 捕获 mirror sink callback 的 panic，归一化为
// failed outcome；panic 不击穿 mirror worker goroutine。
func mirrorSubmitWithPanicGuard(submit func(context.Context, MirrorEnvelope) MirrorSinkResult,
	ctx context.Context, env MirrorEnvelope) (res MirrorSinkResult) {
	defer func() {
		if r := recover(); r != nil {
			res = MirrorSinkResult{
				Status:         MirrorFailed,
				ErrorClass:     DeliveryErrorSink,
				Err:            NewClassifiedError(DeliveryErrorSink, "mirror sink panic"),
				AttemptedBytes: len(env.Bytes),
			}
		}
	}()
	return submit(ctx, env)
}

// FlushableSink 有界刷盘能力（如 journal buffers）。
type FlushableSink interface {
	Flush(ctx context.Context) error
}

// CapturePayloadAccess 描述 payload 的可读取模式。
type CapturePayloadAccess string

const (
	CapturePayloadIndexed   CapturePayloadAccess = "indexed"
	CapturePayloadIncluding CapturePayloadAccess = "including"
	CapturePayloadExcluding CapturePayloadAccess = "excluding"
)

// CapturePayloadErrorClass 是 payload 访问失败的稳定类别。
type CapturePayloadErrorClass string

const (
	CapturePayloadErrorNone         CapturePayloadErrorClass = ""
	CapturePayloadErrorNotFound     CapturePayloadErrorClass = "not_found"
	CapturePayloadErrorRevoked      CapturePayloadErrorClass = "revoked"
	CapturePayloadErrorUnauthorized CapturePayloadErrorClass = "unauthorized"
)

// CapturePayloadRequest 请求 capture 中的 payload。
type CapturePayloadRequest struct {
	CaptureEntryID string
	Access         CapturePayloadAccess
	LimitBytes     int
}

// CapturePayloadHandle 是 payload 访问结果。
type CapturePayloadHandle struct {
	EntryID     string
	Mode        RecordedPayloadMode
	BytesLength int
	Hash        string
	Payload     []byte // detached copy；Path 非零时可能为空
	Path        string
	ExpiresAt   time.Time
	ErrorClass  CapturePayloadErrorClass
}

// CapturePayloadAuthorizer 检查 payload 访问授权。
type CapturePayloadAuthorizer interface {
	Authorize(ctx context.Context, request CapturePayloadRequest) error
}

// VirtualProjectionSink 提供投影快照（Phase 2；primary 或 mirror 均可）。
type VirtualProjectionSink interface {
	Projection() VirtualProjectionSnapshot
}

// CursorShape 是虚拟投影光标形状（7.3）。
type CursorShape string

const (
	CursorShapeUnknown   CursorShape = "unknown"
	CursorShapeBlock     CursorShape = "block"
	CursorShapeUnderline CursorShape = "underline"
	CursorShapeBar       CursorShape = "bar"
	CursorShapeHidden    CursorShape = "hidden"
)

// TerminalCursor 是虚拟投影光标（output/vt 自有类型，零基行/列）。
type TerminalCursor struct {
	Row     int // zero-based
	Column  int // zero-based display cell，非 UTF-8 byte offset
	Visible bool
	Shape   CursorShape
}

// ProjectionValidity 描述投影有效状态（9.4/6.6 floor）。
type ProjectionValidity string

const (
	ProjectionUnavailable ProjectionValidity = "unavailable" // 尚未建立 projection
	ProjectionValid       ProjectionValidity = "valid"
	ProjectionUnknown     ProjectionValidity = "unknown" // partial/abort 后不可证明
)

// VirtualProjectionSnapshot 是虚拟终端投影的 detached 快照（7.3）。
// SchemaVersion 第一版为 1。Validity==ProjectionValid 时 geometry 必须为正，
// cursor 坐标必须落在当前 geometry 内；Unavailable/Unknown 时 rows/scrollback
// 只可用于诊断，不能作为 recovery source 或物理 projection 证明。
// NonAuthoritative=true 时 Validity 不得为 ProjectionValid。
// 所有 slice 必须是 detached copy。
type VirtualProjectionSnapshot struct {
	SchemaVersion           uint32
	Width, Height           int
	Rows                    []string
	Scrollback              []string
	Cursor                  TerminalCursor
	Alternate               bool
	Validity                ProjectionValidity
	NonAuthoritative        bool
	LastSequence            uint64
	LastBatchID             string
	LastMirrorEntryID       string // primary virtual 时为空
	ProjectionTargetID      string
	ObservedPrimaryTargetID string
	Profile                 TerminalProfileRef
}

// DiscardSink 丢弃全部字节（zero proof），不进行异步 I/O，接受所有几何
// 变化。用于静态拒绝路径/benchmark；不吞掉非交互协议本应输出的数据
// （见 8.3）。
type DiscardSink struct {
	desc  TargetDescriptor
	state SinkLifecycleState
}

// NewDiscardSink 创建丢弃 sink，ProjectionTargetID 由调用方给定。
func NewDiscardSink(projectionTargetID string) *DiscardSink {
	return &DiscardSink{
		desc: TargetDescriptor{
			SinkID:             "discard",
			Class:              TargetClassDiscard,
			ProjectionTargetID: projectionTargetID,
		},
		state: SinkLifecycleOpen,
	}
}

func (d *DiscardSink) Descriptor() TargetDescriptor { return d.desc }

// Submit 丢弃 batch；counts==len, certainty=zero, status=failed_zero_bytes。
func (d *DiscardSink) Submit(_ context.Context, batch RenderBatch) SinkDeliveryResult {
	if d == nil || d.state != SinkLifecycleOpen {
		return SinkDeliveryResult{
			Status:         DeliveryRejected,
			Certainty:      WriteCertaintyZero,
			ErrorClass:     DeliveryErrorClosed,
			AttemptedBytes: len(batch.Bytes),
			Err:            NewClassifiedError(DeliveryErrorClosed, "discard sink is closed"),
		}
	}
	return SinkDeliveryResult{
		Status:         DeliveryFailedZeroBytes,
		Certainty:      WriteCertaintyZero,
		ErrorClass:     DeliveryErrorNone,
		AttemptedBytes: len(batch.Bytes),
		AcceptedBytes:  0,
	}
}

func (d *DiscardSink) Snapshot() SinkSnapshot {
	return SinkSnapshot{Descriptor: d.desc, State: d.state}
}

func (d *DiscardSink) Abort(AbortProof) error { return nil }
func (d *DiscardSink) Close(context.Context) error {
	if d == nil {
		return nil
	}
	d.state = SinkLifecycleClosed
	return nil
}

// MemorySink 接受全部字节（full proof）。测试/审计用，不做重进保护。
type MemorySink struct {
	desc      TargetDescriptor
	mu        sync.Mutex
	writes    uint64
	batches   []RenderBatch
	retained  int
	maxRetain int // <=0 表示无上限
	state     SinkLifecycleState
	errInject func() error
}

// NewMemorySink 创建内存 sink。
func NewMemorySink(desc TargetDescriptor) *MemorySink {
	return &MemorySink{desc: desc, state: SinkLifecycleOpen}
}

func (m *MemorySink) Descriptor() TargetDescriptor { return m.desc }

// Submit 接受 batch（full proof）；超过 maxRetain 的旧 batch 被丢弃。
func (m *MemorySink) Submit(_ context.Context, batch RenderBatch) SinkDeliveryResult {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state != SinkLifecycleOpen {
		return SinkDeliveryResult{
			Status:         DeliveryRejected,
			Certainty:      WriteCertaintyZero,
			ErrorClass:     DeliveryErrorClosed,
			AttemptedBytes: len(batch.Bytes),
			Err:            NewClassifiedError(DeliveryErrorClosed, "memory sink is closed"),
		}
	}
	m.writes++
	if m.errInject != nil {
		if err := m.errInject(); err != nil {
			return SinkDeliveryResult{
				Status:                  DeliveryUnknownPartial,
				Certainty:               WriteCertaintyUnknown,
				ErrorClass:              ClassOf(err),
				AttemptedBytes:          len(batch.Bytes),
				AcceptedBytes:           0,
				MayHavePartiallyWritten: false,
				Err:                     err,
			}
		}
	}
	cp := batch.deepCopy()
	m.batches = append(m.batches, cp)
	m.retained += len(batch.Bytes)
	if m.maxRetain > 0 {
		for m.retained > m.maxRetain && len(m.batches) > 0 {
			m.retained -= len(m.batches[0].Bytes)
			m.batches = m.batches[1:]
		}
	}
	return SinkDeliveryResult{
		Status:         DeliveryCommitted,
		Certainty:      WriteCertaintyFull,
		ErrorClass:     DeliveryErrorNone,
		AttemptedBytes: len(batch.Bytes),
		AcceptedBytes:  len(batch.Bytes),
	}
}

// SubmitMirror 让 MemorySink 也可以作为 mirror（测试/审计用）。
func (m *MemorySink) SubmitMirror(ctx context.Context, env MirrorEnvelope) MirrorSinkResult {
	r := m.Submit(ctx, env.RenderBatch)
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
			SinkID:             m.desc.SinkID,
			TargetClass:        m.desc.Class,
			ProjectionTargetID: m.desc.ProjectionTargetID,
			SinkDeliveryResult: r,
			CallbackReturned:   true,
		},
	}
}

func (m *MemorySink) Snapshot() SinkSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	return SinkSnapshot{
		SchemaVersion: SchemaVersion,
		Descriptor:    m.desc,
		State:         m.state,
		WriteCount:    m.writes,
		RetainedBytes: m.retained,
		LastSeenAt:    time.Now(),
	}
}

func (m *MemorySink) Abort(AbortProof) error { return nil }

func (m *MemorySink) Close(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state = SinkLifecycleClosed
	return nil
}

// SnapshotBatches 返回当前 retained batches（detached copy）。
func (m *MemorySink) SnapshotBatches() []RenderBatch {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]RenderBatch, 0, len(m.batches))
	for _, b := range m.batches {
		out = append(out, b.deepCopy())
	}
	return out
}

// ErrWriterContract 是 io.Writer 契约破坏的稳定 class（供 sink 使用）。
const ErrWriterContract DeliveryErrorClass = DeliveryErrorWriterContract
