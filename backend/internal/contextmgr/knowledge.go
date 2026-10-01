package contextmgr

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/wwsheng009/ai-agent-runtime/internal/knowledge"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// 知识层注入（06 §4 Phase 2 W5 起步；Phase 6 切片 3 起统一走 Context Compiler）。
//
// 口径：
//   - KnowledgeMode = off | signals | broad，与 WorkspaceMode/RecallMode 对称；
//     off（含空值 / 未知值，fail closed）零调用 Planner、零注入，Build 结果与
//     改动前逐字节一致（G4 可逆主承载）。
//   - 只注入 Planner 的 Reuse 项；Explore 只计数不注入（探索由调用方执行）。
//   - **Phase 6 切片 3**：注入前统一走 `knowledge.CompilePlan`（信任等级 /
//     来源冲突 / stale / 置信度下限 / token 预算 / 可解释性字段），渲染统一为
//     data block（03 §14.5 规则 2/4）；stale 条目由编译器丢弃并计数
//     （stale_item_injected 恒为 0 的第三道防线）。
//   - 有缓存（`Manager.KnowledgeCache`）且 Planner 可提供版本观测
//     （`*knowledge.Layer`）时走 compile 层缓存：键含知识版本与编译器版本，
//     命中不重编译；缓存故障降级直算（切片 2 语义）。
//   - 条目按 tier 映射 hot/warm/cold（04 §5 Phase 6 交付 2）：高置信直接复用 →
//     hot；Verify/Provisional → warm；未注入（stale/低置信/超预算/被覆盖）→
//     cold（只留在可解释性记录里）。
//   - Verify=true / Provisional 条目照常注入，但在 metadata 标 reason/tier，并把
//     验证读取目标写入 knowledge_verify_targets，由调用方（agent 循环）用
//     既有 grep / view 完成一次低成本验证；本 Phase 不依赖 code.*。
//   - Plan.Degraded / Planner 错误 → 零注入，回退基线（Degrade-Not-Fail）。
//   - signals 只注入摘要/信号（一个 digest data block）；broad 注入条目 data
//     block，受保守 token 预算截断（由编译器执行，口径见 DefaultKnowledgeTokens）。
const (
	// KnowledgeModeOff 关闭知识层注入（默认）：零调用、零注入、可逆回基线。
	KnowledgeModeOff = "off"
	// KnowledgeModeSignals 只注入复用信号摘要（不注入条目明细）。
	KnowledgeModeSignals = "signals"
	// KnowledgeModeBroad 注入复用条目明细（token 预算内截断）。
	KnowledgeModeBroad = "broad"

	// DefaultKnowledgeTokens 是 broad 档注入的保守 token 预算（header + Σ条目行，
	// 按 1 rune ≈ 1 token 上界估算，见 approxKnowledgeTokens）。
	//
	// W7 校准依据（2026-09-30，有界演练：对 reports/phase1_shadow_calls.jsonl
	// 真实 grep/view 调用 n=385 条回放；入口 contextmgr/knowledge_calibration_test.go
	// + KNOWLEDGE_PHASE2_SHADOW_CALLS）：单条目整条消息成本中位数 420 rune、
	// 95% CI [416,424]、p95 450、max 498；CI 上界按 100 取整的建议预算 = 500。
	// 800 保留 1.6× 余量、容纳 1 条典型条目行（2 条典型行需 ≥ 813）。
	// 该演练是装置自检而非 A/B 收益实测；真实 on-mode 实测跑完后用
	// knowledge.CalibrateTokenBudget 回写（见 reports/phase2_exploration_report.md §校准）。
	DefaultKnowledgeTokens = 800

	// knowledgeStage 是注入消息的 context_stage（与 recall/workspace 同列）。
	knowledgeStage = "knowledge"

	// 稳定 Reason token（供 metadata 与 W7 测量，不随版本漂移）。
	knowledgeReasonPlannerUnavailable = "planner_unavailable"
	knowledgeReasonPlannerError       = "planner_error"
	knowledgeReasonNoReuse            = "no_reuse"
	knowledgeReasonAllFiltered        = "all_reuse_filtered"
	knowledgeReasonSuppressed         = "suppressed_for_active_turn"

	// knowledgeItemType / knowledgeItemSource 是上下文条目的分层口径约定
	// （04 交付 2）：探索记忆区别于 memorystore 的人工长期笔记。
	knowledgeItemType   = "exploration"
	knowledgeItemSource = "memory"
)

