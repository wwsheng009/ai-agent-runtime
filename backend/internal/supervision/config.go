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
}

// DefaultConfig 返回默认调参。
func DefaultConfig() Config {
	return Config{
		ExecutionDeadline: 30 * time.Minute,
		HeartbeatTimeout:  5 * time.Minute,
		DigestMaxItems:    20,
		DigestMaxChars:    4000,
		SnapshotMaxItems:  200,
		ActionTTL:         24 * time.Hour,
		WakeRateWindow:    time.Hour,
		WakeMaxAutoWake:   5,
		WakeBudgetMode:    string(WakeBudgetModeMemory),
	}
}

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
	return d
}

// ProgressCheckEnabled reports whether the opt-in periodic progress sweep is
// configured. It never enables the sweep implicitly, so an unwired or
// zero-valued config keeps the historical behavior.
func (c Config) ProgressCheckEnabled() bool {
	return c.WithDefaults().ProgressCheckInterval > 0
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
