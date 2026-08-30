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
	if interfaceIsNil(route.Primary) {
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
		if interfaceIsNil(m.Sink) {
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
	aNil, bNil := interfaceIsNil(a), interfaceIsNil(b)
	if aNil || bNil {
		return aNil && bNil
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

func interfaceIsNil(v interface{}) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map,
		reflect.Ptr, reflect.Slice:
		return rv.IsNil()
	default:
		return false
	}
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
	BeginReconfigure(ctx context.Context, route RenderRouteConfig) (RouteChangePlan, error)
	CommitReconfigure(ctx context.Context, token string) error
	AbortReconfigure(ctx context.Context, token string) error
}

type reconfigureCompletion struct {
	err error
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
	nextInvocation uint64 // gateway-owned, non-zero and monotonic
	closeCutoff    uint64 // Closing 后捕获；Open 时为 lastStamped

	// mirrors
	mirrors []*mirrorSlot
	running bool

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
	recordsMu          sync.Mutex
	recordSlots        map[uint64]*deliveryRecordSlot
	primaryDoneThrough uint64
	primaryDonePending map[uint64]struct{}
	recordDoneThrough  uint64
	recordDonePending  map[uint64]struct{}
	progressMu         sync.Mutex
	progressCh         chan struct{}

	closedCh  chan struct{}
	closeOnce sync.Once
	closeErr  error
	runOnce   sync.Once
	runCh     chan struct{}

	// reconfigure 两阶段状态（9.2）
	pendingRoute                 RenderRouteConfig
	reconfigureCutoffSequence    uint64
	nextRouteEpoch               uint64
	reconfigurePlan              RouteChangePlan
	reconfigureDisposition       reconfigureDisposition
	reconfigureFinalized         bool
	reconfigureDone              chan struct{}
	reconfigureMemo              map[string]reconfigureCompletion

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
		opts:               opts,
		clock:              opts.Clock,
		sessID:             sessionID,
		state:              GatewayOpen,
		route:              normalized,
		routeEpoch:         1,
		nextRouteEpoch:     1,
		sequence:           0,
		targetDesc:         desc,
		primary:            normalized.Primary,
		hub:                newEventHub(opts.EventJournalLimit, opts.MaxSubscriptions, opts.MaxSubscriptionBuffer),
		closedCh:           make(chan struct{}),
		runCh:              make(chan struct{}),
		recordSlots:        make(map[uint64]*deliveryRecordSlot),
		primaryDonePending: make(map[uint64]struct{}),
		recordDonePending:  make(map[uint64]struct{}),
		progressCh:         make(chan struct{}),
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
		g.mu.Lock()
		g.running = true
		mirrors := append([]*mirrorSlot(nil), g.mirrors...)
		g.mu.Unlock()
		for _, ms := range mirrors {
			ms.start()
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
	batch.primaryDesc = g.targetDesc
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
	for g.primaryBusy && !g.primaryAborted {
		g.primaryCond.Wait()
	}
	g.primaryWaiters--
	// 等待期间 gateway 可能被 Close：
	//   - 正常 Close 走排空：已盖章 batch 必须继续执行（closeCutoff 之内），
	//     不在此处拒绝——否则等待中的 batch 全被静默丢弃；
	//   - deviate（Close 超时/取消、或 Abort）置 primaryAborted=true，
	//     等待中的 batch 以 closed/abandoned 快速收敛，不执行 sink。
	sink := batch.primarySink
	if g.primaryAborted {
		// Preserve the accepted-batch shape and let the synthetic uninvoked
		// path below finalize its primary receipt, mirrors, and record slot.
		sink = nil
	}
	targetInvoked := !interfaceIsNil(sink)
	var invocationID uint64
	if targetInvoked {
		g.nextInvocation++
		if g.nextInvocation == 0 {
			g.nextInvocation++
		}
		invocationID = g.nextInvocation
	}
	if targetInvoked {
		g.primaryBusy = true
		g.stats.primaryInFlight++
	}
	g.mu.Unlock()

	var startedAt time.Time
	if targetInvoked {
		startedAt = g.clock.Now()
		gate.Publish(OutputEvent{
			SchemaVersion: SchemaVersion,
			Kind:          EventPrimaryStarted,
			At:            startedAt,
			SessionID:     batch.SessionID,
			Sequence:      batch.Sequence,
			RouteEpoch:    batch.RouteEpoch,
			BatchID:       batch.BatchID,
			IntentID:      batch.IntentID,
			Transaction:   batch.Kind,
			Terminal:      batch.Terminal,
			History:       batch.History,
			TargetInvoked: true,
			InvocationID:  invocationID,
		})
	}

	if !targetInvoked {
		// This should be impossible after admission, but fail closed rather than
		// dereferencing a route that may have been concurrently replaced.
		res := SinkDeliveryResult{
			Status:         DeliveryRejected,
			Certainty:      WriteCertaintyZero,
			ErrorClass:     DeliveryErrorAbandoned,
			AttemptedBytes: len(batch.Bytes),
			Err:            NewClassifiedError(DeliveryErrorAbandoned, "admitted batch lost its primary sink"),
		}
		outcomeFixedAt := g.clock.Now()
		tr := TargetReceipt{
			SessionID:          batch.SessionID,
			Sequence:           batch.Sequence,
			BatchID:            batch.BatchID,
			RouteEpoch:         batch.RouteEpoch,
			BindingGeneration:  batch.BindingGeneration,
			SinkID:             batch.primaryDesc.SinkID,
			TargetClass:        batch.primaryDesc.Class,
			ProjectionTargetID: batch.primaryDesc.ProjectionTargetID,
			InvocationID:       0,
			Synthetic:          true,
			SinkDeliveryResult: normalizeSinkResult(res, len(batch.Bytes)),
			CallbackReturned:   false,
			OutcomeFixedAt:     outcomeFixedAt,
		}
		gate.Publish(OutputEvent{
			SchemaVersion:    SchemaVersion,
			Kind:             EventPrimaryCompleted,
			At:               outcomeFixedAt,
			SessionID:        batch.SessionID,
			Sequence:         batch.Sequence,
			RouteEpoch:       batch.RouteEpoch,
			BatchID:          batch.BatchID,
			TargetInvoked:    false,
			CallbackReturned: false,
			InvocationID:     0,
			Synthetic:        true,
			Status:           tr.Status,
			Certainty:        tr.Certainty,
			ErrorClass:       tr.ErrorClass,
			AttemptedBytes:   tr.AttemptedBytes,
			AcceptedBytes:    tr.AcceptedBytes,
		})
		_ = gate.Freeze(EventReceiptCutoff, outcomeFixedAt)
		mirrorAdmissions := g.skipFrozenMirrors(batch, DeliveryErrorAbandoned)

		g.mu.Lock()
		g.stats.primaryRejected++
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
			BindingGeneration:     batch.BindingGeneration,
			History:               cloneHistory(batch.History),
			Admission: AdmissionReceipt{
				Decision:   AdmissionAccepted,
				ErrorClass: DeliveryErrorNone,
			},
			TargetInvoked:              false,
			Primary:                    &tr,
			MirrorAdmissions:           mirrorAdmissions,
			ReceiptCutoffEventSequence: gate.CutoffSequence(),
			ObserverDrops:              gate.CutoffDrops(),
		}
		g.setRecordReceipt(batch.Sequence, receipt)
		gate.Retire()
		g.trySealRecord(batch.Sequence)
		return receipt
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
		BindingGeneration:  batch.BindingGeneration,
		SinkID:             batch.primaryDesc.SinkID,
		TargetClass:        batch.primaryDesc.Class,
		ProjectionTargetID: batch.primaryDesc.ProjectionTargetID,
		InvocationID:       invocationID,
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
	_ = gate.Freeze(EventReceiptCutoff, outcomeFixedAt)

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
		BindingGeneration:     batch.BindingGeneration,
		History:               cloneHistory(batch.History),
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
	if len(batch.mirrorSlots) == 0 {
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

func nonNilContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
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

// skipFrozenMirrors fixes every mirror selected at admission as a terminal,
// non-authoritative skip when the admitted primary cannot be invoked.  It must
// use batch.mirrorSlots rather than the current route: reconfiguration may
// replace g.mirrors while this batch waits at the primary serial boundary.
func (g *RenderOutputGateway) skipFrozenMirrors(batch RenderBatch, class DeliveryErrorClass) []MirrorAdmissionReceipt {
	mirrors := append([]*mirrorSlot(nil), batch.mirrorSlots...)
	ads := make([]MirrorAdmissionReceipt, 0, len(mirrors))
	for i, ms := range mirrors {
		ad := MirrorAdmissionReceipt{
			EntryID:            randomID("me"),
			MirrorIndex:        i,
			EffectiveApplyMode: MirrorApplyMetadataOnly,
			NonAuthoritative:   true,
			Scheduled:          false,
			ErrorClass:         class,
			SkipReason:         MirrorSkipPrimaryNotCommitted,
		}
		if ms != nil {
			ad.SinkID = ms.desc.SinkID
			ad.TargetClass = ms.desc.Class
			ad.ProjectionTargetID = ms.desc.ProjectionTargetID
			ad.Policy = ms.cfg.Policy
			ad.RequestedApplyMode = ms.cfg.ApplyMode
		}
		ads = append(ads, ad)
		g.recordAdmission(batch.Sequence, ad)
	}
	return ads
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

func markContiguous(sequence uint64, through *uint64, pending map[uint64]struct{}) {
	if sequence <= *through {
		return
	}
	pending[sequence] = struct{}{}
	for {
		next := *through + 1
		if _, ok := pending[next]; !ok {
			return
		}
		delete(pending, next)
		*through = next
	}
}

func (g *RenderOutputGateway) signalProgress() {
	g.progressMu.Lock()
	close(g.progressCh)
	g.progressCh = make(chan struct{})
	g.progressMu.Unlock()
}

func (g *RenderOutputGateway) progressSignal() <-chan struct{} {
	g.progressMu.Lock()
	ch := g.progressCh
	g.progressMu.Unlock()
	return ch
}

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
	markContiguous(sequence, &g.primaryDoneThrough, g.primaryDonePending)
	record := g.takeReadyRecordLocked(sequence, slot)
	g.recordsMu.Unlock()
	if record != nil {
		g.appendDeliveryRecord(*record)
	}
	g.signalProgress()
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
	markContiguous(sequence, &g.recordDoneThrough, g.recordDonePending)
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
	g.journalMu.Unlock()
	g.signalProgress()
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
		LastHistory:           cloneHistory(lastHistory),
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
	g.journalMu.Unlock()
	g.recordsMu.Lock()
	snap.DeliveryRecordsUnsealed = len(g.recordSlots)
	g.recordsMu.Unlock()

	// Mirrors：填各 slot 的可观察快照（detached）。
	for _, ms := range g.mirrorSlots() {
		snap.Mirrors = append(snap.Mirrors, ms.MirrorSnapshot())
	}
	return snap
}

func (g *RenderOutputGateway) waitForCutoff(ctx context.Context, cutoff uint64, records bool) error {
	for {
		progress := g.progressSignal()
		g.recordsMu.Lock()
		doneThrough := g.primaryDoneThrough
		if records {
			doneThrough = g.recordDoneThrough
		}
		g.recordsMu.Unlock()
		if doneThrough >= cutoff {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-progress:
		}
	}
}

// WaitIdle captures the current admission cutoff and waits only for primary
// outcomes at or below it. Later submissions therefore cannot extend this
// caller's wait.
func (g *RenderOutputGateway) WaitIdle(ctx context.Context) error {
	ctx = nonNilContext(ctx)
	g.mu.Lock()
	cutoff := g.sequence
	g.mu.Unlock()
	return g.waitForCutoff(ctx, cutoff, false)
}

// Drain captures one cutoff and additionally waits for every configured mirror
// entry and delivery record at or below that cutoff to seal.
func (g *RenderOutputGateway) Drain(ctx context.Context) error {
	ctx = nonNilContext(ctx)
	g.mu.Lock()
	cutoff := g.sequence
	g.mu.Unlock()
	if err := g.waitForCutoff(ctx, cutoff, false); err != nil {
		return err
	}
	return g.waitForCutoff(ctx, cutoff, true)
}

// Close 进入 Closing、捕获 cutoff、排空并关闭 owned sinks。幂等。
//
// 语义：Close 捕获 closeCutoff=lastStamped 后，已盖章（Sequence<=cutoff）
// 的排队 batch 仍然继续执行（排空契约），直到 serial 空闲；若 ctx 取消或
// 超过 CloseTimeout，则 deviate：置 primaryAborted 让排队 batch 以
// abandoned 快速收敛，随后关闭 sinks。
func (g *RenderOutputGateway) Close(ctx context.Context) error {
	ctx = nonNilContext(ctx)
	g.closeOnce.Do(func() {
		g.mu.Lock()
		g.state = GatewayClosing
		g.closeCutoff = g.sequence
		cutoff := g.closeCutoff
		pendingRoute := g.pendingRoute
		g.pendingRoute = RenderRouteConfig{}
		g.mu.Unlock()
		// The shared close operation owns its own deadline. A caller context
		// only controls that caller's wait below.
		go g.finishClose(cutoff, pendingRoute)
	})
	select {
	case <-g.closedCh:
		g.mu.Lock()
		err := g.closeErr
		g.mu.Unlock()
		return err
	case <-ctx.Done():
		class := DeliveryErrorControlCanceled
		if ctx.Err() == context.DeadlineExceeded {
			class = DeliveryErrorTimeout
		}
		return NewClassifiedError(class, "close wait canceled")
	}
}

func (g *RenderOutputGateway) finishClose(cutoff uint64, pendingRoute RenderRouteConfig) {
	closeCtx, cancel := contextWithClockTimeout(g.clock, context.Background(), g.opts.CloseTimeout)
	defer cancel()

	closeErr := g.waitForCutoff(closeCtx, cutoff, false)
	if closeErr == nil {
		closeErr = g.waitForCutoff(closeCtx, cutoff, true)
	}
	deviated := closeErr != nil

	g.mu.Lock()
	route := g.route
	primary := g.primary
	mirrors := append([]*mirrorSlot(nil), g.mirrors...)
	primaryWasBusy := g.primaryBusy
	if deviated {
		g.primaryAborted = true
		// The physical callback may still be quarantined, but no subsequent
		// batch may invoke the sink once primaryAborted is set. Releasing the
		// logical serial gate lets already-admitted waiters finalize synthetic
		// abandoned receipts instead of waiting on an uninterruptible writer.
		g.primaryBusy = false
		g.primaryCond.Broadcast()
	}
	g.mu.Unlock()

	primaryTerminated := !primaryWasBusy
	if deviated && route.PrimaryOwnership == SinkOwned && !interfaceIsNil(primary) {
		if err := primary.Abort(AbortProofRequested); err == nil && primaryWasBusy {
			abortSnapshot := primary.Snapshot()
			primaryTerminated = abortSnapshot.AbortProof == AbortProofTerminated
		}
	}

	seen := make([]interface{}, 0, 2+len(mirrors)+len(pendingRoute.Mirrors))
	closeOwned := func(sink interface{}, owned bool) {
		if !owned || interfaceIsNil(sink) {
			return
		}
		for _, prior := range seen {
			if sameSink(prior, sink) {
				return
			}
		}
		seen = append(seen, sink)
		switch typed := sink.(type) {
		case RenderOutputSink:
			_ = typed.Close(closeCtx)
		case RenderMirrorSink:
			_ = typed.Close(closeCtx)
		}
	}
	for _, ms := range mirrors {
		ms.retire()
		closeOwned(ms.cfg.Sink, ms.cfg.Ownership == SinkOwned)
	}
	closeOwned(primary, route.PrimaryOwnership == SinkOwned)
	for _, mirror := range pendingRoute.Mirrors {
		closeOwned(mirror.Sink, mirror.Ownership == SinkOwned)
	}
	closeOwned(pendingRoute.Primary, pendingRoute.PrimaryOwnership == SinkOwned)

	finalState := GatewayClosed
	var terminalErr error
	if deviated {
		terminalErr = NewClassifiedError(DeliveryErrorTimeout, "gateway close deadline exceeded")
		if !primaryTerminated {
			finalState = GatewayAbandoned
			terminalErr = NewClassifiedError(
				DeliveryErrorAbandoned,
				"primary callback termination could not be proven",
			)
		}
	}
	g.hub.submit(OutputEvent{
		SchemaVersion: SchemaVersion,
		Kind:          EventGatewayClosed,
		At:            g.clock.Now(),
		SessionID:     g.sessID,
		GatewayState:  finalState,
	})
	g.hub.shutdown()
	g.mu.Lock()
	g.state = finalState
	g.closeErr = terminalErr
	g.mu.Unlock()
	close(g.closedCh)
}

// ============================================================================
// Reconfigure（两阶段 barrier；9.2）
// ============================================================================

// BeginReconfigure 校验新 route、原子进入 Reconfiguring、捕获 cutoff 并
// 预留新 epoch；old route 仍是唯一已安装 route。返回 plan（token/transition
// 等）。此后并发 Submit 返回 AdmissionRejected+Reconfiguring（Primary=nil）。
// closeCutoff 只按 accepted batch 的 Sequence<=ReconfigureCutoffSequence 选取。
func (g *RenderOutputGateway) BeginReconfigure(ctx context.Context, newRoute RenderRouteConfig) (RouteChangePlan, error) {
	if ctx != nil && ctx.Err() != nil {
		return RouteChangePlan{}, NewClassifiedError(DeliveryErrorControlCanceled, "reconfigure begin canceled")
	}
	normalized, err := normalizeRoute(newRoute)
	if err != nil {
		return RouteChangePlan{}, err
	}
	newDesc := normalized.Primary.Descriptor()
	g.mu.Lock()
	if !canBeginReconfigure(g.state) {
		state := g.state
		g.mu.Unlock()
		class := DeliveryErrorReconfiguring
		if state == GatewayClosing || state == GatewayClosed || state == GatewayAbandoned {
			class = DeliveryErrorClosed
		}
		return RouteChangePlan{}, NewClassifiedError(class,
			"cannot begin reconfigure in state "+string(state))
	}
	// 进入 Reconfiguring：原子捕获 cutoff 与旧 descriptor，预留新 epoch。
	oldDesc := g.targetDesc
	oldEpoch := g.routeEpoch
	cutoff := g.sequence
	newEpoch := g.nextRouteEpoch + 1
	plan := RouteChangePlan{
		Token:                     fmt.Sprintf("rt-%d-%s", newEpoch, randomID("tok")),
		OldRouteEpoch:             oldEpoch,
		NewRouteEpoch:             newEpoch,
		OldTarget:                 oldDesc,
		NewTarget:                 newDesc,
		ReconfigureCutoffSequence: cutoff,
		Transition: ProjectionTransition{
			OldRouteEpoch:    oldEpoch,
			NewRouteEpoch:    newEpoch,
			OldTargetID:      oldDesc.ProjectionTargetID,
			OldTargetClass:   oldDesc.Class,
			NewTargetID:      newDesc.ProjectionTargetID,
			NewTargetClass:   newDesc.Class,
			Continuity:       ContinuityUnproven,
			ScreenAction:     ProjectionInvalidate,
			HistoryAction:    ProjectionRebuild,
			Bootstrap:        BootstrapReplayStable,
			ContinuityReason: "route switch requires new target proof",
		},
	}
	if err := validateTransition(plan.Transition); err != nil {
		g.mu.Unlock()
		return RouteChangePlan{}, err
	}
	g.state = GatewayReconfiguring
	g.reconfigureCutoffSequence = cutoff
	g.nextRouteEpoch = newEpoch
	g.reconfigurePlan = plan
	g.pendingRoute = normalized
	g.reconfigureDisposition = reconfigureInstallNew
	g.reconfigureFinalized = false
	g.reconfigureDone = nil
	g.mu.Unlock()
	return plan, nil
}

// CommitReconfigure 是唯一解除 admission barrier 的操作：统一 finalizer，
// 按 disposition 收口 install-new（切换 primary/mirrors 并回 Open）或
// rollback-old（保留 old route 并回 Open）。token 校验失败或 finalization
// 前的 context 取消返回错误；finalization 一旦开始不可取消（除非进入
// Abandoned）。同 token 重复调用返回 memoized result。
func (g *RenderOutputGateway) CommitReconfigure(ctx context.Context, token string) error {
	ctx = nonNilContext(ctx)
	g.mu.Lock()
	if completion, ok := g.reconfigureMemo[token]; ok {
		g.mu.Unlock()
		return completion.err
	}
	if g.state != GatewayReconfiguring || g.reconfigurePlan.Token == "" ||
		g.reconfigurePlan.Token != token {
		state := g.state
		g.mu.Unlock()
		class := DeliveryErrorStaleRoute
		if state == GatewayClosing || state == GatewayClosed || state == GatewayAbandoned {
			class = DeliveryErrorClosed
		}
		return NewClassifiedError(class,
			"reconfigure token mismatch or not begun (state="+string(state)+")")
	}
	if g.reconfigureFinalized {
		done := g.reconfigureDone
		g.mu.Unlock()
		return g.waitReconfigure(ctx, token, done)
	}
	if err := ctx.Err(); err != nil {
		g.mu.Unlock()
		return reconfigureWaitError(err)
	}
	g.reconfigureFinalized = true
	g.reconfigureDone = make(chan struct{})
	done := g.reconfigureDone
	g.mu.Unlock()

	go g.finishReconfigure(token)
	return g.waitReconfigure(ctx, token, done)
}

func reconfigureWaitError(err error) error {
	class := DeliveryErrorControlCanceled
	if err == context.DeadlineExceeded {
		class = DeliveryErrorTimeout
	}
	return NewClassifiedError(class, "reconfigure wait canceled")
}

func (g *RenderOutputGateway) waitReconfigure(ctx context.Context, token string, done <-chan struct{}) error {
	select {
	case <-done:
		g.mu.Lock()
		completion, ok := g.reconfigureMemo[token]
		g.mu.Unlock()
		if !ok {
			return NewClassifiedError(DeliveryErrorStaleRoute, "reconfigure result unavailable")
		}
		return completion.err
	case <-ctx.Done():
		return reconfigureWaitError(ctx.Err())
	}
}

func (g *RenderOutputGateway) completeReconfigureLocked(token string, err error) {
	if g.reconfigureMemo == nil {
		g.reconfigureMemo = make(map[string]reconfigureCompletion)
	}
	g.reconfigureMemo[token] = reconfigureCompletion{err: err}
	if g.reconfigureDone != nil {
		close(g.reconfigureDone)
		g.reconfigureDone = nil
	}
}

func (g *RenderOutputGateway) clearReconfigureLocked() {
	g.reconfigurePlan = RouteChangePlan{}
	g.pendingRoute = RenderRouteConfig{}
	g.reconfigureCutoffSequence = 0
	g.reconfigureDisposition = reconfigureInstallNew
}

func (g *RenderOutputGateway) finishReconfigure(token string) {
	g.mu.Lock()
	if g.state != GatewayReconfiguring || g.reconfigurePlan.Token != token {
		g.completeReconfigureLocked(token,
			NewClassifiedError(DeliveryErrorClosed, "reconfigure superseded by gateway close"))
		g.mu.Unlock()
		return
	}
	cutoff := g.reconfigureCutoffSequence
	newRoute := g.pendingRoute
	oldRoute := g.route
	oldMirrors := append([]*mirrorSlot(nil), g.mirrors...)
	newEpoch := g.reconfigurePlan.NewRouteEpoch
	newDesc := g.reconfigurePlan.NewTarget
	rollback := g.reconfigureDisposition == reconfigureRollback
	g.mu.Unlock()

	finalizeCtx, cancel := contextWithClockTimeout(
		g.clock, context.Background(), g.opts.ReconfigureTimeout,
	)
	defer cancel()
	if err := g.waitForCutoff(finalizeCtx, cutoff, true); err != nil {
		g.mu.Lock()
		if g.state != GatewayReconfiguring || g.reconfigurePlan.Token != token {
			g.completeReconfigureLocked(token,
				NewClassifiedError(DeliveryErrorClosed, "reconfigure superseded by gateway close"))
			g.mu.Unlock()
			return
		}
		g.state = GatewayOpen
		g.clearReconfigureLocked()
		g.mu.Unlock()
		closeRouteResources(finalizeCtx, newRoute, nil, oldRoute)
		g.mu.Lock()
		g.completeReconfigureLocked(token,
			NewClassifiedError(DeliveryErrorTimeout, "reconfigure drain deadline exceeded"))
		g.mu.Unlock()
		return
	}

	if rollback {
		g.mu.Lock()
		if g.state != GatewayReconfiguring || g.reconfigurePlan.Token != token {
			g.completeReconfigureLocked(token,
				NewClassifiedError(DeliveryErrorClosed, "reconfigure superseded by gateway close"))
			g.mu.Unlock()
			return
		}
		g.state = GatewayOpen
		g.clearReconfigureLocked()
		g.mu.Unlock()
		closeRouteResources(finalizeCtx, newRoute, nil, oldRoute)
		g.mu.Lock()
		g.completeReconfigureLocked(token, nil)
		g.mu.Unlock()
		return
	}

	newMirrors := make([]*mirrorSlot, 0, len(newRoute.Mirrors))
	for i, mirror := range newRoute.Mirrors {
		newMirrors = append(newMirrors, newMirrorSlot(g, i, mirror, newEpoch))
	}

	g.mu.Lock()
	if g.state != GatewayReconfiguring || g.reconfigurePlan.Token != token {
		for _, ms := range newMirrors {
			ms.retire()
		}
		g.completeReconfigureLocked(token,
			NewClassifiedError(DeliveryErrorClosed, "reconfigure superseded by gateway close"))
		g.mu.Unlock()
		return
	}
	running := g.running
	g.primary = newRoute.Primary
	g.targetDesc = newDesc
	g.route = newRoute
	g.mirrors = newMirrors
	g.routeEpoch = newEpoch
	g.state = GatewayOpen
	g.clearReconfigureLocked()
	g.mu.Unlock()

	if running {
		for _, ms := range newMirrors {
			ms.start()
		}
	}
	closeRouteResources(finalizeCtx, oldRoute, oldMirrors, newRoute)

	g.mu.Lock()
	g.completeReconfigureLocked(token, nil)
	g.mu.Unlock()
}

// AbortReconfigure 只把 disposition 固定为 rollback-old；不开放 admission、
// 不清 token、不改 state——presenter 必须以同一 token 调用 CommitReconfigure
// 收口（9.2 第 9 条）。重复 abort 幂等返回 memoized/conflict 结果。
func (g *RenderOutputGateway) AbortReconfigure(ctx context.Context, token string) error {
	ctx = nonNilContext(ctx)
	if err := ctx.Err(); err != nil {
		return reconfigureWaitError(err)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.state != GatewayReconfiguring || g.reconfigurePlan.Token == "" {
		return NewClassifiedError(DeliveryErrorStaleRoute, "reconfigure not begun or already finalized")
	}
	if g.reconfigurePlan.Token != token {
		return NewClassifiedError(DeliveryErrorStaleRoute, "reconfigure token mismatch")
	}
	if g.reconfigureFinalized {
		return NewClassifiedError(DeliveryErrorReconfiguring, "reconfigure finalization already started")
	}
	g.reconfigureDisposition = reconfigureRollback
	return nil
}

// SetPendingRoute 在 Begin 后、Commit 前由调用方设置 final route（校验）。
func (g *RenderOutputGateway) SetPendingRoute(route RenderRouteConfig) error {
	normalized, err := normalizeRoute(route)
	if err != nil {
		return err
	}
	desc := normalized.Primary.Descriptor()
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.state != GatewayReconfiguring {
		return NewClassifiedError(DeliveryErrorInvalid, "must be reconfiguring to set pending route")
	}
	if g.reconfigureFinalized {
		return NewClassifiedError(DeliveryErrorReconfiguring, "reconfigure finalization already started")
	}
	g.pendingRoute = normalized
	g.reconfigurePlan.NewTarget = desc
	g.reconfigurePlan.Transition.NewTargetID = desc.ProjectionTargetID
	g.reconfigurePlan.Transition.NewTargetClass = desc.Class
	return nil
}

func routeContainsSink(route RenderRouteConfig, sink interface{}) bool {
	if interfaceIsNil(sink) {
		return false
	}
	if sameSink(route.Primary, sink) {
		return true
	}
	for _, mirror := range route.Mirrors {
		if sameSink(mirror.Sink, sink) {
			return true
		}
	}
	return false
}

// closeRouteResources retires the route's workers and closes owned sinks that
// are not retained by the selected route. Sink callbacks are never made while
// holding the gateway control lock.
func closeRouteResources(
	ctx context.Context,
	route RenderRouteConfig,
	slots []*mirrorSlot,
	retained RenderRouteConfig,
) {
	ctx = nonNilContext(ctx)
	for _, slot := range slots {
		if slot != nil {
			slot.retire()
		}
	}
	seen := make([]interface{}, 0, 1+len(route.Mirrors))
	closeOne := func(sink interface{}, owned bool) {
		if !owned || interfaceIsNil(sink) || routeContainsSink(retained, sink) {
			return
		}
		for _, prior := range seen {
			if sameSink(prior, sink) {
				return
			}
		}
		seen = append(seen, sink)
		switch typed := sink.(type) {
		case RenderOutputSink:
			_ = typed.Close(ctx)
		case RenderMirrorSink:
			_ = typed.Close(ctx)
		}
	}
	closeOne(route.Primary, route.PrimaryOwnership == SinkOwned)
	for _, mirror := range route.Mirrors {
		closeOne(mirror.Sink, mirror.Ownership == SinkOwned)
	}
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
