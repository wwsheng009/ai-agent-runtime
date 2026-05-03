package commands

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func handleModelCommand(session *ChatSession, command string, noInteractive bool) bool {
	if session == nil {
		fmt.Println("错误: 当前没有活动会话")
		return false
	}

	request, err := parseModelCommandRequest(command)
	if err != nil {
		fmt.Printf("错误: %v\n", err)
		return false
	}

	if request.ShowStatus && !request.HasMutation() {
		printRuntimeModelState(session)
		return false
	}

	if err := executeModelCommand(session, request, !noInteractive); err != nil {
		fmt.Printf("错误: %v\n", err)
		return false
	}

	if !request.ShowStatus {
		printRuntimeModelState(session)
	}
	return false
}

func printRuntimeModelState(session *ChatSession) {
	if session == nil {
		return
	}
	beginDirectInteractiveOutput(session)
	providerName := strings.TrimSpace(session.ProviderName)
	if providerName == "" {
		providerName = "(无)"
	}
	protocol := strings.TrimSpace(session.Provider.GetProtocol())
	if protocol == "" {
		protocol = "(无)"
	}
	model := strings.TrimSpace(session.Model)
	if model == "" {
		model = "(无)"
	}
	reasoning := runtimetypes.NormalizeReasoningEffort(session.ReasoningEffort)
	if reasoning == "" {
		reasoning = "(无)"
	}
	baseURL := strings.TrimSpace(session.BaseURL)
	if baseURL == "" {
		baseURL = "(无)"
	}
	fmt.Printf("当前 provider: %s\n", providerName)
	fmt.Printf("当前 protocol: %s\n", protocol)
	fmt.Printf("当前模型: %s\n", model)
	fmt.Printf("当前 reasoning_effort: %s\n", reasoning)
	fmt.Printf("当前 baseURL: %s\n", baseURL)
}

func applyRuntimeModelSwitch(session *ChatSession, requestedModel string, interactive bool) (bool, error) {
	if session == nil {
		return false, fmt.Errorf("当前没有活动会话")
	}

	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" {
		requestedModel = effectiveRuntimeModel(session)
	}
	if requestedModel == "" {
		return false, fmt.Errorf("未指定可切换的模型")
	}

	resolvedModel := strings.TrimSpace(config.ApplyModelMapping(&session.Provider, requestedModel))
	if resolvedModel == "" {
		return false, fmt.Errorf("未指定可切换的模型")
	}

	if !strings.EqualFold(requestedModel, resolvedModel) {
		beginDirectInteractiveOutput(session)
		fmt.Printf("提示: 模型已映射 %s -> %s\n", requestedModel, resolvedModel)
	}

	reasoningEffort := runtimetypes.NormalizeReasoningEffort(session.ReasoningEffort)
	catalog := reasoningEffortCatalogForModel(session.Provider, resolvedModel)
	popupUsed := false
	if catalog.supported {
		if interactive {
			selectedReasoning, usedPopup, err := selectRuntimeReasoningEffort(session, reasoningEffort, catalog.options)
			if err != nil {
				return usedPopup, err
			}
			popupUsed = popupUsed || usedPopup
			reasoningEffort = selectedReasoning
		} else if reasoningEffort != "" && !reasoningEffortAllowed(reasoningEffort, catalog.options) {
			fmt.Fprintf(os.Stderr, "Warning: reasoning_effort %q 不被模型 %s 支持，已清空\n", reasoningEffort, resolvedModel)
			reasoningEffort = ""
		}
	}

	apiPath := ""
	if session.Adapter != nil {
		apiPath = session.Adapter.GetAPIPath()
	}

	session.Model = resolvedModel
	session.ReasoningEffort = reasoningEffort
	session.BaseURL = buildProviderURL(session.Provider, apiPath, resolvedModel)
	session.ContextWindowTokenCount = 0
	syncChatLoggerModelState(session)
	warnIfChatSessionSyncFails(session, "toggle model", syncRuntimeSessionFromChat(session))
	if session.Interaction != nil {
		session.Interaction.RefreshStatus("")
	}
	return popupUsed, nil
}

