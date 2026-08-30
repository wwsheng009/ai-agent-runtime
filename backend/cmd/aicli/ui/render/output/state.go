package output

// GatewayLifecycleState 描述 gateway 的 lifecycle：Open 为唯一接受业务
// 输入的状态；Reconfiguring 是 route/sink 变更的过渡状态；Closing 表示
// gateway 已接受 Close/Drain 但仍在排空或关闭资源；Closed 是终态；
// Abandoned 表示 gateway 已经被放弃（panic/sink fault 后自动固定）。
type GatewayLifecycleState string

const (
	GatewayOpen          GatewayLifecycleState = "open"
	GatewayReconfiguring GatewayLifecycleState = "reconfiguring"
	GatewayClosing       GatewayLifecycleState = "closing"
	GatewayClosed        GatewayLifecycleState = "closed"
	GatewayAbandoned     GatewayLifecycleState = "abandoned"
)

// canClose 判断状态是否可接受 Close/Drain 请求。
func canClose(s GatewayLifecycleState) bool {
	return s == GatewayOpen || s == GatewayReconfiguring
}

// canBeginReconfigure 判断状态是否可接受 BeginReconfigure 请求。
func canBeginReconfigure(s GatewayLifecycleState) bool {
	return s == GatewayOpen
}

// canSubmit 判断状态是否可接受业务 Submit（Open 为唯一接受输入的状态）。
func canSubmit(s GatewayLifecycleState) bool {
	return s == GatewayOpen
}

// canSnapshot 判断状态是否允许快照/观察（Closed 后仍允许读取最后状态）。
func canSnapshot(s GatewayLifecycleState) bool {
	switch s {
	case GatewayOpen, GatewayReconfiguring, GatewayClosing, GatewayClosed, GatewayAbandoned:
		return true
	default:
		return false
	}
}

// switchState 校验 gateway 状态机迁移（8.2/9.2）：
//
//	open -> reconfiguring -> open
//	open -> closing -> closed
//	reconfiguring -> closing -> closed
//	open/reconfiguring/closing -> abandoned（panic/sink fault 自动固定）
//
// 非法迁移返回 DeliveryErrorInvalid；相同状态返回 nil。
func switchState(from, to GatewayLifecycleState) error {
	if from == to {
		return nil
	}
	legal := func(a, b GatewayLifecycleState) bool {
		switch a {
		case GatewayOpen:
			return b == GatewayReconfiguring || b == GatewayClosing || b == GatewayAbandoned
		case GatewayReconfiguring:
			return b == GatewayOpen || b == GatewayClosing || b == GatewayAbandoned
		case GatewayClosing:
			return b == GatewayClosed || b == GatewayAbandoned
		default:
			return false // closed/abandoned 是终态
		}
	}
	if !legal(from, to) {
		return NewClassifiedError(DeliveryErrorInvalid,
			"illegal gateway state transition: "+string(from)+" -> "+string(to))
	}
	return nil
}

// SinkLifecycleState 是 sink 安装状态机（见 sink.go）。此处只放 gateway 状态。
// 注意：sink.go 已定义 SinkLifecycleState/SinkSnapshot，这里不再重复。

// GatewaySnapshot 是 gateway 的观测快照；所有字段 detached。生产代码不得
// 依赖快照返回值在下次调用后仍然有效。
type GatewaySnapshot struct {
	SessionID         string
	State             GatewayLifecycleState
	RouteEpoch        uint64
	BindingGeneration uint64
	Sequence          uint64 // 最后盖章的 sequence
	RecordedCount     uint64 // 已封存 delivery records 数量
	ObserverDrops     uint64
	AbandonedReason   string
}

// reconfigureDisposition 是两阶段 barrier 的 outcome disposition（9.2）。
type reconfigureDisposition int

const (
	reconfigureInstallNew reconfigureDisposition = iota
	reconfigureRollback
)

