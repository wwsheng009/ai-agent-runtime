package skill

// P2-1B：技能目录（catalog）渲染与预算 —— 借鉴 Codex `core-skills/src/render.rs`。
//
// 职责：把"有哪些 skill 可用"这个轻量 catalog 注入上下文，**不**注入正文。
// 正文（ProgramGuide）按需、按提及注入；catalog 只放 name + description + 定位符。
// 预算默认 min(8000 字符, 上下文 2%)，超限时先截描述、再去描述，**技能永不消失**。

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// ExecutionMode 控制 skill 的执行形态。
//   - "" / "auto"：默认，走现有 Handler → Workflow → executeDefault 链路。
//   - "model"：跳过直执，交由模型选择程序（既有语义）。
//   - "document"：指令文档、无执行器（SK-7）。
const (
	ExecutionModeAuto     = ""
	ExecutionModeModel    = "model"
	ExecutionModeDocument = "document"
)

// SkillCatalogEntry 是 model 可见的轻量技能条目（catalog 层）。
type SkillCatalogEntry struct {
	Name             string
	Description      string
	ShortDescription string
	Scope            string // repo/user/system/admin/unknown，或 legacy layer
	SourcePath       string // SKILL.md / skill.yaml 绝对路径
	SourceDir        string // 技能目录（用于别名表）
	UseAliases       bool   // 渲染提示：是否以别名+short path 形态
}

// CatalogBudget 约束 catalog 在上下文中的字符开销。
type CatalogBudget struct {
	Characters int
}

// DefaultCatalogBudget 镜像 Codex：min(8000 字符, 上下文 2%)。
// ctxWindowTokens 为 0 时退化为固定 8000 字符。
func DefaultCatalogBudget(ctxWindowTokens int) CatalogBudget {
	const charBudget = 8_000
	const windowPct = 2
	tokenBudget := 0
	if ctxWindowTokens > 0 {
		tokenBudget = ctxWindowTokens * windowPct / 100
	}
	// 约 4 字节/词 → 字符数上限。
	charFromTokens := tokenBudget * 4
	if tokenBudget == 0 || charFromTokens < charBudget {
		return CatalogBudget{Characters: charBudget}
	}
	return CatalogBudget{Characters: charFromTokens}
}

// RenderReport 描述一次 catalog 渲染的裁剪情况。
type RenderReport struct {
	Total                     int
	Included                  int
	BudgetChars               int // 本次渲染的字符预算
	BodyChars                 int // 模型可见 catalog 正文长度（含前缀与纪律块，不含诊断告警）
	TruncatedDescriptionCount int
	TruncatedDescriptionChars int
	OmittedDescriptionCount   int
	Warnings                  []string
}

// Degraded 报告本次渲染是否发生预算降级（截断或省略描述）。
func (r RenderReport) Degraded() bool {
	return r.TruncatedDescriptionCount > 0 || r.OmittedDescriptionCount > 0
}

func (r RenderReport) String() string {
	return fmt.Sprintf(
		"catalog render: total=%d included=%d budget=%d body=%d truncated_descriptions=%d omitted_descriptions=%d",
		r.Total, r.Included, r.BudgetChars, r.BodyChars, r.TruncatedDescriptionCount, r.OmittedDescriptionCount,
	)
}

// BuildCatalogEntries 从摘要派生 catalog 条目并按 scope 优先级 → name 稳定排序。
func BuildCatalogEntries(skills []*SkillSummary) []SkillCatalogEntry {
	entries := make([]SkillCatalogEntry, 0, len(skills))
	for _, s := range skills {
		if s == nil || s.Name == "" {
			continue
		}
		scope := ""
		if s.Codex != nil {
			scope = s.Codex.Scope
		}
		if scope == "" && s.Source != nil {
			scope = s.Source.Layer
		}
		// Source 允许缺省（程序化注册/测试 stub）：路径与别名降级为空，
		// 不得 panic——catalog 注入在 runtime-server 每回合都会渲染。
		sourcePath, sourceDir := "", ""
		if s.Source != nil {
			sourcePath = s.Source.Path
			sourceDir = s.Source.Dir
		}
		useAliases := scope != "" && sourceDir != "" && sourcePath != ""
		entries = append(entries, SkillCatalogEntry{
			Name:             s.Name,
			Description:      s.Description,
			ShortDescription: s.ShortDescription,
			Scope:            scope,
			SourcePath:       sourcePath,
			SourceDir:        sourceDir,
			UseAliases:       useAliases,
		})
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if ri, rj := codexScopeRank(entries[i].Scope), codexScopeRank(entries[j].Scope); ri != rj {
			return ri < rj
		}
		return entries[i].Name < entries[j].Name
	})
	return entries
}

