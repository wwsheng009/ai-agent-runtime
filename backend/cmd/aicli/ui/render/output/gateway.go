package output

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"reflect"
	"sort"
	"sync"
	"time"
)

// ============================================================================
// Route / options（6.4/8.x）
// ============================================================================

// MirrorPolicy 决定 mirror 何时接收 batch（outcome-aware，见 6.5 计算表）。
type MirrorPolicy string

const (
	MirrorBestEffort    MirrorPolicy = "best_effort"
	MirrorCommittedOnly MirrorPolicy = "committed_only"
	MirrorAttempted     MirrorPolicy = "attempted"
)

// MirrorApplyMode 决定 mirror 实际应用的模式；metadata_only 不承载 bytes。
type MirrorApplyMode string

const (
	MirrorApplyMetadataOnly MirrorApplyMode = "metadata_only"
	MirrorApplyBytes        MirrorApplyMode = "bytes"
)

// SinkOwnership 描述 gateway 是否在 replace/close 时关闭 sink。
type SinkOwnership string

const (
	SinkOwned    SinkOwnership = "owned"    // gateway 在 replace/close 时关闭
	SinkBorrowed SinkOwnership = "borrowed" // owner 负责关闭，gateway 只停止调用
)

// RenderMirror 是 route 中的一条 mirror 配置。
type RenderMirror struct {
	Sink      RenderMirrorSink
	Policy    MirrorPolicy
	ApplyMode MirrorApplyMode
	Ownership SinkOwnership
	Timeout   time.Duration
}

// RenderRouteConfig 是 primary + mirrors 的路由配置；替换配置生成新 RouteEpoch。
type RenderRouteConfig struct {
	Primary            RenderOutputSink
	PrimaryOwnership   SinkOwnership
	ProjectionTargetID string // expected value；必须等于 Primary.Descriptor().ProjectionTargetID
	Mirrors            []RenderMirror
}

// RenderGatewayOptions 是 gateway 构造参数。
type RenderGatewayOptions struct {
	Clock                 Clock
	CloseTimeout          time.Duration
	ReconfigureTimeout    time.Duration
	MaxIntentBytes        int
	MirrorQueueCapacity   int
	DeliveryJournalLimit  JournalLimit
	EventJournalLimit     JournalLimit
	MaxSubscriptions      int
	MaxSubscriptionBuffer int
}

// normalizeRoute 校验并冻结 route 配置。Route 是 gateway 的 immutable
// admission 输入，因此 requested metadata-only 是唯一允许的隐式默认值；
// 其余缺失/非法字段一律 fail closed。
func normalizeRoute(route RenderRouteConfig) (RenderRouteConfig, error) {
	if route.Primary == nil {
		return RenderRouteConfig{}, NewClassifiedError(DeliveryErrorInvalid, "route primary must not be nil")
	}
	if !validOwnership(route.PrimaryOwnership) {
		return RenderRouteConfig{}, NewClassifiedError(DeliveryErrorInvalid, "invalid primary sink ownership")
	}
	desc := route.Primary.Descriptor()
	if err := validateDescriptor(desc, "primary"); err != nil {
		return RenderRouteConfig{}, err
	}
	if route.ProjectionTargetID == "" {
		return RenderRouteConfig{}, NewClassifiedError(DeliveryErrorInvalid, "projection target id must not be empty")
	}
	if desc.ProjectionTargetID != route.ProjectionTargetID {
		return RenderRouteConfig{}, NewClassifiedError(DeliveryErrorInvalid,
			"route projection target id mismatch: "+route.ProjectionTargetID+" != "+desc.ProjectionTargetID)
	}

	out := route
	out.Mirrors = append([]RenderMirror(nil), route.Mirrors...)
	mirrorIDs := map[string]bool{desc.SinkID: true}
	for i := range out.Mirrors {
		m := &out.Mirrors[i]
		if m.Sink == nil {
			return RenderRouteConfig{}, NewClassifiedError(DeliveryErrorInvalid,
				fmt.Sprintf("route mirror %d sink must not be nil", i))
		}
		d := m.Sink.Descriptor()
		if err := validateDescriptor(d, fmt.Sprintf("route mirror %d", i)); err != nil {
			return RenderRouteConfig{}, err
		}
		if d.Class == TargetClassPhysical {
			return RenderRouteConfig{}, NewClassifiedError(DeliveryErrorInvalid,
				"physical mirror is not allowed in v1")
		}
		if sameSink(route.Primary, m.Sink) || mirrorIDs[d.SinkID] {
			return RenderRouteConfig{}, NewClassifiedError(DeliveryErrorInvalid,
				"sink must not appear more than once in an active route: "+d.SinkID)
		}
		mirrorIDs[d.SinkID] = true
		switch m.Policy {
		case MirrorBestEffort, MirrorCommittedOnly, MirrorAttempted:
		default:
			return RenderRouteConfig{}, NewClassifiedError(DeliveryErrorInvalid,
				fmt.Sprintf("route mirror %d has invalid policy", i))
		}
		if m.ApplyMode == "" {
			m.ApplyMode = MirrorApplyMetadataOnly
		}
		if m.ApplyMode != MirrorApplyMetadataOnly && m.ApplyMode != MirrorApplyBytes {
			return RenderRouteConfig{}, NewClassifiedError(DeliveryErrorInvalid,
				fmt.Sprintf("route mirror %d has invalid apply mode", i))
		}
		if !validOwnership(m.Ownership) {
			return RenderRouteConfig{}, NewClassifiedError(DeliveryErrorInvalid,
				fmt.Sprintf("route mirror %d has invalid ownership", i))
		}
		if m.Timeout <= 0 {
			return RenderRouteConfig{}, NewClassifiedError(DeliveryErrorInvalid,
				fmt.Sprintf("route mirror %d timeout must be positive", i))
		}
	}
	return out, nil
}

func validateRoute(route RenderRouteConfig) error {
	_, err := normalizeRoute(route)
	return err
}

func validOwnership(o SinkOwnership) bool {
	return o == SinkOwned || o == SinkBorrowed
}

func validTargetClass(c TargetClass) bool {
	switch c {
	case TargetClassPhysical, TargetClassCapture, TargetClassVirtual, TargetClassDiscard:
		return true
	default:
		return false
	}
}

func validateDescriptor(desc TargetDescriptor, where string) error {
	if desc.SinkID == "" {
		return NewClassifiedError(DeliveryErrorInvalid, where+" sink id must not be empty")
	}
	if !validTargetClass(desc.Class) {
		return NewClassifiedError(DeliveryErrorInvalid, where+" has invalid target class")
	}
	if desc.ProjectionTargetID == "" {
		return NewClassifiedError(DeliveryErrorInvalid, where+" projection target id must not be empty")
	}
	return nil
}

