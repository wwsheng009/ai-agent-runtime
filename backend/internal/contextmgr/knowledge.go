package contextmgr

import (
	"context"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/wwsheng009/ai-agent-runtime/internal/knowledge"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// 知识层注入（06 §4 Phase 2 W5；语义见 04 §4.4/§4.5）。
//
// 口径：
//   - KnowledgeMode = off | signals | broad，与 WorkspaceMode/RecallMode 对称；
//     off（含空值 / 未知值，fail closed）零调用 Planner、零注入，Build 结果与
//     改动前逐字节一致（G4 可逆主承载）。
//   - 只注入 Planner 的 Reuse 项；Explore 只计数不注入（探索由调用方执行）。
//   - 进入 prompt 前做二次 stale/version 过滤：knowledge_version 为空、或
//     Reason 为版本不匹配/未知的条目一律丢弃（为 Phase 6 的
//     stale_item_injected=0 留扣；当前恒为 0）。
//   - Verify=true / Provisional 条目照常注入，但在 metadata 标 reason，并把
//     验证读取目标写入 knowledge_verify_targets，由调用方（agent 循环）用
//     既有 grep / view 完成一次低成本验证；本 Phase 不依赖 code.*。
//   - Plan.Degraded / Planner 错误 → 零注入，回退基线（Degrade-Not-Fail）。
//   - signals 只注入摘要/信号；broad 注入条目行，受保守 token 预算截断。
//
// 本 Phase 只做 contextmgr 内注入与 metadata；context_items 的写入路径留给
// Phase 6（item_type=exploration / source=memory 是分层口径约定，不落库）。
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
	Degraded         bool
	Reason           string
	Error            string
	Items            []map[string]interface{}
	VerifyTargets    []map[string]interface{}
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

	floor := m.Strategy.ReuseConfidenceFloor
	kept := make([]knowledge.ReuseItem, 0, len(plan.Reuse))
	for _, item := range plan.Reuse {
		if knowledgeItemStale(item) {
			stats.StaleFiltered++
			continue
		}
		if floor > 0 && (math.IsNaN(item.Confidence) || item.Confidence < floor) {
			stats.FloorFiltered++
			continue
		}
		kept = append(kept, item)
	}
	if len(kept) == 0 {
		if stats.ReuseCount == 0 {
			stats.Reason = knowledgeReasonNoReuse
		} else {
			stats.Reason = knowledgeReasonAllFiltered
		}
		return nil, stats
	}

	var content string
	if stats.Mode == KnowledgeModeBroad {
		content, stats.InjectedCount = knowledgeBroadContent(kept, DefaultKnowledgeTokens)
		if content == "" {
			stats.Reason = knowledgeReasonAllFiltered
			return nil, stats
		}
	} else {
		content = knowledgeSignalsContent(kept)
		stats.InjectedCount = len(kept)
	}

	represented := kept
	if stats.Mode == KnowledgeModeBroad && stats.InjectedCount < len(kept) {
		represented = kept[:stats.InjectedCount]
	}
	stats.Items = make([]map[string]interface{}, 0, len(represented))
	for _, item := range represented {
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
	if stats.Reason != "" {
		message.Metadata["knowledge_reason"] = stats.Reason
	}
	if len(stats.VerifyTargets) > 0 {
		message.Metadata["knowledge_verify_targets"] = stats.VerifyTargets
	}
	return message, stats
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
	metrics["degraded"] = stats.Degraded
}

// knowledgeItemStale 是注入前的二次 stale/version 过滤：无版本或版本不匹配/
// 未知的条目一律不得进入 prompt（04 §4.4；Phase 6 stale_item_injected=0）。
func knowledgeItemStale(item knowledge.ReuseItem) bool {
	if strings.TrimSpace(item.KnowledgeVersion) == "" {
		return true
	}
	switch item.Reason {
	case knowledge.ReuseReasonVersionMismatch, knowledge.ReuseReasonVersionUnknown:
		return true
	default:
		return false
	}
}

func knowledgeItemMetadata(item knowledge.ReuseItem) map[string]interface{} {
	return map[string]interface{}{
		"item_type":         knowledgeItemType,
		"source":            knowledgeItemSource,
		"node_id":           item.NodeID,
		"node_type":         string(item.NodeType),
		"target":            item.Target,
		"confidence":        item.Confidence,
		"knowledge_version": item.KnowledgeVersion,
		"scope":             string(item.Scope),
		"verify":            item.Verify,
		"provisional":       item.Provisional,
		"reason":            item.Reason,
	}
}

func knowledgeVerifyTarget(item knowledge.ReuseItem) map[string]interface{} {
	return map[string]interface{}{
		"node_id":           item.NodeID,
		"target":            item.Target,
		"confidence":        item.Confidence,
		"knowledge_version": item.KnowledgeVersion,
		"reason":            item.Reason,
	}
}

// knowledgeSignalsContent 生成 signals 档摘要：只给信号（数量/验证需求/目标
// 名单），不注入条目明细。
func knowledgeSignalsContent(items []knowledge.ReuseItem) string {
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
	return builder.String()
}

// knowledgeBroadContent 生成 broad 档条目行，并按保守 token 预算截断；
// 返回实际注入条目数。预算放不下第一条时返回空串（零注入优于超预算）。
func knowledgeBroadContent(items []knowledge.ReuseItem, tokenBudget int) (string, int) {
	if tokenBudget <= 0 {
		tokenBudget = DefaultKnowledgeTokens
	}
	header := "Exploration memory reuse:"
	used := approxKnowledgeTokens(header)
	lines := make([]string, 0, len(items)+1)
	lines = append(lines, header)
	injected := 0
	for _, item := range items {
		line := knowledgeBroadLine(item)
		cost := approxKnowledgeTokens(line)
		if used+cost > tokenBudget {
			break
		}
		lines = append(lines, line)
		used += cost
		injected++
	}
	if injected == 0 {
		return "", 0
	}
	return strings.Join(lines, "\n"), injected
}

func knowledgeBroadLine(item knowledge.ReuseItem) string {
	line := fmt.Sprintf("- [%s] target=%s confidence=%.2f version=%s reason=%s verify=%t item_type=%s source=%s",
		knowledgeItemType,
		strings.TrimSpace(item.Target),
		item.Confidence,
		strings.TrimSpace(item.KnowledgeVersion),
		item.Reason,
		item.Verify,
		knowledgeItemType,
		knowledgeItemSource,
	)
	if item.Provisional {
		line += " provisional=true"
	}
	if summary := summarizeLine(item.Summary, 200); summary != "" {
		line += "\n  summary: " + summary
	}
	return line
}

// approxKnowledgeTokens 保守估算 token：1 rune ≈ 1 token（CJK 上界，ASCII
// 偏保守）。注入预算只需上界正确，宁少勿多。
func approxKnowledgeTokens(text string) int {
	return maxInt(1, utf8.RuneCountInString(text))
}