// normalizeKnowledgeMode 归一化知识层档位；未知/空值 fail closed 到 off。
func normalizeKnowledgeMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case KnowledgeModeSignals:
		return KnowledgeModeSignals
	case KnowledgeModeBroad:
		return KnowledgeModeBroad
	default:
		return KnowledgeModeOff
	}
}

// KnowledgeModeForLayerMode 把知识层总开关（`knowledge.mode`：off|shadow|on，
// W7 激活切片）映射为上下文注入档位：
//
//   - off / 空 / 未知值 → off（fail closed；装配处对 off 不新增任何 options
//     key，保持"默认 off 与改动前逐字节一致"）；
//   - shadow → off：ModeShadow 的契约是"只构建索引与统计，绝不注入 prompt"，
//     因此 shadow 下即使 Layer 存在也不得注入（shadow 观测/采集由独立钩子接线）；
//   - on → signals：保守默认（只注入复用信号摘要，不注入条目明细），token /
//     延迟风险最低；broad 档留给后续校准切片显式开启。
func KnowledgeModeForLayerMode(mode knowledge.Mode) string {
	if mode == knowledge.ModeOn {
		return KnowledgeModeSignals
	}
	return KnowledgeModeOff
}

// effectiveMinKnowledgeQueryLength 返回最小查询长度：Strategy 未设置（<=0）
// 时退回 W4 Planner 的默认值（不在 contextmgr 复制常量）。
func (m *Manager) effectiveMinKnowledgeQueryLength() int {
	if m != nil && m.Strategy.MinKnowledgeQueryLength > 0 {
		return m.Strategy.MinKnowledgeQueryLength
	}
	return knowledge.DefaultMinKnowledgeQueryLength
}

// knowledgeInjectionStats 记录一次知识层注入的判定与过滤计数。
type knowledgeInjectionStats struct {
	Mode             string
	Attempted        bool
	ReuseCount       int
	ExploreCount     int
	InjectedCount    int
	ProvisionalCount int
	StaleFiltered    int
	FloorFiltered    int
	// Phase 6 切片 3：编译器口径的补充计数与缓存命中。
	BudgetFiltered     int
	OverriddenFiltered int
	CacheHit           bool
	Degraded           bool
	Reason             string
	Error              string
	Items              []map[string]interface{}
	VerifyTargets      []map[string]interface{}
}

// knowledgeTierHotConfidence 是 hot 档阈值：与 Verify 读取阈值同口径（0.90）。
const knowledgeTierHotConfidence = 0.90

// knowledgeVersionObserver 由 *knowledge.Layer 实现：提供当前工作区版本观测
// （含 `#pendingN` 未稳定标记），用于 compile 层缓存键。
type knowledgeVersionObserver interface {
	ObserveVersion(ctx context.Context, ttl time.Duration, now time.Time) (knowledge.VersionObservation, error)
}

