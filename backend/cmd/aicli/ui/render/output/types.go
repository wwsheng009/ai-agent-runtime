// Package output 实现渲染交付中间层：RenderIntent -> RenderBatch ->
// OutputReceipt/DeliveryRecord。它只依赖标准库与本包定义的低层 value types，
// 不导入 ui/vt、ui/render、commands 或 TerminalSession，避免循环依赖。
//
// 本包是交付边界（gateway），不是 projection authority；TerminalSession
// 仍然是业务事实的唯一权威。所有公开 snapshot 都返回 detached copy。
package output

import (
	"errors"
	"time"
)

// SchemaVersion 是 DeliveryRecord / Snapshot / SemanticPayload / event 等
// 版本的第一个公开版本。
const SchemaVersion uint32 = 1

// TransactionKind 描述一笔渲染事务的业务性质；它由 producer 在 RenderIntent
// 中声明，gateway 只校验合法性，不重新解释。
type TransactionKind string

const (
	TransactionFrame           TransactionKind = "frame"
	TransactionHistoryHandoff  TransactionKind = "history_handoff"
	TransactionFrameAndHistory TransactionKind = "frame_history"
	TransactionAlternateEnter  TransactionKind = "alternate_enter"
	TransactionAlternateWrite  TransactionKind = "alternate_write"
	TransactionAlternateExit   TransactionKind = "alternate_exit"
	TransactionPromptEditor    TransactionKind = "prompt_editor"
	TransactionBell            TransactionKind = "bell"
	TransactionTitle           TransactionKind = "title"
	TransactionContextBarrier  TransactionKind = "terminal_context"
	TransactionShutdownCleanup TransactionKind = "shutdown_cleanup"
	TransactionLegacyFlush     TransactionKind = "legacy_flush"
	TransactionLegacyImmediate TransactionKind = "legacy_immediate"
)

// historyBearingKinds 是允许携带 HistoryEpoch 的事务类型；其他 kind 携带
// HistoryEpoch 必须在 pre-admission 被拒绝。
func historyBearingKind(k TransactionKind) bool {
	switch k {
	case TransactionHistoryHandoff, TransactionFrameAndHistory:
		return true
	default:
		return false
	}
}

func validTransactionKind(k TransactionKind) bool {
	switch k {
	case TransactionFrame,
		TransactionHistoryHandoff,
		TransactionFrameAndHistory,
		TransactionAlternateEnter,
		TransactionAlternateWrite,
		TransactionAlternateExit,
		TransactionPromptEditor,
		TransactionBell,
		TransactionTitle,
		TransactionContextBarrier,
		TransactionShutdownCleanup,
		TransactionLegacyFlush,
		TransactionLegacyImmediate:
		return true
	default:
		return false
	}
}

func validOperationKind(k RenderOperationKind) bool {
	switch k {
	case RenderOperationText, RenderOperationCursor, RenderOperationErase,
		RenderOperationScroll, RenderOperationStyle, RenderOperationControl:
		return true
	default:
		return false
	}
}

// validateIntent checks producer-owned fields before a sequence is allocated.
// In particular, an empty payload is a context transition, never a visible
// frame/control transaction.
func validateIntent(i RenderIntent, maxBytes int) error {
	if !validTransactionKind(i.Kind) {
		return NewClassifiedError(DeliveryErrorInvalid, "unknown transaction kind")
	}
	if len(i.Bytes) == 0 && i.Kind != TransactionContextBarrier {
		return NewClassifiedError(DeliveryErrorInvalid, "empty render batch bytes")
	}
	if maxBytes > 0 && len(i.Bytes) > maxBytes {
		return NewClassifiedError(DeliveryErrorOversized, "intent exceeds max bytes")
	}
	if i.HistoryEpoch != nil && !historyBearingKind(i.Kind) {
		return NewClassifiedError(DeliveryErrorInvalid,
			"history epoch not allowed for kind "+string(i.Kind))
	}
	if i.Semantic != nil {
		if i.Semantic.SchemaVersion != SchemaVersion {
			return NewClassifiedError(DeliveryErrorInvalid, "unsupported semantic schema")
		}
	}
	for _, op := range i.Operations {
		if op.SchemaVersion != SchemaVersion || op.Count == 0 || !validOperationKind(op.Kind) {
			return NewClassifiedError(DeliveryErrorInvalid, "invalid render operation")
		}
	}
	return nil
}