// RenderSkillCatalog 把 catalog 注入上下文：intro →（可选）skill roots → available skills →
// How to use skills 纪律块。超预算时按降级顺序裁剪描述。
func RenderSkillCatalog(entries []SkillCatalogEntry, budget CatalogBudget) (body string, report RenderReport) {
	return RenderSkillCatalogWithOptions(entries, budget, true)
}

// RenderSkillCatalogWithOptions 在 RenderSkillCatalog 基础上允许关闭纪律块
// （SK-10 skills.discipline_block 灰度）。withDiscipline=false 时不附加
// "How to use skills" 纪律块，但仍保留 catalog 条目与预算降级。
func RenderSkillCatalogWithOptions(entries []SkillCatalogEntry, budget CatalogBudget, withDiscipline bool) (body string, report RenderReport) {
	report.Total = len(entries)
	if budget.Characters <= 0 {
		budget.Characters = 8_000
	}
	report.BudgetChars = budget.Characters

	// 0) 复制条目：裁剪只改本地副本，不改写调用方持有的 slice。
	localEntries := append([]SkillCatalogEntry(nil), entries...)
	useAliases := len(localEntries) > 0 && localEntries[0].UseAliases

	// 1) 前缀（intro / skill roots / 段落标题）与纪律块都计入同一预算（SK-10）。
	var prefix strings.Builder
	prefix.WriteString("## Skills\n")
	if useAliases {
		prefix.WriteString(SKILLS_INTRO_WITH_ALIASES)
		prefix.WriteString("\n### Skill roots\n")
		seen := map[string]struct{}{}
		for _, e := range localEntries {
			if e.SourceDir == "" {
				continue
			}
			key := e.Scope + "|" + e.SourceDir
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			prefix.WriteString(fmt.Sprintf("- %s: %s\n", e.Scope, e.SourceDir))
		}
	} else {
		prefix.WriteString(SKILLS_INTRO_WITH_ABSOLUTE_PATHS)
	}
	prefix.WriteString("\n### Available skills\n")

	discipline := ""
	if withDiscipline {
		discipline = "\n" + SkillUsageDiscipline(useAliases)
	}

	// 2) 行预算 = 总预算 − 前缀 − 纪律块；技能条目永不消失，只降级描述。
	lines := make([]string, 0, len(localEntries))
	for i := range localEntries {
		lines = append(lines, catalogLine(&localEntries[i]))
	}
	lineBudget := budget.Characters - len(prefix.String()) - len(discipline)
	lines = trimLinesToBudget(lines, localEntries, lineBudget, &report)

	// 3) 汇编；告警是诊断信息，追加在预算之外（不计入 BodyChars）。
	var b strings.Builder
	b.WriteString(prefix.String())
	for _, l := range lines {
		b.WriteString(l)
		b.WriteString("\n")
	}
	if discipline != "" {
		b.WriteString(discipline)
	}
	report.Included = len(localEntries)
	report.BodyChars = b.Len()

	// 4) 可见告警（Codex 在 body 里 surfaced 告警，而非仅 metrics）。
	for _, w := range report.Warnings {
		b.WriteString(w)
		b.WriteString("\n")
	}

	return b.String(), report
}

