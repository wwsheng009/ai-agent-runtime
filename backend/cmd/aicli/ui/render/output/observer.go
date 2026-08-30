package output

import (
	"sync"
	"time"
)

// ============================================================================
// Observer / event contract（10.1/10.2）
// ============================================================================

// OutputEventKind 是观察事件的闭集。
type OutputEventKind string

const (
	EventRouteChanged          OutputEventKind = "route_changed"
	EventGatewayStateChanged   OutputEventKind = "gateway_state_changed"
	EventAdmissionDeferred     OutputEventKind = "admission_deferred"
	EventAdmissionRejected     OutputEventKind = "admission_rejected"
	EventBatchPrepared         OutputEventKind = "batch_prepared"
	EventPrimaryStarted        OutputEventKind = "primary_started"
	EventPrimaryCompleted      OutputEventKind = "primary_completed"
	EventPrimaryLateCompletion OutputEventKind = "primary_late_completion"
	EventMirrorLifecycle       OutputEventKind = "mirror_lifecycle"
	EventReceiptCutoff         OutputEventKind = "receipt_cutoff"
	EventBatchCompleted        OutputEventKind = "batch_completed"
	EventProjectionReported    OutputEventKind = "projection_reported"
	EventSinkFaulted           OutputEventKind = "sink_faulted"
	EventCaptureDropped        OutputEventKind = "capture_dropped"
	EventGatewayClosed         OutputEventKind = "gateway_closed"
)

// MirrorEventPhase 只描述 gateway 已观察到的 callback/entry 生命周期，不是
// delivery outcome。
type MirrorEventPhase string

const (
	MirrorPhaseScheduled       MirrorEventPhase = "scheduled"
	MirrorPhaseCallbackStarted MirrorEventPhase = "callback_started"
	MirrorPhaseEntrySealed     MirrorEventPhase = "entry_sealed"
	MirrorPhaseLateCompletion  MirrorEventPhase = "late_completion"
)

// LateCompletionDiagnostic 只描述 authoritative outcome 已固定后才到达的
// callback return；它不能改写 receipt/record。不包含 error interface 或
// 原始 payload。
type LateCompletionDiagnostic struct {
	InvocationID     uint64
	CallbackReturned bool
	TargetInvoked    bool
	Status           DeliveryStatus
	MirrorStatus     MirrorDeliveryStatus
	Certainty        WriteCertainty
	ErrorClass       DeliveryErrorClass
	AttemptedBytes   int
	AcceptedBytes    int
	SafeMessage      string
}

// OutputEvent 是最小可观测事件。字段有效性规则见设计文档 10.1 的字段表。
type OutputEvent struct {
	SchemaVersion              uint32
	EventSequence              uint64
	Kind                       OutputEventKind
	At                         time.Time
	GatewayState               GatewayLifecycleState
	PreviousGatewayState       GatewayLifecycleState
	SessionID                  string
	BindingGeneration          uint64
	RouteEpoch                 uint64
	PreviousRouteEpoch         uint64
	Sequence                   uint64
	BatchID                    string
	RecordID                   string
	MirrorEntryID              string
	MirrorIndex                int
	IntentID                   string
	Transaction                TransactionKind
	Source                     string
	Cause                      string
	Terminal                   RenderTerminalContext
	History                    *HistoryDeliveryDomain
	Admission                  AdmissionReceipt
	TargetInvoked              bool
	CallbackReturned           bool
	SinkInvoked                bool
	InvocationID               uint64
	Synthetic                  bool
	Status                     DeliveryStatus
	MirrorStatus               MirrorDeliveryStatus
	Certainty                  WriteCertainty
	ErrorClass                 DeliveryErrorClass
	SafeMessage                string
	SkipReason                 MirrorSkipReason
	MirrorPhase                MirrorEventPhase
	Policy                     MirrorPolicy
	MirrorPolicy               MirrorPolicy
	RequestedApplyMode         MirrorApplyMode
	EffectiveApplyMode         MirrorApplyMode
	NonAuthoritative           bool
	Scheduled                  bool
	TargetClass                TargetClass
	ObservedPrimaryTargetID    string
	PreviousProjectionTargetID string
	AttemptedBytes             int
	AcceptedBytes              int
	MirrorsScheduled           int
	MirrorScheduleDrops        int
	ReceiptCutoffEventSequence uint64
	ReceiptObserverDrops       uint64
	ObserverDrops              uint64
	JournalDropCount           uint64
	SubscriberDropCount        uint64
	ProjectionValidity         ProjectionValidity
	ProjectionFrame            uint64
	Locked                     bool
	Late                       *LateCompletionDiagnostic
	ProjectionTargetID         string
	SinkID                     string
	SummaryHash                string
}