func sameSink(a, b interface{}) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	ta, tb := reflect.TypeOf(a), reflect.TypeOf(b)
	if ta != tb {
		return false
	}
	if ta.Comparable() {
		return reflect.ValueOf(a).Interface() == reflect.ValueOf(b).Interface()
	}
	return false
}

// validateRoute 校验 route 配置约束（8.x 配置约束 1-5）：
//  1. Primary 非空（nil 时用 DiscardSink 显式声明）；
//  2. ProjectionTargetID 非空且 route 与 descriptor 相等；
//  3. Class==physical 是物理唯一判定；
//  4. 同一 sink 不得同时出现在 primary 和 mirrors；
//  5. 物理 mirror 第一版禁止。
// ============================================================================
// RenderOutputGateway
// ============================================================================

// RenderSubmitPort 是 session-bound 提交端口。
type RenderSubmitPort interface {
	Submit(ctx context.Context, intent RenderIntent) OutputReceipt
}

// RenderOutputPort 是完整 gateway 端口（含控制面）。
type RenderOutputPort interface {
	RenderSubmitPort
	Snapshot() RenderOutputSnapshot
	WaitIdle(ctx context.Context) error
	Drain(ctx context.Context) error
	Close(ctx context.Context) error
	BeginReconfigure(ctx context.Context, token string) error
	CommitReconfigure(ctx context.Context, token string) error
	AbortReconfigure(ctx context.Context, token string) error
}

// RenderOutputGateway 是性能边界：所有业务调用只访问公开字段与只读不可变
// 配置，writer/aborter 由 invocation runner 独占。
type RenderOutputGateway struct {
	opts   RenderGatewayOptions
	clock  Clock
	sessID string

	mu           sync.Mutex
	state        GatewayLifecycleState
	route        RenderRouteConfig
	routeEpoch   uint64
	sequence     uint64 // 最后盖章 sequence（admission 临界区内分配）
	targetDesc   TargetDescriptor
	lastTerminal RenderTerminalContext
	lastHistory  *HistoryDeliveryDomain
	stats        gatewayStats

	// primary serial boundary
	primary        RenderOutputSink
	primaryBusy    bool
	primaryWaiters int    // cond.Wait 中等待 serial 的 submit 数（Close 排空判定用）
	pendingStamps  uint64 // 已盖章、尚未进入 deliver serial 段的 batch 数
	primaryAborted bool   // deviate 后置位：等待中的 batch 快速收敛，不再执行 sink
	primaryCond    *sync.Cond
	closeCutoff    uint64 // Closing 后捕获；Open 时为 lastStamped

	// mirrors
	mirrors []*mirrorSlot

	// observer
	hub *eventHub
	// delivery journal（sanitized records）
	journalMu     sync.Mutex
	journal       []DeliveryRecord
	journalBytes  int
	journalDrops  uint64
	recordsSealed uint64
	// recordSlots is intentionally separate from journalMu.  Mirror workers
	// complete concurrently with Snapshot/RecentDeliveries and must not hold
	// the journal lock while assembling a final record.
	recordsMu   sync.Mutex
	recordSlots map[uint64]*deliveryRecordSlot

	closedCh  chan struct{}
	closeOnce sync.Once
	runOnce   sync.Once
	runCh     chan struct{}

	// reconfigure 两阶段状态（Phase 0 骨架）
	reconfigureToken string
	pendingRoute     RenderRouteConfig

	// 上次读到的 observer drop 累计（用于把 cutoff delta 归因到 submit）
	lastObserverDrops uint64
}

// gatewayStats 是 gateway 级计数器（驱动 RenderOutputSnapshot；全部累计单调，
// 除 ring buffer 内按记录重算的部分外不做带内归零）。
type gatewayStats struct {
	admissionAccepted   uint64
	admissionDeferred   uint64 // Phase 0 无 deferred 产生路径（骨架声明，后续 Phase 接入）
	admissionRejected   uint64
	primaryCommitted    uint64
	primaryZeroFailed   uint64
	primaryUnknown      uint64
	primaryDeferred     uint64
	primaryRejected     uint64
	mirrorScheduled     uint64
	mirrorScheduleDrops uint64
	mirrorSealed        uint64
	observerDrops       uint64
	journalDrops        uint64
	primaryInFlight     int
	mirrorPending       int
	mirrorInFlight      int
}

// deliveryRecordSlot keeps the mutable part of a record until every
// configured mirror has reached a terminal state.  A receipt is returned to
// the submitter before asynchronous mirrors finish, so the journal cannot be
// built from that receipt alone.
type deliveryRecordSlot struct {
	batch      RenderBatch
	receipt    OutputReceipt
	receiptSet bool
	admissions []MirrorAdmissionReceipt
	mirrors    []MirrorReceipt
	final      []bool
	sealed     bool
}

// NewRenderOutputGateway 构造 gateway；route 冻结校验，主 sink 打开。
func NewRenderOutputGateway(sessionID string, opts RenderGatewayOptions, route RenderRouteConfig) (*RenderOutputGateway, error) {
	if sessionID == "" {
		return nil, NewClassifiedError(DeliveryErrorInvalid, "gateway session id must not be empty")
	}
	if opts.Clock == nil {
		opts.Clock = SystemClock{}
	}
	// Capacity/deadline zero values are deliberately not treated as "unlimited"
	// or silently replaced.  A route is a security/lifecycle boundary and must
	// be explicitly bounded by its owner.
	if opts.MaxIntentBytes <= 0 {
		return nil, NewClassifiedError(DeliveryErrorInvalid, "max intent bytes must be positive")
	}
	if opts.MirrorQueueCapacity <= 0 {
		return nil, NewClassifiedError(DeliveryErrorInvalid, "mirror queue capacity must be positive")
	}
	if opts.CloseTimeout <= 0 {
		return nil, NewClassifiedError(DeliveryErrorInvalid, "close timeout must be positive")
	}
	if opts.ReconfigureTimeout <= 0 {
		return nil, NewClassifiedError(DeliveryErrorInvalid, "reconfigure timeout must be positive")
	}
	if opts.MaxSubscriptions <= 0 {
		return nil, NewClassifiedError(DeliveryErrorInvalid, "max subscriptions must be positive")
	}
	if opts.MaxSubscriptionBuffer <= 0 {
		return nil, NewClassifiedError(DeliveryErrorInvalid, "max subscription buffer must be positive")
	}
	if err := opts.DeliveryJournalLimit.Validate(); err != nil {
		return nil, err
	}
	if err := opts.EventJournalLimit.Validate(); err != nil {
		return nil, err
	}
	normalized, err := normalizeRoute(route)
	if err != nil {
		return nil, err
	}
	desc := normalized.Primary.Descriptor()
	g := &RenderOutputGateway{
		opts:        opts,
		clock:       opts.Clock,
		sessID:      sessionID,
		state:       GatewayOpen,
		route:       normalized,
		routeEpoch:  1,
		sequence:    0,
		targetDesc:  desc,
		primary:     normalized.Primary,
		hub:         newEventHub(opts.EventJournalLimit, opts.MaxSubscriptions, opts.MaxSubscriptionBuffer),
		closedCh:    make(chan struct{}),
		runCh:       make(chan struct{}),
		recordSlots: make(map[uint64]*deliveryRecordSlot),
	}
	g.primaryCond = sync.NewCond(&g.mu)
	for i, m := range normalized.Mirrors {
		g.mirrors = append(g.mirrors, newMirrorSlot(g, i, m))
	}
	return g, nil
}

