package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/prompt"
	runtimeskill "github.com/wwsheng009/ai-agent-runtime/internal/skill"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// P0：Codex 风格 `$skill-name` 提及注入（抽象指令层，score=turn）。
//
// 方案：docs/plan/codex-text-skill-mention-injection-plan-20261002.md §4.2/§4.4/§4.11。
// 本文件只做三件事并保持相互独立、可单测：
//  1. collectSkillMentionNames —— 纯词法：$name、名称字符集、env 名单、代码块/行内代码豁免；
//  2. resolveMentionedTextSkills —— 解析候选：await 能力面 → 直读 binding.skillFunctions
//     （不走 SelectRequestFunctions / AnalyzeSkillExposure）→ 文本类过滤/禁用/歧义/上限；
//  3. buildSkillMentionFragments —— 读取正文（resolvedTurnSkill + SubstituteSkillText）、
//     单技能/总量/回合预算三级降级，产出 canonical 指令消息。
//
// 注入的消息不选择 wire 角色：NewInstructionMessage 携带 scope=session/turn 与
// source，协议适配器按 §4.12 契约统一转换（Q1 两层架构）。

// 诊断 reason 口径（plan §4.10）。
const (
	skillMentionSkipAmbiguous = "ambiguous"
	skillMentionSkipDisabled  = "disabled"
	skillMentionSkipLimit     = "limit"
	skillMentionSkipReadError = "read_error"
	skillMentionSkipSystem    = "system_input"
	// Q11：技能因声明工具/依赖不可用而未加载（registry.UnavailableSkills）。
	skillMentionSkipDependencyUnavailable = "dependency_unavailable"
	// Q12：auto 模式下未信任项目默认禁用 mention 注入。
	skillMentionSkipUntrustedProject = "untrusted_project"
)

const (
	// Q7：mention 路径没有显式参数，正文引用 $ARGUMENTS 时补一行提示，避免模型臆造。
	skillMentionNoArgumentsNote = "（未提供显式参数）"
	// 截断标记：单技能上限、回合总量上限、preflight 本地预算（§4.11）。
	skillMentionSingleTruncateMarker = "\n[skill instructions truncated: exceeded the per-skill character limit]"
	skillMentionTotalTruncateMarker  = "\n[skill instructions truncated: exceeded the per-turn character budget]"
	skillMentionBudgetTruncateMarker = "\n[skill instructions truncated: exceeded the turn prompt budget]"
	// 片段头部/尾部的固定长度下限：低于该余量时整条丢弃（§4.11 降级顺序的“全弃”落点）。
	skillMentionMinFragmentRunes = 16
)

// skillMentionIgnoredNames 是 `$` 提及解析要忽略的常见环境变量/占位符名单
// （大小写不敏感；对齐 Codex is_common_env_var + PowerShell 形态）。
var skillMentionIgnoredNames = map[string]struct{}{
	"home":   {},
	"path":   {},
	"pwd":    {},
	"user":   {},
	"temp":   {},
	"uid":    {},
	"shell":  {},
	"env":    {}, // 覆盖 $env:FOO（`:` 终止名称）
	"null":   {}, // $null
	"_":      {}, // $_（同时覆盖 $PSItem 之外的常见形态）
	"pshome": {},
}

// skillMention 是一次词法命中的提及：Name 为已知集合中的规范名，Path 为可选的
// composer 绑定路径（P0 恒空；P2 绑定优先用）。
type skillMention struct {
	Name string
	Path string
}

// skillMentionInjected 记录实际注入的技能（plan §4.10 的 skill_injected）。
type skillMentionInjected struct {
	Name      string `json:"name"`
	Chars     int    `json:"chars"`
	Truncated bool   `json:"truncated,omitempty"`
}

// skillMentionSkipped 记录未注入的提及及原因。
type skillMentionSkipped struct {
	Name   string `json:"name,omitempty"`
	Reason string `json:"reason"`
	Detail string `json:"detail,omitempty"`
}

// skillMentionDiagnostics 是 mentioned/injected/skipped 两级口径。
type skillMentionDiagnostics struct {
	Mentioned []string               `json:"mentioned,omitempty"`
	Injected  []skillMentionInjected `json:"injected,omitempty"`
	Skipped   []skillMentionSkipped  `json:"skipped,omitempty"`
	Notice    string                 `json:"notice,omitempty"`
}

func (d *skillMentionDiagnostics) addSkipped(name, reason, detail string) {
	if d == nil {
		return
	}
	d.Skipped = append(d.Skipped, skillMentionSkipped{
		Name:   strings.TrimSpace(name),
		Reason: reason,
		Detail: strings.TrimSpace(detail),
	})
}

// collectSkillMentionNames 从 prompt 中做纯词法提取：`$name`，名称字符集
// [A-Za-z0-9_-]；忽略 env 名单与纯数字；代码块（```）与行内代码（`）内的 `$`
// 不解析；大小写不敏感匹配 known（Q9）。
//
// known 的键是技能名（任意大小写），值是可选 canonical path（P0 为空）。
// 未命中 known 的 `$xxx`（含 `$var`、价格等）一律忽略、不报错。
func collectSkillMentionNames(prompt string, known map[string]string) []skillMention {
	if strings.TrimSpace(prompt) == "" || len(known) == 0 {
		return nil
	}
	type knownEntry struct {
		name string
		path string
	}
	entries := make([]knownEntry, 0, len(known))
	for name, path := range known {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		entries = append(entries, knownEntry{name: name, path: path})
	}
	if len(entries) == 0 {
		return nil
	}
	// 稳定匹配顺序：先按小写名，再按原样字节序，避免 map 迭代随机导致
	// 大小写重复键的行为漂移。
	sort.Slice(entries, func(i, j int) bool {
		li, lj := strings.ToLower(entries[i].name), strings.ToLower(entries[j].name)
		if li != lj {
			return li < lj
		}
		return entries[i].name < entries[j].name
	})

	mentions := make([]skillMention, 0, 4)
	seen := make(map[string]struct{})
	scanSkillMentionTokens(prompt, func(token string) {
		lower := strings.ToLower(token)
		if _, ignored := skillMentionIgnoredNames[lower]; ignored {
			return
		}
		if isASCIIDigits(token) {
			return
		}
		if _, dup := seen[lower]; dup {
			return
		}
		for _, entry := range entries {
			if strings.EqualFold(entry.name, token) {
				seen[lower] = struct{}{}
				mentions = append(mentions, skillMention{Name: entry.name, Path: entry.path})
				return
			}
		}
		// 未在 known 中：普通文本（不注入、不报错）。
	})
	return mentions
}

