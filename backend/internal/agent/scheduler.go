package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentresult"
	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	runtimehooks "github.com/wwsheng009/ai-agent-runtime/internal/hooks"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// FilePatch 预留给 writer agent 的 patch 回执。
type FilePatch struct {
	Path               string   `json:"path,omitempty" yaml:"path,omitempty"`
	Diff               string   `json:"diff,omitempty" yaml:"diff,omitempty"`
	Summary            string   `json:"summary,omitempty" yaml:"summary,omitempty"`
	ApplyStatus        string   `json:"apply_status,omitempty" yaml:"apply_status,omitempty"`
	AppliedBy          []string `json:"applied_by,omitempty" yaml:"applied_by,omitempty"`
	ArtifactRefs       []string `json:"artifact_refs,omitempty" yaml:"artifact_refs,omitempty"`
	VerificationStatus string   `json:"verification_status,omitempty" yaml:"verification_status,omitempty"`
	VerifiedBy         []string `json:"verified_by,omitempty" yaml:"verified_by,omitempty"`
}

// SubagentTask 描述一个子代理任务包。
type SubagentTask struct {
	ID                  string      `json:"id,omitempty" yaml:"id,omitempty"`
	Role                string      `json:"role,omitempty" yaml:"role,omitempty"`
	Goal                string      `json:"goal" yaml:"goal"`
	Difficulty          string      `json:"difficulty,omitempty" yaml:"difficulty,omitempty"`
	DifficultyRationale string      `json:"difficulty_rationale,omitempty" yaml:"difficulty_rationale,omitempty"`
	Provider            string      `json:"provider,omitempty" yaml:"provider,omitempty"`
	Model               string      `json:"model,omitempty" yaml:"model,omitempty"`
	ReasoningEffort     string      `json:"reasoning_effort,omitempty" yaml:"reasoning_effort,omitempty"`
	RoutingSource       string      `json:"-" yaml:"-"`
	RouteWarnings       []string    `json:"-" yaml:"-"`
	ToolsWhitelist      []string    `json:"tools_whitelist,omitempty" yaml:"tools_whitelist,omitempty"`
	DependsOn           []string    `json:"depends_on,omitempty" yaml:"depends_on,omitempty"`
	PatchContext        []FilePatch `json:"patches,omitempty" yaml:"patches,omitempty"`
	BudgetTokens        int         `json:"budget_tokens,omitempty" yaml:"budget_tokens,omitempty"`
	TimeoutSec          int         `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	ReadOnly            bool        `json:"read_only,omitempty" yaml:"read_only,omitempty"`
	// ReadOnlySource records an inherited or explicit read-only boundary for
	// diagnostics. It is internal metadata and is not part of the tool payload.
	ReadOnlySource string `json:"-" yaml:"-"`
	// ReadOnlyFilteredTools contains write-like tools removed from the
	// requested whitelist before the child is started. It is internal audit
	// metadata; execution policy remains the final hard boundary.
	ReadOnlyFilteredTools []string `json:"-" yaml:"-"`
	// CompletionRequirement is none|complete_task for child harness loops.
	// Empty inherits none at the child factory (team workers set complete_task via RunMeta).
	CompletionRequirement string `json:"completion_requirement,omitempty" yaml:"completion_requirement,omitempty"`
}

// SubagentResult 是父代理可见的结构化回执。
type SubagentResult struct {
	ID               string `json:"id,omitempty" yaml:"id,omitempty"`
	Role             string `json:"role,omitempty" yaml:"role,omitempty"`
	SessionID        string `json:"session_id,omitempty" yaml:"session_id,omitempty"`
	ParentSessionID  string `json:"parent_session_id,omitempty" yaml:"parent_session_id,omitempty"`
	ParentToolCallID string `json:"parent_tool_call_id,omitempty" yaml:"parent_tool_call_id,omitempty"`
	ReadOnly         bool   `json:"read_only,omitempty" yaml:"read_only,omitempty"`
	// ReadOnlySource records the effective read-only boundary origin (explicit /
	// agentdef / parent_tool_execution_policy) for the parent spawn report and
	// the child prompt banner, so the parent can see WHY writes were stripped.
	ReadOnlySource string `json:"read_only_source,omitempty" yaml:"read_only_source,omitempty"`
	// ReadOnlyFilteredTools lists write-like tools dropped from the requested
	// whitelist because the child ran read-only. It is surfaced to the parent so
	// a silently narrowed allowlist is visible in the spawn report.
	ReadOnlyFilteredTools []string            `json:"read_only_filtered_tools,omitempty" yaml:"read_only_filtered_tools,omitempty"`
	BudgetTokens          int                 `json:"budget_tokens,omitempty" yaml:"budget_tokens,omitempty"`
	Success               bool                `json:"success" yaml:"success"`
	Summary               string              `json:"summary" yaml:"summary"`
	Patches               []FilePatch         `json:"patches,omitempty" yaml:"patches,omitempty"`
	Findings              []string            `json:"findings,omitempty" yaml:"findings,omitempty"`
	Usage                 *types.TokenUsage   `json:"usage,omitempty" yaml:"usage,omitempty"`
	Error                 string              `json:"error,omitempty" yaml:"error,omitempty"`
	Contract              *agentresult.Result `json:"result_contract,omitempty" yaml:"result_contract,omitempty"`
	// 失败分类与重试证据（方案 §6.1）：供父代理选择重派/拆分/本地完成，
	// 并随 subagent.completed 载荷进入分析库（failure_category 等）。
	FailureCategory string `json:"failure_category,omitempty" yaml:"failure_category,omitempty"`
	ErrorCode       string `json:"error_code,omitempty" yaml:"error_code,omitempty"`
	Retryable       bool   `json:"retryable,omitempty" yaml:"retryable,omitempty"`
	Attempt         int    `json:"attempt,omitempty" yaml:"attempt,omitempty"`
	MaxAttempts     int    `json:"max_attempts,omitempty" yaml:"max_attempts,omitempty"`
	RetryReason     string `json:"retry_reason,omitempty" yaml:"retry_reason,omitempty"`
	RetryAdvice     string `json:"retry_advice,omitempty" yaml:"retry_advice,omitempty"`
}

// SubagentSchedulerConfig 控制子代理并发与递归深度。
type SubagentSchedulerConfig struct {
	MaxConcurrent       int  `json:"maxConcurrent" yaml:"maxConcurrent"`
	MaxDepth            int  `json:"maxDepth" yaml:"maxDepth"`
	EnforceSingleWriter bool `json:"enforceSingleWriter" yaml:"enforceSingleWriter"`
	// GlobalLimiter is an optional process-wide admission limiter shared by
	// every SubagentScheduler a host builds. MaxConcurrent bounds one batch;
	// GlobalLimiter bounds the sum of all batches running in the process, which
	// is what removes the historical 4×N overshoot (N background batches each
	// getting their own 4-slot window). nil = no process-wide ceiling, i.e. the
	// per-batch MaxConcurrent remains the only limit.
	//
	// Durable/concurrency semantics: a slot is held only for the duration of
	// one child execution and is released by the goroutine that acquired it
	// (defer, so an error or a cancelled context cannot leak it). A depth-N
	// delegation chain does not acquire a second slot for its subtree — the
	// descendant wave detects the slot already held by its ancestor through the
	// context (see withSubagentGlobalSlot) and runs under it, so a saturated
	// limiter cannot deadlock against itself.
	GlobalLimiter *SubagentConcurrencyLimiter `json:"-" yaml:"-"`
	// MaxQueueDepth is the opt-in backpressure knob for one reader wave: with a
	// positive value a wave carrying more than MaxConcurrent+MaxQueueDepth
	// reader tasks is rejected before any child starts, naming both limits and
	// the agents.maxConcurrentQueueDepth knob. 0 (default) preserves the
	// historical behavior — unbounded waiting, no queue depth limit.
	MaxQueueDepth int `json:"maxQueueDepth,omitempty" yaml:"maxQueueDepth,omitempty"`
	// QueueTimeout is the opt-in bound on how long one task may wait for a
	// per-batch concurrency slot. 0 (default) waits indefinitely, exactly like
	// before this knob existed; a positive value fails the batch with an
	// actionable timeout error instead of queueing forever.
	QueueTimeout time.Duration `json:"queueTimeout,omitempty" yaml:"queueTimeout,omitempty"`
	// DelegationPolicy controls whether an agent created by this scheduler may
	// create another execution node. The empty value is the root/default
	// policy: the current agent may delegate, but its children are restricted
	// unless the caller explicitly sets "enabled".
	DelegationPolicy string `json:"delegationPolicy,omitempty" yaml:"delegationPolicy,omitempty"`
	// MaxConsecutiveFailures opens a circuit after this many consecutive
	// unsuccessful batches. Zero uses the conservative runtime default.
	MaxConsecutiveFailures int                                     `json:"maxConsecutiveFailures" yaml:"maxConsecutiveFailures"`
	Routing                *agentconfig.AICLISubagentRoutingConfig `json:"-" yaml:"-"`
	// MaxAttemptsPerTask 是**只读任务瞬时失败**的任务级有界重试上限
	//（方案 §6.2）。0 使用默认值 2；1 表示关闭自动重试。
	// 写任务永不在任务级自动重试（避免重复副作用），只产出 §6.1 的建议。
	MaxAttemptsPerTask int `json:"maxAttemptsPerTask,omitempty" yaml:"maxAttemptsPerTask,omitempty"`
}

// 子代理任务级重试默认参数（§6.2：指数退避 + jitter，封顶 5s）。
const (
	defaultSubagentMaxAttemptsPerTask = 2
	defaultSubagentRetryBaseDelay     = 200 * time.Millisecond
	defaultSubagentRetryMaxDelay      = 5 * time.Second
)

// SubagentRunOptions 描述一次 parent -> child 协同批次的上下文。
type SubagentRunOptions struct {
	TraceID          string
	ParentSessionID  string
	ParentToolCallID string
	Depth            int
	// OnTaskEvent, when set, is invoked serially as child tasks transition
	// between running and finished so a durable coordinator can mirror
	// progress without racing concurrent store writes from wave goroutines.
	// event is "started" | "completed".
	OnTaskEvent func(taskID, event string)
	// OnTaskBound, when set, is invoked once per task as soon as the child
	// session id exists — i.e. before the child actually runs. It exists so a
	// durable coordinator can write child_session_id back at runtime instead of
	// only at terminal settle, which is what makes a running task resolvable by
	// task_id (wait_agent / read_agent_events) while it is still working.
	OnTaskBound func(taskID, childSessionID string)
}

// notifyTaskEvent calls OnTaskEvent when configured. It is best-effort: the
// scheduler must not fail the run because a metadata mirror failed.
func (o SubagentRunOptions) notifyTaskEvent(taskID, event string) {
	if o.OnTaskEvent != nil {
		o.OnTaskEvent(taskID, event)
	}
}

// notifyTaskBound calls OnTaskBound when configured. Same best-effort contract
// as notifyTaskEvent: a metadata mirror must never fail the run.
func (o SubagentRunOptions) notifyTaskBound(taskID, childSessionID string) {
	if o.OnTaskBound != nil {
		o.OnTaskBound(taskID, childSessionID)
	}
}

// SubagentScheduler 在 Go 侧调度 fresh child agents。
type SubagentScheduler struct {
	parent                *Agent
	config                SubagentSchedulerConfig
	expertSem             chan struct{}
	delegationAllowed     bool
	nestedDelegationOptIn bool
	failureMu             sync.Mutex
	consecutiveFailures   int
	circuitOpen           bool
}

// NewSubagentScheduler 创建一个最小子代理调度器。
func NewSubagentScheduler(parent *Agent, config SubagentSchedulerConfig) *SubagentScheduler {
	if config.MaxConcurrent <= 0 {
		config.MaxConcurrent = 4
	}
	if config.MaxDepth <= 0 {
		config.MaxDepth = 1
	}
	if !config.EnforceSingleWriter {
		config.EnforceSingleWriter = true
	}
	if config.MaxConsecutiveFailures <= 0 {
		config.MaxConsecutiveFailures = 2
	}
	delegationAllowed, nestedOptIn := resolveDelegationPolicy(config.DelegationPolicy)
	scheduler := &SubagentScheduler{
		parent:                parent,
		config:                config,
		delegationAllowed:     delegationAllowed,
		nestedDelegationOptIn: nestedOptIn,
	}
	if limit := subagentExpertConcurrencyLimit(config.Routing); limit > 0 {
		scheduler.expertSem = make(chan struct{}, limit)
	}
	return scheduler
}

// SubagentConcurrencyLimiter is the process-wide admission limiter for
// subagent executions (plan P1-4/H12). One limiter is shared by every
// SubagentScheduler a host builds, so N background batches can no longer each
// run their own MaxConcurrent window: the per-batch semaphore bounds one batch,
// this limiter bounds the sum.
//
// Concurrency semantics:
//   - A slot is acquired before a child execution starts and released when it
//     finishes, errors or is cancelled (callers must use defer).
//   - Acquire is context-aware: a cancelled caller fails fast instead of
//     occupying the queue forever.
//   - A nil *SubagentConcurrencyLimiter is valid and means "unlimited"; all
//     methods are nil-safe so an unset GlobalLimiter reproduces the historical
//     behavior exactly.
//   - The limiter is not durable state: it lives for the process, and nothing
//     about it is persisted. Durable lifecycle state stays in the agent
//     registry/batch store.
type SubagentConcurrencyLimiter struct {
	slots chan struct{}
}

// NewSubagentConcurrencyLimiter returns a limiter with the given ceiling, or
// nil (unlimited) when limit <= 0. Hosts size it from agents.maxThreads when
// that is positive; the documented "explicitly unlimited" value (-1) and the
// unset value both map to nil here.
func NewSubagentConcurrencyLimiter(limit int) *SubagentConcurrencyLimiter {
	if limit <= 0 {
		return nil
	}
	return &SubagentConcurrencyLimiter{slots: make(chan struct{}, limit)}
}

// Limit reports the configured ceiling; 0 means unlimited (nil receiver).
func (l *SubagentConcurrencyLimiter) Limit() int {
	if l == nil {
		return 0
	}
	return cap(l.slots)
}

// Acquire blocks until a slot is free or ctx is done. It returns nil when a
// slot was taken (the caller must Release exactly once, normally via defer) and
// ctx.Err() when the caller gave up; no slot is held in the error case.
func (l *SubagentConcurrencyLimiter) Acquire(ctx context.Context) error {
	if l == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case l.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Release returns one slot. It is nil-safe and idempotent-guarded: an
// unbalanced release (no outstanding acquire) is ignored rather than blocking
// the goroutine, which keeps a defer-based release panic-safe.
func (l *SubagentConcurrencyLimiter) Release() {
	if l == nil {
		return
	}
	select {
	case <-l.slots:
	default:
	}
}

// subagentGlobalSlotKey marks a context whose execution already holds a slot in
// the named limiter. It is how a depth-N delegation chain stays inside the slot
// its ancestor acquired instead of deadlocking against a saturated limiter.
type subagentGlobalSlotKey struct{}

func withSubagentGlobalSlot(ctx context.Context, limiter *SubagentConcurrencyLimiter) context.Context {
	if limiter == nil {
		return ctx
	}
	return context.WithValue(ctx, subagentGlobalSlotKey{}, limiter)
}

func subagentGlobalSlotHeld(ctx context.Context, limiter *SubagentConcurrencyLimiter) bool {
	if ctx == nil || limiter == nil {
		return false
	}
	held, _ := ctx.Value(subagentGlobalSlotKey{}).(*SubagentConcurrencyLimiter)
	return held == limiter
}

// acquireGlobalSlot admits one child execution into the process-wide limiter.
// acquired=false means nothing was taken, either because no limiter is
// configured or because this execution already runs inside an ancestor's slot.
func (s *SubagentScheduler) acquireGlobalSlot(ctx context.Context) (bool, error) {
	limiter := s.config.GlobalLimiter
	if limiter == nil || subagentGlobalSlotHeld(ctx, limiter) {
		return false, nil
	}
	if err := limiter.Acquire(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// releaseGlobalSlot returns the slot only when this execution acquired it.
func (s *SubagentScheduler) releaseGlobalSlot(acquired bool) {
	if !acquired {
		return
	}
	s.config.GlobalLimiter.Release()
}

const (
	DelegationPolicyEnabled  = "enabled"
	DelegationPolicyDisabled = "disabled"
)

func resolveDelegationPolicy(policy string) (allowed bool, nestedOptIn bool) {
	switch strings.ToLower(strings.TrimSpace(policy)) {
	case DelegationPolicyDisabled, "deny", "none":
		return false, false
	case DelegationPolicyEnabled, "allow":
		return true, true
	default:
		// A scheduler explicitly attached to the root agent is useful by
		// default. Child factories use DelegationPolicyDisabled unless the
		// root explicitly opted into nested delegation.
		return true, false
	}
}

// AllowsDelegation reports whether this scheduler can launch children.
func (s *SubagentScheduler) AllowsDelegation() bool {
	return s != nil && s.delegationAllowed
}

// NestedDelegationOptIn reports whether the parent explicitly opted into
// propagating delegation capability to descendants.
func (s *SubagentScheduler) NestedDelegationOptIn() bool {
	return s != nil && s.delegationAllowed && s.nestedDelegationOptIn
}

func subagentExpertConcurrencyLimit(cfg *agentconfig.AICLISubagentRoutingConfig) int {
	if cfg == nil || cfg.Enabled == nil || !*cfg.Enabled || cfg.MaxExpertConcurrency <= 0 {
		return 0
	}
	return cfg.MaxExpertConcurrency
}

func (s *SubagentScheduler) acquireExpertSlot(ctx context.Context, difficulty string) (func(), error) {
	if s == nil || s.expertSem == nil || !strings.EqualFold(strings.TrimSpace(difficulty), "expert") {
		return nil, nil
	}
	select {
	case s.expertSem <- struct{}{}:
		return func() { <-s.expertSem }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// RunChildren 执行一批子代理任务，并只返回结构化摘要。
func (s *SubagentScheduler) RunChildren(ctx context.Context, options SubagentRunOptions, tasks []SubagentTask) (results []SubagentResult, runErr error) {
	if s == nil {
		return nil, fmt.Errorf("subagent scheduler is nil")
	}
	if !s.delegationAllowed {
		err := runtimeerrors.Newf(
			runtimeerrors.ErrAgentNestedDelegation,
			"nested subagent delegation is disabled before child creation: set delegationPolicy=enabled on the root/coordinator or complete the work locally; retrying the same child spawn will not help",
		)
		s.emitSubagentDenied(options, "", "nested_delegation", err.Error(), map[string]interface{}{
			"delegation_policy": DelegationPolicyDisabled,
			"depth":             options.Depth,
		})
		return nil, err
	}
	// 动态扩展 depth：expert 难度任务允许额外 1 层深度
	maxDepth := s.config.MaxDepth
	for _, task := range tasks {
		if strings.EqualFold(strings.TrimSpace(task.Difficulty), "expert") ||
			strings.EqualFold(strings.TrimSpace(task.Difficulty), "hard") {
			maxDepth = s.config.MaxDepth + 1
			break
		}
	}
	if options.Depth > maxDepth {
		err := runtimeerrors.Newf(
			runtimeerrors.ErrAgentSpawnDepthLimit,
			"subagent spawn depth limit reached before child creation: requested_depth=%d effective_max_depth=%d (base=%d, expert+1 extension); next_action=complete_locally_or_use_spawn_team — continue the work in the current agent instead of retrying the same spawn",
			options.Depth, maxDepth, s.config.MaxDepth,
		)
		s.emitSubagentDenied(options, "", "max_depth", err.Error(), map[string]interface{}{
			"depth":      options.Depth,
			"max_depth":  maxDepth,
			"base_depth": s.config.MaxDepth,
		})
		return nil, err
	}
	if len(tasks) == 0 {
		return nil, nil
	}
	if err := s.checkFailureCircuit(options); err != nil {
		return nil, err
	}

	preparedTasks, err := s.prepareTasks(tasks)
	if err != nil {
		s.emitSubagentDenied(options, "", classifySubagentDeniedPolicy(err.Error()), err.Error(), map[string]interface{}{
			"task_count": len(tasks),
		})
		return nil, err
	}

	readers, writers, err := s.partitionTasks(preparedTasks)
	if err != nil {
		s.emitSubagentDenied(options, "", classifySubagentDeniedPolicy(err.Error()), err.Error(), map[string]interface{}{
			"task_count": len(tasks),
		})
		return nil, err
	}
	defer func() {
		s.recordBatchOutcome(options, results, runErr)
	}()

	results = make([]SubagentResult, len(tasks))
	done := make([]bool, len(tasks))
	completedByID := make(map[string]SubagentResult, len(tasks))
	remaining := len(tasks)

	for remaining > 0 {
		readyReaders, readyWriters, depErr := s.readyTasks(readers, writers, done, completedByID)
		if depErr != nil {
			s.emitSubagentDenied(options, "", "dependency", depErr.Error(), map[string]interface{}{
				"task_count": len(tasks),
			})
			return results, depErr
		}
		if len(readyReaders) == 0 && len(readyWriters) == 0 {
			err := fmt.Errorf("subagent dependency deadlock detected")
			s.emitSubagentDenied(options, "", "dependency", err.Error(), map[string]interface{}{
				"task_count": len(tasks),
			})
			return results, err
		}

		if err := s.runReaderWave(ctx, options, readyReaders, results, done, completedByID); err != nil {
			return results, err
		}
		remaining -= len(readyReaders)

		for _, item := range readyWriters {
			task := s.enrichTaskFromDependencies(item.task, completedByID)
			options.notifyTaskEvent(task.ID, "started")
			result, err := s.runChild(ctx, options, task)
			options.notifyTaskEvent(task.ID, "completed")
			results[item.index] = result
			done[item.index] = true
			completedByID[task.ID] = result
			remaining--
			if err != nil {
				return results, err
			}
		}
	}

	s.finalizeWriterPatches(options, preparedTasks, results)
	return results, nil
}

func (s *SubagentScheduler) runChild(ctx context.Context, options SubagentRunOptions, task SubagentTask) (SubagentResult, error) {
	report, err := s.runChildUncontracted(ctx, options, task)
	ensureSubagentResultContract(&report, task)
	return report, err
}

func (s *SubagentScheduler) runChildUncontracted(ctx context.Context, options SubagentRunOptions, task SubagentTask) (SubagentResult, error) {
	if s.parent == nil {
		return SubagentResult{}, fmt.Errorf("parent agent is nil")
	}
	if task.Goal == "" {
		s.emitSubagentDenied(options, task.ID, "validation", "subagent goal is required", map[string]interface{}{
			"subagent_id": task.ID,
		})
		return SubagentResult{
			ID:                    task.ID,
			Role:                  task.Role,
			ParentSessionID:       options.ParentSessionID,
			ParentToolCallID:      options.ParentToolCallID,
			ReadOnly:              task.ReadOnly,
			ReadOnlySource:        subagentReadOnlySource(task),
			ReadOnlyFilteredTools: subagentReadOnlyFilteredTools(task),
			BudgetTokens:          task.BudgetTokens,
			Success:               false,
			Error:                 "subagent goal is required",
		}, nil
	}

	factory := ChildAgentFactory{}
	spec, err := factory.Build(ctx, ChildBuildRequest{
		Parent:  s.parent,
		Task:    task,
		Options: options,
		Config:  s.config,
	})
	if err != nil {
		s.emitSubagentDenied(options, task.ID, "routing", err.Error(), map[string]interface{}{
			"subagent_id": task.ID,
		})
		return SubagentResult{
			ID:                    task.ID,
			Role:                  task.Role,
			ParentSessionID:       options.ParentSessionID,
			ParentToolCallID:      options.ParentToolCallID,
			ReadOnly:              task.ReadOnly,
			ReadOnlySource:        subagentReadOnlySource(task),
			ReadOnlyFilteredTools: subagentReadOnlyFilteredTools(task),
			BudgetTokens:          task.BudgetTokens,
			Success:               false,
			Error:                 err.Error(),
			Summary:               err.Error(),
		}, nil
	}
	releaseExpertSlot, err := s.acquireExpertSlot(ctx, spec.Decision.Difficulty)
	if err != nil {
		s.emitSubagentDenied(options, task.ID, "expert_concurrency", err.Error(), map[string]interface{}{
			"subagent_id": task.ID,
			"difficulty":  spec.Decision.Difficulty,
		})
		return SubagentResult{
			ID:                    task.ID,
			Role:                  task.Role,
			ParentSessionID:       options.ParentSessionID,
			ParentToolCallID:      options.ParentToolCallID,
			ReadOnly:              task.ReadOnly,
			ReadOnlySource:        subagentReadOnlySource(task),
			ReadOnlyFilteredTools: subagentReadOnlyFilteredTools(task),
			BudgetTokens:          task.BudgetTokens,
			Success:               false,
			Error:                 err.Error(),
			Summary:               err.Error(),
		}, nil
	}
	if releaseExpertSlot != nil {
		defer releaseExpertSlot()
	}
	childCtx := ctx
	var cancel context.CancelFunc
	if spec.Decision.Timeout > 0 {
		childCtx, cancel = context.WithTimeout(ctx, spec.Decision.Timeout)
		defer cancel()
	}
	task = spec.Task
	childConfig := spec.Config
	childAgent := spec.Agent
	defer func() {
		_ = childAgent.Close()
	}()
	childSessionID := spec.SessionID
	// Runtime binding (P1-1 / H7): the child session id is known here, before
	// the child loop runs. Publish it now so a durable coordinator can write
	// child_session_id back while the task is still running instead of only at
	// terminal settle.
	options.notifyTaskBound(task.ID, childSessionID)
	if hookMgr := s.parent.GetHookManager(); hookMgr != nil {
		payload := mergeRouteAuditPayload(map[string]interface{}{
			"subagent_id":         task.ID,
			"role":                task.Role,
			"goal":                task.Goal,
			"depth":               options.Depth,
			"read_only":           task.ReadOnly,
			"budget_tokens":       task.BudgetTokens,
			"parent_session_id":   options.ParentSessionID,
			"parent_tool_call_id": options.ParentToolCallID,
			"child_session_id":    childSessionID,
			"child_agent_name":    childConfig.Name,
			"trace_id":            options.TraceID,
		}, spec.Decision)
		hookMgr.DispatchAsync(context.Background(), runtimehooks.EventSubagentStart, payload)
	}
	s.parent.emitRuntimeEvent("subagent.started", childSessionID, "", mergeRouteAuditPayload(map[string]interface{}{
		"subagent_id":         task.ID,
		"role":                task.Role,
		"goal":                task.Goal,
		"depth":               options.Depth,
		"read_only":           task.ReadOnly,
		"budget_tokens":       task.BudgetTokens,
		"parent_session_id":   options.ParentSessionID,
		"parent_tool_call_id": options.ParentToolCallID,
		"child_agent_name":    childConfig.Name,
		"trace_id":            options.TraceID,
	}, spec.Decision))

	loop := NewReActLoop(childAgent, spec.Runtime, spec.LoopConfig)

	// §6.2：只读任务的瞬时失败做有界重试（写任务永不自动重试）。
	// 熔断语义保持：一次自动重试链条只以最终结果计入批次连续失败数
	//（recordBatchOutcome 看到的仍是每任务一条结果），中间尝试因此不会
	// 额外推高 consecutiveFailures。
	maxAttempts := s.maxAttemptsPerTask()
	attempt := 0
	retryReason := ""
	var (
		result      *Result
		runErr      error
		disposition SubagentFailureDisposition
	)
	for attempt = 1; attempt <= maxAttempts; attempt++ {
		result, runErr = loop.run(childCtx, task.Goal, loopRunOptions{
			TraceID:       options.TraceID,
			SessionID:     childSessionID,
			IncludePrompt: true,
			Depth:         options.Depth,
			BudgetTokens:  task.BudgetTokens,
			ToolWhitelist: task.ToolsWhitelist,
		})
		if runErr == nil {
			break
		}
		disposition = ClassifySubagentFailure(runErr)
		if !shouldAutoRetrySubagentTask(task, disposition, attempt, maxAttempts, childCtx.Err()) {
			break
		}
		retryReason = disposition.Category
		// 中间尝试也留完成事件（attempt 可见），标记 intermediate_attempt：
		// 分析侧按 conflict_count 统计而不把中间失败当最终结果。
		s.emitSubagentAttemptEvent(childSessionID, options, task, childConfig.Name, spec, attempt, maxAttempts, disposition, runErr)
		if !sleepWithContext(childCtx, subagentRetryBackoff(attempt, defaultSubagentRetryBaseDelay, defaultSubagentRetryMaxDelay)) {
			break
		}
	}
	if attempt > maxAttempts {
		attempt = maxAttempts
	}
	if runErr != nil {
		retryAdvice := SubagentRetryAdvice(disposition.Category, task.ReadOnly)
		// 写任务只给建议，不自动重派（§6.2）。
		report := SubagentResult{
			ID:                    task.ID,
			Role:                  task.Role,
			SessionID:             childSessionID,
			ParentSessionID:       options.ParentSessionID,
			ParentToolCallID:      options.ParentToolCallID,
			ReadOnly:              task.ReadOnly,
			ReadOnlySource:        subagentReadOnlySource(task),
			ReadOnlyFilteredTools: subagentReadOnlyFilteredTools(task),
			BudgetTokens:          task.BudgetTokens,
			Success:               false,
			Error:                 runErr.Error(),
			Summary:               runErr.Error(),
			FailureCategory:       disposition.Category,
			ErrorCode:             disposition.ErrorCode,
			Retryable:             disposition.Retryable,
			Attempt:               attempt,
			MaxAttempts:           maxAttempts,
			RetryReason:           retryReason,
			RetryAdvice:           retryAdvice,
		}
		s.parent.emitRuntimeEvent("subagent.completed", childSessionID, "", mergeRouteAuditPayload(map[string]interface{}{
			"subagent_id":         task.ID,
			"role":                task.Role,
			"read_only":           task.ReadOnly,
			"success":             false,
			"status":              "failed",
			"completion_reason":   subagentCompletionReason(false, disposition),
			"failure_category":    disposition.Category,
			"error_code":          disposition.ErrorCode,
			"retryable":           disposition.Retryable,
			"attempt":             attempt,
			"max_attempts":        maxAttempts,
			"retry_reason":        retryReason,
			"retry_advice":        retryAdvice,
			"source":              "scheduler",
			"error":               runErr.Error(),
			"budget_tokens":       task.BudgetTokens,
			"parent_session_id":   options.ParentSessionID,
			"parent_tool_call_id": options.ParentToolCallID,
			"child_agent_name":    childConfig.Name,
			"trace_id":            options.TraceID,
		}, spec.Decision))
		if hookMgr := s.parent.GetHookManager(); hookMgr != nil {
			payload := mergeRouteAuditPayload(map[string]interface{}{
				"subagent_id":         task.ID,
				"role":                task.Role,
				"read_only":           task.ReadOnly,
				"success":             false,
				"status":              "failed",
				"completion_reason":   subagentCompletionReason(false, disposition),
				"failure_category":    disposition.Category,
				"error_code":          disposition.ErrorCode,
				"attempt":             attempt,
				"max_attempts":        maxAttempts,
				"retry_reason":        retryReason,
				"retry_advice":        retryAdvice,
				"error":               runErr.Error(),
				"budget_tokens":       task.BudgetTokens,
				"parent_session_id":   options.ParentSessionID,
				"parent_tool_call_id": options.ParentToolCallID,
				"child_session_id":    childSessionID,
				"child_agent_name":    childConfig.Name,
				"trace_id":            options.TraceID,
			}, spec.Decision)
			hookMgr.DispatchAsync(context.Background(), runtimehooks.EventSubagentStop, payload)
		}
		return report, nil
	}

	report := SubagentResult{
		ID:                    task.ID,
		Role:                  task.Role,
		SessionID:             childSessionID,
		ParentSessionID:       options.ParentSessionID,
		ParentToolCallID:      options.ParentToolCallID,
		ReadOnly:              task.ReadOnly,
		ReadOnlySource:        subagentReadOnlySource(task),
		ReadOnlyFilteredTools: subagentReadOnlyFilteredTools(task),
		BudgetTokens:          task.BudgetTokens,
		Success:               result.Success,
		Summary:               result.Output,
		Usage:                 result.Usage,
		Contract:              result.Contract.Clone(),
		Attempt:               attempt,
		MaxAttempts:           maxAttempts,
		RetryReason:           retryReason,
	}
	if task.ReadOnly {
		report.Findings = collectFindings(result.Observations)
	} else {
		report.Patches = collectPatches(result.Observations)
	}
	if result.Error != "" {
		report.Error = result.Error
	}
	// §6.3（部分实施）：预算/上下文耗尽时保留已收集的 Findings/Patches
	//（上面已按只读/写任务分别收集），并把 completion_reason 标注为
	// budget_exceeded / context_overflow，供分析侧把"有部分产出"的失败单列；
	// 不做自动拆分/重派（避免无界递归），只给父代理 split_task 建议。
	completionReason := "completed"
	if result.LimitReached {
		completionReason = "budget_exceeded"
	}
	if !result.Success {
		disposition := ClassifySubagentFailure(errors.New(firstNonEmptyString(report.Error, "subagent run finished unsuccessfully")))
		if disposition.Category == llm.FailureCategoryUnknown && result.LimitReached {
			disposition = SubagentFailureDisposition{Category: llm.FailureCategoryBudgetExceeded, Retryable: false}
		}
		report.FailureCategory = disposition.Category
		report.ErrorCode = disposition.ErrorCode
		report.Retryable = disposition.Retryable
		report.RetryAdvice = SubagentRetryAdvice(disposition.Category, task.ReadOnly)
		completionReason = subagentCompletionReason(false, disposition)
	}
	s.parent.emitRuntimeEvent("subagent.completed", childSessionID, "", mergeRouteAuditPayload(map[string]interface{}{
		"subagent_id":         task.ID,
		"role":                task.Role,
		"read_only":           task.ReadOnly,
		"success":             report.Success,
		"status":              statusAliasForCompletionReason(completionReason, report.Success),
		"completion_reason":   completionReason,
		"failure_category":    report.FailureCategory,
		"error_code":          report.ErrorCode,
		"retryable":           report.Retryable,
		"retry_advice":        report.RetryAdvice,
		"attempt":             attempt,
		"max_attempts":        maxAttempts,
		"retry_reason":        retryReason,
		"source":              "scheduler",
		"error":               report.Error,
		"budget_tokens":       task.BudgetTokens,
		"parent_session_id":   options.ParentSessionID,
		"parent_tool_call_id": options.ParentToolCallID,
		"child_agent_name":    childConfig.Name,
		"usage_total_tokens":  usageTotal(report.Usage),
		"trace_id":            options.TraceID,
	}, spec.Decision))
	if hookMgr := s.parent.GetHookManager(); hookMgr != nil {
		payload := mergeRouteAuditPayload(map[string]interface{}{
			"subagent_id":         task.ID,
			"role":                task.Role,
			"read_only":           task.ReadOnly,
			"success":             report.Success,
			"status":              statusAliasForCompletionReason(completionReason, report.Success),
			"completion_reason":   completionReason,
			"failure_category":    report.FailureCategory,
			"error_code":          report.ErrorCode,
			"attempt":             attempt,
			"max_attempts":        maxAttempts,
			"retry_reason":        retryReason,
			"retry_advice":        report.RetryAdvice,
			"error":               report.Error,
			"budget_tokens":       task.BudgetTokens,
			"parent_session_id":   options.ParentSessionID,
			"parent_tool_call_id": options.ParentToolCallID,
			"child_session_id":    childSessionID,
			"child_agent_name":    childConfig.Name,
			"usage_total_tokens":  usageTotal(report.Usage),
			"trace_id":            options.TraceID,
		}, spec.Decision)
		hookMgr.DispatchAsync(context.Background(), runtimehooks.EventSubagentStop, payload)
	}
	return report, nil
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func collectFindings(observations []types.Observation) []string {
	findings := make([]string, 0, len(observations))
	for _, observation := range observations {
		if output, ok := observation.Output.(string); ok && output != "" {
			findings = append(findings, output)
		} else if observation.Error != "" {
			findings = append(findings, observation.Error)
		}
		if len(findings) >= 4 {
			break
		}
	}
	return findings
}

func collectPatches(observations []types.Observation) []FilePatch {
	patches := make([]FilePatch, 0, len(observations))
	for _, observation := range observations {
		if !observation.Success || !isWriteLikeToolName(observation.Tool) {
			continue
		}

		toolMetadata := metricMapValue(observation, "tool_metadata")
		path := firstNonEmptyString(
			stringMapValue(toolMetadata, "file_path"),
			stringMapValue(toolMetadata, "path"),
			pathFromToolInput(observation.Input),
		)
		summary := patchSummary(observation.Tool, toolMetadata, observation.Output)
		diff := firstNonEmptyString(
			stringMapValue(toolMetadata, "diff"),
			stringMapValue(toolMetadata, "patch"),
		)
		if diff == "" {
			if outputText, ok := observation.Output.(string); ok {
				diff = extractUnifiedDiff(outputText)
			}
		}
		if path == "" && diff != "" {
			path = pathFromDiff(diff)
		}
		if summary == "" && diff != "" {
			summary = "captured unified diff from tool output"
		}
		if path == "" && summary == "" && diff == "" {
			continue
		}
		artifactRefs := metricStringSliceValue(observation, "artifact_refs")

		patches = append(patches, FilePatch{
			Path:               path,
			Diff:               diff,
			Summary:            summary,
			ApplyStatus:        derivePatchApplyStatus(observation.Tool, toolMetadata, observation.Output),
			ArtifactRefs:       artifactRefs,
			VerificationStatus: "unverified",
		})
	}
	return patches
}

func (s *SubagentScheduler) finalizeWriterPatches(options SubagentRunOptions, tasks []SubagentTask, results []SubagentResult) {
	if len(tasks) == 0 || len(results) == 0 {
		return
	}

	taskIndex := make(map[string]int, len(tasks))
	writerIDs := make([]string, 0, 1)
	for index, task := range tasks {
		taskIndex[task.ID] = index
		if !task.ReadOnly {
			writerIDs = append(writerIDs, task.ID)
		}
	}
	if len(writerIDs) == 0 {
		return
	}

	verifiersByWriter := make(map[string][]string, len(writerIDs))
	for _, task := range tasks {
		if !task.ReadOnly || len(task.DependsOn) == 0 {
			continue
		}
		for _, dep := range task.DependsOn {
			dep = strings.TrimSpace(dep)
			if dep == "" {
				continue
			}
			if _, ok := taskIndex[dep]; !ok {
				continue
			}
			verifiersByWriter[dep] = append(verifiersByWriter[dep], task.ID)
		}
	}

	for _, writerID := range writerIDs {
		writerIndex, ok := taskIndex[writerID]
		if !ok || writerIndex < 0 || writerIndex >= len(results) {
			continue
		}
		writer := results[writerIndex]
		if len(writer.Patches) == 0 {
			continue
		}

		verifierIDs := verifiersByWriter[writerID]
		if len(verifierIDs) == 0 {
			for i := range writer.Patches {
				ensurePatchAppliedBy(&writer.Patches[i], writerID)
			}
			results[writerIndex] = writer
			s.emitPatchAppliedEvents(options, writerID, writer.Patches)
			continue
		}

		verifiedBy := make([]string, 0, len(verifierIDs))
		allVerified := true
		for _, verifierID := range verifierIDs {
			verifierIndex, exists := taskIndex[verifierID]
			if !exists || verifierIndex < 0 || verifierIndex >= len(results) {
				allVerified = false
				continue
			}
			verifier := results[verifierIndex]
			if verifier.Success && strings.TrimSpace(verifier.Error) == "" {
				verifiedBy = append(verifiedBy, verifierID)
				continue
			}
			allVerified = false
		}

		status := "needs_review"
		if allVerified && len(verifiedBy) > 0 && allPatchesApplied(writer.Patches) {
			status = "verified"
		}
		for i := range writer.Patches {
			ensurePatchAppliedBy(&writer.Patches[i], writerID)
			writer.Patches[i].VerificationStatus = status
			if len(verifiedBy) > 0 {
				writer.Patches[i].VerifiedBy = append([]string(nil), verifiedBy...)
			} else {
				writer.Patches[i].VerifiedBy = nil
			}
		}
		results[writerIndex] = writer
		s.emitPatchAppliedEvents(options, writerID, writer.Patches)
	}
}

func ensurePatchAppliedBy(patch *FilePatch, writerID string) {
	if patch == nil {
		return
	}
	if strings.TrimSpace(patch.ApplyStatus) == "" {
		patch.ApplyStatus = "applied"
	}
	if strings.TrimSpace(writerID) == "" {
		return
	}
	for _, existing := range patch.AppliedBy {
		if existing == writerID {
			return
		}
	}
	patch.AppliedBy = append(patch.AppliedBy, writerID)
}

func allPatchesApplied(patches []FilePatch) bool {
	if len(patches) == 0 {
		return false
	}
	for _, patch := range patches {
		if !patchIsApplied(patch) {
			return false
		}
	}
	return true
}

func patchIsApplied(patch FilePatch) bool {
	status := strings.TrimSpace(patch.ApplyStatus)
	return status == "" || status == "applied"
}

func (s *SubagentScheduler) emitPatchAppliedEvents(options SubagentRunOptions, writerID string, patches []FilePatch) {
	if s == nil || s.parent == nil || len(patches) == 0 {
		return
	}
	for _, patch := range patches {
		payload := map[string]interface{}{
			"trace_id":            options.TraceID,
			"subagent_id":         writerID,
			"parent_session_id":   options.ParentSessionID,
			"parent_tool_call_id": options.ParentToolCallID,
			"path":                patch.Path,
			"summary":             patch.Summary,
			"apply_status":        patch.ApplyStatus,
			"applied_by":          append([]string(nil), patch.AppliedBy...),
			"verification_status": patch.VerificationStatus,
			"verified_by":         append([]string(nil), patch.VerifiedBy...),
			"artifact_refs":       append([]string(nil), patch.ArtifactRefs...),
			"artifact_ref_count":  len(patch.ArtifactRefs),
			"diff_present":        strings.TrimSpace(patch.Diff) != "",
		}
		s.parent.emitRuntimeEvent("patch.applied", options.ParentSessionID, "spawn_subagents", payload)
	}
}

func buildSubagentSessionID(taskID string) string {
	base := firstNonEmptyString(taskID, "subagent")
	return fmt.Sprintf("subagent_%s_%s", base, strings.ReplaceAll(uuid.NewString(), "-", ""))
}

func (s *SubagentScheduler) runReaderWave(ctx context.Context, options SubagentRunOptions, readers []indexedSubagentTask, results []SubagentResult, done []bool, completedByID map[string]SubagentResult) error {
	if len(readers) == 0 {
		return nil
	}
	// P1-4/H12 背压（可选，默认关闭）：队列深度上限。MaxQueueDepth=0 时不设限，
	// 与历史行为完全一致（无限等待）。
	if err := s.checkQueueDepth(len(readers)); err != nil {
		return err
	}

	sem := make(chan struct{}, s.config.MaxConcurrent)
	errs := make([]error, len(readers))
	waveResults := make([]SubagentResult, len(readers))
	var wg sync.WaitGroup

	for i, item := range readers {
		options.notifyTaskEvent(item.task.ID, "started")
		index := i
		task := s.enrichTaskFromDependencies(item.task, completedByID)
		wg.Add(1)
		go func(task indexedSubagentTask, prepared SubagentTask) {
			defer wg.Done()
			// 每批窗口（MaxConcurrent）：ctx 取消 / 可选队列超时都会快速失败，
			// 不会留下永久阻塞的 goroutine。
			if err := s.acquireBatchSlot(ctx, sem); err != nil {
				errs[index] = err
				return
			}
			defer func() { <-sem }()

			// P1-4/H12：进程级上限。GlobalLimiter 为 nil 时行为与历史一致
			//（仅受每批 sem 限制）；acquired=false 表示本次执行已运行在祖先
			// 持有的槽位内（depth-N 递归），不再重复占槽，避免自锁。
			acquired, err := s.acquireGlobalSlot(ctx)
			if err != nil {
				errs[index] = err
				return
			}
			defer s.releaseGlobalSlot(acquired)
			childCtx := ctx
			if acquired {
				childCtx = withSubagentGlobalSlot(ctx, s.config.GlobalLimiter)
			}

			result, err := s.runChild(childCtx, options, prepared)
			waveResults[index] = result
			errs[index] = err
		}(item, task)
	}

	wg.Wait()
	for i, item := range readers {
		options.notifyTaskEvent(item.task.ID, "completed")
		results[item.index] = waveResults[i]
		done[item.index] = true
		completedByID[item.task.ID] = waveResults[i]
		if errs[i] != nil {
			return errs[i]
		}
	}
	return nil
}

// acquireBatchSlot admits one task into the per-batch concurrency window.
// Without QueueTimeout it waits until a slot frees or the caller is cancelled
// (historical behavior); with QueueTimeout it fails the task with an actionable
// error instead of queueing forever.
func (s *SubagentScheduler) acquireBatchSlot(ctx context.Context, sem chan struct{}) error {
	if s.config.QueueTimeout <= 0 {
		select {
		case sem <- struct{}{}:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	timer := time.NewTimer(s.config.QueueTimeout)
	defer timer.Stop()
	select {
	case sem <- struct{}{}:
		return nil
	case <-timer.C:
		return fmt.Errorf(
			"subagent task waited more than %s for a concurrency slot (agents.maxConcurrent=%d, agents.maxConcurrentQueueTimeoutMs=%d); raise agents.maxConcurrent or agents.maxConcurrentQueueTimeoutMs",
			s.config.QueueTimeout, s.config.MaxConcurrent, s.config.QueueTimeout.Milliseconds(),
		)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// checkQueueDepth enforces the opt-in backpressure limit for one reader wave:
// more than MaxConcurrent+MaxQueueDepth tasks are rejected before any child
// starts. MaxQueueDepth=0 disables the check (unbounded waiting, as before).
func (s *SubagentScheduler) checkQueueDepth(readers int) error {
	depth := s.config.MaxQueueDepth
	if depth <= 0 || readers <= s.config.MaxConcurrent+depth {
		return nil
	}
	return fmt.Errorf(
		"subagent batch rejected by agents.maxConcurrentQueueDepth=%d: %d reader tasks exceed agents.maxConcurrent=%d + queue depth %d; raise agents.maxConcurrent/agents.maxConcurrentQueueDepth or set agents.maxConcurrentQueueDepth=0 for unbounded waiting",
		depth, readers, s.config.MaxConcurrent, depth,
	)
}

func (s *SubagentScheduler) readyTasks(readers []indexedSubagentTask, writers []indexedSubagentTask, done []bool, completedByID map[string]SubagentResult) ([]indexedSubagentTask, []indexedSubagentTask, error) {
	readyReaders := make([]indexedSubagentTask, 0, len(readers))
	for _, item := range readers {
		if done[item.index] {
			continue
		}
		satisfied, err := dependenciesSatisfied(item.task, completedByID)
		if err != nil {
			return nil, nil, err
		}
		if satisfied {
			readyReaders = append(readyReaders, item)
		}
	}

	readyWriters := make([]indexedSubagentTask, 0, len(writers))
	for _, item := range writers {
		if done[item.index] {
			continue
		}
		satisfied, err := dependenciesSatisfied(item.task, completedByID)
		if err != nil {
			return nil, nil, err
		}
		if satisfied {
			readyWriters = append(readyWriters, item)
		}
	}
	return readyReaders, readyWriters, nil
}

func dependenciesSatisfied(task SubagentTask, completedByID map[string]SubagentResult) (bool, error) {
	for _, dependency := range task.DependsOn {
		dependency = strings.TrimSpace(dependency)
		if dependency == "" {
			continue
		}
		if dependency == task.ID {
			return false, fmt.Errorf("subagent %q cannot depend on itself", task.ID)
		}
		if _, ok := completedByID[dependency]; !ok {
			return false, nil
		}
	}
	return true, nil
}

func (s *SubagentScheduler) enrichTaskFromDependencies(task SubagentTask, completedByID map[string]SubagentResult) SubagentTask {
	if len(task.DependsOn) == 0 {
		return task
	}

	seen := make(map[string]bool, len(task.PatchContext))
	merged := make([]FilePatch, 0, len(task.PatchContext))
	for _, patch := range task.PatchContext {
		key := patchIdentity(patch)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		merged = append(merged, patch)
	}

	for _, dependency := range task.DependsOn {
		result, ok := completedByID[strings.TrimSpace(dependency)]
		if !ok {
			continue
		}
		for _, patch := range result.Patches {
			key := patchIdentity(patch)
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			merged = append(merged, patch)
		}
	}

	task.PatchContext = merged
	return task
}

func metricMapValue(observation types.Observation, key string) map[string]interface{} {
	value, ok := observation.GetMetric(key)
	if !ok {
		return nil
	}
	typed, _ := value.(map[string]interface{})
	return typed
}

func pathFromToolInput(input interface{}) string {
	args, ok := input.(map[string]interface{})
	if !ok {
		return ""
	}
	for _, key := range []string{"file_path", "path", "target", "destination"} {
		if value, ok := args[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func stringMapValue(values map[string]interface{}, key string) string {
	if len(values) == 0 {
		return ""
	}
	if value, ok := values[key].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func intMapValue(values map[string]interface{}, key string) (int, bool) {
	if len(values) == 0 {
		return 0, false
	}
	switch typed := values[key].(type) {
	case int:
		return typed, true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), true
	case float32:
		return int(typed), true
	case float64:
		return int(typed), true
	default:
		return 0, false
	}
}

func patchSummary(tool string, metadata map[string]interface{}, output interface{}) string {
	switch {
	case strings.Contains(strings.ToLower(tool), "write"):
		action := firstNonEmptyString(stringMapValue(metadata, "action"), "updated")
		if oldSize, ok := intMapValue(metadata, "old_size"); ok {
			if newSize, ok := intMapValue(metadata, "new_size"); ok {
				return fmt.Sprintf("%s file (%d -> %d bytes)", action, oldSize, newSize)
			}
		}
		return action + " file"
	case strings.Contains(strings.ToLower(tool), "edit"), strings.Contains(strings.ToLower(tool), "patch"):
		if replacements, ok := intMapValue(metadata, "replacements"); ok {
			return fmt.Sprintf("applied %d replacement(s)", replacements)
		}
		if editsApplied, ok := intMapValue(metadata, "edits_applied"); ok {
			return fmt.Sprintf("applied %d edit(s)", editsApplied)
		}
	}

	if outputText, ok := output.(string); ok && strings.TrimSpace(outputText) != "" {
		return summarizePatchText(outputText, 160)
	}
	return ""
}

func summarizePatchText(text string, limit int) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	if text == "" || limit <= 0 || len(text) <= limit {
		return text
	}
	if limit <= 3 {
		return text[:limit]
	}
	return text[:limit-3] + "..."
}

func extractUnifiedDiff(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}

	lines := strings.Split(text, "\n")
	start := -1
	for index, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "--- ") {
			start = index
			break
		}
	}
	if start < 0 {
		return ""
	}

	end := len(lines)
	for index := start + 1; index < len(lines); index++ {
		if strings.HasPrefix(strings.TrimSpace(lines[index]), "```") {
			end = index
			break
		}
	}
	diff := strings.TrimSpace(strings.Join(lines[start:end], "\n"))
	if !strings.Contains(diff, "+++ ") {
		return ""
	}
	return diff
}