// Run 启动 mirror workers（幂等）。在开始镜像调度前调用；Phase 0 测试可
// 不调用 Run（mirrors 仍登记但不会消费）。
func (g *RenderOutputGateway) Run() {
	g.runOnce.Do(func() {
		for _, ms := range g.mirrors {
			go ms.workerLoop()
		}
		close(g.runCh)
	})
}

// ============================================================================
// 内部 identity / event helper
// ============================================================================

func randomID(prefix string) string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	}
	return prefix + "-" + hex.EncodeToString(b)
}

// event 发布 helper：在 mu 外调用；hub 自带锁。
func (g *RenderOutputGateway) publish(ev OutputEvent) EventPublishResult {
	return g.hub.submit(ev)
}

// ============================================================================
// Submit：pre-admission -> stamp -> primary dispatch -> mirror admission
// ============================================================================

// Submit 提交一笔 intent。契约：
//   - 返回前已送达 admission 决策与（若被接受）primary outcome；
//   - pre-admission rejection/defer 的 Primary 为 nil、Sequence 为 0；
//   - 已调用 sink 后 Primary 非 nil（即使 sink 返回 rejected/deferred/zero）；
//   - Close 后（含 Closing 且超出 cutoff）返回 rejected/closed，Sequence=0。
func (g *RenderOutputGateway) Submit(ctx context.Context, intent RenderIntent) OutputReceipt {
	g.mu.Lock()
	if !canSubmit(g.state) {
		state := g.state
		g.mu.Unlock()
		if state == GatewayReconfiguring {
			return g.rejectedReceipt(intent, DeliveryErrorReconfiguring, "gateway is reconfiguring")
		}
		return g.rejectedReceipt(intent, DeliveryErrorClosed, "gateway not accepting submissions")
	}
	if err := validateIntent(intent, g.opts.MaxIntentBytes); err != nil {
		class := ClassOf(err)
		g.mu.Unlock()
		return g.rejectedReceipt(intent, class, safeAdmissionMessage(class, err.Error()))
	}
	// A caller-cancelled context is still a pre-admission cancellation.  Do
	// this while holding the same admission lock as the lifecycle fence so a
	// cancelled intent cannot consume a sequence or create a record slot.
	if ctx != nil {
		select {
		case <-ctx.Done():
			g.mu.Unlock()
			return g.rejectedReceipt(intent, DeliveryErrorCanceledBeforeIO, "submission canceled before admission")
		default:
		}
	}
	g.sequence++
	seq := g.sequence
	g.pendingStamps++ // stamp→dispatch 窗口：Close 排空必须等它归零
	batch := RenderBatch{
		RenderIntent:          intent.deepCopy(),
		SessionID:             g.sessID,
		Sequence:              seq,
		BatchID:               randomID("rb"),
		RouteEpoch:            g.routeEpoch,
		ProjectionTargetID:    g.targetDesc.ProjectionTargetID,
		ProjectionTargetClass: g.targetDesc.Class,
		BindingGeneration:     0, // Phase 3 前 binding generation 未启用；Phase 0 置 0
		PreparedAt:            g.clock.Now(),
	}
	// Keep the exact route membership selected by this admission.  A later
	// reconfiguration may replace g.primary/g.mirrors while this batch is
	// waiting for the serial boundary; it must never retarget an admitted
	// batch.
	batch.primarySink = g.primary
	batch.mirrorSlots = append([]*mirrorSlot(nil), g.mirrors...)
	g.initRecordSlot(batch, len(batch.mirrorSlots))
	if intent.HistoryEpoch != nil && historyBearingKind(intent.Kind) {
		ep := *intent.HistoryEpoch
		batch.History = &HistoryDeliveryDomain{
			ProjectionTargetID: g.targetDesc.ProjectionTargetID,
			HistoryEpoch:       ep,
		}
	}
	g.lastTerminal = intent.Terminal
	if batch.History != nil {
		h := *batch.History
		g.lastHistory = &h
	}
	g.stats.admissionAccepted++
	g.mu.Unlock()

	return g.deliver(ctx, batch)
}

func safeAdmissionMessage(class DeliveryErrorClass, _ string) string {
	switch class {
	case DeliveryErrorInvalid:
		return "invalid render intent"
	case DeliveryErrorOversized:
		return "intent exceeds max bytes"
	case DeliveryErrorCanceledBeforeIO:
		return "submission canceled before admission"
	case DeliveryErrorReconfiguring:
		return "gateway is reconfiguring"
	case DeliveryErrorClosed:
		return "gateway not accepting submissions"
	default:
		if class == DeliveryErrorNone {
			return "invalid render intent"
		}
		return string(class)
	}
}

func (g *RenderOutputGateway) rejectedReceipt(intent RenderIntent, class DeliveryErrorClass, msg string) OutputReceipt {
	r := OutputReceipt{
		SessionID: g.sessID,
		Admission: AdmissionReceipt{
			Decision:   AdmissionRejected,
			ErrorClass: class,
			Message:    msg,
		},
	}
	g.mu.Lock()
	g.stats.admissionRejected++
	// Read diagnostics only while holding the admission lock.  A
	// pre-admission receipt deliberately carries no batch/route/target
	// identity; those fields belong to an admitted stamped batch.
	sessionID := g.sessID
	g.mu.Unlock()
	r.SessionID = sessionID
	g.hub.submit(OutputEvent{
		SchemaVersion: SchemaVersion,
		Kind:          EventAdmissionRejected,
		At:            g.clock.Now(),
		SessionID:     sessionID,
		IntentID:      intent.IntentID,
		Transaction:   intent.Kind,
		Admission:     r.Admission,
	})
	return r
}