func catalogLine(e *SkillCatalogEntry) string {
	name := e.Name
	if name == "" {
		name = "(unnamed)"
	}
	desc := firstNonEmpty(e.ShortDescription, e.Description)
	locator := e.SourcePath
	if e.UseAliases {
		locator = e.SourceDir
	}
	if desc == "" {
		return fmt.Sprintf("- %s (path: %s)", name, locator)
	}
	return fmt.Sprintf("- %s — %s (path: %s)", name, desc, locator)
}

// trimLinesToBudget 在累计行预算内降级描述：先截长描述到阈值，再从尾部省略描述。
// 技能条目永不消失；预算耗尽后仍装不下的部分只追加可见告警，不静默超限。
func trimLinesToBudget(lines []string, entries []SkillCatalogEntry, budget int, report *RenderReport) []string {
	const descTruncateThreshold = 100 // 与 Codex 对齐的阈值（按 rune 计）

	// 预算不足以容纳任何描述：直接输出名称行。
	if budget <= 0 {
		for i := range lines {
			if catalogHasDescription(&entries[i]) {
				report.OmittedDescriptionCount++
			}
			entries[i].ShortDescription = ""
			entries[i].Description = ""
			lines[i] = catalogLine(&entries[i])
		}
		report.Warnings = append(report.Warnings,
			"Skill descriptions were omitted to fit the catalog budget. Codex can still see every skill.")
		return lines
	}

	if linesTotalChars(lines) <= budget {
		return lines
	}

	// 降级 1：按行序把超阈值描述截到阈值，直到累计行宽进入预算。
	for i := range lines {
		if linesTotalChars(lines) <= budget {
			break
		}
		e := &entries[i]
		desc := firstNonEmpty(e.ShortDescription, e.Description)
		if utf8.RuneCountInString(desc) <= descTruncateThreshold {
			continue
		}
		e.ShortDescription = ""
		e.Description = truncateRunes(desc, descTruncateThreshold) + "..."
		lines[i] = catalogLine(e)
		report.TruncatedDescriptionCount++
		report.TruncatedDescriptionChars += utf8.RuneCountInString(desc) - descTruncateThreshold
	}

	// 降级 2：仍超预算时从尾部向前省略描述（优先保留高优先级条目的信息）。
	for i := len(lines) - 1; i >= 0 && linesTotalChars(lines) > budget; i-- {
		e := &entries[i]
		if !catalogHasDescription(e) {
			continue
		}
		e.ShortDescription = ""
		e.Description = ""
		lines[i] = catalogLine(e)
		report.OmittedDescriptionCount++
	}

	if report.Degraded() {
		report.Warnings = append(report.Warnings,
			"Skill descriptions were shortened to fit the catalog budget. Codex can still see every skill, but some descriptions are shorter. Disable unused skills or plugins to leave more room for the rest.")
	}
	if remaining := linesTotalChars(lines); remaining > budget {
		report.Warnings = append(report.Warnings,
			fmt.Sprintf("Skill catalog exceeds the configured budget even after descriptions were omitted (budget=%d, actual=%d).", budget, remaining))
	}
	return lines
}

func linesTotalChars(lines []string) int {
	total := 0
	for _, l := range lines {
		total += len(l) + 1 // +\n
	}
	return total
}

