// Package schedulehost 提供"调度领导权"的通用运行时（v1.1 联盟式调度，S1）。
//
// 背景与设计：docs/automation-tasks/mesh-scheduler-federation-design.md §3.2 / §12.2.1。
// 调度面不再是"某个专属守护进程"，而是一个**可抢占的 mesh 角色**：任何具备
// scheduler 能力位的进程（cmd/scheduled、runtime-server，未来的 aicli）用同一把
// mesh 租约（LeasePurpose/LeaseKey）竞争领导权——持租约者驱动 schedule.Runtime
// 的定时器，未持者进入 standby；持有者退出/失租后，任意待命节点在 TTL 内接管。
//
// 职责边界：
//   - 本包只负责"谁持定时器"：租约竞争、状态机、Runtime 的起停（每届任期一个
//     新的 Runtime，因为 Runtime.Stop 不可重启）；
//   - 投递路由与执行体仍由调用方通过 schedule.Runtime 的既有接缝注入
//     （Deliverer / Executor / Arbiter），本包不改变其语义。
package schedulehost

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/mesh"
	"github.com/wwsheng009/ai-agent-runtime/internal/schedule"
)

const (
	// LeasePurpose / LeaseKey 是调度领导权租约的稳定标识（与 daemon 历史一致）。
	LeasePurpose = "schedule-scheduler"
	LeaseKey     = "global"

	// DefaultLeaseTTL 是 driver 租约的显式 TTL：mesh 默认是 90s，driver 用 30s
	// 换取更快的接管（M3）；续租周期为 TTL/3，留三次失败预算。
	DefaultLeaseTTL = 30 * time.Second

	// CapabilityScheduler 是 mesh 节点档案上的"可参与调度领导权"能力位：
	// aicli-mesh 视图据此回答"当前有没有调度者在岗"（在岗可见性，§4.1）。
	CapabilityScheduler = "scheduler"

	// 角色名（driver.participants 词表；与 internal/config 的 ScheduleRole* 对齐）。
	RoleDaemon        = "daemon"
	RoleRuntimeServer = "runtime-server"
	RoleAICLI         = "aicli"
)

// DegradedPolicy 选择 mesh 不可用时的行为。
type DegradedPolicy string

const (
	// DegradedStandby 是 fail-closed：无法判定唯一性时待命，不驱动（宿主默认）。
	DegradedStandby DegradedPolicy = "standby"
	// DegradedStandalone 单机自跑（仅 daemon 默认；历史行为，带警告）。
	DegradedStandalone DegradedPolicy = "standalone"
)

// State 是调度者状态机（§12.2.1）。
type State string

const (
	StateCandidate State = "candidate"
	StateStandby   State = "standby"
	StateLeader    State = "leader"
	StateDraining  State = "draining"
	StateQuiesced  State = "quiesced"
	StateDegraded  State = "degraded"
	// StateIncompatible：store schema 超出本二进制支持档——不参与/退出领导权
	// （S3 §4.2 版本握手），等待合格节点接管。
	StateIncompatible State = "incompatible"
)

// DriverIdentity 标识一个调度者候选/在岗者。
type DriverIdentity struct {
	NodeID   string
	Kind     string
	Priority int
}

