package supervision

import (
	"strings"
	"time"
)

// MinProgressCheckInterval is the lower bound applied to an explicitly
// configured ProgressCheckInterval: a sub-30s sweep would turn the opt-in
// progress report into a near-resident poller, which the plan explicitly
// rules out (P0-2 改动 3). Values below the floor are clamped instead of
// rejected so a stale config keeps working while the host logs a warning.
const MinProgressCheckInterval = 30 * time.Second

// Config 集中 P2 控制面调参（doc 6.2-6.6）。零值字段在 WithDefaults 中
// 使用语义默认值，便于 yaml/json 配置部分覆盖。
type Config struct {
	// ExecutionDeadline 是 child execution 的默认 deadline 阈值。运行时
	// 超时扫描用它判定 timed_out 并生成 critical notification。
	// 0 使用默认 30m。
	ExecutionDeadline time.Duration `json:"execution_deadline,omitempty" yaml:"execution_deadline,omitempty"`
	// HeartbeatTimeout 是 heartbeat/progress 无更新的 stall 阈值。
	// 0 使用默认 5m。
	HeartbeatTimeout time.Duration `json:"heartbeat_timeout,omitempty" yaml:"heartbeat_timeout,omitempty"`
	// DigestMaxItems 限制单个 preflight digest 的最大条目数（doc 6.4）。
	// 0 使用默认 20。
	DigestMaxItems int `json:"digest_max_items,omitempty" yaml:"digest_max_items,omitempty"`
	// DigestMaxChars 限制 digest 文本预算，避免撑爆父上下文。
	// 0 使用默认 4000。
	DigestMaxChars int `json:"digest_max_chars,omitempty" yaml:"digest_max_chars,omitempty"`
	// SnapshotMaxItems 是 snapshot/descendants 投影在调用方未显式给 limit 时的
	// 缺省行数上限（plan §9 待决项：此前硬编码 200，两个宿主各写一份）。
	// 0 使用默认 200。
	SnapshotMaxItems int `json:"snapshot_max_items,omitempty" yaml:"snapshot_max_items,omitempty"`
	// ActionTTL 是 pending action 的存活时间；超时后由扫描标记 failed。
	// 0 使用默认 24h。
	ActionTTL time.Duration `json:"action_ttl,omitempty" yaml:"action_ttl,omitempty"`
	// WakeRateWindow 与 WakeMaxAutoWake 传给 WakeScheduler（doc 6.5 规则 4）。
	// 0 分别使用默认 1h 与 5。
	WakeRateWindow  time.Duration `json:"wake_rate_window,omitempty" yaml:"wake_rate_window,omitempty"`
	WakeMaxAutoWake int           `json:"wake_max_auto_wake,omitempty" yaml:"wake_max_auto_wake,omitempty"`
	// WakeMaxApprovalWake 是 approval 类别的独立窗口上限（P1-6 方案 1）。
	// 0（默认）或负数表示不设硬上限：审批被延迟会阻塞等待决策的子会话；
	// 正数为显式上限。failure/other 类别仍由 WakeMaxAutoWake 约束。
	WakeMaxApprovalWake int `json:"wake_max_approval_wake,omitempty" yaml:"wake_max_approval_wake,omitempty"`
	// WakeBudgetMode 选择 auto-wake 预算账本（P1-6 方案 2）：
	// "memory"（默认）为进程内计数；"durable" 把每次投递记为 supervision
	// store 的 claim 行，多进程共享数据库时预算一致且重启不清零。
	WakeBudgetMode string `json:"wake_budget_mode,omitempty" yaml:"wake_budget_mode,omitempty"`
	// WakeSelfCheckPerWindow 是父 turn 结束自检的窗口配额（P1-6 方案 4）：
	// 当某类预算已耗尽、wake 只被延后时，父回合结束仍可再起一个只消费
	// mailbox/digest 的父 turn，避免父会话在子 agent 仍异常时静默空闲。
	// 0（默认）关闭该行为，保持历史语义；正数即该 scope 每窗口的自检次数上限。
	WakeSelfCheckPerWindow int `json:"wake_self_check_per_window,omitempty" yaml:"wake_self_check_per_window,omitempty"`
	// TurnEndCheck 是「父会话 turn 结束后自动做一次技术核查」的开关
	// （docs/plan/supervision-manual-audit-plan-20260922.md）。
	// nil（未配置）与显式 false 都表示关闭：turn 结束后不再 drain durable wake、
	// 不再触发 digest-only self-check，父会话完全交还用户；积压事件由下一次自然
	// turn 的 preflight digest 被动注入，或由用户显式执行
	// `/supervision audit`（只读核查）/ `/supervision wake`（显式投递）处理。
	// 显式 true 恢复 2026-09-16 方案 doc 6.5 规则 2 的 turn-end 闭合语义，用于
	// 灰度回退；此开关不影响事件驱动（子会话完成 / 审批）的异常投递。
	TurnEndCheck *bool `json:"turn_end_check,omitempty" yaml:"turn_end_check,omitempty"`
	// ProgressCheckInterval 是 P2-D 的 opt-in 周期巡查间隔：正数时宿主按该
	// 间隔检查"是否存在 running background batch"，只在父会话空闲且无待投递
	// wake 时经既有 wake 通道注入一次 progress 汇报 turn。
	// 0（默认）不注册任何 ticker，宿主行为与引入该开关前完全一致：progress 只
	// 在父 turn 的 preflight digest 里被动出现，不存在常驻巡检 goroutine。
	ProgressCheckInterval time.Duration `json:"progress_check_interval,omitempty" yaml:"progress_check_interval,omitempty"`
	// TaskProgressInterval is the P0-1 task-level progress write-back window.
	// 0 (the default during the gray release) disables the write-back entirely:
	// hosts behave byte-identically to the pre-P0-1 code. A positive value makes
	// the background batch coordinator refresh SubagentTaskRecord.LastProgressAt
	// for running tasks at that cadence (at most one write per task and window),
	// so the parent-side progress rollup reports a real progress age instead of
	// falling back to the 60s heartbeat granularity.
	TaskProgressInterval time.Duration `json:"task_progress_interval,omitempty" yaml:"task_progress_interval,omitempty"`
	// WakeMaxProgressWake is the independent rolling-window allowance of the
	// progress wake class (P0-2/ADR-2). 0 uses the default (6 per window); a
	// negative value removes the cap (accepted for symmetry with the other
	// classes but not recommended: progress reports are the floodable family).
	WakeMaxProgressWake int `json:"wake_max_progress_wake,omitempty" yaml:"wake_max_progress_wake,omitempty"`
	// ApprovalTerminalGuard 是「run 终态后到达的审批决议零恢复」守卫的灰度
	// 开关（docs/plan/supervision-approval-resume-past-deadline-fix-plan.md §8，
	// 装配处见 chat.SessionActorConfig.ApprovalTerminalGuard）。
	// nil（未配置）与 true 均表示启用（默认开）；显式 false 时决策点与恢复
	// 入口回退到引入守卫前的行为，用于灰度与快速回滚，无需回滚二进制。
	ApprovalTerminalGuard *bool `json:"approval_terminal_guard,omitempty" yaml:"approval_terminal_guard,omitempty"`
	// MessageSemanticsV2 是指令投递语义 v2 的灰度开关（P0-3a/M5，方案
	// §3.3/§7.4）。false（默认）时 send_message/followup_task/send_input 的
	// 行为与返回字段逐字段保持 2026-09-17 现状；true 时启用三态语义矩阵与
	// delivered/queued/triggered/duplicate 返回字段。
	MessageSemanticsV2 bool `json:"message_semantics_v2,omitempty" yaml:"message_semantics_v2,omitempty"`
	// TriggerTurnAuto 控制 v2 语义下 busy 指令的 trigger_turn 自动消费
	// （P0-3b/M6，方案 §3.3 改动 2）。nil/true（默认）启用；显式 false 时
	// busy followup/send_input 只投递 mailbox、不自动起 turn。仅在
	// MessageSemanticsV2 为 true 时生效（见 TriggerTurnDrainEnabled）。
	TriggerTurnAuto *bool `json:"trigger_turn_auto,omitempty" yaml:"trigger_turn_auto,omitempty"`

	// --- 托管 turn / escalate-first（C1-4 = 改动 #8；方案 §6.2/§6.3、I5、Q2–Q5） ---

	// EscalateFirst 是 escalate-first 阶梯的开关（方案 §6.3；§10 回滚项）。
	// nil/true（默认）时软阈值只走"上报 → 决策 → 兜底"，不再直接 cancel；
	// 显式 false 恢复引入该机制前的强制分支（软阈值 ⇒ cancel_requested），
	// 用于灰度回退，无需回滚二进制。
	EscalateFirst *bool `json:"escalate_first,omitempty" yaml:"escalate_first,omitempty"`
	// SuspensionEnabled 是 turn 挂起（suspend + 同 turn resume）的开关
	// （I9 / §10 回滚项）。nil/true（默认）且 durability 探测通过时允许挂起；
	// 显式 false 时一律走 legacy 同步路径（"结束再唤醒"），不占用 turn。
	SuspensionEnabled *bool `json:"suspension_enabled,omitempty" yaml:"suspension_enabled,omitempty"`
	// StallEscalationMultiplier 是 stall 升级阈值倍率（Q3）：progress_age ≥
	// 倍率 × ProgressDeadlineAt 时进入"上报"档（ActionTaken=escalated），
	// 而不是直接取消。0 取默认 2。
	StallEscalationMultiplier float64 `json:"stall_escalation_multiplier,omitempty" yaml:"stall_escalation_multiplier,omitempty"`
	// DecisionWindow 是决策宽限期 W（Q2）：上报后留给主 Agent 决策的时间窗，
	// 到期仍无决策即执行兜底强制分支（CancelSource=decision_window_expired）。
	// 0 取默认 2 × 生效的 HeartbeatTimeout（默认配置下 10m）。
	DecisionWindow time.Duration `json:"decision_window,omitempty" yaml:"decision_window,omitempty"`
	// DecisionWindowMax 是决策宽限期的墙钟上限（Q2：上限 2W），用于防止
	// "可运行时钟"把兜底无限推迟。0 取默认 2 × DecisionWindow。
	DecisionWindowMax time.Duration `json:"decision_window_max,omitempty" yaml:"decision_window_max,omitempty"`
	// MaxExtensions 是单个 obligation 的延长次数上限（I5/Q4，默认 3）。
	MaxExtensions int `json:"max_extensions,omitempty" yaml:"max_extensions,omitempty"`
	// MaxExtensionPerCall 是单次延长的上限倍率（I5/Q4：单次 ≤ 1× 原始预算，
	// 默认 1）。
	MaxExtensionPerCall float64 `json:"max_extension_per_call,omitempty" yaml:"max_extension_per_call,omitempty"`
	// MaxExtensionTotal 是累计延长的上限倍率（I5/Q4：总量 ≤ 4× 原始预算，
	// 默认 4）。
	MaxExtensionTotal float64 `json:"max_extension_total,omitempty" yaml:"max_extension_total,omitempty"`
	// TurnHardCap 是 turn 级 hard cap（Q5/EC-E1/EC-H12，默认 24h）：挂起 turn
	// 的最长存活时间；到期前必须产生一次 critical 决策上报，禁止静默结束。
	TurnHardCap time.Duration `json:"turn_hard_cap,omitempty" yaml:"turn_hard_cap,omitempty"`
	// PatrolInterval 是 obligation 巡检预约（check_in_after，改动 #9/C3-2）的
	// 默认粒度：到点触发一次巡检或搭车下一次 progress resume。0 取默认
	// HeartbeatTimeout（默认配置下 5m）。
	PatrolInterval time.Duration `json:"patrol_interval,omitempty" yaml:"patrol_interval,omitempty"`
}