func pathFromDiff(diff string) string {
	if strings.TrimSpace(diff) == "" {
		return ""
	}

	lines := strings.Split(diff, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "+++ ") {
			if path := normalizeDiffPath(trimmed[4:]); path != "" {
				return path
			}
		}
	}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "--- ") {
			if path := normalizeDiffPath(trimmed[4:]); path != "" {
				return path
			}
		}
	}
	return ""
}

func normalizeDiffPath(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parts := strings.Fields(raw)
	if len(parts) == 0 {
		return ""
	}
	path := strings.TrimSpace(parts[0])
	if path == "/dev/null" {
		return ""
	}
	path = strings.TrimPrefix(path, "a/")
	path = strings.TrimPrefix(path, "b/")
	return strings.TrimSpace(path)
}

func patchIdentity(patch FilePatch) string {
	return firstNonEmptyString(strings.TrimSpace(patch.Path), strings.TrimSpace(patch.Diff), strings.TrimSpace(patch.Summary))
}

func metricStringSliceValue(observation types.Observation, key string) []string {
	value, ok := observation.GetMetric(key)
	if !ok {
		return nil
	}
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []interface{}:
		values := make([]string, 0, len(typed))
		for _, item := range typed {
			text, _ := item.(string)
			text = strings.TrimSpace(text)
			if text == "" {
				continue
			}
			values = append(values, text)
		}
		return values
	default:
		return nil
	}
}