func catalogHasDescription(e *SkillCatalogEntry) bool {
	return firstNonEmpty(e.ShortDescription, e.Description) != ""
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if limit <= 0 || len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

// SkillUsageDiscipline 返回 "How to use skills" 纪律块（借鉴 Codex，术语对齐本仓库）。
func SkillUsageDiscipline(useAliases bool) string {
	if useAliases {
		return SKILLS_HOW_TO_USE_WITH_ALIASES
	}
	return SKILLS_HOW_TO_USE_WITH_ABSOLUTE_PATHS
}

// SKILLS_INTRO_WITH_ABSOLUTE_PATHS / _WITH_ALIASES 镜像 Codex 的使用说明，
// 术语对齐：skill.yaml + prompt.md + SKILL.md / references/ / scripts/ / assets/。
const SKILLS_INTRO_WITH_ABSOLUTE_PATHS = "A skill is a set of instructions provided through a `skill.yaml` (with a companion `prompt.md`) or a Codex-style `SKILL.md`. Below is the list of skills available in this session (name + description + source locator). `file` locators are on the host filesystem; point the model at the same locator to read the full instructions."

const SKILLS_INTRO_WITH_ALIASES = "A skill is a set of local instructions to follow that is stored in a `skill.yaml` (with a companion `prompt.md`) or a Codex-style `SKILL.md`. Below is the list of skills available in this session (name + description + short path). Skill bodies live on disk at the listed paths after expanding the matching alias from the skill roots table below."

const SKILLS_HOW_TO_USE_WITH_ABSOLUTE_PATHS = `
- Discovery: The list above is the skills available in this session (name + description + source locator).
- Trigger rules: If the user names a skill (with /skill <name> or a mention) OR the task clearly matches a skill's description shown above, you must use that skill for this turn. Multiple mentions mean use them all. Do not carry skills across turns unless re-mentioned.
- Missing/blocked: If a named skill isn't in the list or its source can't be read, say so briefly and continue with the best fallback.
- How to use a skill (progressive disclosure):
  1) After deciding to use a skill, read its instructions completely before taking task actions. For a file locator, open the listed path (the SKILL.md body or the skill.yaml + prompt.md manifest). If a read is truncated or paginated, continue until EOF.
  2) When the instructions reference other resources (references/, scripts/, assets/), resolve them relative to the skill directory and read each required file yourself before acting. Do not delegate reading, summarizing, or interpreting skill instructions to a subagent. Subagents may still perform task work when the selected skill allows it.
  3) If scripts/ exist, prefer running or patching them instead of retyping large code blocks.
  4) If assets/ or templates exist, reuse them instead of recreating from scratch.
  5) Avoid deep reference-chasing: prefer opening only files directly linked from the instructions unless you are blocked.
- Coordination and sequencing:
  - If multiple skills apply, choose the minimal set that covers the request and state the order you'll use them.
  - Announce which skill(s) you're using and why (one short line). If you skip an obvious skill, say why.
- Safety and fallback: If a skill can't be applied cleanly (missing files, unclear instructions), state the issue, pick the next-best approach, and continue.
`

const SKILLS_HOW_TO_USE_WITH_ALIASES = `
- Discovery: The list above is the skills available in this session (name + description + short path). Skill bodies live on disk at the listed paths after expanding the matching alias from the skill roots table below.
- Trigger rules: If the user names a skill (with /skill <name> or a mention) OR the task clearly matches a skill's description shown above, you must use that skill for this turn. Multiple mentions mean use them all. Do not carry skills across turns unless re-mentioned.
- Missing/blocked: If a named skill isn't in the list or the path can't be read, say so briefly and continue with the best fallback.
- How to use a skill (progressive disclosure):
  1) After deciding to use a skill, expand the listed short path with the matching alias from the skill roots table, then open and read its instructions completely before taking task actions. If a read is truncated or paginated, continue until EOF.
  2) When the instructions reference relative paths (e.g. references/foo.md, scripts/foo.py), resolve them relative to the directory containing that expanded skill directory first. Read each required file yourself before acting; do not delegate reading, summarizing, or interpreting skill instructions to a subagent. Subagents may still perform task work when the selected skill allows it.
  3) If scripts/ exist, prefer running or patching them instead of retyping large code blocks.
  4) If assets/ or templates exist, reuse them instead of recreating from scratch.
  5) Avoid deep reference-chasing: prefer opening only files directly linked from the instructions unless you are blocked.
- Coordination and sequencing:
  - If multiple skills apply, choose the minimal set that covers the request and state the order you'll use them.
  - Announce which skill(s) you're using and why (one short line). If you skip an obvious skill, say why.
- Safety and fallback: If a skill can't be applied cleanly (missing files, unclear instructions), state the issue, pick the next-best approach, and continue.
`

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	if strings.TrimSpace(b) != "" {
		return b
	}
	return ""
}