// scanSkillMentionTokens 扫描 prompt 中的 `$name` token；跳过 fenced code block
// （以 ``` 开头的行翻转状态）与行内代码（反引号配对翻转）。
func scanSkillMentionTokens(prompt string, visit func(token string)) {
	if visit == nil {
		return
	}
	fence := false
	for _, line := range strings.Split(prompt, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			fence = !fence
			continue
		}
		if fence {
			continue
		}
		inCode := false
		for i := 0; i < len(line); {
			switch {
			case line[i] == '`':
				inCode = !inCode
				i++
			case line[i] == '$' && !inCode:
				j := i + 1
				for j < len(line) && isSkillMentionNameByte(line[j]) {
					j++
				}
				if j == i+1 {
					i++
					continue
				}
				visit(line[i+1 : j])
				i = j
			default:
				i++
			}
		}
	}
}

func isSkillMentionNameByte(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z':
		return true
	case c >= 'A' && c <= 'Z':
		return true
	case c >= '0' && c <= '9':
		return true
	case c == '_' || c == '-':
		return true
	default:
		return false
	}
}

func isASCIIDigits(text string) bool {
	if text == "" {
		return false
	}
	for i := 0; i < len(text); i++ {
		if text[i] < '0' || text[i] > '9' {
			return false
		}
	}
	return true
}

// skillMentionBinding 直读会话的 skills binding：优先 session.SkillsBinding，
// 退回函数目录持有的 binding。绝不经过 SelectRequestFunctions / AnalyzeSkillExposure，
// 保证 P3 隐藏文本类函数后 `$name` 解析仍生效（plan P0 第 9 行）。
func skillMentionBinding(session *ChatSession) *skillsRuntimeBinding {
	if session == nil {
		return nil
	}
	if session.SkillsBinding != nil {
		return session.SkillsBinding
	}
	if catalog := ensureFunctionCatalog(session); catalog != nil {
		return catalog.SkillsBinding()
	}
	return nil
}

// skillMentionKnownNames 构造 collect 用的已知集合：已加载技能名（含被禁用但
// 仍在配置名单中的名字，便于诊断 reason=disabled），值为 canonical path。
func skillMentionKnownNames(session *ChatSession) map[string]string {
	known := make(map[string]string)
	if binding := skillMentionBinding(session); binding != nil {
		for _, fn := range binding.skillFunctions {
			name := skillFunctionDisplayName(fn)
			if name == "" {
				continue
			}
			key := strings.ToLower(name)
			path := normalizeSkillMentionPath(fn.sourcePath)
			// P2 绑定优先：composer `$` 补全记录的 path 覆盖函数的默认 sourcePath，
			// 同名多技能时用户的选择不被目录顺序覆盖（plan §4.2）。
			if bound := session.skillMentionBoundPath(name); bound != "" {
				path = bound
			}
			if current, exists := known[key]; !exists || (current == "" && path != "") {
				known[key] = path
			}
		}
	}
	for _, name := range disabledSkillNames(session.Config) {
		key := strings.ToLower(strings.TrimSpace(name))
		if key == "" {
			continue
		}
		if _, exists := known[key]; !exists {
			known[key] = ""
		}
	}
	// Q11：不可用技能名同样进入 known 集合，提及它们才能命中依赖诊断与提示
	// （否则 collect 阶段就把名字忽略了）。
	for _, item := range skillMentionUnavailableSkills(skillMentionBinding(session)) {
		key := strings.ToLower(strings.TrimSpace(item.Name))
		if key == "" {
			continue
		}
		if _, exists := known[key]; !exists {
			known[key] = ""
		}
	}
	return known
}

func skillFunctionDisplayName(fn *SkillFunction) string {
	if fn == nil {
		return ""
	}
	if fn.summary != nil {
		if name := strings.TrimSpace(fn.summary.Name); name != "" {
			return name
		}
	}
	if fn.skill != nil {
		if name := strings.TrimSpace(fn.skill.Name); name != "" {
			return name
		}
	}
	return strings.TrimSpace(strings.TrimPrefix(fn.functionName, "skill__"))
}

// normalizeSkillMentionPath 把技能路径统一为 `/` 分隔，保证 Windows/跨会话
// 注入文本逐字节稳定（plan §4.2 路径归一化）。
func normalizeSkillMentionPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	normalized := filepath.ToSlash(filepath.Clean(path))
	if normalized == "." {
		return ""
	}
	return normalized
}

type skillMentionCandidate struct {
	fn    *SkillFunction
	name  string
	path  string
	skill *runtimeskill.Skill
}

type skillMentionSelection struct {
	skills []skillMentionCandidate
	// dependencyNote 记录"提及了但依赖不可用（未加载）"的技能，作为 Q11
	// 回合提示素材；已加载技能的工具可用性在回合入口另行检查。
	dependencyNote *skillMentionDependencyNote
}