// TerminalGeometry 描述一次渲染上下文的几何形状，immutable。
type TerminalGeometry struct {
	Width  int
	Height int
}

// TerminalProfileRef 是终端能力 profile（ANSI/color/Unicode capability）的
// 身份；同一 ID 的不同版本视为不同 profile。
type TerminalProfileRef struct {
	ID      string
	Version uint32
}

// HistoryDeliveryDomain 由 gateway 按当前 primary target 盖章。HistoryEpoch
// 的数值由 TerminalSession/history authority 提供；gateway 不生成 token，
// 也不拥有 Ack 权限。非 history batch 的 History 为 nil。
type HistoryDeliveryDomain struct {
	ProjectionTargetID string
	HistoryEpoch       uint64
}

// RenderTerminalContext 在 serial boundary 内与 bytes 一起记录，禁止依赖
// 不可回放的并发 SetGeometry 旁路。
type RenderTerminalContext struct {
	Geometry         TerminalGeometry
	Profile          TerminalProfileRef
	LayoutGeneration uint64
	TerminalEpoch    uint64
	Frame            uint64
	LeaseID          uint64
}

// SemanticPayload 是 producer 已有的可信语义；gateway 不从 ANSI bytes 反推。
// immutable、有大小上限，且 replay 可版本化验证。
type SemanticPayload struct {
	SchemaVersion uint32
	PlainText     string
	LogicalRows   []string
	SummaryHash   string
	SourceIDs     []string
}

// RenderOperationKind 是低层诊断枚举闭集。
type RenderOperationKind string

const (
	RenderOperationText    RenderOperationKind = "text"
	RenderOperationCursor  RenderOperationKind = "cursor"
	RenderOperationErase   RenderOperationKind = "erase"
	RenderOperationScroll  RenderOperationKind = "scroll"
	RenderOperationStyle   RenderOperationKind = "style"
	RenderOperationControl RenderOperationKind = "control"
)

// RenderOperation 是可选、低基数的诊断汇总，不携带业务对象或待执行参数。
type RenderOperation struct {
	SchemaVersion uint32
	Kind          RenderOperationKind
	Count         uint32
}

// RenderIntent 只含 producer 确定的事实；session identity 由 gateway 绑定。
type RenderIntent struct {
	IntentID      string // 可选诊断 ID，不作为 gateway 幂等键
	ParentBatchID string // retry/recovery/补偿时关联旧 batch
	Kind          TransactionKind
	Source        string
	Cause         string
	Bytes         []byte
	Semantic      *SemanticPayload  // 可选可信语义，不从 ANSI 反推
	Operations    []RenderOperation // 可选诊断摘要，不是执行计划
	HistoryEpoch  *uint64           // 可选；只由 history authority 提供 epoch
	Terminal      RenderTerminalContext
}

// RenderBatch 由 gateway 复制 intent 并盖章；producer 不得自行填写这些字段。
type RenderBatch struct {
	RenderIntent
	SessionID             string
	Sequence              uint64
	BatchID               string
	RouteEpoch            uint64
	ProjectionTargetID    string
	ProjectionTargetClass TargetClass
	BindingGeneration     uint64
	History               *HistoryDeliveryDomain
	PreparedAt            time.Time

	// These gateway-owned snapshots keep an admitted batch bound to the
	// route/sink that stamped it. They are intentionally unexported and never
	// serialized as part of the public batch contract.
	primarySink RenderOutputSink
	primaryDesc TargetDescriptor
	mirrorSlots []*mirrorSlot
}

// deepCopy 返回 batch 的深拷贝（sink 不得修改 batch）。
func (b RenderBatch) deepCopy() RenderBatch {
	out := b
	intent := b.RenderIntent.deepCopy()
	out.RenderIntent = intent
	if b.History != nil {
		h := *b.History
		out.History = &h
	}
	out.PreparedAt = b.PreparedAt
	return out
}

