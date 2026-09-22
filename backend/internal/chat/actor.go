package chat

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/artifact"
	"github.com/wwsheng009/ai-agent-runtime/internal/checkpoint"
	"github.com/wwsheng009/ai-agent-runtime/internal/compactruntime"
	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimehooks "github.com/wwsheng009/ai-agent-runtime/internal/hooks"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	runtimeoutput "github.com/wwsheng009/ai-agent-runtime/internal/output"
	"github.com/wwsheng009/ai-agent-runtime/internal/planmode"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	runtimeskill "github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

const aicliRuntimeContextTokenCountKey = "aicli_context_token_count"

const interruptedRunReplyGrace = 250 * time.Millisecond

// ErrSessionActorStopped is returned when a command targets an actor whose
// command loop has already been stopped.
var ErrSessionActorStopped = errors.New("session actor is stopped")

// ErrSessionBusy is returned when a command that starts a new turn targets an
// actor that still owns an active turn. Callers that intentionally serialize
// follow-up work may wait for readiness and retry; control-plane callers can
// continue returning the error to make concurrent submissions explicit.
var ErrSessionBusy = errors.New("session is busy")

var errSessionRunSuperseded = errors.New("session run was superseded")

// errSessionRunTerminal refuses to resume pending work whose run already ended in
// a terminal cancellation class (execution deadline / external cancel). The
// durable state is left untouched: callers must not restart the run
// (docs/plan/supervision-approval-resume-past-deadline-fix-plan.md, P0-2).
var errSessionRunTerminal = errors.New("session run already terminated")

// Approval resolutions reported on the approval_resolved event and on
// toolbroker.AgentApprovalResult.Resolution (see P0-3/P1-1). toolbroker owns the
// literals so the event contract and the tool contract cannot drift.
const (
	ApprovalResolutionAllowed             = toolbroker.ApprovalResolutionAllowed
	ApprovalResolutionDenied              = toolbroker.ApprovalResolutionDenied
	ApprovalResolutionExpired             = toolbroker.ApprovalResolutionExpired
	ApprovalResolutionRunTerminated       = toolbroker.ApprovalResolutionRunTerminated
	ApprovalResolutionRunTerminalNoResume = toolbroker.ApprovalResolutionRunTerminalNoResume
)

type approvalDetachContextKey struct{}

type approvalDetachState struct {
	detached atomic.Bool
}

// approvalOutcome records how the most recent approval decision was applied so
// hosts can report it without re-deriving the decision (P0-3/P1-1).
type approvalOutcome struct {
	requestID  string
	resolution string
	resumed    bool
}

type sessionRunControlContextKey struct{}

// sessionRunControl is the comparable ownership token for one actor run.
// Mutable cancellation ownership is guarded by SessionActor.mu; wait-group
// bookkeeping is guarded by runWaitMu; the remaining shared flags are atomic
// because the watchdog, command loop, and run tail use them concurrently.
type sessionRunControl struct {
	// sessionID records which actor minted this token. Broker tools forward the
	// caller's run context into another session's actor, so a persist path that
	// receives a foreign token must not judge it against its own active run.
	sessionID             string
	generation            uint64
	turnID                string
	stripMetadataKeys     []string
	reply                 chan SubmitResult
	replyOnce             sync.Once
	runWaitMu             sync.Mutex
	runWaitOnce           sync.Once
	runWaitTracked        bool
	runWaitDetached       bool
	runWaitReleasePending bool
	cancel                context.CancelFunc
	interrupted           atomic.Bool
	abandoned             atomic.Bool
	finalizing            atomic.Bool
	lastActivity          atomic.Int64
}

// SessionActorConfig configures a SessionActor instance.
type SessionActorConfig struct {
	Agent        *agent.Agent
	LLMRuntime   *llm.LLMRuntime
	SessionStore SessionStorage
	StateStore   RuntimeStateStore
	EventStore   EventStore
	EventBus     *runtimeevents.Bus
	LoopConfig   *agent.LoopReActConfig
	PrepareRun   func(ctx context.Context, session *Session, resume bool) error
	PersistHook  func(ctx context.Context, session *Session) (*Session, error)
	// EnsureSession is the host-side recovery hook the actor invokes when a
	// session-store Load/Update observes ErrSessionNotFound (the durable row
	// was deleted, expired, or never persisted). The host rebuilds the row
	// from its own in-memory snapshot; returning nil means "row restored,
	// retry". nil disables actor self-heal and keeps the pre-existing error
	// semantics. The actor applies single-flight plus a short negative cache
	// (sessionRecoveryRetryInterval) so a persistently missing row cannot
	// cause a retry storm.
	EnsureSession func(ctx context.Context, sessionID string) error
	// RecoverStale releases transient busy states left by a previous
	// actor instance. Enable it only after acquiring exclusive session ownership.
	RecoverStale bool
	OnStop       func()
	// RunStallTimeout 无进展超时：run 启动后，若超过该时长没有任何进展事件
	// （assistant_delta / assistant.reasoning / tool.progress / tool.requested /
	// tool.completed / 状态更新），判定 run 挂死（如上游挂死），自动强制中止并
	// 写回 stopped 状态，解除 busy 锁。
	// 0 表示禁用；宿主默认禁用（0），因为强制中止会以 context.Canceled 结束
	// 整个 turn，属于"意外结束会话"。
	RunStallTimeout time.Duration
	// OnRunStalled 在 run 因停滞被 watchdog 强制中止后回调（宿主在此释放 lease）。
	OnRunStalled func(turnID string)
	// OnRunFinished 在每次 run 完全结束后回调一次（无论成功、失败还是被
	// 中断）。宿主可在此把 run 级 session lease 释放掉，使 actor 空闲时
	// 不再长期占用 session 锁（配合 PrepareRun 在下一个 run 前重新获取）。
	// 回调在 actor 的命令循环线程执行，不应阻塞或长时间运行。
	OnRunFinished func()
	// CheckpointInterval 控制长 turn 中途增量落库的最小间隔：ReAct 循环每次
	// 提交 durable 历史（assistant 文本 / tool 结果 / 压缩改写）后都会请求一次
	// checkpoint，实际写入按该间隔节流。0 使用 DefaultSessionCheckpointInterval；
	// 负数禁用中途落库（turn 结束的 post-turn sync 仍照常落库）。
	CheckpointInterval time.Duration
	// ApprovalTerminalGuard 是「run 终态后到达的审批决议零恢复」守卫的灰度
	// 开关（docs/plan/supervision-approval-resume-past-deadline-fix-plan.md §8）。
	// nil 与 true 均为启用（默认开）；显式 false 让决策点、恢复入口与终态
	// 收尾回退到引入守卫前的行为。宿主从 supervision.approval_terminal_guard
	// 透传（supervision.Config.ApprovalTerminalGuard）。
	ApprovalTerminalGuard *bool
	// TriggerTurnDrain 开启 P0-3b 的 run 结束 drain：把子会话 mailbox 中
	// trigger_turn=true 且未消费的指令合并成一个 prompt 自动起新 turn。宿主
	// 从 supervision.Config.TriggerTurnDrainEnabled() 透传（v2 语义且
	// trigger_turn_auto 未关闭）。
	TriggerTurnDrain bool
	// TriggerTurnRunMeta 为 drain 提交的新 turn 构造 RunMeta。宿主复用
	// followup_task 的 child-session RunMeta 重建口径
	// (toolbroker.SpawnAgentRunMetaFromContext)；nil 表示提交空 RunMeta。
	TriggerTurnRunMeta func(ctx context.Context, session *Session) *team.RunMeta
}

// SessionActor serializes session commands and manages execution state.
type SessionActor struct {
	id           string
	agent        *agent.Agent
	llmRuntime   *llm.LLMRuntime
	loopConfig   *agent.LoopReActConfig
	sessionStore SessionStorage
	stateStore   RuntimeStateStore
	eventStore   EventStore
	eventBus     *runtimeevents.Bus
	// replayedReceipts 记录本次进程内已从回执库回放并消费掉的 tool_call_id：
	// 回执在消费后被删除是既有语义（避免陈旧结果被再次回放），因此终局补回执
	// 时必须跳过这些调用，不能把已消费的回执重新写成"待回放"状态。
	replayedReceiptsMu sync.Mutex
	replayedReceipts   map[string]struct{}
	prepareRun         func(context.Context, *Session, bool) error
	persistHook        func(context.Context, *Session) (*Session, error)
	ensureSession      func(context.Context, string) error
	// sessionHealMu guards ensureSession single-flight and negative caching.
	sessionHealMu           sync.Mutex
	sessionHealBlockedUntil time.Time
	recoverStale            bool
	// terminalGuard 解析自 SessionActorConfig.ApprovalTerminalGuard，默认 true
	// （见该字段注释）。
	terminalGuard bool
	// triggerTurnDrain / 限流状态实现 P0-3b：run 结束后消费 trigger_turn
	// mailbox 指令。triggerTurnMu 保护 lastAutoAt/consecutive 与 dropped 去重。
	triggerTurnDrain          bool
	triggerTurnRunMeta        func(ctx context.Context, session *Session) *team.RunMeta
	triggerTurnDrainInFlight  atomic.Bool
	triggerTurnMu             sync.Mutex
	triggerTurnLastAutoAt     time.Time
	triggerTurnConsecutive    int
	triggerTurnLastDroppedAt  time.Time
	triggerTurnLastDroppedKey string
	// runStallTimeout / onRunStalled mirror SessionActorConfig; see there.
	runStallTimeout time.Duration
	onRunStalled    func(turnID string)
	onRunFinished   func()
	runSequence     atomic.Uint64
	// checkpointInterval / lastCheckpointAt 实现长 turn 中途落库的节流，
	// 语义见 SessionActorConfig.CheckpointInterval。
	checkpointInterval time.Duration
	lastCheckpointAt   atomic.Int64

	cmdCh chan Command
	stop  chan struct{}
	done  chan struct{}

	startOnce  sync.Once
	stopOnce   sync.Once
	onStop     func()
	onStopOnce sync.Once

	mu        sync.RWMutex
	state     *RuntimeState
	activeRun *sessionRunControl

	runLifecycleMu   sync.Mutex
	statePersistMu   sync.Mutex
	sessionPersistMu sync.Mutex

	waiterMu        sync.Mutex
	approvalWaiters map[string]chan runtimepolicy.ApprovalResponse
	questionWaiters map[string]chan string
	// lastApprovalOutcome is guarded by waiterMu.
	lastApprovalOutcome approvalOutcome
	activeRunWG         sync.WaitGroup
}

// NewSessionActor creates a new session actor.
func NewSessionActor(sessionID string, cfg SessionActorConfig) (*SessionActor, error) {
	sessionID = NormalizeSessionID(sessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("session id is required")
	}
	if cfg.Agent == nil {
		return nil, fmt.Errorf("agent is required")
	}
	bus := cfg.EventBus
	if bus == nil {
		bus = cfg.Agent.GetEventBus()
		if bus == nil {
			bus = runtimeevents.NewBus()
		}
	}
	loopConfig := cfg.LoopConfig
	if loopConfig == nil && cfg.Agent != nil {
		if agentConfig := cfg.Agent.GetConfig(); agentConfig != nil {
			loopConfig = &agent.LoopReActConfig{
				MaxSteps:             agent.NormalizeMaxSteps(agentConfig.MaxSteps),
				MaxToolCalls:         agentConfig.MaxToolCalls,
				MaxRunDuration:       agentConfig.MaxRunDuration,
				MaxExplorationSteps:  agentConfig.MaxExplorationSteps,
				MaxRepeatedToolCalls: agentConfig.MaxRepeatedToolCalls,
				MaxRepeatedPollCalls: agentConfig.MaxRepeatedPollCalls,
				EnableThought:        true,
				EnableToolCalls:      true,
				EnableParallelTools:  true,
				MaxParallelToolCalls: 4,
				Temperature:          agentConfig.Temperature,
			}
		}
	}
	actor := &SessionActor{
		id:                 sessionID,
		agent:              cfg.Agent,
		llmRuntime:         cfg.LLMRuntime,
		loopConfig:         loopConfig,
		sessionStore:       cfg.SessionStore,
		stateStore:         cfg.StateStore,
		eventStore:         cfg.EventStore,
		eventBus:           bus,
		prepareRun:         cfg.PrepareRun,
		persistHook:        cfg.PersistHook,
		ensureSession:      cfg.EnsureSession,
		recoverStale:       cfg.RecoverStale,
		terminalGuard:      cfg.ApprovalTerminalGuard == nil || *cfg.ApprovalTerminalGuard,
		triggerTurnDrain:   cfg.TriggerTurnDrain,
		triggerTurnRunMeta: cfg.TriggerTurnRunMeta,
		onStop:             cfg.OnStop,
		runStallTimeout:    cfg.RunStallTimeout,
		onRunStalled:       cfg.OnRunStalled,
		onRunFinished:      cfg.OnRunFinished,
		checkpointInterval: resolveSessionCheckpointInterval(cfg.CheckpointInterval),
		cmdCh:              make(chan Command, 32),
		stop:               make(chan struct{}),
		done:               make(chan struct{}),
		approvalWaiters:    make(map[string]chan runtimepolicy.ApprovalResponse),
		questionWaiters:    make(map[string]chan string),
	}
	if err := actor.loadState(context.Background()); err != nil {
		return nil, err
	}
	actor.configureRuntime()
	return actor, nil
}

// Start launches the actor goroutine.
func (a *SessionActor) Start() {
	if a == nil {
		return
	}
	a.startOnce.Do(func() {
		go a.run()
	})
}

// Stop terminates the actor loop and waits for it to exit.
func (a *SessionActor) Stop() {
	_ = a.StopContext(context.Background())
}

// StopAsync begins stopping the actor loop without waiting for it to exit.
// The stop signal is delivered and any in-flight run is cancelled via its run
// context; the actor loop then finishes in the background as soon as the run
// goroutines return.
//
// Use StopAsync instead of Stop/StopContext from code that executes on the
// actor's own run goroutine (e.g. a tool call that closes the current
// session): waiting for a.done there would deadlock, because the actor loop
// only exits after every run goroutine — including the caller — returns.
func (a *SessionActor) StopAsync() {
	if a == nil {
		return
	}
	a.Start()
	a.stopOnce.Do(func() {
		close(a.stop)
		a.cancelActive()
	})
}