// resolveMentionedTextSkills 解析提及候选（plan §4.4）：
//   - 先 awaitChatCapabilitiesForTurn（与 09fc19b2 同语义），失败降级不注入；
//   - 候选直读 binding.skillFunctions；仅文本类（Handler==nil && !HasWorkflow）；
//   - 过滤 disabled_skills；歧义跳过并诊断；按 catalog 顺序排序；
//   - 超出 mention_multi_limit 截断并汇总提示；
//   - excludedFunctions 是 `/skill` pin 的函数名集合，用于「pin 优先、不重复注入」。
func resolveMentionedTextSkills(ctx context.Context, session *ChatSession, mentions []skillMention, excludedFunctions map[string]struct{}) (*skillMentionSelection, *skillMentionDiagnostics) {
	diag := &skillMentionDiagnostics{Mentioned: skillMentionNames(mentions)}
	selection := &skillMentionSelection{}
	if session == nil || len(mentions) == 0 {
		return selection, diag
	}
	if err := awaitChatCapabilitiesForTurn(ctx, session); err != nil {
		for _, mention := range mentions {
			diag.addSkipped(mention.Name, skillMentionSkipReadError, "capabilities unavailable: "+err.Error())
		}
		return selection, diag
	}
	binding := skillMentionBinding(session)
	if binding == nil {
		for _, mention := range mentions {
			diag.addSkipped(mention.Name, skillMentionSkipReadError, "skills runtime is not available")
		}
		return selection, diag
	}
	// Q11：注册表中因依赖（工具）不可用而未加载的技能，用于把提及诊断为
	// dependency_unavailable，而不是笼统的 read_error。
	unavailable := skillMentionUnavailableIndex(binding)

	disabled := make(map[string]struct{})
	for _, name := range disabledSkillNames(session.Config) {
		if key := strings.ToLower(strings.TrimSpace(name)); key != "" {
			disabled[key] = struct{}{}
		}
	}

	byName := make(map[string][]skillMentionCandidate, len(binding.skillFunctions))
	for _, fn := range binding.skillFunctions {
		name := skillFunctionDisplayName(fn)
		if fn == nil || name == "" {
			continue
		}
		candidate := skillMentionCandidate{fn: fn, name: name, path: normalizeSkillMentionPath(fn.sourcePath)}
		key := strings.ToLower(name)
		byName[key] = append(byName[key], candidate)
	}
	order := skillMentionCatalogOrder(binding)

	selected := make([]skillMentionCandidate, 0, len(mentions))
	seenPaths := make(map[string]struct{})
	for _, mention := range mentions {
		name := strings.TrimSpace(mention.Name)
		key := strings.ToLower(name)
		if key == "" {
			continue
		}
		if _, isDisabled := disabled[key]; isDisabled {
			diag.addSkipped(name, skillMentionSkipDisabled, "skills_runtime.disabled_skills")
			continue
		}
		candidates := byName[key]
		if len(candidates) == 0 {
			if item, ok := unavailable[key]; ok {
				tools := normalizeSkillMentionToolNames(item.MissingTools)
				detail := strings.Join(tools, ", ")
				if detail == "" {
					detail = strings.TrimSpace(item.Reason)
				}
				diag.addSkipped(name, skillMentionSkipDependencyUnavailable, detail)
				selection.dependencyNote = appendSkillMentionDependencyNote(selection.dependencyNote, name, tools)
				continue
			}
			// collect 只返回 known 中的名字；缺失通常意味着热加载窗口。
			diag.addSkipped(name, skillMentionSkipReadError, "skill is not loaded")
			continue
		}
		// P2 绑定优先：path 命中则唯一化；path 失败时阻断同名文本兜底（plan §4.2）。
		if mention.Path != "" {
			boundPath := normalizeSkillMentionPath(mention.Path)
			var matched *skillMentionCandidate
			for i := range candidates {
				if candidates[i].path != "" && candidates[i].path == boundPath {
					matched = &candidates[i]
					break
				}
			}
			if matched == nil {
				diag.addSkipped(name, skillMentionSkipAmbiguous, "bound path is not available for this skill")
				continue
			}
			candidates = []skillMentionCandidate{*matched}
		}
		unique := dedupeSkillMentionCandidatesByPath(candidates)
		if len(unique) > 1 {
			diag.addSkipped(name, skillMentionSkipAmbiguous, "multiple skills share this name")
			continue
		}
		candidate := unique[0]
		if excludedFunctions != nil {
			if _, pinned := excludedFunctions[candidate.fn.Name()]; pinned {
				// pin 已提供该技能：静默去重，不重复注入正文。
				continue
			}
		}
		skillItem := candidate.fn.resolvedTurnSkill()
		if skillItem == nil {
			// 诊断精度：能从摘要判定形态时给出精确原因，避免把「非文本 / 无正文」
			// 与「本可注入但读取失败」混在同一个 read_error 里（实测
			// skill_runtime_smoke 被报成 read_error，调用方无法判断该不该重试）。
			summary := candidate.fn.summary
			switch {
			case summary != nil && summary.HasWorkflow():
				writeSessionDebugInfo(session, fmt.Sprintf("[skill-mention] ignored non-text skill %q (workflow)", candidate.name), false)
				diag.addSkipped(candidate.name, skillMentionSkipDisabled, "not an instruction-only (text) skill")
				continue
			case summary != nil && summary.Handler != nil:
				writeSessionDebugInfo(session, fmt.Sprintf("[skill-mention] ignored non-text skill %q (handler)", candidate.name), false)
				diag.addSkipped(candidate.name, skillMentionSkipDisabled, "not an instruction-only (text) skill")
				continue
			case summary != nil && !summary.IsDocumentMode():
				// prompt-only / 传统 skill（systemPrompt + userPrompt，无 SKILL.md
				// 正文，如 skill_runtime_smoke）：即便惰性 resolver 存在且解析
				// 失败，也不是"可重试的读取失败"，而是不存在可注入的指令文档。
				writeSessionDebugInfo(session, fmt.Sprintf("[skill-mention] ignored non-injectable skill %q (non-document)", candidate.name), false)
				diag.addSkipped(candidate.name, skillMentionSkipDisabled, "skill is not an injectable instruction document")
				continue
			case candidate.fn.skillResolver == nil && candidate.fn.skill == nil:
				// summary-only 条目（discovery 轻量注册，永不产生完整定义）没有可
				// 注入的正文，属结构性不可注入，而不是读取失败。
				writeSessionDebugInfo(session, fmt.Sprintf("[skill-mention] ignored non-injectable skill %q (summary-only)", candidate.name), false)
				diag.addSkipped(candidate.name, skillMentionSkipDisabled, "skill has no injectable instruction body")
				continue
			}
			diag.addSkipped(candidate.name, skillMentionSkipReadError, "skill definition is unavailable")
			continue
		}
		if !skillUsesDefaultExecution(skillItem) {
			// D3：handler/workflow 技能被 `$` 提及时 P0 忽略并记 debug。
			writeSessionDebugInfo(session, fmt.Sprintf("[skill-mention] ignored non-text skill %q", candidate.name), false)
			diag.addSkipped(candidate.name, skillMentionSkipDisabled, "not an instruction-only (text) skill")
			continue
		}
		if strings.TrimSpace(skillItem.Body) == "" {
			// 无可注入正文（prompt-only / resolver 失败回退到 stub）：属结构性
			// 不可注入，必须在解析阶段就按 disabled 归类。否则会带着空正文进入
			// buildSkillMentionFragments，被兜底的 "skill body is empty" 误报成
			// read_error——看起来像可重试的读取失败（实测 skill_runtime_smoke）。
			detail := "skill has no injectable instruction body"
			if summary := candidate.fn.summary; summary != nil && !summary.IsDocumentMode() {
				detail = "skill is not an injectable instruction document"
			}
			writeSessionDebugInfo(session, fmt.Sprintf("[skill-mention] ignored non-injectable skill %q (empty body)", candidate.name), false)
			diag.addSkipped(candidate.name, skillMentionSkipDisabled, detail)
			continue
		}
		if candidate.path != "" {
			if _, dup := seenPaths[candidate.path]; dup {
				continue
			}
			seenPaths[candidate.path] = struct{}{}
		}
		candidate.skill = skillItem
		selected = append(selected, candidate)
	}

	sortSkillMentionCandidates(selected, order)
	limit := skillRuntimeConfig(session.Config).MentionMultiLimitValue()
	if limit > 0 && len(selected) > limit {
		for _, extra := range selected[limit:] {
			diag.addSkipped(extra.name, skillMentionSkipLimit, fmt.Sprintf("mention_multi_limit=%d", limit))
		}
		diag.Notice = fmt.Sprintf("%d 个技能因超过单回合上限（最多 %d 个）未注入", len(selected)-limit, limit)
		selected = selected[:limit]
	}
	selection.skills = selected
	return selection, diag
}

