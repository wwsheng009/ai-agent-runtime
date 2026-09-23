package supervision

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// C2-5（改动 #5 / A6）：resume = 起 turn，必须先过宿主侧容量门控。
//
// 设计稿 §6.1 步骤 5 与 EC-H1/H9：resume 不是"把摘要塞进上下文"而已，它等同
// 于起一轮 turn，因此必须与普通 spawn 走同一套门控（MaxConcurrent / MaxDepth /
// shouldExposeSpawnSubagents）。超限时**不丢 wake**：进入有界 FIFO 排队，digest
// 展示排队位次；排队超时或队列深度越界则 escalate（critical + action_required），
// 而不是继续无限排队。挂起态（suspended）不占额度，由宿主的 probe 自行判定。
//
// 宿主拥有额度策略（并发槽位、深度、可见性），supervision 只提供：
//   - 一个非阻塞的探测接口（ResumeCapacityProbe）；
//   - 一个可测试的排队判定（ResumeQueuePolicy + resumeQueueEscalation）；
//   - 一条降级路径：释放 claim（保留 wake 行与 FIFO 位次）并按需升级通知。

// ResumeGate* 是门控拒绝原因的词表（写入升级通知的 reason 字段）。
const (
	// ResumeGateConcurrency：父 turn 的并发额度已满（MaxConcurrent）。
	ResumeGateConcurrency = "concurrency"
	// ResumeGateDepth：后代深度达到上限（MaxDepth）。
	ResumeGateDepth = "depth"
	// ResumeGateVisibility：宿主策略不允许在当前上下文暴露/启动该 turn
	// （shouldExposeSpawnSubagents 为假）。
	ResumeGateVisibility = "visibility"
	// ResumeGateQueueDepth：排队深度本身越界（有界 FIFO 的边界）。
	ResumeGateQueueDepth = "queue_depth"
	// ResumeGateQueueTimeout：排队等待超过策略时限。
	ResumeGateQueueTimeout = "queue_timeout"
)

// 有界 FIFO 的默认边界（宿主可通过 ResumeQueuePolicy 覆盖）。
const (
	// defaultResumeQueueTimeout：排队超过该时长仍未获得额度 ⇒ escalate。
	defaultResumeQueueTimeout = 5 * time.Minute
	// defaultResumeQueueMaxDepth：队列中待处理 wake 超过该数量 ⇒ escalate。
	defaultResumeQueueMaxDepth = 8
)

// ResumeCapacityRequest describes the resume a claimed wake is about to start.
// It carries only what the host gate needs; the host owns the actual limiter,
// depth counter and visibility policy.
type ResumeCapacityRequest struct {
	RootScopeID           string
	TargetParentSessionID string
	TargetParentTeamID    string
	// TurnID is the scheduling-time turn hint (ledger backfill remains the
	// authoritative identity).
	TurnID string
	// WakeIDs / WakeReasons identify the wakes this resume would consume.
	WakeIDs     []string
	WakeReasons []string
	// QueuedAt is when the oldest of these wakes was queued (its row's
	// created_at). It is the clock the queue-timeout policy measures.
	QueuedAt time.Time
	// QueueDepth is how many other wakes are already waiting unclaimed for the
	// same parent, i.e. the queue the resume would join when denied.
	QueueDepth int
}

// ResumeCapacityVerdict is the host's decision. A denial must be a capacity
// outcome ("not yet"), never a semantic error: the caller keeps the wake
// durable instead of dropping or resolving it.
type ResumeCapacityVerdict struct {
	// Allowed true lets the resume proceed immediately.
	Allowed bool
	// Reason names the denying gate (ResumeGate* vocabulary).
	Reason string
	// Detail is optional host text surfaced in the escalation notification.
	Detail string
	// Restricted 标记**静态策略**拒绝（深度 / 可见性）。它必须与容量拒绝区分：
	// 静态策略不会因为等待而变得可满足，据此排队只会得到 G12 自述的"永久排队
	// 变相死锁"。因此 Restricted 与 Allowed=true 同时成立——resume 照常投递
	// （纯汇报型回合不 spawn 任何东西），但 digest 会显式告知父回合"本回合不得
	// 再派发"。硬拒绝仍在派发面（scheduler 深度校验 / shouldExposeSpawnSubagents
	// 工具面门控），这里只负责**不静默**。
	Restricted bool
}

