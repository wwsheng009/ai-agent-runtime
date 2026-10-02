package commands

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
)

const (
	skillCatalogSelectionPrompt = "编号/名称 (回车=1, q取消): "
	skillExecutionPrompt        = "请输入 prompt (q取消): "
)

func buildChatSkillCatalogDocument(skills []aicliFunctionDescriptorReport, query string, roots []string) render.Document {
	return textLinesDocument(buildSkillCatalogLines(skills, query, "", roots))
}

func handleSkillsMenuCommand(session *ChatSession, command string) bool {
	if unifiedDirectInteractiveOutput(session) {
		if result, handled := executeStructuredSkillsMenuCommand(session, command); handled {
			renderErr := renderChatCommandResult(session, result, false)
			if renderErr == nil {
				// 批次 5（D-E）：picker 效应经 CommandResult.Screen 统一派发。
				dispatchChatScreenEffects(session, result)
			}
			if renderErr == nil && result.RestoreComposerDraft != "" {
				_ = restoreChatRetryDraft(session, result.RestoreComposerDraft)
			}
			return false
		}
		// executeStructuredSkillsMenuCommand handles every /skills variant today,
		// so this branch is defensive. The deny-list fence no longer contains
		// /skills (it is fully migrated), so rejectUnifiedInteractiveLegacyCommand
		// would fail open into the legacy stdout handler; fail closed instead.
		_ = renderChatCommandResult(session, commandTextResult("错误: /skills 变体无法通过统一渲染命令通道处理"), false)
		return false
	}
	if rejectUnifiedInteractiveLegacyCommand(session, "/skills") {
		return false
	}
	if session == nil {
		fmt.Println("错误: 当前没有活动会话")
		return false
	}

	query, jsonOutput := extractCommandArgumentOptions(command)
	query = strings.TrimSpace(query)
	useJSON := jsonOutput || session.JSONOutput

	// per-skill 启停：/skills disable|enable <name>（写配置 + 运行面热刷新）。
	if enable, name, ok := parseSkillToggleQuery(query); ok {
		message, err := runSkillToggleCommand(session, enable, name)
		if err != nil {
			message = formatCommandError(err.Error(), useJSON)
		}
		fmt.Println(message)
		return false
	}

	catalog := ensureFunctionCatalog(session)
	if catalog == nil || catalog.Registry() == nil {
		fmt.Println(formatCommandError("Function Catalog: 未初始化", useJSON))
		return false
	}

	report := buildFunctionCatalogReport(catalog)
	if report == nil {
		fmt.Println(formatCommandError("Function Catalog: 未初始化", useJSON))
		return false
	}

	skills := chatSkillCatalogEntries(session, catalog, report, query)
	if useJSON {
		payload := struct {
			Count  int                             `json:"count"`
			Query  string                          `json:"query,omitempty"`
			Skills []aicliFunctionDescriptorReport `json:"skills,omitempty"`
		}{
			Count:  len(skills),
			Query:  query,
			Skills: append([]aicliFunctionDescriptorReport(nil), skills...),
		}
		fmt.Println(marshalIndentedJSON(payload))
		return false
	}

	if session.NoInteractive {
		printSkillCatalogReport(skills, query, sessionSkillRoots(session))
		return false
	}

	if len(skills) == 0 {
		fmt.Println("错误: 未找到匹配 skill")
		fmt.Println("输入 /functions 查看全部 function catalog")
		return false
	}

	beginDirectInteractiveOutput(session)
	selected, err := promptSkillCatalogSelection(session, skills, query)
	if err != nil {
		fmt.Printf("错误: %v\n", err)
		return false
	}
	if selected == nil {
		fmt.Println("已取消选择 skill")
		return false
	}

	prompt, err := promptSkillExecutionInput(session)
	if err != nil {
		fmt.Printf("错误: %v\n", err)
		return false
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		fmt.Println("已取消执行 skill")
		return false
	}

	beginDirectInteractiveOutput(session)
	return handleDirectSkillCommand(session, fmt.Sprintf("/skill %s %s", selected.FunctionName, prompt))
}

func printSkillCatalogReport(skills []aicliFunctionDescriptorReport, query string, roots []string) {
	for _, line := range buildSkillCatalogLines(skills, query, "", roots) {
		fmt.Println(line)
	}
}