// LeaderOptions 是"以调度者身份运行"的全部依赖（与 daemon 现有装配一一对应）。
type LeaderOptions struct {
	// Store / Service / Deliverer 是 schedule.Runtime 的必需依赖。
	Store     schedule.Store
	Service   *schedule.Service
	Deliverer schedule.Deliverer
	// Executor / Arbiter 是可选执行接缝（embedded / headless）。注意 headless
	// 执行由 Runtime 通过类型断言发现（Executor 同时实现 HeadlessExecutor），
	// 因此没有单独的 Headless 字段。
	Executor schedule.SessionExecutor
	Arbiter  schedule.LeaseArbiter
	// Mode 选择执行模式（delegate | embedded | auto），透传给 Runtime。
	Mode string
	// MaxAttempts / FallbackToMailbox 透传给 Runtime。
	MaxAttempts       int
	FallbackToMailbox bool
	// MisfireGrace 透传给 Runtime（S4b，§4.4）：一次性任务补发宽限窗口。
	MisfireGrace time.Duration
	// Identity 是租约 owner 与状态展示身份（NodeID 缺省 schedule-driver-<pid>）。
	Identity DriverIdentity
	// LeaseTTL 是租约寿命（<=0 → DefaultLeaseTTL）。
	LeaseTTL time.Duration
	// StandbyRetry 是待命轮询间隔（<=0 → LeaseTTL/3，限幅 [250ms, 10s]）。
	StandbyRetry time.Duration
	// Degraded 是 mesh 不可用时的策略（空值 → DegradedStandby，fail-closed）。
	Degraded DegradedPolicy
	// Paths 覆盖 mesh 布局（nil → mesh.ResolvePaths()；测试与多根部署用）。
	Paths *mesh.Paths
	// StoreCompatCheck 是"本进程能否理解当前 store"的自检（§4.2 版本握手）：
	// 选主前每次循环、持租约期间每次续租前运行；返回错误时不参与/退出领导权
	// （释放租约，让更新的节点接管），状态置 StateIncompatible；恢复后自动回到
	// 候选循环。nil 表示不做检查（跟随 Store 打开时的 fail-closed 行为）。
	StoreCompatCheck func() error
	// DeferToHigherPriority 开启"优先级让位 v2"（S5）：存在比本角色优先级更高
	// 的在岗调度者（daemon > runtime-server > aicli）时，本进程不抢租约、且在
	// 持租约期间发现更高优先级者上线即主动释放。交互宿主（aicli）opt-in 时置
	// true，避免与常驻调度者争抢。
	DeferToHigherPriority bool
	// CreatorAffinity 开启"创建者亲和让位"（S6）：活动任务集合由调用方按最新
	// 快照注入（nil = 关闭）。开启后，当最近创建的活动任务的创建者会话宿主在
	// 另一个存活调度者节点上时，本进程让位——多 aicli 并存时创建作业的 aicli
	// 优先持租（投递就近 L1 直达），旁观者 aicli 回到 standby。
	// 只对 DeferToHigherPriority=true 的角色生效（daemon/runtime-server 永不让位）。
	CreatorAffinity CreatorAffinitySource
	// Logf 接收状态迁移与降级日志（nil → 丢弃）。
	Logf func(format string, args ...any)
}

// LeaderStatus 是 Status() 的一致性快照（/status、CLI、测试使用）。
type LeaderStatus struct {
	State    State  `json:"state"`
	NodeID   string `json:"node_id"`
	Kind     string `json:"kind"`
	Priority int    `json:"priority,omitempty"`
	// Epoch 是本届任期的 fencing token（M2）：随接管递增，可据此判定"我是
	// 否仍是最新任期"（legacy 租约 → 0 = 不可判定）。
	Epoch int64 `json:"epoch,omitempty"`
	// Takeovers 是从"其他节点持有（或残留）的租约"上位的累计次数（S4b 观测：
	// 首次空租约上位不计入；读取与抢占间的竞态只影响观测精度）。
	Takeovers      int                      `json:"takeovers"`
	Driving        bool                     `json:"driving"`
	LeaseExpiresAt time.Time                `json:"lease_expires_at,omitempty"`
	LastError      string                   `json:"last_error,omitempty"`
	Metrics        schedule.MetricsSnapshot `json:"metrics"`
}

// Leader 持有 mesh 租约并驱动 schedule.Runtime；失租/退出时停止驱动并回到 standby。
type Leader struct {
	opts  LeaderOptions
	paths mesh.Paths
	owner mesh.LeaseOwner
	ttl   time.Duration
	retry time.Duration

	mu           sync.Mutex
	state        State
	rt           *schedule.Runtime
	driving      bool
	leaseExpires time.Time
	lastErr      string
	lastMetrics  schedule.MetricsSnapshot
	takeovers    int
	epoch        int64
	started      bool
	stopped      bool
	runCtx       context.Context
	cancel       context.CancelFunc
	doneCh       chan struct{}
	doneOnce     sync.Once
}