// deepCopy 返回 intent 的深拷贝；sink 不得修改 batch，故 gateway 在准入时
// 调用一次。
func (i RenderIntent) deepCopy() RenderIntent {
	out := i
	if i.Bytes != nil {
		out.Bytes = make([]byte, len(i.Bytes))
		copy(out.Bytes, i.Bytes)
	}
	if i.Semantic != nil {
		sem := *i.Semantic
		sem.LogicalRows = append([]string(nil), i.Semantic.LogicalRows...)
		sem.SourceIDs = append([]string(nil), i.Semantic.SourceIDs...)
		out.Semantic = &sem
	}
	if i.Operations != nil {
		out.Operations = append([]RenderOperation(nil), i.Operations...)
	}
	if i.HistoryEpoch != nil {
		v := *i.HistoryEpoch
		out.HistoryEpoch = &v
	}
	return out
}

// WriteCertainty 描述目标对字节写入的证明强度。
type WriteCertainty string

const (
	WriteCertaintyZero    WriteCertainty = "zero"
	WriteCertaintyFull    WriteCertainty = "full"
	WriteCertaintyUnknown WriteCertainty = "unknown"
)

// DeliveryStatus 是 target-level 交付结果。pre-admission 不使用本类型。
type DeliveryStatus string

const (
	DeliveryCommitted       DeliveryStatus = "committed"
	DeliveryFailedZeroBytes DeliveryStatus = "failed_zero_bytes"
	DeliveryUnknownPartial  DeliveryStatus = "unknown_partial"
	DeliveryDeferred        DeliveryStatus = "deferred"
	DeliveryRejected        DeliveryStatus = "rejected"
)

// DeliveryErrorClass 是稳定、可分支的 error class；运行时原始 error 只保留
// 在诊断中，不进入 delivery record。
type DeliveryErrorClass string

const (
	DeliveryErrorNone               DeliveryErrorClass = ""
	DeliveryErrorSink               DeliveryErrorClass = "sink"
	DeliveryErrorWriterContract     DeliveryErrorClass = "writer_contract"
	DeliveryErrorCanceledBeforeIO   DeliveryErrorClass = "canceled_before_io"
	DeliveryErrorCanceledAfterStart DeliveryErrorClass = "canceled_after_start"
	DeliveryErrorControlCanceled    DeliveryErrorClass = "control_canceled"
	DeliveryErrorClosed             DeliveryErrorClass = "closed"
	DeliveryErrorInvalid            DeliveryErrorClass = "invalid"
	DeliveryErrorOversized          DeliveryErrorClass = "oversized"
	DeliveryErrorStaleRoute         DeliveryErrorClass = "stale_route"
	DeliveryErrorReconfiguring      DeliveryErrorClass = "reconfiguring"
	DeliveryErrorQueueFull          DeliveryErrorClass = "queue_full"
	DeliveryErrorTimeout            DeliveryErrorClass = "timeout"
	DeliveryErrorAbandoned          DeliveryErrorClass = "abandoned"
)

// ClassifiedDeliveryError 是 lifecycle/control API 返回的稳定 typed error；
// receipt 仍以 ErrorClass 为分支依据。
type ClassifiedDeliveryError interface {
	error
	DeliveryClass() DeliveryErrorClass
}

// classifiedError 实现 ClassifiedDeliveryError。
type classifiedError struct {
	class DeliveryErrorClass
	msg   string
}

func (e *classifiedError) Error() string {
	if e == nil || e.msg == "" {
		return string(e.class)
	}
	return e.msg
}

func (e *classifiedError) DeliveryClass() DeliveryErrorClass {
	if e == nil {
		return DeliveryErrorNone
	}
	return e.class
}

// NewClassifiedError 构造一个稳定 typed error；class 不能为空。
func NewClassifiedError(class DeliveryErrorClass, msg string) ClassifiedDeliveryError {
	return &classifiedError{class: class, msg: msg}
}

// ClassOf 提取任意 error 的 DeliveryErrorClass；非 classified error 返回
// DeliveryErrorSink（保守默认）。
func ClassOf(err error) DeliveryErrorClass {
	var ce ClassifiedDeliveryError
	if errors.As(err, &ce) {
		return ce.DeliveryClass()
	}
	return DeliveryErrorSink
}