// skillMentionCatalogOrder 构建函数名 → catalog 序号索引；catalog 缺失时返回
// 空表，排序回退到技能名升序（保持跨会话稳定）。
func skillMentionCatalogOrder(binding *skillsRuntimeBinding) map[string]int {
	order := make(map[string]int)
	if binding == nil || binding.catalog == nil {
		return order
	}
	for index, name := range binding.catalog.SkillFunctionNames() {
		if _, exists := order[name]; !exists {
			order[name] = index
		}
	}
	return order
}

func dedupeSkillMentionCandidatesByPath(candidates []skillMentionCandidate) []skillMentionCandidate {
	if len(candidates) <= 1 {
		return candidates
	}
	unique := make([]skillMentionCandidate, 0, len(candidates))
	seenPaths := make(map[string]struct{}, len(candidates))
	emptyPathSeen := false
	for _, candidate := range candidates {
		if candidate.path == "" {
			if emptyPathSeen {
				continue
			}
			emptyPathSeen = true
			unique = append(unique, candidate)
			continue
		}
		if _, dup := seenPaths[candidate.path]; dup {
			continue
		}
		seenPaths[candidate.path] = struct{}{}
		unique = append(unique, candidate)
	}
	return unique
}

func sortSkillMentionCandidates(candidates []skillMentionCandidate, order map[string]int) {
	if len(candidates) <= 1 {
		return
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		left, right := candidates[i], candidates[j]
		leftOrder, leftOK := order[left.fn.Name()]
		rightOrder, rightOK := order[right.fn.Name()]
		switch {
		case leftOK && rightOK && leftOrder != rightOrder:
			return leftOrder < rightOrder
		case leftOK != rightOK:
			return leftOK
		}
		leftName, rightName := strings.ToLower(left.name), strings.ToLower(right.name)
		if leftName != rightName {
			return leftName < rightName
		}
		return left.fn.Name() < right.fn.Name()
	})
}

func skillMentionNames(mentions []skillMention) []string {
	names := make([]string, 0, len(mentions))
	for _, mention := range mentions {
		if name := strings.TrimSpace(mention.Name); name != "" {
			names = append(names, name)
		}
	}
	return names
}

func skillMentionPinnedFunctionSet(pin *skillTurnPin) map[string]struct{} {
	if pin == nil || len(pin.PinnedFunctions) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(pin.PinnedFunctions))
	for _, name := range pin.PinnedFunctions {
		if name = strings.TrimSpace(name); name != "" {
			set[name] = struct{}{}
		}
	}
	return set
}

// skillMentionGuideTokens 估算 pin guide 在回合预算中的占用，用于 §4.11
// 本地裁决（guide 已先于 mention 片段进入本回合）。
func skillMentionGuideTokens(pin *skillTurnPin) int {
	if pin == nil {
		return 0
	}
	guide := strings.TrimSpace(pin.Guide)
	if guide == "" {
		return 0
	}
	return estimateSharedChatTokenCount(guide) + 4
}

