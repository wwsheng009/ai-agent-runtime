package agent

// iterate_goal.go — 有界全新上下文迭代（DSH 借鉴方案 P2-2'）。
//
// 设计来源：docs/plan/deepseek-harness-agent-borrowing-plan-review-v2-20261016.md
// §4 P2-2'。固定编排移植 DSH Ralph（packages/experimental/tool-ralph）的
// "deployment-owned orchestration, model supplies data only" 原则：
//
//   - 模型只提供 objective 与可选 max_rounds；循环/路由/handoff 校验全部
//     部署侧固定，模型不可改变。
//   - 每轮 spawn 一个**全新**子级（fresh 上下文，禁 fork/路由覆盖——由
//     Negotiate 的 fork 拒绝 + 本实现的固定任务包双保险）。
//   - 跨轮状态 = 共享工作区（子级各自验证）+ 有界结构化 handoff。
//
// 与 DSH 的有意偏离（v2 方案 §5.2 差异表）：
//   - 默认 16 轮（DSH 256），硬上限 64：本地单机成本考虑；
//   - 进展门控（G6，超越 DSH）：每轮按 goal 语义分类 progress/no-progress，
//     连续 no-progress 达阈值提前返回 stalled 终态，不烧满轮数预算；
//   - 状态归集复用 SubagentScheduler（每轮一个任务行），不走 workflow 引擎。

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// iterateGoalDefaults 部署缺省（deployment-owned，模型不可改）。
const (
	// DefaultIterateGoalMaxRounds 默认轮数（v2 方案：本地成本考虑取 16）。
	DefaultIterateGoalMaxRounds = 16
	// MaxIterateGoalRounds 硬上限（模型请求超过即拒绝）。
	MaxIterateGoalRounds = 64
	// IterateGoalNoProgressStrikeLimit 连续无进展轮阈值（G6 进展门控，
	// 语义对齐 goal/continuation.go 的 ContinuationNoProgressStrikeLimit=3）。
	IterateGoalNoProgressStrikeLimit = 3
)

// IterateGoalRunStatus 终态四 + 1 分类（v2 方案新增 stalled）。
type IterateGoalRunStatus string

const (
	IterateGoalComplete      IterateGoalRunStatus = "complete"
	IterateGoalBlocked       IterateGoalRunStatus = "blocked"
	IterateGoalBudgetLimited IterateGoalRunStatus = "budget-limited"
	IterateGoalRoundFailed   IterateGoalRunStatus = "round-failed"
	IterateGoalStalled       IterateGoalRunStatus = "stalled" // G6 进展门控
)

// IterateGoalResult 一次迭代的父代理可见终态。
type IterateGoalResult struct {
	Status        IterateGoalRunStatus `json:"status"`
	RoundsStarted int                  `json:"rounds_started"`
	Report        *Handoff             `json:"report,omitempty"`
}

// IterateGoalProgressClassifier 按轮结算分类该轮是否有实质进展。
// 返回 true = progress；false = no-progress（状态复述/空转）。
// 判定基于子级 handoff 的产出证据而不是自由文本承诺（对齐 goal 续跑的
// no-progress 语义：status restatements and unexecuted plans are no progress）。
type IterateGoalProgressClassifier func(round int, report Handoff) bool

// DefaultIterateGoalProgressClassifier 内建分类器：continue 轮的 nextSteps
// 若与上一轮完全相同视为复述（无进展）；complete 视为进展；blocked 单列。
func DefaultIterateGoalProgressClassifier(prev, report Handoff) bool {
	if report.Status != HandoffContinue {
		return true // complete/blocked 都是终态推进
	}
	// 与上一轮 nextSteps 完全一致 → 复述循环。
	if len(prev.NextSteps) > 0 && len(report.NextSteps) > 0 &&
		strings.Join(prev.NextSteps, "\x00") == strings.Join(report.NextSteps, "\x00") {
		return false
	}
	return true
}

// IterateGoalConfig 部署侧配置（宿主装配；模型不可见）。
type IterateGoalConfig struct {
	MaxRounds         int
	MaxHandoffChars   int
	ProgressClassifier func(prev, report Handoff) bool
	// MaxAttemptsPerTask 每轮子级对只读瞬时失败的有界重试上限（透传）。
	MaxAttemptsPerTask int
}