func derivePatchApplyStatus(tool string, metadata map[string]interface{}, output interface{}) string {
	if !isWriteLikeToolName(tool) {
		return ""
	}
	if action := strings.TrimSpace(stringMapValue(metadata, "action")); action != "" {
		return "applied"
	}
	if replacements, ok := intMapValue(metadata, "replacements"); ok && replacements >= 0 {
		return "applied"
	}
	if editsApplied, ok := intMapValue(metadata, "edits_applied"); ok && editsApplied >= 0 {
		return "applied"
	}
	if _, ok := output.(string); ok {
		return "applied"
	}
	return "applied"
}

func (s *SubagentScheduler) prepareTasks(tasks []SubagentTask) ([]SubagentTask, error) {
	prepared := make([]SubagentTask, 0, len(tasks))
	parentAgent := (*Agent)(nil)
	if s != nil {
		parentAgent = s.parent
	}
	parentPolicy := (*ToolExecutionPolicy)(nil)
	if parentAgent != nil {
		parentPolicy = parentAgent.GetToolExecutionPolicy()
	}
	knownIDs := make(map[string]bool, len(tasks))

	for index, task := range tasks {
		effective := task
		if effective.ID == "" {
			effective.ID = fmt.Sprintf("subagent_%d", index+1)
		}
		if knownIDs[effective.ID] {
			return nil, fmt.Errorf("duplicate subagent task id %q", effective.ID)
		}
		if parentPolicy != nil && parentPolicy.ReadOnly && !effective.ReadOnly {
			// A child cannot widen a read-only parent boundary. Narrow the
			// request instead of rejecting the whole batch; the requested
			// write-like tools are removed below and the child remains subject
			// to the execution-side hard deny.
			effective.ReadOnly = true
			effective.ReadOnlySource = "parent_tool_execution_policy"
		}
		// Resolve the child tool surface through the shared helper: it
		// normalizes the requested allowlist, rejects requests that could only
		// produce a tool-less child (retired vocabulary / parent intersection)
		// and narrows the task to what the derived child policy grants.
		resolved, _, err := resolveChildToolSurface(parentAgent, effective)
		if err != nil {
			return nil, err
		}
		effective = resolved
		if effective.ReadOnly {
			if effective.ReadOnlySource == "" {
				effective.ReadOnlySource = "spawn_subagents.read_only"
			}
			effective.ToolsWhitelist, effective.ReadOnlyFilteredTools = filterReadOnlyTools(
				effective.ToolsWhitelist,
			)
		}
		knownIDs[effective.ID] = true
		prepared = append(prepared, effective)
	}

	for _, task := range prepared {
		for _, dependency := range task.DependsOn {
			dependency = strings.TrimSpace(dependency)
			if dependency == "" {
				continue
			}
			if !knownIDs[dependency] {
				return nil, fmt.Errorf("subagent %q depends on unknown task %q", task.ID, dependency)
			}
		}
	}
	if err := validateHighRiskWriterVerifierTasks(prepared); err != nil {
		return nil, err
	}

	return prepared, nil
}