// AdmissionDecision 是 pre-admission decision。
type AdmissionDecision string

const (
	AdmissionAccepted AdmissionDecision = "accepted"
	AdmissionDeferred AdmissionDecision = "deferred"
	AdmissionRejected AdmissionDecision = "rejected"
)

// AdmissionReceipt 是 pre-admission 决策；Message 必须经过清理。
type AdmissionReceipt struct {
	Decision   AdmissionDecision
	ErrorClass DeliveryErrorClass
	Message    string
}

// SinkDeliveryResult 是 sink 返回的纯 I/O outcome；identity、invocation 和
// 时间由 gateway 盖章。
type SinkDeliveryResult struct {
	Status                  DeliveryStatus
	Certainty               WriteCertainty
	ErrorClass              DeliveryErrorClass
	AttemptedBytes          int
	AcceptedBytes           int
	MayHavePartiallyWritten bool
	Err                     error
}

// normalizeSinkResult 校验 sink result 的合法性，非法结果保守归一化为
// UnknownPartial/Unknown + DeliveryErrorSink，绝不提升为 committed/zero
// proof。
//
// 证明判据（与 io.Writer 语义一致：AttemptedBytes 是尝试计数，不是证明）：
//   - full proof：attempted==accepted==batchLen 且 certainty==Full 且无 Err；
//   - zero proof：accepted==0 且 certainty==Zero；
//   - committed 带 Err/ErrorClass：非法，降级为 unknown；
//   - 其余不匹配组合：降级为 unknown partial。
func normalizeSinkResult(r SinkDeliveryResult, batchLen int) SinkDeliveryResult {
	out := r
	if out.AttemptedBytes < 0 {
		out.AttemptedBytes = 0
	}
	if out.AcceptedBytes < 0 {
		out.AcceptedBytes = 0
	}
	if out.AttemptedBytes > batchLen {
		out.AttemptedBytes = batchLen
	}
	if out.AcceptedBytes > out.AttemptedBytes {
		out.AcceptedBytes = out.AttemptedBytes
	}
	hasErr := out.Err != nil || out.ErrorClass != DeliveryErrorNone
	switch out.Status {
	case DeliveryCommitted:
		if out.AttemptedBytes == batchLen && out.AcceptedBytes == batchLen &&
			out.Certainty == WriteCertaintyFull && !hasErr &&
			!out.MayHavePartiallyWritten {
			out.ErrorClass = DeliveryErrorNone
			out.Err = nil
			out.MayHavePartiallyWritten = false
			return out
		}
		// committed 带错误/计数不匹配：非法，降级 unknown。
		out.Status = DeliveryUnknownPartial
		out.Certainty = WriteCertaintyUnknown
		out.ErrorClass = DeliveryErrorSink
		out.MayHavePartiallyWritten = true
		if out.Err == nil {
			out.Err = NewClassifiedError(DeliveryErrorSink, "committed result with error")
		}
		return out
	case DeliveryFailedZeroBytes:
		// A zero-byte failure may report how many bytes it attempted, but it
		// must never report accepted bytes.  The gateway supplies a stable
		// class when a sink omitted one.
		if out.AcceptedBytes == 0 && out.Certainty == WriteCertaintyZero {
			if !hasErr {
				out.ErrorClass = DeliveryErrorSink
			}
			out.MayHavePartiallyWritten = false
			return out
		}
	case DeliveryDeferred, DeliveryRejected:
		// Deferred/rejected are explicit no-accepted-byte outcomes.  Some
		// sinks report the size they inspected/attempted even when they can
		// prove that no byte was accepted (the attempted counter is a count,
		// not a write proof).  Preserve that diagnostic count rather than
		// manufacturing an UnknownPartial result; only AcceptedBytes must be
		// zero and the count must already have been clamped above.
		if out.AcceptedBytes == 0 && out.Certainty == WriteCertaintyZero {
			if !hasErr {
				out.ErrorClass = DeliveryErrorSink
			}
			out.MayHavePartiallyWritten = false
			return out
		}
	case DeliveryUnknownPartial:
		out.Certainty = WriteCertaintyUnknown
		if !hasErr {
			out.ErrorClass = DeliveryErrorSink
		}
		out.MayHavePartiallyWritten = true
		return out
	}
	// 非法或者任何未覆盖组合：保守归一化。
	out.Status = DeliveryUnknownPartial
	out.Certainty = WriteCertaintyUnknown
	out.ErrorClass = DeliveryErrorSink
	out.MayHavePartiallyWritten = true
	if out.Err == nil {
		out.Err = errors.New("invalid sink delivery result")
	}
	return out
}