func (c IterateGoalConfig) resolved() IterateGoalConfig {
	if c.MaxRounds <= 0 {
		c.MaxRounds = DefaultIterateGoalMaxRounds
	}
	if c.MaxRounds > MaxIterateGoalRounds {
		c.MaxRounds = MaxIterateGoalRounds
	}
	if c.MaxHandoffChars <= 0 {
		c.MaxHandoffChars = MaxHandoffChars
	}
	if c.ProgressClassifier == nil {
		classifier := DefaultIterateGoalProgressClassifier
		c.ProgressClassifier = classifier
	}
	return c
}

// iterateGoalRoundPrompt 构造第 round 轮子级提示。文案移植 DSH RALPH_SCRIPT
// 语义（fresh worker / immutable objective / workspace as long-term memory /
// bounded handoff），措辞本地化。报告交付走 submit_report 契约（部署自有
// round schema）；最终文本的 JSON 对象仍作为兼容回退被防御解码接受。
func iterateGoalRoundPrompt(objective string, round, maxRounds int, previous *Handoff) string {
	prior := "(none — this is the first round)"
	if previous != nil {
		if encoded, err := json.Marshal(previous); err == nil {
			prior = string(encoded)
		}
	}
	lines := []string{
		"You are one fresh worker in a bounded iteration toward one objective. You receive no parent conversation and no prior child session. Do not spawn or iterate: this round already is the worker.",
		"Immutable objective:\n" + objective,
		fmt.Sprintf("Round: %d of %d.", round, maxRounds),
		"The shared workspace and its current working tree are the long-term memory and source of truth. Inspect them before acting, preserve existing work, perform concrete in-scope work, and verify what you change. Treat the previous report only as a bounded handoff; confirm it against the workspace.",
		"Previous structured handoff:\n" + prior,
		"Deliver your round report by calling the submit_report tool exactly once, when the round's work is done or definitively blocked. Arguments: status (continue|complete|blocked), summary, evidence[], next_steps[], blocker. Use status continue with at least one next_steps entry while useful work remains; complete only with concrete evidence and no next_steps; blocked only when no meaningful progress is possible without human input or an external-state change. blocker must be empty unless blocked.",
	}
	return strings.Join(lines, "\n\n")
}

// SubagentRoundRunner 是迭代每轮的执行内核：*SubagentScheduler 满足它；
// 测试可注入脚本化实现（G2 契约优先的依赖倒置——编排逻辑不绑定具体类型）。
type SubagentRoundRunner interface {
	RunChildren(ctx context.Context, options SubagentRunOptions, tasks []SubagentTask) ([]SubagentResult, error)
}