func validateHighRiskWriterVerifierTasks(tasks []SubagentTask) error {
	for _, writer := range tasks {
		if !isHighRiskWriterTask(writer) {
			continue
		}
		verifierFound := false
		for _, task := range tasks {
			if !strings.EqualFold(strings.TrimSpace(task.Role), "verifier") ||
				!task.ReadOnly ||
				!dependsOnAny(task.DependsOn, []string{writer.ID}) {
				continue
			}
			verifierFound = true
			if subagentDifficultyRank(task.Difficulty) < subagentDifficultyRank("hard") {
				return fmt.Errorf("hard or expert writer subagent %q requires a hard-or-higher verifier", writer.ID)
			}
		}
		if !verifierFound {
			return fmt.Errorf("hard or expert writer subagent %q requires a read-only verifier dependency", writer.ID)
		}
	}
	return nil
}

func (s *SubagentScheduler) childPolicy(task SubagentTask) *ToolExecutionPolicy {
	parent := (*Agent)(nil)
	if s != nil {
		parent = s.parent
	}
	return agentChildPolicy(parent, task)
}

func subagentWritePaths(task SubagentTask) []string {
	paths := make([]string, 0, len(task.PatchContext))
	for _, patch := range task.PatchContext {
		if path := strings.TrimSpace(patch.Path); path != "" {
			paths = append(paths, path)
		}
	}
	return paths
}