// EventPublishResult 是 sequencer publish transaction 的内部结果，不是公开
// receipt；仅供 gateway 把 drop delta 归因到正确的 submit 和 stats ledger。
type EventPublishResult struct {
	EventSequence       uint64
	SubscriberDropCount uint64
	JournalDropCount    uint64
}

// Subscription 是事件订阅；事件经过 bounded mailbox 异步派发。
type Subscription interface {
	Events() <-chan OutputEvent
	Close() error
}

// MirrorRouteSnapshot 是 route 中一个 mirror 的可观测 snapshot（10.2）。
type MirrorRouteSnapshot struct {
	RouteEpoch         uint64
	MirrorIndex        int
	Sink               SinkSnapshot
	Policy             MirrorPolicy
	RequestedApplyMode MirrorApplyMode
	RegisteredEntries  uint64 // 本 route 已登记、尚未从 snapshot 移除的 slots
	ScheduleInFlight   int    // 已登记但 enqueue/drop 判定尚未固定的 slots
	Pending            int    // 本 RouteEpoch 已入队但 callback 尚未开始
	InFlight           int    // 本 RouteEpoch callback 已开始但 entry 尚未 seal
	EntriesUnsealed    int    // Pending + InFlight；只统计本 RouteEpoch 的 live entry
	Scheduled          uint64
	Applied            uint64
	Skipped            uint64
	Failed             uint64
	TimedOut           uint64
	LateCompleted      uint64
	ScheduleDrops      uint64
	Quarantined        bool
	QuarantineReason   DeliveryErrorClass
	Abandoned          uint64
	EntrySealCount     uint64
}

// RenderOutputSnapshot 是 gateway 的完整观测快照（10.2）。SchemaVersion 第一
// 版为 1。Snapshot() 必须返回 detached value。
type RenderOutputSnapshot struct {
	SchemaVersion           uint32
	StatsEpoch              uint64
	SessionID               string
	BindingGeneration       uint64
	State                   GatewayLifecycleState
	RouteEpoch              uint64
	CloseCutoffSequence     uint64 // Closing 后固定；Open/Reconfiguring 为零
	Primary                 SinkSnapshot
	Mirrors                 []MirrorRouteSnapshot
	PrimaryInFlight         int
	MirrorScheduleInFlight  uint64
	MirrorPending           int
	MirrorInFlight          int
	MirrorEntriesUnsealed   int
	DeliveryRecordsUnsealed int // admitted 但尚未 seal；包括 primary in-flight 或 unsealed mirror
	LastSequence            uint64
	LastBatchID             string
	LastRecordID            string
	LastEventSequence       uint64
	LastTerminal            RenderTerminalContext
	LastHistory             *HistoryDeliveryDomain
	AdmissionAccepted       uint64
	AdmissionDeferred       uint64
	AdmissionRejected       uint64
	PrimaryCommitted        uint64
	PrimaryZeroFailed       uint64
	PrimaryUnknown          uint64
	PrimaryDeferred         uint64
	PrimaryRejected         uint64
	MirrorEntriesRegistered uint64 // session-level 累计登记的 configured slots
	MirrorsScheduled        uint64
	MirrorScheduleDrops     uint64
	MirrorEntrySealCount    uint64
	MirrorsApplied          uint64
	MirrorsSkipped          uint64
	MirrorsFailed           uint64
	MirrorsTimedOut         uint64
	MirrorsLate             uint64
	MirrorQuarantines       uint64
	MirrorAbandons          uint64
	ObserverDrops           uint64
	EventJournalDrops       uint64
	DeliveryJournalDrops    uint64
	DeliveryRecordsSealed   uint64
	LastPrimaryDuration     time.Duration
	Abandoned               uint64
	EntrySealCount          uint64
}

