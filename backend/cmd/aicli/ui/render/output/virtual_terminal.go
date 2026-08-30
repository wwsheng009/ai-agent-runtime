package output

import (
	"context"
	"sync"
	"time"
)

// ============================================================================
// VirtualTerminalSink（7.3）
// ============================================================================

// TerminalEmulator 是 render/output 依赖的最小终端解释器窄接口。由
// ui/integration 层提供 ui/vt adapter（ui/vt 依赖 ui/render，render/output
// 不得直接 import 上层包）。实现必须：
//   - ApplyContext 在应用任何 bytes 前原子设置 geometry/profile；
//   - Apply 解释已经编码的 ANSI bytes；
//   - Snapshot 返回 detached 快照；
//   - Invalidate 把内部投影标记为不可证明（partial/abort 后）。
type TerminalEmulator interface {
	ApplyContext(geometry TerminalGeometry, profile TerminalProfileRef) error
	Apply(bytes []byte) error
	Snapshot() VirtualProjectionSnapshot
	Invalidate()
}

// VirtualSinkOptions 是 VirtualTerminalSink 的可选配置。
type VirtualSinkOptions struct {
	// MaxScrollback 限制 snapshots 中的 scrollback 行数（0 表示不额外截断）。
	MaxScrollback int
}

// VirtualTerminalSink 把已经编码的 ANSI bytes 应用到注入的 terminal
// emulator，生成可观察 screen/scrollback/cursor 快照。它验证"终端解释器
// 看到什么"，与 semantic capture 不同。
//
// 应用策略由 MirrorEnvelope.EffectiveApplyMode、mirror policy 和 primary
// outcome 共同决定，不由 virtual sink 猜测（见 7.3 策略 1-3）：
//   - primary committed：best_effort/committed_only 可 ApplyBytes；virtual
//     只是观察 target，不成为 physical authority；
//   - primary zero/deferred/rejected：默认 MetadataOnly + Skipped，只有
//     显式 Attempted+Bytes 才应用并标 non-authoritative；
//   - primary UnknownPartial：默认立即 Invalidate，不猜测物理前缀。
//
// 每个 batch 先原子应用 RenderTerminalContext 的 geometry/profile，再解释
// effective bytes；非法 geometry/profile 在任何 bytes 前返回 zero rejection。
// 作为 mirror 时从 MirrorEnvelope 获取 primary outcome；作为 primary 时
// ObservedPrimaryTargetID==ProjectionTargetID 且 LastMirrorEntryID 为空。
type VirtualTerminalSink struct {
	desc                TargetDescriptor
	emu                 TerminalEmulator
	opts                VirtualSinkOptions
	clock               Clock
	mu                  sync.Mutex
	state               SinkLifecycleState
	validity            ProjectionValidity
	nonAuth             bool
	lastSeq             uint64
	lastBatchID         string
	lastMirrorEntryID   string
	lastObservedPrimary string
	lastProfile         TerminalProfileRef
	lastSeenAt          time.Time
}

// NewVirtualTerminalSink 创建 virtual sink；emu 不能为 nil。
func NewVirtualTerminalSink(projectionTargetID string, emu TerminalEmulator, opts VirtualSinkOptions) *VirtualTerminalSink {
	if emu == nil {
		// 防御：nil emulator 保持 Unavailable 快照，不 panic。
		emu = &nullEmulator{}
	}
	return &VirtualTerminalSink{
		desc: TargetDescriptor{
			SinkID:             "virtual",
			Class:              TargetClassVirtual,
			ProjectionTargetID: projectionTargetID,
		},
		emu:      emu,
		opts:     opts,
		clock:    SystemClock{},
		state:    SinkLifecycleOpen,
		validity: ProjectionUnavailable,
	}
}

func (v *VirtualTerminalSink) Descriptor() TargetDescriptor { return v.desc }

// SetClock injects the timestamp source used by sink snapshots and projection
// metadata.  It is primarily useful for deterministic contract tests.
func (v *VirtualTerminalSink) SetClock(clk Clock) {
	if v == nil || clk == nil {
		return
	}
	v.mu.Lock()
	v.clock = clk
	v.mu.Unlock()
}