// skillMentionBudget 是本地注入预算裁决（plan §4.11 step 1-3）：
// 配置的单技能/总量字符上限，加上 preflight 可用 token 余量（20% 预留）。
type skillMentionBudget struct {
	// MaxCharsPerSkill 是单技能正文（不含 <skill> 包装）字符上限；<=0 表示不设限。
	MaxCharsPerSkill int
	// MaxCharsTotal 是单回合片段总量（含包装）字符上限；<=0 表示不设限。
	MaxCharsTotal int
	// HasTokenBudget 为 true 时启用 ActiveTurnMaxTokens 余量裁决。
	HasTokenBudget bool
	// AvailableTokens 是既有历史 + pin guide 之后的可用 token 余量。
	AvailableTokens int
}

// resolveSkillMentionBudget 复用现有 prompt 预算 API 的结果（调用方传入
// resolveSharedChatPromptBudget(...).ActiveTurnMaxTokens 与已用 token 估算），
// 预留 ≥20% 余量给模型输出与工具结果，防止注入把请求推过 preflight 阈值。
func resolveSkillMentionBudget(session *ChatSession, usedTokens, budgetTokens int) skillMentionBudget {
	cfg := skillRuntimeConfig(session.Config)
	budget := skillMentionBudget{
		MaxCharsPerSkill: cfg.MentionInjectMaxCharsValue(),
		MaxCharsTotal:    cfg.MentionInjectTotalCharsValue(),
	}
	if budgetTokens > 0 {
		usable := budgetTokens - budgetTokens/5
		budget.HasTokenBudget = true
		budget.AvailableTokens = usable - usedTokens
		if budget.AvailableTokens < 0 {
			budget.AvailableTokens = 0
		}
	}
	return budget
}

// buildSkillMentionFragments 读取选中技能的正文并构造 canonical 指令消息。
// 降级顺序（§4.11）：单技能截断 → 回合总量截断/丢弃（limit）→ 预算不足全弃。
func buildSkillMentionFragments(session *ChatSession, selection *skillMentionSelection, budget skillMentionBudget) ([]runtimetypes.Message, *skillMentionDiagnostics) {
	diag := &skillMentionDiagnostics{}
	if session == nil || selection == nil || len(selection.skills) == 0 {
		return nil, diag
	}
	cfg := skillRuntimeConfig(session.Config)
	projectDir := ""
	if cwd, err := os.Getwd(); err == nil {
		projectDir = cwd
	}
	messages := make([]runtimetypes.Message, 0, len(selection.skills))
	usedChars := 0
	usedTokens := 0
	for _, entry := range selection.skills {
		skillItem := entry.skill
		if skillItem == nil {
			skillItem = entry.fn.resolvedTurnSkill()
		}
		if skillItem == nil {
			diag.addSkipped(entry.name, skillMentionSkipReadError, "skill definition is unavailable")
			continue
		}
		rawBody := strings.TrimSpace(skillItem.Body)
		if rawBody == "" {
			diag.addSkipped(entry.name, skillMentionSkipReadError, "skill body is empty")
			continue
		}
		// Q7：先判定原始正文是否引用 $ARGUMENTS/${ARGUMENTS}，再替换为空。
		noExplicitArguments := strings.Contains(rawBody, "$ARGUMENTS") || strings.Contains(rawBody, "${ARGUMENTS}")
		effort := strings.TrimSpace(session.ReasoningEffort)
		if skillItem.Codex != nil {
			if declared := strings.TrimSpace(skillItem.Codex.Effort); declared != "" {
				effort = declared
			}
		}
		substitution := runtimeskill.NewSubstitutionContext(
			skillItem,
			nil,
			projectDir,
			chatSessionID(session),
			effort,
			cfg.ArgumentSubstitutionEnabled(),
		)
		rendered, _ := runtimeskill.SubstituteSkillText(rawBody, substitution)
		rendered = strings.TrimSpace(rendered)
		if rendered == "" {
			diag.addSkipped(entry.name, skillMentionSkipReadError, "skill body is empty after substitution")
			continue
		}
		truncated := false
		if budget.MaxCharsPerSkill > 0 && runeCountString(rendered) > budget.MaxCharsPerSkill {
			rendered = truncateSkillMentionText(rendered, budget.MaxCharsPerSkill, skillMentionSingleTruncateMarker)
			truncated = true
		}

		head := skillMentionFragmentHead(entry.name, entry.path, noExplicitArguments)
		tail := skillMentionFragmentTail
		overhead := runeCountString(head) + runeCountString(tail)

		if budget.MaxCharsTotal > 0 {
			remaining := budget.MaxCharsTotal - usedChars
			if remaining-overhead <= skillMentionMinFragmentRunes {
				diag.addSkipped(entry.name, skillMentionSkipLimit, "mention_inject_total_chars exhausted")
				continue
			}
			if contentLimit := remaining - overhead; runeCountString(rendered) > contentLimit {
				rendered = truncateSkillMentionText(rendered, contentLimit, skillMentionTotalTruncateMarker)
				truncated = true
			}
		}

		body := head + rendered + tail
		if budget.HasTokenBudget {
			remainingTokens := budget.AvailableTokens - usedTokens
			// 余量连片段包装都放不下：丢弃本条（后续只会更小 → 自然全弃）。
			if remainingTokens <= 0 {
				diag.addSkipped(entry.name, skillMentionSkipLimit, "turn prompt budget exhausted")
				continue
			}
			allowedRunes := remainingTokens*4 - overhead
			if allowedRunes <= skillMentionMinFragmentRunes {
				diag.addSkipped(entry.name, skillMentionSkipLimit, "turn prompt budget too small for skill instructions")
				continue
			}
			if runeCountString(rendered) > allowedRunes {
				rendered = truncateSkillMentionText(rendered, allowedRunes, skillMentionBudgetTruncateMarker)
				truncated = true
				body = head + rendered + tail
			}
		}

		message := prompt.NewInstructionMessage(
			runtimetypes.InstructionScopeTurn,
			runtimetypes.InstructionSourceSkillInstructions,
			body,
		)
		if message == nil {
			continue
		}
		messages = append(messages, *message)
		usedChars += runeCountString(body)
		usedTokens += estimateSharedChatTokenCount(body) + 4
		diag.Injected = append(diag.Injected, skillMentionInjected{
			Name:      entry.name,
			Chars:     runeCountString(body),
			Truncated: truncated,
		})
	}
	return messages, diag
}

