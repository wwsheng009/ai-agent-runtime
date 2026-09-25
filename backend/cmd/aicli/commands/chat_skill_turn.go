package commands

import (
	"fmt"
	"os"
	"strings"

	logpkg "github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
	runtimeskill "github.com/wwsheng009/ai-agent-runtime/internal/skill"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// skillTurnPin 是一次 `/skill` 回合的物化叠加内容：ProgramGuide system 消息 +
// 本回合函数面叠加（skill 函数本身 + skill 声明的、且在函数目录中命中的程序）。
// 它只属于触发它的那一个回合，不得写回会话级稳定工具面缓存。
type skillTurnPin struct {
	Guide             string
	PinnedFunctions   []string
	PinnedTools       []runtimetypes.ToolDefinition
	UnmatchedPrograms []string
	// Agent Skills 的 allowed-tools / disallowed-tools 折算出的本回合工具面收窄
	// （见 chat_skill_tool_restriction.go）：AllowedFunctions 非空时只保留这些名字
	// ∪ skill 函数本身；DisallowedFunctions 恒剔除。二者都只减不增。
	AllowedFunctions    []string
	DisallowedFunctions []string
	// UnavailableAllowed 记录 allowed-tools 里在当前函数目录中不存在（或未命中）
	// 的原始声明，QualifiedAllowed 记录带括号限定的声明，均只用于诊断与提示。
	UnavailableAllowed []string
	QualifiedAllowed   []string
	// SkillModel / SkillEffort 是 skill 声明的执行建议（不强制覆盖会话设置）。
	SkillModel  string
	SkillEffort string
}

// buildSkillTurnVisiblePrompt 还原提交到会话与界面的完整命令文本。
func buildSkillTurnVisiblePrompt(requestedName, rawPrompt string) string {
	name := strings.TrimSpace(requestedName)
	prompt := strings.TrimSpace(rawPrompt)
	if name == "" {
		return "/skill"
	}
	if prompt == "" {
		return "/skill " + name
	}
	return "/skill " + name + " " + prompt
}

// stripSkillDirectOption 剥离 payload 前导的 `--direct` 选项，返回剩余文本与是否
// 命中。只识别命令名之前的选项，避免把 skill 参数里的同名字面量误当开关。
func stripSkillDirectOption(payload string) (string, bool) {
	trimmed := strings.TrimSpace(payload)
	if trimmed == "" {
		return "", false
	}
	lower := strings.ToLower(trimmed)
	if lower != "--direct" && !strings.HasPrefix(lower, "--direct ") && !strings.HasPrefix(lower, "--direct\t") {
		return trimmed, false
	}
	return strings.TrimSpace(trimmed[len("--direct"):]), true
}

// stashPendingSkillTurn 登记一次性 pin；dispatch 在提交回合前调用，回合开始即消费。
func stashPendingSkillTurn(session *ChatSession, request *SendSkillTurnRequest) {
	if session == nil || request == nil {
		return
	}
	session.pendingSkillTurn.Store(request)
}

// consumePendingSkillTurn 读取并清空一次性 pin（消费即焚），返回 nil 表示当前回合
// 不是 `/skill` 回合。
func consumePendingSkillTurn(session *ChatSession) *SendSkillTurnRequest {
	if session == nil {
		return nil
	}
	return session.pendingSkillTurn.Swap(nil)
}

// clearPendingSkillTurn 丢弃尚未消费的 pin（发送失败/回合未启动时兜底）。
func clearPendingSkillTurn(session *ChatSession) {
	if session == nil {
		return
	}
	session.pendingSkillTurn.Store(nil)
}

// consumeSkillTurnPin 消费一次性 pin 并物化为 guide/工具叠加。pin 不属于本回合、
// 目录不可用或解析失败时返回 nil：回合照常执行，不因叠加内容阻断用户请求。
func consumeSkillTurnPin(session *ChatSession) *skillTurnPin {
	request := consumePendingSkillTurn(session)
	if request == nil {
		return nil
	}
	pin, err := resolveSkillTurnPin(session, request)
	if err != nil {
		writeSessionDebugInfo(session, fmt.Sprintf("[skill-turn] 解析 skill 回合 pin 失败: %v", err), false)
		return nil
	}
	if pin != nil && len(pin.UnmatchedPrograms) > 0 {
		writeSessionDebugInfo(session, fmt.Sprintf("[skill-turn] skill=%s 未命中的程序（仅 pin 命中项）: %s",
			request.SkillName, strings.Join(pin.UnmatchedPrograms, ", ")), false)
	}
	return pin
}

// sendSkillTurnRequest 是 `/skill` 默认路径的 post-commit 发送助手：先登记本回合
// pin，再经既有 send 管线把 `/skill <name> <args>` 作为普通用户消息提交。pin 只对
// 这一条 send 生效，发送返回后无条件清除，避免残留污染后续回合。
func sendSkillTurnRequest(session *ChatSession, request *SendSkillTurnRequest) error {
	if session == nil || request == nil {
		return nil
	}
	visible := strings.TrimSpace(request.VisiblePrompt)
	if visible == "" {
		visible = buildSkillTurnVisiblePrompt(request.SkillName, request.Prompt)
	}
	if strings.TrimSpace(visible) == "" {
		return nil
	}
	stashPendingSkillTurn(session, request)
	defer clearPendingSkillTurn(session)

	response, err := sendMessage(session, visible)
	if err != nil {
		return err
	}
	finishSuccessfulChatSend(session, response, session.NoInteractive)
	return nil
}

// resolvedTurnSkill 解析 SkillFunction 对应的完整 skill 定义（hydrated 优先）。
// ProgramGuide/ProgramTools 都依赖完整定义，summary-only 的条目退化为 nil。
func (f *SkillFunction) resolvedTurnSkill() *runtimeskill.Skill {
	if f == nil {
		return nil
	}
	if f.skillResolver != nil {
		if resolved, err := f.skillResolver(); err == nil && resolved != nil {
			return resolved
		}
	}
	return f.skill
}

// resolveSkillTurnPin 为 `/skill` 回合构造 guide 与工具叠加。只有函数目录中真实
// 存在的名字才会进入 pin（D1）：skill 函数本身恒 pin，ProgramTools 命中的才 pin，
// 未命中的程序记录在 UnmatchedPrograms（不阻断回合）。
func resolveSkillTurnPin(session *ChatSession, request *SendSkillTurnRequest) (*skillTurnPin, error) {
	if session == nil || request == nil {
		return nil, nil
	}
	functionName := strings.TrimSpace(request.SkillName)
	if functionName == "" {
		return nil, nil
	}
	catalog := ensureFunctionCatalog(session)
	if catalog == nil || catalog.Registry() == nil {
		return nil, fmt.Errorf("Function Catalog: 未初始化")
	}

	var skillItem *runtimeskill.Skill
	if binding := catalog.SkillsBinding(); binding != nil {
		if fn, ok := binding.skillFunctions[functionName]; ok && fn != nil {
			skillItem = fn.resolvedTurnSkill()
		}
	}

	pin := &skillTurnPin{PinnedFunctions: []string{functionName}}
	if skillItem != nil {
		// SK-7：文档模式技能不走 ProgramGuide，把正文注入上下文，由模型用既有工具完成任务。
		cfg := skillRuntimeConfig(session.Config)
		// Agent Skills 占位符替换：单趟替换，插入内容不再解析。灰度开关
		// skills_runtime.argument_substitution（默认 on）。
		projectDir := ""
		if cwd, err := os.Getwd(); err == nil {
			projectDir = cwd
		}
		// skill 声明的 effort 优先作为本回合 ${effort} 取值（建议值语义：只影响
		// skill 正文与占位符，不覆盖会话推理强度）。
		substitutionEffort := strings.TrimSpace(session.ReasoningEffort)
		if skillItem.Codex != nil {
			if declared := strings.TrimSpace(skillItem.Codex.Effort); declared != "" {
				substitutionEffort = declared
			}
		}
		substitutionCtx := runtimeskill.NewSubstitutionContext(
			skillItem,
			runtimeskill.SplitSkillArguments(request.Prompt),
			projectDir,
			chatSessionID(session),
			substitutionEffort,
			cfg.ArgumentSubstitutionEnabled(),
		)
		if skillItem.IsDocumentModeEnabled(cfg != nil && cfg.DocumentModeAuto()) {
			if body := strings.TrimSpace(skillItem.Body); body != "" {
				if rendered, _ := runtimeskill.SubstituteSkillText(body, substitutionCtx); strings.TrimSpace(rendered) != "" {
					body = rendered
				}
				pin.Guide = "## Skill instructions (document mode: " + functionName + ")\n" + body
			}
		} else {
			guide, _ := runtimeskill.SubstituteSkillText(runtimeskill.ProgramGuide(skillItem), substitutionCtx)
			pin.Guide = guide
		}
	}
	if strings.TrimSpace(pin.Guide) == "" {
		// 没有完整 skill 定义（binding 缺失或 summary-only）时 guide 退化为函数描述，
		// 保证模型至少拿到 skill 名称与用途，而不是一个空回合。
		if description := skillTurnFunctionDescription(catalog, functionName); description != "" {
			pin.Guide = fmt.Sprintf("Skill program guide:\n- skill: %s\n- description: %s", functionName, description)
		}
	}
	// SK-1/SK-2：前附全量 catalog（所有可用技能）+ 纪律块，镜像 Codex 常驻 directory。
	if catalogBlock := buildSkillCatalogText(session, catalog); catalogBlock != "" {
		if pin.Guide != "" {
			pin.Guide = catalogBlock + "\n\n" + pin.Guide
		} else {
			pin.Guide = catalogBlock
		}
	}

	schemas := catalogSchemaIndex(catalog)
	if definition, ok := toolDefinitionForCatalogSchema(schemas, functionName); ok {
		pin.PinnedTools = append(pin.PinnedTools, definition)
	}
	if skillItem != nil {
		for _, program := range runtimeskill.ProgramTools(skillItem) {
			name := strings.TrimSpace(program)
			if name == "" || strings.EqualFold(name, functionName) {
				continue
			}
			definition, ok := toolDefinitionForCatalogSchema(schemas, name)
			if !ok {
				pin.UnmatchedPrograms = append(pin.UnmatchedPrograms, name)
				continue
			}
			pin.PinnedFunctions = appendUniqueCaseInsensitive(pin.PinnedFunctions, name)
			pin.PinnedTools = append(pin.PinnedTools, definition)
		}
	}
	// SK-3: record the explicit /skill invocation at dispatch time, regardless of
	// which pinned tool (skill function or program like bash) finishes the turn.
	// Agent Skills 标准字段：allowed-tools / disallowed-tools 折算成工具面收窄，
	// model / effort 作为建议写进 guide（见 chat_skill_tool_restriction.go）。
	if skillItem != nil {
		applySkillToolRestriction(pin, skillItem, schemas)
		if hints := skillTurnHints(pin); hints != "" {
			if strings.TrimSpace(pin.Guide) == "" {
				pin.Guide = hints
			} else {
				pin.Guide = pin.Guide + "\n\n" + hints
			}
		}
	}
	publishSkillTurnInvocation(session, functionName, skillItem)
	return pin, nil
}

// skillTurnFunctionDescription 依次从 SkillFunction 与函数目录 schema 里取描述，
// 供 guide 在完整 skill 定义不可用时降级使用。
func skillTurnFunctionDescription(catalog *aicliFunctionCatalog, functionName string) string {
	if fn := skillFunctionForName(catalog, functionName); fn != nil {
		if description := strings.TrimSpace(fn.Description()); description != "" {
			return description
		}
	}
	if catalog != nil {
		if schema := catalog.SkillSchema(strings.TrimSpace(functionName)); len(schema) > 0 {
			if description, _ := schema["description"].(string); strings.TrimSpace(description) != "" {
				return strings.TrimSpace(description)
			}
		}
	}
	return ""
}

// skillFunctionForName 在 skills binding 缺失时按函数名回退查找 SkillFunction。
func skillFunctionForName(catalog *aicliFunctionCatalog, functionName string) *SkillFunction {
	if catalog == nil {
		return nil
	}
	binding := catalog.SkillsBinding()
	if binding == nil {
		return nil
	}
	if fn, ok := binding.skillFunctions[functionName]; ok {
		return fn
	}
	for name, fn := range binding.skillFunctions {
		if strings.EqualFold(strings.TrimSpace(name), strings.TrimSpace(functionName)) {
			return fn
		}
	}
	return nil
}

// catalogSchemaIndex 构建"函数名 → schema"索引：builtin 函数与 skill 函数同表，
// 供 pin 只挑真实存在的名字。
func catalogSchemaIndex(catalog *aicliFunctionCatalog) map[string]map[string]interface{} {
	if catalog == nil {
		return nil
	}
	index := make(map[string]map[string]interface{})
	for _, schema := range catalog.BuiltinSchemas() {
		if key := schemaLookupKey(schema); key != "" {
			index[key] = schema
		}
	}
	for _, name := range catalog.SkillFunctionNames() {
		schema := catalog.SkillSchema(name)
		if key := schemaLookupKey(schema); key != "" {
			index[key] = schema
		}
	}
	return index
}

func schemaLookupKey(schema map[string]interface{}) string {
	if len(schema) == 0 {
		return ""
	}
	name, _ := schema["name"].(string)
	return strings.ToLower(strings.TrimSpace(name))
}

func toolDefinitionForCatalogSchema(schemas map[string]map[string]interface{}, name string) (runtimetypes.ToolDefinition, bool) {
	key := strings.ToLower(strings.TrimSpace(name))
	if key == "" || len(schemas) == 0 {
		return runtimetypes.ToolDefinition{}, false
	}
	schema, ok := schemas[key]
	if !ok {
		return runtimetypes.ToolDefinition{}, false
	}
	return toolDefinitionFromSchema(schema)
}

// overlayPinnedFunctions 把 pin 叠加到本回合函数选择上，返回新副本；稳定快照
// （session.stableSharedToolSelection）始终不被修改。
func overlayPinnedFunctions(selection *aicliFunctionSelection, pin *skillTurnPin) *aicliFunctionSelection {
	// 没有可叠加工具、也没有声明收窄时原样返回；只要声明了 allowed/disallowed，
	// 即使本回合没有 pin 到任何工具（例如 skill 函数不在目录里）也要走收窄。
	if pin == nil || (len(pin.PinnedTools) == 0 && len(pin.AllowedFunctions) == 0 && len(pin.DisallowedFunctions) == 0) {
		return selection
	}
	overlaid := cloneFunctionSelection(selection)
	if overlaid == nil {
		overlaid = &aicliFunctionSelection{}
	}
	existing := make(map[string]int, len(overlaid.Schemas))
	for index, schema := range overlaid.Schemas {
		if key := schemaLookupKey(schema); key != "" {
			existing[key] = index
		}
	}
	for _, definition := range pin.PinnedTools {
		schema, ok := toolDefinitionSchema(definition)
		if !ok {
			continue
		}
		key := schemaLookupKey(schema)
		if index, exists := existing[key]; exists {
			overlaid.Schemas[index] = schema
			continue
		}
		existing[key] = len(overlaid.Schemas)
		overlaid.Schemas = append(overlaid.Schemas, schema)
	}
	for _, name := range pin.PinnedFunctions {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		overlaid.FinalFunctionNames = appendUniqueCaseInsensitive(overlaid.FinalFunctionNames, name)
		if strings.HasPrefix(name, skillFunctionPrefix) {
			overlaid.SkillFunctions = appendUniqueCaseInsensitive(overlaid.SkillFunctions, name)
		}
	}
	// 收窄必须在叠加之后：先让 pin 里的程序进入函数面，再按 skill 声明的
	// allowed-tools / disallowed-tools 做减法（只减不增）。
	return restrictFunctionSelection(overlaid, pin)
}

// toolDefinitionSchema 把单个工具定义还原为函数目录使用的 schema 结构。
func toolDefinitionSchema(definition runtimetypes.ToolDefinition) (map[string]interface{}, bool) {
	if strings.TrimSpace(definition.Name) == "" {
		return nil, false
	}
	schema := map[string]interface{}{
		"name":        definition.Name,
		"description": definition.Description,
		"parameters":  cloneToolParametersSchema(definition.Parameters),
	}
	if len(definition.Metadata) > 0 {
		schema["metadata"] = cloneFunctionSchema(definition.Metadata)
	}
	return schema, true
}

// buildSkillCatalogText 构建 aicli /skill 回合的技能目录文本（SK-1/SK-2）。
// 镜像 Codex 的常驻 directory：name + description + 定位符，受预算与灰度控制；
// 仅在配置可用且注册表非空时返回，catalog 条目永不因预算消失，只裁剪描述。
func buildSkillCatalogText(session *ChatSession, catalog *aicliFunctionCatalog) string {
	if session == nil || catalog == nil {
		return ""
	}
	cfg := skillRuntimeConfig(session.Config)
	if cfg == nil {
		return ""
	}
	var summaries []*runtimeskill.SkillSummary
	if binding := catalog.SkillsBinding(); binding != nil {
		if binding.manager != nil {
			if reg := binding.manager.Registry(); reg != nil {
				summaries = reg.ListSummaries()
			}
		}
		// 退化：manager/registry 缺失时，从已暴露的 skill 函数收集摘要。
		if len(summaries) == 0 {
			for _, fn := range binding.skillFunctions {
				if fn != nil && fn.summary != nil {
					summaries = append(summaries, fn.summary)
				}
			}
		}
	}
	if len(summaries) == 0 {
		return ""
	}
	entries := runtimeskill.BuildCatalogEntries(summaries)
	if len(entries) == 0 {
		return ""
	}
	budget := runtimeskill.CatalogBudget{Characters: cfg.CatalogBudget()}
	body, report := runtimeskill.RenderSkillCatalogWithOptions(entries, budget, cfg.DisciplineBlockEnabled())
	if report.Degraded() {
		logpkg.Warn("skill catalog degraded to fit budget", logpkg.String("report", report.String()))
	}
	if strings.TrimSpace(body) == "" {
		return ""
	}
	return body
}
