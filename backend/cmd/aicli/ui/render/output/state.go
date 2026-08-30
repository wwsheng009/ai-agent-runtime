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

// RouteChangePlan 是两阶段切换的 mid-flight 状态（9.2）。
type RouteChangePlan struct {
	Token         string
	OldRouteEpoch uint64
	NewRouteEpoch uint64 // Begin 时保留但尚未安装；abort 后不得复用
	OldTarget     TargetDescriptor
	NewTarget     TargetDescriptor
	Transition    ProjectionTransition
}

// ProjectionTransition 是 presenter 必须作为可回滚小事务应用的 ledgers
// （投影 + history journal），只含 detached value，不包含 writer/sink/lock。
type ProjectionTransition struct {
	OldProjectionTargetID string
	NewProjectionTargetID string
	NewRouteEpoch         uint64
	HistoryEpoch          uint64
}