const skillMentionFragmentTail = "\n</skill>"

func skillMentionFragmentHead(name, path string, noExplicitArguments bool) string {
	var builder strings.Builder
	builder.WriteString("## Skill: ")
	builder.WriteString(name)
	builder.WriteByte('\n')
	if noExplicitArguments {
		builder.WriteString(skillMentionNoArgumentsNote)
		builder.WriteByte('\n')
	}
	builder.WriteString(`<skill name="`)
	builder.WriteString(escapeSkillMentionAttribute(name))
	builder.WriteString(`" path="`)
	builder.WriteString(escapeSkillMentionAttribute(path))
	builder.WriteString("\">\n")
	return builder.String()
}

func escapeSkillMentionAttribute(value string) string {
	return strings.ReplaceAll(value, `"`, "&quot;")
}

func truncateSkillMentionText(text string, limit int, marker string) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	markerRunes := []rune(marker)
	keep := limit - len(markerRunes)
	if keep < 0 {
		keep = 0
	}
	return string(runes[:keep]) + marker
}

func runeCountString(text string) int {
	return len([]rune(text))
}

// skillMentionTurnInput 是接入两条回合路径的输入；两条路径只在
// Interactive/SystemGenerated/UsedTokens/BudgetTokens 上取值不同。
type skillMentionTurnInput struct {
	Prompt string
	// Interactive 对应 plan §4.7：auto 模式仅交互式用户回合注入。
	Interactive bool
	// SystemGenerated 为 true 时不注入（goal/guardian/subagent/team 唤醒）。
	SystemGenerated bool
	// Pin 是本回合 `/skill` pin（用于 guide 顺序与技能去重）。
	Pin *skillTurnPin
	// UsedTokens 是既有历史 + pin guide 的 token 估算；用于 §4.11 本地裁决。
	UsedTokens int
	// BudgetTokens 是 active-turn prompt 预算（<=0 表示未知，跳过 token 裁决）。
	BudgetTokens int
}

// buildSkillMentionTurnMessages 是两条注入路径共用入口：门控（off/auto/on +
// folder-trust + 系统输入）→ 词法 → 解析 → 片段 → Q11 依赖提示 → 预算降级。
// 返回的 messages 直接按序追加到 pin guide 之后（guide 在前、mention 在后，
// 依赖提示在注入片段之后）。
func buildSkillMentionTurnMessages(ctx context.Context, session *ChatSession, input skillMentionTurnInput) ([]runtimetypes.Message, *skillMentionDiagnostics) {
	if session == nil {
		return nil, nil
	}
	gate := skillMentionResolveGate(session, input.Interactive)
	if gate == skillMentionGateDisabled {
		return nil, nil
	}
	if !strings.Contains(input.Prompt, "$") {
		// `$` 零开销短路：没有候选 token 时不读技能目录、不碰预算。
		return nil, nil
	}
	mentions := collectSkillMentionNames(input.Prompt, skillMentionKnownNames(session))
	if len(mentions) == 0 {
		return nil, nil
	}
	diag := &skillMentionDiagnostics{Mentioned: skillMentionNames(mentions)}
	if input.SystemGenerated {
		for _, mention := range mentions {
			diag.addSkipped(mention.Name, skillMentionSkipSystem, "system-generated turn does not inject skill mentions")
		}
		writeSkillMentionDebug(session, diag)
		return nil, diag
	}
	if gate == skillMentionGateUntrusted {
		// Q12：auto 且未信任项目默认禁用注入（显式 on 已在上面的 gate 放行）。
		for _, mention := range mentions {
			diag.addSkipped(mention.Name, skillMentionSkipUntrustedProject, skillMentionUntrustedProjectDetail)
		}
		writeSkillMentionDebug(session, diag)
		return nil, diag
	}
	selection, resolveDiag := resolveMentionedTextSkills(ctx, session, mentions, skillMentionPinnedFunctionSet(input.Pin))
	mergeSkillMentionDiagnostics(diag, resolveDiag)
	fragments, buildDiag := buildSkillMentionFragments(session, selection, resolveSkillMentionBudget(session, input.UsedTokens, input.BudgetTokens))
	mergeSkillMentionDiagnostics(diag, buildDiag)
	// Q11：聚合"技能声明工具不可用"提示（依赖不可用提及 + 已注入技能的工具检查），
	// 每回合至多一条、置于注入片段之后；只提示，不阻断、不安装。
	var note *skillMentionDependencyNote
	if selection != nil {
		note = mergeSkillMentionDependencyNotes(note, selection.dependencyNote)
	}
	note = mergeSkillMentionDependencyNotes(note, skillMentionSelectedMissingTools(session, selection))
	if note.hasEntries() {
		if message := prompt.NewInstructionMessage(
			runtimetypes.InstructionScopeTurn,
			runtimetypes.InstructionSourceSkillDependencies,
			note.noticeText(),
		); message != nil {
			fragments = append(fragments, *message)
		}
		appendSkillMentionNotice(diag, note.summaryText())
	}
	writeSkillMentionDebug(session, diag)
	return fragments, diag
}