// deliver 执行 primary dispatch + mirror admission（serial 语义，见 6.3（1））。
func (g *RenderOutputGateway) deliver(ctx context.Context, batch RenderBatch) OutputReceipt {
	gate := newReceiptPublicationGate(BatchIdentity{
		SessionID:  batch.SessionID,
		Sequence:   batch.Sequence,
		BatchID:    batch.BatchID,
		RouteEpoch: batch.RouteEpoch,
	}, g.hub)

	// primary serial boundary：同一时刻只有一个 sink callback 在跑。
	g.mu.Lock()
	g.pendingStamps-- // stamp→dispatch 窗口关闭（deviate 拒绝路径也递减）
	g.primaryWaiters++
	for g.primaryBusy {
		g.primaryCond.Wait()
	}
	g.primaryWaiters--
	// 等待期间 gateway 可能被 Close：
	//   - 正常 Close 走排空：已盖章 batch 必须继续执行（closeCutoff 之内），
	//     不在此处拒绝——否则等待中的 batch 全被静默丢弃；
	//   - deviate（Close 超时/取消、或 Abort）置 primaryAborted=true，
	//     等待中的 batch 以 closed/abandoned 快速收敛，不执行 sink。
	if g.primaryAborted {
		g.mu.Unlock()
		gate.Retire()
		return OutputReceipt{
			SessionID:             batch.SessionID,
			Sequence:              batch.Sequence,
			BatchID:               batch.BatchID,
			RouteEpoch:            batch.RouteEpoch,
			ProjectionTargetID:    batch.ProjectionTargetID,
			ProjectionTargetClass: batch.ProjectionTargetClass,
			History:               batch.History,
			Admission: AdmissionReceipt{
				Decision:   AdmissionRejected,
				ErrorClass: DeliveryErrorAbandoned,
				Message:    "gateway aborted during close drain",
			},
		}
	}
	g.primaryBusy = true
	g.stats.primaryInFlight++
	g.mu.Unlock()

	// 事件：primary started（gate 未 frozen，仍然 open）。
	gate.Publish(OutputEvent{
		SchemaVersion: SchemaVersion,
		Kind:          EventPrimaryStarted,
		At:            g.clock.Now(),
		SessionID:     batch.SessionID,
		Sequence:      batch.Sequence,
		RouteEpoch:    batch.RouteEpoch,
		BatchID:       batch.BatchID,
		IntentID:      batch.IntentID,
		Transaction:   batch.Kind,
		Terminal:      batch.Terminal,
		History:       batch.History,
		TargetInvoked: false,
		InvocationID:  randomInvocation(),
	})

	startedAt := g.clock.Now()
	sink := batch.primarySink
	if sink == nil {
		// This should be impossible after admission, but fail closed rather than
		// dereferencing a route that may have been concurrently replaced.
		res := SinkDeliveryResult{
			Status:         DeliveryRejected,
			Certainty:      WriteCertaintyZero,
			ErrorClass:     DeliveryErrorAbandoned,
			AttemptedBytes: len(batch.Bytes),
			Err:            NewClassifiedError(DeliveryErrorAbandoned, "admitted batch lost its primary sink"),
		}
		_ = res
	}
	sinkCtx, cancel := contextWithClockTimeout(g.clock, ctx, g.opts.CloseTimeout) // 无专用 primary timeout，用 close budget 兜底
	res := deliverWithPanicGuard(sink.Submit, sinkCtx, batch)
	cancel()

	finishedAt := g.clock.Now()
	outcomeFixedAt := g.clock.Now()
	tr := TargetReceipt{
		SessionID:          batch.SessionID,
		Sequence:           batch.Sequence,
		BatchID:            batch.BatchID,
		RouteEpoch:         batch.RouteEpoch,
		BindingGeneration:  0,
		SinkID:             batch.ProjectionTargetID,
		TargetClass:        batch.ProjectionTargetClass,
		ProjectionTargetID: batch.ProjectionTargetID,
		InvocationID:       randomInvocation(),
		SinkDeliveryResult: normalizeSinkResult(res, len(batch.Bytes)),
		CallbackReturned:   true,
		StartedAt:          startedAt,
		FinishedAt:         finishedAt,
		OutcomeFixedAt:     outcomeFixedAt,
	}
	// 事件：primary completed + cutoff（同一 gate）。
	gate.Publish(OutputEvent{
		SchemaVersion:    SchemaVersion,
		Kind:             EventPrimaryCompleted,
		At:               finishedAt,
		SessionID:        batch.SessionID,
		Sequence:         batch.Sequence,
		RouteEpoch:       batch.RouteEpoch,
		BatchID:          batch.BatchID,
		TargetInvoked:    true,
		CallbackReturned: true,
		InvocationID:     tr.InvocationID,
		Status:           tr.Status,
		Certainty:        tr.Certainty,
		ErrorClass:       tr.ErrorClass,
		AttemptedBytes:   tr.AttemptedBytes,
		AcceptedBytes:    tr.AcceptedBytes,
	})
	_ = gate.Freeze(EventReceiptCutoff, finishedAt)

	// admission mirrors（非阻塞；在 cutoff 之后记录，只含已完成的 enqueue/drop）。
	mirrorAdmissions, scheduled, drops := g.admitMirrors(batch, tr)

	g.mu.Lock()
	g.stats.primaryInFlight--
	g.primaryBusy = false
	g.primaryCond.Broadcast()
	switch tr.Status {
	case DeliveryCommitted:
		g.stats.primaryCommitted++
	case DeliveryFailedZeroBytes:
		g.stats.primaryZeroFailed++
	case DeliveryUnknownPartial:
		g.stats.primaryUnknown++
	case DeliveryDeferred:
		g.stats.primaryDeferred++
	case DeliveryRejected:
		g.stats.primaryRejected++
	}
	// observer drops：以 cutoff marker 冻结时的累计为准，归因到本 submit。
	if cutoffDrops := gate.CutoffDrops(); cutoffDrops > 0 {
		g.stats.observerDrops += cutoffDrops - g.lastObserverDrops
		g.lastObserverDrops = cutoffDrops
	}
	g.mu.Unlock()

	receipt := OutputReceipt{
		SessionID:             batch.SessionID,
		Sequence:              batch.Sequence,
		BatchID:               batch.BatchID,
		RouteEpoch:            batch.RouteEpoch,
		ProjectionTargetID:    batch.ProjectionTargetID,
		ProjectionTargetClass: batch.ProjectionTargetClass,
		BindingGeneration:     0,
		History:               batch.History,
		Admission: AdmissionReceipt{
			Decision:   AdmissionAccepted,
			ErrorClass: DeliveryErrorNone,
		},
		TargetInvoked:              true,
		Primary:                    &tr,
		MirrorsScheduled:           scheduled,
		MirrorScheduleDrops:        drops,
		ObserverDrops:              gate.CutoffDrops(),
		ReceiptCutoffEventSequence: gate.CutoffSequence(),
		MirrorAdmissions:           mirrorAdmissions,
	}
	g.setRecordReceipt(batch.Sequence, receipt)
	gate.Retire()

	// 交付记录封存：Phase 0 在 primary outcome 固定、mirror 终态后由
	// mirror seal path 完成；若没有 mirrors 立即封存。
	if len(g.mirrors) == 0 {
		g.trySealRecord(batch.Sequence)
	}
	return receipt
}