// ============================================================================
// Event sequencer / hub
// ============================================================================

// eventHub 是有界 event journal + bounded subscription mailbox 的实现。
// 同一把 mutex 覆盖：journal 追加/淘汰、订阅 mailbox reserve/commit、
// ActivityGate publish/freeze。publisher 不直接向订阅者 channel 阻塞发送。
type eventHub struct {
	mu           sync.Mutex
	limit        JournalLimit
	maxSubs      int
	maxBuf       int
	closed       bool
	seq          uint64
	journal      []OutputEvent
	journalBytes int
	subs         map[uint64]*subscription
	subSeq       uint64
	drops        uint64 // SubscriberDropCount 累计（observer_drops）
	journalDrops uint64 // journal 淘汰丢弃计数
}

// subscription 是 bounded mailbox + dispatcher：hub 只向 mailbox 非阻塞
// reserve/commit，dispatcher 再把已提交事件交给公开 Events() channel。
type subscription struct {
	hub     *eventHub
	id      uint64
	mailbox chan OutputEvent // bounded，hub 非阻塞写入
	ch      chan OutputEvent // 公开 channel，dispatcher 写入
	stop    chan struct{}
	done    chan struct{}
}

// newEventHub 创建有界 event hub。
func newEventHub(limit JournalLimit, maxSubs, maxBuf int) *eventHub {
	if maxSubs <= 0 {
		maxSubs = 16
	}
	if maxBuf <= 0 {
		maxBuf = 256
	}
	return &eventHub{
		limit:   limit,
		maxSubs: maxSubs,
		maxBuf:  maxBuf,
		subs:    make(map[uint64]*subscription),
	}
}

// Subscribe 创建具有 caller-requested 容量的订阅（bounded mailbox）。
// 容量必须显式为正，且不得超过 gateway freeze 时配置的上限。
func (h *eventHub) Subscribe(buffer int) (Subscription, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, NewClassifiedError(DeliveryErrorClosed, "event hub is closed")
	}
	if buffer <= 0 || buffer > h.maxBuf {
		return nil, NewClassifiedError(DeliveryErrorInvalid, "invalid subscription buffer")
	}
	if len(h.subs) >= h.maxSubs {
		return nil, NewClassifiedError(DeliveryErrorQueueFull, "max subscriptions reached")
	}
	h.subSeq++
	s := &subscription{
		hub:     h,
		id:      h.subSeq,
		mailbox: make(chan OutputEvent, buffer),
		ch:      make(chan OutputEvent, buffer),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
	h.subs[s.id] = s
	go s.dispatch()
	return s, nil
}

func (s *subscription) Events() <-chan OutputEvent { return s.ch }

// Close 幂等；丢弃的 mailbox 余量属于 unsubscribe，不计作 ObserverDrops。
func (s *subscription) Close() error {
	if s == nil || s.hub == nil {
		return nil
	}
	s.hub.mu.Lock()
	select {
	case <-s.stop:
		s.hub.mu.Unlock()
		<-s.done
		return nil
	default:
	}
	delete(s.hub.subs, s.id)
	close(s.stop)
	s.hub.mu.Unlock()
	<-s.done
	return nil
}

// dispatch 把已提交 mailbox 事件送到公开 channel；stop 后恰好关闭一次
// 公开 channel 与 done，不会产生 send-on-closed panic。
func (s *subscription) dispatch() {
	defer close(s.ch)
	defer close(s.done)
	for {
		select {
		case ev := <-s.mailbox:
			select {
			case s.ch <- ev:
			case <-s.stop:
				return
			}
		case <-s.stop:
			return
		}
	}
}