// DefaultConfig 返回默认调参。
func DefaultConfig() Config {
	d := Config{
		ExecutionDeadline: 30 * time.Minute,
		HeartbeatTimeout:  5 * time.Minute,
		DigestMaxItems:    20,
		DigestMaxChars:    4000,
		SnapshotMaxItems:  200,
		ActionTTL:         24 * time.Hour,
		WakeRateWindow:    time.Hour,
		WakeMaxAutoWake:   5,
		WakeBudgetMode:    string(WakeBudgetModeMemory),
		// C1-4（#8）：托管 turn / escalate-first 默认值（Q2–Q5、I5）。
		EscalateFirst:             boolPtr(true),
		SuspensionEnabled:         boolPtr(true),
		StallEscalationMultiplier: 2,
		MaxExtensions:             3,
		MaxExtensionPerCall:       1,
		MaxExtensionTotal:         4,
		TurnHardCap:               24 * time.Hour,
	}
	// Q2：决策宽限期默认 2×HeartbeatTimeout，墙钟上限默认 2W。
	d.DecisionWindow = 2 * d.HeartbeatTimeout
	d.DecisionWindowMax = 2 * d.DecisionWindow
	// 巡检预约默认取心跳粒度（改动 #9）。
	d.PatrolInterval = d.HeartbeatTimeout
	return d
}

