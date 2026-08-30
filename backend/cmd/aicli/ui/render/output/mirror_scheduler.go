package output

import (
	"context"
	"sync"
	"time"
)

// ============================================================================
// outcome-aware mirror scheduler（6.5/11.2）
// ============================================================================

// mirrorEntry 是 mirror 队列中的一个 batch 的运行时状态。
type mirrorEntry struct {
	envelope  MirrorEnvelope
	primary   TargetReceipt
	admission MirrorAdmissionReceipt

	// outcome 状态
	status       MirrorDeliveryStatus
	target       *TargetReceipt // callback 返回后非 nil（含 rejected）
	errClass     DeliveryErrorClass
	errMsg       string
	skipReason   MirrorSkipReason
	sealed       bool
	sealedAt     time.Time
	callbackDone bool
	sinkInvoked  bool
}

// mirrorSlot 管理一个 mirror 的 bounded 队列与 entry seal。
type mirrorSlot struct {
	g         *RenderOutputGateway
	index     int
	cfg       RenderMirror
	desc      TargetDescriptor
	queue     chan *mirrorEntry
	mu        sync.Mutex
	pending   int
	inFlight  int
	scheduled uint64
	applied   uint64
	skipped   uint64
	failed    uint64
	timedOut  uint64
	late      uint64
	drops     uint64
}

func newMirrorSlot(g *RenderOutputGateway, index int, cfg RenderMirror) *mirrorSlot {
	desc := cfg.Sink.Descriptor()
	cap := g.opts.MirrorQueueCapacity
	if cap <= 0 {
		cap = 1024
	}
	return &mirrorSlot{
		g:     g,
		index: index,
		cfg:   cfg,
		desc:  desc,
		queue: make(chan *mirrorEntry, cap),
		mu:    sync.Mutex{},
	}
}

// closed 返回 gateway 是否已关闭（供 drop 分类）。
func (ms *mirrorSlot) closed() bool {
	select {
	case <-ms.g.closedCh:
		return true
	default:
		return false
	}
}

// enqueue 尝试入队；成功返回 true。队列满或 gateway 已 closing 时返回
// false（不登记、不 seal——drop 由 admission receipt 记录）。
//
// 并发契约：closedCh 检查 + 计数登记 + 发送在同一个 ms.mu 临界区内完成，
// 与 worker drain 分支的退出判定（同锁）互斥，杜绝“发送后 worker 已退出、
// entry 无人消费”的残留窗口。
func (ms *mirrorSlot) enqueue(batch RenderBatch, primary TargetReceipt, ad MirrorAdmissionReceipt) bool {
	// The entry is only constructed for a successful enqueue.  Keep its
	// immutable admission metadata consistent with the return value even
	// though the caller cannot know success until the channel send commits.
	ad.Scheduled = true
	entry := &mirrorEntry{
		envelope: MirrorEnvelope{
			MirrorEntryRef: MirrorEntryRef{
				EntryID:            ad.EntryID,
				MirrorIndex:        ad.MirrorIndex,
				SinkID:             ad.SinkID,
				TargetClass:        ad.TargetClass,
				ProjectionTargetID: ad.ProjectionTargetID,
			},
			RenderBatch:        batch,
			RequestedApplyMode: ad.RequestedApplyMode,
			NonAuthoritative:   ad.NonAuthoritative,
			Timeout:            ms.cfg.Timeout,
		},
		primary:   primary,
		admission: ad,
	}
	ms.mu.Lock()
	defer ms.mu.Unlock()
	select {
	case <-ms.g.closedCh:
		return false
	default:
	}
	// 先登记后发送；失败回滚登记。
	ms.pending++
	ms.scheduled++
	ms.g.mu.Lock()
	ms.g.stats.mirrorScheduled++
	ms.g.stats.mirrorPending++
	ms.g.mu.Unlock()
	select {
	case ms.queue <- entry:
		return true
	default:
		ms.pending--
		ms.scheduled--
		ms.drops++
		ms.g.mu.Lock()
		ms.g.stats.mirrorScheduled--
		ms.g.stats.mirrorPending--
		ms.g.stats.mirrorScheduleDrops++
		ms.g.mu.Unlock()
		return false
	}
}

