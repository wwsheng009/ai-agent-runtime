package runtimeapi

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

// skillCatalogProjection 是对"模型将看到的 catalog"的只读投影（SK-5）。
//
// 目的：前端展示与模型所见使用**同一渲染器与同一预算/降级口径**——这里直接复用
// SK-1 的 BuildCatalogEntries + RenderSkillCatalogWithOptions，而不是在 API 层
// 另写一套裁剪逻辑，避免两套口径随迭代漂移。
//
// 注意：投影的输入随宿主不同而不同（Codex list 用 discovery 结果，registry list
// 用注册表摘要）；同一技能名在两侧描述一致时，裁剪/降级结果一致（同一实现决定）。
type skillCatalogProjection struct {
	EntryCount            int    `json:"entry_count"`
	BudgetChars           int    `json:"budget_chars"`
	BodyChars             int    `json:"body_chars"`
	Degraded              bool   `json:"degraded"`
	DisciplineBlock       bool   `json:"discipline_block"`
	TruncatedDescriptions int    `json:"truncated_descriptions"`
	OmittedDescriptions   int    `json:"omitted_descriptions"`
	Fingerprint           string `json:"fingerprint,omitempty"`
}

// catalogSummariesFromCodexSkills 把 discovery 结果映射为 catalog 摘要（与注册表口径同构），
// 使 list 与注入走同一 BuildCatalogEntries 排序/别名规则。
func catalogSummariesFromCodexSkills(items []*skill.CodexSkillMetadata) []*skill.SkillSummary {
	summaries := make([]*skill.SkillSummary, 0, len(items))
	for _, item := range items {
		if item == nil || strings.TrimSpace(item.Name) == "" {
			continue
		}
		path := strings.TrimSpace(item.PathToSkillsMD)
		dir := ""
		if path != "" {
			dir = filepath.Dir(path)
		}
		summaries = append(summaries, &skill.SkillSummary{
			Name:             item.Name,
			Description:      item.Description,
			ShortDescription: item.ShortDescription,
			Source: &skill.SkillSource{
				Path:   path,
				Dir:    dir,
				Layer:  strings.TrimSpace(item.Scope),
				Format: skill.SkillSourceFormatCodex,
			},
			Codex: item,
		})
	}
	return summaries
}

// buildCatalogProjectionFromSummaries 是投影的唯一实现：任何宿主（注入、Codex list、
// 注册表 list/stats）都经它计算，保证预算与降级口径不分叉。
// cfg 缺失或没有条目时返回 nil（调用方省略该字段）。
func buildCatalogProjectionFromSummaries(summaries []*skill.SkillSummary, budgetChars int, disciplineBlock bool) *skillCatalogProjection {
	if len(summaries) == 0 {
		return nil
	}
	entries := skill.BuildCatalogEntries(summaries)
	if len(entries) == 0 {
		return nil
	}
	body, report := skill.RenderSkillCatalogWithOptions(
		entries,
		skill.CatalogBudget{Characters: budgetChars},
		disciplineBlock,
	)
	fingerprint := sha256.Sum256([]byte(body))
	return &skillCatalogProjection{
		EntryCount:            report.Included,
		BudgetChars:           report.BudgetChars,
		BodyChars:             report.BodyChars,
		Degraded:              report.Degraded(),
		DisciplineBlock:       disciplineBlock,
		TruncatedDescriptions: report.TruncatedDescriptionCount,
		OmittedDescriptions:   report.OmittedDescriptionCount,
		Fingerprint:           hex.EncodeToString(fingerprint[:8]),
	}
}

// buildCodexListCatalogProjection 用注入同源渲染器计算 Codex list 侧的 catalog 投影。
func buildCodexListCatalogProjection(groups []codexSkillsListGroup, budgetChars int, disciplineBlock bool) *skillCatalogProjection {
	if len(groups) == 0 {
		return nil
	}
	var all []*skill.CodexSkillMetadata
	for _, group := range groups {
		all = append(all, group.Skills...)
	}
	return buildCatalogProjectionFromSummaries(catalogSummariesFromCodexSkills(all), budgetChars, disciplineBlock)
}

// attachCatalogProjection 挂载/刷新 Codex list 响应的 catalog 投影。
// 缓存命中路径也会调用：预算与灰度开关是运行时配置，不能随 discovery 缓存冻结。
func (h *Handler) attachCatalogProjection(response *codexSkillsListResponse) {
	if h == nil || response == nil {
		return
	}
	cfg := h.runtimeSkillsConfig()
	if cfg == nil {
		response.Catalog = nil
		return
	}
	response.Catalog = buildCodexListCatalogProjection(response.Results, cfg.CatalogBudget(), cfg.DisciplineBlockEnabled())
}

// catalogProjectionFromRegistry 计算注册表口径的 catalog 投影（GET /skills、/stats 共用）。
// 它是"模型实际会看到的 catalog"的直接投影：输入与注入路径同为注册表摘要。
func (h *Handler) catalogProjectionFromRegistry() *skillCatalogProjection {
	if h == nil || h.skillRegistry == nil {
		return nil
	}
	cfg := h.runtimeSkillsConfig()
	if cfg == nil {
		return nil
	}
	return buildCatalogProjectionFromSummaries(h.skillRegistry.ListSummaries(), cfg.CatalogBudget(), cfg.DisciplineBlockEnabled())
}