func syncChatLoggerModelState(session *ChatSession) {
	if session == nil || session.Logger == nil || session.Logger.sessionLog == nil {
		return
	}
	session.Logger.sessionLog.Provider = strings.TrimSpace(session.ProviderName)
	session.Logger.sessionLog.Protocol = session.Provider.GetProtocol()
	session.Logger.sessionLog.Model = strings.TrimSpace(session.Model)
	session.Logger.sessionLog.BaseURL = strings.TrimSpace(session.BaseURL)
}

func promptRuntimeModelSelection(session *ChatSession) (string, bool, error) {
	if useRuntimeSelectionPopup(session) {
		return promptRuntimeModelSelectionPopup(session)
	}
	return promptRuntimeModelSelectionLegacy(session)
}

func promptRuntimeModelSelectionPopup(session *ChatSession) (string, bool, error) {
	if session == nil {
		return "", false, fmt.Errorf("当前没有活动会话")
	}

	currentModel := effectiveRuntimeModel(session)
	options := runtimeModelSelectionOptions(session)
	currentMatch, _ := matchCaseInsensitive(options, currentModel)
	notice := discardPendingInteractiveInputForPriorityPrompt(session, "模型选择")
	hint := "  提示: 输入编号、模型名，回车保持当前"
	popupLines := renderSelectionPopupLines("选择模型", "模型", currentModel, options, currentMatch, "", hint, notice, "")
	prompt := "请输入选项 (回车保持当前): "
	showRuntimeSelectionPopup(session, popupLines, prompt)
	defer clearRuntimeSelectionPopup(session)

	for {
		text, err := chatInteractiveReadPriorityLineWithPrompt(session, context.Background(), prompt)
		if err != nil {
			return "", true, err
		}
		text = strings.TrimSpace(normalizeQueuedInputLine(text))
		selected, ok := resolveRuntimeSelectionInput(text, currentModel, "", options, true, false)
		if ok {
			return selected, true, nil
		}
		popupLines = renderSelectionPopupLines("选择模型", "模型", currentModel, options, currentMatch, "", hint, notice, "  无效的选择，请重新输入")
		showRuntimeSelectionPopup(session, popupLines, prompt)
	}
}

func promptRuntimeModelSelectionLegacy(session *ChatSession) (string, bool, error) {
	beginDirectInteractiveOutput(session)
	currentModel := effectiveRuntimeModel(session)
	options := runtimeModelSelectionOptions(session)

	if notice := discardPendingInteractiveInputForPriorityPrompt(session, "模型选择"); notice != "" {
		fmt.Printf("\n%s\n", formatInteractiveSupplementPromptLine(notice))
	}

	ui.PrintSection("选择模型")
	theme := ui.GetTheme(ui.ThemeAuto)
	if currentModel != "" {
		fmt.Printf("  当前模型: %s\n", theme.Dimmed(currentModel))
	} else {
		fmt.Println("  当前模型: (无)")
	}

	if len(options) > 0 {
		maxLen := 0
		for _, option := range options {
			if len(option) > maxLen {
				maxLen = len(option)
			}
		}
		for i, option := range options {
			label := option
			if strings.EqualFold(option, currentModel) {
				fmt.Printf("  [%d] %-*s  %s\n", i+1, maxLen, label, theme.Dimmed("(当前)"))
				continue
			}
			fmt.Printf("  [%d] %-*s\n", i+1, maxLen, label)
		}
		fmt.Println("  提示: 也可以直接输入自定义模型名")
	}

	prompt := "请输入选项 (回车保持当前): "
	ui.PrintEmptyLine()
	for {
		fmt.Print(prompt)
		text, err := chatInteractiveReadPriorityLineWithPrompt(session, context.Background(), prompt)
		if err != nil {
			return "", false, err
		}
		text = strings.TrimSpace(normalizeQueuedInputLine(text))
		if text == "" {
			return currentModel, false, nil
		}

		if num, err := strconv.Atoi(text); err == nil {
			if num >= 1 && num <= len(options) {
				return options[num-1], false, nil
			}
			ui.PrintWarning("无效的选择，请重新输入")
			continue
		}

		if matched, ok := matchCaseInsensitive(options, text); ok {
			return matched, false, nil
		}

		return text, false, nil
	}
}