// TargetReceipt 是 gateway 为一次真实 sink callback 盖章的 target-level 事实。
type TargetReceipt struct {
	SessionID          string
	Sequence           uint64
	BatchID            string
	RouteEpoch         uint64
	BindingGeneration  uint64
	SinkID             string
	TargetClass        TargetClass
	ProjectionTargetID string
	InvocationID       uint64 // gateway 为该 sink callback 分配；与 SinkID 组合唯一
	Synthetic          bool   // outcome 在 callback return 前由 lifecycle finalizer 固定
	SinkDeliveryResult
	CallbackReturned bool // synthetic timeout/close outcome 为 false
	StartedAt        time.Time
	FinishedAt       time.Time // CallbackReturned=false 时必须为零值
	OutcomeFixedAt   time.Time // gateway 固定 outcome 的时间；不等同 callback 返回时间
}

// OutputReceipt 同时表达 admission 和 primary target delivery。
// pre-admission rejection/defer 时 Primary=nil、Sequence=0；sink 已被调用后
// Primary 必须非 nil，即使 target 返回 Rejected/Deferred/zero-byte failure。
type OutputReceipt struct {
	SessionID                  string
	Sequence                   uint64
	BatchID                    string
	RouteEpoch                 uint64
	ProjectionTargetID         string
	ProjectionTargetClass      TargetClass
	BindingGeneration          uint64
	History                    *HistoryDeliveryDomain
	Admission                  AdmissionReceipt
	TargetInvoked              bool
	Primary                    *TargetReceipt
	MirrorsScheduled           int // 已非阻塞接纳到 bounded scheduler 的数量
	MirrorScheduleDrops        int // queue 满/closing 等即时未接纳数量
	ObserverDrops              uint64
	ReceiptCutoffEventSequence uint64                   // 唯一 cutoff marker 的 EventSequence
	MirrorAdmissions           []MirrorAdmissionReceipt // 按 route.Mirrors 顺序；detached
}

// MirrorDeliveryStatus 是 mirror entry 的最终 delivery 状态。
type MirrorDeliveryStatus string

const (
	MirrorApplied MirrorDeliveryStatus = "applied"
	MirrorSkipped MirrorDeliveryStatus = "skipped"
	MirrorFailed  MirrorDeliveryStatus = "failed"
)

// MirrorSkipReason 解释 MirrorSkipped 的原因。
type MirrorSkipReason string

const (
	MirrorSkipMetadataOnly        MirrorSkipReason = "metadata_only"
	MirrorSkipPrimaryNotCommitted MirrorSkipReason = "primary_not_committed"
	MirrorSkipCapturePolicy       MirrorSkipReason = "capture_policy"
)

// MirrorEntryRef 是 gateway 为一笔已 admission batch 的某个 configured
// mirror 分配的稳定引用。它在 schedule drop、quarantine 和 late completion
// 中仍然保留；pre-admission intent 没有 mirror entry。
type MirrorEntryRef struct {
	EntryID            string
	MirrorIndex        int // route.Mirrors 中的零基索引，决定 record 顺序
	SinkID             string
	TargetClass        TargetClass
	ProjectionTargetID string
}

// MirrorAdmissionReceipt 只报告 receipt 返回前已经完成的 enqueue/drop 判定，
// 不代表 mirror I/O 已开始或已完成。
type MirrorAdmissionReceipt struct {
	EntryID            string
	MirrorIndex        int
	SinkID             string
	TargetClass        TargetClass
	ProjectionTargetID string
	Policy             MirrorPolicy
	RequestedApplyMode MirrorApplyMode
	EffectiveApplyMode MirrorApplyMode
	NonAuthoritative   bool
	Scheduled          bool
	ErrorClass         DeliveryErrorClass // drop 时非空；scheduled 时为空
	SkipReason         MirrorSkipReason   // outcome-aware 跳过原因；未跳过为空
}