// buildSkillCatalogLines 渲染 /skills 清单正文。roots 是本次会话实际扫描的 skill
// 目录（加载时快照），直接回显在标题行：清单必须能回答“工作区 .agents/skills 与
// 用户 ~/.aicli/skills 里，我改的那个 skill 到底被扫到了没有”。
func buildSkillCatalogLines(skills []aicliFunctionDescriptorReport, query, warning string, roots []string) []string {
	lines := []string{
		fmt.Sprintf("Skill Catalog: total=%d", len(skills)),
	}
	if len(roots) > 0 {
		lines = append(lines, "Roots: "+strings.Join(roots, ", "))
	}
	if query != "" {
		lines = append(lines, "Filter: "+query)
	}
	if warning != "" {
		lines = append(lines, warning)
	}
	if len(skills) == 0 {
		lines = append(lines, "  <none>")
		return lines
	}

	labelWidth := 0
	for _, item := range skills {
		if w := ui.DisplayWidth(skillCatalogEntryLabel(item)); w > labelWidth {
			labelWidth = w
		}
	}
	if labelWidth < 8 {
		labelWidth = 8
	}
	if labelWidth > 24 {
		labelWidth = 24
	}

	width := ui.GetTerminalWidth()
	for i, item := range skills {
		lines = append(lines, formatSkillCatalogItemLine(i, item, labelWidth, width))
	}
	return lines
}

func promptSkillCatalogSelection(session *ChatSession, skills []aicliFunctionDescriptorReport, query string) (*aicliFunctionDescriptorReport, error) {
	if len(skills) == 0 {
		return nil, nil
	}

	usePopup := useRuntimeSelectionPopup(session)
	if usePopup {
		defer clearRuntimeSelectionPopup(session)
	}

	warning := ""
	for {
		// 停用的 skill 不在函数面里，补成"已停用"行后同一个入口既能停用也能启用。
		skills = buildSkillPickerCatalogEntries(session, skills)
		lines := buildSkillCatalogLines(skills, query, warning, sessionSkillRoots(session))
		if usePopup {
			showRuntimeSelectionPopup(session, lines, skillCatalogSelectionPrompt)
		} else {
			printChatSelectionSection("选择 Skill")
			for _, line := range lines {
				printChatSelectionLine("%s", line)
			}
			printChatSelectionPrompt(skillCatalogSelectionPrompt)
		}

		text, err := chatInteractiveReadPriorityLineWithPrompt(session, context.Background(), skillCatalogSelectionPrompt)
		if !usePopup {
			fmt.Println()
		}
		if err != nil {
			return nil, err
		}

		choice := strings.TrimSpace(normalizeQueuedInputLine(text))
		warning = ""
		switch strings.ToLower(choice) {
		case "", "1":
			if skills[0].Disabled {
				warning = "  该 skill 已停用，输入 x <编号> 可启用"
				continue
			}
			return &skills[0], nil
		case "q", "quit", "cancel", "exit":
			return nil, nil
		}

		// x <编号|名称>：与统一全屏选择器的 x 键同义（写配置 + 热刷新）。
		if target, isToggle := parseSkillCatalogToggleInput(choice); isToggle {
			index, found := findSkillCatalogEntryIndex(skills, target)
			if !found {
				warning = "  未找到该 skill"
				continue
			}
			entry := skills[index]
			message, toggleErr := runSkillToggleCommand(session, entry.Disabled, skillCatalogEntryLabel(entry))
			if toggleErr != nil {
				warning = "  启停失败: " + toggleErr.Error()
				continue
			}
			warning = "  " + message
			continue
		}

		if index, ok := findSkillCatalogEntryIndex(skills, choice); ok {
			if skills[index].Disabled {
				warning = "  该 skill 已停用，输入 x <编号> 可启用"
				continue
			}
			return &skills[index], nil
		}

		if usePopup {
			warning = "  无效的选择，请重新输入"
		} else {
			printChatSelectionWarning("无效的选择，请重新输入")
		}
	}
}

func promptSkillExecutionInput(session *ChatSession) (string, error) {
	for {
		printChatSelectionPrompt(skillExecutionPrompt)
		text, err := chatInteractiveReadPriorityLineWithPrompt(session, context.Background(), skillExecutionPrompt)
		fmt.Println()
		if err != nil {
			return "", err
		}
		value := strings.TrimSpace(normalizeQueuedInputLine(text))
		switch strings.ToLower(value) {
		case "q", "quit", "cancel", "exit":
			return "", nil
		}
		if value == "" {
			printChatSelectionWarning("prompt 不能为空，请重新输入")
			continue
		}
		return value, nil
	}
}