func selectRuntimeReasoningEffort(session *ChatSession, current string, options []string) (string, bool, error) {
	if useRuntimeSelectionPopup(session) {
		return selectRuntimeReasoningEffortPopup(session, current, options)
	}
	return selectRuntimeReasoningEffortLegacy(session, current, options)
}

func selectRuntimeReasoningEffortPopup(session *ChatSession, current string, options []string) (string, bool, error) {
	if session == nil {
		return "", false, fmt.Errorf("当前没有活动会话")
	}

	normalizedOptions := normalizeReasoningEffortOptions(options)
	currentEffort := runtimetypes.NormalizeReasoningEffort(current)
	currentMatch, currentValid := reasoningEffortOptionMatch(currentEffort, normalizedOptions)
	defaultOption := ""
	if !currentValid && len(normalizedOptions) > 0 {
		defaultOption = normalizedOptions[0]
	}

	notice := discardPendingInteractiveInputForPriorityPrompt(session, "reasoning_effort 选择")
	hint := "  提示: 输入编号或名称，回车保留当前，输入 0 清空"
	popupLines := renderSelectionPopupLines("选择 reasoning_effort 值", "reasoning_effort", currentEffort, normalizedOptions, currentMatch, defaultOption, hint, notice, "")
	prompt := reasoningEffortSelectionPrompt(currentValid, defaultOption)
	showRuntimeSelectionPopup(session, popupLines, prompt)
	defer clearRuntimeSelectionPopup(session)

	for {
		text, err := chatInteractiveReadPriorityLineWithPrompt(session, context.Background(), prompt)
		if err != nil {
			return "", true, err
		}
		text = strings.TrimSpace(normalizeQueuedInputLine(text))
		normalized := runtimetypes.NormalizeReasoningEffort(text)
		selected, ok := resolveRuntimeReasoningEffortInput(normalized, currentMatch, currentValid, defaultOption, normalizedOptions)
		if ok {
			return selected, true, nil
		}
		popupLines = renderSelectionPopupLines("选择 reasoning_effort 值", "reasoning_effort", currentEffort, normalizedOptions, currentMatch, defaultOption, hint, notice, "  无效的选择，请重新输入")
		showRuntimeSelectionPopup(session, popupLines, prompt)
	}
}