// ResumeCapacityProbe is the host-owned gate for "resume = 起 turn" (A6).
// Implementations must be non-blocking: the probe is called on the parent's
// runnable transition path, and a blocking probe would stall the drain loop.
//
// A probe error is treated as "allowed" (fail-open): an unreadable limiter must
// never wedge supervision. Callers that need the opposite must return an
// explicit denial verdict instead of an error.
type ResumeCapacityProbe interface {
	CanResume(ctx context.Context, req ResumeCapacityRequest) (ResumeCapacityVerdict, error)
}

// ResumeCapacityProbeFunc adapts a plain function to ResumeCapacityProbe.
type ResumeCapacityProbeFunc func(ctx context.Context, req ResumeCapacityRequest) (ResumeCapacityVerdict, error)

// CanResume implements ResumeCapacityProbe.
func (f ResumeCapacityProbeFunc) CanResume(ctx context.Context, req ResumeCapacityRequest) (ResumeCapacityVerdict, error) {
	return f(ctx, req)
}

// SubagentCapacityView 是宿主对"进程级子代理准入"的瞬时只读视图。supervision
// 只消费这个中立视图，不依赖 agent 包：宿主（CLI/API）各自把
// agent.SubagentConcurrencyLimiter 的读数喂进来。
type SubagentCapacityView struct {
	// Limit 是进程级并发上限；<= 0 表示未配置上限（等价无限）。
	Limit int
	// InFlight 是当前已占用的槽位数。
	InFlight int
}

// NewSubagentCapacityProbe 把宿主的瞬时容量视图适配成 resume 门控探测（A6）。
//
// 语义边界（设计红线）：
//   - 只表达**瞬时容量**：槽位占满（Limit > 0 且 InFlight >= Limit）时拒绝本次
//     resume；额度释放后同一条 wake 会被重新投递，位次不变，绝不丢 wake。
//   - 静态策略不在此拒绝：delegationPolicy / shouldExposeSpawnSubagents 为假时
//     **放行**。纯汇报型 resume 不需要 spawn 子任务，按静态策略拒绝会让它永久
//     排队并不断升级（行为回归）；可见性由宿主自己的 spawn 路径把关。
//   - view 为 nil 或返回 Limit <= 0 ⇒ 一律放行（未配置上限 = 旧行为/回滚开关）。
//   - view 必须是纯内存读取：探测在父会话 runnable 转换路径上被调用，不得做
//     I/O 或阻塞（阻塞探测会卡死 drain 循环）。
func NewSubagentCapacityProbe(view func() SubagentCapacityView) ResumeCapacityProbe {
	if view == nil {
		return nil
	}
	return ResumeCapacityProbeFunc(func(_ context.Context, _ ResumeCapacityRequest) (ResumeCapacityVerdict, error) {
		v := view()
		if v.Limit <= 0 || v.InFlight < v.Limit {
			return ResumeCapacityVerdict{Allowed: true}, nil
		}
		return ResumeCapacityVerdict{
			Allowed: false,
			Reason:  ResumeGateConcurrency,
			Detail:  fmt.Sprintf("子代理槽位已满 %d/%d", v.InFlight, v.Limit),
		}, nil
	})
}

// ResumePolicyView 是宿主对"该父会话这一回合能否再派发子任务"的静态策略视图
// （A6 的深度 / 可见性门控）。与 SubagentCapacityView 的区别在语义：容量会随
// 子任务终态释放，静态策略不会。
type ResumePolicyView struct {
	// Restricted 为真 ⇒ 本次 resume 的回合不得再派发子任务。
	Restricted bool
	// Reason 取 ResumeGate* 词表（ResumeGateDepth / ResumeGateVisibility）。
	Reason string
	// Detail 是可选的宿主说明，直接出现在 digest 的 resume_gate 行里。
	Detail string
}