// workerLoop 消费队列并调用 sink。每笔 entry 在 serial per-mirror 语义下
// 处理（mirror 之间并发，mirror 内部串行）。
func (ms *mirrorSlot) workerLoop() {
	for {
		select {
		case entry := <-ms.queue:
			ms.mu.Lock()
			ms.pending--
			ms.inFlight++
			ms.g.mu.Lock()
			ms.g.stats.mirrorPending--
			ms.g.stats.mirrorInFlight++
			ms.g.mu.Unlock()
			ms.mu.Unlock()

			entry.sinkInvoked = true
			ms.g.publish(OutputEvent{
				SchemaVersion: SchemaVersion,
				Kind:          EventMirrorLifecycle,
				At:            ms.g.clock.Now(),
				SessionID:     entry.envelope.SessionID,
				Sequence:      entry.envelope.Sequence,
				RouteEpoch:    entry.envelope.RouteEpoch,
				BatchID:       entry.envelope.BatchID,
				MirrorEntryID: entry.envelope.EntryID,
				MirrorIndex:   ms.index,
				MirrorPhase:   MirrorPhaseCallbackStarted,
				Policy:        ms.cfg.Policy,
				SinkInvoked:   true,
			})

			ctx, cancel := contextWithClockTimeout(ms.g.clock, context.Background(), ms.cfg.Timeout)
			res := mirrorSubmitWithPanicGuard(ms.cfg.Sink.SubmitMirror, ctx, entry.envelope)
			cancel()

			entry.callbackDone = true
			ms.applyMirrorOutcome(entry, res)
			ms.sealEntry(entry)

			ms.mu.Lock()
			ms.inFlight--
			ms.g.mu.Lock()
			ms.g.stats.mirrorInFlight--
			ms.g.mu.Unlock()
			ms.mu.Unlock()
		case <-ms.g.closedCh:
			// gateway 关闭后不再处理新任务；队列残留 entry 全部置为
			// failed(closed) 封存并递减计数，避免 close 超时收尾后
			// EntriesUnsealed 残留。
			for {
				select {
				case entry := <-ms.queue:
					ms.mu.Lock()
					ms.pending--
					ms.g.mu.Lock()
					ms.g.stats.mirrorPending--
					ms.g.mu.Unlock()
					ms.mu.Unlock()
					entry.sinkInvoked = true
					ms.applyMirrorOutcome(entry, MirrorSinkResult{
						Status:     MirrorFailed,
						ErrorClass: DeliveryErrorClosed,
						Err:        NewClassifiedError(DeliveryErrorClosed, "gateway closed"),
					})
					ms.sealEntry(entry)
				default:
					return
				}
			}
		}
	}
}

// applyMirrorOutcome 把 sink result 归一化到 entry（含 late/超时处理）。
func (ms *mirrorSlot) applyMirrorOutcome(entry *mirrorEntry, res MirrorSinkResult) {
	entry.status = res.Status
	entry.errClass = res.ErrorClass
	if res.Err != nil {
		entry.errMsg = res.Err.Error()
	}
	entry.skipReason = res.SkipReason
	if res.Target != nil {
		t := *res.Target
		entry.target = &t
	}
	// avatar：mirror receipt 必须携带 primary outcome 决定（6.5）。
	ts := entry.target
	g := ms.g
	now := g.clock.Now()
	if ts == nil {
		var status DeliveryStatus
		var certainty WriteCertainty
		switch res.Status {
		case MirrorApplied:
			status, certainty = DeliveryCommitted, WriteCertaintyFull
		case MirrorSkipped:
			status, certainty = DeliveryDeferred, WriteCertaintyZero
		case MirrorFailed:
			status, certainty = DeliveryUnknownPartial, WriteCertaintyUnknown
		}
		ts = &TargetReceipt{
			SessionID:          entry.envelope.SessionID,
			Sequence:           entry.envelope.Sequence,
			BatchID:            entry.envelope.BatchID,
			RouteEpoch:         entry.envelope.RouteEpoch,
			SinkID:             ms.desc.SinkID,
			TargetClass:        ms.desc.Class,
			ProjectionTargetID: ms.desc.ProjectionTargetID,
			InvocationID:       0,
			SinkDeliveryResult: SinkDeliveryResult{
				Status:         status,
				Certainty:      certainty,
				ErrorClass:     entry.errClass,
				AttemptedBytes: res.AttemptedBytes,
				AcceptedBytes:  res.AcceptedBytes,
				Err:            res.Err,
			},
			CallbackReturned: entry.callbackDone,
			StartedAt:        now,
			FinishedAt:       now,
			OutcomeFixedAt:   now,
		}
		entry.target = ts
	}
}