// boolPtr returns a pointer to v for the gray-release switches whose default is
// "on" (nil and true both mean enabled; only an explicit false turns them off).
func boolPtr(v bool) *bool { return &v }

// WithDefaults 返回补齐默认值后的配置副本，调用方字段保持原值。
func (c Config) WithDefaults() Config {
	d := DefaultConfig()
	if c.ExecutionDeadline > 0 {
		d.ExecutionDeadline = c.ExecutionDeadline
	}
	if c.HeartbeatTimeout > 0 {
		d.HeartbeatTimeout = c.HeartbeatTimeout
	}
	if c.DigestMaxItems > 0 {
		d.DigestMaxItems = c.DigestMaxItems
	}
	if c.DigestMaxChars > 0 {
		d.DigestMaxChars = c.DigestMaxChars
	}
	if c.SnapshotMaxItems > 0 {
		d.SnapshotMaxItems = c.SnapshotMaxItems
	}
	if c.ActionTTL > 0 {
		d.ActionTTL = c.ActionTTL
	}
	if c.WakeRateWindow > 0 {
		d.WakeRateWindow = c.WakeRateWindow
	}
	if c.WakeMaxAutoWake > 0 {
		d.WakeMaxAutoWake = c.WakeMaxAutoWake
	}
	if c.WakeMaxApprovalWake != 0 {
		d.WakeMaxApprovalWake = c.WakeMaxApprovalWake
	}
	if c.WakeMaxProgressWake != 0 {
		d.WakeMaxProgressWake = c.WakeMaxProgressWake
	}
	if strings.EqualFold(strings.TrimSpace(c.WakeBudgetMode), string(WakeBudgetModeDurable)) {
		d.WakeBudgetMode = string(WakeBudgetModeDurable)
	}
	// ProgressCheckInterval 没有默认值：它是显式 opt-in，0 必须保持 0。
	if c.ProgressCheckInterval > 0 {
		d.ProgressCheckInterval = c.ProgressCheckInterval
		if d.ProgressCheckInterval < MinProgressCheckInterval {
			d.ProgressCheckInterval = MinProgressCheckInterval
		}
	}
	// TaskProgressInterval 同样是显式 opt-in（灰度期出厂 0=不写）。
	if c.TaskProgressInterval > 0 {
		d.TaskProgressInterval = c.TaskProgressInterval
	}
	// TurnEndCheck 默认关：nil 保持 nil（等价关闭），仅显式值需传递。
	if c.TurnEndCheck != nil {
		d.TurnEndCheck = c.TurnEndCheck
	}
	// ApprovalTerminalGuard 默认开：nil 保持 nil（等价启用），仅显式值需传递。
	if c.ApprovalTerminalGuard != nil {
		d.ApprovalTerminalGuard = c.ApprovalTerminalGuard
	}
	// MessageSemanticsV2 默认关：只有显式 true 才打开灰度。
	if c.MessageSemanticsV2 {
		d.MessageSemanticsV2 = true
	}
	// TriggerTurnAuto 默认开：nil 保持 nil（等价启用），仅显式值需传递。
	if c.TriggerTurnAuto != nil {
		d.TriggerTurnAuto = c.TriggerTurnAuto
	}
	// C1-4（#8）托管 turn / escalate-first：nil/0 取默认，显式值透传。
	if c.EscalateFirst != nil {
		d.EscalateFirst = c.EscalateFirst
	}
	if c.SuspensionEnabled != nil {
		d.SuspensionEnabled = c.SuspensionEnabled
	}
	if c.StallEscalationMultiplier > 0 {
		d.StallEscalationMultiplier = c.StallEscalationMultiplier
	}
	// Q2 的派生默认必须基于"生效的" HeartbeatTimeout，否则显式心跳覆盖
	// 会让 W 与实际心跳粒度脱钩。
	if c.DecisionWindow > 0 {
		d.DecisionWindow = c.DecisionWindow
	} else {
		d.DecisionWindow = 2 * d.HeartbeatTimeout
	}
	if c.DecisionWindowMax > 0 {
		d.DecisionWindowMax = c.DecisionWindowMax
	} else {
		d.DecisionWindowMax = 2 * d.DecisionWindow
	}
	if c.MaxExtensions > 0 {
		d.MaxExtensions = c.MaxExtensions
	}
	if c.MaxExtensionPerCall > 0 {
		d.MaxExtensionPerCall = c.MaxExtensionPerCall
	}
	if c.MaxExtensionTotal > 0 {
		d.MaxExtensionTotal = c.MaxExtensionTotal
	}
	if c.TurnHardCap > 0 {
		d.TurnHardCap = c.TurnHardCap
	}
	if c.PatrolInterval > 0 {
		d.PatrolInterval = c.PatrolInterval
	} else {
		d.PatrolInterval = d.HeartbeatTimeout
	}
	return d
}