// MirrorReceipt 是 mirror entry 的最终事实。
type MirrorReceipt struct {
	EntryID                 string
	MirrorIndex             int
	SinkID                  string
	TargetClass             TargetClass
	ProjectionTargetID      string
	InvocationID            uint64 // callback 未启动/未调用 sink 时为零
	Synthetic               bool   // gateway 在 callback return 前固定或补齐的 outcome
	ObservedPrimaryTargetID string
	Policy                  MirrorPolicy
	RequestedApplyMode      MirrorApplyMode
	EffectiveApplyMode      MirrorApplyMode
	NonAuthoritative        bool
	Scheduled               bool
	SinkInvoked             bool
	TargetInvoked           bool
	CallbackReturned        bool // callback 已返回；无 target 时仍用于区分 timeout/quarantine
	Status                  MirrorDeliveryStatus
	Target                  *TargetReceipt
	ErrorClass              DeliveryErrorClass
	Err                     error // 仅运行时；record 只保存 class + safe message
	SkipReason              MirrorSkipReason
	SealedAt                time.Time // entry 终态被固定的时间；不是 target 完成时间的替代
}

// RecordedPayloadMode 描述 journal/record 中的 payload 保存模式。
type RecordedPayloadMode string

const (
	RecordedMetadataOnly  RecordedPayloadMode = "metadata_only"
	RecordedHashOnly      RecordedPayloadMode = "hash_only"
	RecordedTruncated     RecordedPayloadMode = "truncated"
	RecordedFullAvailable RecordedPayloadMode = "full_available"
)

// RecordedBatch 是 sanitized batch 元数据；网关不保存原始 bytes。
type RecordedBatch struct {
	SessionID             string
	BatchID               string
	IntentID              string
	ParentBatchID         string
	Sequence              uint64
	RouteEpoch            uint64
	ProjectionTargetID    string
	ProjectionTargetClass TargetClass
	BindingGeneration     uint64
	Kind                  TransactionKind
	Source                string
	Cause                 string
	Terminal              RenderTerminalContext
	History               *HistoryDeliveryDomain
	PayloadMode           RecordedPayloadMode // gateway journal 仅允许 metadata_only/hash_only
	BytesLength           int
	BytesHash             string
}

// RecordedTargetReceipt 是 TargetReceipt 的 sanitized 形态。
type RecordedTargetReceipt struct {
	SessionID          string
	Sequence           uint64
	BatchID            string
	RouteEpoch         uint64
	BindingGeneration  uint64
	SinkID             string
	TargetClass        TargetClass
	ProjectionTargetID string
	InvocationID       uint64
	Synthetic          bool
	Status             DeliveryStatus
	Certainty          WriteCertainty
	ErrorClass         DeliveryErrorClass
	SafeMessage        string
	AttemptedBytes     int
	AcceptedBytes      int
	CallbackReturned   bool
	StartedAt          time.Time
	FinishedAt         time.Time
	OutcomeFixedAt     time.Time
}

// RecordedMirrorReceipt 是 MirrorReceipt 的 sanitized 形态。
type RecordedMirrorReceipt struct {
	EntryID                 string
	MirrorIndex             int
	SinkID                  string
	TargetClass             TargetClass
	ProjectionTargetID      string
	InvocationID            uint64
	Synthetic               bool
	ObservedPrimaryTargetID string
	Policy                  MirrorPolicy
	RequestedApplyMode      MirrorApplyMode
	EffectiveApplyMode      MirrorApplyMode
	NonAuthoritative        bool
	Scheduled               bool
	SinkInvoked             bool
	TargetInvoked           bool
	CallbackReturned        bool
	Status                  MirrorDeliveryStatus
	Target                  *RecordedTargetReceipt
	ErrorClass              DeliveryErrorClass
	SafeMessage             string
	SkipReason              MirrorSkipReason
	SealedAt                time.Time
}