// subagentReadOnlyFilteredTools returns a copy of the write-like tools removed
// from a read-only child's requested whitelist so the parent report can show
// which requested capabilities the child never received.
func subagentReadOnlyFilteredTools(task SubagentTask) []string {
	if len(task.ReadOnlyFilteredTools) == 0 {
		return nil
	}
	return append([]string(nil), task.ReadOnlyFilteredTools...)
}

// subagentReadOnlySource resolves the effective read-only boundary origin for
// the parent spawn report. decodeSubagentTasks stamps the explicit source on
// the task; this helper mirrors the child factory's fallback so scheduler and
// factory never disagree about which boundary is in force.
func subagentReadOnlySource(task SubagentTask) string {
	if source := strings.TrimSpace(task.ReadOnlySource); source != "" {
		return source
	}
	if task.ReadOnly {
		return "spawn_subagents.read_only"
	}
	return ""
}

type indexedSubagentTask struct {
	index int
	task  SubagentTask
}

func (s *SubagentScheduler) partitionTasks(tasks []SubagentTask) ([]indexedSubagentTask, []indexedSubagentTask, error) {
	readers := make([]indexedSubagentTask, 0, len(tasks))
	writers := make([]indexedSubagentTask, 0, 1)

	for index, task := range tasks {
		if task.ReadOnly {
			if containsWriteLikeTool(task.ToolsWhitelist) {
				return nil, nil, fmt.Errorf("read-only subagent %q requested write-like tools", task.ID)
			}
			readers = append(readers, indexedSubagentTask{index: index, task: task})
			continue
		}
		writers = append(writers, indexedSubagentTask{index: index, task: task})
	}

	if s.config.EnforceSingleWriter && len(writers) > 1 {
		return nil, nil, fmt.Errorf("single-writer policy violation: %d writer subagents requested", len(writers))
	}

	return readers, writers, nil
}