// buildKnowledgeMessage 执行一次知识层注入决策；失败/降级一律零注入。
//
// off 由调用方短路，本函数不会在 off 下调用 Planner（Attempted 保持 false）。
func (m *Manager) buildKnowledgeMessage(ctx context.Context, input BuildInput) (*types.Message, knowledgeInjectionStats) {
	stats := knowledgeInjectionStats{Mode: normalizeKnowledgeMode(m.Strategy.KnowledgeMode)}
	if stats.Mode == KnowledgeModeOff {
		stats.Reason = knowledge.PlanReasonDisabled
		return nil, stats
	}
	if m.Knowledge == nil {
		stats.Reason = knowledgeReasonPlannerUnavailable
		return nil, stats
	}
	goal := strings.TrimSpace(input.Goal)
	if goal == "" {
		stats.Reason = knowledge.PlanReasonQueryTooShort
		return nil, stats
	}
	if utf8.RuneCountInString(goal) < m.effectiveMinKnowledgeQueryLength() {
		// 调用前短路：过短查询不触达 Planner。
		stats.Reason = knowledge.PlanReasonQueryTooShort
		return nil, stats
	}

	// 任务锚点与会话回退：agent 循环在无真实任务时用 sessionID 兜底 TaskID
	// （loop.go `taskID := sessionID`），那是会话级工作集而不是任务语义——
	// 按 W1 DTO 口径折叠为 task_id 为空 + SessionID 检索，避免用会话 id 去
	// 匹配从未按该 id 落库的 task_id 行（2026-09-30 真实会话零命中修复）。
	taskID := strings.TrimSpace(input.TaskID)
	sessionID := strings.TrimSpace(input.SessionID)
	if taskID == sessionID {
		taskID = ""
	}
	// 同任务/同会话有锚点时用更宽松的同工作集阈值；否则按跨任务（更保守）。
	scope := knowledge.ReuseScopeCrossTask
	if taskID != "" || sessionID != "" {
		scope = knowledge.ReuseScopeTask
	}
	stats.Attempted = true
	plan, err := m.Knowledge.Plan(ctx, knowledge.PlanInput{
		WorkspaceID:    input.WorkspaceID,
		TaskID:         taskID,
		SessionID:      sessionID,
		Query:          goal,
		Scope:          scope,
		Write:          input.KnowledgeWrite,
		MinQueryLength: m.Strategy.MinKnowledgeQueryLength,
	})
	if err != nil {
		stats.Degraded = true
		stats.Reason = knowledgeReasonPlannerError
		stats.Error = err.Error()
		return nil, stats
	}
	stats.ReuseCount = len(plan.Reuse)
	stats.ExploreCount = len(plan.Explore)
	if plan.Degraded {
		stats.Degraded = true
		stats.Reason = plan.Reason
		return nil, stats
	}

	// Phase 6 切片 3：注入前统一走 Context Compiler（信任/冲突/stale/下限/预算/
	// 可解释性）；signals 档不按预算截断（只注入摘要），broad 档按默认预算截断。
	tokenBudget := 0
	if stats.Mode == KnowledgeModeBroad {
		tokenBudget = DefaultKnowledgeTokens
	}
	compileReq := knowledge.CompileRequest{
		Plan:            plan,
		TokenBudget:     tokenBudget,
		ConfidenceFloor: m.Strategy.ReuseConfidenceFloor,
	}
	compiled := m.compileKnowledgePlan(ctx, input, taskID, scope, compileReq, &stats)
	for _, dropped := range compiled.Dropped {
		switch dropped.DropReason {
		case knowledge.CompileReasonStale:
			stats.StaleFiltered++
		case knowledge.CompileReasonBelowFloor:
			stats.FloorFiltered++
		case knowledge.CompileReasonBudget:
			stats.BudgetFiltered++
		case knowledge.CompileReasonOverridden:
			stats.OverriddenFiltered++
		}
	}
	if len(compiled.Items) == 0 {
		if stats.ReuseCount == 0 {
			stats.Reason = knowledgeReasonNoReuse
		} else {
			stats.Reason = knowledgeReasonAllFiltered
		}
		return nil, stats
	}

	var content string
	if stats.Mode == KnowledgeModeBroad {
		content = knowledgeBroadContent(compiled.Items)
	} else {
		content = knowledgeSignalsContent(compiled.Items)
	}
	if content == "" {
		stats.Reason = knowledgeReasonAllFiltered
		return nil, stats
	}
	stats.InjectedCount = len(compiled.Items)
	stats.Items = make([]map[string]interface{}, 0, len(compiled.Items))
	for _, item := range compiled.Items {
		stats.Items = append(stats.Items, knowledgeItemMetadata(item))
		if item.Verify {
			stats.VerifyTargets = append(stats.VerifyTargets, knowledgeVerifyTarget(item))
		}
		if item.Provisional {
			stats.ProvisionalCount++
		}
	}
	stats.Reason = plan.Reason

	message := types.NewAssistantMessage(content)
	message.Metadata["context_stage"] = knowledgeStage
	message.Metadata["knowledge_mode"] = stats.Mode
	message.Metadata["knowledge_count"] = stats.InjectedCount
	message.Metadata["knowledge_items"] = stats.Items
	if stats.CacheHit {
		message.Metadata["knowledge_cache_hit"] = true
	}
	if stats.Reason != "" {
		message.Metadata["knowledge_reason"] = stats.Reason
	}
	if len(stats.VerifyTargets) > 0 {
		message.Metadata["knowledge_verify_targets"] = stats.VerifyTargets
	}
	return message, stats
}