// Submit 是 virtual 作为 primary 的入口（Phase 2+ 支持 primary virtual
// route）。每个 batch 原子应用 context，再解释 bytes。
func (v *VirtualTerminalSink) Submit(ctx context.Context, batch RenderBatch) SinkDeliveryResult {
	if ctx != nil && ctx.Err() != nil {
		return SinkDeliveryResult{
			Status:         DeliveryFailedZeroBytes,
			Certainty:      WriteCertaintyZero,
			ErrorClass:     DeliveryErrorCanceledAfterStart,
			AttemptedBytes: len(batch.Bytes),
			AcceptedBytes:  0,
			Err:            NewClassifiedError(DeliveryErrorCanceledAfterStart, "canceled before virtual apply"),
		}
	}
	if len(batch.Bytes) == 0 && batch.Kind != TransactionContextBarrier {
		return SinkDeliveryResult{
			Status:         DeliveryRejected,
			Certainty:      WriteCertaintyZero,
			ErrorClass:     DeliveryErrorInvalid,
			AttemptedBytes: 0,
			AcceptedBytes:  0,
			Err:            NewClassifiedError(DeliveryErrorInvalid, "empty non-barrier virtual batch"),
		}
	}
	v.mu.Lock()
	if v.state != SinkLifecycleOpen {
		state := v.state
		v.mu.Unlock()
		class := DeliveryErrorClosed
		if state == SinkLifecycleAbandoned {
			class = DeliveryErrorAbandoned
		}
		return SinkDeliveryResult{
			Status:         DeliveryRejected,
			Certainty:      WriteCertaintyZero,
			ErrorClass:     class,
			AttemptedBytes: len(batch.Bytes),
			AcceptedBytes:  0,
			Err:            NewClassifiedError(class, "virtual sink "+string(state)),
		}
	}
	v.mu.Unlock()

	// 原子应用 context（geometry/profile）；非法时在任何 bytes 前拒绝。
	if batch.Terminal.Geometry.Width < 1 || batch.Terminal.Geometry.Height < 1 {
		return SinkDeliveryResult{
			Status:         DeliveryRejected,
			Certainty:      WriteCertaintyZero,
			ErrorClass:     DeliveryErrorInvalid,
			AttemptedBytes: len(batch.Bytes),
			AcceptedBytes:  0,
			Err:            NewClassifiedError(DeliveryErrorInvalid, "invalid virtual geometry"),
		}
	}
	emulated := v.applyContext(batch.Terminal)
	emulated = v.applyBytes(emulated, batch.Bytes)
	v.mu.Lock()
	clock := v.clock
	v.lastSeq = batch.Sequence
	v.lastBatchID = batch.BatchID
	v.lastMirrorEntryID = ""
	v.lastObservedPrimary = v.desc.ProjectionTargetID
	validity := ProjectionValid
	if !emulated {
		validity = ProjectionUnknown
	}
	v.validity = validity
	v.nonAuth = false // primary 路径无条件清除 mirror 残留（7.3）。
	if clock == nil {
		clock = SystemClock{}
	}
	v.lastSeenAt = clock.Now()
	v.mu.Unlock()
	return SinkDeliveryResult{
		Status:         DeliveryCommitted,
		Certainty:      WriteCertaintyFull,
		ErrorClass:     DeliveryErrorNone,
		AttemptedBytes: len(batch.Bytes),
		AcceptedBytes:  len(batch.Bytes),
	}
}

// SubmitMirror 是 virtual 作为 mirror 的入口（7.3 策略 1-3）。
func (v *VirtualTerminalSink) SubmitMirror(_ context.Context, env MirrorEnvelope) MirrorSinkResult {
	v.mu.Lock()
	state := v.state
	v.mu.Unlock()
	if state != SinkLifecycleOpen {
		class := DeliveryErrorClosed
		if state == SinkLifecycleAbandoned {
			class = DeliveryErrorAbandoned
		}
		return MirrorSinkResult{
			Status:     MirrorFailed,
			ErrorClass: class,
			Err:        NewClassifiedError(class, "virtual sink "+string(state)),
		}
	}
	// 空 batch：barrier 只推进 context。
	if len(env.Bytes) == 0 {
		if env.Kind == TransactionContextBarrier {
			if !v.applyContext(env.Terminal) {
				return MirrorSinkResult{
					Status:     MirrorFailed,
					ErrorClass: DeliveryErrorInvalid,
					Err:        NewClassifiedError(DeliveryErrorInvalid, "invalid barrier context"),
				}
			}
			return MirrorSinkResult{Status: MirrorApplied, ErrorClass: DeliveryErrorNone}
		}
		return MirrorSinkResult{
			Status:     MirrorSkipped,
			ErrorClass: DeliveryErrorInvalid,
			Err:        NewClassifiedError(DeliveryErrorInvalid, "empty non-barrier virtual envelope"),
		}
	}
	// 策略：effective mode 与 non-authoritative 由 gateway/scheduler 决定。
	applyBytes := env.EffectiveApplyMode == MirrorApplyBytes
	nonAuth := env.NonAuthoritative
	if !applyBytes {
		// MetadataOnly：不解释 bytes；mirror 记录元数据（skipped）。
		v.mu.Lock()
		clock := v.clock
		v.lastSeq = env.Sequence
		v.lastBatchID = env.BatchID
		v.lastMirrorEntryID = env.EntryID
		v.lastObservedPrimary = env.MirrorEntryRef.ProjectionTargetID
		if clock == nil {
			clock = SystemClock{}
		}
		v.lastSeenAt = clock.Now()
		v.mu.Unlock()
		return MirrorSinkResult{
			Status:     MirrorSkipped,
			ErrorClass: DeliveryErrorNone,
		}
	}
	emulated := v.applyContext(env.Terminal)
	emulated = v.applyBytes(emulated, env.Bytes)
	v.mu.Lock()
	clock := v.clock
	v.lastSeq = env.Sequence
	v.lastBatchID = env.BatchID
	v.lastMirrorEntryID = env.EntryID
	v.lastObservedPrimary = env.MirrorEntryRef.ProjectionTargetID
	if nonAuth {
		v.validity = ProjectionUnknown
	} else if emulated {
		v.validity = ProjectionValid
	} else {
		v.validity = ProjectionUnknown
	}
	v.nonAuth = nonAuth
	if clock == nil {
		clock = SystemClock{}
	}
	v.lastSeenAt = clock.Now()
	v.mu.Unlock()
	if !emulated {
		return MirrorSinkResult{Status: MirrorFailed, ErrorClass: DeliveryErrorSink}
	}
	return MirrorSinkResult{Status: MirrorApplied, ErrorClass: DeliveryErrorNone}
}

