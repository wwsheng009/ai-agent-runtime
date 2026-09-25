package commands

import (
	"strings"

	runtimeskill "github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

// Agent Skills 的 allowed-tools / disallowed-tools 在本运行时的生效点：
// `/skill` 回合物化 pin 时把它折算成"本回合工具面收窄"。
//
// 语义与既有策略保持一致：**只减不增**。声明 allowed-tools 只会把本回合
// selection 收窄到"允许的名字 ∪ skill 函数本身"，绝不放大会话已允许的工具面；
// disallowed-tools 恒剔除。名字按函数目录（大小写不敏感）匹配，Claude Code 风格
// 的括号限定（`Bash(git:*)`）取括号前的工具名，未命中的声明记入诊断列表，
// 沿用既有 unavailable 语义（不阻断回合）。

// skillToolDeclaration 是一条工具声明归一化后的结果。
// Qualifier 保留括号里的限定内容（如 `git:*`），仅用于诊断：本运行时没有
// 命令级准入层，故不做命令白名单求交，避免给出"已限制命令"的错觉。
type skillToolDeclaration struct {
	Raw       string
	Name      string
	Qualifier string
}

func parseSkillToolDeclaration(raw string) skillToolDeclaration {
	declaration := skillToolDeclaration{Raw: strings.TrimSpace(raw)}
	value := declaration.Raw
	if value == "" {
		return declaration
	}
	if index := strings.Index(value, "("); index >= 0 {
		declaration.Qualifier = strings.TrimSpace(strings.Trim(value[index:], "()"))
		value = strings.TrimSpace(value[:index])
	}
	declaration.Name = strings.TrimSpace(value)
	return declaration
}

// toolDeclarationLookupKey 与函数目录的 schema 名字口径一致：小写 + 去空白。
func toolDeclarationLookupKey(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// resolveSkillToolName 在函数目录 schema 索引里按名字（大小写不敏感）解析声明，
// 返回目录中的规范名。
func resolveSkillToolName(schemas map[string]map[string]interface{}, name string) (string, bool) {
	key := toolDeclarationLookupKey(name)
	if key == "" {
		return "", false
	}
	if schema, ok := schemas[key]; ok {
		if resolved := strings.TrimSpace(schemaLookupKey(schema)); resolved != "" {
			// schemaLookupKey 已是小写口径，回填目录里的原始名字。
			if raw, hasRaw := schema["name"].(string); hasRaw && strings.TrimSpace(raw) != "" {
				return strings.TrimSpace(raw), true
			}
			return resolved, true
		}
	}
	return "", false
}

// applySkillToolRestriction 把 skill 声明的 allowed-tools / disallowed-tools 与
// model / effort 建议物化到 pin 上。
func applySkillToolRestriction(pin *skillTurnPin, skill *runtimeskill.Skill, schemas map[string]map[string]interface{}) {
	if pin == nil || skill == nil || skill.Codex == nil {
		return
	}
	codex := skill.Codex
	pin.SkillModel = strings.TrimSpace(codex.Model)
	pin.SkillEffort = strings.TrimSpace(codex.Effort)

	for _, raw := range codex.DisallowedTools {
		declaration := parseSkillToolDeclaration(raw)
		if declaration.Name == "" {
			continue
		}
		// 剔除不需要目录命中：名字对得上就剔，对不上也无害。
		pin.DisallowedFunctions = appendUniqueCaseInsensitive(pin.DisallowedFunctions, declaration.Name)
	}
	for _, raw := range codex.AllowedTools {
		declaration := parseSkillToolDeclaration(raw)
		if declaration.Name == "" {
			continue
		}
		resolved, ok := resolveSkillToolName(schemas, declaration.Name)
		if !ok {
			pin.UnavailableAllowed = appendUniqueCaseInsensitive(pin.UnavailableAllowed, declaration.Raw)
			continue
		}
		pin.AllowedFunctions = appendUniqueCaseInsensitive(pin.AllowedFunctions, resolved)
		if declaration.Qualifier != "" {
			pin.QualifiedAllowed = appendUniqueCaseInsensitive(pin.QualifiedAllowed, declaration.Raw)
		}
	}
}

// restrictFunctionSelection 按 pin 收窄本回合函数选择，返回新副本（稳定快照不被修改）。
// allowed 非空时只保留 allowed ∪ skill 函数本身；disallowed 恒剔除。
func restrictFunctionSelection(selection *aicliFunctionSelection, pin *skillTurnPin) *aicliFunctionSelection {
	if selection == nil || pin == nil {
		return selection
	}
	restrict := len(pin.AllowedFunctions) > 0
	if !restrict && len(pin.DisallowedFunctions) == 0 {
		return selection
	}
	allowed := map[string]bool{}
	if restrict {
		for _, name := range pin.AllowedFunctions {
			allowed[toolDeclarationLookupKey(name)] = true
		}
		// skill 函数本身恒保留：本回合正是它触发的，模型可能需要再次调用/续写。
		for _, name := range pin.PinnedFunctions {
			if strings.HasPrefix(strings.TrimSpace(name), skillFunctionPrefix) {
				allowed[toolDeclarationLookupKey(name)] = true
			}
		}
	}
	denied := map[string]bool{}
	for _, name := range pin.DisallowedFunctions {
		denied[toolDeclarationLookupKey(name)] = true
	}
	keep := func(name string) bool {
		key := toolDeclarationLookupKey(name)
		if key == "" {
			return true
		}
		if denied[key] {
			return false
		}
		if !restrict {
			return true
		}
		return allowed[key]
	}

	restricted := cloneFunctionSelection(selection)
	if restricted == nil {
		return selection
	}
	restricted.BuiltinFunctions = filterFunctionNames(restricted.BuiltinFunctions, keep)
	restricted.SkillFunctions = filterFunctionNames(restricted.SkillFunctions, keep)
	restricted.FinalFunctionNames = filterFunctionNames(restricted.FinalFunctionNames, keep)
	if len(restricted.Schemas) > 0 {
		schemas := make([]map[string]interface{}, 0, len(restricted.Schemas))
		for _, schema := range restricted.Schemas {
			if keep(schemaLookupKey(schema)) {
				schemas = append(schemas, schema)
			}
		}
		restricted.Schemas = schemas
	}
	return restricted
}

func filterFunctionNames(names []string, keep func(string) bool) []string {
	if len(names) == 0 {
		return names
	}
	filtered := make([]string, 0, len(names))
	for _, name := range names {
		if keep(name) {
			filtered = append(filtered, name)
		}
	}
	return filtered
}

// skillTurnHints 生成写进本回合 guide 的执行建议块（只声明才出现）。
// 建议不覆盖会话设置：模型/推理强度仍由会话决定，这里只提供 skill 作者的意图。
func skillTurnHints(pin *skillTurnPin) string {
	if pin == nil {
		return ""
	}
	lines := make([]string, 0, 4)
	if pin.SkillModel != "" {
		lines = append(lines, "- model: "+pin.SkillModel+"（skill 声明，建议值；不覆盖当前会话模型）")
	}
	if pin.SkillEffort != "" {
		lines = append(lines, "- effort: "+pin.SkillEffort+"（skill 声明，建议值）")
	}
	if len(pin.UnavailableAllowed) > 0 {
		lines = append(lines, "- unavailable allowed-tools: "+strings.Join(pin.UnavailableAllowed, ", ")+"（不在当前函数目录，已忽略）")
	}
	if len(pin.QualifiedAllowed) > 0 {
		lines = append(lines, "- allowed-tools 限定（本运行时无命令级准入层，仅工具名生效）: "+strings.Join(pin.QualifiedAllowed, ", "))
	}
	if len(pin.DisallowedFunctions) > 0 {
		lines = append(lines, "- disallowed-tools: "+strings.Join(pin.DisallowedFunctions, ", ")+"（本回合已从工具面移除）")
	}
	if len(lines) == 0 {
		return ""
	}
	return "## Skill execution hints\n" + strings.Join(lines, "\n")
}
