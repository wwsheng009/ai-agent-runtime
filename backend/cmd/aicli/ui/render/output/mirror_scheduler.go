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
	sealReason   string // "timeout"/"callback"/"drain"
	callbackDone bool
	sinkInvoked  bool
	// timedOutByWatchdog 标记 entry 已被看门狗按时限封存（callback 仍在跑）。
	timedOutByWatchdog bool
	// abandonedByRetire 标记 slot 在 callback 返回前被 lifecycle finalizer
	// 接管；该 callback 的稍后返回只能作为 late diagnostic。
	abandonedByRetire bool
	// wdDone 由 worker/drain 在移除 watchdog 时关闭，令 watchdogWait 退出。
	wdOnce sync.Once
	wdDone chan struct{}
}

// mirrorSlot 管理一个 mirror 的 bounded 队列与 entry seal。
type mirrorSlot struct {
	g           *RenderOutputGateway
	index       int
	cfg         RenderMirror
	desc        TargetDescriptor
	queue       chan *mirrorEntry
	routeEpoch  uint64
	mu          sync.Mutex
	startOnce   sync.Once
	retireOnce  sync.Once
	retireCh    chan struct{}
	retired     bool
	retireClass DeliveryErrorClass
	started     bool
	pending     int
	inFlight    int
	scheduled   uint64
	applied     uint64
	skipped     uint64
	failed      uint64
	timedOut    uint64
	late        uint64
	drops       uint64
	// highWater 是 queue 同时 in-flight+pending 的最大值（overload health）。
	highWater int
	// watchdog 跟踪正在执行的 callback 的时限（key=entry）。
	watchdog map[*mirrorEntry]ClockTimer
	active   map[*mirrorEntry]struct{}
}

func newMirrorSlot(g *RenderOutputGateway, index int, cfg RenderMirror, routeEpoch ...uint64) *mirrorSlot {
	desc := cfg.Sink.Descriptor()
	cap := g.opts.MirrorQueueCapacity
	if cap <= 0 {
		cap = 1024
	}
	epoch := g.routeEpoch
	if len(routeEpoch) > 0 && routeEpoch[0] != 0 {
		epoch = routeEpoch[0]
	}
	return &mirrorSlot{
		g:          g,
		index:      index,
		cfg:        cfg,
		desc:       desc,
		queue:      make(chan *mirrorEntry, cap),
		routeEpoch: epoch,
		watchdog:   map[*mirrorEntry]ClockTimer{},
		retireCh:   make(chan struct{}),
		active:     map[*mirrorEntry]struct{}{},
	}
}

// start launches the single consumer for this slot.  Keeping the launch behind
// a once gate is important when Run is called concurrently with a lifecycle
// cleanup path: a mirror must never acquire two consumers for its serial
// callback boundary.
func (ms *mirrorSlot) start() {
	ms.startOnce.Do(func() {
		ms.mu.Lock()
		ms.started = true
		ms.mu.Unlock()
		go ms.workerLoop()
	})
}

// closed 返回 gateway 是否已关闭（供 drop 分类）。
func (ms *mirrorSlot) closed() bool {
	select {
	case <-ms.g.closedCh:
		return true
	default:
	}
	select {
	case <-ms.retireCh:
		return true
	default:
		return false
	}
}

func (ms *mirrorSlot) retire() {
	ms.retireWithError(DeliveryErrorClosed)
}

// abandonQueued stops this slot and seals entries that were accepted by the
// mirror queue but never reached its callback.  Close may retire a slot before
// closedCh is published; doing this synchronously is therefore necessary when
// Run was never called or when the worker selected retireCh before queue drain.
func (ms *mirrorSlot) abandonQueued() {
	ms.retireWithError(DeliveryErrorAbandoned)
}