// submit 分配 EventSequence、追加 journal（可淘汰）、为订阅者 reserve/commit
// mailbox slot，并统计 drop。自带 hub mutex；可在 gateway 锁外调用
// （mirror worker 直接 publish）。
func (h *eventHub) submit(ev OutputEvent) EventPublishResult {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seq++
	ev.EventSequence = h.seq
	res := EventPublishResult{EventSequence: h.seq}
	h.appendJournalLocked(ev)
	// 订阅者 mailbox：向每个存活的 subscription 非阻塞 reserve/commit。
	// 满则计为 drop（ObserverDrops），不阻塞 publisher。
	for _, s := range h.subs {
		select {
		case s.mailbox <- cloneOutputEvent(ev):
		default:
			h.drops++
		}
	}
	res.SubscriberDropCount = h.drops
	res.JournalDropCount = h.journalDrops
	return res
}

// submitCutoff performs the receipt-cutoff reservation and commit under one
// hub lock.  Unlike publishing a placeholder and rewriting only the journal,
// this makes the exact same finalized marker visible to the journal and every
// subscriber that wins a bounded mailbox reservation.
func (h *eventHub) submitCutoff(ev OutputEvent, startDrops uint64) EventPublishResult {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.seq++
	ev.EventSequence = h.seq
	ev.ReceiptCutoffEventSequence = h.seq

	deliver := make([]*subscription, 0, len(h.subs))
	for _, s := range h.subs {
		// The dispatcher is the only receiver and all publishers hold h.mu.
		// A non-full mailbox observed here therefore remains sendable below;
		// a full mailbox is deterministically counted as this marker's drop.
		if len(s.mailbox) >= cap(s.mailbox) {
			h.drops++
			continue
		}
		deliver = append(deliver, s)
	}
	if h.drops >= startDrops {
		ev.ReceiptObserverDrops = h.drops - startDrops
	}
	h.appendJournalLocked(ev)
	for _, s := range deliver {
		s.mailbox <- cloneOutputEvent(ev)
	}
	return EventPublishResult{
		EventSequence:       h.seq,
		SubscriberDropCount: h.drops,
		JournalDropCount:    h.journalDrops,
	}
}

func (h *eventHub) appendJournalLocked(ev OutputEvent) {
	stored := cloneOutputEvent(ev)
	h.journal = append(h.journal, stored)
	h.journalBytes += approxEventBytes(stored)
	for len(h.journal) > h.limit.MaxItems || h.journalBytes > h.limit.MaxBytes {
		drop := h.journal[0]
		h.journal = h.journal[1:]
		h.journalBytes -= approxEventBytes(drop)
		h.journalDrops++
	}
}

func cloneOutputEvent(ev OutputEvent) OutputEvent {
	out := ev
	out.History = cloneHistory(ev.History)
	if ev.Late != nil {
		late := *ev.Late
		out.Late = &late
	}
	return out
}

// snapshotEvents 返回最近 limit 个事件（detached）。
func (h *eventHub) snapshotEvents(limit int) []OutputEvent {
	h.mu.Lock()
	defer h.mu.Unlock()
	if limit <= 0 || limit > len(h.journal) {
		limit = len(h.journal)
	}
	out := make([]OutputEvent, 0, limit)
	for _, ev := range h.journal[len(h.journal)-limit:] {
		out = append(out, cloneOutputEvent(ev))
	}
	return out
}

// shutdown 停止所有订阅者并移除。在 gateway Close 收尾时调用；此后事件
// 仍可安全 submit（hub 自锁；journal 继续追加到有界容量），但不再派发给
// 订阅者。Close 自身最后会 submit 一个 EventGatewayClosed。
func (h *eventHub) shutdown() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	for _, s := range h.subs {
		delete(h.subs, s.id)
		close(s.stop)
	}
}

func approxEventBytes(ev OutputEvent) int {
	return 128 + len(ev.BatchID) + len(ev.RecordID) + len(ev.MirrorEntryID) +
		len(ev.Source) + len(ev.Cause) + len(ev.SafeMessage) +
		len(ev.SessionID) + len(ev.SinkID) + len(ev.ProjectionTargetID)
}

// ============================================================================
// ReceiptPublicationGate（10.1/10.2）
// ============================================================================

// gateState 是 gate 的 tombstone 状态。
type gateState int

