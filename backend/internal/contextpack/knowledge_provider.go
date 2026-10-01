package contextpack

// knowledge.Provider（只读视图）：06 §4 Phase 6 切片 4；04 §5 Phase 6 文件落点
// `contextpack/knowledge_provider.go`。
//
// 契约：
//   - **只读**：不写库、不记录探索、不触发索引；外部调用只有 Planner.Plan 与
//     编译内核（纯函数）；
//   - **零成本跳过**：Planner 为 nil / Prompt 为空或短于最小查询长度 / Plan
//     降级 / 无注入条目时返回 (nil, nil)——pack 中不出现 knowledge 键，
//     行为与未装配时逐字节一致；
//   - **防注入**：`digest_block` 是 RenderDataBlock 产出的 data block
//     （03 §14.5 规则 2/4）；任何进入 prompt 的路径都取它，而不是裸条目文本；
//   - **结构化字段**（items/dropped/tiers）供程序化消费与审计，不进 prompt；
//   - Planner 错误经 Builder 变成 pack 的 `_warnings`（可见但不致命）。

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/wwsheng009/ai-agent-runtime/internal/knowledge"
)

const (
	// DefaultKnowledgePackBudget 是 provider 侧条目 token 预算的默认值。
	// 与 contextmgr.DefaultKnowledgeTokens（800）同口径：同一批条目无论从
	// 注入路径还是 pack 路径进入，都受同一上界约束。
	DefaultKnowledgePackBudget = 800
	// DefaultKnowledgeDigestBudget 是 digest data block 的 rune 上界（1 rune
	// ≈ 1 token 保守口径）。超界时退化为计数摘要——宁可不给，不给半个 block。
	DefaultKnowledgeDigestBudget = 600
	// DefaultKnowledgeDigestTargets 是 digest 里列出的目标数上限。
	DefaultKnowledgeDigestTargets = 5
)

// KnowledgeProvider 把编译后的知识层视图注入 context pack。
type KnowledgeProvider struct {
	// Planner 是知识层句柄（建议 *knowledge.Layer：补齐 WorkspaceVersion 观测）。
	Planner knowledge.Planner
	// Compile 是编译内核；nil 用 knowledge.CompilePlan。
	Compile knowledge.CompileFunc
	// Budget 是条目 token 预算；<=0 用 DefaultKnowledgePackBudget。
	Budget int
	// Floor 是置信度下限（0 = 只信 Planner 阈值）。
	Floor float64
	// MinQueryLength <=0 时用 knowledge.DefaultMinKnowledgeQueryLength。
	MinQueryLength int
	// DigestBudget 是 digest data block 的 rune 上界；<=0 用默认值。
	DigestBudget int
	// MaxDigestTargets 是 digest 目标数上限；<=0 用默认值。
	MaxDigestTargets int
}

// NewKnowledgeProvider 创建只读知识层 provider。
func NewKnowledgeProvider(planner knowledge.Planner) *KnowledgeProvider {
	return &KnowledgeProvider{Planner: planner}
}

// Name 是 pack 中的键名。
func (p *KnowledgeProvider) Name() string { return "knowledge" }