// deliver 内部使用；primarySink 每次取当前 route primary。
func (g *RenderOutputGateway) primarySink() RenderOutputSink {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.primary
}

// randomInvocation 生成 invocation id。
func randomInvocation() uint64 {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return 0
	}
	return uint64(b[0])<<56 | uint64(b[1])<<48 | uint64(b[2])<<40 | uint64(b[3])<<32 |
		uint64(b[4])<<24 | uint64(b[5])<<16 | uint64(b[6])<<8 | uint64(b[7])
}

func contextWithClockTimeout(clock Clock, parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	if clock == nil {
		clock = SystemClock{}
	}
	ctx, cancel := context.WithCancel(parent)
	timer := clock.NewTimer(d)
	// A child context plus the injected timer keeps timeout behavior
	// deterministic under FakeClock while still honoring caller cancellation.
	go func() {
		select {
		case <-timer.C():
			cancel()
		case <-ctx.Done():
			timer.Stop()
		}
	}()
	return ctx, func() {
		timer.Stop()
		cancel()
	}
}

// deliverWithPanicGuard 捕获 sink callback 的 panic，归一化为 unknown
// outcome；panic 不击穿 gateway。
func deliverWithPanicGuard(submit func(context.Context, RenderBatch) SinkDeliveryResult,
	ctx context.Context, batch RenderBatch) (res SinkDeliveryResult) {
	defer func() {
		if r := recover(); r != nil {
			res = SinkDeliveryResult{
				Status:                  DeliveryUnknownPartial,
				Certainty:               WriteCertaintyUnknown,
				ErrorClass:              DeliveryErrorSink,
				AttemptedBytes:          len(batch.Bytes),
				AcceptedBytes:           0,
				MayHavePartiallyWritten: true,
				Err:                     NewClassifiedError(DeliveryErrorSink, "sink panic"),
			}
		}
	}()
	return submit(ctx, batch)
}

// admitMirrors 非阻塞地把 batch 调度到每个配置 mirror；只统计已完成的
// enqueue/drop，不等待 mirror I/O。
func (g *RenderOutputGateway) admitMirrors(batch RenderBatch, primary TargetReceipt) ([]MirrorAdmissionReceipt, int, int) {
	mirrors := append([]*mirrorSlot(nil), batch.mirrorSlots...)

	var ads []MirrorAdmissionReceipt
	scheduled, drops := 0, 0
	for i, ms := range mirrors {
		ad := MirrorAdmissionReceipt{
			EntryID:            randomID("me"),
			MirrorIndex:        i,
			SinkID:             ms.desc.SinkID,
			TargetClass:        ms.desc.Class,
			ProjectionTargetID: ms.desc.ProjectionTargetID,
			Policy:             ms.cfg.Policy,
			RequestedApplyMode: ms.cfg.ApplyMode,
			EffectiveApplyMode: ms.cfg.ApplyMode,
			NonAuthoritative:   false,
		}
		apply, skip := mirrorDecide(ms.cfg.Policy, primary.Status)
		nonAuthoritative := primary.Status != DeliveryCommitted ||
			primary.Certainty != WriteCertaintyFull
		effective := ms.cfg.ApplyMode
		if nonAuthoritative && ms.cfg.Policy != MirrorAttempted {
			effective = MirrorApplyMetadataOnly
		}
		ad.EffectiveApplyMode = effective
		ad.NonAuthoritative = nonAuthoritative
		switch {
		case !apply:
			ad.EffectiveApplyMode = MirrorApplyMetadataOnly
			ad.Scheduled = false
			ad.ErrorClass = DeliveryErrorNone
			ad.SkipReason = skip
		case ms.enqueue(batch, primary, ad):
			ad.Scheduled = true
			scheduled++
		case ms.closed():
			// gateway 已关闭：drop 分类为 closed，不是 queue full。
			ad.Scheduled = false
			ad.ErrorClass = DeliveryErrorClosed
			drops++
		default:
			ad.Scheduled = false
			ad.ErrorClass = DeliveryErrorQueueFull
			drops++
		}
		ads = append(ads, ad)
		// Record every configured mirror, including policy skips and scheduler
		// drops.  This is what lets a final DeliveryRecord distinguish a
		// terminal skip from an accepted-but-not-yet-sealed callback.
		g.recordAdmission(batch.Sequence, ad)
	}
	return ads, scheduled, drops
}

// mirrorDecide 是 6.5 outcome-aware 计算表：
//   - best_effort：任何 primary outcome 都 apply（包括 failed_zero）；
//   - committed_only：仅 committed apply；其余 skipped（metadata_only）；
//   - attempted：status != rejected 即 apply（含 deferred/unknown/zero）；
//     与 committed_only 的区别只对 deferred/unknown/zero 有意义。
func mirrorDecide(policy MirrorPolicy, status DeliveryStatus) (apply bool, skip MirrorSkipReason) {
	switch policy {
	case MirrorCommittedOnly:
		if status == DeliveryCommitted {
			return true, ""
		}
		return false, MirrorSkipPrimaryNotCommitted
	case MirrorAttempted:
		if status == DeliveryRejected {
			return false, MirrorSkipPrimaryNotCommitted
		}
		return true, ""
	default: // best_effort
		return true, ""
	}
}

// ============================================================================
// seal：delivery record 封存（journal）
// ============================================================================

func (g *RenderOutputGateway) initRecordSlot(batch RenderBatch, mirrorCount int) {
	if mirrorCount < 0 {
		mirrorCount = 0
	}
	g.recordsMu.Lock()
	g.recordSlots[batch.Sequence] = &deliveryRecordSlot{
		batch:      batch.deepCopy(),
		admissions: make([]MirrorAdmissionReceipt, mirrorCount),
		mirrors:    make([]MirrorReceipt, mirrorCount),
		final:      make([]bool, mirrorCount),
	}
	g.recordsMu.Unlock()
}