func mergeSkillMentionDiagnostics(destination, source *skillMentionDiagnostics) {
	if destination == nil || source == nil {
		return
	}
	destination.Injected = append(destination.Injected, source.Injected...)
	destination.Skipped = append(destination.Skipped, source.Skipped...)
	appendSkillMentionNotice(destination, source.Notice)
}

func writeSkillMentionDebug(session *ChatSession, diag *skillMentionDiagnostics) {
	if session == nil || diag == nil {
		return
	}
	if len(diag.Injected) == 0 && len(diag.Skipped) == 0 {
		return
	}
	reasons := make([]string, 0, len(diag.Skipped))
	for _, skipped := range diag.Skipped {
		if skipped.Reason == "" {
			continue
		}
		entry := skipped.Name + ":" + skipped.Reason
		// 附上细节（截断）：线上只有 reason 时无法区分"未加载/解析失败/非文档"，
		// 这是上一轮 read_error 无法定位分支的直接原因。
		if detail := strings.TrimSpace(skipped.Detail); detail != "" {
			if runes := []rune(detail); len(runes) > 60 {
				detail = string(runes[:60]) + "…"
			}
			entry += "(" + detail + ")"
		}
		reasons = append(reasons, entry)
	}
	line := fmt.Sprintf("[skill-mention] mentioned=%d injected=%d skipped=%d",
		len(diag.Mentioned), len(diag.Injected), len(diag.Skipped))
	if len(reasons) > 0 {
		line += " reasons=" + strings.Join(reasons, ",")
	}
	if diag.Notice != "" {
		line += " notice=" + diag.Notice
	}
	writeSessionDebugInfo(session, line, false)
}

// chatSkillMentionInteractiveTurn 报告会话是否处于交互式用户回合
// （auto 模式的门控输入；headless/JSON 不注入）。
func chatSkillMentionInteractiveTurn(session *ChatSession) bool {
	return session != nil && !session.NoInteractive && !session.JSONOutput
}

// chatSkillMentionSystemGeneratedPrompt 报告 actor 路径上明确的系统生成提示。
// goal 续跑走 ContinueGoal；guardian/subagent/team 唤醒走 actor 内部通道而非
// Execute。这里做防御性识别，确保这些形态永不解析 mention。
func chatSkillMentionSystemGeneratedPrompt(prompt string) bool {
	trimmed := strings.TrimSpace(prompt)
	return trimmed == "" || trimmed == goalAutoContinuationPrompt
}

// skillMentionTurnEligible 是注入调用点的廉价短路：off/auto 非交互/提示中
// 根本没有 `$` 时不进入解析与 §4.11 预算计算，保证 flag=off 的回合开销为零。
// 与 buildSkillMentionTurnMessages 共用 skillMentionResolveGate：auto+未信任
// 项目仍放行到 build（只产出诊断、不注入），其余门控形状完全一致。
func skillMentionTurnEligible(session *ChatSession, prompt string, interactive bool) bool {
	if !strings.Contains(prompt, "$") {
		return false
	}
	return skillMentionResolveGate(session, interactive) != skillMentionGateDisabled
}

// ---------------------------------------------------------------------------
// Q11 依赖提示 + Q12 folder-trust 门控
// ---------------------------------------------------------------------------

// skillMentionUntrustedProjectDetail 是 auto 模式未信任项目的诊断 detail；
// 显式设置 skills_runtime.mention_injection=on 可覆盖（plan §4.8 第 7 条 / Q12）。
const skillMentionUntrustedProjectDetail = "project is not trusted (folder trust); run /trust grant or set skills_runtime.mention_injection=on to override"

// skillMentionUnavailableSkills 读取技能注册表的"依赖不可用"快照。
// 变量化以便单测注入：binding.manager 是具体 bootstrap manager，单测不便启动
// 全量 runtime；生产路径始终走 registry.UnavailableSkills()（Q11）。
var skillMentionUnavailableSkills = func(binding *skillsRuntimeBinding) []runtimeskill.UnavailableSkill {
	if binding == nil || binding.manager == nil {
		return nil
	}
	registry := binding.manager.Registry()
	if registry == nil {
		return nil
	}
	return registry.UnavailableSkills()
}

// skillMentionUnavailableIndex 构造小写技能名 → 不可用记录的索引
// （大小写不敏感命中；同名首次记录优先）。
func skillMentionUnavailableIndex(binding *skillsRuntimeBinding) map[string]runtimeskill.UnavailableSkill {
	items := skillMentionUnavailableSkills(binding)
	if len(items) == 0 {
		return nil
	}
	index := make(map[string]runtimeskill.UnavailableSkill, len(items))
	for _, item := range items {
		key := strings.ToLower(strings.TrimSpace(item.Name))
		if key == "" {
			continue
		}
		if _, exists := index[key]; !exists {
			index[key] = item
		}
	}
	return index
}

// skillMentionDependencyEntry 是一条"技能声明的工具不可用"记录。
type skillMentionDependencyEntry struct {
	name         string
	missingTools []string
}

// skillMentionDependencyNote 聚合单回合全部依赖不可用技能，最多产出一条提示。
type skillMentionDependencyNote struct {
	entries []skillMentionDependencyEntry
}

func (n *skillMentionDependencyNote) hasEntries() bool {
	return n != nil && len(n.entries) > 0
}

// appendSkillMentionDependencyNote 登记一个依赖不可用技能；同名合并缺失工具
// （保留首次记录的名称大小写与工具顺序）。
func appendSkillMentionDependencyNote(note *skillMentionDependencyNote, name string, tools []string) *skillMentionDependencyNote {
	name = strings.TrimSpace(name)
	if name == "" {
		return note
	}
	if note == nil {
		note = &skillMentionDependencyNote{}
	}
	normalized := normalizeSkillMentionToolNames(tools)
	key := strings.ToLower(name)
	for i := range note.entries {
		if strings.ToLower(note.entries[i].name) == key {
			note.entries[i].missingTools = mergeSkillMentionToolNames(note.entries[i].missingTools, normalized)
			return note
		}
	}
	note.entries = append(note.entries, skillMentionDependencyEntry{name: name, missingTools: normalized})
	return note
}