// sealEntry 封存 entry（不可变）；sealed 后 late return 只进诊断。
func (ms *mirrorSlot) sealEntry(entry *mirrorEntry) {
	if entry.sealed {
		return
	}
	entry.sealed = true
	entry.sealedAt = ms.g.clock.Now()
	if entry.status == "" {
		entry.status = MirrorFailed
		if entry.errClass == DeliveryErrorNone {
			entry.errClass = DeliveryErrorSink
		}
	}
	mr := MirrorReceipt{
		EntryID:                 entry.envelope.EntryID,
		MirrorIndex:             entry.envelope.MirrorIndex,
		SinkID:                  entry.envelope.SinkID,
		TargetClass:             entry.envelope.TargetClass,
		ProjectionTargetID:      entry.envelope.MirrorEntryRef.ProjectionTargetID,
		ObservedPrimaryTargetID: entry.primary.ProjectionTargetID,
		Policy:                  entry.admission.Policy,
		RequestedApplyMode:      entry.admission.RequestedApplyMode,
		EffectiveApplyMode:      entry.admission.EffectiveApplyMode,
		NonAuthoritative:        entry.admission.NonAuthoritative,
		Scheduled:               entry.admission.Scheduled,
		SinkInvoked:             entry.sinkInvoked,
		TargetInvoked:           entry.target != nil,
		CallbackReturned:        entry.callbackDone,
		Status:                  entry.status,
		Target:                  entry.target,
		ErrorClass:              entry.errClass,
		SkipReason:              entry.skipReason,
		SealedAt:                entry.sealedAt,
	}
	if entry.errMsg != "" {
		mr.Err = NewClassifiedError(entry.errClass, entry.errMsg)
	}
	ms.g.recordMirrorOutcome(entry.envelope.Sequence, mr)
	ms.mu.Lock()
	switch entry.status {
	case MirrorApplied:
		ms.applied++
	case MirrorSkipped:
		ms.skipped++
	case MirrorFailed:
		ms.failed++
	}
	ms.mu.Unlock()
	ms.g.mu.Lock()
	ms.g.stats.mirrorSealed++
	ms.g.mu.Unlock()
	ms.g.publish(OutputEvent{
		SchemaVersion: SchemaVersion,
		Kind:          EventMirrorLifecycle,
		At:            entry.sealedAt,
		SessionID:     entry.envelope.SessionID,
		Sequence:      entry.envelope.Sequence,
		RouteEpoch:    entry.envelope.RouteEpoch,
		BatchID:       entry.envelope.BatchID,
		MirrorEntryID: entry.envelope.EntryID,
		MirrorIndex:   ms.index,
		MirrorPhase:   MirrorPhaseEntrySealed,
		Policy:        ms.cfg.Policy,
		MirrorStatus:  entry.status,
		ErrorClass:    entry.errClass,
		SkipReason:    entry.skipReason,
	})
	// batch record 封存时机：primary 已固定 + 所有配置 mirror 终态后。
	ms.g.maybeSealBatchRecord(entry.envelope.Sequence)
}

// maybeSealBatchRecord 在 primary outcome 固定且所有 mirror entries seal 后
// 封存整笔 record。具体的 slot 聚合由 gateway 负责。
func (g *RenderOutputGateway) maybeSealBatchRecord(seq uint64) {
	g.trySealRecord(seq)
}

// waitSealed 等待该 mirror 全部已登记 entry 终态，直到 ctx 超时或 deadline。
func (ms *mirrorSlot) waitSealed(ctx context.Context, _ time.Time) error {
	for {
		ms.mu.Lock()
		pending := ms.pending + ms.inFlight
		if pending == 0 {
			ms.mu.Unlock()
			return nil
		}
		ms.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// MirrorSnapshot 返回该 mirror 的可观察快照。
func (ms *mirrorSlot) MirrorSnapshot() MirrorRouteSnapshot {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	return MirrorRouteSnapshot{
		RouteEpoch:         ms.g.routeEpochSnapshot(),
		MirrorIndex:        ms.index,
		Sink:               ms.cfg.Sink.Snapshot(),
		Policy:             ms.cfg.Policy,
		RequestedApplyMode: ms.cfg.ApplyMode,
		ScheduleInFlight:   ms.pending,
		Pending:            ms.pending,
		InFlight:           ms.inFlight,
		EntriesUnsealed:    ms.pending + ms.inFlight,
		Scheduled:          ms.scheduled,
		Applied:            ms.applied,
		Skipped:            ms.skipped,
		Failed:             ms.failed,
		TimedOut:           ms.timedOut,
		LateCompleted:      ms.late,
		ScheduleDrops:      ms.drops,
	}
}