func containsWriteLikeTool(tools []string) bool {
	for _, tool := range tools {
		if isWriteLikeToolName(tool) {
			return true
		}
	}
	return false
}

// filterReadOnlyTools narrows a requested child allowlist without widening it.
// A read-only task may still use every explicitly requested non-mutating tool,
// while write-like names are omitted before the child is built. The returned
// empty-but-non-nil slice intentionally means "no requested tools", not an
// unrestricted child.
func filterReadOnlyTools(tools []string) ([]string, []string) {
	if tools == nil {
		return nil, nil
	}
	filtered := make([]string, 0, len(tools))
	removed := make([]string, 0)
	seen := make(map[string]bool, len(tools))
	for _, tool := range tools {
		tool = strings.TrimSpace(tool)
		if tool == "" {
			continue
		}
		if isWriteLikeToolName(tool) {
			if !seen[tool] {
				removed = append(removed, tool)
				seen[tool] = true
			}
			continue
		}
		if seen[tool] {
			continue
		}
		seen[tool] = true
		filtered = append(filtered, tool)
	}
	if len(filtered) == 0 {
		filtered = []string{}
	}
	return filtered, removed
}

func (s *SubagentScheduler) emitSubagentDenied(options SubagentRunOptions, taskID, policy, reason string, payload map[string]interface{}) {
	if s == nil || s.parent == nil {
		return
	}
	if payload == nil {
		payload = map[string]interface{}{}
	}
	if taskID != "" {
		payload["subagent_id"] = taskID
	}
	if policy != "" {
		payload["policy"] = policy
	}
	if options.ParentSessionID != "" {
		payload["parent_session_id"] = options.ParentSessionID
	}
	if options.ParentToolCallID != "" {
		payload["parent_tool_call_id"] = options.ParentToolCallID
	}
	payload["reason"] = reason
	payload["trace_id"] = options.TraceID
	s.parent.emitRuntimeEvent("subagent.denied", "", "", payload)
}