// StopContext terminates the actor loop, waiting at most until ctx expires.
// The actor keeps stopping in the background after a timeout so leases and
// stop hooks are still released once the in-flight command returns.
func (a *SessionActor) StopContext(ctx context.Context) error {
	if a == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	a.StopAsync()
	select {
	case <-a.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// IsStopped reports whether the actor has begun stopping or its command loop
// has already exited.
func (a *SessionActor) IsStopped() bool {
	if a == nil {
		return true
	}
	select {
	case <-a.stop:
		return true
	default:
	}
	select {
	case <-a.done:
		return true
	default:
		return false
	}
}

func (a *SessionActor) runStopHook() {
	if a == nil {
		return
	}
	a.onStopOnce.Do(func() {
		if a.agent != nil {
			_ = a.agent.Close()
		}
		if a.onStop != nil {
			a.onStop()
		}
	})
}

// SubmitPromptOption configures a SubmitPrompt call.
type SubmitPromptOption struct {
	ImagePaths       []string
	ImageArtifactDir string
	RouteOverride    *RunRouteOverride
	// TurnSystemMessages 是本回合一次性 system 注入（如 /skill 的 ProgramGuide），
	// 只进入本次 run 的请求历史，不写入会话持久历史。
	TurnSystemMessages []runtimetypes.Message
	// TurnPinnedTools 是本回合一次性工具叠加（如 /skill 注入的 skill 函数与程序），
	// 在稳定工具面冻结之后附加，不写回会话级稳定工具面。
	TurnPinnedTools []runtimetypes.ToolDefinition
	// TriggerTurnAuto marks a prompt submitted by the trigger_turn drain, so
	// the loop guard can tell automatic turns from explicit ones (P0-3b).
	TriggerTurnAuto bool
}

// turnInjection carries one-shot per-turn injections for a single prompt run.
// It must never be persisted or reused by a later turn.
type turnInjection struct {
	systemMessages []runtimetypes.Message
	pinnedTools    []runtimetypes.ToolDefinition
}

// SubmitPrompt submits a prompt and waits for the result.
func (a *SessionActor) SubmitPrompt(ctx context.Context, prompt string, runMeta *team.RunMeta, opts ...SubmitPromptOption) (*agent.Result, error) {
	if a == nil {
		return nil, fmt.Errorf("session actor is nil")
	}
	a.Start()
	reply := make(chan SubmitResult, 1)
	var opt SubmitPromptOption
	if len(opts) > 0 {
		opt = opts[0]
	}
	cmd := SubmitPrompt{
		Ctx:                ctx,
		Prompt:             prompt,
		ImagePaths:         opt.ImagePaths,
		ImageArtifactDir:   opt.ImageArtifactDir,
		RunMeta:            runMeta.Clone(),
		RouteOverride:      opt.RouteOverride.Clone(),
		TurnSystemMessages: cloneRuntimeMessages(opt.TurnSystemMessages),
		TurnPinnedTools:    cloneRuntimeToolDefinitions(opt.TurnPinnedTools),
		TriggerTurnAuto:    opt.TriggerTurnAuto,
		Reply:              reply,
	}
	if err := a.send(ctx, cmd); err != nil {
		return nil, err
	}
	select {
	case res := <-reply:
		return res.Result, res.Err
	case <-a.done:
		return nil, ErrSessionActorStopped
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Continue resumes execution without appending a new visible user prompt.
func (a *SessionActor) Continue(ctx context.Context, runMeta *team.RunMeta, opts ...ContinueOption) (*agent.Result, error) {
	if a == nil {
		return nil, fmt.Errorf("session actor is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	continuation := mergeContinueOptions(opts)
	a.Start()
	reply := make(chan SubmitResult, 1)
	cmd := ContinueSession{
		Ctx:                  ctx,
		ContinuationPrompt:   continuation.ContinuationPrompt,
		ContinuationMetadata: cloneRuntimeInterfaceMap(continuation.ContinuationMetadata),
		StripMetadataKeys:    append([]string(nil), continuation.StripMetadataKeys...),
		RunMeta:              runMeta.Clone(),
		Reply:                reply,
	}
	if err := a.send(ctx, cmd); err != nil {
		return nil, err
	}
	select {
	case res := <-reply:
		return res.Result, res.Err
	case <-a.done:
		return nil, ErrSessionActorStopped
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// SubmitPromptAsync submits a prompt without waiting for the final result.
func (a *SessionActor) SubmitPromptAsync(ctx context.Context, prompt string, runMeta *team.RunMeta, opts ...SubmitPromptOption) error {
	if a == nil {
		return fmt.Errorf("session actor is nil")
	}
	a.Start()
	reply := make(chan SubmitResult, 1)
	var opt SubmitPromptOption
	if len(opts) > 0 {
		opt = opts[0]
	}
	cmd := SubmitPrompt{
		Ctx:                ctx,
		Prompt:             prompt,
		ImagePaths:         opt.ImagePaths,
		ImageArtifactDir:   opt.ImageArtifactDir,
		RunMeta:            runMeta.Clone(),
		RouteOverride:      opt.RouteOverride.Clone(),
		TurnSystemMessages: cloneRuntimeMessages(opt.TurnSystemMessages),
		TurnPinnedTools:    cloneRuntimeToolDefinitions(opt.TurnPinnedTools),
		TriggerTurnAuto:    opt.TriggerTurnAuto,
		Reply:              reply,
	}
	if err := a.send(ctx, cmd); err != nil {
		return err
	}
	go func() {
		select {
		case <-reply:
		case <-a.done:
		}
	}()
	return nil
}

// ApproveTool resolves a pending approval request.
func (a *SessionActor) ApproveTool(ctx context.Context, requestID string, allow bool) error {
	return a.ApproveToolWithArgs(ctx, requestID, allow, nil)
}

// ApproveToolWithArgs resolves a pending approval request with optional patched args.
func (a *SessionActor) ApproveToolWithArgs(ctx context.Context, requestID string, allow bool, patchedArgs json.RawMessage) error {
	if a == nil {
		return fmt.Errorf("session actor is nil")
	}
	a.Start()
	reply := make(chan error, 1)
	cmd := ApproveTool{
		Ctx:       ctx,
		RequestID: requestID,
		Allow:     allow,
		PatchedArgs: func() json.RawMessage {
			if len(patchedArgs) == 0 {
				return nil
			}
			return append(json.RawMessage(nil), patchedArgs...)
		}(),
		Reply: reply,
	}
	if err := a.send(ctx, cmd); err != nil {
		return err
	}
	select {
	case err := <-reply:
		return err
	case <-a.done:
		return ErrSessionActorStopped
	case <-ctx.Done():
		return ctx.Err()
	}
}

// AnswerQuestion resolves a pending question request.
func (a *SessionActor) AnswerQuestion(ctx context.Context, questionID, answer string) error {
	if a == nil {
		return fmt.Errorf("session actor is nil")
	}
	a.Start()
	reply := make(chan error, 1)
	cmd := AnswerQuestion{
		Ctx:        ctx,
		QuestionID: questionID,
		Answer:     answer,
		Reply:      reply,
	}
	if err := a.send(ctx, cmd); err != nil {
		return err
	}
	select {
	case err := <-reply:
		return err
	case <-a.done:
		return ErrSessionActorStopped
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Interrupt cancels an active execution.
func (a *SessionActor) Interrupt(ctx context.Context) error {
	if a == nil {
		return fmt.Errorf("session actor is nil")
	}
	a.Start()
	reply := make(chan error, 1)
	cmd := Interrupt{Ctx: ctx, Reply: reply}
	if err := a.send(ctx, cmd); err != nil {
		return err
	}
	select {
	case err := <-reply:
		return err
	case <-a.done:
		return ErrSessionActorStopped
	case <-ctx.Done():
		return ctx.Err()
	}
}

// RewindTo requests a rewind to a checkpoint.
func (a *SessionActor) RewindTo(ctx context.Context, checkpointID, mode string) error {
	_, err := a.Rewind(ctx, checkpointID, mode)
	return err
}

// Rewind requests a rewind to a checkpoint and returns the restore result.
func (a *SessionActor) Rewind(ctx context.Context, checkpointID, mode string) (*checkpoint.RestoreResult, error) {
	if a == nil {
		return nil, fmt.Errorf("session actor is nil")
	}
	a.Start()
	reply := make(chan RewindResult, 1)
	cmd := RewindTo{Ctx: ctx, CheckpointID: checkpointID, Mode: mode, Reply: reply}
	if err := a.send(ctx, cmd); err != nil {
		return nil, err
	}
	select {
	case res := <-reply:
		return res.Result, res.Err
	case <-a.done:
		return nil, ErrSessionActorStopped
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Compact triggers a manual session compaction and returns the compaction result.
func (a *SessionActor) Compact(ctx context.Context, mode string) (*compactruntime.Result, compactruntime.Status, error) {
	if a == nil {
		return nil, compactruntime.Status{}, fmt.Errorf("session actor is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	a.Start()
	reply := make(chan CompactResult, 1)
	cmd := CompactSession{
		Ctx:   ctx,
		Mode:  mode,
		Reply: reply,
	}
	if err := a.send(ctx, cmd); err != nil {
		return nil, compactruntime.Status{}, err
	}
	select {
	case res := <-reply:
		return res.Result, res.Status, res.Err
	case <-a.done:
		return nil, compactruntime.Status{}, ErrSessionActorStopped
	case <-ctx.Done():
		return nil, compactruntime.Status{}, ctx.Err()
	}
}

// PreviewCheckpoint returns a checkpoint restore preview without applying it.
func (a *SessionActor) PreviewCheckpoint(ctx context.Context, checkpointID, mode string) (*checkpoint.RestoreResult, error) {
	if a == nil {
		return nil, fmt.Errorf("session actor is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	checkpointID = strings.TrimSpace(checkpointID)
	if checkpointID == "" {
		return nil, fmt.Errorf("checkpoint id is required")
	}
	checkpointMgr := a.agent.GetCheckpointRestoreManager()
	if checkpointMgr == nil {
		return nil, fmt.Errorf("checkpoint manager is not configured")
	}
	mode = strings.TrimSpace(mode)
	if mode == "" {
		mode = string(checkpoint.RestoreCode)
	}
	return checkpointMgr.Restore(ctx, checkpoint.RestoreRequest{
		SessionID:    a.id,
		CheckpointID: checkpointID,
		Mode:         checkpoint.RestoreMode(mode),
		PreviewOnly:  true,
	})
}

// ListCheckpoints returns checkpoints associated with this session.
func (a *SessionActor) ListCheckpoints(ctx context.Context, limit, offset int) ([]artifact.Checkpoint, error) {
	if a == nil {
		return nil, fmt.Errorf("session actor is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	store := a.agent.GetArtifactStore()
	if store == nil {
		return nil, fmt.Errorf("artifact store is not configured")
	}
	return store.ListCheckpoints(ctx, a.id, limit, offset)
}

// GetCheckpointFiles returns file metadata for a checkpoint.
func (a *SessionActor) GetCheckpointFiles(ctx context.Context, checkpointID string) ([]artifact.CheckpointFile, error) {
	if a == nil {
		return nil, fmt.Errorf("session actor is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	checkpointID = strings.TrimSpace(checkpointID)
	if checkpointID == "" {
		return nil, fmt.Errorf("checkpoint id is required")
	}
	store := a.agent.GetArtifactStore()
	if store == nil {
		return nil, fmt.Errorf("artifact store is not configured")
	}
	checkpoint, err := store.GetCheckpoint(ctx, checkpointID)
	if err != nil {
		return nil, err
	}
	if checkpoint == nil {
		return nil, fmt.Errorf("checkpoint not found: %s", checkpointID)
	}
	if strings.TrimSpace(checkpoint.SessionID) != "" && strings.TrimSpace(checkpoint.SessionID) != strings.TrimSpace(a.id) {
		return nil, fmt.Errorf("checkpoint does not belong to session")
	}
	return store.GetCheckpointFiles(ctx, checkpointID)
}

// EnableStreaming 在运行中开启此会话的流式输出，使 LLM 增量事件
// （assistant_delta / reasoning_delta）实时推送到 EventBus。幂等。
func (a *SessionActor) EnableStreaming() {
	if a == nil {
		return
	}
	if a.agent != nil {
		a.agent.EnableStreaming()
	}
}

// DeliverMailboxMessage notifies the actor of a mailbox message.
func (a *SessionActor) DeliverMailboxMessage(ctx context.Context, message team.MailMessage) error {
	if a == nil {
		return fmt.Errorf("session actor is nil")
	}
	a.Start()
	reply := make(chan error, 1)
	cmd := DeliverMailboxMessage{Ctx: ctx, Message: message, Reply: reply}
	if err := a.send(ctx, cmd); err != nil {
		return err
	}
	select {
	case err := <-reply:
		return err
	case <-a.done:
		return ErrSessionActorStopped
	case <-ctx.Done():
		return ctx.Err()
	}
}

// SubscribeEvents wires a channel to the actor's event bus.
func (a *SessionActor) SubscribeEvents(ctx context.Context, eventType string, ch chan runtimeevents.Event) error {
	if a == nil {
		return fmt.Errorf("session actor is nil")
	}
	a.Start()
	reply := make(chan error, 1)
	cmd := SubscribeEvents{Ctx: ctx, EventType: eventType, Ch: ch, Reply: reply}
	if err := a.send(ctx, cmd); err != nil {
		return err
	}
	select {
	case err := <-reply:
		return err
	case <-a.done:
		return ErrSessionActorStopped
	case <-ctx.Done():
		return ctx.Err()
	}
}

// State returns the current runtime state snapshot.
func (a *SessionActor) State() *RuntimeState {
	return a.cloneState(true, true)
}

// StateForInspection returns a defensive state snapshot without large tool
// surfaces or replayed tool-result bytes.
func (a *SessionActor) StateForInspection() *RuntimeState {
	return a.cloneState(false, false)
}

func (a *SessionActor) stateWithoutToolSurfaces() *RuntimeState {
	return a.cloneState(false, true)
}

func (a *SessionActor) cloneState(includeToolSurfaces, includeToolResult bool) *RuntimeState {
	if a == nil {
		return nil
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.state == nil {
		return nil
	}
	return a.state.clone(includeToolSurfaces, includeToolResult)
}

// StateSummary returns the small status projection without cloning tool schemas,
// pending receipt bytes, or other large recovery payloads.
func (a *SessionActor) StateSummary() (RuntimeStateSummary, bool) {
	if a == nil {
		return RuntimeStateSummary{}, false
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.state == nil {
		return RuntimeStateSummary{}, false
	}
	return a.state.Summary(), true
}

// PendingApproval returns only the approval payload required by approval UIs.
func (a *SessionActor) PendingApproval() *ApprovalRequest {
	if a == nil {
		return nil
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.state == nil || a.state.PendingApproval == nil {
		return nil
	}
	approval := *a.state.PendingApproval
	approval.ArgsJSON = append(json.RawMessage(nil), approval.ArgsJSON...)
	return &approval
}

// RunInFlight reports whether an in-process run currently owns this actor. A
// busy state backed by such a run converges on its own (bounded by the run
// stall watchdog); a busy state without one is the leftover of a process that
// died mid-turn and will never finish, so callers waiting for readiness must
// not treat both cases the same way.
func (a *SessionActor) RunInFlight() bool {
	if a == nil {
		return false
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	run := a.activeRun
	if run == nil {
		return false
	}
	return !run.abandoned.Load() && !run.interrupted.Load()
}

func (a *SessionActor) run() {
	defer func() {
		a.runStopHook()
		close(a.done)
	}()
	for {
		select {
		case cmd := <-a.cmdCh:
			a.handle(cmd)
		case <-a.stop:
			a.cancelActive()
			a.activeRunWG.Wait()
			return
		}
	}
}

func (a *SessionActor) handle(cmd Command) {
	switch payload := cmd.(type) {
	case SubmitPrompt:
		a.handleSubmitPrompt(payload)
	case ContinueSession:
		a.handleContinueSession(payload)
	case ApproveTool:
		a.handleApproveTool(payload)
	case AnswerQuestion:
		a.handleAnswerQuestion(payload)
	case Interrupt:
		a.handleInterrupt(payload)
	case RewindTo:
		a.handleRewindTo(payload)
	case BacktrackTo:
		a.handleBacktrackTo(payload)
	case CompactSession:
		a.handleCompactSession(payload)
	case DeliverMailboxMessage:
		a.handleDeliverMailboxMessage(payload)
	case SubscribeEvents:
		a.handleSubscribeEvents(payload)
	}
}

func (a *SessionActor) handleSubmitPrompt(cmd SubmitPrompt) {
	reply := cmd.Reply
	if reply == nil {
		return
	}
	ctx := cmd.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	prompt := strings.TrimSpace(cmd.Prompt)
	if prompt == "" {
		reply <- SubmitResult{Err: fmt.Errorf("prompt is empty")}
		return
	}
	if err := a.ensureReady(); err != nil {
		reply <- SubmitResult{Err: err}
		return
	}
	a.noteRunTriggerOrigin(cmd.TriggerTurnAuto)
	turnID := "turn_" + uuid.NewString()
	run := a.claimSessionRun(turnID, nil, reply)
	runCtx := withSessionRunControl(ctx, run)
	session, err := a.loadSession(runCtx)
	if err != nil {
		a.releaseSessionRun(run)
		run.complete(SubmitResult{Err: err})
		return
	}
	preparedPrompt := llm.NewUserPromptMessage(prompt)
	if len(cmd.ImagePaths) > 0 {
		msg, err := llm.NewUserPromptMessageWithImages(prompt, cmd.ImagePaths)
		if err != nil {
			a.releaseSessionRun(run)
			run.complete(SubmitResult{Err: fmt.Errorf("resolving image attachments: %w", err)})
			return
		}
		preparedPrompt = msg
	}
	if cmd.ImageArtifactDir != "" && preparedPrompt != nil {
		if persistErr := llm.PersistLocalInputImages(preparedPrompt, cmd.ImageArtifactDir); persistErr != nil {
			// Non-fatal: log but don't block the prompt
			a.eventBus.Publish(runtimeevents.Event{
				Type:    "image_persist_error",
				Payload: map[string]interface{}{"error": persistErr.Error()},
			})
		}
	}
	appendedPrompt := false
	if shouldAppendUserPromptMessage(session.LastMessage(), preparedPrompt) {
		session.AddMessage(*preparedPrompt)
		if err := a.persistSession(runCtx, session); err != nil {
			a.releaseSessionRun(run)
			run.complete(SubmitResult{Err: err})
			return
		}
		appendedPrompt = true
	}

	if err := a.updateState(ctx, func(state *RuntimeState) error {
		state.Status = SessionRunning
		state.CurrentTurnID = turnID
		state.CurrentRunMeta = cmd.RunMeta.Clone()
		resetFrozenTurnTools(state)
		// A new run invalidates the previous run's terminal marker (P0-1).
		state.LastRunTerminalReason = ""
		state.UpdatedAt = time.Now().UTC()
		return nil
	}); err != nil {
		a.releaseSessionRun(run)
		run.complete(SubmitResult{Err: err})
		return
	}
	var injection *turnInjection
	if len(cmd.TurnSystemMessages) > 0 || len(cmd.TurnPinnedTools) > 0 {
		injection = &turnInjection{
			systemMessages: cmd.TurnSystemMessages,
			pinnedTools:    cmd.TurnPinnedTools,
		}
	}
	a.startSessionRun(runCtx, session, prompt, false, turnID, cmd.RunMeta, cmd.RouteOverride, injection, reply, appendedPrompt, run)
}

func (a *SessionActor) handleContinueSession(cmd ContinueSession) {
	reply := cmd.Reply
	if reply == nil {
		return
	}
	ctx := cmd.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	if err := a.ensureReady(); err != nil {
		reply <- SubmitResult{Err: err}
		return
	}
	a.noteRunTriggerOrigin(false)
	turnID := "turn_" + uuid.NewString()
	run := a.claimSessionRun(turnID, cmd.StripMetadataKeys, reply)
	runCtx := withSessionRunControl(ctx, run)
	session, err := a.loadSession(runCtx)
	if err != nil {
		a.releaseSessionRun(run)
		run.complete(SubmitResult{Err: err})
		return
	}
	appendTransientContinuationPrompt(session, cmd.ContinuationPrompt, cmd.ContinuationMetadata)
	if err := a.updateState(ctx, func(state *RuntimeState) error {
		state.Status = SessionRunning
		state.CurrentTurnID = turnID
		state.CurrentRunMeta = cmd.RunMeta.Clone()
		resetFrozenTurnTools(state)
		// A new run invalidates the previous run's terminal marker (P0-1).
		state.LastRunTerminalReason = ""
		state.UpdatedAt = time.Now().UTC()
		return nil
	}); err != nil {
		a.releaseSessionRun(run)
		run.complete(SubmitResult{Err: err})
		return
	}
	a.startSessionRun(runCtx, session, "", true, turnID, cmd.RunMeta, runRouteOverrideFromRunMeta(cmd.RunMeta), nil, reply, false, run)
}

func (a *SessionActor) handleApproveTool(cmd ApproveTool) {
	if cmd.Reply == nil {
		return
	}
	ctx := cmd.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	// The approval arrives through the broker carrying the *calling* session's
	// run token. It must not be judged against this session's active run, or
	// every external approval would fail as "session run was superseded".
	ctx = a.detachForeignSessionRunControl(ctx)
	state := a.stateWithoutToolSurfaces()
	if state == nil {
		cmd.Reply <- fmt.Errorf("approval request not found")
		return
	}
	// 幂等：审批已被其它入口（如 web client）处理过，不再报错。
	if state.PendingApproval == nil {
		// P0-3: after a restart the retirement is visible only through durable
		// state. A terminal-run marker with no pending approval means the
		// decision died with its run, so report it as recorded-but-not-applied
		// instead of implying it was honoured.
		if reason := strings.TrimSpace(state.LastRunTerminalReason); a.terminalGuard && reason != "" && !a.hasApprovalWaiter(cmd.RequestID) {
			if recorded, _ := a.ApprovalOutcome(cmd.RequestID); recorded == "" {
				a.recordApprovalOutcome(cmd.RequestID, ApprovalResolutionRunTerminalNoResume, false)
			}
		}
		cmd.Reply <- nil
		return
	}
	if state.PendingApproval.ID != cmd.RequestID {
		cmd.Reply <- fmt.Errorf("approval request not found")
		return
	}
	// P0-1: the run already ended in a terminal cancellation class, so this
	// decision must not restart it. Report the outcome, retire the stale pending
	// approval durably, and never resume.
	if reason, terminal := a.pendingApprovalRunTerminal(state, cmd.RequestID); terminal {
		a.resolveApproval(cmd.RequestID, runtimepolicy.ApprovalResponse{
			Allowed: false,
			Reason:  ApprovalResolutionRunTerminalNoResume,
		})
		if err := a.updateState(ctx, func(runtimeState *RuntimeState) error {
			if runtimeState.PendingApproval != nil && runtimeState.PendingApproval.ID == cmd.RequestID {
				runtimeState.PendingApproval = nil
				runtimeState.PendingTool = nil
			}
			if strings.TrimSpace(runtimeState.CurrentTurnID) == "" {
				runtimeState.Status = SessionStopped
			}
			runtimeState.UpdatedAt = time.Now().UTC()
			return nil
		}); err != nil {
			cmd.Reply <- err
			return
		}
		payload := approvalResolvedEventPayload(state, cmd.RequestID, cmd.Allow)
		payload["resolution"] = ApprovalResolutionRunTerminalNoResume
		payload["resumed"] = false
		payload["run_terminal_reason"] = reason
		a.publish(runtimeevents.Event{Type: EventApprovalResolved, SessionID: a.id, Payload: payload})
		a.recordApprovalOutcome(cmd.RequestID, ApprovalResolutionRunTerminalNoResume, false)
		cmd.Reply <- nil
		return
	}
	if approvalRequestExpired(state.PendingApproval, time.Now().UTC()) {
		expiryErr := approvalExpiredError(state.PendingApproval)
		response := runtimepolicy.ApprovalResponse{Allowed: false, Reason: expiryErr.Error()}
		if a.resolveApproval(cmd.RequestID, response) {
			if err := a.updateState(ctx, func(runtimeState *RuntimeState) error {
				if runtimeState.PendingApproval == nil || runtimeState.PendingApproval.ID != cmd.RequestID {
					return fmt.Errorf("approval request not found")
				}
				runtimeState.PendingApproval = nil
				runtimeState.PendingTool = nil
				runtimeState.Status = approvalResumeStatus(runtimeState)
				runtimeState.UpdatedAt = time.Now().UTC()
				return nil
			}); err != nil {
				cmd.Reply <- err
				return
			}
		} else if err := a.resumePendingToolWithResult(ctx, state, nil, expiryErr.Error(), approvalErrorMetadata(expiryErr)); err != nil {
			cmd.Reply <- err
			return
		}
		payload := approvalResolvedEventPayload(state, cmd.RequestID, false)
		payload["resolution"] = ApprovalResolutionExpired
		payload["resumed"] = false
		payload["error_code"] = string(runtimeerrors.ErrApprovalExpired)
		a.publish(runtimeevents.Event{Type: EventApprovalResolved, SessionID: a.id, Payload: payload})
		a.recordApprovalOutcome(cmd.RequestID, ApprovalResolutionExpired, false)
		cmd.Reply <- expiryErr
		return
	}
	resumed := false
	if a.resolveApproval(cmd.RequestID, runtimepolicy.ApprovalResponse{
		Allowed:     cmd.Allow,
		PatchedArgs: cmd.PatchedArgs,
	}) {
		// A blocked run picked the decision up in place: nothing is restarted.
		if err := a.updateState(ctx, func(state *RuntimeState) error {
			if state.PendingApproval == nil || state.PendingApproval.ID != cmd.RequestID {
				return fmt.Errorf("approval request not found")
			}
			state.PendingApproval = nil
			state.PendingTool = nil
			state.Status = SessionRunning
			state.UpdatedAt = time.Now().UTC()
			return nil
		}); err != nil {
			cmd.Reply <- err
			return
		}
	} else if !cmd.Allow {
		if err := a.resumePendingToolWithResult(ctx, state, nil, "approval_denied"); err != nil {
			cmd.Reply <- err
			return
		}
		resumed = true
	} else {
		if err := a.resumeApprovedPendingTool(ctx, state, cmd.PatchedArgs); err != nil {
			cmd.Reply <- err
			return
		}
		resumed = true
	}
	resolution := ApprovalResolutionDenied
	if cmd.Allow {
		resolution = ApprovalResolutionAllowed
	}
	payload := approvalResolvedEventPayload(state, cmd.RequestID, cmd.Allow)
	payload["resolution"] = resolution
	payload["resumed"] = resumed
	a.recordApprovalOutcome(cmd.RequestID, resolution, resumed)
	a.publish(runtimeevents.Event{
		Type:      EventApprovalResolved,
		SessionID: a.id,
		Payload:   payload,
	})
	cmd.Reply <- nil
}

func (a *SessionActor) handleAnswerQuestion(cmd AnswerQuestion) {
	if cmd.Reply == nil {
		return
	}
	ctx := cmd.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	// Same foreign-token rule as handleApproveTool: answering a child's question
	// from another session's run must not be refused as a superseded run.
	ctx = a.detachForeignSessionRunControl(ctx)
	state := a.StateForInspection()
	if state == nil {
		cmd.Reply <- fmt.Errorf("question request not found")
		return
	}
	// 幂等：问题已被其它入口（如 web client）回答过，不再报错。
	if state.PendingQuestion == nil {
		cmd.Reply <- nil
		return
	}
	if state.PendingQuestion.ID != cmd.QuestionID {
		cmd.Reply <- fmt.Errorf("question request not found")
		return
	}
	if a.resolveQuestion(cmd.QuestionID, cmd.Answer) {
		if err := a.updateState(ctx, func(state *RuntimeState) error {
			if state.PendingQuestion == nil || state.PendingQuestion.ID != cmd.QuestionID {
				return fmt.Errorf("question request not found")
			}
			state.PendingQuestion = nil
			state.PendingTool = nil
			state.Status = SessionRunning
			state.UpdatedAt = time.Now().UTC()
			return nil
		}); err != nil {
			cmd.Reply <- err
			return
		}
	} else {
		result := toolbroker.AskUserQuestionResult{
			QuestionID: cmd.QuestionID,
			Answer:     cmd.Answer,
		}
		if err := a.resumePendingToolWithResult(ctx, state, result, ""); err != nil {
			cmd.Reply <- err
			return
		}
	}
	payload := map[string]interface{}{
		"question_id": cmd.QuestionID,
		"answer":      cmd.Answer,
	}
	if turnID := strings.TrimSpace(state.CurrentTurnID); turnID != "" {
		payload["turn_id"] = turnID
	}
	a.publish(runtimeevents.Event{Type: EventQuestionAnswered, SessionID: a.id, Payload: payload})
	cmd.Reply <- nil
}

func (a *SessionActor) handleInterrupt(cmd Interrupt) {
	if cmd.Reply == nil {
		return
	}
	run := a.interruptActiveSessionRun()
	_ = a.updateStateConvergent(context.Background(), func(state *RuntimeState) error {
		state.Status = SessionStopped
		state.CurrentTurnID = ""
		state.CurrentRunMeta = nil
		resetFrozenTurnTools(state)
		state.PendingTool = nil
		state.PendingApproval = nil
		state.PendingQuestion = nil
		state.UpdatedAt = time.Now().UTC()
		return nil
	})
	payload := map[string]interface{}{
		"reason": "interrupt",
	}
	if run != nil && strings.TrimSpace(run.turnID) != "" {
		payload["turn_id"] = run.turnID
	}
	a.publish(runtimeevents.Event{
		Type:      EventSessionInterrupted,
		SessionID: a.id,
		TraceID:   sessionRunTurnID(run),
		Payload:   payload,
	})
	if run != nil {
		// Give a cancel-aware execution tail a short opportunity to return its
		// structured Result/telemetry. A provider that ignores cancellation still
		// cannot strand the public caller indefinitely.
		a.retireInterruptedSessionRunAfter(run, interruptedRunReplyGrace)
	}
	cmd.Reply <- nil
}

// touchRunActivity marks run as making progress when it still owns the actor
// and the event is not explicitly stamped for another turn.
func (a *SessionActor) touchRunActivity(run *sessionRunControl, event runtimeevents.Event) {
	if run == nil || !a.sessionRunOwned(run) || !sessionRunEventMatches(run, event, a.id) {
		return
	}
	run.lastActivity.Store(time.Now().UnixNano())
}

// startRunStallWatchdog launches the stall watchdog for a run and returns a
// stop function (never nil) that unsubscribes progress events and halts it.
// A stall is judged as zero progress events for RunStallTimeout; when hit the
// run is force-aborted, the busy state released and OnRunStalled invoked.
// RunStallTimeout <= 0 disables the watchdog, which is the default: automation
// must be able to run to completion, so an unexpected context.Canceled from
// this watchdog only happens when a caller explicitly opts in.
func (a *SessionActor) startRunStallWatchdog(runCtx context.Context, run *sessionRunControl) func() {
	if a == nil || run == nil || a.runStallTimeout <= 0 {
		return func() {}
	}
	run.lastActivity.Store(time.Now().UnixNano())
	var unsubs []runtimeevents.Unsubscribe
	// Loop 的 assistant_delta/assistant.reasoning 与工具事件 emit 到 agent 的
	// bus；actor 自身的结构化事件走 a.eventBus。两处都订阅，任一进展都算。
	seen := map[*runtimeevents.Bus]bool{}
	if a.eventBus != nil {
		seen[a.eventBus] = true
	}
	if a.agent != nil {
		if agentBus := a.agent.GetEventBus(); agentBus != nil {
			seen[agentBus] = true
		}
	}
	for bus := range seen {
		for _, t := range []string{
			"assistant_delta",
			"assistant.reasoning",
			"tool.requested",
			// tool.progress（toolprotocol.EventTypeProgress）同样是进展信号：
			// 长任务可能长时间只上报进度而没有 tool.completed，不能因此被
			// 误判为挂死。
			"tool.progress",
			"tool.completed",
		} {
			u := bus.SubscribeCancelable(t, func(event runtimeevents.Event) {
				a.touchRunActivity(run, event)
			})
			unsubs = append(unsubs, u)
		}
	}
	interval := a.runStallTimeout / 6
	if interval < 10*time.Millisecond {
		interval = 10 * time.Millisecond
	}
	if interval > 30*time.Second {
		interval = 30 * time.Second
	}
	done := make(chan struct{})
	stopOnce := sync.Once{}
	cleanup := func() {
		stopOnce.Do(func() {
			select {
			case <-done:
			default:
				close(done)
			}
			for _, u := range unsubs {
				if u != nil {
					u()
				}
			}
		})
	}
	go func() {
		// 无论因何退出（run 结束 / 主动停止 / 触发中止），都清理订阅，
		// 避免 run goroutine 卡死永不返回时总线订阅泄漏。
		defer cleanup()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-done:
				return
			case <-ticker.C:
				last := time.Unix(0, run.lastActivity.Load())
				if time.Since(last) >= a.runStallTimeout {
					a.abortStalledRun(run)
					return
				}
			}
		}
	}()
	return cleanup
}

// abortStalledRun force-terminates a run the stall watchdog judged hung and
// releases the busy state so the session is usable again without a restart.
// It mirrors the interrupt path: mark interrupted, cancel the run context,
// write back stopped, publish the event and notify the host (lease release).
func (a *SessionActor) abortStalledRun(run *sessionRunControl) {
	if !a.abandonSessionRun(run) {
		return
	}
	_ = a.updateStateConvergent(context.Background(), func(state *RuntimeState) error {
		if state.CurrentTurnID != run.turnID {
			return nil
		}
		state.Status = SessionStopped
		state.CurrentTurnID = ""
		state.CurrentRunMeta = nil
		resetFrozenTurnTools(state)
		state.PendingTool = nil
		state.PendingApproval = nil
		state.PendingQuestion = nil
		state.UpdatedAt = time.Now().UTC()
		return nil
	})
	// Complete before event persistence/callbacks: those integrations must not
	// keep the public submit caller blocked after runtime state is released.
	run.complete(SubmitResult{Err: context.Canceled})
	// The abandoned provider may never return. The local runtime state is now
	// non-busy (durable persistence was attempted), so its goroutine must not
	// keep Stop/StopContext waiting forever.
	a.releaseDetachedSessionRunWait(run)
	// Lease release must not sit behind an unbounded event-store append.
	if a.onRunStalled != nil {
		a.onRunStalled(run.turnID)
	}
	a.publish(runtimeevents.Event{
		Type:      EventSessionInterrupted,
		SessionID: a.id,
		TraceID:   run.turnID,
		Payload: map[string]interface{}{
			"reason":     "stall_timeout",
			"turn_id":    run.turnID,
			"timeout_ns": a.runStallTimeout,
		},
	})
}

func (a *SessionActor) handleRewindTo(cmd RewindTo) {
	if cmd.Reply == nil {
		return
	}
	ctx := cmd.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	_ = a.updateStateConvergent(ctx, func(state *RuntimeState) error {
		state.Status = SessionRewinding
		state.CurrentCheckpointID = cmd.CheckpointID
		state.UpdatedAt = time.Now().UTC()
		return nil
	})
	a.publish(runtimeevents.Event{
		Type:      EventRewindStarted,
		SessionID: a.id,
		Payload: map[string]interface{}{
			"checkpoint_id": cmd.CheckpointID,
			"mode":          cmd.Mode,
		},
	})

	checkpointMgr := a.agent.GetCheckpointRestoreManager()
	if checkpointMgr == nil {
		cmd.Reply <- RewindResult{Err: fmt.Errorf("checkpoint manager is not configured")}
		return
	}

	mode := strings.ToLower(strings.TrimSpace(cmd.Mode))
	var (
		result *checkpoint.RestoreResult
		err    error
	)
	restoreMode := checkpoint.RestoreMode(mode)
	if mode == "" {
		restoreMode = checkpoint.RestoreCode
	}
	switch restoreMode {
	case checkpoint.RestoreCode, checkpoint.RestoreConversation, checkpoint.RestoreBoth:
		result, err = checkpointMgr.Restore(ctx, checkpoint.RestoreRequest{
			SessionID:    a.id,
			CheckpointID: cmd.CheckpointID,
			Mode:         restoreMode,
		})
		if err == nil && result != nil && result.ConversationChanged {
			err = a.applyConversationRestore(ctx, result)
		}
	default:
		err = fmt.Errorf("unsupported rewind mode: %s", cmd.Mode)
	}
	status := SessionIdle
	if err != nil {
		status = SessionStopped
	}
	_ = a.updateStateConvergent(ctx, func(state *RuntimeState) error {
		state.Status = status
		state.UpdatedAt = time.Now().UTC()
		return nil
	})
	payload := map[string]interface{}{
		"checkpoint_id": cmd.CheckpointID,
		"mode":          cmd.Mode,
		"error":         errorString(err),
	}
	if result != nil {
		payload["applied_paths"] = result.AppliedPaths
		payload["errors"] = result.Errors
		payload["conversation_changed"] = result.ConversationChanged
		payload["conversation_head"] = result.ConversationHead
		payload["conversation_exact"] = result.ConversationExact
	}
	a.publish(runtimeevents.Event{
		Type:      EventRewindFinished,
		SessionID: a.id,
		Payload:   payload,
	})
	if hookMgr := a.agent.GetHookManager(); hookMgr != nil {
		hookPayload := map[string]interface{}{
			"session_id":    a.id,
			"checkpoint_id": cmd.CheckpointID,
			"mode":          cmd.Mode,
			"error":         errorString(err),
		}
		if result != nil {
			hookPayload["applied_paths"] = result.AppliedPaths
			hookPayload["errors"] = result.Errors
			hookPayload["conversation_changed"] = result.ConversationChanged
			hookPayload["conversation_head"] = result.ConversationHead
			hookPayload["conversation_exact"] = result.ConversationExact
		}
		hookMgr.DispatchAsync(ctx, runtimehooks.EventRewindCompleted, hookPayload)
	}
	cmd.Reply <- RewindResult{Result: result, Err: err}
}

func (a *SessionActor) handleCompactSession(cmd CompactSession) {
	if cmd.Reply == nil {
		return
	}
	ctx := cmd.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	if err := a.ensureReady(); err != nil {
		cmd.Reply <- CompactResult{
			Status: compactruntime.Status{
				Mode:   strings.TrimSpace(cmd.Mode),
				Phase:  compactruntime.PhasePreTurn,
				Reason: "session_busy",
			},
			Err: err,
		}
		return
	}

	session, err := a.loadSession(ctx)
	if err != nil {
		cmd.Reply <- CompactResult{
			Status: compactruntime.Status{
				Mode:   strings.TrimSpace(cmd.Mode),
				Phase:  compactruntime.PhasePreTurn,
				Reason: "load_session_failed",
			},
			Err: err,
		}
		return
	}

	result, status, err := a.runManualCompact(ctx, session, strings.TrimSpace(cmd.Mode))
	cmd.Reply <- CompactResult{Result: result, Status: status, Err: err}
}

func (a *SessionActor) applyConversationRestore(ctx context.Context, restore *checkpoint.RestoreResult) error {
	if restore == nil {
		return fmt.Errorf("restore result is nil")
	}
	if restore.ConversationExact {
		return a.applyConversationSnapshot(ctx, restore.ConversationMessages)
	}
	return a.applyConversationPrefix(ctx, restore.ConversationHead)
}

func (a *SessionActor) applyConversationSnapshot(ctx context.Context, messages []runtimetypes.Message) error {
	if a == nil {
		return fmt.Errorf("session actor is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	session, err := a.loadSession(ctx)
	if err != nil {
		return err
	}
	replaceSessionHistoryAndAdvancePromptCacheEpoch(session, messages)
	session.MarkHistoryTruncated()
	session.SetHeadOffset(0)
	if err := a.persistSession(ctx, session); err != nil {
		return err
	}
	_ = a.updateStateConvergent(ctx, func(state *RuntimeState) error {
		state.HeadOffset = 0
		state.UpdatedAt = time.Now().UTC()
		return nil
	})
	return nil
}

func (a *SessionActor) applyConversationPrefix(ctx context.Context, targetCount int) error {
	if a == nil {
		return fmt.Errorf("session actor is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	session, err := a.loadSession(ctx)
	if err != nil {
		return err
	}
	if targetCount < 0 {
		targetCount = 0
	}
	if targetCount > len(session.History) {
		targetCount = len(session.History)
	}
	cloned := make([]runtimetypes.Message, targetCount)
	for i := 0; i < targetCount; i++ {
		cloned[i] = *session.History[i].Clone()
	}
	replaceSessionHistoryAndAdvancePromptCacheEpoch(session, cloned)
	session.MarkHistoryTruncated()
	session.SetHeadOffset(0)
	if err := a.persistSession(ctx, session); err != nil {
		return err
	}
	_ = a.updateStateConvergent(ctx, func(state *RuntimeState) error {
		state.HeadOffset = 0
		state.UpdatedAt = time.Now().UTC()
		return nil
	})
	return nil
}

func (a *SessionActor) handleDeliverMailboxMessage(cmd DeliverMailboxMessage) {
	if cmd.Reply == nil {
		return
	}
	a.publish(NewMailboxReceivedEvent(a.id, cmd.Message))
	cmd.Reply <- nil
}

func (a *SessionActor) handleSubscribeEvents(cmd SubscribeEvents) {
	if cmd.Reply == nil {
		return
	}
	if a.eventBus == nil {
		cmd.Reply <- fmt.Errorf("event bus is not configured")
		return
	}
	if cmd.Ch == nil {
		cmd.Reply <- fmt.Errorf("event channel is required")
		return
	}
	handler := func(event runtimeevents.Event) {
		select {
		case cmd.Ch <- event:
		default:
		}
	}
	unsubscribe := a.eventBus.SubscribeCancelable(cmd.EventType, handler)
	if cmd.Ctx != nil {
		go func() {
			select {
			case <-cmd.Ctx.Done():
			case <-a.done:
			}
			unsubscribe()
		}()
	}
	cmd.Reply <- nil
}

func (a *SessionActor) send(ctx context.Context, cmd Command) error {
	if a == nil {
		return fmt.Errorf("session actor is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if a.IsStopped() {
		return ErrSessionActorStopped
	}
	select {
	case a.cmdCh <- cmd:
		return nil
	case <-a.stop:
		return ErrSessionActorStopped
	case <-a.done:
		return ErrSessionActorStopped
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (a *SessionActor) ensureReady() error {
	state, ok := a.StateSummary()
	if !ok {
		return nil
	}
	switch state.Status {
	case SessionRunning, SessionWaitingApproval, SessionWaitingInput, SessionRewinding:
		return fmt.Errorf("%w (%s)", ErrSessionBusy, state.Status)
	default:
		return nil
	}
}

func (a *SessionActor) runLoop(ctx context.Context, prompt string, session *Session, routeOverride *RunRouteOverride) (*agent.Result, error) {
	if a.agent == nil {
		return nil, fmt.Errorf("agent is not configured")
	}
	if result, matched, err := a.tryRouteSkill(ctx, prompt, session); matched || err != nil {
		return result, err
	}
	if a.llmRuntime == nil {
		return nil, fmt.Errorf("llm runtime is not configured")
	}
	runMeta, _ := team.GetRunMeta(ctx)
	loop := agent.NewReActLoop(a.agent, a.llmRuntime, a.historyCheckpointLoopConfig(routeOverride, runMeta, session))
	return loop.RunWithSession(ctx, prompt, session)
}

func (a *SessionActor) continueLoop(ctx context.Context, session *Session, routeOverride *RunRouteOverride) (*agent.Result, error) {
	if a.agent == nil {
		return nil, fmt.Errorf("agent is not configured")
	}
	if a.llmRuntime == nil {
		return nil, fmt.Errorf("llm runtime is not configured")
	}
	runMeta, _ := team.GetRunMeta(ctx)
	loop := agent.NewReActLoop(a.agent, a.llmRuntime, a.historyCheckpointLoopConfig(routeOverride, runMeta, session))
	return loop.ContinueWithSession(ctx, session)
}

func cloneLoopConfigWithRouteOverride(base *agent.LoopReActConfig, routeOverride *RunRouteOverride) *agent.LoopReActConfig {
	if base == nil && routeOverride == nil {
		return nil
	}
	cfg := agent.LoopReActConfig{
		MaxSteps:             0,
		EnableThought:        true,
		EnableToolCalls:      true,
		EnableParallelTools:  true,
		MaxParallelToolCalls: 4,
		Temperature:          0.7,
		StopOnSuccess:        true,
		MaxIterations:        10,
	}
	if base != nil {
		cfg = *base
		cfg.Thinking = runtimetypes.CloneThinkingConfig(base.Thinking)
	}
	if routeOverride != nil {
		if value := strings.TrimSpace(routeOverride.Provider); value != "" {
			cfg.Provider = value
		}
		if value := strings.TrimSpace(routeOverride.Model); value != "" {
			cfg.Model = value
		}
		if value := strings.TrimSpace(routeOverride.ReasoningEffort); value != "" {
			cfg.ReasoningEffort = value
		}
	}
	return &cfg
}

func cloneLoopConfigForRun(base *agent.LoopReActConfig, routeOverride *RunRouteOverride, runMeta *team.RunMeta) *agent.LoopReActConfig {
	cfg := cloneLoopConfigWithRouteOverride(base, routeOverride)
	if cfg == nil {
		if runMeta == nil {
			return nil
		}
		cfg = &agent.LoopReActConfig{
			MaxSteps:             0,
			EnableThought:        true,
			EnableToolCalls:      true,
			EnableParallelTools:  true,
			MaxParallelToolCalls: 4,
			Temperature:          0.7,
			StopOnSuccess:        true,
			MaxIterations:        10,
		}
	}
	// complete_task is a Team task terminal protocol, not a reusable session
	// default. A profile, persisted session, or lightweight spawn_agent RunMeta
	// may still contain the legacy value, but enforcing it without both TeamID
	// and CurrentTaskID would require tools that are intentionally hidden outside
	// an active Team task. Start from none, then accept an explicit per-run Team
	// assignment injected by TeammateRunner.
	cfg.CompletionRequirement = agent.CompletionRequirementNone
	if runMeta != nil && strings.TrimSpace(runMeta.CompletionRequirement) != "" {
		requirement := agent.NormalizeCompletionRequirement(team.EffectiveCompletionRequirement(runMeta))
		if requirement != agent.CompletionRequirementCompleteTask || hasBoundTeamTask(runMeta) {
			cfg.CompletionRequirement = requirement
		}
	}
	return cfg
}

func hasBoundTeamTask(runMeta *team.RunMeta) bool {
	return runMeta != nil &&
		runMeta.Team != nil &&
		strings.TrimSpace(runMeta.Team.TeamID) != "" &&
		strings.TrimSpace(runMeta.Team.CurrentTaskID) != ""
}

func runRouteOverrideFromRunMeta(runMeta *team.RunMeta) *RunRouteOverride {
	route := team.TaskExecutionRouteFromRunMeta(runMeta)
	if route == nil {
		return nil
	}
	override := &RunRouteOverride{
		Provider:        strings.TrimSpace(route.Provider),
		Model:           strings.TrimSpace(route.Model),
		ReasoningEffort: strings.TrimSpace(route.ReasoningEffort),
	}
	if override.Provider == "" && override.Model == "" && override.ReasoningEffort == "" {
		return nil
	}
	return override
}

func (a *SessionActor) tryRouteSkill(ctx context.Context, prompt string, session *Session) (*agent.Result, bool, error) {
	if a == nil || a.agent == nil {
		return nil, false, nil
	}
	if runMeta, ok := team.GetRunMeta(ctx); ok && runMeta != nil && runMeta.Team != nil && strings.TrimSpace(runMeta.Team.TeamID) != "" {
		return nil, false, nil
	}
	router := a.agent.GetSkillRouter()
	executor := a.agent.GetSkillExecutor()
	if router == nil || executor == nil {
		return nil, false, nil
	}
	routes := router.RouteDirect(ctx, prompt)
	if len(routes) == 0 || routes[0] == nil || routes[0].Skill == nil {
		return nil, false, nil
	}
	// SK-3：执行前刷新隐式调用判定索引，保证与当前技能面一致。
	executor.RefreshImplicitInvocationIndex()
	req := runtimetypes.NewRequest(prompt)
	req.History = routeHistoryForSkillPrompt(session, prompt)
	req.Metadata.Set("permissions", []string{"*"})
	skillResult, err := executor.Execute(ctx, routes[0].Skill, req)
	if err != nil {
		return &agent.Result{
			Success:  false,
			Output:   "",
			Skill:    routes[0].Skill.Name,
			Duration: req.Duration,
			Error:    err.Error(),
		}, true, err
	}
	if skillResult == nil {
		return &agent.Result{
			Success:  false,
			Output:   "",
			Skill:    routes[0].Skill.Name,
			Duration: req.Duration,
			Error:    "skill execution returned nil result",
		}, true, fmt.Errorf("skill execution returned nil result")
	}
	a.publishSkillInvocations(skillResult.ImplicitInvocations)
	if skillResult.Success {
		session.AddMessage(*runtimetypes.NewAssistantMessage(skillResult.Output))
	}
	req.MarkCompleted()
	return &agent.Result{
		Success:      skillResult.Success,
		Output:       skillResult.Output,
		Observations: skillResult.Observations,
		Skill:        routes[0].Skill.Name,
		Usage:        skillResult.Usage,
		Duration:     req.Duration,
		Error:        skillResult.Error,
	}, true, nil
}

// publishSkillInvocations 把技能回合命中的隐式调用发布为 skills.invoked（SK-3）。
// 与 skills API 口径一致：事件本身是总线级（Event.SessionID 留空，避免被 A 通道
// 记为未落盘丢弃），会话归属只写进载荷 session_id。
func (a *SessionActor) publishSkillInvocations(invocations []runtimeskill.ImplicitInvocation) {
	if a == nil || a.eventBus == nil || len(invocations) == 0 {
		return
	}
	for _, inv := range runtimeskill.DedupeInvocations(invocations) {
		payload := runtimeskill.SkillInvokedEventPayload(inv)
		if a.id != "" {
			payload["session_id"] = a.id
		}
		a.eventBus.Publish(runtimeevents.Event{
			Type:      runtimeskill.SkillInvokedEventType,
			AgentName: "chat-actor",
			Payload:   payload,
		})
	}
}

func routeHistoryForSkillPrompt(session *Session, prompt string) []runtimetypes.Message {
	if session == nil {
		return nil
	}
	history := session.GetMessages()
	if len(history) == 0 {
		return nil
	}
	last := history[len(history)-1]
	if last.Role == "user" && last.Content == prompt {
		history = history[:len(history)-1]
	}
	cloned := make([]runtimetypes.Message, len(history))
	for i := range history {
		cloned[i] = *history[i].Clone()
	}
	return cloned
}

func shouldAppendUserPromptMessage(last *runtimetypes.Message, prepared *runtimetypes.Message) bool {
	if prepared == nil {
		return false
	}
	if last == nil {
		return true
	}
	if last.Role != "user" || last.Content != prepared.Content {
		return true
	}
	return llm.MessageHasLocalInputImages(prepared) && !llm.MessageHasLocalInputImages(last)
}

func shouldRollbackFailedPrompt(execErr error, result *agent.Result, resume bool, appendedPrompt bool) bool {
	if execErr == nil || resume || !appendedPrompt {
		return false
	}
	if result == nil {
		return true
	}
	return strings.TrimSpace(result.Output) == "" && len(result.Observations) == 0
}

func (a *SessionActor) rollbackLastUserPrompt(ctx context.Context, session *Session, prompt string) error {
	if a == nil || session == nil {
		return nil
	}
	history := session.GetMessages()
	if len(history) == 0 {
		return nil
	}
	last := history[len(history)-1]
	if last.Role != "user" || last.Content != prompt {
		return nil
	}
	replaceSessionHistoryAndAdvancePromptCacheEpoch(session, history[:len(history)-1])
	return a.persistSession(ctx, session)
}

func countRuntimeChatContextTokens(llmRuntime *llm.LLMRuntime, messages []runtimetypes.Message) int {
	if len(messages) == 0 {
		return 0
	}
	if llmRuntime != nil {
		if count := llmRuntime.CountMessagesTokens(messages); count > 0 {
			return count
		}
	}
	total := 0
	for _, message := range messages {
		total += len(message.Role)/4 + len(message.Content)/4 + len(message.ToolCallID)/4 + 4
		for _, call := range message.ToolCalls {
			total += len(call.ID)/4 + len(call.Name)/4 + 4
			if len(call.Args) > 0 {
				if payload, err := json.Marshal(call.Args); err == nil {
					total += len(payload) / 4
				}
			}
		}
	}
	if total <= 0 {
		return 0
	}
	return total + 8
}

func runtimeSessionObservedTokenUsage(session *Session) int {
	value, _ := runtimeSessionObservedContextTokenUsage(session)
	return value
}

func runtimeSessionObservedContextTokenUsage(session *Session) (int, bool) {
	if session == nil {
		return 0, false
	}
	value, ok := session.GetContext(aicliRuntimeContextTokenCountKey)
	if !ok {
		return 0, false
	}
	return runtimeContextIntValue(value), true
}

func setRuntimeSessionObservedTokenUsage(session *Session, value int) {
	if session == nil {
		return
	}
	if session.Metadata.Context == nil {
		session.Metadata.Context = make(map[string]interface{})
	}
	if value > 0 {
		session.Metadata.Context[aicliRuntimeContextTokenCountKey] = value
		return
	}
	delete(session.Metadata.Context, aicliRuntimeContextTokenCountKey)
}

func runtimeSessionActiveContextTokens(runtime *llm.LLMRuntime, session *Session, history []runtimetypes.Message) (int, bool) {
	historyTokens := countRuntimeChatContextTokens(runtime, history)
	observed, ok := runtimeSessionObservedContextTokenUsage(session)
	if !ok || observed <= 0 {
		return historyTokens, false
	}
	observed += countRuntimeChatPendingTokensAfterLastModelGenerated(runtime, history)
	if historyTokens > observed {
		observed = historyTokens
	}
	return observed, true
}

func countRuntimeChatPendingTokensAfterLastModelGenerated(runtime *llm.LLMRuntime, history []runtimetypes.Message) int {
	lastAssistant := -1
	for index := len(history) - 1; index >= 0; index-- {
		if history[index].Role == "assistant" {
			lastAssistant = index
			break
		}
	}
	if lastAssistant < 0 || lastAssistant >= len(history)-1 {
		return 0
	}
	pending := make([]runtimetypes.Message, 0, len(history)-lastAssistant-1)
	for _, message := range history[lastAssistant+1:] {
		if strings.EqualFold(message.Metadata.GetString("context_stage", ""), "compaction") {
			continue
		}
		pending = append(pending, message)
	}
	return countRuntimeChatContextTokens(runtime, pending)
}

func runtimeContextIntValue(value interface{}) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int8:
		return int(typed)
	case int16:
		return int(typed)
	case int32:
		return int(typed)
	case int64:
		return int(typed)
	case uint:
		return int(typed)
	case uint8:
		return int(typed)
	case uint16:
		return int(typed)
	case uint32:
		return int(typed)
	case uint64:
		return int(typed)
	case float32:
		return int(typed)
	case float64:
		return int(typed)
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			return int(parsed)
		}
	case string:
		if parsed, err := strconv.Atoi(strings.TrimSpace(typed)); err == nil {
			return parsed
		}
	default:
		return 0
	}
	return 0
}

func (a *SessionActor) maybeAutoCompactSession(ctx context.Context, session *Session, turnID string, runMeta *team.RunMeta, resume bool) {
	if a == nil || session == nil || a.llmRuntime == nil || a.agent == nil {
		return
	}

	payload := map[string]interface{}{
		"session_id": a.id,
		"turn_id":    turnID,
		"phase":      compactruntime.PhasePreTurn,
		"mode":       compactruntime.ModeLocal,
	}
	if resume {
		payload["reason"] = "resume_run"
		a.publish(runtimeevents.Event{
			Type:      EventSessionCompactSkipped,
			SessionID: a.id,
			TraceID:   turnID,
			Payload:   payload,
		})
		return
	}

	state, _ := a.StateSummary()
	switch {
	case state.PendingTool:
		payload["reason"] = "pending_tool"
	case state.PendingApproval:
		payload["reason"] = "pending_approval"
	case state.PendingQuestion:
		payload["reason"] = "pending_question"
	}
	if payload["reason"] != nil {
		a.publish(runtimeevents.Event{
			Type:      EventSessionCompactSkipped,
			SessionID: a.id,
			TraceID:   turnID,
			Payload:   payload,
		})
		return
	}

	history := session.GetMessages()
	manager := a.agent.GetContextManager()
	keepRecent := 0
	cfg := a.agent.GetConfig()
	if manager != nil {
		keepRecent = manager.Budget.KeepRecentMessages
	}

	taskID := a.id
	if runMeta != nil && runMeta.Team != nil && strings.TrimSpace(runMeta.Team.CurrentTaskID) != "" {
		taskID = strings.TrimSpace(runMeta.Team.CurrentTaskID)
	}

	if allow, blockMsg := a.dispatchPreCompactHook(ctx, payload); !allow {
		payload["reason"] = "pre_compact_hook_blocked"
		if strings.TrimSpace(blockMsg) != "" {
			payload["hook_message"] = strings.TrimSpace(blockMsg)
		}
		a.publish(runtimeevents.Event{
			Type:      EventSessionCompactSkipped,
			SessionID: a.id,
			TraceID:   turnID,
			Payload:   payload,
		})
		return
	}

	observedTokens, hasObservedTokens := runtimeSessionActiveContextTokens(a.llmRuntime, session, history)
	runtime := compactruntime.New(a.llmRuntime, manager)
	result, status, err := runtime.MaybeCompact(ctx, compactruntime.Request{
		SessionID:          a.id,
		TaskID:             taskID,
		Provider:           firstNonEmpty(chatActorConfigValue(cfg, "provider"), a.llmRuntime.DefaultProvider()),
		Model:              firstNonEmpty(chatActorConfigValue(cfg, "model"), a.llmRuntime.DefaultModel()),
		History:            history,
		KeepRecentMessages: keepRecent,
		Phase:              compactruntime.PhasePreTurn,
		CountTokens:        a.llmRuntime.CountMessagesTokens,
		ObservedTokens:     observedTokens,
		HasObservedTokens:  hasObservedTokens,
		Tools:              a.compactToolSurface(ctx, turnID),
	})
	payload["reason"] = status.Reason
	payload["mode"] = status.Mode
	payload["token_before"] = status.TokenBefore
	payload["trigger_token_limit"] = status.TriggerTokenLimit
	payload["max_context_tokens"] = status.MaxContextTokens
	payload["provider"] = status.ResolvedProvider
	payload["model"] = status.ResolvedModel

	if status.TriggerTokenLimit > 0 && status.TokenBefore > status.TriggerTokenLimit {
		a.publish(runtimeevents.Event{
			Type:      EventSessionCompactStarted,
			SessionID: a.id,
			TraceID:   turnID,
			Payload:   cloneEventPayload(payload),
		})
	}

	if err != nil {
		payload["error"] = err.Error()
		a.publish(runtimeevents.Event{
			Type:      EventSessionCompactFailed,
			SessionID: a.id,
			TraceID:   turnID,
			Payload:   payload,
		})
		return
	}
	if result == nil {
		a.publish(runtimeevents.Event{
			Type:      EventSessionCompactSkipped,
			SessionID: a.id,
			TraceID:   turnID,
			Payload:   payload,
		})
		return
	}

	a.reconcileCompactResult(ctx, session, result)
	originalHistory := history
	// Capture root title before history rewrite for diagnostic lineage;
	// ReplaceHistory keeps the sticky derived/manual title unchanged.
	rootTitleHint := session.CompactRootTitleCandidate()
	titleSnapshot := snapshotSessionTitleState(session)
	previousPromptCacheEpoch := replaceSessionHistoryAndAdvancePromptCacheEpoch(session, result.ReplacementHistory)
	// Explicit session compaction rewrites an already-cacheable prefix outside
	// ReActLoop. Advance the same durable generation that RunWithSession reads
	// before the replacement history can be persisted.
	// Compact rewrites history in place; record parent/root lineage + generation
	// as diagnostics without changing the user-visible title.
	session.ApplyCompactTitleLineage(session.ID, rootTitleHint)
	if persistErr := a.persistSession(ctx, session); persistErr != nil {
		restoreSessionHistoryAndPromptCacheEpoch(session, originalHistory, previousPromptCacheEpoch)
		restoreSessionTitleState(session, titleSnapshot)
		payload["error"] = persistErr.Error()
		a.publish(runtimeevents.Event{
			Type:      EventSessionCompactFailed,
			SessionID: a.id,
			TraceID:   turnID,
			Payload:   payload,
		})
		return
	}

	payload["token_after"] = result.TokenAfter
	payload["compacted_messages"] = result.CompactedMessages
	payload["message_count_after"] = len(result.ReplacementHistory)
	if gen := contextIntValue(session.Metadata.Context, ContextCompactGeneration); gen > 0 {
		payload["compact_generation"] = gen
	}
	if rootTitle := contextStringValue(session.Metadata.Context, ContextCompactRootTitle); rootTitle != "" {
		payload["compact_root_title"] = rootTitle
	}
	if len(result.CheckpointIDs) > 0 {
		payload["checkpoint_ids"] = append([]string(nil), result.CheckpointIDs...)
		payload["checkpoint_id"] = result.CheckpointIDs[len(result.CheckpointIDs)-1]
	}
	a.publish(runtimeevents.Event{
		Type:      EventSessionCompactCompleted,
		SessionID: a.id,
		TraceID:   turnID,
		Payload:   payload,
	})
	a.dispatchPostCompactHook(ctx, payload)
	a.publishCompactReconciliation(turnID, result)
}

func (a *SessionActor) compactToolSurface(ctx context.Context, turnID string) []runtimetypes.ToolDefinition {
	if a == nil || strings.TrimSpace(turnID) == "" {
		return nil
	}
	snapshot := a.turnToolSurfaceSnapshot(turnID)
	if snapshot == nil {
		return nil
	}
	tools, cached, err := snapshot.LoadTurnToolSurface(ctx)
	if err != nil || !cached || len(tools) == 0 {
		return nil
	}
	return cloneRuntimeToolDefinitions(tools)
}

func (a *SessionActor) runManualCompact(
	ctx context.Context,
	session *Session,
	requestedMode string,
) (*compactruntime.Result, compactruntime.Status, error) {
	status := compactruntime.Status{
		Mode:  strings.TrimSpace(requestedMode),
		Phase: compactruntime.PhasePreTurn,
	}
	if a == nil || session == nil {
		status.Reason = "session_unavailable"
		return nil, status, fmt.Errorf("session actor is not ready")
	}
	if a.llmRuntime == nil || a.agent == nil {
		status.Reason = "runtime_unavailable"
		return nil, status, fmt.Errorf("llm runtime is not configured")
	}

	payload := map[string]interface{}{
		"session_id": a.id,
		"phase":      compactruntime.PhasePreTurn,
		"mode":       strings.TrimSpace(requestedMode),
		"manual":     true,
		"forced":     true,
	}
	if payload["mode"] == "" {
		payload["mode"] = compactruntime.ModeAuto
	}

	state, _ := a.StateSummary()
	switch {
	case state.PendingTool:
		status.Reason = "pending_tool"
	case state.PendingApproval:
		status.Reason = "pending_approval"
	case state.PendingQuestion:
		status.Reason = "pending_question"
	}
	if status.Reason != "" {
		payload["reason"] = status.Reason
		a.publish(runtimeevents.Event{
			Type:      EventSessionCompactSkipped,
			SessionID: a.id,
			TraceID:   "compact_" + uuid.NewString(),
			Payload:   payload,
		})
		return nil, status, nil
	}

	traceID := "compact_" + uuid.NewString()
	if allow, blockMsg := a.dispatchPreCompactHook(ctx, payload); !allow {
		status.Reason = "pre_compact_hook_blocked"
		payload["reason"] = status.Reason
		if strings.TrimSpace(blockMsg) != "" {
			payload["hook_message"] = strings.TrimSpace(blockMsg)
		}
		a.publish(runtimeevents.Event{
			Type:      EventSessionCompactSkipped,
			SessionID: a.id,
			TraceID:   traceID,
			Payload:   payload,
		})
		return nil, status, nil
	}

	manager := a.agent.GetContextManager()
	keepRecent := 0
	cfg := a.agent.GetConfig()
	if manager != nil {
		keepRecent = manager.Budget.KeepRecentMessages
	}

	taskID := a.id
	runtime := compactruntime.New(a.llmRuntime, manager)
	result, resolvedStatus, err := runtime.MaybeCompact(ctx, compactruntime.Request{
		SessionID:          a.id,
		TaskID:             taskID,
		Provider:           firstNonEmpty(chatActorConfigValue(cfg, "provider"), a.llmRuntime.DefaultProvider()),
		Model:              firstNonEmpty(chatActorConfigValue(cfg, "model"), a.llmRuntime.DefaultModel()),
		Mode:               strings.TrimSpace(requestedMode),
		Force:              true,
		History:            session.GetMessages(),
		KeepRecentMessages: keepRecent,
		Phase:              compactruntime.PhasePreTurn,
		CountTokens:        a.llmRuntime.CountMessagesTokens,
		ObservedTokens:     countRuntimeChatContextTokens(a.llmRuntime, session.GetMessages()),
		HasObservedTokens:  true,
		Tools:              a.compactToolSurface(ctx, traceID),
	})
	status = resolvedStatus
	payload["reason"] = status.Reason
	payload["mode"] = status.Mode
	payload["token_before"] = status.TokenBefore
	payload["trigger_token_limit"] = status.TriggerTokenLimit
	payload["max_context_tokens"] = status.MaxContextTokens
	payload["provider"] = status.ResolvedProvider
	payload["model"] = status.ResolvedModel

	a.publish(runtimeevents.Event{
		Type:      EventSessionCompactStarted,
		SessionID: a.id,
		TraceID:   traceID,
		Payload:   cloneEventPayload(payload),
	})

	if err != nil {
		payload["error"] = err.Error()
		a.publish(runtimeevents.Event{
			Type:      EventSessionCompactFailed,
			SessionID: a.id,
			TraceID:   traceID,
			Payload:   payload,
		})
		return nil, status, err
	}
	if result == nil {
		a.publish(runtimeevents.Event{
			Type:      EventSessionCompactSkipped,
			SessionID: a.id,
			TraceID:   traceID,
			Payload:   payload,
		})
		return nil, status, nil
	}

	a.reconcileCompactResult(ctx, session, result)
	originalHistory := session.GetMessages()
	// Capture root title before history rewrite for diagnostic lineage;
	// ReplaceHistory keeps the sticky derived/manual title unchanged.
	rootTitleHint := session.CompactRootTitleCandidate()
	titleSnapshot := snapshotSessionTitleState(session)
	previousPromptCacheEpoch := replaceSessionHistoryAndAdvancePromptCacheEpoch(session, result.ReplacementHistory)
	// Keep explicit actor-side compaction on the same durable cache-generation
	// lane as automatic ReAct session recovery.
	// Compact rewrites history in place; record parent/root lineage + generation
	// as diagnostics without changing the user-visible title.
	session.ApplyCompactTitleLineage(session.ID, rootTitleHint)
	if persistErr := a.persistSession(ctx, session); persistErr != nil {
		restoreSessionHistoryAndPromptCacheEpoch(session, originalHistory, previousPromptCacheEpoch)
		restoreSessionTitleState(session, titleSnapshot)
		payload["error"] = persistErr.Error()
		a.publish(runtimeevents.Event{
			Type:      EventSessionCompactFailed,
			SessionID: a.id,
			TraceID:   traceID,
			Payload:   payload,
		})
		return nil, status, persistErr
	}

	payload["token_after"] = result.TokenAfter
	payload["compacted_messages"] = result.CompactedMessages
	payload["message_count_after"] = len(result.ReplacementHistory)
	if gen := contextIntValue(session.Metadata.Context, ContextCompactGeneration); gen > 0 {
		payload["compact_generation"] = gen
	}
	if rootTitle := contextStringValue(session.Metadata.Context, ContextCompactRootTitle); rootTitle != "" {
		payload["compact_root_title"] = rootTitle
	}
	if len(result.CheckpointIDs) > 0 {
		payload["checkpoint_ids"] = append([]string(nil), result.CheckpointIDs...)
		payload["checkpoint_id"] = result.CheckpointIDs[len(result.CheckpointIDs)-1]
	}
	a.publish(runtimeevents.Event{
		Type:      EventSessionCompactCompleted,
		SessionID: a.id,
		TraceID:   traceID,
		Payload:   payload,
	})
	a.dispatchPostCompactHook(ctx, payload)
	a.publishCompactReconciliation(traceID, result)
	return result, status, nil
}

// dispatchPreCompactHook returns false when compaction should be skipped.
func (a *SessionActor) dispatchPreCompactHook(ctx context.Context, payload map[string]interface{}) (allow bool, message string) {
	if a == nil || a.agent == nil {
		return true, ""
	}
	hookMgr := a.agent.GetHookManager()
	if hookMgr == nil {
		return true, ""
	}
	decision, err := hookMgr.Dispatch(ctx, runtimehooks.EventPreCompact, cloneEventPayload(payload))
	if err != nil {
		return true, ""
	}
	if runtimehooks.IsBlockingAction(decision.Action) {
		msg := strings.TrimSpace(decision.Message)
		if msg == "" {
			msg = "pre_compact hook blocked compaction"
		}
		return false, msg
	}
	return true, strings.TrimSpace(decision.Message)
}

// dispatchPostCompactHook notifies hooks after a successful compaction.
func (a *SessionActor) dispatchPostCompactHook(ctx context.Context, payload map[string]interface{}) {
	if a == nil || a.agent == nil {
		return
	}
	hookMgr := a.agent.GetHookManager()
	if hookMgr == nil {
		return
	}
	hookMgr.DispatchAsync(ctx, runtimehooks.EventPostCompact, cloneEventPayload(payload))
}

func cloneEventPayload(input map[string]interface{}) map[string]interface{} {
	if len(input) == 0 {
		return map[string]interface{}{}
	}
	cloned := make(map[string]interface{}, len(input))
	for key, value := range input {
		cloned[key] = value
	}
	return cloned
}

func mergeContinueOptions(opts []ContinueOption) ContinueOption {
	var merged ContinueOption
	for _, opt := range opts {
		if strings.TrimSpace(opt.ContinuationPrompt) != "" {
			merged.ContinuationPrompt = opt.ContinuationPrompt
		}
		if len(opt.ContinuationMetadata) > 0 {
			if merged.ContinuationMetadata == nil {
				merged.ContinuationMetadata = make(map[string]interface{})
			}
			for key, value := range opt.ContinuationMetadata {
				if strings.TrimSpace(key) != "" {
					merged.ContinuationMetadata[key] = value
				}
			}
		}
		for _, key := range opt.StripMetadataKeys {
			if strings.TrimSpace(key) != "" {
				merged.StripMetadataKeys = append(merged.StripMetadataKeys, strings.TrimSpace(key))
			}
		}
	}
	return merged
}

func appendTransientContinuationPrompt(session *Session, prompt string, metadata map[string]interface{}) {
	if session == nil || strings.TrimSpace(prompt) == "" {
		return
	}
	message := runtimetypes.NewUserMessage(prompt)
	for key, value := range metadata {
		key = strings.TrimSpace(key)
		if key != "" {
			message.Metadata.Set(key, value)
		}
	}
	// Mark the prompt as request-scoped. Auto-continuation / audit prompts are
	// replayed to the model, not typed by the user: without the marker the
	// durable write path stores them as fresh user turns, which is exactly the
	// duplicated user message the workspace then renders.
	message.Metadata.Set(runtimetypes.MetadataKeyTransientPrompt, true)
	session.AddMessage(*message)
}

func stripMessagesWithMetadataKeys(session *Session, keys []string) {
	if session == nil || len(keys) == 0 {
		return
	}
	history := session.GetMessages()
	if len(history) == 0 {
		return
	}
	stripped := make([]runtimetypes.Message, 0, len(history))
	changed := false
	for _, message := range history {
		if messageHasAnyMetadataKey(message, keys) {
			changed = true
			continue
		}
		stripped = append(stripped, message)
	}
	if changed {
		replaceSessionHistoryAndAdvancePromptCacheEpoch(session, stripped)
	}
}

// replaceSessionHistoryAndAdvancePromptCacheEpoch is the actor-side boundary
// for intentional history rewrites. Append-only AddMessage paths retain their
// current generation; restore, rollback, compaction, and transient-message
// removal move to a new one before the replacement can be persisted.
func replaceSessionHistoryAndAdvancePromptCacheEpoch(session *Session, messages []runtimetypes.Message) int {
	if session == nil {
		return 0
	}
	previous := contextIntValue(session.Metadata.Context, agent.PromptCacheEpochSessionContextKey)
	session.ReplaceHistory(messages)
	session.SetContext(agent.PromptCacheEpochSessionContextKey, previous+1)
	return previous
}

func restoreSessionHistoryAndPromptCacheEpoch(session *Session, messages []runtimetypes.Message, epoch int) {
	if session == nil {
		return
	}
	session.ReplaceHistory(messages)
	if epoch < 0 {
		epoch = 0
	}
	session.SetContext(agent.PromptCacheEpochSessionContextKey, epoch)
}

func messageHasAnyMetadataKey(message runtimetypes.Message, keys []string) bool {
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if _, ok := message.Metadata.Get(key); ok {
			return true
		}
	}
	return false
}

func normalizedMetadataKeys(keys []string) []string {
	if len(keys) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(keys))
	normalized := make([]string, 0, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		normalized = append(normalized, key)
	}
	return normalized
}

func (a *SessionActor) startSessionRun(ctx context.Context, session *Session, prompt string, resume bool, turnID string, runMeta *team.RunMeta, routeOverride *RunRouteOverride, injection *turnInjection, reply chan SubmitResult, appendedPrompt bool, run *sessionRunControl) {
	if a == nil || session == nil {
		if a != nil {
			a.releaseSessionRun(run)
		}
		if run != nil {
			run.complete(SubmitResult{Err: fmt.Errorf("session is nil")})
		} else if reply != nil {
			reply <- SubmitResult{Err: fmt.Errorf("session is nil")}
		}
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(turnID) == "" {
		turnID = "turn_" + uuid.NewString()
	}
	if run == nil || run.turnID != turnID {
		run = a.claimSessionRun(turnID, nil, reply)
	}
	ctx = withSessionRunControl(ctx, run)
	runCtx, cancel := context.WithCancel(ctx)
	approvalDetach := &approvalDetachState{}
	runCtx = context.WithValue(runCtx, approvalDetachContextKey{}, approvalDetach)
	runCtx = team.WithRunMeta(runCtx, runMeta)
	runCtx = agent.WithTurnID(runCtx, turnID)
	runCtx = agent.WithTurnToolSurfaceSnapshot(runCtx, a.turnToolSurfaceSnapshot(turnID))
	if injection != nil {
		runCtx = agent.WithTurnSystemMessages(runCtx, injection.systemMessages)
		runCtx = agent.WithTurnPinnedTools(runCtx, injection.pinnedTools)
	}
	if !a.installSessionRunCancel(run, cancel) {
		cancel()
		a.releaseSessionRun(run)
		run.complete(SubmitResult{Err: context.Canceled})
		return
	}
	abortStartup := func() {
		cancel()
		a.clearSessionRunCancel(run)
		run.complete(SubmitResult{Err: context.Canceled})
		if a.sessionRunOwned(run) {
			_ = a.updateStateConvergent(context.Background(), func(state *RuntimeState) error {
				if state.CurrentTurnID != turnID {
					return nil
				}
				state.Status = SessionStopped
				state.CurrentTurnID = ""
				state.CurrentRunMeta = nil
				resetFrozenTurnTools(state)
				state.PendingTool = nil
				state.PendingApproval = nil
				state.PendingQuestion = nil
				state.UpdatedAt = time.Now().UTC()
				return nil
			})
		}
		a.releaseSessionRun(run)
	}
	if a.prepareRun != nil {
		if err := a.prepareRun(runCtx, session, resume); err != nil {
			cancel()
			a.clearSessionRunCancel(run)
			_ = a.updateStateConvergent(context.Background(), func(state *RuntimeState) error {
				if state.CurrentTurnID != turnID {
					return nil
				}
				state.Status = SessionIdle
				state.CurrentTurnID = ""
				state.CurrentRunMeta = nil
				resetFrozenTurnTools(state)
				state.UpdatedAt = time.Now().UTC()
				return nil
			})
			a.releaseSessionRun(run)
			run.complete(SubmitResult{Err: err})
			return
		}
	}
	// Re-apply plan mode after prepareRun so durable plan_mode context and
	// session permission mode stay enforced for this turn.
	a.applyDurablePlanModeToRun(runCtx, session)
	if runCtx.Err() != nil || !a.sessionRunOwnsRunningState(run) {
		abortStartup()
		return
	}

	if hookMgr := a.agent.GetHookManager(); hookMgr != nil {
		hookPayload := map[string]interface{}{
			"session_id": a.id,
			"turn_id":    turnID,
			"resume":     resume,
		}
		if !resume {
			hookPayload["prompt"] = prompt
			hookMgr.DispatchAsync(runCtx, runtimehooks.EventUserPromptSubmit, hookPayload)
		}
		hookMgr.DispatchAsync(runCtx, runtimehooks.EventSessionStart, hookPayload)
	}
	startPayload := map[string]interface{}{
		"turn_id": turnID,
		"resume":  resume,
	}
	if !resume {
		startPayload["prompt_length"] = len(prompt)
	}
	a.publish(runtimeevents.Event{
		Type:      EventSessionStart,
		SessionID: a.id,
		TraceID:   turnID,
		Payload:   startPayload,
	})

	a.maybeAutoCompactSession(runCtx, session, turnID, runMeta, resume)
	a.runLifecycleMu.Lock()
	if runCtx.Err() != nil || !a.sessionRunOwnsRunningState(run) {
		a.runLifecycleMu.Unlock()
		abortStartup()
		return
	}

	runStartedAt := time.Now()
	a.trackSessionRunWait(run)
	stopStallWatchdog := a.startRunStallWatchdog(runCtx, run)
	go func() {
		defer a.releaseSessionRunWait(run)
		defer stopStallWatchdog()
		var (
			result  *agent.Result
			execErr error
		)
		if resume {
			result, execErr = a.continueLoop(runCtx, session, routeOverride)
		} else {
			result, execErr = a.runLoop(runCtx, prompt, session, routeOverride)
			if preflightErr, ok := agent.AsPromptPreflightError(execErr); ok && preflightErr != nil {
				if compactResult, _, compactErr := a.runManualCompact(runCtx, session, compactruntime.ModeLocal); compactErr == nil && compactResult != nil {
					result, execErr = a.runLoop(runCtx, prompt, session, routeOverride)
				}
			}
		}
		if result != nil {
			// The actor owns the durable turn identity. Stamp it here as the
			// authoritative fallback even if a custom loop did not propagate
			// agent.WithTurnID through its Result.
			result.TurnID = turnID
		}
		// Once the loop returns, a non-interrupted run may expose idle/stopped
		// state before terminal event/reply dispatch. A successor may replace its
		// actor slot, but must not turn that already-completed result into a
		// synthetic cancellation.
		run.finalizing.Store(true)
		// The execution loop has returned. Stop supervision before durable tail
		// cleanup so a slow store write cannot be misclassified as an LLM stall.
		stopStallWatchdog()
		cancel()
		a.clearSessionRunCancel(run)
		approvalDetached := approvalDetach.detached.Load()
		interrupted := run.interrupted.Load()
		cancelSource := sessionRunCancelSource(ctx, execErr, interrupted, result)
		// P0-4: a deadline/cancel-class run must not leave a resumable approval
		// behind. Once the execution deadline fired, a late decision must never
		// restart the run, so the approval is terminated with the run instead of
		// being detached across it. A clean completion or an explicit user
		// interrupt keeps the existing cross-run approval semantics. 灰度关闭
		// （§8）时本段不生效：审批照旧 detach，保持引入守卫前的跨 run 语义。
		terminalApproval := approvalDetached && a.terminalGuard && sessionRunTerminalCancelSource(cancelSource)
		var terminalApprovalEvent map[string]interface{}
		status := SessionIdle
		if ctx.Err() != nil || interrupted || errors.Is(execErr, context.Canceled) || errors.Is(execErr, context.DeadlineExceeded) {
			status = SessionStopped
		}
		// A watchdog-abandoned or superseded run may still return after a newer
		// turn starts. Only the current ownership token may mutate or persist
		// session state.
		publishTerminal := false
		if a.sessionRunOwned(run) {
			finalizeCtx := withSessionRunControl(context.Background(), run)
			if !approvalDetached && shouldRollbackFailedPrompt(execErr, result, resume, appendedPrompt) {
				if rollbackErr := a.rollbackLastUserPrompt(finalizeCtx, session, prompt); rollbackErr != nil &&
					!errors.Is(rollbackErr, errSessionRunSuperseded) && execErr == nil {
					execErr = rollbackErr
				}
			}
			if !approvalDetached && a.sessionRunOwned(run) && len(run.stripMetadataKeys) > 0 {
				stripMessagesWithMetadataKeys(session, run.stripMetadataKeys)
			}
			if !approvalDetached && a.sessionRunOwned(run) {
				// Persist history before exposing an idle/stopped runtime state.
				// Once the state is non-busy, a successor is allowed to load and
				// append to this durable snapshot.
				if persistErr := a.persistSession(finalizeCtx, session); persistErr != nil &&
					!errors.Is(persistErr, errSessionRunSuperseded) && execErr == nil {
					execErr = persistErr
				}
				// 工具回执补齐（方案 §0.3 / G3）：内联工具执行不经过审批恢复
				// 路径，历史落盘后统一为本次回合已完成、尚无回执的工具落回执。
				// 逐条失败只跳过该条，不影响主流程与终态发布。
				a.reconcileToolReceipts(finalizeCtx, session, turnID)
			}
			// Latch terminal ownership while the runtime state is still busy.
			// Once idle/stopped is exposed a successor may claim the actor before
			// this goroutine reaches event dispatch, but the completed turn must
			// still publish its assistant_message/session_end pair.
			publishTerminal = a.sessionRunOwned(run)
			terminalApproval = terminalApproval && publishTerminal
			if publishTerminal && (!approvalDetached || terminalApproval) {
				approvalSnapshot := a.stateWithoutToolSurfaces()
				applied := false
				_ = a.updateStateConvergent(context.Background(), func(state *RuntimeState) error {
					if state.CurrentTurnID != turnID {
						return nil
					}
					applied = true
					state.Status = status
					state.CurrentTurnID = ""
					state.CurrentRunMeta = nil
					resetFrozenTurnTools(state)
					state.PendingTool = nil
					if terminalApproval {
						// The approval died with the run: no later decision may
						// revive it (P0-1/P0-4).
						state.PendingApproval = nil
					}
					switch {
					case !a.terminalGuard:
						// 灰度关闭（§8）：不写终态标记，并清掉可能残留的旧值。
						state.LastRunTerminalReason = ""
					case sessionRunTerminalCancelSource(cancelSource):
						// Durable terminal marker for the host preflight and the
						// decision-point guard (P0-1/P0-3).
						state.LastRunTerminalReason = strings.TrimSpace(cancelSource)
					case !approvalDetached:
						state.LastRunTerminalReason = ""
					}
					state.UpdatedAt = time.Now().UTC()
					return nil
				})
				if applied && terminalApproval {
					terminalApprovalEvent = terminalApprovalResolvedEventPayload(approvalSnapshot, cancelSource)
					if approvalSnapshot != nil && approvalSnapshot.PendingApproval != nil {
						// The approval was retired here, before the decision
						// arrived. Record how it ended so hosts still report
						// the late decision as resolved-but-not-resumed
						// instead of falling back to an empty outcome (P0-3).
						a.recordApprovalOutcome(approvalSnapshot.PendingApproval.ID, ApprovalResolutionRunTerminated, false)
					}
				}
			}
		}

		duration := durationMillis(result)
		if duration <= 0 {
			duration = time.Since(runStartedAt).Milliseconds()
		}
		payload := map[string]interface{}{
			"turn_id":  turnID,
			"resume":   resume,
			"success":  execErr == nil && result != nil && result.Success,
			"steps":    resultSteps(result),
			"error":    firstNonEmptyError(execErr, result),
			"duration": duration,
			"status":   status,
		}
		if cancelSource != "" {
			payload["cancel_source"] = cancelSource
		}
		appendStructuredRunErrorPayload(payload, execErr)
		if result != nil {
			payload["trace_id"] = result.TraceID
			appendSessionActorUsagePayload(payload, result.Usage)
			appendSessionActorToolErrorPayload(payload, result)
			if result.LimitReached {
				payload["limit_reached"] = true
				if result.LimitReason != "" {
					payload["limit_reason"] = result.LimitReason
				}
				if result.StepLimit > 0 {
					payload["step_limit"] = result.StepLimit
				}
				if result.ToolCallLimit > 0 {
					payload["tool_call_limit"] = result.ToolCallLimit
				}
			}
		}
		// No run-owned mutation occurs after this point. Release ownership before
		// dispatching terminal hooks/events so a successor cannot invalidate one
		// half of the assistant_message/session_end terminal pair.
		a.releaseSessionRun(run)
		if publishTerminal {
			if hookMgr := a.agent.GetHookManager(); hookMgr != nil {
				hookPayload := map[string]interface{}{
					"session_id": a.id,
					"turn_id":    turnID,
					"resume":     resume,
					"success":    execErr == nil && result != nil && result.Success,
					"error":      firstNonEmptyError(execErr, result),
				}
				appendStructuredRunErrorPayload(hookPayload, execErr)
				if result != nil {
					hookPayload["trace_id"] = result.TraceID
					appendSessionActorUsagePayload(hookPayload, result.Usage)
					appendSessionActorToolErrorPayload(hookPayload, result)
				}
				hookMgr.DispatchAsync(ctx, runtimehooks.EventSessionEnd, hookPayload)
			}
		}
		// assistant.message is the authoritative terminal event for the whole
		// model response, not merely a non-empty text block. Publish it even
		// when content is empty so a reasoning-only/whitespace response can
		// reconcile its final reasoning snapshot and retire the stream identity.
		if publishTerminal && result != nil {
			messagePayload := map[string]interface{}{
				"turn_id": turnID,
				"content": result.Output,
				"mode":    "snapshot",
			}
			if streamID := strings.TrimSpace(result.AssistantStreamID); streamID != "" {
				messagePayload["stream_id"] = streamID
				messagePayload["sequence"] = result.AssistantStreamSequence + 1
			}
			if result.LimitReached {
				messagePayload["limit_reached"] = true
				if result.StepLimit > 0 {
					messagePayload["step_limit"] = result.StepLimit
				}
			}
			if result.Reasoning != nil {
				if encoded := result.Reasoning.ToMap(); len(encoded) > 0 {
					messagePayload["reasoning"] = encoded
				}
			}
			a.publish(runtimeevents.Event{
				Type:      EventAssistantMessage,
				SessionID: a.id,
				TraceID:   resultTraceID(result, turnID),
				Payload:   messagePayload,
			})
		}
		// Publish assistant_message before session_end so interactive clients can
		// complete any buffered streaming output before session teardown.
		if publishTerminal {
			a.publish(runtimeevents.Event{
				Type:      EventSessionEnd,
				SessionID: a.id,
				TraceID:   resultTraceID(result, turnID),
				Payload:   payload,
			})
		}
		// P0-4: a deadline/cancel-class run retires its detached approval here, so
		// the supervision projector closes the blocked row instead of treating the
		// later decision as a resumable edge.
		if publishTerminal && terminalApprovalEvent != nil {
			a.publish(runtimeevents.Event{
				Type:      EventApprovalResolved,
				SessionID: a.id,
				Payload:   terminalApprovalEvent,
			})
		}
		if !run.abandoned.Load() {
			run.complete(SubmitResult{Result: result, Err: execErr})
		}
		// P0-3b: the run released the session; consume trigger_turn mailbox
		// instructions that arrived while it was busy. Detached from this run so
		// a slow store read cannot delay the terminal reply; the drain itself is
		// idempotent and reverts to the mailbox on failure.
		if a.triggerTurnDrain && publishTerminal {
			go a.drainTriggerTurnMailbox()
		}
	}()
	a.runLifecycleMu.Unlock()
}

// sessionNotFoundError builds a typed SESSION_NOT_FOUND error for a missing
// session record. The broker's error classifier falls back to message string
// matching ("not found" → TOOL_PATH_NOT_FOUND) for untyped errors, which used
// to mislabel a missing/expired session as a path failure; returning the code
// directly keeps classification tied to the error type, not its wording.
func sessionNotFoundError(sessionID string) *runtimeerrors.RuntimeError {
	sessionID = strings.TrimSpace(sessionID)
	return runtimeerrors.Newf(runtimeerrors.ErrSessionNotFound, "session not found: %s", sessionID).
		WithContext("session_id", sessionID)
}

// sessionRecoveryRetryInterval bounds how often the actor asks the host to
// rebuild a missing session row after a failed attempt (negative cache).
const sessionRecoveryRetryInterval = 5 * time.Second

// tryEnsureSession asks the host to rebuild the missing session row and
// reports whether it succeeded. Failed attempts are negatively cached for
// sessionRecoveryRetryInterval so a persistently unavailable row cannot cause
// a retry storm across tools/turns in the same run.
func (a *SessionActor) tryEnsureSession(ctx context.Context) bool {
	if a == nil || a.ensureSession == nil {
		return false
	}
	a.sessionHealMu.Lock()
	defer a.sessionHealMu.Unlock()
	if now := time.Now(); !a.sessionHealBlockedUntil.IsZero() && now.Before(a.sessionHealBlockedUntil) {
		return false
	}
	if err := a.ensureSession(ctx, a.id); err != nil {
		a.sessionHealBlockedUntil = time.Now().Add(sessionRecoveryRetryInterval)
		return false
	}
	a.sessionHealBlockedUntil = time.Time{}
	return true
}

// publishSessionRecovered emits the audit event for a self-healed session row.
func (a *SessionActor) publishSessionRecovered(trigger string) {
	if a == nil || a.eventBus == nil {
		return
	}
	a.eventBus.Publish(runtimeevents.Event{
		Type:      "session_recovered_from_missing_row",
		SessionID: a.id,
		Payload:   map[string]interface{}{"trigger": trigger},
	})
}

// wrapSessionStoreError promotes the storage-level ErrSessionNotFound sentinel
// to the typed runtime error while leaving every other storage failure intact.
func wrapSessionStoreError(err error, sessionID string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrSessionNotFound) {
		return sessionNotFoundError(sessionID)
	}
	return err
}

func (a *SessionActor) loadSession(ctx context.Context) (*Session, error) {
	if a.sessionStore == nil {
		return nil, fmt.Errorf("session store is not configured")
	}
	session, err := a.sessionStore.Load(ctx, a.id)
	if err != nil && errors.Is(err, ErrSessionNotFound) {
		// The durable row is missing (deleted/expired/never persisted). Give
		// the host one chance to rebuild it from its in-memory snapshot before
		// surfacing the typed error to the tool layer.
		if a.tryEnsureSession(ctx) {
			session, err = a.sessionStore.Load(ctx, a.id)
			if err == nil && session != nil {
				a.publishSessionRecovered("load")
			}
		}
	}
	if err != nil {
		return nil, wrapSessionStoreError(err, a.id)
	}
	if session == nil {
		return nil, sessionNotFoundError(a.id)
	}
	// Lazily mint stable message_id/turn_id for pre-Phase-6 sessions so
	// backtrack message_id selectors and history consumers stay durable.
	if session.EnsureMessageIdentities() {
		if persistErr := a.persistSession(ctx, session); persistErr != nil && a.eventBus != nil {
			// Non-fatal: ids still exist in-memory for this request.
			a.eventBus.Publish(runtimeevents.Event{
				Type:      "session.message_identity_persist_error",
				SessionID: a.id,
				Payload:   map[string]interface{}{"error": persistErr.Error()},
			})
		}
	}
	return session, nil
}

func (a *SessionActor) persistSession(ctx context.Context, session *Session) error {
	if session == nil || a.sessionStore == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	a.sessionPersistMu.Lock()
	defer a.sessionPersistMu.Unlock()

	run, hasRun := sessionRunControlFromContext(ctx)
	if hasRun && !a.sessionRunOwned(run) {
		return errSessionRunSuperseded
	}
	keys := a.activeSessionRunMetadataKeys()
	if hasRun {
		keys = run.stripMetadataKeys
	}
	// Transient prompts are request-scoped by contract, so strip them
	// unconditionally instead of relying on the caller passing
	// StripMetadataKeys. A continuation prompt that reaches the store becomes a
	// user turn the workspace shows next to the real one.
	keys = normalizedMetadataKeys(append(append([]string{}, keys...), runtimetypes.MetadataKeyTransientPrompt))
	if len(keys) > 0 {
		stripMessagesWithMetadataKeys(session, keys)
	}
	// Rebuilt transcripts carry request-only context layers (fact ledger,
	// recall, correction, ...) with fresh identities. Collapse the surplus
	// copies before they are appended as new canonical rows.
	session.PruneRequestScopedHistory()
	if a.persistHook != nil {
		prepared, err := a.persistHook(ctx, session)
		if err != nil {
			return err
		}
		if prepared != nil {
			session = prepared
		}
	}
	// Broker tools persist opaque job/session aliases directly into the parent
	// session while this actor still owns an older in-memory session snapshot.
	// Reload that broker-owned context immediately before the actor's full-row
	// update so the end-of-turn write cannot erase handles created in the turn.
	latest, err := a.sessionStore.Load(ctx, session.ID)
	if err != nil && errors.Is(err, ErrSessionNotFound) {
		if a.tryEnsureSession(ctx) {
			latest, err = a.sessionStore.Load(ctx, session.ID)
		}
	}
	if err != nil {
		if errors.Is(err, ErrSessionNotFound) && a.ensureSession != nil {
			// The row vanished mid-turn: recreate it from the actor's full
			// in-memory snapshot instead of failing the turn's persist.
			if saveErr := a.sessionStore.Save(ctx, session); saveErr != nil {
				return wrapSessionStoreError(saveErr, session.ID)
			}
			a.publishSessionRecovered("persist_reload")
			return nil
		}
		return wrapSessionStoreError(err, session.ID)
	}
	if latest != nil {
		if aliases, ok := latest.GetContext(toolbroker.SessionHandleAliasesContextKey); ok {
			copiedAliases, err := cloneSessionContextValue(aliases)
			if err != nil {
				return fmt.Errorf("clone broker handle aliases: %w", err)
			}
			session.SetContext(toolbroker.SessionHandleAliasesContextKey, copiedAliases)
		}
		// Plan-mode mutations run on a store-loaded session (enter_plan_mode /
		// exit_plan_mode tool calls) while this actor still holds the run-scoped
		// snapshot. Prefer the store copy only when it carries a newer lifecycle
		// transition: the enter upgrades the stale snapshot, while a completed
		// exit is not resurrected back to active by the end-of-turn write
		// (observed live 2026-09-18: exit returned status=exited, the next row
		// write restored status=active).
		if storedPlan, ok := latest.GetContext(planmode.ContextKey); ok {
			if _, hasOutgoing := session.GetContext(planmode.ContextKey); !hasOutgoing ||
				planModeStateRevision(planmode.Load(latest)).After(planModeStateRevision(planmode.Load(session))) {
				copiedPlan, err := cloneSessionContextValue(storedPlan)
				if err != nil {
					return fmt.Errorf("clone plan mode state: %w", err)
				}
				session.SetContext(planmode.ContextKey, copiedPlan)
			}
		}
	}
	if hasRun && !a.sessionRunOwned(run) {
		return errSessionRunSuperseded
	}
	if err := a.sessionStore.Update(ctx, session); err != nil {
		if errors.Is(err, ErrSessionNotFound) && a.ensureSession != nil {
			// Lost a race with an external delete between reload and update:
			// fall back to create semantics with the current snapshot.
			if saveErr := a.sessionStore.Save(ctx, session); saveErr != nil {
				return wrapSessionStoreError(saveErr, session.ID)
			}
			a.publishSessionRecovered("persist_update")
			return nil
		}
		return wrapSessionStoreError(err, session.ID)
	}
	return nil
}

// planModeStateRevision reports the newest lifecycle timestamp recorded in a
// durable plan-mode state. Persist-time merging compares revisions to decide
// whether the stored copy is newer than the outgoing in-memory snapshot; the
// writer always stores UTC RFC3339Nano timestamps.
func planModeStateRevision(state planmode.State) time.Time {
	var newest time.Time
	for _, raw := range []string{state.ExitedAt, state.EnteredAt} {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		parsed, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			continue
		}
		if parsed.After(newest) {
			newest = parsed
		}
	}
	return newest
}

// checkpointSessionHistory 是 ReAct 循环 OnHistoryCheckpoint 的落点：把长 turn
// 中已提交的 durable 历史增量写回会话存储。
//
// 与 persistSession 的区别只有时机与失败语义：这里是 turn 中途的尽力而为写入，
// 按 checkpointInterval 节流（窗口内直接跳过；窗口策略与 runtime HTTP
// agent-chat 共用 reserveCheckpointWindow），失败只上报事件、绝不冒泡成 turn
// 失败；turn 结束的 post-turn sync 仍是最终一致性的保证。
func (a *SessionActor) checkpointSessionHistory(ctx context.Context, session *Session) {
	if a == nil || session == nil || a.sessionStore == nil {
		return
	}
	undo, ok := reserveCheckpointWindow(&a.lastCheckpointAt, a.checkpointInterval)
	if !ok {
		return
	}
	if err := a.persistSession(ctx, session); err != nil {
		// 失败回退时间戳：让下一个提交点立刻重试，而不是再等一个节流窗口。
		if undo != nil {
			undo()
		}
		a.publishSessionCheckpointFailure(ctx, err)
	}
}

// publishSessionCheckpointFailure 上报中途落库失败。中断/取消属于正常收尾路径
// （turn 结束会统一落库），不为它们制造噪声事件。
func (a *SessionActor) publishSessionCheckpointFailure(ctx context.Context, err error) {
	if a == nil || err == nil || a.eventBus == nil {
		return
	}
	if errors.Is(err, errSessionRunSuperseded) ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) {
		return
	}
	payload := map[string]interface{}{"error": err.Error(), "stage": "mid_turn_checkpoint"}
	if run, ok := sessionRunControlFromContext(ctx); ok && run != nil {
		payload["turn_id"] = run.turnID
	}
	a.eventBus.Publish(runtimeevents.Event{
		Type:      "session.checkpoint_persist_error",
		SessionID: a.id,
		Payload:   payload,
	})
}

// historyCheckpointLoopConfig 返回本轮 ReAct 配置，并在长 turn 中途落库启用时
// 挂载 checkpoint 回调。
func (a *SessionActor) historyCheckpointLoopConfig(routeOverride *RunRouteOverride, runMeta *team.RunMeta, session *Session) *agent.LoopReActConfig {
	if a == nil {
		return agent.DefaultLoopReActConfig()
	}
	cfg := cloneLoopConfigForRun(a.loopConfig, routeOverride, runMeta)
	if cfg == nil {
		cfg = agent.DefaultLoopReActConfig()
	}
	if session == nil || a.checkpointInterval <= 0 {
		return cfg
	}
	cfg.OnHistoryCheckpoint = func(ctx context.Context, _ []runtimetypes.Message) {
		a.checkpointSessionHistory(ctx, session)
	}
	return cfg
}

func cloneSessionContextValue(value interface{}) (interface{}, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var copied interface{}
	if err := json.Unmarshal(payload, &copied); err != nil {
		return nil, err
	}
	return copied, nil
}

func (a *SessionActor) loadState(ctx context.Context) error {
	if a.stateStore == nil {
		if a.state == nil {
			a.state = &RuntimeState{
				SessionID: a.id,
				Status:    SessionIdle,
				UpdatedAt: time.Now().UTC(),
			}
		}
		return nil
	}
	state, err := a.stateStore.LoadState(ctx, a.id)
	if err != nil {
		return err
	}
	if state == nil {
		state = &RuntimeState{
			SessionID: a.id,
			Status:    SessionIdle,
			UpdatedAt: time.Now().UTC(),
		}
		_ = a.stateStore.SaveState(ctx, state)
	}
	if a.recoverStale && reconcileRecoveredRuntimeState(state) {
		if err := a.stateStore.SaveState(ctx, state); err != nil {
			return err
		}
	}
	a.state = state
	return nil
}

// LoadRuntimeStateForInspection loads a persisted state for a passive status
// query. When no live session lease remains, transient actor states are
// reconciled so a crashed process cannot keep reporting "running" forever.
// A live lease is authoritative and is never changed by inspection.
func LoadRuntimeStateForInspection(ctx context.Context, stateStore RuntimeStateStore, sessionID string) (*RuntimeState, error) {
	if stateStore == nil {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	state, err := stateStore.LoadState(ctx, strings.TrimSpace(sessionID))
	if err != nil || state == nil {
		return state, err
	}
	leaseStore, ok := stateStore.(SessionLeaseStore)
	if !ok || leaseStore == nil {
		return state, nil
	}
	lease, err := leaseStore.GetLease(ctx, strings.TrimSpace(sessionID))
	if err != nil {
		return nil, err
	}
	// A missing lease is not enough evidence of a crashed actor: lightweight
	// embedders and older runtimes may persist state without registering leases.
	// Only an observed, expired lease proves that a previously owned run is stale.
	if lease == nil || !leaseExpired(lease, time.Now().UTC()) {
		return state, nil
	}
	if !reconcileRecoveredRuntimeState(state) {
		return state, nil
	}
	if err := stateStore.SaveState(ctx, state); err != nil {
		return nil, err
	}
	return state, nil
}

// A newly constructed actor cannot still own an in-process run from a previous
// actor instance. Preserve durable approval/question waits, but release
// transient states that would otherwise leave a restored session permanently
// busy after the previous process exited unexpectedly.
func reconcileRecoveredRuntimeState(state *RuntimeState) bool {
	if state == nil {
		return false
	}

	switch state.Status {
	case SessionRunning:
		switch {
		case state.PendingApproval != nil:
			state.Status = SessionWaitingApproval
			state.UpdatedAt = time.Now().UTC()
			return true
		case state.PendingQuestion != nil:
			state.Status = SessionWaitingInput
			state.UpdatedAt = time.Now().UTC()
			return true
		default:
			stopRecoveredRuntimeState(state)
			return true
		}
	case SessionWaitingApproval:
		if state.PendingApproval == nil {
			stopRecoveredRuntimeState(state)
			return true
		}
	case SessionWaitingInput:
		if state.PendingQuestion == nil {
			stopRecoveredRuntimeState(state)
			return true
		}
	case SessionRewinding:
		stopRecoveredRuntimeState(state)
		return true
	}
	return false
}

func stopRecoveredRuntimeState(state *RuntimeState) {
	state.Status = SessionStopped
	state.CurrentTurnID = ""
	state.CurrentRunMeta = nil
	resetFrozenTurnTools(state)
	state.PendingTool = nil
	state.PendingApproval = nil
	state.PendingQuestion = nil
	state.UpdatedAt = time.Now().UTC()
}

// Cancel sources that describe a run which ended on its own execution deadline or
// through an external cancel. A pending approval left behind by such a run is not
// resumable: the deadline already fired, so deciding it later must never restart
// the run (P0-4). "user_interrupt" is deliberately excluded — that path keeps its
// existing cross-run approval semantics.
const (
	runCancelSourceUserInterrupt    = "user_interrupt"
	runCancelSourceRunTimeout       = "run_timeout"
	runCancelSourceDeadline         = "deadline"
	runCancelSourceExecutionContext = "execution_context"
	runCancelSourceParentDeadline   = "parent_deadline"
	runCancelSourceParentContext    = "parent_context"
)

// sessionRunTerminalCancelSource reports whether a cancel source describes a run
// that must not be revived by a late approval decision.
func sessionRunTerminalCancelSource(source string) bool {
	switch strings.TrimSpace(source) {
	case runCancelSourceRunTimeout, runCancelSourceDeadline, runCancelSourceExecutionContext,
		runCancelSourceParentDeadline, runCancelSourceParentContext:
		return true
	default:
		return false
	}
}

func sessionRunCancelSource(ctx context.Context, execErr error, interrupted bool, result *agent.Result) string {
	if interrupted {
		return runCancelSourceUserInterrupt
	}
	if result != nil && result.LimitReason == "run_timeout" {
		return runCancelSourceRunTimeout
	}
	if errors.Is(execErr, context.DeadlineExceeded) {
		return runCancelSourceDeadline
	}
	if errors.Is(execErr, context.Canceled) {
		return runCancelSourceExecutionContext
	}
	if ctx != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return runCancelSourceParentDeadline
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			return runCancelSourceParentContext
		}
	}
	return ""
}

func (a *SessionActor) updateState(ctx context.Context, mutate func(*RuntimeState) error) error {
	if a == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	a.statePersistMu.Lock()
	defer a.statePersistMu.Unlock()

	a.mu.RLock()
	var next *RuntimeState
	if a.state != nil {
		next = a.state.Clone()
	}
	a.mu.RUnlock()
	if next == nil {
		next = &RuntimeState{SessionID: a.id, Status: SessionIdle}
	}
	if mutate != nil {
		if err := mutate(next); err != nil {
			return err
		}
	}
	if next.UpdatedAt.IsZero() {
		next.UpdatedAt = time.Now().UTC()
	}

	if a.stateStore != nil {
		snapshot := next.Clone()
		var saveErr error
		for attempt := 1; attempt <= 3; attempt++ {
			saveErr = a.stateStore.SaveState(ctx, snapshot)
			if saveErr != nil && attempt < 3 {
				time.Sleep(time.Duration(attempt) * 25 * time.Millisecond)
			}
			if saveErr == nil {
				break
			}
		}
		if saveErr != nil {
			return saveErr
		}
	}

	// Install only after durable persistence succeeds. This keeps a failed
	// SaveState from exposing a phantom running turn or clearing recoverable
	// approval/tool state in memory.
	a.mu.Lock()
	a.state = next
	run := a.activeRun
	a.mu.Unlock()
	if run != nil {
		a.touchRunActivity(run, runtimeevents.Event{
			SessionID: a.id,
			Payload:   map[string]interface{}{"turn_id": run.turnID},
		})
	}
	return nil
}

// updateStateConvergent is for terminal/best-effort transitions whose local
// state must advance even when durable state persistence is unavailable. Its
// mutator must be idempotent and must not have side effects: on SaveState
// failure it is applied a second time to the current in-memory state.
func (a *SessionActor) updateStateConvergent(ctx context.Context, mutate func(*RuntimeState) error) error {
	err := a.updateState(ctx, mutate)
	if err == nil || a == nil || mutate == nil {
		return err
	}

	a.statePersistMu.Lock()
	defer a.statePersistMu.Unlock()
	a.mu.Lock()
	if a.state == nil {
		a.state = &RuntimeState{SessionID: a.id, Status: SessionIdle}
	}
	fallbackErr := mutate(a.state)
	if a.state.UpdatedAt.IsZero() {
		a.state.UpdatedAt = time.Now().UTC()
	}
	a.mu.Unlock()
	if fallbackErr != nil {
		return errors.Join(err, fallbackErr)
	}
	return err
}

// UpdateStateForTest mutates runtime state for unit/integration tests.
// Prefer this over reflecting into private fields.
func (a *SessionActor) UpdateStateForTest(ctx context.Context, mutate func(*RuntimeState) error) error {
	return a.updateState(ctx, mutate)
}

func (a *SessionActor) recordPendingToolCall(ctx context.Context, pending *PendingToolInvocation) error {
	if a == nil || pending == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	session, err := a.loadSession(ctx)
	if err != nil {
		return err
	}
	batchCtx, _ := agent.ToolBatchContextFromContext(ctx)
	ensurePendingToolBatchInSession(session, pending, batchCtx.CompletedToolMessages)
	return a.persistSession(ctx, session)
}

func (a *SessionActor) resumePendingToolWithResult(ctx context.Context, state *RuntimeState, content interface{}, toolErr string, metadata ...map[string]interface{}) error {
	if a == nil {
		return fmt.Errorf("session actor is nil")
	}
	if state == nil || state.PendingTool == nil {
		return fmt.Errorf("pending tool not found")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	session, err := a.loadSession(ctx)
	if err != nil {
		return err
	}
	if !sessionHasToolCall(session, state.PendingTool.ToolCallID) {
		if err := a.recordPendingToolCall(ctx, state.PendingTool); err != nil {
			return err
		}
		session, err = a.loadSession(ctx)
		if err != nil {
			return err
		}
	}
	if sessionHasToolResult(session, state.PendingTool.ToolCallID) {
		return a.resumeFromPersistedToolResult(ctx, state, session)
	}
	toolMessage, err := a.buildPendingToolResultMessage(ctx, state.PendingTool, content, toolErr, metadata...)
	if err != nil {
		return err
	}
	session.AddMessage(*toolMessage)
	if err := a.persistSession(ctx, session); err != nil {
		return err
	}
	return a.resumePendingBatchAfterCurrentResult(ctx, state, state.PendingTool, session, false)
}

func (a *SessionActor) resumeFromPersistedToolResult(ctx context.Context, state *RuntimeState, session *Session) error {
	if a == nil {
		return fmt.Errorf("session actor is nil")
	}
	if state == nil || state.PendingTool == nil {
		return fmt.Errorf("pending tool not found")
	}
	if session == nil {
		return fmt.Errorf("session is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return a.resumePendingBatchAfterCurrentResult(ctx, state, state.PendingTool, session, true)
}

func (a *SessionActor) appendPendingToolReceiptToSession(ctx context.Context, pending *PendingToolInvocation, session *Session) (*Session, error) {
	if a == nil {
		return nil, fmt.Errorf("session actor is nil")
	}
	if pending == nil {
		return nil, fmt.Errorf("pending tool not found")
	}
	if session == nil {
		return nil, fmt.Errorf("session is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if sessionHasToolResult(session, pending.ToolCallID) {
		return session, nil
	}
	message, ok := decodePendingToolResultMessage(pending.ResultMessageJSON)
	if !ok || message == nil {
		return nil, fmt.Errorf("pending tool result receipt not found")
	}
	session.AddMessage(*message)
	if err := a.persistSession(ctx, session); err != nil {
		return nil, err
	}
	return session, nil
}

func (a *SessionActor) resumeApprovedPendingTool(ctx context.Context, state *RuntimeState, patchedArgs json.RawMessage) error {
	if a == nil {
		return fmt.Errorf("session actor is nil")
	}
	if state == nil || state.PendingTool == nil {
		return fmt.Errorf("pending tool not found")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	session, err := a.loadSession(ctx)
	if err != nil {
		return err
	}
	pending := *state.PendingTool
	if len(patchedArgs) > 0 {
		applyPendingToolPatchedArgs(&pending, patchedArgs)
	}
	if !sessionHasToolCall(session, pending.ToolCallID) {
		if err := a.recordPendingToolCall(ctx, &pending); err != nil {
			return err
		}
		session, err = a.loadSession(ctx)
		if err != nil {
			return err
		}
	}
	if sessionHasToolResult(session, pending.ToolCallID) {
		return a.resumeFromPersistedToolResult(ctx, state, session)
	}
	if receipt, message, ok, err := a.loadStoredToolReceipt(ctx, a.id, pending.ToolCallID); err != nil {
		return err
	} else if ok && message != nil {
		a.markReplayedToolReceipt(pending.ToolCallID)
		session.AddMessage(*message)
		if err := a.persistSession(ctx, session); err != nil {
			return err
		}
		a.publishToolReceiptEvent(EventToolReceiptReplayed, strings.TrimSpace(state.CurrentTurnID), "receipt_store", *receipt)
		return a.resumeFromPersistedToolResult(ctx, state, session)
	}
	if strings.TrimSpace(pending.ExecutionState) == PendingToolExecutionCompleted {
		session, err = a.appendPendingToolReceiptToSession(ctx, &pending, session)
		if err != nil {
			return err
		}
		a.markReplayedToolReceipt(pending.ToolCallID)
		a.publishToolReceiptEvent(EventToolReceiptReplayed, strings.TrimSpace(state.CurrentTurnID), "runtime_state", toolExecutionReceiptFromPending(a.id, &pending))
		return a.resumeFromPersistedToolResult(ctx, state, session)
	}
	if strings.TrimSpace(pending.ExecutionState) == PendingToolExecutionStarted {
		toolMessage, err := a.buildPendingToolResultMessage(ctx, &pending, nil, "approved tool execution may have completed before interruption; verify side effects before retrying")
		if err != nil {
			return err
		}
		session.AddMessage(*toolMessage)
		if err := a.persistSession(ctx, session); err != nil {
			return err
		}
		return a.resumeFromPersistedToolResult(ctx, state, session)
	}
	if err := a.updateState(ctx, func(runtimeState *RuntimeState) error {
		if runtimeState.PendingTool == nil || runtimeState.PendingTool.ToolCallID != state.PendingTool.ToolCallID {
			return fmt.Errorf("pending tool changed while resuming")
		}
		if len(patchedArgs) > 0 {
			applyPendingToolPatchedArgs(runtimeState.PendingTool, patchedArgs)
		}
		runtimeState.PendingTool.ExecutionState = PendingToolExecutionStarted
		runtimeState.PendingTool.ExecutionStartedAt = time.Now().UTC()
		runtimeState.UpdatedAt = time.Now().UTC()
		return nil
	}); err != nil {
		return err
	}
	runMeta := state.CurrentRunMeta.Clone()
	runCtx := team.WithRunMeta(ctx, runMeta)
	message, err := a.agent.ExecuteApprovedToolCall(runCtx, a.id, runtimetypes.ToolCall{
		ID:       pending.ToolCallID,
		Type:     pending.ToolType,
		Name:     pending.ToolName,
		Args:     decodePendingToolArgs(pending.ArgsJSON),
		RawInput: pending.RawInput,
	}, session.GetMessages())
	if err != nil {
		return err
	}
	receipt, err := encodePendingToolResultMessage(message)
	if err != nil {
		return err
	}
	storedReceipt := newToolExecutionReceipt(a.id, pending.ToolCallID, pending.ToolName, receipt, time.Now().UTC())
	if len(pending.ArgsJSON) > 0 {
		storedReceipt.ArgsJSON = append(json.RawMessage(nil), pending.ArgsJSON...)
	}
	if err := a.saveStoredToolReceipt(ctx, storedReceipt); err != nil {
		return err
	}
	a.publishToolReceiptEvent(EventToolReceiptRecorded, strings.TrimSpace(state.CurrentTurnID), "receipt_store", storedReceipt)
	if err := a.updateState(ctx, func(runtimeState *RuntimeState) error {
		if runtimeState.PendingTool == nil || runtimeState.PendingTool.ToolCallID != state.PendingTool.ToolCallID {
			return fmt.Errorf("pending tool changed while storing receipt")
		}
		runtimeState.PendingTool.ResultMessageJSON = receipt
		runtimeState.PendingTool.ExecutionState = PendingToolExecutionCompleted
		runtimeState.PendingTool.ExecutionCompletedAt = time.Now().UTC()
		runtimeState.UpdatedAt = time.Now().UTC()
		return nil
	}); err != nil {
		return err
	}
	session.AddMessage(*message)
	if err := a.persistSession(ctx, session); err != nil {
		return err
	}
	return a.resumePendingBatchAfterCurrentResult(ctx, state, &pending, session, true)
}

func (a *SessionActor) buildPendingToolResultMessage(ctx context.Context, pending *PendingToolInvocation, content interface{}, toolErr string, metadata ...map[string]interface{}) (*runtimetypes.Message, error) {
	if a == nil || pending == nil {
		return nil, fmt.Errorf("pending tool is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	gateway := a.agent.GetOutputGateway()
	var toolMetadata map[string]interface{}
	if len(metadata) > 0 && len(metadata[0]) > 0 {
		toolMetadata = make(map[string]interface{}, len(metadata[0]))
		for key, value := range metadata[0] {
			toolMetadata[key] = value
		}
	}
	envelope, gatewayErr := gateway.Process(ctx, runtimeoutput.RawToolResult{
		SessionID:  a.id,
		ToolName:   pending.ToolName,
		ToolCallID: pending.ToolCallID,
		Content:    content,
		Error:      toolErr,
		Metadata:   toolMetadata,
		Args:       decodePendingToolArgs(pending.ArgsJSON),
	})
	message := runtimetypes.NewToolMessage(pending.ToolCallID, "")
	message.Content = runtimeoutput.RenderToolResultContentForModel(content, toolErr, envelope)
	if envelope != nil {
		if len(envelope.Metadata) > 0 {
			message.Metadata = runtimetypes.NewMetadata()
			for key, value := range envelope.Metadata {
				message.Metadata[key] = value
			}
		}
	}
	if gatewayErr != nil && message.Metadata != nil {
		message.Metadata["gateway_error"] = gatewayErr.Error()
	}
	return message, nil
}

func (a *SessionActor) resumePendingBatchAfterCurrentResult(ctx context.Context, state *RuntimeState, pending *PendingToolInvocation, session *Session, deleteStoredReceipt bool) error {
	if a == nil {
		return fmt.Errorf("session actor is nil")
	}
	if state == nil || pending == nil {
		return fmt.Errorf("pending tool not found")
	}
	if session == nil {
		return fmt.Errorf("session is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	turnID := strings.TrimSpace(state.CurrentTurnID)
	// P0-2: never restart a run that already ended in a terminal cancellation
	// class. The durable reason field is the signal; a superseded/foreign run
	// token is refused by run ownership instead. 灰度关闭（§8）时该断言不生效，
	// 恢复入口回到引入守卫前的行为。
	if reason := strings.TrimSpace(state.LastRunTerminalReason); a.terminalGuard && reason != "" {
		return fmt.Errorf("%w: %s", errSessionRunTerminal, reason)
	}
	if turnID == "" {
		turnID = "turn_" + uuid.NewString()
	}
	run := a.claimSessionRun(turnID, nil, nil)
	runCtx := withSessionRunControl(ctx, run)
	ensurePendingToolBatchInSession(session, pending, nil)
	if err := a.persistSession(runCtx, session); err != nil {
		a.releaseSessionRun(run)
		return err
	}
	recoveryPending := clonePendingToolInvocation(pending, false)
	runMeta := state.CurrentRunMeta.Clone()
	if err := a.updateState(ctx, func(runtimeState *RuntimeState) error {
		if runtimeState.PendingTool == nil || runtimeState.PendingTool.ToolCallID != state.PendingTool.ToolCallID {
			return fmt.Errorf("pending tool changed while resuming")
		}
		runtimeState.PendingTool = nil
		runtimeState.PendingApproval = nil
		runtimeState.PendingQuestion = nil
		runtimeState.Status = SessionRunning
		runtimeState.CurrentTurnID = turnID
		runtimeState.UpdatedAt = time.Now().UTC()
		return nil
	}); err != nil {
		a.releaseSessionRun(run)
		return err
	}
	if deleteStoredReceipt {
		a.deleteStoredToolReceipt(context.Background(), a.id, pending.ToolCallID)
	}
	a.startPendingBatchRecoveryRun(runCtx, session, recoveryPending, turnID, runMeta, run)
	return nil
}

func (a *SessionActor) startPendingBatchRecoveryRun(ctx context.Context, session *Session, pending *PendingToolInvocation, turnID string, runMeta *team.RunMeta, run *sessionRunControl) {
	if a == nil || session == nil || pending == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	runCtx, cancel := context.WithCancel(ctx)
	runCtx = team.WithRunMeta(runCtx, runMeta)
	runCtx = agent.WithTurnToolSurfaceSnapshot(runCtx, a.turnToolSurfaceSnapshot(turnID))
	if !a.installSessionRunCancel(run, cancel) {
		cancel()
		a.releaseSessionRun(run)
		return
	}
	a.activeRunWG.Add(1)
	go func() {
		defer a.activeRunWG.Done()
		if err := a.executeRemainingPendingBatch(runCtx, session, pending); err != nil {
			cancel()
			a.clearSessionRunCancel(run)
			a.finishPendingBatchRecovery(session, turnID, err, run)
			return
		}
		cancel()
		a.clearSessionRunCancel(run)
		if !a.sessionRunOwnsRunningState(run) {
			a.finishPendingBatchRecovery(session, turnID, context.Canceled, run)
			return
		}
		a.startSessionRun(withSessionRunControl(context.Background(), run), session, "", true, turnID, runMeta, runRouteOverrideFromRunMeta(runMeta), nil, nil, false, run)
	}()
}

func (a *SessionActor) executeRemainingPendingBatch(ctx context.Context, session *Session, pending *PendingToolInvocation) error {
	if a == nil || a.agent == nil {
		return fmt.Errorf("agent is not configured")
	}
	if session == nil || pending == nil {
		return fmt.Errorf("pending batch is not available")
	}
	batch := pendingRuntimeToolCalls(session, pending)
	if len(batch) == 0 {
		return nil
	}
	currentIndex := 0
	for index, call := range batch {
		if strings.TrimSpace(call.ID) == strings.TrimSpace(pending.ToolCallID) {
			currentIndex = index
			break
		}
	}
	for index := currentIndex + 1; index < len(batch); index++ {
		call := batch[index]
		if strings.TrimSpace(call.ID) == "" || sessionHasToolResult(session, call.ID) {
			continue
		}
		message, err := a.agent.ExecuteToolCall(ctx, a.id, call, session.GetMessages(), batch)
		if err != nil {
			return err
		}
		session.AddMessage(*message)
		if err := a.persistSession(ctx, session); err != nil {
			return err
		}
	}
	return nil
}

func (a *SessionActor) finishPendingBatchRecovery(session *Session, turnID string, execErr error, run *sessionRunControl) {
	if a == nil {
		return
	}
	if !a.sessionRunOwned(run) {
		return
	}
	run.finalizing.Store(true)
	status := SessionIdle
	if run.interrupted.Load() || errors.Is(execErr, context.Canceled) || errors.Is(execErr, context.DeadlineExceeded) ||
		(execErr != nil && (strings.Contains(execErr.Error(), "context canceled") || strings.Contains(execErr.Error(), "deadline exceeded"))) {
		status = SessionStopped
	}
	finalizeCtx := withSessionRunControl(context.Background(), run)
	if session != nil {
		if persistErr := a.persistSession(finalizeCtx, session); persistErr != nil &&
			!errors.Is(persistErr, errSessionRunSuperseded) && execErr == nil {
			execErr = persistErr
		}
	}
	if a.sessionRunOwned(run) {
		_ = a.updateStateConvergent(context.Background(), func(state *RuntimeState) error {
			if state.CurrentTurnID != strings.TrimSpace(turnID) {
				return nil
			}
			state.Status = status
			state.CurrentTurnID = ""
			state.CurrentRunMeta = nil
			resetFrozenTurnTools(state)
			state.PendingTool = nil
			state.PendingApproval = nil
			state.PendingQuestion = nil
			state.UpdatedAt = time.Now().UTC()
			return nil
		})
		a.publish(runtimeevents.Event{
			Type:      EventSessionEnd,
			SessionID: a.id,
			TraceID:   strings.TrimSpace(turnID),
			Payload: map[string]interface{}{
				"turn_id":  strings.TrimSpace(turnID),
				"resume":   true,
				"success":  false,
				"steps":    0,
				"error":    errorString(execErr),
				"duration": int64(0),
				"status":   status,
			},
		})
	}
	a.releaseSessionRun(run)
}

func withSessionRunControl(ctx context.Context, run *sessionRunControl) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if run == nil {
		return ctx
	}
	return context.WithValue(ctx, sessionRunControlContextKey{}, run)
}

func sessionRunControlFromContext(ctx context.Context) (*sessionRunControl, bool) {
	if ctx == nil {
		return nil, false
	}
	run, ok := ctx.Value(sessionRunControlContextKey{}).(*sessionRunControl)
	return run, ok && run != nil
}

// foreignSessionRunControl reports whether ctx carries a run-ownership token
// minted by a different session actor. Tokens without a recorded owner (legacy
// or hand-built) are treated as local so the superseded-run guard stays intact.
func foreignSessionRunControl(ctx context.Context, sessionID string) (*sessionRunControl, bool) {
	run, ok := sessionRunControlFromContext(ctx)
	if !ok {
		return nil, false
	}
	owner := strings.TrimSpace(run.sessionID)
	if owner == "" || owner == strings.TrimSpace(sessionID) {
		return nil, false
	}
	return run, true
}

// detachForeignSessionRunControl drops a run token that belongs to another
// session actor. Control-plane callers (resolve_agent_approval, answer
// question, …) reach this actor through the broker with the caller's run
// context, and judgeOwnership would otherwise reject every write with
// errSessionRunSuperseded. This actor's own token — including a stale
// generation — is kept so a genuinely superseded local run is still refused.
func (a *SessionActor) detachForeignSessionRunControl(ctx context.Context) context.Context {
	if a == nil || ctx == nil {
		return ctx
	}
	if _, foreign := foreignSessionRunControl(ctx, a.id); !foreign {
		return ctx
	}
	return sessionRunControlDetachedContext{Context: ctx}
}

// sessionRunControlDetachedContext hides the run-ownership token carried by the
// wrapped context while leaving cancellation, deadlines and every other value
// untouched. context.WithoutValue is newer than the toolchain floor of this
// repository (Go 1.21 for the Win7 target), so the detach uses a shadowing
// wrapper instead of the upstream helper.
type sessionRunControlDetachedContext struct {
	context.Context
}

func (c sessionRunControlDetachedContext) Value(key any) any {
	if _, ok := key.(sessionRunControlContextKey); ok {
		return nil
	}
	return c.Context.Value(key)
}

func sessionRunTurnID(run *sessionRunControl) string {
	if run == nil {
		return ""
	}
	return strings.TrimSpace(run.turnID)
}

func (run *sessionRunControl) complete(result SubmitResult) {
	if run == nil || run.reply == nil {
		return
	}
	run.replyOnce.Do(func() {
		run.reply <- result
	})
}

func (a *SessionActor) retireInterruptedSessionRunAfter(run *sessionRunControl, delay time.Duration) {
	if a == nil || run == nil {
		return
	}
	retire := func() {
		if run.finalizing.Load() {
			return
		}
		if a.abandonSessionRun(run) {
			a.releaseDetachedSessionRunWait(run)
			run.complete(SubmitResult{Err: context.Canceled})
			return
		}
		// The execution loop may have entered finalization while abandonment
		// was acquiring lifecycle ownership. Preserve its structured result.
		if !run.finalizing.Load() {
			run.complete(SubmitResult{Err: context.Canceled})
		}
	}
	if delay <= 0 {
		retire()
		return
	}
	time.AfterFunc(delay, retire)
}

func (a *SessionActor) releaseSessionRunWait(run *sessionRunControl) {
	a.releaseSessionRunWaitMode(run, false)
}

func (a *SessionActor) detachSessionRunWait(run *sessionRunControl) {
	if run == nil {
		return
	}
	run.runWaitMu.Lock()
	run.runWaitDetached = true
	run.runWaitMu.Unlock()
}

func (a *SessionActor) releaseDetachedSessionRunWait(run *sessionRunControl) {
	a.releaseSessionRunWaitMode(run, true)
}

func (a *SessionActor) trackSessionRunWait(run *sessionRunControl) {
	if a == nil || run == nil {
		return
	}
	run.runWaitMu.Lock()
	defer run.runWaitMu.Unlock()
	if run.runWaitTracked {
		return
	}
	a.activeRunWG.Add(1)
	run.runWaitTracked = true
	if run.runWaitReleasePending {
		run.runWaitOnce.Do(func() {
			a.activeRunWG.Done()
		})
	}
}

func (a *SessionActor) releaseSessionRunWaitMode(run *sessionRunControl, allowDetached bool) {
	if a == nil || run == nil {
		return
	}
	run.runWaitMu.Lock()
	defer run.runWaitMu.Unlock()
	if run.runWaitDetached && !allowDetached {
		return
	}
	if !run.runWaitTracked {
		// A forced supersession can win the race with asynchronous run startup.
		// Remember the release so tracking performs a balanced Add/Done pair.
		run.runWaitReleasePending = true
		return
	}
	run.runWaitOnce.Do(func() {
		a.activeRunWG.Done()
	})
}

func sessionRunEventMatches(run *sessionRunControl, event runtimeevents.Event, sessionID string) bool {
	if run == nil {
		return false
	}
	sessionMatched := false
	if eventSessionID := strings.TrimSpace(event.SessionID); eventSessionID != "" {
		if eventSessionID != strings.TrimSpace(sessionID) {
			return false
		}
		sessionMatched = true
	}
	if event.Payload != nil {
		if eventTurnID, ok := event.Payload["turn_id"].(string); ok && strings.TrimSpace(eventTurnID) != "" {
			return strings.TrimSpace(eventTurnID) == run.turnID
		}
	}
	if traceID := strings.TrimSpace(event.TraceID); strings.HasPrefix(traceID, "turn_") {
		return traceID == run.turnID
	}
	// Anonymous events on a shared agent bus are not proof of progress for this
	// session; require at least a positive session match when no turn is present.
	return sessionMatched
}

func (a *SessionActor) claimSessionRun(turnID string, stripMetadataKeys []string, reply chan SubmitResult) *sessionRunControl {
	if a == nil {
		return nil
	}
	a.runLifecycleMu.Lock()
	defer a.runLifecycleMu.Unlock()

	run := &sessionRunControl{
		sessionID:         a.id,
		generation:        a.runSequence.Add(1),
		turnID:            strings.TrimSpace(turnID),
		stripMetadataKeys: normalizedMetadataKeys(append(append([]string{}, stripMetadataKeys...), runtimetypes.MetadataKeyTransientPrompt)),
		reply:             reply,
	}
	run.lastActivity.Store(time.Now().UnixNano())

	var (
		previous         *sessionRunControl
		superseded       *sessionRunControl
		supersededCancel context.CancelFunc
	)
	a.mu.Lock()
	if previous = a.activeRun; previous != nil {
		// A completed run can briefly remain installed after it exposes
		// idle/stopped state. Replacing that finalizing token is safe, but
		// canceling it would incorrectly discard its successful public result.
		if !previous.finalizing.Load() {
			superseded = previous
			a.detachSessionRunWait(previous)
			previous.interrupted.Store(true)
			previous.abandoned.Store(true)
			supersededCancel = previous.cancel
			previous.cancel = nil
		}
	}
	a.mu.Unlock()
	if supersededCancel != nil {
		supersededCancel()
	}
	if superseded != nil {
		superseded.complete(SubmitResult{Err: context.Canceled})
	}

	// Do not install the successor until any predecessor write that passed its
	// ownership check has drained. Cancellation/completion happen first so an
	// uncooperative provider or cancel-aware store cannot strand the old caller.
	a.sessionPersistMu.Lock()
	a.mu.Lock()
	a.activeRun = run
	a.mu.Unlock()
	a.sessionPersistMu.Unlock()
	if superseded != nil {
		a.releaseDetachedSessionRunWait(superseded)
	}
	return run
}

func (a *SessionActor) installSessionRunCancel(run *sessionRunControl, cancel context.CancelFunc) bool {
	if a == nil || run == nil || cancel == nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.activeRun != run || run.abandoned.Load() || run.interrupted.Load() {
		return false
	}
	run.cancel = cancel
	return true
}

func (a *SessionActor) clearSessionRunCancel(run *sessionRunControl) {
	if a == nil || run == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.activeRun == run {
		run.cancel = nil
	}
}

func (a *SessionActor) sessionRunOwned(run *sessionRunControl) bool {
	if a == nil || run == nil || run.abandoned.Load() {
		return false
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.activeRun == run && a.activeRun.generation == run.generation
}

func (a *SessionActor) sessionRunOwnsRunningState(run *sessionRunControl) bool {
	if a == nil || run == nil || run.abandoned.Load() || run.interrupted.Load() {
		return false
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.activeRun == run &&
		a.activeRun.generation == run.generation &&
		a.state != nil &&
		a.state.Status == SessionRunning &&
		a.state.CurrentTurnID == run.turnID
}

func (a *SessionActor) activeSessionRunMetadataKeys() []string {
	if a == nil {
		return nil
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.activeRun == nil || a.activeRun.abandoned.Load() || a.activeRun.interrupted.Load() {
		return nil
	}
	return append([]string(nil), a.activeRun.stripMetadataKeys...)
}

func (a *SessionActor) releaseSessionRun(run *sessionRunControl) {
	if a == nil || run == nil {
		return
	}
	var cancel context.CancelFunc
	finished := false
	a.mu.Lock()
	if a.activeRun == run {
		cancel = run.cancel
		run.cancel = nil
		a.activeRun = nil
		finished = true
	}
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	// The run that owned the actor slot has fully ended (or was superseded by
	// a newer run, in which case the newer run owns the slot and will trigger
	// this callback when it ends). Notify the host so a run-scoped session
	// lease can be released while the actor stays idle.
	if finished && a.onRunFinished != nil {
		a.onRunFinished()
	}
}

func (a *SessionActor) abandonSessionRun(run *sessionRunControl) bool {
	if a == nil || run == nil {
		return false
	}
	a.runLifecycleMu.Lock()
	defer a.runLifecycleMu.Unlock()

	var cancel context.CancelFunc
	a.mu.Lock()
	if a.activeRun != run || run.abandoned.Load() || run.finalizing.Load() {
		a.mu.Unlock()
		return false
	}
	a.detachSessionRunWait(run)
	run.interrupted.Store(true)
	run.abandoned.Store(true)
	cancel = run.cancel
	run.cancel = nil
	a.activeRun = nil
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}

	// A persist that passed its ownership check before abandonment may still be
	// writing a full session row. Drain it before OnRunStalled can release the
	// lease or a local successor can load the session, fencing stale history.
	a.sessionPersistMu.Lock()
	a.sessionPersistMu.Unlock()
	return true
}

func (a *SessionActor) interruptActiveSessionRun() *sessionRunControl {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	run := a.activeRun
	var cancel context.CancelFunc
	if run != nil {
		run.interrupted.Store(true)
		cancel = run.cancel
		run.cancel = nil
	}
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return run
}

func (a *SessionActor) cancelActive() {
	if a == nil {
		return
	}
	a.mu.Lock()
	var cancel context.CancelFunc
	if a.activeRun != nil {
		cancel = a.activeRun.cancel
		a.activeRun.cancel = nil
	}
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// RequestApproval implements runtimepolicy.ApprovalHandler for interactive approvals.
func (a *SessionActor) RequestApproval(ctx context.Context, req runtimepolicy.ApprovalRequest) (runtimepolicy.ApprovalResponse, error) {
	if a == nil {
		return runtimepolicy.ApprovalResponse{}, fmt.Errorf("session actor is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(req.ID) == "" {
		req.ID = "approval_" + uuid.NewString()
	}
	if strings.TrimSpace(req.SessionID) == "" {
		req.SessionID = a.id
	}
	if approvalRequestExpired(&req, time.Now().UTC()) {
		return runtimepolicy.ApprovalResponse{}, approvalExpiredError(&req)
	}
	pendingTool, err := newPendingToolInvocation(ctx, req.ToolCallID, req.ToolName, req.ArgsJSON)
	if err != nil {
		return runtimepolicy.ApprovalResponse{}, err
	}
	if err := a.recordPendingToolCall(ctx, pendingTool); err != nil {
		return runtimepolicy.ApprovalResponse{}, err
	}
	pending := &ApprovalRequest{
		ID:         req.ID,
		SessionID:  req.SessionID,
		ToolCallID: req.ToolCallID,
		ToolName:   req.ToolName,
		ArgsJSON:   req.ArgsJSON,
		Reason:     req.Reason,
		RiskLevel:  req.RiskLevel,
		ExpiresAt:  req.ExpiresAt,
	}
	if err := a.updateState(ctx, func(state *RuntimeState) error {
		state.PendingTool = pendingTool
		state.PendingApproval = pending
		state.Status = SessionWaitingApproval
		state.UpdatedAt = time.Now().UTC()
		return nil
	}); err != nil {
		return runtimepolicy.ApprovalResponse{}, err
	}
	waiter := a.registerApprovalWaiter(req.ID)
	state := a.StateForInspection()
	a.publish(runtimeevents.Event{
		Type:      EventApprovalRequested,
		SessionID: a.id,
		Payload:   approvalRequestedEventPayload(state, pending, req),
	})
	var expiryTimer *time.Timer
	var expiry <-chan time.Time
	if !pending.ExpiresAt.IsZero() {
		expiryTimer = time.NewTimer(time.Until(pending.ExpiresAt))
		expiry = expiryTimer.C
		defer expiryTimer.Stop()
	}
	select {
	case resp := <-waiter:
		return resp, nil
	case <-expiry:
		a.unregisterApprovalWaiter(req.ID)
		expiryErr := approvalExpiredError(pending)
		_ = a.updateStateConvergent(context.Background(), func(state *RuntimeState) error {
			if state.PendingApproval != nil && state.PendingApproval.ID == req.ID {
				state.PendingApproval = nil
				state.PendingTool = nil
				state.Status = approvalResumeStatus(state)
				state.UpdatedAt = time.Now().UTC()
			}
			return nil
		})
		payload := approvalResolvedEventPayload(state, req.ID, false)
		payload["resolution"] = "expired"
		payload["error_code"] = string(runtimeerrors.ErrApprovalExpired)
		a.publish(runtimeevents.Event{Type: EventApprovalResolved, SessionID: a.id, Payload: payload})
		return runtimepolicy.ApprovalResponse{}, expiryErr
	case <-ctx.Done():
		if detach, ok := ctx.Value(approvalDetachContextKey{}).(*approvalDetachState); ok && detach != nil {
			detach.detached.Store(true)
		}
		a.unregisterApprovalWaiter(req.ID)
		_ = a.updateStateConvergent(context.Background(), func(state *RuntimeState) error {
			if state.PendingApproval != nil && state.PendingApproval.ID == req.ID {
				state.Status = SessionWaitingApproval
				state.UpdatedAt = time.Now().UTC()
			}
			return nil
		})
		return runtimepolicy.ApprovalResponse{}, ctx.Err()
	}
}

func approvalRequestedEventPayload(state *RuntimeState, pending *ApprovalRequest, req runtimepolicy.ApprovalRequest) map[string]interface{} {
	payload := map[string]interface{}{
		"request_id": req.ID,
		"tool_name":  req.ToolName,
		"reason":     req.Reason,
		"risk_level": req.RiskLevel,
	}
	if pending != nil && strings.TrimSpace(pending.ToolCallID) != "" {
		payload["tool_call_id"] = strings.TrimSpace(pending.ToolCallID)
	}
	if pending != nil && !pending.ExpiresAt.IsZero() {
		payload["expires_at"] = pending.ExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	appendApprovalRunMetaPayload(payload, state)
	return payload
}

func approvalRequestExpired(req *ApprovalRequest, now time.Time) bool {
	return req != nil && !req.ExpiresAt.IsZero() && !now.Before(req.ExpiresAt)
}

func approvalResumeStatus(state *RuntimeState) SessionStatus {
	if state != nil && strings.TrimSpace(state.CurrentTurnID) != "" {
		return SessionRunning
	}
	return SessionIdle
}

func approvalExpiredError(req *ApprovalRequest) *runtimeerrors.RuntimeError {
	metadata := map[string]interface{}{}
	if req != nil {
		metadata["approval_id"] = strings.TrimSpace(req.ID)
		metadata["tool_call_id"] = strings.TrimSpace(req.ToolCallID)
		metadata["tool_name"] = strings.TrimSpace(req.ToolName)
		if !req.ExpiresAt.IsZero() {
			metadata["expired_at"] = req.ExpiresAt.UTC().Format(time.RFC3339Nano)
		}
	}
	return runtimeerrors.WrapWithContext(runtimeerrors.ErrApprovalExpired, "tool approval request expired", nil, metadata)
}

func approvalErrorMetadata(err *runtimeerrors.RuntimeError) map[string]interface{} {
	if err == nil {
		return nil
	}
	metadata := err.GetContext()
	metadata["error_code"] = string(err.Code)
	metadata["error_message"] = err.Message
	return metadata
}

func approvalResolvedEventPayload(state *RuntimeState, requestID string, allowed bool) map[string]interface{} {
	payload := map[string]interface{}{
		"request_id": strings.TrimSpace(requestID),
		"allowed":    allowed,
	}
	if state != nil && state.PendingApproval != nil {
		if toolName := strings.TrimSpace(state.PendingApproval.ToolName); toolName != "" {
			payload["tool_name"] = toolName
		}
		if toolCallID := strings.TrimSpace(state.PendingApproval.ToolCallID); toolCallID != "" {
			payload["tool_call_id"] = toolCallID
		}
	}
	appendApprovalRunMetaPayload(payload, state)
	return payload
}

// terminalApprovalResolvedEventPayload describes an approval that was retired
// with its run instead of being decided: the run already ended under its
// execution deadline / an external cancel, so no resume happened (P0-4).
func terminalApprovalResolvedEventPayload(state *RuntimeState, cancelSource string) map[string]interface{} {
	if state == nil || state.PendingApproval == nil {
		return nil
	}
	requestID := strings.TrimSpace(state.PendingApproval.ID)
	if requestID == "" {
		return nil
	}
	payload := approvalResolvedEventPayload(state, requestID, false)
	payload["resolution"] = ApprovalResolutionRunTerminated
	payload["resumed"] = false
	if reason := strings.TrimSpace(cancelSource); reason != "" {
		payload["run_terminal_reason"] = reason
	}
	return payload
}

func appendApprovalRunMetaPayload(payload map[string]interface{}, state *RuntimeState) {
	if payload == nil {
		return
	}
	if state == nil {
		return
	}
	if turnID := strings.TrimSpace(state.CurrentTurnID); turnID != "" {
		payload["turn_id"] = turnID
	}
	if state.CurrentRunMeta == nil {
		return
	}
	if permissionMode := strings.TrimSpace(state.CurrentRunMeta.PermissionMode); permissionMode != "" {
		payload["permission_mode"] = permissionMode
	}
	if state.CurrentRunMeta.Team == nil {
		return
	}
	teamMeta := state.CurrentRunMeta.Team
	if teamID := strings.TrimSpace(teamMeta.TeamID); teamID != "" {
		payload["team_id"] = teamID
	}
	if agentID := strings.TrimSpace(teamMeta.AgentID); agentID != "" {
		payload["agent_id"] = agentID
		payload["teammate_id"] = agentID
	}
	if taskID := strings.TrimSpace(teamMeta.CurrentTaskID); taskID != "" {
		payload["task_id"] = taskID
	}
	if difficulty := strings.TrimSpace(teamMeta.Difficulty); difficulty != "" {
		payload["difficulty"] = difficulty
	}
	if source := strings.TrimSpace(teamMeta.DifficultySource); source != "" {
		payload["difficulty_source"] = source
	}
	if rationale := strings.TrimSpace(teamMeta.DifficultyRationale); rationale != "" {
		payload["difficulty_rationale"] = rationale
	}
	if provider := strings.TrimSpace(teamMeta.RouteProvider); provider != "" {
		payload["route_provider"] = provider
	}
	if model := strings.TrimSpace(teamMeta.RouteModel); model != "" {
		payload["route_model"] = model
	}
	if effort := strings.TrimSpace(teamMeta.RouteReasoningEffort); effort != "" {
		payload["route_reasoning_effort"] = effort
	}
	if source := strings.TrimSpace(teamMeta.RouteSource); source != "" {
		payload["route_source"] = source
	}
	if len(teamMeta.RouteWarnings) > 0 {
		payload["route_warnings"] = append([]string(nil), teamMeta.RouteWarnings...)
	}
	if teamMeta.RouteFallbackUsed {
		payload["fallback_used"] = true
	}
	if fallbackReason := strings.TrimSpace(teamMeta.RouteFallbackReason); fallbackReason != "" {
		payload["fallback_reason"] = fallbackReason
	}
}

// AskUserQuestion implements toolbroker.UserInputHandler.
func (a *SessionActor) AskUserQuestion(ctx context.Context, req toolbroker.UserQuestionRequest) (string, error) {
	if a == nil {
		return "", fmt.Errorf("session actor is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(req.ID) == "" {
		req.ID = "question_" + uuid.NewString()
	}
	if strings.TrimSpace(req.SessionID) == "" {
		req.SessionID = a.id
	}
	pendingTool, err := newPendingToolInvocation(ctx, req.ToolCallID, toolbroker.ToolAskUserQuestion, askUserQuestionArgsJSON(req))
	if err != nil {
		return "", err
	}
	if err := a.recordPendingToolCall(ctx, pendingTool); err != nil {
		return "", err
	}
	pending := &UserQuestionRequest{
		ID:          req.ID,
		SessionID:   req.SessionID,
		Prompt:      req.Prompt,
		Suggestions: append([]string(nil), req.Suggestions...),
		Required:    req.Required,
		CreatedAt:   time.Now().UTC(),
		ExpiresAt:   req.ExpiresAt,
	}
	if err := a.updateState(ctx, func(state *RuntimeState) error {
		state.PendingTool = pendingTool
		state.PendingQuestion = pending
		state.Status = SessionWaitingInput
		state.UpdatedAt = time.Now().UTC()
		return nil
	}); err != nil {
		return "", err
	}
	waiter := a.registerQuestionWaiter(req.ID)
	turnID := agent.TurnIDFromContext(ctx)
	if turnID == "" {
		if state := a.StateForInspection(); state != nil {
			turnID = strings.TrimSpace(state.CurrentTurnID)
		}
	}
	payload := map[string]interface{}{
		"question_id": req.ID,
		"prompt":      req.Prompt,
		"required":    req.Required,
		"suggestions": req.Suggestions,
	}
	if turnID != "" {
		payload["turn_id"] = turnID
	}
	a.publish(runtimeevents.Event{
		Type:      EventQuestionAsked,
		SessionID: a.id,
		Payload:   payload,
	})
	select {
	case answer := <-waiter:
		return answer, nil
	case <-ctx.Done():
		a.unregisterQuestionWaiter(req.ID)
		_ = a.updateStateConvergent(context.Background(), func(state *RuntimeState) error {
			if state.PendingQuestion != nil && state.PendingQuestion.ID == req.ID {
				state.PendingQuestion = nil
				state.PendingTool = nil
				state.Status = SessionIdle
				state.CurrentTurnID = ""
				state.CurrentRunMeta = nil
				resetFrozenTurnTools(state)
				state.UpdatedAt = time.Now().UTC()
			}
			return nil
		})
		return "", ctx.Err()
	}
}

func (a *SessionActor) registerApprovalWaiter(requestID string) chan runtimepolicy.ApprovalResponse {
	a.waiterMu.Lock()
	defer a.waiterMu.Unlock()
	ch := make(chan runtimepolicy.ApprovalResponse, 1)
	a.approvalWaiters[requestID] = ch
	return ch
}

func (a *SessionActor) unregisterApprovalWaiter(requestID string) {
	a.waiterMu.Lock()
	defer a.waiterMu.Unlock()
	delete(a.approvalWaiters, requestID)
}

func (a *SessionActor) resolveApproval(requestID string, resp runtimepolicy.ApprovalResponse) bool {
	a.waiterMu.Lock()
	ch := a.approvalWaiters[requestID]
	delete(a.approvalWaiters, requestID)
	a.waiterMu.Unlock()
	if ch == nil {
		return false
	}
	select {
	case ch <- resp:
	default:
	}
	return true
}

// hasApprovalWaiter reports whether this actor still has a run blocked on the
// approval, i.e. the decision can be applied in place instead of resuming.
func (a *SessionActor) hasApprovalWaiter(requestID string) bool {
	if a == nil {
		return false
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return false
	}
	a.waiterMu.Lock()
	defer a.waiterMu.Unlock()
	_, ok := a.approvalWaiters[requestID]
	return ok
}

// recordApprovalOutcome stores how the decision for requestID was applied.
func (a *SessionActor) recordApprovalOutcome(requestID, resolution string, resumed bool) {
	if a == nil {
		return
	}
	a.waiterMu.Lock()
	defer a.waiterMu.Unlock()
	a.lastApprovalOutcome = approvalOutcome{
		requestID:  strings.TrimSpace(requestID),
		resolution: strings.TrimSpace(resolution),
		resumed:    resumed,
	}
}

// ApprovalOutcome reports how the most recent decision for requestID was applied
// (P0-3): resolution is one of the ApprovalResolution* constants and resumed
// reports whether a recovery run was started to apply it. It returns ("", false)
// when another entry resolved the request or the decision predates this actor
// instance, so hosts can fall back to echoing the request.
func (a *SessionActor) ApprovalOutcome(requestID string) (string, bool) {
	if a == nil {
		return "", false
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return "", false
	}
	a.waiterMu.Lock()
	defer a.waiterMu.Unlock()
	if strings.TrimSpace(a.lastApprovalOutcome.requestID) != requestID {
		return "", false
	}
	return a.lastApprovalOutcome.resolution, a.lastApprovalOutcome.resumed
}

// pendingApprovalRunTerminal reports whether the pending approval belongs to a
// run that already ended in a terminal cancellation class (P0-1). The verdict
// comes from the durable reason field — never from "no active run" — and a live
// approval waiter vetoes it: that waiter proves a run is still blocked on this
// decision, so the normal resume path stays intact. The gray-release switch
// (SessionActorConfig.ApprovalTerminalGuard, plan §8) turns the whole guard
// off, restoring the pre-fix resume path.
func (a *SessionActor) pendingApprovalRunTerminal(state *RuntimeState, requestID string) (string, bool) {
	if a == nil || state == nil {
		return "", false
	}
	if !a.terminalGuard {
		// 灰度关闭（§8）：回退到引入守卫前的行为，迟到决议仍走旧恢复路径。
		return "", false
	}
	reason := strings.TrimSpace(state.LastRunTerminalReason)
	if reason == "" {
		return "", false
	}
	if a.hasApprovalWaiter(requestID) {
		return "", false
	}
	return reason, true
}

func (a *SessionActor) registerQuestionWaiter(questionID string) chan string {
	a.waiterMu.Lock()
	defer a.waiterMu.Unlock()
	ch := make(chan string, 1)
	a.questionWaiters[questionID] = ch
	return ch
}

func (a *SessionActor) unregisterQuestionWaiter(questionID string) {
	a.waiterMu.Lock()
	defer a.waiterMu.Unlock()
	delete(a.questionWaiters, questionID)
}

func (a *SessionActor) resolveQuestion(questionID, answer string) bool {
	a.waiterMu.Lock()
	ch := a.questionWaiters[questionID]
	delete(a.questionWaiters, questionID)
	a.waiterMu.Unlock()
	if ch == nil {
		return false
	}
	select {
	case ch <- answer:
	default:
	}
	return true
}

func newPendingToolInvocation(ctx context.Context, toolCallID, toolName string, argsJSON json.RawMessage) (*PendingToolInvocation, error) {
	toolName = strings.TrimSpace(toolName)
	if toolName == "" {
		return nil, fmt.Errorf("tool name is required")
	}
	toolCallID = strings.TrimSpace(toolCallID)
	if toolCallID == "" {
		toolCallID = pendingToolCallIDFromContext(ctx, toolName, argsJSON)
	}
	pending := &PendingToolInvocation{
		ToolCallID: toolCallID,
		ToolName:   toolName,
		CreatedAt:  time.Now().UTC(),
	}
	if len(argsJSON) > 0 {
		pending.ArgsJSON = append(json.RawMessage(nil), argsJSON...)
	}
	pending.BatchToolCalls = pendingToolCallsFromContext(ctx, pending.ToolCallID, pending.ToolName, pending.ArgsJSON)
	for _, call := range pending.BatchToolCalls {
		if call.ToolCallID == pending.ToolCallID {
			pending.ToolType = call.ToolType
			pending.RawInput = call.RawInput
			break
		}
	}
	return pending, nil
}

func pendingToolCallIDFromContext(ctx context.Context, toolName string, argsJSON json.RawMessage) string {
	batchCtx, ok := agent.ToolBatchContextFromContext(ctx)
	if !ok || len(batchCtx.ToolCalls) == 0 {
		return runtimetypes.DeterministicToolCallIDFromJSON("toolcall_pending_", 0, toolName, argsJSON)
	}

	if current := strings.TrimSpace(batchCtx.CurrentToolCallID); current != "" {
		return current
	}

	batch := normalizedPendingToolCalls(batchCtx.ToolCalls)
	currentIndex := len(batchCtx.CompletedToolMessages)
	if currentIndex >= 0 && currentIndex < len(batch) && strings.TrimSpace(batch[currentIndex].ToolCallID) != "" {
		return batch[currentIndex].ToolCallID
	}

	canonicalArgs := canonicalPendingToolArgsJSON(argsJSON)
	normalizedToolName := strings.TrimSpace(toolName)
	for _, call := range batch {
		if strings.TrimSpace(call.ToolName) != normalizedToolName {
			continue
		}
		if canonicalPendingToolArgsJSON(call.ArgsJSON) == canonicalArgs {
			return call.ToolCallID
		}
	}

	return runtimetypes.DeterministicToolCallIDFromJSON("toolcall_", 0, toolName, argsJSON)
}

func pendingToolCallsFromContext(ctx context.Context, toolCallID, toolName string, argsJSON json.RawMessage) []PendingToolCall {
	batchCtx, ok := agent.ToolBatchContextFromContext(ctx)
	if !ok || len(batchCtx.ToolCalls) == 0 {
		return []PendingToolCall{{
			ToolCallID: strings.TrimSpace(toolCallID),
			ToolName:   strings.TrimSpace(toolName),
			ArgsJSON:   append(json.RawMessage(nil), argsJSON...),
		}}
	}
	calls := normalizedPendingToolCalls(batchCtx.ToolCalls)
	if len(calls) == 0 {
		calls = append(calls, PendingToolCall{
			ToolCallID: strings.TrimSpace(toolCallID),
			ToolName:   strings.TrimSpace(toolName),
			ArgsJSON:   append(json.RawMessage(nil), argsJSON...),
		})
		return calls
	}

	current := PendingToolCall{
		ToolCallID: strings.TrimSpace(toolCallID),
		ToolName:   strings.TrimSpace(toolName),
		ArgsJSON:   append(json.RawMessage(nil), argsJSON...),
	}
	currentIndex := len(batchCtx.CompletedToolMessages)
	if currentIndex >= 0 && currentIndex < len(batchCtx.ToolCalls) {
		current.ToolType = batchCtx.ToolCalls[currentIndex].Type
		current.RawInput = batchCtx.ToolCalls[currentIndex].RawInput
	}
	if current.ToolCallID == "" {
		return calls
	}
	for _, call := range calls {
		if call.ToolCallID == current.ToolCallID {
			return calls
		}
	}

	if currentIndex >= 0 && currentIndex < len(calls) {
		calls[currentIndex] = current
		return calls
	}
	return append(calls, current)
}

func normalizedPendingToolCalls(toolCalls []runtimetypes.ToolCall) []PendingToolCall {
	if len(toolCalls) == 0 {
		return nil
	}

	calls := make([]PendingToolCall, 0, len(toolCalls))
	for index, call := range toolCalls {
		payload, err := json.Marshal(call.Args)
		if err != nil || string(payload) == "null" {
			payload = nil
		}
		callID := strings.TrimSpace(call.ID)
		if callID == "" {
			callID = runtimetypes.DeterministicToolCallIDFromJSON("toolcall_", index, call.Name, payload)
		}
		calls = append(calls, PendingToolCall{
			ToolCallID: callID,
			ToolType:   call.Type,
			ToolName:   strings.TrimSpace(call.Name),
			ArgsJSON:   append(json.RawMessage(nil), payload...),
			RawInput:   call.RawInput,
		})
	}
	return calls
}

func canonicalPendingToolArgsJSON(argsJSON json.RawMessage) string {
	trimmed := strings.TrimSpace(string(argsJSON))
	if trimmed == "" || strings.EqualFold(trimmed, "null") {
		return "{}"
	}
	var decoded interface{}
	if err := json.Unmarshal(argsJSON, &decoded); err != nil {
		return trimmed
	}
	canonical, err := json.Marshal(decoded)
	if err != nil || len(canonical) == 0 || string(canonical) == "null" {
		return "{}"
	}
	return string(canonical)
}

func ensurePendingToolBatchInSession(session *Session, pending *PendingToolInvocation, completed []runtimetypes.Message) {
	if session == nil || pending == nil {
		return
	}
	batch := pendingRuntimeToolCalls(session, pending)
	if len(batch) == 0 {
		return
	}
	index := assistantMessageIndexForToolCall(session, pending.ToolCallID)
	if index >= 0 {
		session.History[index].ToolCalls = batch
	} else {
		assistant := runtimetypes.NewAssistantMessage("")
		assistant.ToolCalls = batch
		session.AddMessage(*assistant)
	}
	for _, message := range completed {
		if message.Role != "tool" || strings.TrimSpace(message.ToolCallID) == "" || sessionHasToolResult(session, message.ToolCallID) {
			continue
		}
		session.AddMessage(*message.Clone())
	}
}

func pendingRuntimeToolCalls(session *Session, pending *PendingToolInvocation) []runtimetypes.ToolCall {
	if pending == nil {
		return nil
	}
	if len(pending.BatchToolCalls) == 0 {
		if batch := sessionToolCallsForPending(session, pending.ToolCallID); len(batch) > 0 {
			return batch
		}
		return []runtimetypes.ToolCall{{
			ID:       pending.ToolCallID,
			Type:     pending.ToolType,
			Name:     pending.ToolName,
			Args:     decodePendingToolArgs(pending.ArgsJSON),
			RawInput: pending.RawInput,
		}}
	}
	calls := make([]runtimetypes.ToolCall, 0, len(pending.BatchToolCalls))
	for _, call := range pending.BatchToolCalls {
		callID := strings.TrimSpace(call.ToolCallID)
		if callID == "" {
			continue
		}
		calls = append(calls, runtimetypes.ToolCall{
			ID:       callID,
			Type:     call.ToolType,
			Name:     strings.TrimSpace(call.ToolName),
			Args:     decodePendingToolArgs(call.ArgsJSON),
			RawInput: call.RawInput,
		})
	}
	return calls
}

func sessionToolCallsForPending(session *Session, toolCallID string) []runtimetypes.ToolCall {
	if session == nil || strings.TrimSpace(toolCallID) == "" {
		return nil
	}
	index := assistantMessageIndexForToolCall(session, toolCallID)
	if index < 0 || index >= len(session.History) {
		return nil
	}
	message := session.History[index]
	if len(message.ToolCalls) == 0 {
		return nil
	}
	calls := make([]runtimetypes.ToolCall, len(message.ToolCalls))
	for i := range message.ToolCalls {
		calls[i] = runtimetypes.ToolCall{
			ID:       message.ToolCalls[i].ID,
			Type:     message.ToolCalls[i].Type,
			Name:     message.ToolCalls[i].Name,
			RawInput: message.ToolCalls[i].RawInput,
		}
		if len(message.ToolCalls[i].Args) > 0 {
			calls[i].Args = make(map[string]interface{}, len(message.ToolCalls[i].Args))
			for key, value := range message.ToolCalls[i].Args {
				calls[i].Args[key] = value
			}
		}
	}
	return calls
}

func assistantMessageIndexForToolCall(session *Session, toolCallID string) int {
	if session == nil || strings.TrimSpace(toolCallID) == "" {
		return -1
	}
	for index := len(session.History) - 1; index >= 0; index-- {
		message := session.History[index]
		if message.Role != "assistant" || len(message.ToolCalls) == 0 {
			continue
		}
		if toolCall, ok := message.GetToolCall(toolCallID); ok && toolCall != nil {
			return index
		}
	}
	return -1
}

func askUserQuestionArgsJSON(req toolbroker.UserQuestionRequest) json.RawMessage {
	payload, err := json.Marshal(toolbroker.AskUserQuestionArgs{
		Prompt:      req.Prompt,
		Suggestions: append([]string(nil), req.Suggestions...),
		Required:    req.Required,
	})
	if err != nil {
		return nil
	}
	return payload
}

func decodePendingToolArgs(argsJSON json.RawMessage) map[string]interface{} {
	if len(argsJSON) == 0 {
		return map[string]interface{}{}
	}
	decoded := map[string]interface{}{}
	if err := json.Unmarshal(argsJSON, &decoded); err != nil {
		return map[string]interface{}{}
	}
	return decoded
}

func applyPendingToolPatchedArgs(pending *PendingToolInvocation, argsJSON json.RawMessage) {
	if pending == nil || len(argsJSON) == 0 {
		return
	}
	pending.ArgsJSON = append(json.RawMessage(nil), argsJSON...)
	if strings.EqualFold(strings.TrimSpace(pending.ToolType), "custom_tool_call") {
		pending.RawInput = pendingFreeformInput(argsJSON, pending.RawInput)
	}
	for index := range pending.BatchToolCalls {
		call := &pending.BatchToolCalls[index]
		if strings.TrimSpace(call.ToolCallID) != strings.TrimSpace(pending.ToolCallID) {
			continue
		}
		call.ArgsJSON = append(json.RawMessage(nil), argsJSON...)
		call.ToolType = pending.ToolType
		call.RawInput = pending.RawInput
		break
	}
}

func pendingFreeformInput(argsJSON json.RawMessage, fallback string) string {
	args := decodePendingToolArgs(argsJSON)
	if raw, ok := args["_raw"].(string); ok {
		return raw
	}
	value := ""
	for key, raw := range args {
		if strings.HasPrefix(strings.TrimSpace(key), "_") {
			continue
		}
		text, ok := raw.(string)
		if !ok || value != "" {
			return fallback
		}
		value = text
	}
	if value == "" {
		return fallback
	}
	return value
}

func encodePendingToolResultMessage(message *runtimetypes.Message) (json.RawMessage, error) {
	if message == nil {
		return nil, fmt.Errorf("message is nil")
	}
	payload, err := json.Marshal(message)
	if err != nil {
		return nil, err
	}
	return payload, nil
}

func decodePendingToolResultMessage(payload json.RawMessage) (*runtimetypes.Message, bool) {
	if len(payload) == 0 {
		return nil, false
	}
	var message runtimetypes.Message
	if err := json.Unmarshal(payload, &message); err != nil {
		return nil, false
	}
	return &message, true
}

// extractToolOutput decodes the persisted tool result message and returns its
// content text, bounded to a summary length so the trajectory event store is
// not bloated by large tool outputs. Returns "" when there is no decodable
// content.
func extractToolOutput(messageJSON json.RawMessage) string {
	if len(messageJSON) == 0 {
		return ""
	}
	message, ok := decodePendingToolResultMessage(messageJSON)
	if !ok || message == nil {
		return ""
	}
	const maxOutput = 4096
	if len(message.Content) > maxOutput {
		return message.Content[:maxOutput]
	}
	return message.Content
}

func (a *SessionActor) toolReceiptStore() ToolReceiptStore {
	if a == nil || a.stateStore == nil {
		return nil
	}
	store, _ := a.stateStore.(ToolReceiptStore)
	return store
}

func (a *SessionActor) saveStoredToolReceipt(ctx context.Context, receipt ToolExecutionReceipt) error {
	store := a.toolReceiptStore()
	if store == nil {
		return nil
	}
	return store.SaveToolReceipt(ctx, receipt)
}

func (a *SessionActor) loadStoredToolReceipt(ctx context.Context, sessionID, toolCallID string) (*ToolExecutionReceipt, *runtimetypes.Message, bool, error) {
	store := a.toolReceiptStore()
	if store == nil {
		return nil, nil, false, nil
	}
	receipt, err := store.GetToolReceipt(ctx, sessionID, toolCallID)
	if err != nil || receipt == nil {
		return nil, nil, false, err
	}
	message, ok := decodePendingToolResultMessage(receipt.MessageJSON)
	if !ok {
		return nil, nil, false, fmt.Errorf("failed to decode stored tool receipt for %s", toolCallID)
	}
	return receipt, message, true, nil
}

func (a *SessionActor) deleteStoredToolReceipt(ctx context.Context, sessionID, toolCallID string) {
	store := a.toolReceiptStore()
	if store == nil {
		return
	}
	_ = store.DeleteToolReceipt(ctx, sessionID, toolCallID)
}

func newToolExecutionReceipt(sessionID, toolCallID, toolName string, messageJSON json.RawMessage, createdAt time.Time) ToolExecutionReceipt {
	receipt := ToolExecutionReceipt{
		SessionID:  strings.TrimSpace(sessionID),
		ToolCallID: strings.TrimSpace(toolCallID),
		ToolName:   strings.TrimSpace(toolName),
		CreatedAt:  createdAt.UTC(),
	}
	if receipt.CreatedAt.IsZero() {
		receipt.CreatedAt = time.Now().UTC()
	}
	if len(messageJSON) > 0 {
		receipt.MessageJSON = append(json.RawMessage(nil), messageJSON...)
	}
	return receipt
}

func toolExecutionReceiptFromPending(sessionID string, pending *PendingToolInvocation) ToolExecutionReceipt {
	if pending == nil {
		return ToolExecutionReceipt{SessionID: strings.TrimSpace(sessionID)}
	}
	createdAt := pending.ExecutionCompletedAt
	if createdAt.IsZero() {
		createdAt = pending.CreatedAt
	}
	receipt := newToolExecutionReceipt(sessionID, pending.ToolCallID, pending.ToolName, pending.ResultMessageJSON, createdAt)
	if len(pending.ArgsJSON) > 0 {
		receipt.ArgsJSON = append(json.RawMessage(nil), pending.ArgsJSON...)
	}
	return receipt
}

func (a *SessionActor) publishToolReceiptEvent(eventType, turnID, source string, receipt ToolExecutionReceipt) {
	if a == nil {
		return
	}
	turnID = strings.TrimSpace(turnID)
	messageHash := sha256.Sum256(receipt.MessageJSON)
	payload := map[string]interface{}{
		"tool_call_id": receipt.ToolCallID,
		"source":       strings.TrimSpace(source),
		"receipt": map[string]interface{}{
			"session_id":     receipt.SessionID,
			"tool_call_id":   receipt.ToolCallID,
			"message_bytes":  len(receipt.MessageJSON),
			"message_sha256": fmt.Sprintf("%x", messageHash),
			"created_at":     receipt.CreatedAt,
		},
	}
	// Receipt callers supply the owning logical turn, not a provider trace ID.
	// Make that identity explicit so the strict TUI turn fence can admit a
	// current-turn receipt and still reject a retired-turn receipt. Keep the
	// legacy Event.TraceID alias below for older observers; new consumers must
	// read payload.turn_id and must not infer ownership from TraceID.
	if turnID != "" {
		payload["turn_id"] = turnID
		payload["receipt"].(map[string]interface{})["turn_id"] = turnID
	}
	if strings.TrimSpace(receipt.ToolName) != "" {
		payload["tool_name"] = receipt.ToolName
		payload["receipt"].(map[string]interface{})["tool_name"] = receipt.ToolName
	}
	// 方案 §0.3：失败工具的回执必须携带 ok=false 与失败分类，否则审计侧
	// 只能靠解析 message_json 才能区分成功/失败。
	if receipt.OK != nil {
		payload["ok"] = *receipt.OK
		payload["receipt"].(map[string]interface{})["ok"] = *receipt.OK
	}
	if strings.TrimSpace(receipt.FailureCategory) != "" {
		payload["failure_category"] = strings.TrimSpace(receipt.FailureCategory)
		payload["receipt"].(map[string]interface{})["failure_category"] = strings.TrimSpace(receipt.FailureCategory)
	}
	// Enrich the payload with call arguments and a bounded result output so the
	// trajectory can reconstruct full tool info after recovery (P0). Args are
	// the raw JSON-encoded parameter object; output is the decoded result text
	// truncated to a summary length.
	if len(receipt.ArgsJSON) > 0 {
		payload["args"] = string(receipt.ArgsJSON)
	}
	if output := extractToolOutput(receipt.MessageJSON); output != "" {
		payload["output"] = output
	}
	a.publish(runtimeevents.Event{
		Type:      eventType,
		SessionID: a.id,
		TraceID:   turnID,
		ToolName:  receipt.ToolName,
		Payload:   payload,
	})
}

func sessionHasToolCall(session *Session, toolCallID string) bool {
	if session == nil || strings.TrimSpace(toolCallID) == "" {
		return false
	}
	for _, message := range session.History {
		if toolCall, ok := message.GetToolCall(toolCallID); ok && toolCall != nil {
			return true
		}
	}
	return false
}

func sessionHasToolResult(session *Session, toolCallID string) bool {
	if session == nil || strings.TrimSpace(toolCallID) == "" {
		return false
	}
	for _, message := range session.History {
		if message.Role == "tool" && strings.TrimSpace(message.ToolCallID) == strings.TrimSpace(toolCallID) {
			return true
		}
	}
	return false
}

func (a *SessionActor) configureRuntime() {
	if a == nil || a.agent == nil {
		return
	}

	engine := a.agent.GetPermissionEngine()
	if engine == nil {
		engine = agent.NewPermissionEngine()
		engine.AskHandler = a
		if hookMgr := a.agent.GetHookManager(); hookMgr != nil {
			engine.Hooks = hookMgr
		}
		a.agent.SetPermissionEngine(engine)
	} else if engine.AskHandler == nil {
		engine.AskHandler = a
	}
	runtimepolicy.EnsurePlanWriteAllowPaths(engine)
	a.applyPlanModeToEngine(engine)

	broker := a.agent.GetToolBroker()
	if broker == nil {
		a.agent.SetToolBroker(&toolbroker.Broker{UserInput: a, PlanMode: a})
	} else {
		if broker.UserInput == nil {
			broker.UserInput = a
		}
		if broker.PlanMode == nil {
			broker.PlanMode = a
		}
	}
}

// applyPlanModeToEngine loads durable plan_mode context and forces plan write
// allow paths when the session is actively in plan mode.
func (a *SessionActor) applyPlanModeToEngine(engine *runtimepolicy.Engine) {
	if a == nil || engine == nil {
		return
	}
	var state planmode.State
	if a.sessionStore != nil {
		if session, err := a.sessionStore.Load(context.Background(), a.id); err == nil && session != nil {
			state = planmode.Load(session)
		}
	}
	a.applyPlanModeStateToEngine(engine, state)
}

// applyPlanModeStateToEngine applies an already-loaded plan state to the engine.
func (a *SessionActor) applyPlanModeStateToEngine(engine *runtimepolicy.Engine, state planmode.State) {
	if engine == nil {
		return
	}
	if !planmode.IsActive(state) {
		if engine.Mode == runtimepolicy.ModePlan {
			// Bare permission_mode=plan (no durable plan_mode state) still gets default allow paths.
			runtimepolicy.EnsurePlanWriteAllowPaths(engine)
		}
		return
	}
	planmode.ApplyToEngine(engine, state)
}

// applyDurablePlanModeToRun re-applies durable plan state after prepareRun and
// pins the run meta of the current turn to plan while that state is active.
//
// The agent loop evaluates tools with EvalRequest.Mode taken from run meta
// (permissionModeFromContext), and policy.Engine.Evaluate lets that per-run mode
// win over engine.Mode. A stale run meta - typically the pre-plan
// bypass_permissions snapshotted by the host before this turn - would otherwise
// silently disable plan-mode write gating for the whole turn even though the
// engine itself is in plan mode. Pinning the in-context run meta keeps the two
// sources of truth aligned.
func (a *SessionActor) applyDurablePlanModeToRun(ctx context.Context, session *Session) {
	if a == nil || session == nil {
		return
	}
	state := planmode.Load(session)
	if engine := a.agent.GetPermissionEngine(); engine != nil {
		a.applyPlanModeStateToEngine(engine, state)
	}
	if planmode.IsActive(state) {
		a.syncLivePermissionMode(ctx, string(runtimepolicy.ModePlan))
	}
}

func (a *SessionActor) publish(event runtimeevents.Event) {
	if a == nil {
		return
	}
	if event.SessionID == "" {
		event.SessionID = a.id
	}
	a.mu.RLock()
	run := a.activeRun
	a.mu.RUnlock()
	a.touchRunActivity(run, event)
	if event.AgentName == "" && a.agent != nil && a.agent.GetConfig() != nil {
		event.AgentName = a.agent.GetConfig().Name
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	if a.eventStore != nil {
		seq, err := a.eventStore.AppendEvent(context.Background(), event)
		if err == nil {
			if event.Payload == nil {
				event.Payload = map[string]interface{}{}
			}
			event.Payload["seq"] = seq
		}
	}
	if a.eventBus != nil {
		a.eventBus.Publish(event)
	}
}

func resultTraceID(result *agent.Result, fallback string) string {
	if result != nil && strings.TrimSpace(result.TraceID) != "" {
		return result.TraceID
	}
	return fallback
}

func resultSteps(result *agent.Result) int {
	if result == nil {
		return 0
	}
	return result.Steps
}

func durationMillis(result *agent.Result) int64 {
	if result == nil {
		return 0
	}
	return result.Duration.GetDuration().Milliseconds()
}

func appendSessionActorUsagePayload(payload map[string]interface{}, usage *runtimetypes.TokenUsage) {
	if payload == nil || usage == nil || usage.IsZero() {
		return
	}
	if usage.PromptTokens > 0 {
		payload["usage_prompt_tokens"] = usage.PromptTokens
	}
	if usage.CompletionTokens > 0 {
		payload["usage_completion_tokens"] = usage.CompletionTokens
	}
	if usage.TotalTokens > 0 {
		payload["usage_total_tokens"] = usage.TotalTokens
	}
	if usage.CachedTokens > 0 {
		payload["usage_cached_tokens"] = usage.CachedTokens
	}
	cacheReadTokens := usage.CacheReadTokens
	if cacheReadTokens == 0 {
		cacheReadTokens = usage.CachedTokens
	}
	if cacheReadTokens > 0 {
		payload["usage_cache_read_tokens"] = cacheReadTokens
	}
	if usage.CacheCreationTokens > 0 {
		payload["usage_cache_creation_tokens"] = usage.CacheCreationTokens
	}
	payload["usage_cache_read_reported"] = usage.CacheReadReported || usage.CachedTokens > 0
	if usage.ReasoningTokens > 0 {
		payload["usage_reasoning_tokens"] = usage.ReasoningTokens
	}
	if source := strings.TrimSpace(usage.UsageSource); source != "" {
		payload["usage_source"] = source
	}
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func firstNonEmptyError(err error, result *agent.Result) string {
	if value := errorString(err); strings.TrimSpace(value) != "" {
		return value
	}
	if result == nil {
		return ""
	}
	return strings.TrimSpace(result.Error)
}

func appendSessionActorToolErrorPayload(payload map[string]interface{}, result *agent.Result) {
	if payload == nil || result == nil {
		return
	}
	if result.ToolErrorCount > 0 {
		payload["tool_error_count"] = result.ToolErrorCount
	}
	if result.RecoveredToolErrorCount > 0 {
		payload["recovered_tool_error_count"] = result.RecoveredToolErrorCount
	}
	if result.UnrecoveredToolErrorCount > 0 {
		payload["unrecovered_tool_error_count"] = result.UnrecoveredToolErrorCount
	}
}

func appendStructuredRunErrorPayload(payload map[string]interface{}, err error) {
	if len(payload) == 0 || err == nil {
		return
	}
	preflightErr, ok := agent.AsPromptPreflightError(err)
	if !ok || preflightErr == nil {
		return
	}
	payload["error_type"] = "prompt_preflight"
	for key, value := range preflightErr.Metadata() {
		payload[key] = value
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func chatActorConfigValue(cfg *agent.Config, field string) string {
	if cfg == nil {
		return ""
	}
	switch field {
	case "provider":
		return strings.TrimSpace(cfg.Provider)
	case "model":
		return strings.TrimSpace(cfg.Model)
	default:
		return ""
	}
}