// mergeSkillMentionDependencyNotes 把 src 合并进 dst（用于"提及未加载"与
// "已加载但声明工具缺失"两条来源汇总）。
func mergeSkillMentionDependencyNotes(dst, src *skillMentionDependencyNote) *skillMentionDependencyNote {
	if !src.hasEntries() {
		return dst
	}
	for _, entry := range src.entries {
		dst = appendSkillMentionDependencyNote(dst, entry.name, entry.missingTools)
	}
	return dst
}

// noticeText 生成回合提示正文：简短列出技能与缺失工具，明确"不要调用、如需请
// 提示用户启用/安装（本回合不安装）"（Q11：不阻断、不安装）。
func (n *skillMentionDependencyNote) noticeText() string {
	if !n.hasEntries() {
		return ""
	}
	parts := make([]string, 0, len(n.entries))
	for _, entry := range n.entries {
		if len(entry.missingTools) == 0 {
			parts = append(parts, fmt.Sprintf("技能 %s 当前不可用", entry.name))
			continue
		}
		parts = append(parts, fmt.Sprintf("技能 %s 声明的工具 %s 当前不可用", entry.name, strings.Join(entry.missingTools, ", ")))
	}
	return strings.Join(parts, "；") + "；不要调用这些工具，如需请提示用户启用/安装（本回合不安装）"
}

// summaryText 是写回诊断 Notice 的汇总文案。
func (n *skillMentionDependencyNote) summaryText() string {
	if !n.hasEntries() {
		return ""
	}
	parts := make([]string, 0, len(n.entries))
	for _, entry := range n.entries {
		if len(entry.missingTools) == 0 {
			parts = append(parts, entry.name)
			continue
		}
		parts = append(parts, fmt.Sprintf("%s（缺少工具 %s）", entry.name, strings.Join(entry.missingTools, ", ")))
	}
	return "依赖不可用：" + strings.Join(parts, "；")
}

func normalizeSkillMentionToolNames(names []string) []string {
	normalized := make([]string, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, name)
	}
	return normalized
}

func mergeSkillMentionToolNames(existing, extra []string) []string {
	if len(extra) == 0 {
		return existing
	}
	seen := make(map[string]struct{}, len(existing)+len(extra))
	for _, name := range existing {
		seen[strings.ToLower(name)] = struct{}{}
	}
	merged := append([]string(nil), existing...)
	for _, name := range extra {
		key := strings.ToLower(strings.TrimSpace(name))
		if key == "" {
			continue
		}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		merged = append(merged, name)
	}
	return merged
}

// skillMentionSelectedMissingTools 检查已选中技能声明的工具（Q11）：
// binding.mcpRuntime 为 nil 或技能未声明工具时跳过检查；只收集不可用工具，
// 不影响正文注入（不阻断、不安装）。
func skillMentionSelectedMissingTools(session *ChatSession, selection *skillMentionSelection) *skillMentionDependencyNote {
	if session == nil || selection == nil {
		return nil
	}
	binding := skillMentionBinding(session)
	if binding == nil || binding.mcpRuntime == nil {
		return nil
	}
	var note *skillMentionDependencyNote
	for _, entry := range selection.skills {
		skillItem := entry.skill
		if skillItem == nil || len(skillItem.Tools) == 0 {
			continue
		}
		var missing []string
		for _, toolName := range normalizeSkillMentionToolNames(skillItem.Tools) {
			if _, err := binding.mcpRuntime.FindTool(toolName); err != nil {
				missing = append(missing, toolName)
			}
		}
		if len(missing) == 0 {
			continue
		}
		note = appendSkillMentionDependencyNote(note, entry.name, missing)
	}
	return note
}

// appendSkillMentionNotice 把一条 Notice 追加进诊断（分号分隔，保持既有
// mergeSkillMentionDiagnostics 的拼接风格）。
func appendSkillMentionNotice(diag *skillMentionDiagnostics, notice string) {
	notice = strings.TrimSpace(notice)
	if diag == nil || notice == "" {
		return
	}
	if diag.Notice == "" {
		diag.Notice = notice
		return
	}
	diag.Notice += "; " + notice
}

// skillMentionInjectionGate 是 Q12 门控结论。
type skillMentionInjectionGate int

const (
	skillMentionGateDisabled skillMentionInjectionGate = iota
	skillMentionGateUntrusted
	skillMentionGateEnabled
)

// skillMentionResolveGate 统一注入入口与廉价短路的门控判定：
//   - off / auto 非交互（含 headless/JSON）：disabled（零开销，不解析）；
//   - auto 且未信任项目：untrusted（只产出诊断，不注入）；
//   - on（未信任也显式覆盖）与 auto+交互+已信任：enabled。
//
// folder-trust feature off 时 sessionProjectScopeAllowed 返回 true，照常注入。
func skillMentionResolveGate(session *ChatSession, interactive bool) skillMentionInjectionGate {
	if session == nil {
		return skillMentionGateDisabled
	}
	cfg := skillRuntimeConfig(session.Config)
	// 与 SkillsRuntimeConfig.MentionInjectionEnabled 同一口径（on 恒开、
	// auto 仅交互式、off/未知恒关），避免两处语义漂移。
	if !cfg.MentionInjectionEnabled(interactive) {
		return skillMentionGateDisabled
	}
	if cfg.MentionInjectionMode() == config.SkillMentionInjectionAuto && !sessionProjectScopeAllowed(session) {
		return skillMentionGateUntrusted
	}
	return skillMentionGateEnabled
}