// NewResumePolicyGate 把宿主的静态派发策略适配成 resume 探测（A6 的深度 /
// 可见性门控）。
//
// 它**永不 defer**：静态策略不可恢复，拒绝投递只会让纯汇报型 resume 永久排队
// 并不断升级（行为回归）。命中时返回 Allowed=true + Restricted=true，由投递
// 路径把限制写进 digest；未命中即放行。view 为 nil ⇒ nil 探测（未接线 = 旧
// 行为）。view 跑在父会话 runnable 转换路径上：只允许宿主自己的只读查询，
// 不得起 turn、不得阻塞等待，查询失败一律 fail-open。
func NewResumePolicyGate(view func(ctx context.Context, req ResumeCapacityRequest) ResumePolicyView) ResumeCapacityProbe {
	if view == nil {
		return nil
	}
	return ResumeCapacityProbeFunc(func(ctx context.Context, req ResumeCapacityRequest) (ResumeCapacityVerdict, error) {
		v := view(ctx, req)
		if !v.Restricted {
			return ResumeCapacityVerdict{Allowed: true}, nil
		}
		return ResumeCapacityVerdict{
			Allowed:    true,
			Restricted: true,
			Reason:     v.Reason,
			Detail:     v.Detail,
		}, nil
	})
}

// CombineResumeProbes 按 A6 的优先级合并多个探测：**defer 优先于 restrict，
// restrict 优先于放行**。理由是两者的可恢复性不同——容量是"现在不行"，等待即可
// 满足，所以 wake 必须保持排队并保留 FIFO 位次；静态策略是"这一回合不能派发"，
// 投递仍然安全，因此只带限制、不排队。探测报错一律 fail-open（继续看下一个）：
// 限流器不可读不得卡死 supervision。全部探测为 nil ⇒ 返回 nil（旧行为）。
func CombineResumeProbes(probes ...ResumeCapacityProbe) ResumeCapacityProbe {
	active := make([]ResumeCapacityProbe, 0, len(probes))
	for _, p := range probes {
		if p != nil {
			active = append(active, p)
		}
	}
	if len(active) == 0 {
		return nil
	}
	return ResumeCapacityProbeFunc(func(ctx context.Context, req ResumeCapacityRequest) (ResumeCapacityVerdict, error) {
		merged := ResumeCapacityVerdict{Allowed: true}
		for _, p := range active {
			verdict, err := p.CanResume(ctx, req)
			if err != nil {
				continue
			}
			if !verdict.Allowed {
				return verdict, nil
			}
			if verdict.Restricted {
				merged = verdict
			}
		}
		return merged, nil
	})
}

// ResumeQueuePolicy bounds the FIFO (A6 有界 FIFO). Zero values select the
// package defaults, which keeps the zero-value consumer backward compatible.
type ResumeQueuePolicy struct {
	// Timeout is how long a wake may wait queued before escalating.
	Timeout time.Duration
	// MaxDepth is the queue depth above which a deferral escalates instead of
	// queueing further.
	MaxDepth int
}

// EffectiveTimeout returns the configured timeout or the package default.
func (p ResumeQueuePolicy) EffectiveTimeout() time.Duration {
	if p.Timeout > 0 {
		return p.Timeout
	}
	return defaultResumeQueueTimeout
}

// EffectiveMaxDepth returns the configured depth bound or the package default.
func (p ResumeQueuePolicy) EffectiveMaxDepth() int {
	if p.MaxDepth > 0 {
		return p.MaxDepth
	}
	return defaultResumeQueueMaxDepth
}