// recordAdmission records the admission result for one configured mirror.
// A non-scheduled admission is already terminal and is sealed immediately.
func (g *RenderOutputGateway) recordAdmission(sequence uint64, ad MirrorAdmissionReceipt) {
	g.recordsMu.Lock()
	slot := g.recordSlots[sequence]
	if slot == nil || ad.MirrorIndex < 0 || ad.MirrorIndex >= len(slot.admissions) {
		g.recordsMu.Unlock()
		return
	}
	idx := ad.MirrorIndex
	slot.admissions[idx] = ad
	if !ad.Scheduled {
		slot.mirrors[idx] = mirrorReceiptFromAdmission(ad)
		slot.final[idx] = true
	}
	record := g.takeReadyRecordLocked(sequence, slot)
	g.recordsMu.Unlock()
	if record != nil {
		g.appendDeliveryRecord(*record)
	}
}

// recordMirrorOutcome records a worker's terminal callback outcome. It is
// deliberately independent from the mirror worker's queue locks, so a fast
// callback cannot deadlock record sealing.
func (g *RenderOutputGateway) recordMirrorOutcome(sequence uint64, receipt MirrorReceipt) {
	g.recordsMu.Lock()
	slot := g.recordSlots[sequence]
	if slot == nil || receipt.MirrorIndex < 0 || receipt.MirrorIndex >= len(slot.mirrors) {
		g.recordsMu.Unlock()
		return
	}
	idx := receipt.MirrorIndex
	slot.mirrors[idx] = receipt
	slot.final[idx] = true
	record := g.takeReadyRecordLocked(sequence, slot)
	g.recordsMu.Unlock()
	if record != nil {
		g.appendDeliveryRecord(*record)
	}
}

func (g *RenderOutputGateway) setRecordReceipt(sequence uint64, receipt OutputReceipt) {
	g.recordsMu.Lock()
	slot := g.recordSlots[sequence]
	if slot == nil {
		g.recordsMu.Unlock()
		return
	}
	slot.receipt = receipt
	slot.receiptSet = true
	record := g.takeReadyRecordLocked(sequence, slot)
	g.recordsMu.Unlock()
	if record != nil {
		g.appendDeliveryRecord(*record)
	}
}

func (g *RenderOutputGateway) trySealRecord(sequence uint64) {
	g.recordsMu.Lock()
	slot := g.recordSlots[sequence]
	if slot == nil {
		g.recordsMu.Unlock()
		return
	}
	record := g.takeReadyRecordLocked(sequence, slot)
	g.recordsMu.Unlock()
	if record != nil {
		g.appendDeliveryRecord(*record)
	}
}

// takeReadyRecordLocked consumes a complete slot and returns a detached
// record. The caller must hold recordsMu and must append the returned record
// after releasing it.
func (g *RenderOutputGateway) takeReadyRecordLocked(sequence uint64, slot *deliveryRecordSlot) *DeliveryRecord {
	if slot == nil || slot.sealed || !slot.receiptSet {
		return nil
	}
	for i := range slot.admissions {
		if slot.admissions[i].EntryID == "" || !slot.final[i] {
			return nil
		}
	}
	mirrors := make([]RecordedMirrorReceipt, len(slot.mirrors))
	for i := range slot.mirrors {
		safe := safeMirrorMessage(slot.mirrors[i])
		mirrors[i] = slot.mirrors[i].ToRecorded(safe)
	}
	record := &DeliveryRecord{
		RecordID:      randomID("rd"),
		SchemaVersion: SchemaVersion,
		Batch:         SanitizedBatch(slot.batch, RecordedMetadataOnly, nil),
		Output:        slot.receipt.ToRecorded(),
		Mirrors:       mirrors,
		SealedAt:      g.clock.Now(),
	}
	slot.sealed = true
	delete(g.recordSlots, sequence)
	return record
}

func mirrorReceiptFromAdmission(ad MirrorAdmissionReceipt) MirrorReceipt {
	status := MirrorSkipped
	reason := ad.SkipReason
	if ad.Scheduled {
		status = MirrorFailed
		reason = MirrorSkipMetadataOnly
	}
	return MirrorReceipt{
		EntryID:            ad.EntryID,
		MirrorIndex:        ad.MirrorIndex,
		SinkID:             ad.SinkID,
		TargetClass:        ad.TargetClass,
		ProjectionTargetID: ad.ProjectionTargetID,
		Policy:             ad.Policy,
		RequestedApplyMode: ad.RequestedApplyMode,
		EffectiveApplyMode: ad.EffectiveApplyMode,
		NonAuthoritative:   ad.NonAuthoritative,
		Scheduled:          false,
		SinkInvoked:        false,
		TargetInvoked:      false,
		CallbackReturned:   false,
		Status:             status,
		ErrorClass:         ad.ErrorClass,
		SkipReason:         reason,
		SealedAt:           time.Time{},
	}
}

func safeMirrorMessage(r MirrorReceipt) string {
	if r.ErrorClass == DeliveryErrorNone {
		return ""
	}
	// Never persist Err.Error(): sink errors can contain payloads, paths, or
	// provider data. The stable class is the only safe diagnostic message at
	// this boundary.
	return string(r.ErrorClass)
}

func (g *RenderOutputGateway) appendDeliveryRecord(record DeliveryRecord) {
	g.journalMu.Lock()
	defer g.journalMu.Unlock()
	g.journal = append(g.journal, record)
	g.journalBytes += approxRecordBytes(record)
	for len(g.journal) > g.opts.DeliveryJournalLimit.MaxItems || g.journalBytes > g.opts.DeliveryJournalLimit.MaxBytes {
		if len(g.journal) == 0 {
			break
		}
		drop := g.journal[0]
		g.journal = g.journal[1:]
		g.journalBytes -= approxRecordBytes(drop)
		g.journalDrops++
	}
	g.recordsSealed++
}

// sealRecord 把 sanitized DeliveryRecord 追加到有界 journal。幂等由调用点保证
// （每个 batch 只 seal 一次）。
func (g *RenderOutputGateway) sealRecord(batch RenderBatch, receipt OutputReceipt) {
	record := DeliveryRecord{
		RecordID:      randomID("rd"),
		SchemaVersion: SchemaVersion,
		Batch:         SanitizedBatch(batch, RecordedMetadataOnly, nil),
		Output:        receipt.ToRecorded(),
		SealedAt:      g.clock.Now(),
	}
	g.appendDeliveryRecord(record)
}

func approxRecordBytes(r DeliveryRecord) int {
	n := len(r.Batch.BatchID) + len(r.Batch.Source) + len(r.Batch.Cause)
	for _, m := range r.Mirrors {
		n += len(m.EntryID) + len(m.SafeMessage)
	}
	return n + 256
}