// NewLeader 构造调度者；必需依赖缺失直接报错（fail-closed，调用方降级为不参与）。
func NewLeader(opts LeaderOptions) (*Leader, error) {
	if opts.Store == nil {
		return nil, fmt.Errorf("schedulehost: store is required")
	}
	if opts.Deliverer == nil {
		return nil, fmt.Errorf("schedulehost: deliverer is required")
	}
	if opts.LeaseTTL <= 0 {
		opts.LeaseTTL = DefaultLeaseTTL
	}
	// mesh 租约以秒为存储粒度（TTLSec）：小于 1s 会被写成 0 并回落 90s 默认，
	// 出现"配置越小、租约越长"的反直觉陷阱。统一抬到 1s 下限。
	if opts.LeaseTTL < time.Second {
		opts.LeaseTTL = time.Second
	}
	retry := opts.StandbyRetry
	if retry <= 0 {
		retry = opts.LeaseTTL / 3
		if retry < 250*time.Millisecond {
			retry = 250 * time.Millisecond
		}
		if retry > 10*time.Second {
			retry = 10 * time.Second
		}
	}
	if opts.Degraded == "" {
		opts.Degraded = DegradedStandby
	}
	if opts.Logf == nil {
		opts.Logf = func(string, ...any) {}
	}

	paths := mesh.ResolvePaths()
	if opts.Paths != nil {
		paths = *opts.Paths
	}
	nodeID := strings.TrimSpace(opts.Identity.NodeID)
	if nodeID == "" {
		nodeID = fmt.Sprintf("schedule-driver-%d", os.Getpid())
	}
	opts.Identity.NodeID = nodeID
	opts.Identity.Kind = strings.TrimSpace(opts.Identity.Kind)

	return &Leader{
		opts:  opts,
		paths: paths,
		owner: mesh.LeaseOwner{NodeID: nodeID, PID: os.Getpid()},
		ttl:   opts.LeaseTTL,
		retry: retry,
	}, nil
}

// Start 启动监督循环（非阻塞）：候选 → 竞争租约 → 持租约驱动 / 待命轮询。
// ctx 取消即进入 draining 并退出（Stop 与 ctx 二选一即可）。
func (l *Leader) Start(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	l.mu.Lock()
	if l.started || l.stopped {
		l.mu.Unlock()
		return
	}
	l.started = true
	runCtx, cancel := context.WithCancel(ctx)
	l.runCtx = runCtx
	l.cancel = cancel
	l.doneCh = make(chan struct{})
	l.mu.Unlock()

	l.setState(StateCandidate, "start")
	go l.supervise(runCtx)
}

// Stop 请求退出：先停驱动、后释放租约（顺序不可颠倒），并等待监督循环结束。
// ctx 超时只影响等待时长，不影响"绝不带租约退出"的不变量（由 supervise 保证）。
func (l *Leader) Stop(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	l.mu.Lock()
	if !l.started || l.stopped {
		l.stopped = true
		done := l.doneCh
		l.mu.Unlock()
		if done != nil {
			select {
			case <-done:
			default:
			}
		}
		return
	}
	l.stopped = true
	cancel := l.cancel
	done := l.doneCh
	l.mu.Unlock()

	l.setState(StateDraining, "stop requested")
	if cancel != nil {
		cancel()
	}
	if done == nil {
		return
	}
	select {
	case <-done:
	case <-ctx.Done():
		l.logf("schedulehost: stop wait timed out (%v); supervisor still draining", ctx.Err())
	}
}

// Status 返回当前状态快照（并发安全）。
func (l *Leader) Status() LeaderStatus {
	l.mu.Lock()
	defer l.mu.Unlock()
	metrics := l.lastMetrics
	if l.rt != nil {
		metrics = l.rt.Metrics()
	}
	return LeaderStatus{
		State:          l.state,
		NodeID:         l.owner.NodeID,
		Kind:           l.opts.Identity.Kind,
		Priority:       l.opts.Identity.Priority,
		Epoch:          l.epoch,
		Takeovers:      l.takeovers,
		Driving:        l.driving,
		LeaseExpiresAt: l.leaseExpires,
		LastError:      l.lastErr,
		Metrics:        metrics,
	}
}