// ResumeQueueState is the durable description of one deferred resume. It is
// what the host hook surfaces (排队位次) and what the escalation notification
// records.
type ResumeQueueState struct {
	RootScopeID           string
	TargetParentSessionID string
	TargetParentTeamID    string
	TurnID                string
	WakeID                string
	WakeReason            string
	// QueuedAt is when the deferred wake was originally queued.
	QueuedAt time.Time
	// Position is the 1-based FIFO position after the deferral (0 = the wake
	// is no longer queued).
	Position int
	// Depth is the queue length after the deferral.
	Depth int
	// Gate / GateDetail carry the denying gate from the verdict.
	Gate       string
	GateDetail string
	// Waited is how long the wake has been queued at deferral time.
	Waited time.Duration
}

// QueueLine renders the queue-position line the digest shows the parent
// (A6: digest 显示排队位次).
func (s ResumeQueueState) QueueLine() string {
	if s.Position <= 0 {
		return ""
	}
	line := fmt.Sprintf("[supervision] resume 已排队：位次 %d/%d", s.Position, s.Depth)
	if s.Gate != "" {
		line += "，门控 " + s.Gate
	}
	if s.Waited > 0 {
		line += "，已等待 " + s.Waited.Round(time.Second).String()
	}
	return line
}

// ResumeQueueEscalation reports whether a deferred resume must escalate now
// (critical + action_required, EC-H9) and the machine-readable cause. It is a
// pure function so the bounded-FIFO boundary is testable without a store.
//
// Depth wins over timeout: an over-deep queue escalates immediately, because
// waiting longer cannot shrink a queue that is already past its bound.
func ResumeQueueEscalation(state ResumeQueueState, policy ResumeQueuePolicy, now time.Time) (string, bool) {
	if state.Depth > policy.EffectiveMaxDepth() {
		return ResumeGateQueueDepth, true
	}
	if state.QueuedAt.IsZero() {
		return "", false
	}
	if now.Sub(state.QueuedAt) >= policy.EffectiveTimeout() {
		return ResumeGateQueueTimeout, true
	}
	return "", false
}

// resumeQueueNotification builds the escalation notification for a deferred
// resume. The identity (root scope + subject + subject_version + event_type) is
// stable per parent session on purpose: repeated deferrals of the same stuck
// queue coalesce into one durable critical row (UpsertNotification refreshes
// it) instead of spamming the inbox once per drain.
func resumeQueueNotification(state ResumeQueueState, cause string, now time.Time) Notification {
	reason := strings.TrimSpace(state.GateDetail)
	if reason == "" {
		reason = strings.TrimSpace(cause)
	}
	action := fmt.Sprintf("resume 排队位次 %d/%d", state.Position, state.Depth)
	if state.Gate != "" {
		action += "，门控 " + state.Gate
	}
	action += "；请释放额度或显式处理该 wake"
	return Notification{
		RootScopeID:           strings.TrimSpace(state.RootScopeID),
		TargetParentSessionID: strings.TrimSpace(state.TargetParentSessionID),
		TargetParentTeamID:    strings.TrimSpace(state.TargetParentTeamID),
		SubjectKind:           SubjectAgentSession,
		SubjectID:             strings.TrimSpace(state.TargetParentSessionID),
		SubjectVersion:        1,
		EventType:             "resume.queue.escalated",
		Severity:              SeverityCritical,
		Reason:                reason,
		RecommendedAction:     action,
		DeliveryState:         DeliveryPending,
		DecisionState:         DecisionUnacknowledged,
		ResolutionState:       ResolutionUnresolved,
		CreatedAt:             now,
		UpdatedAt:             now,
	}
}

// earliestWakeCreatedAt returns the oldest queued time among the wakes, which
// is the clock the queue-timeout policy measures.
func earliestWakeCreatedAt(wakes []WakePending) time.Time {
	var earliest time.Time
	for _, w := range wakes {
		if w.CreatedAt.IsZero() {
			continue
		}
		if earliest.IsZero() || w.CreatedAt.Before(earliest) {
			earliest = w.CreatedAt
		}
	}
	return earliest
}