// RunIterateGoal 执行一次有界全新上下文迭代。
//
// runner 是调用方（父级）的子代理调度器；options 携带父链归属。每轮
// spawn 一个 fresh 子级并从其最终输出防御解码 handoff；终态由 handoff
// 状态机与轮数/进展预算共同决定。
func RunIterateGoal(ctx context.Context, runner SubagentRoundRunner, options SubagentRunOptions, objective string, requestedMaxRounds int, config IterateGoalConfig) (IterateGoalResult, error) {
	if runner == nil {
		return IterateGoalResult{}, fmt.Errorf("iterate_goal requires a subagent round runner")
	}
	objective = strings.TrimSpace(objective)
	if objective == "" {
		return IterateGoalResult{}, fmt.Errorf("iterate_goal objective is required")
	}
	cfg := config.resolved()
	if requestedMaxRounds < 0 {
		requestedMaxRounds = 0
	}
	if requestedMaxRounds > cfg.MaxRounds {
		return IterateGoalResult{}, fmt.Errorf(
			"iterate_goal max_rounds %d exceeds the deployment ceiling %d; next_action=lower_max_rounds",
			requestedMaxRounds, cfg.MaxRounds,
		)
	}
	maxRounds := requestedMaxRounds
	if maxRounds == 0 {
		maxRounds = cfg.MaxRounds
	}

	var previous *Handoff
	noProgressStrikes := 0
	for round := 1; round <= maxRounds; round++ {
		if ctx.Err() != nil {
			return IterateGoalResult{Status: IterateGoalRoundFailed, RoundsStarted: round, Report: previous}, ctx.Err()
		}
		task := SubagentTask{
			ID:          fmt.Sprintf("iterate_%d", round),
			TaskType:    "generate",
			Goal:        iterateGoalRoundPrompt(objective, round, maxRounds, previous),
			TaskSubject: fmt.Sprintf("iteration round %d", round),
			ReadOnly:    false,
			// 部署自有 round 报告契约（submit_report）：schema 由内核强制，
			// 不经能力协商（ReportSchemaGranted）；HandoffIn 让调度器解码
			// 路径把提交的报告回填为 HandoffOut。
			ReportSchema:        handoffReportSchema(),
			ReportSchemaGranted: true,
			HandoffIn:           previous,
		}
		result, err := runner.RunChildren(ctx, options, []SubagentTask{task})
		if err != nil || len(result) == 0 {
			return IterateGoalResult{Status: IterateGoalRoundFailed, RoundsStarted: round, Report: previous}, err
		}
		roundResult := result[0]
		if !roundResult.Success {
			// 子级 run 失败（非模型 handoff 问题）：round-failed，带最后交接。
			return IterateGoalResult{Status: IterateGoalRoundFailed, RoundsStarted: round, Report: previous}, nil
		}
		// 报告解码按契约优先顺序（HandoffOut → StructuredReport → 文本回退）。
		report, ok := decodeIterateGoalRoundReport(roundResult, cfg.MaxHandoffChars)
		if !ok {
			// 子级没有给出结构化交接：按 no-progress 计，继续下一轮让新
			// 上下文重试。熔断交给进展门控而不是单轮否决。
			noProgressStrikes++
			if noProgressStrikes >= IterateGoalNoProgressStrikeLimit {
				return IterateGoalResult{Status: IterateGoalStalled, RoundsStarted: round, Report: previous}, nil
			}
			continue
		}
		switch report.Status {
		case HandoffComplete:
			return IterateGoalResult{Status: IterateGoalComplete, RoundsStarted: round, Report: &report}, nil
		case HandoffBlocked:
			return IterateGoalResult{Status: IterateGoalBlocked, RoundsStarted: round, Report: &report}, nil
		case HandoffContinue:
			progress := cfg.ProgressClassifier(*orEmpty(previous), report)
			if !progress {
				noProgressStrikes++
			} else {
				noProgressStrikes = 0
			}
			if noProgressStrikes >= IterateGoalNoProgressStrikeLimit {
				return IterateGoalResult{Status: IterateGoalStalled, RoundsStarted: round, Report: &report}, nil
			}
			previous = &report
		}
	}
	return IterateGoalResult{Status: IterateGoalBudgetLimited, RoundsStarted: maxRounds, Report: previous}, nil
}

// decodeIterateGoalRoundReport 按契约优先顺序解码一轮报告：
//  1. HandoffOut（handoff 通道，HandoffIn != nil 的轮次）；
//  2. StructuredReport（submit_report 收口的部署自有 schema，含首轮）；
//  3. 最终文本 JSON 识别（兼容回退）。
//
// 返回 false 表示该轮没有可用的结构化报告。
func decodeIterateGoalRoundReport(roundResult SubagentResult, maxChars int) (Handoff, bool) {
	if roundResult.HandoffOut != nil {
		return *roundResult.HandoffOut, true
	}
	if roundResult.StructuredReport != nil {
		statusValue, _ := roundResult.StructuredReport["status"].(string)
		decoded, err := DecodeHandoff(roundResult.StructuredReport, HandoffStatus(statusValue), maxChars)
		if err == nil {
			return decoded, true
		}
	}
	if candidate := decodeHandoffCandidate(roundResult.Summary); candidate.raw != nil {
		decoded, err := DecodeHandoff(candidate.raw, candidate.status, maxChars)
		if err == nil {
			return decoded, true
		}
	}
	return Handoff{}, false
}

func orEmpty(h *Handoff) *Handoff {
	if h == nil {
		empty := Handoff{Status: HandoffContinue}
		return &empty
	}
	return h
}

// IterateGoalTimeoutSec 每轮子级的缺省 wall-clock（秒）；0 = 走路由缺省。
// 保留常量供宿主配置引用；当前不强制。
const IterateGoalTimeoutSec = 0

// IterateGoalToolName 模型侧工具名。
const IterateGoalToolName = "iterate_goal"

// iterateGoalToolEnabledKey 是宿主启用 iterate_goal 的 Agent option 键
//（G4 默认关 + 一键灰度）。懒初始化的 scheduler 让"有 scheduler"失去
// 判别力——预算敏感场景（长会话压缩轨迹）不应被额外工具 schema 推高，
// 因此工具面只在宿主显式开启时暴露。
const iterateGoalToolEnabledKey = "iterate_goal_enabled"