// supervise 是状态机主循环：candidate →（leader | standby | degraded）→ …
func (l *Leader) supervise(ctx context.Context) {
	defer func() {
		l.stopDriving("supervisor exit")
		l.releaseLease()
		l.setState(StateQuiesced, "exited")
		l.doneOnce.Do(func() { close(l.doneCh) })
	}()

	for {
		if ctx.Err() != nil {
			return
		}
		// S3 版本握手（§4.2）：不兼容时绝不参与领导权；若在任上则先让位。
		if err := l.storeCompatErr(); err != nil {
			l.stopDriving("store incompatible")
			l.releaseLease()
			l.recordErr("store compatibility check failed: " + err.Error())
			l.setState(StateIncompatible, "store incompatible; not participating")
			if !l.sleep(ctx) {
				return
			}
			continue
		}
		// S5：优先级让位 v2——更高优先级的在岗调度者存在时不抢租约。
		if l.deferToHigherPriorityBlocked() {
			l.stopDriving("higher-priority scheduler online")
			l.releaseLease()
			l.setState(StateStandby, "higher-priority scheduler online; deferring")
			if !l.sleep(ctx) {
				return
			}
			continue
		}
		// S6：创建者亲和——活动任务创建者的会话宿主在另一存活调度者上时让位
		// （仅 DeferToHigherPriority 角色生效；daemon/runtime-server 不受影响）。
		if l.creatorAffinityBlocked() {
			l.stopDriving("creator-affinity scheduler online")
			l.releaseLease()
			l.setState(StateStandby, "creator session hosted by another scheduler; deferring")
			if !l.sleep(ctx) {
				return
			}
			continue
		}
		foreignBefore := l.foreignLeaseHeld()
		outcome := mesh.Acquire(l.paths, LeasePurpose, LeaseKey, l.owner, mesh.AcquireOptions{TTL: l.ttl})
		switch {
		case outcome.Acquired:
			if foreignBefore {
				l.countTakeover()
			}
			l.noteAcquired(outcome.Epoch)
			l.ensureDriving()
			l.setState(StateLeader, "lease acquired")
			lost := l.startRenewal(ctx)
			select {
			case <-ctx.Done():
				l.stopDriving("shutdown")
				return
			case <-lost:
				l.stopDriving("lease lost")
				l.releaseLease()
				if ctx.Err() != nil {
					return
				}
				l.setState(StateStandby, "lease lost; standing by")
			}

		case outcome.Degraded:
			if l.degradedPolicy() == DegradedStandalone {
				l.ensureDriving()
				l.setState(StateDegraded, "mesh unavailable; running standalone without mutual exclusion")
			} else {
				l.stopDriving("mesh unavailable")
				l.setState(StateStandby, "mesh unavailable; fail-closed standby")
			}
			if !l.sleep(ctx) {
				return
			}

		default:
			// held / contended / unreadable：不接管，待命轮询。
			l.stopDriving("not leader")
			l.recordErr("lease not acquired: " + outcome.Reason)
			l.setState(StateStandby, "lease held by another instance ("+outcome.Reason+")")
			if !l.sleep(ctx) {
				return
			}
		}
	}
}

// ensureDriving 保证本届任期有一个运行中的 Runtime（幂等）。
func (l *Leader) ensureDriving() {
	l.mu.Lock()
	if l.rt != nil {
		l.mu.Unlock()
		return
	}
	rt := schedule.NewRuntime(schedule.RuntimeOptions{
		Store:             l.opts.Store,
		Service:           l.opts.Service,
		Deliverer:         l.opts.Deliverer,
		MaxAttempts:       l.opts.MaxAttempts,
		FallbackToMailbox: l.opts.FallbackToMailbox,
		Mode:              l.opts.Mode,
		Executor:          l.opts.Executor,
		Arbiter:           l.opts.Arbiter,
		// S4（§4.3）：claim 持有者 = 本节点 id（跨进程唯一）。
		InstanceID: l.owner.NodeID,
		// S4b（§4.4）：一次性任务补发宽限窗口。
		MisfireGrace: l.opts.MisfireGrace,
		Logf:         l.opts.Logf,
	})
	l.rt = rt
	l.driving = true
	runCtx := l.runCtx
	l.mu.Unlock()
	// S2 修复：控制面创建/变更任务后唤醒本任期驱动（空闲期新任务即时可见）。
	if l.opts.Service != nil {
		l.opts.Service.SetDriverWaker(rt.RequestDrive)
	}
	if runCtx == nil {
		runCtx = context.Background()
	}
	rt.Start(runCtx)
}