// RouteChangePlan 是两阶段切换的 mid-flight 状态（9.2）。
// Token 单调不复用；NewRouteEpoch 在 Begin 时预留但尚未安装，abort 后不得
// 复用。reconfigureCutoffSequence 捕获进入 Reconfiguring 时 lastAllocated。
type RouteChangePlan struct {
	Token                     string
	OldRouteEpoch             uint64
	NewRouteEpoch             uint64 // Begin 时预留但尚未安装；abort 后不得复用
	OldTarget                 TargetDescriptor
	NewTarget                 TargetDescriptor
	ReconfigureCutoffSequence uint64 // accepted batch 的 Sequence 上限（9.2 第 3 条）
	Transition                ProjectionTransition
}

// ContinuityDecision 描述 history/target 连续性判定（9.4）。
type ContinuityDecision string

const (
	ContinuityRetain    ContinuityDecision = "retain"
	ContinuityNewDomain ContinuityDecision = "new_domain"
	ContinuityUnproven  ContinuityDecision = "unproven"
)

// ProjectionAction 是 screen/history 的动作（9.4）。
type ProjectionAction string

const (
	ProjectionKeep       ProjectionAction = "keep"
	ProjectionInvalidate ProjectionAction = "invalidate"
	ProjectionRebuild    ProjectionAction = "rebuild"
)

// HistoryBootstrapStrategy 是 source-backed history bootstrap 策略（9.4）。
type HistoryBootstrapStrategy string

const (
	BootstrapNone            HistoryBootstrapStrategy = "none"
	BootstrapCurrentViewport HistoryBootstrapStrategy = "current_viewport_only"
	BootstrapReplayStable    HistoryBootstrapStrategy = "replay_stable_history"
)

// ProjectionTransition 是 presenter 必须作为可回滚小事务应用的 ledgers
// （投影 + history journal），只含 detached value，不包含 writer/sink/lock。
type ProjectionTransition struct {
	OldRouteEpoch    uint64
	NewRouteEpoch    uint64
	OldTargetID      string
	OldTargetClass   TargetClass
	NewTargetID      string
	NewTargetClass   TargetClass
	OldHistory       *HistoryDeliveryDomain
	NewHistory       *HistoryDeliveryDomain
	Continuity       ContinuityDecision
	ScreenAction     ProjectionAction
	HistoryAction    ProjectionAction
	Bootstrap        HistoryBootstrapStrategy
	ContinuityReason string
}

// validateTransition 校验 9.4 规则 9：互相矛盾的 action/bootstrap 组合在
// BeginReconfigure 阶段拒绝，不能留给 presenter 猜测。
func validateTransition(t ProjectionTransition) error {
	if t.ScreenAction != ProjectionKeep && t.ScreenAction != ProjectionInvalidate &&
		t.ScreenAction != ProjectionRebuild {
		return NewClassifiedError(DeliveryErrorInvalid,
			"invalid screen action "+string(t.ScreenAction))
	}
	if t.HistoryAction != ProjectionKeep && t.HistoryAction != ProjectionInvalidate &&
		t.HistoryAction != ProjectionRebuild {
		return NewClassifiedError(DeliveryErrorInvalid,
			"invalid history action "+string(t.HistoryAction))
	}
	// ProjectionKeep 或无需 history 重建 → BootstrapNone（9.4 规则 9）。
	if t.HistoryAction == ProjectionKeep && t.Bootstrap != BootstrapNone {
		return NewClassifiedError(DeliveryErrorInvalid,
			"history keep requires bootstrap none")
	}
	// ProjectionRebuild 且涉及 history → 必须显式选择 bootstrap 策略。
	if t.HistoryAction == ProjectionRebuild &&
		t.Bootstrap != BootstrapCurrentViewport && t.Bootstrap != BootstrapReplayStable {
		return NewClassifiedError(DeliveryErrorInvalid,
			"history rebuild requires explicit bootstrap strategy")
	}
	// ContinuityRetain 需要 history action keep。
	if t.Continuity == ContinuityRetain && t.HistoryAction != ProjectionKeep {
		return NewClassifiedError(DeliveryErrorInvalid,
			"continuity retain requires history keep")
	}
	return nil
}
