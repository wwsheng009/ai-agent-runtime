package runtimehost

import (
	"context"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

// LeaseMode 决定 actor 会话租约的**持有期**（E3.4-0）。
//
// 两个宿主语义不同，必须显式区分（详见
// docs/automation-tasks/aicli-executor-reuse-design.md §11 G1）：
//   - runtimeapi / daemon：**run-scoped**（零值）——构造后释放，run 末/stalled/stop 释放，
//     run 前经 PrepareRun 重取；空闲 actor 不阻塞其他通道。
//   - aicli：**actor-scoped**——构造后**不**释放，run 末**不**释放，仅 stalled/stop 释放
//     （TUI 打开期间独占该会话）。
type LeaseMode string

const (
	// LeaseModeRunScoped 是零值/默认（run-scoped）。
	LeaseModeRunScoped LeaseMode = ""
	// LeaseModeActorScoped 是 actor 生命周期持锁。
	LeaseModeActorScoped LeaseMode = "actor-scoped"
)

// ReleasesAfterConstruction 报告构造后是否释放租约。
func (m LeaseMode) ReleasesAfterConstruction() bool { return m != LeaseModeActorScoped }

// ReleasesOnRunFinished 报告 run 末是否释放租约。
func (m LeaseMode) ReleasesOnRunFinished() bool { return m != LeaseModeActorScoped }

// Deps 是执行核的显式依赖（E3.1b-1：基础设施子集）。
//
// 目标是把 buildSessionActor 读到的 h.* 逐项显式化，使 actor 工厂最终可脱离
// *Handler。本批次先落「会话上下文 + 存储 + 事件 + 日志」这组**公开类型**依赖；
// agent 装配 / profile 解析 / 工具策略 / 会话租约等回调在 E3.1b-2 补齐
// （需要先把 profileRuntimeState 导出）。见
// docs/automation-tasks/runtimehost-executor-extraction-plan.md §6.2。
//
// 约束：本结构**不得**出现 *runtimeapi.Handler 或任何 HTTP 类型——这是「执行核
// 不反向依赖 API 层」的编译期保证。
type Deps struct {
	// SessionContext 是会话元数据派生的执行上下文（见 ExtractSessionContext）。
	SessionContext SessionContext
	// SessionStorage 是会话持久存储（session_history.sqlite）。
	SessionStorage chat.SessionStorage
	// RuntimeStore 是会话运行态存储（session_runtime.sqlite）。
	RuntimeStore chat.RuntimeStateStore
	// EventStore 是会话事件存储。
	EventStore chat.EventStore
	// LLMRuntime 是共享 LLM runtime（provider/model 解析与 actor 执行）。
	LLMRuntime *llm.LLMRuntime
	// AcquireSessionLeaseNoWait 非阻塞取会话执行租约（actor 构建期：hub 锁内不可排队，
	// 构造冲突立即返回给调用方映射为 409）。
	AcquireSessionLeaseNoWait func(ctx context.Context, sessionID string) (*chat.SessionLeaseHandle, error)
	// AcquireSessionLease 取会话执行租约（run 期：允许排队等同进程持有者让出）。
	AcquireSessionLease func(ctx context.Context, sessionID string) (*chat.SessionLeaseHandle, error)
	// LeaseMode 决定租约持有期（E3.4-0；零值 = run-scoped，行为不变）。
	LeaseMode LeaseMode
	// PrepareAgent 组装会话执行所需的 agent 与选中 runtime 配置。
	// 返回的 profileHandle 对 runtimehost **不透明**，仅供同一宿主的
	// BuildLoopConfig 回传复用（避免 profile 二次解析）。
	PrepareAgent func(ctx context.Context, sessionID string, sessionCtx SessionContext) (*agent.Agent, *runtimecfg.RuntimeConfig, any)
	// BuildLoopConfig 组装 loop 配置（义务解析器 / main-agent routing /
	// completion requirement / knowledge observation），复用 PrepareAgent 的 profileHandle。
	BuildLoopConfig func(sessionID string, sessionCtx SessionContext, selectedConfig *runtimecfg.RuntimeConfig, profileHandle any) *agent.LoopReActConfig
	// OnProgress / OnBudgetProgress 是 run 级进度回调（可空）。
	OnProgress       func(kind string)
	OnBudgetProgress func(state agent.TurnBudgetState)
	// ApprovalTerminalGuard 透传 supervision 的审批终态守卫配置（可空）。
	ApprovalTerminalGuard *bool
	// TriggerTurnDrain / TriggerTurnRunMeta 透传 supervision 的 run 末 drain 配置。
	TriggerTurnDrain   bool
	TriggerTurnRunMeta func(ctx context.Context, session *chat.Session) *team.RunMeta
	// SuspendedTurnAbandon 是挂起 turn 的放弃级联钩子。
	SuspendedTurnAbandon chat.SuspendedTurnAbandonHooks
	// PersistHook 是会话持久化钩子（可空）。
	PersistHook func(ctx context.Context, session *chat.Session) (*chat.Session, error)
	// PrepareRun 覆盖默认的 run 前钩子（默认 = 取会话租约）。
	// 宿主注入自定义实现时，需自行保证会话租约已就绪（aicli 走本地 hook）。
	PrepareRun func(ctx context.Context, session *chat.Session, resume bool) error
	// EnsureSession 是 actor 自愈钩子（可空；仅基础会话启用）。
	EnsureSession func(ctx context.Context, sessionID string) error
	// CheckpointInterval 是长 turn 增量落库间隔（0 = chat 默认）。
	CheckpointInterval time.Duration
	// RunStallTimeout 是 run 无进展看门狗超时（0 = 关闭）。
	RunStallTimeout time.Duration
	// EventBus 是运行时事件总线（可空）。
	EventBus *runtimeevents.Bus
	// Logf 是执行核日志（可空）。
	Logf func(format string, args ...any)
}