// stopDriving 排空并停止当前 Runtime（幂等）；Runtime.Stop 会等待进行中的 drive 结束。
func (l *Leader) stopDriving(reason string) {
	l.mu.Lock()
	rt := l.rt
	l.rt = nil
	wasDriving := l.driving
	l.driving = false
	l.mu.Unlock()
	if rt == nil {
		return
	}
	if l.opts.Service != nil {
		// 先清唤醒（避免对已停止的 Runtime 反复触发），再停止驱动。
		l.opts.Service.SetDriverWaker(nil)
	}
	rt.Stop()
	l.mu.Lock()
	l.lastMetrics = rt.Metrics()
	l.mu.Unlock()
	if wasDriving {
		l.logf("schedulehost: runtime stopped (%s)", reason)
	}
}

// startRenewal 启动本届任期的续租循环，返回"确认失租"信号通道（至多一次）。
//
// 自降级兜底：续租连续失败（非 Lost，例如短暂竞争/写失败）超过一个 TTL 仍
// 未成功，则按失租处理——宁可短暂停摆，也不带着过期租约继续驱动。
func (l *Leader) startRenewal(ctx context.Context) <-chan struct{} {
	lost := make(chan struct{}, 1)
	signalLost := func() {
		select {
		case lost <- struct{}{}:
		default:
		}
	}
	every := l.ttl / 3
	if every < 20*time.Millisecond {
		every = 20 * time.Millisecond
	}
	go func() {
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		lastRenewed := time.Now()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// S3：持租约期间持续自检——库被更新的二进制升级到更高 schema 时，
				// 立即释放租约让位（等价失租路径），而不是带着旧认知继续驱动。
				if err := l.storeCompatErr(); err != nil {
					l.recordErr("store compatibility lost: " + err.Error())
					l.logf("schedulehost: store compatibility check failed (%v); releasing lease for a compatible node", err)
					signalLost()
					return
				}
				// S5：持租约期间更高优先级者上线 → 主动让位（不等 TTL）。
				if l.deferToHigherPriorityBlocked() {
					l.logf("schedulehost: higher-priority scheduler came online; releasing lease")
					signalLost()
					return
				}
				// S6：持租约期间活动任务创建者会话被另一存活调度者宿主 → 让位。
				if l.creatorAffinityBlocked() {
					l.logf("schedulehost: creator session hosted by another scheduler; releasing lease")
					signalLost()
					return
				}
				res := mesh.Renew(l.paths, LeasePurpose, LeaseKey, l.owner, mesh.NowUTC(), l.ttl)
				if res.Lost {
					signalLost()
					return
				}
				if res.Renewed {
					// M2（fencing）：续约文件里的 epoch 必须仍是本任期——不一致
					// 说明租约已被接管（同一 node id 的极端情形），立即让位。
					if l.leaseEpochMismatch(res.Lease.Epoch) {
						l.recordErr("lease epoch changed under us")
						l.logf("schedulehost: lease epoch changed (%d → %d); standing down",
							l.currentEpoch(), res.Lease.Epoch)
						signalLost()
						return
					}
					lastRenewed = time.Now()
					l.noteLeaseExpiry(res.Lease.ExpiresAt())
					continue
				}
				if time.Since(lastRenewed) > l.ttl {
					l.recordErr("lease renewal stalled beyond TTL")
					l.logf("schedulehost: lease renewal stalled beyond TTL; demoting to standby")
					signalLost()
					return
				}
			}
		}
	}()
	return lost
}