// Build 产出只读的编译知识视图。
func (p *KnowledgeProvider) Build(ctx context.Context, input *Input) (map[string]interface{}, error) {
	if p == nil || p.Planner == nil || input == nil {
		return nil, nil
	}
	query := strings.TrimSpace(input.Prompt)
	minLength := p.minQueryLength()
	if query == "" || utf8.RuneCountInString(query) < minLength {
		return nil, nil
	}
	plan, err := p.Planner.Plan(ctx, knowledge.PlanInput{
		TaskID:         strings.TrimSpace(input.TaskID),
		SessionID:      knowledgeSessionID(input),
		Query:          query,
		Scope:          knowledge.ReuseScopeTask,
		MinQueryLength: minLength,
	})
	if err != nil {
		return nil, err
	}
	if plan.Degraded {
		return nil, nil
	}

	compile := p.Compile
	if compile == nil {
		compile = knowledge.CompilePlan
	}
	result := compile(knowledge.CompileRequest{
		Plan:            plan,
		TokenBudget:     p.budget(),
		ConfidenceFloor: p.Floor,
	})
	if len(result.Items) == 0 {
		return nil, nil
	}

	items := make([]map[string]interface{}, 0, len(result.Items))
	tiers := map[string]int{}
	totalTokens := 0
	staleInjected := 0
	for _, item := range result.Items {
		items = append(items, map[string]interface{}{
			"item_type":   item.ItemType,
			"source":      string(item.Source),
			"ref":         item.RefID,
			"target":      item.Target,
			"confidence":  item.Confidence,
			"version":     item.Version,
			"trust":       string(item.Trust),
			"tier":        knowledge.CompiledItemTier(item),
			"tokens":      item.Tokens,
			"verify":      item.Verify,
			"provisional": item.Provisional,
			"reason":      item.Reason,
			"stale":       item.Stale,
			"summary":     summarizeString(item.Content, 200),
		})
		tiers[knowledge.CompiledItemTier(item)]++
		totalTokens += item.Tokens
		if item.Stale {
			staleInjected++
		}
	}
	payload := map[string]interface{}{
		"mode":                "compiled",
		"reason":              result.Reason,
		"count":               len(result.Items),
		"tokens":              totalTokens,
		"tiers":               tiers,
		"stale_item_injected": staleInjected,
		"items":               items,
		"digest_block":        p.digestBlock(result.Items),
	}
	if len(result.Dropped) > 0 {
		dropped := make([]map[string]interface{}, 0, len(result.Dropped))
		for _, item := range result.Dropped {
			dropped = append(dropped, map[string]interface{}{
				"ref":    item.RefID,
				"target": item.Target,
				"reason": item.DropReason,
			})
		}
		payload["dropped"] = dropped
	}
	return payload, nil
}

// digestBlock 渲染 prompt 安全的 digest data block：只给计数与目标名单（是
// 数据，不是指令），不含摘要正文；超界退化为计数摘要（完整 block 或退化的
// 完整 block，绝不给半个块）。
func (p *KnowledgeProvider) digestBlock(items []knowledge.CompiledItem) string {
	targets := make([]string, 0, len(items))
	hot, warm, verify := 0, 0, 0
	for _, item := range items {
		if target := strings.TrimSpace(item.Target); target != "" {
			targets = append(targets, target)
		}
		if knowledge.CompiledItemTier(item) == knowledge.CompiledTierHot {
			hot++
		} else {
			warm++
		}
		if item.Verify {
			verify++
		}
	}
	var builder strings.Builder
	fmt.Fprintf(&builder, "Knowledge reuse: %d compiled item(s); hot=%d warm=%d; verify_required=%d.", len(items), hot, warm, verify)
	if len(targets) > 0 {
		builder.WriteString("\nReuse targets: " + strings.Join(limitStringSlice(targets, p.maxDigestTargets()), ", "))
	}
	block := knowledge.RenderDataBlock(knowledge.CompiledItem{
		ItemType: "exploration",
		Source:   knowledge.SourceClassMemory,
		Trust:    knowledge.TrustCodeIntelligence,
		Reason:   "pack_digest",
		Content:  builder.String(),
	})
	if utf8.RuneCountInString(block) > p.digestBudget() {
		return knowledge.RenderDataBlock(knowledge.CompiledItem{
			ItemType: "exploration",
			Source:   knowledge.SourceClassMemory,
			Trust:    knowledge.TrustCodeIntelligence,
			Reason:   "pack_digest",
			Content:  fmt.Sprintf("Knowledge reuse: %d compiled item(s).", len(items)),
		})
	}
	return block
}

func (p *KnowledgeProvider) minQueryLength() int {
	if p != nil && p.MinQueryLength > 0 {
		return p.MinQueryLength
	}
	return knowledge.DefaultMinKnowledgeQueryLength
}

func (p *KnowledgeProvider) budget() int {
	if p != nil && p.Budget > 0 {
		return p.Budget
	}
	return DefaultKnowledgePackBudget
}

func (p *KnowledgeProvider) digestBudget() int {
	if p != nil && p.DigestBudget > 0 {
		return p.DigestBudget
	}
	return DefaultKnowledgeDigestBudget
}

func (p *KnowledgeProvider) maxDigestTargets() int {
	if p != nil && p.MaxDigestTargets > 0 {
		return p.MaxDigestTargets
	}
	return DefaultKnowledgeDigestTargets
}

// knowledgeSessionID 取会话标识（Input 只有 Session 快照）。
func knowledgeSessionID(input *Input) string {
	if input == nil || input.Session == nil {
		return ""
	}
	return strings.TrimSpace(input.Session.ID)
}