const (
	gateOpen gateState = iota
	gateFrozen
	gateRetired
)

// ReceiptPublicationGate 是 per-submit 的受限发布门：open -> frozen ->
// retired。gateway 通过它把 admission/primary/schedule 事件按 cutoff 线性化。
type ReceiptPublicationGate struct {
	mu          sync.Mutex
	state       gateState
	batch       BatchIdentity
	cutoff      uint64 // EventReceiptCutoff 的 EventSequence
	startDrops  uint64
	cutoffDrops uint64
	hub         *eventHub
}

// BatchIdentity 是 gate 保留的最小 batch/entry/invocation 身份。
type BatchIdentity struct {
	SessionID     string
	Sequence      uint64
	BatchID       string
	RouteEpoch    uint64
	MirrorEntryID string
	InvocationID  uint64
}

// newReceiptPublicationGate 登记 gate（不带 hub 时用于被拒绝路径）。
func newReceiptPublicationGate(id BatchIdentity, hub *eventHub) *ReceiptPublicationGate {
	g := &ReceiptPublicationGate{batch: id, hub: hub, state: gateOpen}
	if hub != nil {
		hub.mu.Lock()
		g.startDrops = hub.drops
		hub.mu.Unlock()
	}
	return g
}

// Publish 在 cutoff 前发布事件；若 gate 已 frozen 或 retired，追加到 session
// 累计（由调用方通过 delta 处理），不返回错误。
func (g *ReceiptPublicationGate) Publish(ev OutputEvent) EventPublishResult {
	g.mu.Lock()
	if g.state != gateOpen || g.hub == nil {
		g.mu.Unlock()
		return EventPublishResult{}
	}
	ev.BatchID = g.batch.BatchID
	ev.SessionID = g.batch.SessionID
	ev.RouteEpoch = g.batch.RouteEpoch
	res := g.hub.submit(ev)
	g.mu.Unlock()
	return res
}

// Freeze 提交 cutoff marker：先为 marker 分配 EventSequence、更新 drop 统计，
// 再把最终的 ReceiptObserverDrops/EventSequence 写回 marker 并提交。返回
// marker 的 EventSequence。锁序：gate.mu -> hub.mu（与 Publish 一致）。
func (g *ReceiptPublicationGate) Freeze(kind OutputEventKind, at time.Time) EventPublishResult {
	g.mu.Lock()
	defer g.mu.Unlock()
	marker := OutputEvent{
		SchemaVersion: SchemaVersion,
		Kind:          kind,
		At:            at,
		SessionID:     g.batch.SessionID,
		Sequence:      g.batch.Sequence,
		BatchID:       g.batch.BatchID,
		RouteEpoch:    g.batch.RouteEpoch,
	}
	res := EventPublishResult{}
	if g.hub == nil || g.state != gateOpen {
		return res
	}
	res = g.hub.submitCutoff(marker, g.startDrops)
	g.cutoff = res.EventSequence
	if res.SubscriberDropCount >= g.startDrops {
		g.cutoffDrops = res.SubscriberDropCount - g.startDrops
	}
	g.state = gateFrozen
	return res
}

// Retire 在 record seal 后释放 gate。
func (g *ReceiptPublicationGate) Retire() {
	g.mu.Lock()
	g.state = gateRetired
	g.mu.Unlock()
}

// CutoffSequence 返回 frozen cutoff 的 EventSequence（未 frozen 返回 0）。
func (g *ReceiptPublicationGate) CutoffSequence() uint64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.state == gateFrozen {
		return g.cutoff
	}
	return 0
}

// CutoffDrops 返回 freeze 时累计的 observer drops（含 cutoff marker 自身）。
func (g *ReceiptPublicationGate) CutoffDrops() uint64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.hub == nil || g.state != gateFrozen {
		return 0
	}
	return g.cutoffDrops
}

// getJournalDrops 返回 journal 淘汰累计值。
func (h *eventHub) getJournalDrops() uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.journalDrops
}

// ProjectionValidity / TerminalCursor / CursorShape 完整定义在 sink.go
//（7.3 schema）；此处不再重复，避免两处漂移。