// SetIterateGoalEnabled 开启/关闭本 agent 工具面上的 iterate_goal。
func (a *Agent) SetIterateGoalEnabled(enabled bool) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.config == nil {
		return
	}
	if a.config.Options == nil {
		a.config.Options = map[string]interface{}{}
	}
	a.config.Options[iterateGoalToolEnabledKey] = enabled
}

// iterateGoalToolEnabled 报告本 agent 是否显式启用了 iterate_goal。
func (a *Agent) iterateGoalToolEnabled() bool {
	if a == nil {
		return false
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.config == nil || a.config.Options == nil {
		return false
	}
	enabled, _ := a.config.Options[iterateGoalToolEnabledKey].(bool)
	return enabled
}

// iterateGoalToolOutput 是 executeIterateGoal 的统一出参。
type iterateGoalToolOutput struct {
	text string
	err  string
}

// IterateGoalToolDefinition 返回 iterate_goal 的模型侧 schema。
// 入参刻意最小（G7/6 deployment-owned）：objective + 可选 max_rounds。
func IterateGoalToolDefinition() types.ToolDefinition {
	return types.ToolDefinition{
		Name: IterateGoalToolName,
		Description: "Iterate toward one objective with a fresh child and a bounded structured handoff per round. " +
			"Use only when work needs several independent fresh-context rounds on the same objective (refining a document, debugging a stubborn issue) — the child of each round sees no parent conversation; the shared workspace is the long-term memory and only a bounded handoff crosses rounds. " +
			"The call blocks until a round reports complete/blocked, the progress gate stalls, or the round budget is exhausted. For parallel independent subtasks use subagent_spawn_batch instead; for ordinary same-session work just keep working.",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"objective": map[string]interface{}{
					"type":        "string",
					"description": "The immutable completion objective for every round.",
				},
				"max_rounds": map[string]interface{}{
					"type":        "integer",
					"minimum":     1,
					"description": fmt.Sprintf("Optional round cap (default %d, deployment ceiling %d). Values above the ceiling are rejected.", DefaultIterateGoalMaxRounds, MaxIterateGoalRounds),
				},
			},
			"required": []string{"objective"},
		},
	}
}

// executeIterateGoal 在父级 loop 的 act 路径同步执行一次迭代。
// 与 subagent_spawn_batch 的异步批次不同：迭代需要每轮 handoff 反馈决定下一轮，
// 无法后台化而不破坏语义（对齐 DSH foreground Ralph）。
func (loop *ReActLoop) executeIterateGoal(ctx context.Context, sessionID, traceID string, tc types.ToolCall) iterateGoalToolOutput {
	objective := strings.TrimSpace(stringValue(tc.Args["objective"]))
	if objective == "" {
		return iterateGoalToolOutput{err: "iterate_goal objective is required"}
	}
	maxRounds := 0
	if raw, ok := tc.Args["max_rounds"]; ok && raw != nil {
		switch value := raw.(type) {
		case float64:
			maxRounds = int(value)
		case int:
			maxRounds = value
		case string:
			parsed, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil {
				return iterateGoalToolOutput{err: fmt.Sprintf("iterate_goal max_rounds %q is not an integer", value)}
			}
			maxRounds = parsed
		default:
			return iterateGoalToolOutput{err: "iterate_goal max_rounds must be an integer"}
		}
	}
	scheduler := loop.agent.GetSubagentScheduler()
	if scheduler == nil {
		return iterateGoalToolOutput{err: "iterate_goal requires a subagent scheduler, which is not configured for this agent"}
	}
	options := SubagentRunOptions{
		TraceID:         traceID,
		ParentSessionID: sessionID,
		Depth:           1,
	}
	outcome, err := RunIterateGoal(ctx, scheduler, options, objective, maxRounds, IterateGoalConfig{})
	if err != nil {
		return iterateGoalToolOutput{err: err.Error()}
	}
	lines := []string{
		fmt.Sprintf("iterate_goal finished: status=%s rounds_started=%d", outcome.Status, outcome.RoundsStarted),
	}
	if outcome.Report != nil {
		if encoded, encodeErr := json.Marshal(outcome.Report); encodeErr == nil {
			lines = append(lines, "Final handoff:\n"+string(encoded))
		}
	} else {
		lines = append(lines, "Final handoff: (none — the last round did not produce a structured report)")
	}
	return iterateGoalToolOutput{text: strings.Join(lines, "\n\n")}
}
