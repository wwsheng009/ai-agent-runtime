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

	requestedModel := strings.TrimSpace(extractCommandArgument(command))
	if requestedModel == "" {
		if noInteractive {
			printRuntimeModelState(session)
			return false
		}

		selectedModel, err := promptRuntimeModelSelection(session)
		if err != nil {
			fmt.Printf("错误: %v\n", err)
			return false
		}
		requestedModel = selectedModel
	}

	if err := applyRuntimeModelSwitch(session, requestedModel, !noInteractive); err != nil {
		fmt.Printf("错误: %v\n", err)
		return false
	}

	printRuntimeModelState(session)
	return false
}

func printRuntimeModelState(session *ChatSession) {
	if session == nil {
		return
	}
	model := effectiveRuntimeModel(session)
	if model == "" {
		model = "(无)"
	}
	reasoning := runtimetypes.NormalizeReasoningEffort(session.ReasoningEffort)
	if reasoning == "" {
		reasoning = "(无)"
	}
	fmt.Printf("当前模型: %s\n", model)
	fmt.Printf("当前 reasoning_effort: %s\n", reasoning)
}

func applyRuntimeModelSwitch(session *ChatSession, requestedModel string, interactive bool) error {
	if session == nil {
		return fmt.Errorf("当前没有活动会话")
	}

	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" {
		requestedModel = effectiveRuntimeModel(session)
	}
	if requestedModel == "" {
		return fmt.Errorf("未指定可切换的模型")
	}

	resolvedModel := strings.TrimSpace(config.ApplyModelMapping(&session.Provider, requestedModel))
	if resolvedModel == "" {
		return fmt.Errorf("未指定可切换的模型")
	}

	if !strings.EqualFold(requestedModel, resolvedModel) {
		fmt.Printf("提示: 模型已映射 %s -> %s\n", requestedModel, resolvedModel)
	}

	reasoningEffort := runtimetypes.NormalizeReasoningEffort(session.ReasoningEffort)
	catalog := reasoningEffortCatalogForModel(session.Provider, resolvedModel)
	if catalog.supported {
		if interactive {
			selectedReasoning, err := selectRuntimeReasoningEffort(session, reasoningEffort, catalog.options)
			if err != nil {
				return err
			}
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
	syncChatLoggerModelState(session)
	warnIfChatSessionSyncFails(session, "toggle model", syncRuntimeSessionFromChat(session))
	return nil
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

func promptRuntimeModelSelection(session *ChatSession) (string, error) {
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

	ui.PrintEmptyLine()
	for {
		fmt.Print("请输入选项 (回车保持当前): ")
		text, err := chatInteractiveReadPriorityLine(session, context.Background())
		if err != nil {
			return "", err
		}
		text = strings.TrimSpace(normalizeQueuedInputLine(text))
		if text == "" {
			return currentModel, nil
		}

		if num, err := strconv.Atoi(text); err == nil {
			if num >= 1 && num <= len(options) {
				return options[num-1], nil
			}
			ui.PrintWarning("无效的选择，请重新输入")
			continue
		}

		if matched, ok := matchCaseInsensitive(options, text); ok {
			return matched, nil
		}

		return text, nil
	}
}

func selectRuntimeReasoningEffort(session *ChatSession, current string, options []string) (string, error) {
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
	ui.PrintEmptyLine()

	for {
		if currentValid {
			fmt.Print("请输入选项 (回车保留当前 / 输入 0 清空): ")
		} else if defaultOption != "" {
			fmt.Printf("请输入选项 (回车默认: %s / 输入 0 清空): ", defaultOption)
		} else {
			fmt.Print("请输入选项 (回车清空当前无效值 / 输入 0 清空): ")
		}
		text, err := chatInteractiveReadPriorityLine(session, context.Background())
		if err != nil {
			return "", err
		}
		text = strings.TrimSpace(normalizeQueuedInputLine(text))
		normalized := runtimetypes.NormalizeReasoningEffort(text)
		if normalized == "" {
			if currentValid {
				return currentMatch, nil
			}
			if defaultOption != "" {
				return defaultOption, nil
			}
			return "", nil
		}

		switch strings.ToLower(normalized) {
		case "0", "clear", "none", "off", "清空", "无":
			return "", nil
		}

		if num, err := strconv.Atoi(normalized); err == nil {
			if num >= 1 && num <= len(normalizedOptions) {
				return normalizedOptions[num-1], nil
			}
			ui.PrintWarning("无效的选择，请重新输入")
			continue
		}

		if matched, ok := reasoningEffortOptionMatch(normalized, normalizedOptions); ok {
			return matched, nil
		}

		ui.PrintWarning("无效的选择，请重新输入")
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