// ProgressCheckEnabled reports whether the opt-in periodic progress sweep is
// configured. It never enables the sweep implicitly, so an unwired or
// zero-valued config keeps the historical behavior.
func (c Config) ProgressCheckEnabled() bool {
	return c.WithDefaults().ProgressCheckInterval > 0
}

// EscalateFirstEnabled reports whether the escalate-first ladder (方案 §6.3) is
// active. Unset means enabled: a soft-threshold stall escalates to the parent
// and waits for a decision (or the decision window fallback) instead of being
// cancelled immediately. Only an explicit false restores the legacy forced
// branch, which is the documented gray-release rollback (§10).
func (c Config) EscalateFirstEnabled() bool {
	if c.EscalateFirst == nil {
		return true
	}
	return *c.EscalateFirst
}

// SuspensionAllowed reports whether turn suspension (park a turn and resume it
// with the same turn id) may be used once the durability probe passes
// (I9 / §10). Unset means enabled; an explicit false pins every dispatch to the
// legacy synchronous path ("finish, then wake"), so no turn is ever parked.
func (c Config) SuspensionAllowed() bool {
	if c.SuspensionEnabled == nil {
		return true
	}
	return *c.SuspensionEnabled
}

// TurnEndCheckEnabled reports whether the turn-end automatic technical check
// (drain + optional digest-only self-check) is enabled. Unset means disabled:
// the manual audit plan (2026-09-22) makes the check opt-in so a parent turn
// end never starts a supervision turn on its own. Only an explicit true
// restores the 2026-09-16 §6.5 rule 2 closure for gray rollback.
func (c Config) TurnEndCheckEnabled() bool {
	if c.TurnEndCheck == nil {
		return false
	}
	return *c.TurnEndCheck
}

// ApprovalTerminalGuardEnabled reports whether the terminal-run approval guard
// (docs/plan/supervision-approval-resume-past-deadline-fix-plan.md §8) is
// active. Unset means enabled: the guard is on by default and only an explicit
// false turns it off, so an unwired or zero-valued config keeps the fixed
// behavior.
func (c Config) ApprovalTerminalGuardEnabled() bool {
	if c.ApprovalTerminalGuard == nil {
		return true
	}
	return *c.ApprovalTerminalGuard
}

// WakeSchedulerConfig 导出给装配层使用的 wake 调参。
func (c Config) WakeSchedulerConfig() WakeSchedulerConfig {
	cfg := c.WithDefaults()
	return WakeSchedulerConfig{
		RateWindow:               cfg.WakeRateWindow,
		MaxAutoWakePerWindow:     cfg.WakeMaxAutoWake,
		MaxApprovalWakePerWindow: cfg.WakeMaxApprovalWake,
		MaxProgressWakePerWindow: cfg.WakeMaxProgressWake,
		BudgetMode:               WakeBudgetMode(cfg.WakeBudgetMode),
		SelfCheckPerWindow:       cfg.WakeSelfCheckPerWindow,
	}
}