func selectRuntimeReasoningEffortLegacy(session *ChatSession, current string, options []string) (string, bool, error) {
	beginDirectInteractiveOutput(session)
	normalizedOptions := normalizeReasoningEffortOptions(options)
	currentEffort := runtimetypes.NormalizeReasoningEffort(current)
	currentMatch, currentValid := reasoningEffortOptionMatch(currentEffort, normalizedOptions)
	defaultOption := ""
	if !currentValid && len(normalizedOptions) > 0 {
		defaultOption = normalizedOptions[0]
	}

	if notice := discardPendingInteractiveInputForPriorityPrompt(session, "reasoning_effort 选择"); notice != "" {
		fmt.Printf("\n%s\n", formatInteractiveSupplementPromptLine(notice))
	}

	ui.PrintSection("选择 reasoning_effort 值")
	theme := ui.GetTheme(ui.ThemeAuto)
	switch {
	case currentEffort == "":
		fmt.Println("  当前 reasoning_effort: (无)")
	case currentValid:
		fmt.Printf("  当前 reasoning_effort: %s %s\n", theme.Dimmed(currentMatch), theme.Dimmed("(当前)"))
	default:
		fmt.Printf("  当前 reasoning_effort: %s %s\n", theme.Dimmed(currentEffort), theme.Dimmed("(当前模型不支持)"))
	}

	maxLen := 0
	for _, option := range normalizedOptions {
		if len(option) > maxLen {
			maxLen = len(option)
		}
	}
	for i, option := range normalizedOptions {
		switch {
		case option == currentMatch:
			fmt.Printf("  [%d] %-*s  %s\n", i+1, maxLen, option, theme.Dimmed("(当前)"))
		case defaultOption != "" && option == defaultOption:
			fmt.Printf("  [%d] %-*s  %s\n", i+1, maxLen, option, theme.Dimmed("(默认)"))
		default:
			fmt.Printf("  [%d] %-*s\n", i+1, maxLen, option)
		}
	}
	fmt.Println("  [0] 清空 reasoning_effort")
	prompt := reasoningEffortSelectionPrompt(currentValid, defaultOption)
	ui.PrintEmptyLine()

	for {
		fmt.Print(prompt)
		text, err := chatInteractiveReadPriorityLineWithPrompt(session, context.Background(), prompt)
		if err != nil {
			return "", false, err
		}
		text = strings.TrimSpace(normalizeQueuedInputLine(text))
		normalized := runtimetypes.NormalizeReasoningEffort(text)
		if normalized == "" {
			if currentValid {
				return currentMatch, false, nil
			}
			if defaultOption != "" {
				return defaultOption, false, nil
			}
			return "", false, nil
		}

		switch strings.ToLower(normalized) {
		case "0", "clear", "none", "off", "清空", "无":
			return "", false, nil
		}

		if num, err := strconv.Atoi(normalized); err == nil {
			if num >= 1 && num <= len(normalizedOptions) {
				return normalizedOptions[num-1], false, nil
			}
			ui.PrintWarning("无效的选择，请重新输入")
			continue
		}

		if matched, ok := reasoningEffortOptionMatch(normalized, normalizedOptions); ok {
			return matched, false, nil
		}

		ui.PrintWarning("无效的选择，请重新输入")
	}
}

func useRuntimeSelectionPopup(session *ChatSession) bool {
	return session != nil && session.Surface != nil && session.Surface.Enabled()
}

func showRuntimeSelectionPopup(session *ChatSession, lines []string, prompt string) {
	if !useRuntimeSelectionPopup(session) {
		return
	}
	beginDirectInteractiveOutput(session)
	session.Surface.ShowPopupInput(lines, prompt)
}

func clearRuntimeSelectionPopup(session *ChatSession) {
	if session == nil || session.Surface == nil {
		if session != nil && session.Interaction != nil {
			session.Interaction.ResetPromptState()
		}
		return
	}
	session.Surface.ClearPopup()
	if session.Interaction != nil {
		session.Interaction.ResetPromptState()
	}
}

func renderSelectionPopupLines(title, currentLabel, currentValue string, options []string, currentMatch, defaultOption, hint, notice, warning string) []string {
	lines := make([]string, 0, 3+len(options))
	if title = strings.TrimSpace(title); title != "" {
		lines = append(lines, title)
	}
	if notice = strings.TrimSpace(notice); notice != "" {
		lines = append(lines, notice)
	}
	currentLabel = strings.TrimSpace(currentLabel)
	if currentLabel != "" {
		currentValue = strings.TrimSpace(currentValue)
		if currentValue == "" {
			currentValue = "(无)"
		}
		lines = append(lines, fmt.Sprintf("当前%s: %s", currentLabel, currentValue))
	}
	if warning = strings.TrimSpace(warning); warning != "" {
		lines = append(lines, warning)
	}
	if len(options) > 0 {
		maxLen := 0
		for _, option := range options {
			if len(option) > maxLen {
				maxLen = len(option)
			}
		}
		for i, option := range options {
			switch {
			case strings.EqualFold(option, currentMatch):
				lines = append(lines, fmt.Sprintf("  [%d] %-*s  (当前)", i+1, maxLen, option))
			case defaultOption != "" && strings.EqualFold(option, defaultOption):
				lines = append(lines, fmt.Sprintf("  [%d] %-*s  (默认)", i+1, maxLen, option))
			default:
				lines = append(lines, fmt.Sprintf("  [%d] %-*s", i+1, maxLen, option))
			}
		}
	}
	if hint = strings.TrimSpace(hint); hint != "" {
		lines = append(lines, hint)
	}
	return lines
}