// RecentDeliveries 返回最近 limit 个封存 record（detached）。
func (g *RenderOutputGateway) RecentDeliveries(limit int) []DeliveryRecord {
	g.journalMu.Lock()
	defer g.journalMu.Unlock()
	if limit <= 0 || limit > len(g.journal) {
		limit = len(g.journal)
	}
	out := make([]DeliveryRecord, 0, limit)
	for i := len(g.journal) - limit; i < len(g.journal); i++ {
		out = append(out, cloneDeliveryRecord(g.journal[i]))
	}
	return out
}

// RecentEvents 返回最近 limit 个事件（detached）。
func (g *RenderOutputGateway) RecentEvents(limit int) []OutputEvent {
	return g.hub.snapshotEvents(limit)
}

// Subscribe 创建事件订阅。
func (g *RenderOutputGateway) Subscribe(buffer int) (Subscription, error) {
	if buffer <= 0 {
		buffer = g.opts.MaxSubscriptionBuffer
	}
	return g.hub.Subscribe(buffer)
}

// ============================================================================
// 状态查询 / 控制
// ============================================================================

func (g *RenderOutputGateway) stateOf() GatewayLifecycleState {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.state
}

// routeEpochSnapshot 返回当前 route epoch（加锁读取，供 mirror snapshot）。
func (g *RenderOutputGateway) routeEpochSnapshot() uint64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.routeEpoch
}

// sequenceLocked 返回最后盖章 sequence（测试观察用）。
func (g *RenderOutputGateway) sequenceLocked() uint64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.sequence
}

// Snapshot 返回 detached gateway snapshot。
func (g *RenderOutputGateway) Snapshot() RenderOutputSnapshot {
	g.mu.Lock()
	primary := g.primary
	state := g.state
	routeEpoch := g.routeEpoch
	seq := g.sequence
	lastTerminal := g.lastTerminal
	lastHistory := g.lastHistory
	stats := g.stats
	closeCutoff := g.closeCutoff
	busy := g.primaryBusy
	g.mu.Unlock()

	ps := primary.Snapshot()
	snap := RenderOutputSnapshot{
		SchemaVersion:         SchemaVersion,
		State:                 state,
		SessionID:             g.sessID,
		RouteEpoch:            routeEpoch,
		Primary:               ps,
		PrimaryInFlight:       stats.primaryInFlight,
		LastSequence:          seq,
		LastTerminal:          lastTerminal,
		LastHistory:           lastHistory,
		CloseCutoffSequence:   closeCutoff,
		AdmissionAccepted:     stats.admissionAccepted,
		AdmissionDeferred:     stats.admissionDeferred,
		AdmissionRejected:     stats.admissionRejected,
		PrimaryCommitted:      stats.primaryCommitted,
		PrimaryZeroFailed:     stats.primaryZeroFailed,
		PrimaryUnknown:        stats.primaryUnknown,
		PrimaryDeferred:       stats.primaryDeferred,
		PrimaryRejected:       stats.primaryRejected,
		MirrorsScheduled:      stats.mirrorScheduled,
		MirrorScheduleDrops:   stats.mirrorScheduleDrops,
		ObserverDrops:         stats.observerDrops,
		EventJournalDrops:     g.hub.getJournalDrops(),
		MirrorPending:         stats.mirrorPending,
		MirrorInFlight:        stats.mirrorInFlight,
		MirrorEntriesUnsealed: stats.mirrorPending + stats.mirrorInFlight,
	}
	if busy {
		snap.PrimaryInFlight = maxInt(snap.PrimaryInFlight, 1)
	}
	g.journalMu.Lock()
	snap.DeliveryRecordsSealed = g.recordsSealed
	snap.DeliveryJournalDrops = g.journalDrops
	// 注意：带 mirror 时 record 封存为 Phase 4/5 扩展（maybeSealBatchRecord
	// 当前为空实现），DeliveryRecordsUnsealed 在此仅代表 primary 同步 seal
	// 路径，不代表完整事实，Phase 5 前勿依赖该字段做审计判定。
	snap.DeliveryRecordsUnsealed = 0
	g.journalMu.Unlock()

	// Mirrors：填各 slot 的可观察快照（detached）。
	for _, ms := range g.mirrorSlots() {
		snap.Mirrors = append(snap.Mirrors, ms.MirrorSnapshot())
	}
	return snap
}

// WaitIdle 等待 serial boundary 空闲（primary 无 in-flight；不等待 mirrors）。
// 实现使用简单轮询而非 cond：cond.Wait 需要调用者持锁，与 context 组合时
// 容易死锁；轮询对 Phase 0 语义足够且无锁序问题。
func (g *RenderOutputGateway) WaitIdle(ctx context.Context) error {
	for {
		g.mu.Lock()
		busy := g.primaryBusy
		g.mu.Unlock()
		if !busy {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Millisecond):
		}
	}
}

// Drain 等待 primary idle 且所有已登记 mirror entries seal。
func (g *RenderOutputGateway) Drain(ctx context.Context) error {
	if err := g.WaitIdle(ctx); err != nil {
		return err
	}
	for _, ms := range g.mirrors {
		if err := ms.waitSealed(ctx, g.clock.Now()); err != nil {
			return err
		}
	}
	return nil
}