func filterSkillCatalogEntries(entries []aicliFunctionDescriptorReport, query string) []aicliFunctionDescriptorReport {
	if len(entries) == 0 {
		return nil
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return append([]aicliFunctionDescriptorReport(nil), entries...)
	}

	filtered := make([]aicliFunctionDescriptorReport, 0, len(entries))
	for _, item := range entries {
		if skillCatalogEntryMatchesQuery(item, query) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

// sessionSkillRoots 返回本次会话实际扫描的 skill 目录（加载时快照）。清单用它
// 回显来源层；热刷新会原地更新同一个 binding，所以启停后仍然是真的。
//
// 拿不到目录时这里不返回空切片，而是返回一条把挂载链各道闸真实取值摊开的诊断串。
// 返回空切片只会在清单里渲染成一句猜测性提示，把 enabled / 目录解析 /
// NoSkills / DisableTools 四种完全不同的故障压成同一个 "total=0"，逼人翻配置猜。
// 诊断串让这条命令本身就能指认是哪一道闸关着。
func sessionSkillRoots(session *ChatSession) []string {
	if session == nil {
		return []string{"<none: no session>"}
	}
	if session.SkillsBinding == nil {
		return []string{"<none: skills runtime not attached — " + sessionSkillLoadDiagnosis(session) + ">"}
	}
	roots := append([]string(nil), session.SkillsBinding.roots...)
	if len(roots) == 0 {
		return []string{"<none: binding attached but manager reported 0 skill dir — " + sessionSkillLoadDiagnosis(session) + ">"}
	}
	return roots
}

// sessionSkillLoadDiagnosis 输出 skills runtime 挂载链上每道闸的真实取值。
// 读取口径必须与 skills_integration.go 的加载路径一致（同一份 effective config、
// 同一个 resolveChatSkillDirs），否则诊断本身会说谎。
func sessionSkillLoadDiagnosis(session *ChatSession) string {
	if session == nil {
		return "no session"
	}
	cfg := effectiveChatSkillConfig(nil, session)
	present := cfg != nil && cfg.SkillsRuntime != nil
	enabled := present && cfg.SkillsRuntime.Enabled
	dirs := resolveChatSkillDirs(cfg, session, nil)
	base := fmt.Sprintf(
		"skills_runtime{present=%v enabled=%v} no_skills=%v disable_tools=%v binding=%v resolved_dirs=%d %v %s %s",
		present, enabled, session.NoSkills, session.DisableTools, session.SkillsBinding != nil, len(dirs), dirs,
		sharedManagerView(session), sessionCapabilitiesLoadView(session))
	if session.CapabilitiesInitError != "" {
		// 能力面初始化（discover/attach）失败会连带让 skills/runtime host/tools
		// 全缺失——此时 /skills 的各闸都是“绿”但 binding 还是 nil，最根本的原
		// 因在这里。
		base += fmt.Sprintf(" capabilities_init_error=%q", session.CapabilitiesInitError)
	}
	return base
}

// sharedManagerView 回显共享 bootstrap manager 的真实视角。私有 manager 路径按
// 需现建，共享路径的 manager 是启动时按 runtime config 造的，两者的目录来源
// 不同：加载失败时"配置解析出的目录"和"manager 实际持有的目录"可以完全对不上。
// 只看前者会把 manager 侧的问题误判成解析问题。
func sharedManagerView(session *ChatSession) string {
	if session == nil || session.LocalRuntimeHost == nil || session.LocalRuntimeHost.Bootstrap == nil {
		return "shared_manager{absent}"
	}
	manager := session.LocalRuntimeHost.Bootstrap
	managerDirs := manager.SkillDirs()
	return fmt.Sprintf("shared_manager{dirs=%d %v summaries=%d}",
		len(managerDirs), managerDirs, len(manager.Registry().ListSummaries()))
}

// chatSkillCatalogEntries 是 /skills 清单的唯一口径：TUI 清单、/skills --json、
// 全屏选择器与 Web API 全部经它取条目，避免“命令里看不到、页面上能看到”。
//
// 两步：
//  1. query 过滤（名称/描述/分类/标签/路径全文匹配）
//  2. 补上已停用行——停用后技能退出函数面，但必须仍在清单里可见（否则用户
//     看不到自己停用了什么，也就没有启停的入口）
//
// 这里**不**按 user-invocable 过滤。口径是“能调用就应该能显示”：实测
// user-invocable:false 只被当展示层过滤，执行链（/skill <name> →
// resolveSkillCallableReference → SendSkillTurn）从未查过 UserInvocable()，技能
// 照常注册进函数面——也就是说这类技能**一直可以 /skill 调用**。隐藏它只会造出
// “清单里没有、敲命令能跑”的不一致，而 Web API 侧从来没有这个过滤，命令侧才是
// 唯一的例外。若将来要真正禁止用户显式调用，正确落点是在执行链加门，而不是在
// 清单里藏条目；本函数会随之保持“显示了就能调”的不变式。
func chatSkillCatalogEntries(session *ChatSession, catalog *aicliFunctionCatalog, report *aicliFunctionCatalogReport, query string) []aicliFunctionDescriptorReport {
	if report == nil {
		return nil
	}
	skills := filterSkillCatalogEntries(report.Skills, query)
	return buildSkillPickerCatalogEntries(session, skills)
}

func findSkillCatalogEntryIndex(entries []aicliFunctionDescriptorReport, choice string) (int, bool) {
	choice = strings.TrimSpace(choice)
	if choice == "" || len(entries) == 0 {
		return -1, false
	}

	if num, err := strconv.Atoi(choice); err == nil {
		if num >= 1 && num <= len(entries) {
			return num - 1, true
		}
		return -1, false
	}

	normalized := strings.ToLower(choice)
	exactIndex := -1
	exactCount := 0
	prefixIndex := -1
	prefixCount := 0
	for i, item := range entries {
		tokens := skillCatalogEntrySelectionTokens(item)
		matchedExact := false
		matchedPrefix := false
		for _, token := range tokens {
			token = strings.TrimSpace(strings.ToLower(token))
			if token == "" {
				continue
			}
			if token == normalized {
				matchedExact = true
				break
			}
			if strings.HasPrefix(token, normalized) {
				matchedPrefix = true
			}
		}
		if matchedExact {
			exactIndex = i
			exactCount++
			continue
		}
		if matchedPrefix {
			prefixIndex = i
			prefixCount++
		}
	}
	if exactCount == 1 {
		return exactIndex, true
	}
	if exactCount > 1 {
		return -1, false
	}
	if prefixCount == 1 {
		return prefixIndex, true
	}
	return -1, false
}

func skillCatalogEntryMatchesQuery(item aicliFunctionDescriptorReport, query string) bool {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return true
	}
	return strings.Contains(skillCatalogEntrySearchText(item), query)
}

func skillCatalogEntrySearchText(item aicliFunctionDescriptorReport) string {
	desc := item.Descriptor
	parts := []string{
		skillCatalogEntryLabel(item),
		strings.TrimSpace(item.FunctionName),
	}
	if desc != nil {
		parts = append(parts, strings.TrimSpace(desc.Description), strings.TrimSpace(desc.Category))
		parts = append(parts, strings.Join(desc.Labels, " "), strings.Join(desc.Capabilities, " "))
		if desc.Source != nil {
			parts = append(parts, strings.TrimSpace(desc.Source.Path), strings.TrimSpace(desc.Source.Dir), strings.TrimSpace(desc.Source.Layer))
		}
		if desc.Metadata != nil {
			if value, _ := desc.Metadata["skill_name"].(string); value != "" {
				parts = append(parts, value)
			}
			if value, _ := desc.Metadata["skill_path"].(string); value != "" {
				parts = append(parts, value)
			}
			if value, _ := desc.Metadata["function_name"].(string); value != "" {
				parts = append(parts, value)
			}
		}
	}
	return strings.ToLower(strings.Join(parts, " "))
}

func skillCatalogEntrySelectionTokens(item aicliFunctionDescriptorReport) []string {
	tokens := []string{skillCatalogEntryLabel(item), strings.TrimSpace(item.FunctionName)}
	if desc := item.Descriptor; desc != nil {
		if desc.Metadata != nil {
			if value, _ := desc.Metadata["skill_name"].(string); value != "" {
				tokens = append(tokens, value)
			}
			if value, _ := desc.Metadata["skill_path"].(string); value != "" {
				tokens = append(tokens, value)
			}
			if value, _ := desc.Metadata["function_name"].(string); value != "" {
				tokens = append(tokens, value)
			}
		}
		if desc.Source != nil && desc.Source.Path != "" {
			tokens = append(tokens, desc.Source.Path)
		}
	}
	return tokens
}

func skillCatalogEntryLabel(item aicliFunctionDescriptorReport) string {
	if desc := item.Descriptor; desc != nil {
		if name := strings.TrimSpace(desc.Name); name != "" {
			return name
		}
	}
	if name := strings.TrimSpace(item.FunctionName); name != "" {
		return name
	}
	return "skill"
}

// skillCatalogEntryDescription / skillCatalogEntryMeta 把清单条目的「描述」与
// 「元信息」拆开。渲染时描述必须先被截断到一行内，而元信息（function=/
// category=/path=）是排障入口，既不能被描述挤掉，也不能被切在半截
// （"| function=skil" 这种残片比不显示更糟）。
func skillCatalogEntryDescription(item aicliFunctionDescriptorReport) string {
	if desc := item.Descriptor; desc != nil {
		return strings.TrimSpace(desc.Description)
	}
	return ""
}

// skillCatalogEntryMetaParts 按重要性返回元信息片段：function= 是调用入口，
// category= 次之，path= 最长也最次要。渲染时按此顺序递进装配，空间不够就
// 从尾部丢弃——而不是把任意一段切在半截。
func skillCatalogEntryMetaParts(item aicliFunctionDescriptorReport) []string {
	desc := item.Descriptor
	if desc == nil {
		if fn := strings.TrimSpace(item.FunctionName); fn != "" {
			return []string{"function=" + fn}
		}
		return nil
	}
	metaParts := make([]string, 0, 3)
	if fn, _ := desc.Metadata["function_name"].(string); strings.TrimSpace(fn) != "" {
		metaParts = append(metaParts, "function="+strings.TrimSpace(fn))
	} else if fn := strings.TrimSpace(item.FunctionName); fn != "" {
		metaParts = append(metaParts, "function="+fn)
	}
	if category := strings.TrimSpace(desc.Category); category != "" {
		metaParts = append(metaParts, "category="+category)
	}
	if path, _ := desc.Metadata["skill_path"].(string); strings.TrimSpace(path) != "" {
		metaParts = append(metaParts, "path="+strings.TrimSpace(path))
	}
	return metaParts
}

func skillCatalogEntryMeta(item aicliFunctionDescriptorReport) string {
	return strings.Join(skillCatalogEntryMetaParts(item), ", ")
}

func skillCatalogEntryDetail(item aicliFunctionDescriptorReport) string {
	description := skillCatalogEntryDescription(item)
	meta := skillCatalogEntryMeta(item)
	switch {
	case description != "" && meta != "":
		return description + " | " + meta
	case description != "":
		return description
	case meta != "":
		return meta
	default:
		return "skill"
	}
}

// skillCatalogLineReserve 是渲染通道在命令行结果外额外加的缩进/边距列数。
// 若按终端满宽截断，落屏后会多出这几列而折行——描述就"跑出"一行。这里预留
// 固定余量，保证每条恒为一行。
const skillCatalogLineReserve = 4

// skillCatalogMinDescription 是单行里无论如何都要留给描述的列数。元信息再
// 重要也不能把描述挤到这句以下——描述是清单的主体，元信息是附带排障线索。
const skillCatalogMinDescription = 24

func formatSkillCatalogItemLine(index int, item aicliFunctionDescriptorReport, labelWidth, width int) string {
	label := skillCatalogEntryLabel(item)
	// 停用行必须一眼可辨：它仍在清单里（可启停回来），但不能被当成可执行项。
	if item.Disabled {
		label = "已停用 " + label
	}
	budget := width - skillCatalogLineReserve
	if budget <= 3 {
		// 终端宽度不可信（未初始化/管道）时退回原有语义，避免把清单截成空行。
		budget = width
	}
	if budget <= 3 {
		budget = 80
	}

	prefix := fmt.Sprintf("  [%d] %-*s  ", index+1, labelWidth, label)
	description := skillCatalogEntryDescription(item)
	descBudget := budget - ui.DisplayWidth(prefix)
	if descBudget < skillCatalogMinDescription {
		descBudget = skillCatalogMinDescription
	}

	// 元信息按 function= → category= → path= 递进装配，保留「仍给描述留下
	// 最小列数」的最大前缀。这样要么完整保留、要么整段不显示，绝不会出现
	// "| function=skil" 这种被切断的残片。
	meta := ""
	if description != "" {
		acc := ""
		for _, part := range skillCatalogEntryMetaParts(item) {
			next := part
			if acc != "" {
				next = acc + ", " + part
			}
			if descBudget-ui.DisplayWidth(next)-3 < skillCatalogMinDescription {
				break
			}
			acc = next
		}
		meta = acc
	} else {
		// 没有描述时元信息就是主体，整段保留。
		meta = skillCatalogEntryMeta(item)
	}

	switch {
	case description != "" && meta != "":
		return prefix + truncateStatusValue(description, descBudget-ui.DisplayWidth(meta)-3) + " | " + meta
	case description != "":
		return prefix + truncateStatusValue(description, descBudget)
	case meta != "":
		return prefix + truncateStatusValue(meta, descBudget)
	default:
		return prefix + "skill"
	}
}