// compileKnowledgePlan 执行一次编译：有缓存且可观测知识版本时走 compile 层
// 缓存；否则直算（无缓存可用时不计入命中率口径，见 knowledge.CompileCache.Do）。
func (m *Manager) compileKnowledgePlan(ctx context.Context, input BuildInput, taskID string, scope knowledge.ReuseScope, req knowledge.CompileRequest, stats *knowledgeInjectionStats) knowledge.CompileResult {
	if m == nil || m.KnowledgeCache == nil {
		return knowledge.CompilePlan(req)
	}
	version, ok := m.knowledgeVersionToken(ctx)
	if !ok || strings.TrimSpace(input.WorkspaceID) == "" {
		return knowledge.CompilePlan(req)
	}
	key := knowledge.CompileCacheKeyInput{
		WorkspaceID:      input.WorkspaceID,
		TaskID:           taskID,
		SessionID:        strings.TrimSpace(input.SessionID),
		Query:            strings.TrimSpace(input.Goal),
		Scope:            string(scope),
		Write:            input.KnowledgeWrite,
		Mode:             stats.Mode,
		TokenBudget:      req.TokenBudget,
		ConfidenceFloor:  req.ConfidenceFloor,
		KnowledgeVersion: version,
	}
	result := m.KnowledgeCache.Do(ctx, key, req)
	if stats != nil {
		stats.CacheHit = result.CacheHit
	}
	return result
}

// knowledgeVersionToken 通过 Layer 的版本观测取缓存键所需的知识版本 token；
// 不可观测（裸 Planner / 观测失败 / 空版本）时返回 ok=false（无缓存）。
func (m *Manager) knowledgeVersionToken(ctx context.Context) (string, bool) {
	if m == nil || m.Knowledge == nil {
		return "", false
	}
	observer, ok := m.Knowledge.(knowledgeVersionObserver)
	if !ok {
		return "", false
	}
	observation, err := observer.ObserveVersion(ctx, 0, time.Time{})
	if err != nil {
		return "", false
	}
	version := strings.TrimSpace(observation.Version)
	return version, version != ""
}

// applyKnowledgeMetadata 把一次注入判定写入 BuildResult metadata 与层指标。
// stale_item_injected 恒为 0：进入 prompt 的条目在注入前已做二次 stale 过滤。
func applyKnowledgeMetadata(metadata map[string]interface{}, metrics map[string]interface{}, stats knowledgeInjectionStats) {
	if metadata == nil {
		return
	}
	metadata["knowledge_mode"] = stats.Mode
	metadata["knowledge_injected"] = stats.InjectedCount > 0
	metadata["knowledge_count"] = stats.InjectedCount
	metadata["knowledge_reuse_count"] = stats.ReuseCount
	metadata["knowledge_explore_count"] = stats.ExploreCount
	metadata["knowledge_provisional_count"] = stats.ProvisionalCount
	metadata["knowledge_stale_filtered"] = stats.StaleFiltered
	metadata["knowledge_stale_item_injected"] = 0
	metadata["knowledge_floor_filtered"] = stats.FloorFiltered
	metadata["knowledge_budget_filtered"] = stats.BudgetFiltered
	metadata["knowledge_overridden_filtered"] = stats.OverriddenFiltered
	metadata["knowledge_cache_hit"] = stats.CacheHit
	metadata["knowledge_degraded"] = stats.Degraded
	if stats.Reason != "" {
		metadata["knowledge_reason"] = stats.Reason
	}
	if stats.Error != "" {
		metadata["knowledge_error"] = stats.Error
	}
	if len(stats.VerifyTargets) > 0 {
		metadata["knowledge_verify_required"] = true
		metadata["knowledge_verify_targets"] = stats.VerifyTargets
	}
	if metrics == nil {
		return
	}
	metrics["injected"] = stats.InjectedCount > 0
	metrics["count"] = stats.InjectedCount
	metrics["reuse_count"] = stats.ReuseCount
	metrics["explore_count"] = stats.ExploreCount
	metrics["verify_count"] = len(stats.VerifyTargets)
	metrics["provisional_count"] = stats.ProvisionalCount
	metrics["stale_filtered"] = stats.StaleFiltered
	metrics["floor_filtered"] = stats.FloorFiltered
	metrics["budget_filtered"] = stats.BudgetFiltered
	metrics["overridden_filtered"] = stats.OverriddenFiltered
	metrics["cache_hit"] = stats.CacheHit
	metrics["degraded"] = stats.Degraded
}