// RecordedOutputReceipt 是 OutputReceipt 的 sanitized 形态。
type RecordedOutputReceipt struct {
	SessionID                  string
	Sequence                   uint64
	BatchID                    string
	RouteEpoch                 uint64
	ProjectionTargetID         string
	ProjectionTargetClass      TargetClass
	BindingGeneration          uint64
	History                    *HistoryDeliveryDomain
	Admission                  AdmissionReceipt
	TargetInvoked              bool
	Primary                    *RecordedTargetReceipt
	MirrorsScheduled           int
	MirrorScheduleDrops        int
	ObserverDrops              uint64
	ReceiptCutoffEventSequence uint64
	MirrorAdmissions           []MirrorAdmissionReceipt
}

// DeliveryRecord 是 gateway/journal 封存的最终诊断事实。它不是运行时
// OutputReceipt 的别名：不得包含 error interface、未清理的文本或默认完整
// bytes。封存后不可变。
type DeliveryRecord struct {
	RecordID      string // gateway 在 record seal 时分配；与 batch identity 独立
	SchemaVersion uint32
	Batch         RecordedBatch
	Output        RecordedOutputReceipt
	Mirrors       []RecordedMirrorReceipt
	SealedAt      time.Time
}

func cloneHistory(h *HistoryDeliveryDomain) *HistoryDeliveryDomain {
	if h == nil {
		return nil
	}
	cp := *h
	return &cp
}

func cloneTargetReceipt(r *RecordedTargetReceipt) *RecordedTargetReceipt {
	if r == nil {
		return nil
	}
	cp := *r
	return &cp
}

func cloneMirrorAdmissionSlice(in []MirrorAdmissionReceipt) []MirrorAdmissionReceipt {
	if in == nil {
		return nil
	}
	out := make([]MirrorAdmissionReceipt, len(in))
	copy(out, in)
	return out
}

func cloneRecordedMirror(r RecordedMirrorReceipt) RecordedMirrorReceipt {
	out := r
	out.Target = cloneTargetReceipt(r.Target)
	return out
}

func cloneRecordedOutput(r RecordedOutputReceipt) RecordedOutputReceipt {
	out := r
	out.History = cloneHistory(r.History)
	out.MirrorAdmissions = cloneMirrorAdmissionSlice(r.MirrorAdmissions)
	out.Primary = cloneTargetReceipt(r.Primary)
	return out
}

func cloneDeliveryRecord(r DeliveryRecord) DeliveryRecord {
	out := r
	out.Batch.History = cloneHistory(r.Batch.History)
	out.Output = cloneRecordedOutput(r.Output)
	if r.Mirrors != nil {
		out.Mirrors = make([]RecordedMirrorReceipt, len(r.Mirrors))
		for i := range r.Mirrors {
			out.Mirrors[i] = cloneRecordedMirror(r.Mirrors[i])
		}
	}
	return out
}

// ToRecorded 把运行时 receipt 转换为 sanitized record 形态。
// primaryErr/mirrorErr 只用于提取 stable class + safe message，不进入存储。
func (r TargetReceipt) ToRecorded(safeMessage string) RecordedTargetReceipt {
	return RecordedTargetReceipt{
		SessionID:          r.SessionID,
		Sequence:           r.Sequence,
		BatchID:            r.BatchID,
		RouteEpoch:         r.RouteEpoch,
		BindingGeneration:  r.BindingGeneration,
		SinkID:             r.SinkID,
		TargetClass:        r.TargetClass,
		ProjectionTargetID: r.ProjectionTargetID,
		InvocationID:       r.InvocationID,
		Synthetic:          r.Synthetic,
		Status:             r.Status,
		Certainty:          r.Certainty,
		ErrorClass:         r.ErrorClass,
		SafeMessage:        safeMessage,
		AttemptedBytes:     r.AttemptedBytes,
		AcceptedBytes:      r.AcceptedBytes,
		CallbackReturned:   r.CallbackReturned,
		StartedAt:          r.StartedAt,
		FinishedAt:         r.FinishedAt,
		OutcomeFixedAt:     r.OutcomeFixedAt,
	}
}