func (s *SubagentScheduler) checkFailureCircuit(options SubagentRunOptions) error {
	if s == nil {
		return nil
	}
	s.failureMu.Lock()
	open := s.circuitOpen
	consecutive := s.consecutiveFailures
	threshold := s.config.MaxConsecutiveFailures
	s.failureMu.Unlock()
	if !open {
		return nil
	}
	err := runtimeerrors.Newf(
		runtimeerrors.ErrAgentSubagentCircuitOpen,
		"subagent batch circuit is open after %d consecutive unsuccessful batches (threshold=%d); inspect or reset the coordinator instead of retrying the same spawn",
		consecutive, threshold,
	)
	s.emitSubagentDenied(options, "", "circuit_open", err.Error(), map[string]interface{}{
		"consecutive_failures": consecutive,
		"failure_threshold":    threshold,
	})
	return err
}

func (s *SubagentScheduler) recordBatchOutcome(options SubagentRunOptions, results []SubagentResult, runErr error) {
	if s == nil {
		return
	}
	success := runErr == nil && len(results) > 0
	if success {
		for _, result := range results {
			if !result.Success || strings.TrimSpace(result.Error) != "" {
				success = false
				break
			}
		}
	}

	s.failureMu.Lock()
	if success {
		s.consecutiveFailures = 0
		s.failureMu.Unlock()
		return
	}
	s.consecutiveFailures++
	consecutive := s.consecutiveFailures
	threshold := s.config.MaxConsecutiveFailures
	openedNow := !s.circuitOpen && consecutive >= threshold
	if openedNow {
		s.circuitOpen = true
	}
	s.failureMu.Unlock()
	if !openedNow || s.parent == nil {
		return
	}
	s.parent.emitRuntimeEvent("subagent.batch.circuit_open", options.ParentSessionID, "spawn_subagents", map[string]interface{}{
		"trace_id":             options.TraceID,
		"parent_session_id":    options.ParentSessionID,
		"parent_tool_call_id":  options.ParentToolCallID,
		"consecutive_failures": consecutive,
		"failure_threshold":    threshold,
		"error":                errorString(runErr),
	})
}

func classifySubagentDeniedPolicy(reason string) string {
	lower := strings.ToLower(reason)
	switch {
	case strings.Contains(lower, "single-writer"):
		return "single_writer"
	case strings.Contains(lower, "read-only parent policy"):
		return "read_only"
	case strings.Contains(lower, "write-like tools"):
		return "read_only"
	case strings.Contains(lower, "no longer serves"):
		return "tool_vocabulary"
	default:
		return "subagent_scheduler"
	}
}

func usageTotal(usage *types.TokenUsage) int {
	if usage == nil {
		return 0
	}
	return usage.TotalTokens
}