// knowledgeItemMetadata 生成条目 metadata（与编译条目字段对齐）：source / trust /
// version / reason / tier / tokens 随条目进入 message metadata，供调试、tier
// 统计与可解释性要求（03 §14.4：必须保留 source/confidence/version/explanation）。
func knowledgeItemMetadata(item knowledge.CompiledItem) map[string]interface{} {
	return map[string]interface{}{
		"item_type":         item.ItemType,
		"source":            string(item.Source),
		"node_id":           item.RefID,
		"target":            item.Target,
		"confidence":        item.Confidence,
		"knowledge_version": item.Version,
		"trust":             string(item.Trust),
		"tier":              knowledgeTier(item),
		"tokens":            item.Tokens,
		"verify":            item.Verify,
		"provisional":       item.Provisional,
		"reason":            item.Reason,
	}
}

// knowledgeTier 把编译条目映射到 hot/warm/cold（04 §5 Phase 6 交付 2）。
//
//   - hot：高置信且无需验证的直接复用项（进入下一请求的主承载）；
//   - warm：需验证读取（Verify）/ 暂定（Provisional）/ 置信度未达 hot 阈值；
//   - cold：未注入条目（stale/低置信/超预算/被覆盖），只留在可解释性记录里
//     （由 dropped 计数与 knowledge_*_filtered 元数据承载）。
func knowledgeTier(item knowledge.CompiledItem) string {
	if item.Verify || item.Provisional || item.Confidence < knowledgeTierHotConfidence {
		return "warm"
	}
	return "hot"
}

func knowledgeVerifyTarget(item knowledge.CompiledItem) map[string]interface{} {
	return map[string]interface{}{
		"node_id":           item.RefID,
		"target":            item.Target,
		"confidence":        item.Confidence,
		"knowledge_version": item.Version,
		"reason":            item.Reason,
		"trust":             string(item.Trust),
	}
}

// knowledgeSignalsContent 生成 signals 档摘要：只给信号（数量/验证需求/目标
// 名单），不注入条目明细；整体包裹为一个 data block（03 §14.5 规则 2）。
func knowledgeSignalsContent(items []knowledge.CompiledItem) string {
	targets := make([]string, 0, len(items))
	verifyCount := 0
	provisionalCount := 0
	for _, item := range items {
		if target := strings.TrimSpace(item.Target); target != "" {
			targets = append(targets, target)
		}
		if item.Verify {
			verifyCount++
		}
		if item.Provisional {
			provisionalCount++
		}
	}
	var builder strings.Builder
	fmt.Fprintf(&builder, "Exploration memory signals: %d reusable item(s); verify_required=%d; provisional=%d.", len(items), verifyCount, provisionalCount)
	if len(targets) > 0 {
		builder.WriteString("\nReuse targets: " + strings.Join(limitStrings(targets, 8), ", "))
	}
	return knowledge.RenderDataBlock(knowledge.CompiledItem{
		ItemType: knowledgeItemType,
		Source:   knowledge.SourceClassMemory,
		Trust:    knowledge.TrustCodeIntelligence,
		Reason:   "signals_digest",
		Content:  builder.String(),
	})
}

// knowledgeBroadContent 渲染 broad 档：每条编译条目一个 data block（03 §14.5
// 规则 2/4：来源与版本入块头）。token 预算截断已由 CompilePlan 用同一预算完成，
// 此处只拼接；空集合返回空串（调用方按"全部被过滤"处理）。
func knowledgeBroadContent(items []knowledge.CompiledItem) string {
	blocks := make([]string, 0, len(items))
	for _, item := range items {
		blocks = append(blocks, knowledgeBroadBlock(item))
	}
	return strings.Join(blocks, "\n")
}

// knowledgeBroadBlock 渲染单条 broad 条目：块体给 target/confidence/verify 与
// 摘要（是数据，不是指令），块头给来源/版本/理由（03 §14.5 规则 1/4）。
func knowledgeBroadBlock(item knowledge.CompiledItem) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "target=%s confidence=%.2f verify=%t", item.Target, item.Confidence, item.Verify)
	if item.Provisional {
		builder.WriteString(" provisional=true")
	}
	if summary := summarizeLine(item.Content, 200); summary != "" {
		builder.WriteString("\nsummary: " + summary)
	}
	rendered := item
	rendered.Content = builder.String()
	return knowledge.RenderDataBlock(rendered)
}

// approxKnowledgeTokens 保守估算 token：1 rune ≈ 1 token（CJK 上界，ASCII
// 偏保守）。注入预算只需上界正确，宁少勿多。
func approxKnowledgeTokens(text string) int {
	return maxInt(1, utf8.RuneCountInString(text))
}
