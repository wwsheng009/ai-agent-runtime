package agent

import (
	"fmt"
	"sort"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/modelrouting"
	"github.com/wwsheng009/ai-agent-runtime/internal/providerhealth"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// 主 Agent 动态 provider/model 切换（方案
// docs/plan/main-agent-dynamic-provider-model-switching-plan-20260921.md §5.2–§5.10）。
//
// 不变量（§5.6）：
//   - INV-1 本文件不写 prompt / 不写持久历史：route 只改写 loop.config 的
//     provider / model / reasoning_effort；模型可见的字节流仅由工具结果承载。
//   - INV-2 turn 内逐字节稳定：route 只在 step 边界整体复写，绝不改写已发送的消息。
//   - INV-3 不向 LLM 暴露 provider/model：工具 schema 与占位结果里都没有它们。
//   - INV-4 同 provider 换 model 时前缀字节一致：只替换 config 字段。
//
// 与子 Agent 路由的关系：**独立实现**。profiles 复用 agentconfig 的类型，但解析
// 走一次性构造的 adapter 配置，主 Agent 的开关不会改变 aicli.subagents.routing。
const predictTaskDifficultyToolName = "predict_task_difficulty"

// route source 四值枚举（§5.10 规则 3）。取值必须是闭集，运维面板与测试都按它
// 分类，因此不要用自由文本。
const (
	// mainAgentRouteSourcePredicted 表示难度上报驱动的改道。
	mainAgentRouteSourcePredicted = "predicted"
	// mainAgentRouteSourceBaseline 表示回到 turn 基线（默认档位 / 成本护栏 / 禁用）。
	mainAgentRouteSourceBaseline = "baseline"
	// mainAgentRouteSourceFailoverCandidate 表示主选被健康门禁拦下、候选链接管。
	mainAgentRouteSourceFailoverCandidate = "failover_candidate"
	// mainAgentRouteSourceHealthExhaustedBaseline 表示整条候选链耗尽，只能回到基线
	// （主 Agent 没有父 Agent 可以降级，因此目标固定为基线）。
	mainAgentRouteSourceHealthExhaustedBaseline = "health_exhausted_baseline"
)

// predictTaskDifficultyPlaceholder 是 §5.4 R1 规定的占位工具结果：模型只看到
// 「上报已受理」，看不到它被翻译成了哪个 provider/model（INV-3）。
func predictTaskDifficultyPlaceholder(difficulty string) string {
	return fmt.Sprintf("[route reported: %s]", difficulty)
}

// mainAgentMetaOnlyStepCredit 是「meta-only 响应不消耗 step」（§5.4 R2/R4）的
// 连续额度上限。
//
// 为什么必须有上限：step 是 run() 唯一的循环边界。若上报永不消耗 step，一个只会
// 重复上报的模型就能让 run() 永不终止（且每次迭代都是一次真实 LLM 调用）。
// 额度用尽后上报恢复消耗 step，循环必然收敛。取 2 的理由：正常模型在一个 turn 内
// 至多「上报一次新档位 + 偶尔改口」，连续两次纯上报已属异常；额度是防死循环的
// 兜底，不是鼓励重复上报的许可。
const mainAgentMetaOnlyStepCredit = 2

// isPredictTaskDifficultyCall 是工具名的规范化比较（容忍大小写与空白）。
func isPredictTaskDifficultyCall(name string) bool {
	return strings.ToLower(strings.TrimSpace(name)) == predictTaskDifficultyToolName
}

// metaOnlyToolBatch 报告该批次是否「只有难度上报」——R4 的续轮判据。
func metaOnlyToolBatch(calls []types.ToolCall) bool {
	if len(calls) == 0 {
		return false
	}
	for _, call := range calls {
		if !isPredictTaskDifficultyCall(call.Name) {
			return false
		}
	}
	return true
}

// budgetedToolCallCount 返回计入 MaxToolCalls 工具预算的调用数（§5.4 R2：上报
// 「不计入 tool budget」）。上报是零副作用的内存态写入，把它计入预算会让
// 「上报有代价」——正是 R2 要消除的激励扭曲。
func budgetedToolCallCount(calls []types.ToolCall) int {
	count := 0
	for _, call := range calls {
		if isPredictTaskDifficultyCall(call.Name) {
			continue
		}
		count++
	}
	return count
}

// mainAgentRouteSnapshot 是一次 route 的完整可比较快照。三个字段一起比较，避免
// 「同 provider 同 model 但 reasoning_effort 变了」被误判为未变化。
type mainAgentRouteSnapshot struct {
	Provider        string
	Model           string
	ReasoningEffort string
}

func mainAgentRouteSnapshotOf(cfg *LoopReActConfig) mainAgentRouteSnapshot {
	if cfg == nil {
		return mainAgentRouteSnapshot{}
	}
	return mainAgentRouteSnapshot{
		Provider:        strings.TrimSpace(cfg.Provider),
		Model:           strings.TrimSpace(cfg.Model),
		ReasoningEffort: strings.TrimSpace(cfg.ReasoningEffort),
	}
}

// applyTo 把快照整体复写到循环配置（§5.3：必须复写 loop.config，step 边界生效）。
func (s mainAgentRouteSnapshot) applyTo(cfg *LoopReActConfig) {
	if cfg == nil {
		return
	}
	cfg.Provider = s.Provider
	cfg.Model = s.Model
	cfg.ReasoningEffort = s.ReasoningEffort
}

func (s mainAgentRouteSnapshot) equal(other mainAgentRouteSnapshot) bool {
	return s.Provider == other.Provider && s.Model == other.Model && s.ReasoningEffort == other.ReasoningEffort
}

// effectiveAgainst 用回退基线补齐快照里的空 provider/model，得到「请求路径实际
// 会用的」route。空值在请求路径上表示「沿用下一级配置」，不是「没有 provider」。
func (s mainAgentRouteSnapshot) effectiveAgainst(fallback mainAgentRouteSnapshot) mainAgentRouteSnapshot {
	out := s
	if out.Provider == "" {
		out.Provider = fallback.Provider
	}
	if out.Model == "" {
		out.Model = fallback.Model
	}
	return out
}

// mainAgentRouteEffectiveBaseline 返回 turn 入口「请求路径实际会用的」route。
//
// 回退链必须与 requestProvider/requestModel 逐字对齐（loop.go:308 / loop.go:320）：
// 会话（chat）路径下 buildLocalChatLoopConfig 只填 ReasoningEffort，provider/model
// 留在 agent.config，因此 loop.config 的这两个字段恒为空。若基线只看 loop.config，
// route_cleared 的 restored_provider/restored_model 就永远是空串，而 §6.2 说这两个
// 字段存在的唯一目的正是证明「确实还原到了正确基线」。
func (loop *ReActLoop) mainAgentRouteEffectiveBaseline() mainAgentRouteSnapshot {
	if loop == nil {
		return mainAgentRouteSnapshot{}
	}
	effort := ""
	if loop.config != nil {
		effort = strings.TrimSpace(loop.config.ReasoningEffort)
	}
	return mainAgentRouteSnapshot{
		Provider:        loop.requestProvider(),
		Model:           loop.requestModel(),
		ReasoningEffort: effort,
	}
}

// mainAgentRouteChanged 判断 target 相对 turn 基线是否构成一次真实改道。
//
// 两侧都必须先归一化成「请求路径实际会用的」route：会话路径下 loop.config 的
// provider/model 为空，拿空快照当基线会把任何非空 target 都算成改道（假阳性），
// route_changed 也就不再是「有多少 step 真的改道」的可信分母
// （usageanalytics/ingest_routes.go:111）。
func mainAgentRouteChanged(baselineEffective, target mainAgentRouteSnapshot) bool {
	return !baselineEffective.equal(target.effectiveAgainst(baselineEffective))
}

// mainAgentRouteWarnings 归一化一次路由决策的告警列表。nil 归一为空切片，与
// candidates 同理：「评估过但无告警」必须能和「字段缺失」区分开，否则
// usage_routes.warnings_json 的恒空列无法与「本次真的没有告警」区分。
// 告警是「为什么被升档 / 被降级 / 走了回退」的唯一证据，例如
// difficulty_promoted_by_keyword:*、route_health_exhausted_parent、
// provider_fallback_parent —— 丢弃它们会让这些降级在审计中完全无痕。
func mainAgentRouteWarnings(decision modelrouting.RouteDecision) []string {
	if len(decision.Warnings) == 0 {
		return []string{}
	}
	return append([]string(nil), decision.Warnings...)
}

// mainAgentRouteState 是 turn 内的动态 route 状态：内存态，不落盘、不进 prompt。
//
// 生命周期严格等于一次 run()：beginTurnRoute 在入口重置，endTurnRoute（defer）
// 写回基线并清空。任何中途 return（取消、预算硬停、panic）都必须经过 defer，
// 否则 route 会泄漏到下一个 turn（MG3）。
type mainAgentRouteState struct {
	// baseline 是 turn 入口的宿主配置（写回目标）。
	baseline         mainAgentRouteSnapshot
	baselineCaptured bool
	// baselineEffective 是 turn 入口「请求路径实际会用的」route：baseline 再走一次
	// requestProvider/requestModel 的回退链。两者必须分开——baseline 用于**精确
	// 写回**（空值还原成空值，否则会把会话默认值固化成显式覆盖，改变 config 语义），
	// baselineEffective 用于**对外证明**还原正确（§6.2：restored_provider/
	// restored_model 存在的唯一目的就是「证明确实还原到了正确基线」）。会话路径下
	// loop.config 的 provider/model 恒为空，只看 baseline 会让这条证据永远是空串。
	baselineEffective mainAgentRouteSnapshot
	// floor 是 turn 内冻结的路由下限（§5.10 规则 1：健康维度 turn 内闩锁，禁止逐
	// step 重解析 providerhealth）。所有难度偏移都以它为父默认值解析。
	floor         mainAgentRouteSnapshot
	floorSource   string
	floorResolved bool

	// activeDifficulty 为空表示「尚未显式上报」，按 default_difficulty 处理。
	activeDifficulty   string
	dwellSteps         int
	downgradeCandidate string
	downgradeStreak    int
	invalidStreak      int
	disabledForTurn    bool

	stepsTotal        int
	stepsWithOverride int
	finalDifficulty   string

	// lastBoundaryStep 记录最近一次已记账的 step 号。meta-only 上报会让同一 step
	// 号被重新进入（§5.4 R2 的 step 回退），驻留与昂贵计数必须按「不同 step」而不是
	// 「不同迭代」计，否则一次上报会凭空多算一格驻留/昂贵步。
	lastBoundaryStep int

	consecutiveExpensiveSteps int
	costGuardTrips            int
	// hardGuardTripped 只在 cost_guard_mode=hard 时置位：本 turn 内不再接受升级
	// 类上报（降级仍然受理）。
	hardGuardTripped bool

	lastApplied mainAgentRouteSnapshot
	applied     bool
}

// mainAgentRouteConfig 返回生效的主 Agent 路由配置；未启用时返回 nil，调用方据此
// 走零开销的惰性路径。
func (loop *ReActLoop) mainAgentRouteConfig() *agentconfig.AICLIMainAgentRoutingConfig {
	if loop == nil || loop.config == nil {
		return nil
	}
	cfg := loop.config.MainAgentRouting
	if cfg == nil || !cfg.Enabled {
		return nil
	}
	return cfg
}

// mainAgentRoutingToolDefinition 返回本 turn 是否应暴露难度上报工具。
func (loop *ReActLoop) mainAgentRoutingToolDefinition() (types.ToolDefinition, bool) {
	cfg := loop.mainAgentRouteConfig()
	if cfg == nil {
		return types.ToolDefinition{}, false
	}
	return predictTaskDifficultyToolDefinition(cfg), true
}

// beginTurnRoute 在 run() 入口解析并冻结 turn 基线（§5.2 + §5.10 规则 1）。
func (loop *ReActLoop) beginTurnRoute(sessionID, traceID string) {
	if loop == nil || loop.config == nil {
		return
	}
	st := &loop.mainAgentRoute
	*st = mainAgentRouteState{}
	st.baseline = mainAgentRouteSnapshotOf(loop.config)
	st.baselineCaptured = true
	// 必须在 st.floor.applyTo(loop.config) 之前捕获：applyTo 会覆写 loop.config，
	// 之后回退链拿到的就是本 turn 的 route，而不是基线了。
	st.baselineEffective = loop.mainAgentRouteEffectiveBaseline()
	st.floor = st.baseline
	st.floorSource = mainAgentRouteSourceBaseline
	st.floorResolved = true

	cfg := loop.mainAgentRouteConfig()
	if cfg == nil {
		return
	}

	// 基线档位也要过一次解析链：§5.8 要求 difficulty → profiles → Resolver，
	// 这样默认档位上的候选链与健康门禁同样生效（source=failover_candidate）。
	difficulty := mainAgentDefaultDifficulty(cfg)
	// consultHealth=true：这是 §5.10 规则 1 允许查询健康的**唯一**位置。
	decision, err := loop.resolveMainAgentRoute(cfg, st.baseline, difficulty, "", true)
	switch {
	case err != nil:
		// 配置缺陷导致的链耗尽：大声报错但保持基线，且**不**禁用本 turn（§5.8）。
		loop.emitRuntimeEvent(events.EventMainAgentRoutePredictionUnresolvable, sessionID, "", map[string]interface{}{
			"trace_id":   traceID,
			"step":       0,
			"difficulty": difficulty,
			"reason":     "turn_floor_unresolvable",
			"error":      err.Error(),
		})
	case decision.FallbackUsed:
		st.floor = st.baseline
		st.floorSource = mainAgentRouteSourceHealthExhaustedBaseline
	case decision.Source == modelrouting.SourceFailoverCandidate:
		st.floor = mainAgentRouteSnapshotFromDecision(decision, st.baseline)
		st.floorSource = mainAgentRouteSourceFailoverCandidate
	default:
		st.floor = mainAgentRouteSnapshotFromDecision(decision, st.baseline)
	}

	// §6.2：candidates 是「为什么没选某个候选」的唯一证据来源。字段必须存在
	// （空切片序列化成 []），否则下游 usage_routes.candidates_json 恒为空列。
	candidates := []modelrouting.RouteCandidateEvaluation{}
	if err == nil && len(decision.Candidates) > 0 {
		candidates = decision.Candidates
	}
	// 同理：告警是「为什么没按首选路由」的证据，字段必须存在。
	routeWarnings := []string{}
	if err == nil {
		routeWarnings = mainAgentRouteWarnings(decision)
	}

	st.lastApplied = st.floor
	st.applied = true
	st.finalDifficulty = difficulty
	st.floor.applyTo(loop.config)
	loop.emitRuntimeEvent(events.EventMainAgentRouteApplied, sessionID, "", map[string]interface{}{
		"trace_id":         traceID,
		"step":             0,
		"reason":           "turn_floor",
		"source":           st.floorSource,
		"difficulty":       difficulty,
		"provider":         st.floor.Provider,
		"model":            st.floor.Model,
		"reasoning_effort": st.floor.ReasoningEffort,
		"route_changed":    mainAgentRouteChanged(st.baselineEffective, st.floor),
		// §6.2 契约字段：baseline_* 取用户 /model 的显式值（= 请求路径实际会用的
		// 基线），而不是 default_difficulty 的映射结果。
		"baseline_provider": st.baselineEffective.Provider,
		"baseline_model":    st.baselineEffective.Model,
		// turn floor 不由模型上报驱动，没有 rationale 可报；字段仍必须存在，
		// 下游才能统一解析（与 §6.2「token 允许为 0，但字段必须存在」同理）。
		"rationale":      "",
		"candidates":     candidates,
		"route_warnings": routeWarnings,
		"input_tokens":   0,
		"output_tokens":  0,
	})
}

// endTurnRoute 写回 turn 基线并清空状态（§5.2：由 run() 的 defer 保证执行）。
func (loop *ReActLoop) endTurnRoute(sessionID, traceID string) {
	if loop == nil || loop.config == nil {
		return
	}
	st := &loop.mainAgentRoute
	if !st.baselineCaptured {
		return
	}
	// 无论本 turn 是否改道过，都无条件写回：这是 MG3（route 泄漏）的唯一防线。
	st.baseline.applyTo(loop.config)

	if cfg := loop.mainAgentRouteConfig(); cfg != nil {
		finalDifficulty := st.finalDifficulty
		if finalDifficulty == "" {
			finalDifficulty = mainAgentDefaultDifficulty(cfg)
		}
		loop.emitRuntimeEvent(events.EventMainAgentRouteCleared, sessionID, "", map[string]interface{}{
			"trace_id":            traceID,
			"steps_total":         st.stepsTotal,
			"steps_with_override": st.stepsWithOverride,
			"final_difficulty":    finalDifficulty,
			"cost_guard_trips":    st.costGuardTrips,
			"disabled_for_turn":   st.disabledForTurn,
			// §6.2：这两个字段存在的唯一目的是证明「确实还原到了正确基线」。
			// 必须报**请求路径实际会用的**基线（含 agent.config 回退），否则在
			// 会话路径下（loop.config 的 provider/model 恒为空）永远是空串。
			"restored_provider": st.baselineEffective.Provider,
			"restored_model":    st.baselineEffective.Model,
			"restored_effort":   st.baselineEffective.ReasoningEffort,
			"cost_guard_mode":   cfg.CostGuardMode,
		})
	}
	*st = mainAgentRouteState{}
}

// onStepBoundary 在每次 think() 之前执行：推进驻留计数并执行成本护栏（§5.7）。
//
// 这里**不**解析 route：step 边界只做记账与回退，route 变更一律来自难度上报。
func (loop *ReActLoop) onStepBoundary(sessionID, traceID string, step int) {
	cfg := loop.mainAgentRouteConfig()
	if cfg == nil {
		return
	}
	st := &loop.mainAgentRoute
	// meta-only 上报会让同一个 step 号被重新进入（§5.4 R2 的 step 回退）：驻留与
	// 昂贵计数只按「不同 step」计一次，否则一次上报会凭空多算一格驻留/昂贵步。
	if st.lastBoundaryStep == step {
		return
	}
	st.lastBoundaryStep = step
	st.stepsTotal = step
	// §6.2：steps_with_override 与 steps_total 必须同口径（逐步计数）。判据是
	// 「本 step 实际生效的 route 是否偏离 turn 基线」——turn floor 与成本护栏同样
	// 会改道，只在难度上报路径计数会让 turn floor 改道（default_difficulty 映射到
	// ≠ 用户基线的档位时是常态）被系统性少报为 0。
	if mainAgentRouteChanged(st.baselineEffective, st.lastApplied) {
		st.stepsWithOverride++
	}
	if st.dwellSteps < 0 {
		st.dwellSteps = 0
	}
	st.dwellSteps++

	level := st.activeDifficulty
	if level == "" {
		level = mainAgentDefaultDifficulty(cfg)
	}
	st.finalDifficulty = level

	if mainAgentLevelInList(cfg.ExpensiveLevels, level) {
		st.consecutiveExpensiveSteps++
	} else {
		st.consecutiveExpensiveSteps = 0
	}
	// 边沿触发（== 而非 >=）：还原基线后允许再次升级，计数从新的一轮重新累加，
	// 因此同一次 turn 内可以多次触发护栏（§5.7「还原后允许再升级」）。
	limit := cfg.MaxConsecutiveExpensiveSteps
	if limit > 0 && st.consecutiveExpensiveSteps == limit {
		loop.tripMainAgentCostGuard(cfg, st, level, step, sessionID, traceID)
	}
}

// tripMainAgentCostGuard 把 route 还原到 turn 基线并对外通告（§5.7）。
func (loop *ReActLoop) tripMainAgentCostGuard(cfg *agentconfig.AICLIMainAgentRoutingConfig, st *mainAgentRouteState, level string, step int, sessionID, traceID string) {
	st.costGuardTrips++
	trippedAt := st.consecutiveExpensiveSteps
	// 计数清零而不是保留：护栏还原基线后允许再次升级，重新计数才能让下一次
	// 「连续 N step 昂贵」再次触发（否则计数越过阈值就永远不再命中边沿）。
	st.consecutiveExpensiveSteps = 0
	st.activeDifficulty = mainAgentDefaultDifficulty(cfg)
	st.dwellSteps = 0
	st.downgradeCandidate = ""
	st.downgradeStreak = 0
	if strings.EqualFold(strings.TrimSpace(cfg.CostGuardMode), agentconfig.MainAgentCostGuardModeHard) {
		st.hardGuardTripped = true
	}
	st.lastApplied = st.floor
	st.applied = true
	st.floor.applyTo(loop.config)

	loop.emitRuntimeEvent(events.EventMainAgentRouteCostGuardTripped, sessionID, "", map[string]interface{}{
		"trace_id":                    traceID,
		"step":                        step,
		"level":                       level,
		"mode":                        cfg.CostGuardMode,
		"limit":                       cfg.MaxConsecutiveExpensiveSteps,
		"consecutive_expensive_steps": trippedAt,
		"trips":                       st.costGuardTrips,
	})
	loop.emitRuntimeEvent(events.EventMainAgentRouteApplied, sessionID, "", map[string]interface{}{
		"trace_id":         traceID,
		"step":             step,
		"reason":           "cost_guard",
		"source":           mainAgentRouteSourceBaseline,
		"difficulty":       st.activeDifficulty,
		"provider":         st.floor.Provider,
		"model":            st.floor.Model,
		"reasoning_effort": st.floor.ReasoningEffort,
		// 护栏还原的是 turn floor，不一定是用户基线：floor 本身可能已偏离基线
		// （default_difficulty 映射到别的档位），因此必须实算而不是恒 true。
		"route_changed":     mainAgentRouteChanged(st.baselineEffective, st.floor),
		"baseline_provider": st.baselineEffective.Provider,
		"baseline_model":    st.baselineEffective.Model,
		// 护栏由运行时触发，没有模型上报的 rationale；字段仍必须存在（§6.2）。
		"rationale":  "",
		"candidates": []modelrouting.RouteCandidateEvaluation{},
		// 护栏是运行时动作、不经过 Resolver，因此没有决策告警可报；字段仍必须
		// 存在（空切片序列化成 []），下游 usage_routes.warnings_json 才能统一解析。
		"route_warnings": []string{},
		"input_tokens":   0,
		"output_tokens":  0,
	})
}

// resolveMainAgentRoute 走 §5.8 的解析链：difficulty → profiles[difficulty]（仅
// delta）→ modelrouting.Resolver。未填字段由 Resolver 沿用父默认值（= turn floor）。
//
// consultHealth 是 §5.10 规则 1 的**唯一开关**：只有 turn 入口（beginTurnRoute）
// 允许传 true。turn 内的难度上报一律传 false——难度可以逐 step 变化，健康维度
// 必须在 turn 内冻结，否则「探测失败 ⇒ 立即改道 ⇒ 探测永远无法成功」（MG5）。
//
// 这里构造的 adapter 是**一次性的**：主 Agent 的 profiles 复用子 Agent 的类型，
// 但绝不共享配置实例，因此主 Agent 的开关与候选链不会渗进 aicli.subagents.routing。
func (loop *ReActLoop) resolveMainAgentRoute(cfg *agentconfig.AICLIMainAgentRoutingConfig, parent mainAgentRouteSnapshot, difficulty, rationale string, consultHealth bool) (modelrouting.RouteDecision, error) {
	enabled := true
	failover := true
	// 有运行时目录时才做模型能力校验：没有目录可查时「校验」只会把配置判死，
	// 而主 Agent 的 route 必须永远能回落到基线。
	validateCapabilities := loop.llmRuntime != nil
	adapter := &agentconfig.AICLISubagentRoutingConfig{
		Enabled:                   &enabled,
		CompatibilityMode:         modelrouting.CompatibilityPermissive,
		DefaultDifficulty:         difficulty,
		Failover:                  &failover,
		AvailabilityPolicy:        modelrouting.AvailabilityPolicySkip,
		ValidateModelCapabilities: &validateCapabilities,
		// MaxExpertConcurrency 只约束子 Agent 调度器；主 Agent 不存在并发专家
		// 槽位，取有限值 1 既满足校验语义，又不会把「不限」这种危险语义带进来。
		MaxExpertConcurrency: 1,
		Levels:               mainAgentRouteProfiles(cfg, parent.Provider),
	}
	resolver := modelrouting.Resolver{Config: adapter}
	if loop.llmRuntime != nil {
		resolver.Catalog = modelrouting.NewRuntimeCatalog(loop.llmRuntime)
	}
	if consultHealth {
		if health := mainAgentRouteHealthSource(cfg); health != nil {
			resolver.Health = health
		}
	}
	return resolver.Resolve(modelrouting.ParentDefaults{
		Provider:        parent.Provider,
		Model:           parent.Model,
		ReasoningEffort: parent.ReasoningEffort,
	}, modelrouting.TaskHint{
		Role:                "main",
		Difficulty:          difficulty,
		DifficultyRationale: rationale,
	})
}

// mainAgentRouteHealthSource 返回 turn 入口使用的动态健康源；未开启健康门禁时返回
// nil（不注册健康源 ⇒ Resolver 走无健康门禁的纯配置路径）。
//
// 这是全文件**唯一**引用 providerhealth 的位置，且只被 resolveMainAgentRoute 在
// consultHealth=true 时调用。测试 U15 用机械检查钉住这一点：一旦出现第二处调用
// 或第二处 providerhealth 引用，turn 内闩锁即被破坏，MG5 会立刻复发。
func mainAgentRouteHealthSource(cfg *agentconfig.AICLIMainAgentRoutingConfig) *providerhealth.Registry {
	if cfg == nil || !cfg.HealthGate.RespectProviderHealth {
		return nil
	}
	// 动态健康源与记录 LLM 成败的注册表是同一份：读到的是真实上游表现。
	return providerhealth.Default()
}

// mainAgentRouteProfiles 把配置里的档位映射转成 Resolver 需要的 map，并把省略了
// provider 的档位补成父默认 provider。
//
// 为什么必须补：Resolver 的候选链在 profile 层**不做父级继承**，健康门禁是按
// (provider, model) 查表的（`candidates.go` 的 `candidateHealthGate`）。档位只写
// model（本方案 §11 建议的「同 provider 换 model」默认取向）时，查表键会退化成
// ("", model) 而永远查不到观测记录 ⇒ 健康门禁形同虚设、turn 入口的冻结也就失去
// 意义。补成父 provider 后查表键与真实观测记录一致。
//
// 该补齐不改变既有语义：补齐值恒等于 decision 在 Resolve 开头继承到的父 provider，
// 因此 `applyRouteProfile` 里的 provider 覆写与 model 清空分支都不会被触发。
func mainAgentRouteProfiles(cfg *agentconfig.AICLIMainAgentRoutingConfig, parentProvider string) map[string]agentconfig.AICLISubagentRouteProfile {
	if cfg == nil || len(cfg.Profiles) == 0 {
		return nil
	}
	parentProvider = strings.TrimSpace(parentProvider)
	out := make(map[string]agentconfig.AICLISubagentRouteProfile, len(cfg.Profiles))
	for raw, profile := range cfg.Profiles {
		level, ok := modelrouting.NormalizeDifficulty(raw)
		if !ok {
			continue
		}
		if strings.TrimSpace(profile.Provider) == "" {
			profile.Provider = parentProvider
		}
		out[level] = profile
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func mainAgentRouteSnapshotFromDecision(decision modelrouting.RouteDecision, fallback mainAgentRouteSnapshot) mainAgentRouteSnapshot {
	out := mainAgentRouteSnapshot{
		Provider:        strings.TrimSpace(decision.Provider),
		Model:           strings.TrimSpace(decision.Model),
		ReasoningEffort: strings.TrimSpace(decision.ReasoningEffort),
	}
	// 空 provider/model 会静默清空宿主配置，必须回落到父默认值。
	if out.Provider == "" {
		out.Provider = fallback.Provider
	}
	if out.Model == "" {
		out.Model = fallback.Model
	}
	return out
}

// applyPredictedDifficulty 执行 §5.4/§5.5 的状态机并落地 route。
//
// 生效时机：工具在 step N 的 act 阶段被调用，而 step N 的请求已经发出，因此
// 复写 loop.config 天然只影响 step N+1（R5）。
func (loop *ReActLoop) applyPredictedDifficulty(cfg *agentconfig.AICLIMainAgentRoutingConfig, difficulty, rationale string, step int, sessionID, traceID string) string {
	st := &loop.mainAgentRoute
	placeholder := predictTaskDifficultyPlaceholder(difficulty)
	if st.disabledForTurn {
		return placeholder
	}

	current := st.activeDifficulty
	if current == "" {
		current = mainAgentDefaultDifficulty(cfg)
	}
	currentRank, _ := mainAgentDifficultyRank(current)
	targetRank, _ := mainAgentDifficultyRank(difficulty)

	switch {
	case targetRank == currentRank:
		// 与当前档位一致：无需改道，也不消耗任何预算。
		return placeholder
	case targetRank < currentRank:
		// 降级更难（§5.5）：连续 N step 上报同一档位才生效，且必须已驻留
		// min_dwell_steps。两条迟滞是「与」关系，任一不满足都只记候选。
		if st.downgradeCandidate == difficulty {
			st.downgradeStreak++
		} else {
			st.downgradeCandidate = difficulty
			st.downgradeStreak = 1
		}
		confirm := cfg.DowngradeConfirmSteps
		if confirm < 1 {
			confirm = 1
		}
		if st.downgradeStreak < confirm || st.dwellSteps < cfg.MinDwellSteps {
			return placeholder
		}
	default:
		// 升级立即生效，但 hard 护栏触发后本 turn 内不再升级（降级仍受理）。
		if st.hardGuardTripped {
			return placeholder
		}
	}

	// consultHealth=false：难度可以逐 step 变，健康维度必须保持 turn 内冻结（§5.10）。
	decision, err := loop.resolveMainAgentRoute(cfg, st.floor, difficulty, rationale, false)
	if err != nil {
		// 配置缺陷：保持当前 route，且不禁用本 turn（§5.8 与 health 耗尽区分开）。
		loop.emitRuntimeEvent(events.EventMainAgentRoutePredictionUnresolvable, sessionID, predictTaskDifficultyToolName, map[string]interface{}{
			"trace_id":   traceID,
			"step":       step,
			"difficulty": difficulty,
			"reason":     "resolve_failed",
			"error":      err.Error(),
		})
		return placeholder
	}

	target := mainAgentRouteSnapshotFromDecision(decision, st.floor)
	source := mainAgentRouteSourcePredicted
	switch {
	case decision.FallbackUsed:
		// 候选链被健康门禁耗尽：主 Agent 无父可退，只能回到 turn 基线。
		target = st.floor
		source = mainAgentRouteSourceHealthExhaustedBaseline
	case decision.Source == modelrouting.SourceFailoverCandidate:
		source = mainAgentRouteSourceFailoverCandidate
	}

	st.activeDifficulty = difficulty
	st.dwellSteps = 0
	st.downgradeCandidate = ""
	st.downgradeStreak = 0
	// steps_with_override 不在这里自增：它按 step 计数（与 steps_total 同口径），
	// 由 onStepBoundary 依据「本 step 生效的 route 是否偏离基线」统一记账。
	// 在这里按「上报次数」计数会让 turn floor / 成本护栏的改道漏计（缺陷 A）。
	st.lastApplied = target
	st.applied = true
	target.applyTo(loop.config)

	payload := map[string]interface{}{
		"trace_id":             traceID,
		"step":                 step,
		"reason":               "prediction",
		"source":               source,
		"difficulty":           difficulty,
		"difficulty_rationale": rationale,
		"rationale":            rationale,
		"provider":             target.Provider,
		"model":                target.Model,
		"reasoning_effort":     target.ReasoningEffort,
		"effective_step":       step + 1,
		"route_changed":        mainAgentRouteChanged(st.baselineEffective, target),
		"baseline_provider":    st.baselineEffective.Provider,
		"baseline_model":       st.baselineEffective.Model,
		"input_tokens":         0,
		"output_tokens":        0,
	}
	// §5.10 规则 3：候选评估结论必须进 payload，运维才能看出主选为何被跳过。
	// 字段必须存在（空切片序列化成 []），下游 usage_routes.candidates_json 才能
	// 统一解析，而不是靠「有没有这个键」分支。
	candidates := decision.Candidates
	if candidates == nil {
		candidates = []modelrouting.RouteCandidateEvaluation{}
	}
	payload["candidates"] = candidates
	// 同 §5.10 规则 3 的告警口径：升档/降级/回退的证据必须进 payload，
	// 否则「为什么被升档」在审计里无法自解释（install.md 的提升证据链承诺）。
	payload["route_warnings"] = mainAgentRouteWarnings(decision)
	loop.emitRuntimeEvent(events.EventMainAgentRouteApplied, sessionID, predictTaskDifficultyToolName, payload)
	return placeholder
}

// reportPredictedDifficulty 校验难度上报并返回给模型的占位结果（§5.4 R1）。
func (loop *ReActLoop) reportPredictedDifficulty(sessionID, traceID string, step int, args map[string]interface{}) (string, error) {
	cfg := loop.mainAgentRouteConfig()
	if cfg == nil {
		return "", fmt.Errorf("%s is unavailable: main-agent routing is disabled", predictTaskDifficultyToolName)
	}
	st := &loop.mainAgentRoute
	raw, _ := args["difficulty"].(string)
	reported := strings.TrimSpace(raw)
	difficulty, ok := modelrouting.NormalizeDifficulty(reported)

	switch {
	case !ok:
		return "", loop.rejectPredictedDifficulty(cfg, st, sessionID, traceID, step, reported, "unknown_difficulty")
	case !mainAgentLevelInList(cfg.Levels, difficulty):
		return "", loop.rejectPredictedDifficulty(cfg, st, sessionID, traceID, step, reported, "difficulty_not_allowed")
	case difficulty == modelrouting.DifficultyExpert && !cfg.AllowExpert:
		// §5.9：expert 是显式 opt-in，未开启时按非法上报计数（而不是静默降级）。
		return "", loop.rejectPredictedDifficulty(cfg, st, sessionID, traceID, step, reported, "expert_not_allowed")
	}

	st.invalidStreak = 0
	rationale, _ := args["rationale"].(string)
	return loop.applyPredictedDifficulty(cfg, difficulty, strings.TrimSpace(rationale), step, sessionID, traceID), nil
}

// rejectPredictedDifficulty 记录一次非法上报，并在连续非法达到上限时禁用本 turn
// 的改道能力（§6.2）。返回给模型的错误文本只描述「值不被接受」，不披露后端。
func (loop *ReActLoop) rejectPredictedDifficulty(cfg *agentconfig.AICLIMainAgentRoutingConfig, st *mainAgentRouteState, sessionID, traceID string, step int, reported, reason string) error {
	st.invalidStreak++
	loop.emitRuntimeEvent(events.EventMainAgentRoutePredictionInvalid, sessionID, predictTaskDifficultyToolName, map[string]interface{}{
		"trace_id":       traceID,
		"step":           step,
		"reported":       reported,
		"reason":         reason,
		"invalid_streak": st.invalidStreak,
	})
	limit := cfg.MaxInvalidReportsPerTurn
	if limit > 0 && st.invalidStreak >= limit && !st.disabledForTurn {
		st.disabledForTurn = true
		loop.emitRuntimeEvent(events.EventMainAgentRouteDisabledForTurn, sessionID, "", map[string]interface{}{
			"trace_id":       traceID,
			"step":           step,
			"invalid_streak": st.invalidStreak,
			"limit":          limit,
		})
	}
	return fmt.Errorf("difficulty %q was rejected (%s); allowed values: %s", reported, reason, strings.Join(mainAgentAllowedLevels(cfg), ", "))
}

// predictTaskDifficultyToolDefinition 构造难度上报工具的 schema（§5.4）。
//
// 描述文本刻意不披露因果（禁止「不调用它就不会改道」「影响下一步的模型」这类
// 句子），只保留治理边界与反博弈条款；同时不得出现 provider/model（INV-3）。
func predictTaskDifficultyToolDefinition(cfg *agentconfig.AICLIMainAgentRoutingConfig) types.ToolDefinition {
	levels := mainAgentAllowedLevels(cfg)
	return types.ToolDefinition{
		Name: predictTaskDifficultyToolName,
		Description: "Report your own estimate of the difficulty of the work in front of you as a routing hint for the local runtime. " +
			"Call it only when the estimate is worth recording; it is a hint, not an action, and it never replaces the real tool calls the task still requires. " +
			"Reporting does not consume a step and does not change permissions, approvals, or the tool surface. " +
			"Rate the work itself: do not inflate it to obtain a stronger setup, and do not deflate it to save budget. " +
			"Values outside the allowed set are rejected and repeated rejections disable reporting for the rest of the turn.",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"difficulty": map[string]interface{}{
					"type":        "string",
					"enum":        levels,
					"description": "Estimated difficulty of the current work.",
				},
				"rationale": map[string]interface{}{
					"type":        "string",
					"description": "Optional one-line reason for the estimate.",
				},
			},
			"required": []string{"difficulty"},
		},
	}
}

// mainAgentRoutingSystemFragment 返回主 Agent 路由的回合级引导片段（§5.4 / §7.3）。
//
// 未启用（cfg=nil）时返回空串：注入点不产生任何字节，保证「关闭 = 零行为变化」。
// 片段只依赖冻结后的 loop 配置，档位取自 mainAgentAllowedLevels（已排序），因此
// turn 内逐字节稳定（INV-2）；文本不出现 provider/model（INV-3），也不披露
// 「不调用就不改道」这类因果，避免把上报变成博弈目标。
func mainAgentRoutingSystemFragment(cfg *agentconfig.AICLIMainAgentRoutingConfig) string {
	if cfg == nil {
		return ""
	}
	levels := mainAgentAllowedLevels(cfg)
	if len(levels) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Task difficulty routing is active for this session. ")
	b.WriteString("Use the `")
	b.WriteString(predictTaskDifficultyToolName)
	b.WriteString("` tool to record your own estimate of the difficulty of the work in front of you when it changes; an unchanged estimate does not need to be re-reported. ")
	b.WriteString("Allowed levels: ")
	b.WriteString(strings.Join(levels, ", "))
	b.WriteString(". Default level when you do not report: ")
	b.WriteString(mainAgentDefaultDifficulty(cfg))
	b.WriteString(". It is a hint, not an action: rate the work itself, and do not inflate or deflate the estimate.")
	return b.String()
}

// mainAgentRoutingSystemMessage 构造回合级 system 注入消息（§5.4）。
//
// 走统一的 system-reminder 通道并显式标记 Durable=false：只进入本次 run 的请求
// 历史，落盘前被 DurableMessagesForPersist 剥掉。因此它既不改写任何已发送消息
// （INV-1），也不会在后续 turn 里累积出重复片段。
func mainAgentRoutingSystemMessage(cfg *agentconfig.AICLIMainAgentRoutingConfig) *types.Message {
	body := mainAgentRoutingSystemFragment(cfg)
	if body == "" {
		return nil
	}
	msg := types.NewSystemMessage(FormatSystemReminder(ReminderKindMainAgentRouting, body))
	if msg == nil {
		return nil
	}
	if msg.Metadata == nil {
		msg.Metadata = types.NewMetadata()
	}
	msg.Metadata[MetaSystemReminder] = true
	msg.Metadata[MetaEphemeralInstruction] = true
	msg.Metadata[MetaSystemReminderKind] = ReminderKindMainAgentRouting
	msg.Metadata[MetaSystemReminderDurable] = false
	return msg
}

// mainAgentDefaultDifficulty 返回归一化后的默认档位，缺失时回落到 normal。
func mainAgentDefaultDifficulty(cfg *agentconfig.AICLIMainAgentRoutingConfig) string {
	if cfg == nil {
		return modelrouting.DifficultyNormal
	}
	if level, ok := modelrouting.NormalizeDifficulty(cfg.DefaultDifficulty); ok {
		return level
	}
	return modelrouting.DifficultyNormal
}

// mainAgentAllowedLevels 返回排序后的可用档位（用于 schema enum 与错误提示）。
// 排序保证同一份配置生成的 schema 逐字节稳定（INV-2）。
func mainAgentAllowedLevels(cfg *agentconfig.AICLIMainAgentRoutingConfig) []string {
	if cfg == nil {
		return nil
	}
	levels := make([]string, 0, len(cfg.Levels))
	for _, raw := range cfg.Levels {
		level, ok := modelrouting.NormalizeDifficulty(raw)
		if !ok {
			continue
		}
		levels = append(levels, level)
	}
	sort.Strings(levels)
	return levels
}

func mainAgentLevelInList(list []string, level string) bool {
	if level == "" {
		return false
	}
	for _, raw := range list {
		if candidate, ok := modelrouting.NormalizeDifficulty(raw); ok && candidate == level {
			return true
		}
	}
	return false
}

// mainAgentDifficultyRank 给出档位的严格序（用于判断升级/降级）。未知档位返回
// false，调用方据此拒绝改道，而不是猜一个顺序。
func mainAgentDifficultyRank(level string) (int, bool) {
	switch level {
	case modelrouting.DifficultyEasy:
		return 0, true
	case modelrouting.DifficultyNormal:
		return 1, true
	case modelrouting.DifficultyHard:
		return 2, true
	case modelrouting.DifficultyExpert:
		return 3, true
	default:
		return 0, false
	}
}

// cloneMainAgentRoutingConfig 深拷贝主 Agent 路由配置（§5.2）。NewReActLoop 只做
// LoopReActConfig 的值拷贝，指针字段仍与宿主共享，因此必须显式克隆 map/slice：
// 否则运行期读取 profiles 会与宿主配置互相影响，route 也可能写回宿主持有的对象。
func cloneMainAgentRoutingConfig(cfg *agentconfig.AICLIMainAgentRoutingConfig) *agentconfig.AICLIMainAgentRoutingConfig {
	if cfg == nil {
		return nil
	}
	out := *cfg
	out.Levels = append([]string(nil), cfg.Levels...)
	out.ExpensiveLevels = append([]string(nil), cfg.ExpensiveLevels...)
	if len(cfg.Profiles) > 0 {
		out.Profiles = make(map[string]agentconfig.AICLISubagentRouteProfile, len(cfg.Profiles))
		for key, profile := range cfg.Profiles {
			out.Profiles[key] = cloneMainAgentRouteProfile(profile)
		}
	} else {
		out.Profiles = nil
	}
	return &out
}

func cloneMainAgentRouteProfile(profile agentconfig.AICLISubagentRouteProfile) agentconfig.AICLISubagentRouteProfile {
	out := profile
	out.Candidates = append([]agentconfig.AICLISubagentRouteCandidate(nil), profile.Candidates...)
	if profile.Temperature != nil {
		value := *profile.Temperature
		out.Temperature = &value
	}
	if profile.PromptCache != nil {
		value := *profile.PromptCache
		out.PromptCache = &value
	}
	return out
}