func (ms *mirrorSlot) retireWithError(class DeliveryErrorClass) {
	var queued []*mirrorEntry
	ms.retireOnce.Do(func() {
		ms.mu.Lock()
		ms.retired = true
		ms.retireClass = class
		close(ms.retireCh)
		for {
			select {
			case entry := <-ms.queue:
				ms.pending--
				ms.g.mu.Lock()
				ms.g.stats.mirrorPending--
				ms.g.mu.Unlock()
				queued = append(queued, entry)
			default:
				for entry := range ms.active {
					if entry.sealed {
						continue
					}
					entry.abandonedByRetire = true
					entry.timedOutByWatchdog = true
					if wd, ok := ms.watchdog[entry]; ok {
						wd.Stop()
						delete(ms.watchdog, entry)
						entry.wdOnce.Do(func() { close(entry.wdDone) })
					}
					entry.status = MirrorFailed
					entry.errClass = class
					entry.errMsg = string(class)
					ms.sealEntryLocked(entry, "drain")
				}
				ms.mu.Unlock()
				return
			}
		}
	})
	for _, entry := range queued {
		ms.mu.Lock()
		entry.status = MirrorFailed
		entry.errClass = class
		entry.errMsg = string(class)
		entry.sinkInvoked = false
		entry.callbackDone = false
		ms.sealEntryLocked(entry, "drain")
		ms.mu.Unlock()
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
			Policy:             ad.Policy,
			RequestedApplyMode: ad.RequestedApplyMode,
			EffectiveApplyMode: ad.EffectiveApplyMode,
			NonAuthoritative:   ad.NonAuthoritative,
			Timeout:            ms.cfg.Timeout,
		},
		primary:   primary,
		admission: ad,
		wdDone:    make(chan struct{}),
	}
	ms.mu.Lock()
	defer ms.mu.Unlock()
	select {
	case <-ms.g.closedCh:
		return false
	default:
	}
	select {
	case <-ms.retireCh:
		return false
	default:
	}
	// 先登记后发送；失败回滚登记。
	ms.pending++
	ms.scheduled++
	if cur := ms.pending + ms.inFlight; cur > ms.highWater {
		ms.highWater = cur
	}
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
			if ms.retired {
				ms.pending--
				ms.g.mu.Lock()
				ms.g.stats.mirrorPending--
				ms.g.mu.Unlock()
				entry.status = MirrorFailed
				entry.errClass = ms.retireClass
				entry.errMsg = string(ms.retireClass)
				entry.sinkInvoked = false
				entry.callbackDone = false
				ms.sealEntryLocked(entry, "drain")
				ms.mu.Unlock()
				continue
			}
			ms.pending--
			ms.inFlight++
			ms.active[entry] = struct{}{}
			entry.sinkInvoked = true
			ms.g.mu.Lock()
			ms.g.stats.mirrorPending--
			ms.g.stats.mirrorInFlight++
			ms.g.mu.Unlock()
			ms.mu.Unlock()

			// A lifecycle finalizer may have retired the slot after the
			// dequeue but before callback admission.  Convert that ticket to
			// a synthetic terminal entry rather than invoking the sink.
			ms.mu.Lock()
			if ms.retired {
				entry.abandonedByRetire = true
				entry.timedOutByWatchdog = true
				entry.status = MirrorFailed
				entry.errClass = ms.retireClass
				entry.errMsg = string(ms.retireClass)
				ms.sealEntryLocked(entry, "drain")
				delete(ms.active, entry)
				ms.inFlight--
				ms.g.mu.Lock()
				ms.g.stats.mirrorInFlight--
				ms.g.mu.Unlock()
				ms.mu.Unlock()
				continue
			}
			ms.mu.Unlock()

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

			// 看门狗：callback 超过 Timeout 未返回时，entry 按时限封存为
			// timeout/failed（不可变）；callback 稍后返回只进 late 诊断。
			watchdog := ms.g.clock.NewTimer(ms.cfg.Timeout)
			ms.mu.Lock()
			ms.watchdog[entry] = watchdog
			ms.mu.Unlock()
			go ms.watchdogWait(entry, watchdog)

			ctx, cancel := contextWithClockTimeout(ms.g.clock, context.Background(), ms.cfg.Timeout)
			res := mirrorSubmitWithPanicGuard(ms.cfg.Sink.SubmitMirror, ctx, entry.envelope)
			cancel()

			ms.mu.Lock()
			entry.callbackDone = true
			if wd, ok := ms.watchdog[entry]; ok {
				wd.Stop()
				delete(ms.watchdog, entry)
				entry.wdOnce.Do(func() { close(entry.wdDone) })
			}
			if entry.timedOutByWatchdog {
				// entry 已被看门狗按时限封存（status=timeout/failed）；
				// worker 不再写 status，只记 late 诊断。
				ms.mu.Unlock()
				ms.lateComplete(entry, res)
			} else {
				// 正常路径：同一临界区内确定 outcome 并封存，保证与
				// watchdog 的 timeout seal 互斥。
				ms.applyMirrorOutcomeLocked(entry, res)
				ms.sealEntryLocked(entry, "callback")
				ms.mu.Unlock()
			}

			ms.mu.Lock()
			ms.inFlight--
			delete(ms.active, entry)
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
					entry.sinkInvoked = true
					if wd, ok := ms.watchdog[entry]; ok {
						wd.Stop()
						delete(ms.watchdog, entry)
						entry.wdOnce.Do(func() { close(entry.wdDone) })
					}
					ms.applyMirrorOutcomeLocked(entry, MirrorSinkResult{
						Status:     MirrorFailed,
						ErrorClass: DeliveryErrorClosed,
						Err:        NewClassifiedError(DeliveryErrorClosed, "gateway closed"),
					})
					ms.sealEntryLocked(entry, "drain")
					delete(ms.active, entry)
					ms.mu.Unlock()
				default:
					return
				}
			}
		case <-ms.retireCh:
			// Reconfigure retires a slot only after waitSealed has observed no
			// pending/in-flight entry, so there is nothing left to finalize.
			return
		}
	}
}