// Close 进入 Closing、捕获 cutoff、排空并关闭 owned sinks。幂等。
//
// 语义：Close 捕获 closeCutoff=lastStamped 后，已盖章（Sequence<=cutoff）
// 的排队 batch 仍然继续执行（排空契约），直到 serial 空闲；若 ctx 取消或
// 超过 CloseTimeout，则 deviate：置 primaryAborted 让排队 batch 以
// abandoned 快速收敛，随后关闭 sinks。
func (g *RenderOutputGateway) Close(ctx context.Context) error {
	g.closeOnce.Do(func() {
		g.mu.Lock()
		if g.state == GatewayOpen || g.state == GatewayReconfiguring {
			g.state = GatewayClosing
			g.closeCutoff = g.sequence
		}
		if g.state == GatewayClosing && g.primaryBusy {
			// 有 in-flight callback：等待其返回（排空窗口），
			// 在拿到锁后持续等待直到 idle 或 deviate。
		}
		g.mu.Unlock()

		// 排空（带预算）：等待 serial 空闲；排队 batch 会被逐个执行。
		// 判定 = busy（in-flight） || waiters（cond 排队） || pendingStamps
		// （已盖章未进 serial）——缺任一都会误判为空、提前关闭 sink。
		deadline := time.Now().Add(g.opts.CloseTimeout)
		deviated := false
	drainLoop:
		for {
			g.mu.Lock()
			idle := !g.primaryBusy && g.primaryWaiters == 0 && g.pendingStamps == 0
			g.mu.Unlock()
			if idle {
				break
			}
			select {
			case <-ctx.Done():
				// deviate：排队 batch 快速收敛，不执行 sink。
				g.mu.Lock()
				g.primaryAborted = true
				g.primaryBusy = false
				g.primaryCond.Broadcast()
				g.mu.Unlock()
				deviated = true
				break drainLoop
			case <-time.After(5 * time.Millisecond):
			}
			if time.Now().After(deadline) {
				// deviate（deadline）：与 ctx.Done 相同收尾。
				g.mu.Lock()
				g.primaryAborted = true
				g.primaryBusy = false
				g.primaryCond.Broadcast()
				g.mu.Unlock()
				deviated = true
				break drainLoop
			}
		}
		if deviated {
			// 中断卡在底层 writer 中的 in-flight callback：请求 sink abort
			// （不持锁调用，避免锁序环）。PhysicalSink 会转发到 aborter，
			// 让被阻塞的 Submit 尽快以 canceled/zero 返回，实现 bounded close。
			// 仅对 owned sink 执行：borrowed 由 owner 负责中断（12.5），
			// gateway 只停止调用，避免永久破坏借用方 sink 的 aborted 状态。
			if g.route.PrimaryOwnership == SinkOwned {
				primary := g.primarySink()
				if primary != nil {
					if err := primary.Abort(AbortProofRequested); err != nil {
						// abort 失败不改变收尾路径；Close 仍继续。
						_ = err
					}
				}
			}
		}

		// mirrors：等待 seal（有界）后关闭。
		for _, ms := range g.mirrors {
			_ = ms.waitSealed(ctx, g.clock.Now())
			if ms.cfg.Ownership == SinkOwned {
				ms.cfg.Sink.Close(ctx)
			}
		}
		// primary close（owned 时）。
		primary := g.primarySink()
		if g.route.PrimaryOwnership == SinkOwned {
			primary.Close(ctx)
		}
		g.hub.shutdown()
		g.hub.submit(OutputEvent{
			SchemaVersion: SchemaVersion,
			Kind:          EventGatewayClosed,
			At:            g.clock.Now(),
			SessionID:     g.sessID,
			GatewayState:  GatewayClosed,
		})
		g.mu.Lock()
		g.state = GatewayClosed
		g.mu.Unlock()
		close(g.closedCh)
	})
	select {
	case <-g.closedCh:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ============================================================================
// Reconfigure（两阶段 barrier；Phase 5 完整实现，Phase 0 提供骨架语义）
// ============================================================================

// BeginReconfigure 校验并冻结旧 route（不开放 admission）。
func (g *RenderOutputGateway) BeginReconfigure(ctx context.Context, token string) error {
	if token == "" {
		return NewClassifiedError(DeliveryErrorInvalid, "reconfigure token must not be empty")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if !canBeginReconfigure(g.state) {
		return NewClassifiedError(DeliveryErrorReconfiguring,
			"cannot begin reconfigure in state "+string(g.state))
	}
	if g.primaryBusy {
		return NewClassifiedError(DeliveryErrorReconfiguring, "primary busy")
	}
	g.state = GatewayReconfiguring
	g.reconfigureToken = token
	return nil
}

// CommitReconfigure 安装新 route（生成新 RouteEpoch）。
func (g *RenderOutputGateway) CommitReconfigure(ctx context.Context, token string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.state != GatewayReconfiguring || g.reconfigureToken != token {
		return NewClassifiedError(DeliveryErrorInvalid, "reconfigure token mismatch or not begun")
	}
	newRoute := g.pendingRoute
	if newRoute.Primary == nil {
		return NewClassifiedError(DeliveryErrorInvalid, "no pending route to commit")
	}
	if err := validateRoute(newRoute); err != nil {
		return err
	}
	old := g.primary
	g.primary = newRoute.Primary
	g.targetDesc = newRoute.Primary.Descriptor()
	g.route = newRoute
	g.routeEpoch++
	g.state = GatewayOpen
	g.reconfigureToken = ""
	g.pendingRoute = RenderRouteConfig{}
	if g.route.PrimaryOwnership == SinkOwned && old != newRoute.Primary {
		_ = old.Close(ctx)
	}
	return nil
}

// AbortReconfigure 放弃 pending route；不开放 admission（回到 open 需显式
// Commit 或新的 Begin）。
func (g *RenderOutputGateway) AbortReconfigure(ctx context.Context, token string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.state != GatewayReconfiguring || g.reconfigureToken != token {
		return NewClassifiedError(DeliveryErrorInvalid, "reconfigure token mismatch or not begun")
	}
	g.reconfigureToken = ""
	g.pendingRoute = RenderRouteConfig{}
	if g.route.PrimaryOwnership == SinkOwned {
		// 新 route 未安装，不关闭旧 primary。
	}
	g.state = GatewayOpen
	return nil
}

// SetPendingRoute 在 Begin 后、Commit 前设置新 route（Phase 5 由调用方在
// barrier 内部使用）。
func (g *RenderOutputGateway) SetPendingRoute(route RenderRouteConfig) error {
	if err := validateRoute(route); err != nil {
		return err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.state != GatewayReconfiguring {
		return NewClassifiedError(DeliveryErrorInvalid, "must be reconfiguring to set pending route")
	}
	g.pendingRoute = route
	return nil
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// String 便于日志。
func (k TransactionKind) String() string { return string(k) }

// mirrorSlots 返回当前 mirror slots（加锁拷贝，供 Snapshot）。
func (g *RenderOutputGateway) mirrorSlots() []*mirrorSlot {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]*mirrorSlot(nil), g.mirrors...)
}

// MirrorSlots 暴露内部 mirror 列表供测试观察（read-only）。
func (g *RenderOutputGateway) MirrorSlots() []*mirrorSlot {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]*mirrorSlot(nil), g.mirrors...)
}

// DeliveryJournalDropCount 返回 journal 淘汰计数。
func (g *RenderOutputGateway) DeliveryJournalDropCount() uint64 {
	g.journalMu.Lock()
	defer g.journalMu.Unlock()
	return g.journalDrops
}

// RecordsSealed 返回已封存 record 总数。
func (g *RenderOutputGateway) RecordsSealed() uint64 {
	g.journalMu.Lock()
	defer g.journalMu.Unlock()
	return g.recordsSealed
}

// sortMirrors 供测试断言顺序。
func sortMirrorsByIndex(ms []MirrorRouteSnapshot) {
	sort.Slice(ms, func(i, j int) bool { return ms[i].MirrorIndex < ms[j].MirrorIndex })
}

var _ = sortMirrorsByIndex