func resolveRuntimeSelectionInput(input, current, defaultOption string, options []string, allowCustom, allowClear bool) (string, bool) {
	input = strings.TrimSpace(input)
	if input == "" {
		if current != "" {
			return current, true
		}
		if defaultOption != "" {
			return defaultOption, true
		}
		return "", true
	}

	normalized := strings.ToLower(input)
	if allowClear {
		switch normalized {
		case "0", "clear", "none", "off", "清空", "无":
			return "", true
		}
	}

	if num, err := strconv.Atoi(input); err == nil {
		if num >= 1 && num <= len(options) {
			return options[num-1], true
		}
		return "", false
	}

	if matched, ok := matchCaseInsensitive(options, input); ok {
		return matched, true
	}

	if allowCustom {
		return input, true
	}
	return "", false
}

func resolveRuntimeReasoningEffortInput(input, currentMatch string, currentValid bool, defaultOption string, options []string) (string, bool) {
	input = strings.TrimSpace(input)
	if input == "" {
		if currentValid {
			return currentMatch, true
		}
		if defaultOption != "" {
			return defaultOption, true
		}
		return "", true
	}

	switch strings.ToLower(input) {
	case "0", "clear", "none", "off", "清空", "无":
		return "", true
	}

	if num, err := strconv.Atoi(input); err == nil {
		if num >= 1 && num <= len(options) {
			return options[num-1], true
		}
		return "", false
	}

	if matched, ok := reasoningEffortOptionMatch(input, options); ok {
		return matched, true
	}
	return "", false
}

func reasoningEffortSelectionPrompt(currentValid bool, defaultOption string) string {
	switch {
	case currentValid:
		return "请输入选项 (回车保留当前 / 输入 0 清空): "
	case defaultOption != "":
		return fmt.Sprintf("请输入选项 (回车默认: %s / 输入 0 清空): ", defaultOption)
	default:
		return "请输入选项 (回车清空当前无效值 / 输入 0 清空): "
	}
}

func runtimeModelSelectionOptions(session *ChatSession) []string {
	if session == nil {
		return nil
	}

	seen := make(map[string]struct{})
	options := make([]string, 0, 1+len(session.Provider.SupportedModels))
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		key := strings.ToLower(value)
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		options = append(options, value)
	}

	add(effectiveRuntimeModel(session))
	add(session.Provider.DefaultModel)

	supported := append([]string(nil), session.Provider.SupportedModels...)
	for _, candidate := range supported {
		add(candidate)
	}

	sort.SliceStable(options, func(i, j int) bool {
		left := strings.ToLower(strings.TrimSpace(options[i]))
		right := strings.ToLower(strings.TrimSpace(options[j]))
		if left == right {
			return strings.TrimSpace(options[i]) < strings.TrimSpace(options[j])
		}
		return left < right
	})

	return options
}

func effectiveRuntimeModel(session *ChatSession) string {
	if session == nil {
		return ""
	}
	if model := strings.TrimSpace(session.Model); model != "" {
		return model
	}
	return strings.TrimSpace(session.Provider.DefaultModel)
}

func matchCaseInsensitive(options []string, input string) (string, bool) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", false
	}
	for _, option := range options {
		if strings.EqualFold(option, input) {
			return option, true
		}
	}
	return "", false
}