// applyContext 原子应用 geometry/profile；失败标 invalid。
func (v *VirtualTerminalSink) applyContext(terminal RenderTerminalContext) bool {
	if err := v.emu.ApplyContext(terminal.Geometry, terminal.Profile); err != nil {
		v.mu.Lock()
		v.validity = ProjectionUnknown
		v.mu.Unlock()
		return false
	}
	v.mu.Lock()
	v.lastProfile = terminal.Profile
	v.mu.Unlock()
	return true
}

// applyBytes 解释 bytes；emulator 内部标记 invalid 时返回 false。
func (v *VirtualTerminalSink) applyBytes(emulated bool, bytes []byte) bool {
	if !emulated {
		v.emu.Invalidate()
		return false
	}
	if err := v.emu.Apply(bytes); err != nil {
		v.emu.Invalidate()
		return false
	}
	return true
}

// Invalidate 把投影标为不可证明（partial primary 后由 gateway/上层调用）。
func (v *VirtualTerminalSink) Invalidate() {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.validity != ProjectionUnknown {
		v.validity = ProjectionUnknown
	}
	v.emu.Invalidate()
}

// Projection 返回 detached 快照（7.3 约束：valid 时 geometry 正、cursor
// 在界内；nonAuth 时 validity 不得为 valid——已在更新处保证）。
func (v *VirtualTerminalSink) Projection() VirtualProjectionSnapshot {
	snap := v.emu.Snapshot()
	v.mu.Lock()
	defer v.mu.Unlock()
	// 覆盖 validity/identity 字段：emulator snapshot 不拥有它们。
	snap.SchemaVersion = SchemaVersion
	snap.Validity = v.validity
	snap.NonAuthoritative = v.nonAuth
	snap.LastSequence = v.lastSeq
	snap.LastBatchID = v.lastBatchID
	snap.LastMirrorEntryID = v.lastMirrorEntryID
	snap.ProjectionTargetID = v.desc.ProjectionTargetID
	snap.ObservedPrimaryTargetID = v.lastObservedPrimary
	snap.Profile = v.lastProfile
	// 边界约束：valid 时强制坐标在界内。
	if snap.Validity == ProjectionValid {
		if snap.Width < 1 || snap.Height < 1 {
			snap.Width, snap.Height = 1, 1
		}
		if snap.Cursor.Row < 0 || snap.Cursor.Row >= snap.Height {
			snap.Cursor.Row = 0
		}
		if snap.Cursor.Column < 0 || snap.Cursor.Column >= snap.Width {
			snap.Cursor.Column = 0
		}
	}
	// scrollback 截断（detached）。
	if v.opts.MaxScrollback > 0 && len(snap.Scrollback) > v.opts.MaxScrollback {
		snap.Scrollback = append([]string(nil), snap.Scrollback[len(snap.Scrollback)-v.opts.MaxScrollback:]...)
	} else if len(snap.Scrollback) > 0 {
		snap.Scrollback = append([]string(nil), snap.Scrollback...)
	}
	if len(snap.Rows) > 0 {
		snap.Rows = append([]string(nil), snap.Rows...)
	}
	return snap
}

func (v *VirtualTerminalSink) Snapshot() SinkSnapshot {
	v.mu.Lock()
	defer v.mu.Unlock()
	return SinkSnapshot{
		Descriptor: v.desc,
		State:      v.state,
		LastSeenAt: v.lastSeenAt,
	}
}

func (v *VirtualTerminalSink) Abort(AbortProof) error { return nil }

func (v *VirtualTerminalSink) Close(_ context.Context) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.state = SinkLifecycleClosed
	return nil
}

// nullEmulator 是 nil emulator 的防御实现（sink 保持 Unavailable）。
type nullEmulator struct{}

func (nullEmulator) ApplyContext(TerminalGeometry, TerminalProfileRef) error { return nil }
func (nullEmulator) Apply([]byte) error                                      { return nil }
func (nullEmulator) Invalidate()                                             {}

func (nullEmulator) Snapshot() VirtualProjectionSnapshot {
	return VirtualProjectionSnapshot{
		SchemaVersion: SchemaVersion,
		Validity:      ProjectionUnavailable,
	}
}