// ToRecorded 把运行时 mirror receipt 转换为 sanitized record 形态，
// 并保留回调返回事实；错误只保留稳定 class 与 safe message。
func (r MirrorReceipt) ToRecorded(safeMessage string) RecordedMirrorReceipt {
	out := RecordedMirrorReceipt{
		EntryID:                 r.EntryID,
		MirrorIndex:             r.MirrorIndex,
		SinkID:                  r.SinkID,
		TargetClass:             r.TargetClass,
		ProjectionTargetID:      r.ProjectionTargetID,
		InvocationID:            r.InvocationID,
		Synthetic:               r.Synthetic,
		ObservedPrimaryTargetID: r.ObservedPrimaryTargetID,
		Policy:                  r.Policy,
		RequestedApplyMode:      r.RequestedApplyMode,
		EffectiveApplyMode:      r.EffectiveApplyMode,
		NonAuthoritative:        r.NonAuthoritative,
		Scheduled:               r.Scheduled,
		SinkInvoked:             r.SinkInvoked,
		TargetInvoked:           r.TargetInvoked,
		CallbackReturned:        r.CallbackReturned,
		Status:                  r.Status,
		ErrorClass:              r.ErrorClass,
		SafeMessage:             safeMessage,
		SkipReason:              r.SkipReason,
		SealedAt:                r.SealedAt,
	}
	if r.Target != nil {
		t := r.Target.ToRecorded(safeMessage)
		out.Target = &t
	}
	return out
}

// ToRecorded 把运行时 receipt 转换为 sanitized record 形态。
func (r OutputReceipt) ToRecorded() RecordedOutputReceipt {
	out := RecordedOutputReceipt{
		SessionID:                  r.SessionID,
		Sequence:                   r.Sequence,
		BatchID:                    r.BatchID,
		RouteEpoch:                 r.RouteEpoch,
		ProjectionTargetID:         r.ProjectionTargetID,
		ProjectionTargetClass:      r.ProjectionTargetClass,
		BindingGeneration:          r.BindingGeneration,
		History:                    cloneHistory(r.History),
		Admission:                  r.Admission,
		TargetInvoked:              r.TargetInvoked,
		MirrorsScheduled:           r.MirrorsScheduled,
		MirrorScheduleDrops:        r.MirrorScheduleDrops,
		ObserverDrops:              r.ObserverDrops,
		ReceiptCutoffEventSequence: r.ReceiptCutoffEventSequence,
		MirrorAdmissions:           cloneMirrorAdmissionSlice(r.MirrorAdmissions),
	}
	if r.Primary != nil {
		p := r.Primary.ToRecorded("")
		out.Primary = &p
	}
	return out
}

// SanitizedBatch 构造 RecordedBatch：gateway journal 仅允许
// metadata_only/hash_only 模式；内部调用会把其他模式收敛为 metadata_only。
func SanitizedBatch(b RenderBatch, mode RecordedPayloadMode, hashFn func([]byte) string) RecordedBatch {
	if mode != RecordedMetadataOnly && mode != RecordedHashOnly {
		mode = RecordedMetadataOnly
	}
	rb := RecordedBatch{
		SessionID:             b.SessionID,
		BatchID:               b.BatchID,
		IntentID:              b.IntentID,
		ParentBatchID:         b.ParentBatchID,
		Sequence:              b.Sequence,
		RouteEpoch:            b.RouteEpoch,
		ProjectionTargetID:    b.ProjectionTargetID,
		ProjectionTargetClass: b.ProjectionTargetClass,
		BindingGeneration:     b.BindingGeneration,
		Kind:                  b.Kind,
		Source:                b.Source,
		Cause:                 b.Cause,
		Terminal:              b.Terminal,
		History:               b.History,
		PayloadMode:           mode,
		BytesLength:           len(b.Bytes),
	}
	if mode == RecordedHashOnly && hashFn != nil && len(b.Bytes) > 0 {
		rb.BytesHash = hashFn(b.Bytes)
	}
	return rb
}

// ErrPayloadNotFound 表示 payload handle 未找到或已撤销。
var ErrPayloadNotFound = errors.New("payload not found")