// sleep 等待一个待命轮询周期；ctx 结束返回 false。
func (l *Leader) sleep(ctx context.Context) bool {
	timer := time.NewTimer(l.retry)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (l *Leader) releaseLease() {
	mesh.Release(l.paths, LeasePurpose, LeaseKey, l.owner)
}

// storeCompatErr 运行调用方的 store 兼容自检（未配置时视为通过）。
func (l *Leader) storeCompatErr() error {
	if l.opts.StoreCompatCheck == nil {
		return nil
	}
	return l.opts.StoreCompatCheck()
}

// deferToHigherPriorityBlocked 报告本进程是否应因"更高优先级的在岗调度者"而
// 让位（S5 优先级让位 v2；未开启时恒 false）。
func (l *Leader) deferToHigherPriorityBlocked() bool {
	if !l.opts.DeferToHigherPriority {
		return false
	}
	higher, kind := HigherPrioritySchedulerOnline(l.paths, l.opts.Identity.Kind, l.owner.NodeID)
	if higher {
		l.logf("schedulehost: deferring to higher-priority scheduler (%s)", kind)
	}
	return higher
}

// creatorAffinityBlocked 报告本进程是否应因"活动任务创建者会话宿主在另一存活
// 调度者节点"而让位（S6 创建者亲和；未开启或非让位角色时恒 false）。
// 只对 DeferToHigherPriority=true 的交互宿主生效：daemon/runtime-server 是
// 正统调度者，绝不因亲和让位。检查本身 fail-open：读失败/无任务 = 不亲和。
func (l *Leader) creatorAffinityBlocked() bool {
	if !l.opts.DeferToHigherPriority || l.opts.CreatorAffinity == nil {
		return false
	}
	sessions := l.opts.CreatorAffinity.ActiveCreatorSessions(creatorAffinityScanLimit)
	if len(sessions) == 0 {
		return false
	}
	online, kind := creatorAffinityOnline(l.paths, l.owner.NodeID, sessions)
	if online {
		l.logf("schedulehost: deferring to creator-affinity scheduler (%s)", kind)
	}
	return online
}

// creatorAffinityScanLimit 限制每次亲和检查扫描的创建者会话数（最新优先）。
const creatorAffinityScanLimit = 8

func (l *Leader) degradedPolicy() DegradedPolicy {
	if l.opts.Degraded == DegradedStandalone {
		return DegradedStandalone
	}
	return DegradedStandby
}

func (l *Leader) noteAcquired(epoch int64) {
	l.mu.Lock()
	l.leaseExpires = time.Now().Add(l.ttl)
	l.lastErr = ""
	l.epoch = epoch
	l.mu.Unlock()
}

// leaseEpochMismatch 判定续约观察到的 epoch 是否与本届任期不符（M2）。
// 任一侧为 0（legacy 租约）时不可判定，按匹配处理——绝不因此误让位。
func (l *Leader) leaseEpochMismatch(observed int64) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.epoch <= 0 || observed <= 0 {
		return false
	}
	return observed != l.epoch
}

func (l *Leader) currentEpoch() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.epoch
}

// foreignLeaseHeld 报告当前租约是否由其他节点持有（接管计数的判定输入；
// 读取与抢占之间存在竞态，该计数是运维观测量，不参与控制决策）。
func (l *Leader) foreignLeaseHeld() bool {
	lease, ok := mesh.ReadLease(l.paths.LeasePath(LeasePurpose, LeaseKey))
	return ok && lease.OwnerNodeID != l.owner.NodeID
}

func (l *Leader) countTakeover() {
	l.mu.Lock()
	l.takeovers++
	l.mu.Unlock()
}

func (l *Leader) noteLeaseExpiry(at time.Time) {
	l.mu.Lock()
	l.leaseExpires = at
	l.mu.Unlock()
}

func (l *Leader) recordErr(msg string) {
	l.mu.Lock()
	l.lastErr = msg
	l.mu.Unlock()
}

func (l *Leader) setState(state State, reason string) {
	l.mu.Lock()
	previous := l.state
	l.state = state
	l.mu.Unlock()
	if previous != state {
		l.logf("schedulehost: %s → %s (%s)", previous, state, reason)
	}
}

func (l *Leader) logf(format string, args ...any) {
	if l.opts.Logf != nil {
		l.opts.Logf(format, args...)
	}
}