// applyMirrorOutcomeLocked 把 sink result 归一化到 entry；调用者必须已持
// ms.mu（worker 正常路径在临界区内调用，保证与 watchdog timeout seal 互斥）。
func (ms *mirrorSlot) applyMirrorOutcomeLocked(entry *mirrorEntry, res MirrorSinkResult) {
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

// sealEntry 封存 entry（不可变；加锁入口）。sealed 后 late return 只进诊断。
func (ms *mirrorSlot) sealEntry(entry *mirrorEntry, reason string) {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	ms.sealEntryLocked(entry, reason)
}

// sealEntryLocked 封存 entry；调用者必须已持 ms.mu。reason ∈
// "callback"/"timeout"/"drain"。timeout 封存使用
// DeliveryErrorTimeout + MirrorFailed，callback 仍返回后由 lateComplete
// 只进诊断（不改写 entry）。
func (ms *mirrorSlot) sealEntryLocked(entry *mirrorEntry, reason string) {
	if entry.sealed {
		return
	}
	entry.sealed = true
	entry.sealedAt = ms.g.clock.Now()
	entry.sealReason = reason
	if entry.status == "" {
		entry.status = MirrorFailed
		if entry.errClass == DeliveryErrorNone {
			entry.errClass = DeliveryErrorSink
		}
	}
	if reason == "timeout" {
		entry.status = MirrorFailed
		entry.errClass = DeliveryErrorTimeout
		entry.errMsg = "mirror callback exceeded timeout"
	}
	if reason == "drain" && entry.errClass == DeliveryErrorNone {
		entry.errClass = DeliveryErrorClosed
	}
	if reason == "timeout" {
		ms.timedOut++
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
	switch entry.status {
	case MirrorApplied:
		ms.applied++
	case MirrorSkipped:
		ms.skipped++
	case MirrorFailed:
		ms.failed++
	}
	ms.g.mu.Lock()
	ms.g.stats.mirrorSealed++
	ms.g.mu.Unlock()
	// 注意：ms.g.publish 在 ms.mu 持锁下调用（eventHub 自锁，无环；
	// mirror worker 不与 gateway Close 双向等待）。
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

// watchdogWait 等待 callback 时限；到期时把 entry 按时限封存（不可变）。
// callback 已返回（timer 被 worker 停止）时不做事。timeout 封存后，
// callback 稍后返回走 lateComplete 只进诊断。
func (ms *mirrorSlot) watchdogWait(entry *mirrorEntry, timer ClockTimer) {
	select {
	case <-entry.wdDone:
		// worker/drain 已移除 watchdog：无事可做，直接退出（不泄漏）。
		return
	case <-timer.C():
		ms.mu.Lock()
		if _, registered := ms.watchdog[entry]; !registered {
			// callback 已返回并移除 watchdog；timer 迟到。
			ms.mu.Unlock()
			return
		}
		// entry 仍注册：在锁内置标志并按 timeout 封存（与 worker 的
		// callback path 互斥）。
		entry.timedOutByWatchdog = true
		ms.sealEntryLocked(entry, "timeout")
		ms.mu.Unlock()
	case <-ms.g.closedCh:
	}
}

// lateComplete 处理 timeout 之后才返回的 callback：只进诊断与 late 计数，
// 绝不改写已封存 entry/record。
func (ms *mirrorSlot) lateComplete(entry *mirrorEntry, res MirrorSinkResult) {
	ms.mu.Lock()
	ms.late++
	ms.mu.Unlock()
	ms.g.publish(OutputEvent{
		SchemaVersion:    SchemaVersion,
		Kind:             EventMirrorLifecycle,
		At:               ms.g.clock.Now(),
		SessionID:        entry.envelope.SessionID,
		Sequence:         entry.envelope.Sequence,
		RouteEpoch:       entry.envelope.RouteEpoch,
		BatchID:          entry.envelope.BatchID,
		MirrorEntryID:    entry.envelope.EntryID,
		MirrorIndex:      ms.index,
		MirrorPhase:      MirrorPhaseLateCompletion,
		Policy:           ms.cfg.Policy,
		MirrorStatus:     entry.status, // 已封存的 timeout/failed
		ErrorClass:       entry.errClass,
		SinkInvoked:      true,
		CallbackReturned: true,
	})
}

// MirrorSnapshot 返回该 mirror 的可观察快照。
func (ms *mirrorSlot) MirrorSnapshot() MirrorRouteSnapshot {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	return MirrorRouteSnapshot{
		RouteEpoch:         ms.routeEpoch,
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
		QueueHighWater:     ms.highWater,
	}
}
