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

const runtimeModelSelectionPageSize = 10

type runtimeModelPickerState struct {
	Options   []string
	Preferred string
	Filter    string
	Page      int
	PageSize  int
}

type runtimeModelPickerResult struct {
	Selected string
	Done     bool
	Message  string
	Redraw   bool
}

func handleModelCommand(session *ChatSession, command string, noInteractive bool) bool {
	if unifiedDirectInteractiveOutput(session) {
		if result, handled := executeStructuredModelCommand(session, command); handled {
			renderErr := renderChatCommandResult(session, result, false)
			if renderErr == nil && result.OpenModelPicker != nil {
				openChatModelPicker(session, *result.OpenModelPicker)
			}
			return false
		}
		// executeStructuredModelCommand handles every /model variant today, so
		// this branch is defensive. The deny-list fence no longer contains
		// /model (it is fully migrated), so rejectUnifiedInteractiveLegacyCommand
		// would fail open into the legacy stdout handler; fail closed instead.
		_ = renderChatCommandResult(session, commandTextResult("错误: /model 变体无法通过统一渲染命令通道处理"), false)
		return false
	}
	if rejectUnifiedInteractiveLegacyCommand(session, "/model") {
		return false
	}
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
		if isChatInteractivePromptCancelError(err) {
			return false
		}
		fmt.Printf("错误: %v\n", err)
		return false
	}

	if !request.ShowStatus {
		printRuntimeModelState(session)
	}
	return false
}

func printRuntimeModelState(session *ChatSession) {
	if unifiedDirectInteractiveOutput(session) {
		_ = renderChatCommandResult(session, commandTextResult(runtimeModelStateText(session)), false)
		return
	}
	printDirectInteractiveOutput(session, runtimeModelStateText(session)+"\n")
}

// runtimeModelStateText is shared by the plain presenter and the structured
// /model status command. Keeping this pure projection separate prevents a
// status query from re-entering the model picker or terminal writer.
func runtimeModelStateText(session *ChatSession) string {
	if session == nil {
		return "错误: 当前没有活动会话"
	}
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
	return fmt.Sprintf(
		"当前 provider: %s\n当前 protocol: %s\n当前模型: %s\n当前 reasoning_effort: %s\n当前 baseURL: %s",
		providerName, protocol, model, reasoning, baseURL,
	)
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
		printfDirectInteractiveOutput(session, "提示: 模型已映射 %s -> %s\n", requestedModel, resolvedModel)
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
	resetStableSharedToolSurface(session)
	syncChatLoggerModelState(session)
	refreshChatTitleMetadata(session)
	warnIfChatSessionSyncFails(session, "toggle model", syncRuntimeSessionFromChat(session))
	if err := refreshLocalRuntimeAfterModelSelection(session); err != nil {
		warnIfChatSessionSyncFails(session, "refresh local runtime after model switch", err)
	}
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
	session.Logger.sessionLog.Stream = session.Stream
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
	state := newRuntimeModelPickerState(options, currentMatch, runtimeModelSelectionPageSize)
	notice, restoreInput := prepareRuntimeSelectionInput(session, "模型选择")
	defer restoreInput()
	prompt := runtimeModelPickerPopupPrompt()
	pageOptions, _, _, _ := state.pageWindow()
	selectedIndex := initialRuntimeSelectionIndex(pageOptions, currentMatch, "")
	render := func(selected int, warning string) []string {
		return renderRuntimeModelPickerPopupLines(state, currentModel, currentMatch, notice, warning, selected)
	}
	handle := beginRuntimeSelectionPopup(session, render(selectedIndex, ""), prompt)
	defer clearRuntimeSelectionPopupHandle(session, handle)
	controller := newRuntimeSelectionController(session, handle, prompt, pageOptions, selectedIndex, render)

	for {
		text, err := chatInteractiveReadSelectionLine(session, prompt, controller)
		if err != nil {
			return "", true, err
		}
		text = strings.TrimSpace(normalizeQueuedInputLine(text))
		blankSelection, _ := controller.SelectedOption()
		nextState, result := applyRuntimeModelPickerInput(state, text, blankSelection)
		state = nextState
		if result.Done {
			return result.Selected, true, nil
		}
		if result.Redraw {
			pageOptions, _, _, _ = state.pageWindow()
			selectedIndex = initialRuntimeSelectionIndex(pageOptions, currentMatch, "")
			render = func(selected int, warning string) []string {
				return renderRuntimeModelPickerPopupLines(state, currentModel, currentMatch, notice, warning, selected)
			}
			controller = newRuntimeSelectionController(session, handle, prompt, pageOptions, selectedIndex, render)
		}
		controller.SetWarning(result.Message)
	}
}

func promptRuntimeModelSelectionLegacy(session *ChatSession) (string, bool, error) {
	beginDirectInteractiveOutput(session)
	currentModel := effectiveRuntimeModel(session)
	options := runtimeModelSelectionOptions(session)
	currentMatch, _ := matchCaseInsensitive(options, currentModel)
	state := newRuntimeModelPickerState(options, currentMatch, runtimeModelSelectionPageSize)

	notice, restoreInput := prepareRuntimeSelectionInput(session, "模型选择")
	defer restoreInput()
	if notice != "" {
		fmt.Printf("\n%s\n", formatInteractiveSupplementPromptLine(notice))
	}

	ui.PrintSection("选择模型")
	if currentModel != "" {
		writeChatMutedSuffix(os.Stdout, "  当前模型: ", currentModel)
	} else {
		fmt.Println("  当前模型: (无)")
	}
	printRuntimeModelPickerLegacyPage(state, currentMatch)

	prompt := runtimeModelPickerLegacyPrompt()
	ui.PrintEmptyLine()
	for {
		text, err := chatInteractiveReadPriorityLineWithPrompt(session, context.Background(), prompt)
		if err != nil {
			return "", false, err
		}
		text = strings.TrimSpace(normalizeQueuedInputLine(text))
		blankSelection := ""
		if state.Filter == "" {
			blankSelection = currentMatch
		} else if matched, ok := matchCaseInsensitive(state.filteredOptions(), currentMatch); ok {
			blankSelection = matched
		}
		if blankSelection == "" {
			pageOptions, _, _, _ := state.pageWindow()
			if len(pageOptions) > 0 {
				blankSelection = pageOptions[0]
			}
		}
		nextState, result := applyRuntimeModelPickerInput(state, text, blankSelection)
		state = nextState
		if result.Done {
			return result.Selected, false, nil
		}
		ui.PrintWarning("%s", result.Message)
		if result.Redraw {
			printRuntimeModelPickerLegacyPage(state, currentMatch)
		}
	}
}

func newRuntimeModelPickerState(options []string, preferred string, pageSize int) runtimeModelPickerState {
	state := runtimeModelPickerState{
		Options:   append([]string(nil), options...),
		Preferred: strings.TrimSpace(preferred),
		PageSize:  pageSize,
	}
	state.Page = state.pageForOption(state.Preferred)
	return state
}

func (s runtimeModelPickerState) normalizedPageSize() int {
	if s.PageSize <= 0 {
		return runtimeModelSelectionPageSize
	}
	return s.PageSize
}

func (s runtimeModelPickerState) filteredOptions() []string {
	return filterLoginProviders(s.Options, s.Filter)
}

func (s runtimeModelPickerState) pageWindow() (items []string, page, pageCount, total int) {
	filtered := s.filteredOptions()
	total = len(filtered)
	pageSize := s.normalizedPageSize()
	if total == 0 {
		return nil, 0, 1, 0
	}
	pageCount = (total + pageSize - 1) / pageSize
	page = s.Page
	if page < 0 {
		page = 0
	}
	if page >= pageCount {
		page = pageCount - 1
	}
	start := page * pageSize
	end := min(start+pageSize, total)
	return filtered[start:end], page, pageCount, total
}

func (s runtimeModelPickerState) pageForOption(option string) int {
	index := indexOfCaseInsensitive(s.filteredOptions(), option)
	if index < 0 {
		return 0
	}
	return index / s.normalizedPageSize()
}

func applyRuntimeModelPickerInput(state runtimeModelPickerState, input, blankSelection string) (runtimeModelPickerState, runtimeModelPickerResult) {
	input = strings.TrimSpace(input)
	if input == "" {
		if selected, ok := matchCaseInsensitive(state.Options, blankSelection); ok {
			return state, runtimeModelPickerResult{Selected: selected, Done: true}
		}
		return state, runtimeModelPickerResult{Message: "当前没有可确认的模型，请输入模型名或搜索关键词"}
	}

	// 精确模型名优先于 n/p/c 等控制命令，避免同名模型无法选择。
	if selected, ok := matchCaseInsensitive(state.Options, input); ok {
		return state, runtimeModelPickerResult{Selected: selected, Done: true}
	}

	// 使用 +模型名 可强制按自定义模型处理；单独的 + 仍表示下一页。
	if strings.HasPrefix(input, "+") && input != "+" {
		modelName := strings.TrimSpace(strings.TrimPrefix(input, "+"))
		if modelName == "" {
			return state, runtimeModelPickerResult{Message: "请在 + 后输入自定义模型名"}
		}
		if selected, ok := matchCaseInsensitive(state.Options, modelName); ok {
			return state, runtimeModelPickerResult{Selected: selected, Done: true}
		}
		return state, runtimeModelPickerResult{Selected: modelName, Done: true}
	}

	switch strings.ToLower(input) {
	case "n", "next", ">", "+":
		return moveRuntimeModelPickerPage(state, 1)
	case "p", "prev", "previous", "<", "-":
		return moveRuntimeModelPickerPage(state, -1)
	case "c", "clear":
		if state.Filter == "" {
			return state, runtimeModelPickerResult{Message: "当前没有搜索条件"}
		}
		state.Filter = ""
		state.Page = state.pageForOption(state.Preferred)
		return state, runtimeModelPickerResult{Message: "已清除模型搜索", Redraw: true}
	case "h", "help", "?":
		return state, runtimeModelPickerResult{Message: "用法: 当前页编号/完整名称选择；关键词或 /关键词搜索；n/p 翻页；c 清除搜索；+模型名选择自定义模型；回车确认高亮项"}
	}

	if strings.HasPrefix(input, "/") {
		return applyRuntimeModelPickerFilter(state, strings.TrimSpace(strings.TrimPrefix(input, "/")))
	}

	if number, err := strconv.Atoi(input); err == nil {
		pageOptions, _, _, total := state.pageWindow()
		if total == 0 {
			return state, runtimeModelPickerResult{Message: "当前没有可选模型编号；请继续搜索、输入 c 清除，或用 +模型名选择自定义模型"}
		}
		if number >= 1 && number <= len(pageOptions) {
			return state, runtimeModelPickerResult{Selected: pageOptions[number-1], Done: true}
		}
		return state, runtimeModelPickerResult{Message: fmt.Sprintf("无效编号 %d；请输入当前页 1-%d，或输入关键词搜索", number, len(pageOptions))}
	}

	matched := filterLoginProviders(state.Options, input)
	switch len(matched) {
	case 0:
		return state, runtimeModelPickerResult{Selected: input, Done: true}
	case 1:
		return state, runtimeModelPickerResult{Selected: matched[0], Done: true}
	default:
		return applyRuntimeModelPickerFilter(state, input)
	}
}

func applyRuntimeModelPickerFilter(state runtimeModelPickerState, query string) (runtimeModelPickerState, runtimeModelPickerResult) {
	state.Filter = strings.TrimSpace(query)
	state.Page = 0
	matched := state.filteredOptions()
	if state.Filter == "" {
		state.Page = state.pageForOption(state.Preferred)
		return state, runtimeModelPickerResult{Message: "已清除模型搜索", Redraw: true}
	}
	if len(matched) == 0 {
		return state, runtimeModelPickerResult{
			Message: fmt.Sprintf("没有匹配 %q 的模型；可继续搜索、输入 c 清除，或用 +模型名选择自定义模型", state.Filter),
			Redraw:  true,
		}
	}
	return state, runtimeModelPickerResult{
		Message: fmt.Sprintf("已按 %q 搜索，匹配 %d 个模型", state.Filter, len(matched)),
		Redraw:  true,
	}
}

func moveRuntimeModelPickerPage(state runtimeModelPickerState, delta int) (runtimeModelPickerState, runtimeModelPickerResult) {
	_, page, pageCount, total := state.pageWindow()
	if total == 0 || pageCount <= 1 {
		return state, runtimeModelPickerResult{Message: "当前只有一页，无需翻页"}
	}
	next := page + delta
	if next < 0 {
		return state, runtimeModelPickerResult{Message: "已经是第一页"}
	}
	if next >= pageCount {
		return state, runtimeModelPickerResult{Message: "已经是最后一页"}
	}
	state.Page = next
	return state, runtimeModelPickerResult{
		Message: fmt.Sprintf("第 %d/%d 页", next+1, pageCount),
		Redraw:  true,
	}
}

func runtimeModelPickerTitle(state runtimeModelPickerState) string {
	_, page, pageCount, filteredTotal := state.pageWindow()
	title := fmt.Sprintf("选择模型（共 %d", len(state.Options))
	if strings.TrimSpace(state.Filter) != "" {
		title += fmt.Sprintf("，搜索 %q 匹配 %d", state.Filter, filteredTotal)
	}
	return title + fmt.Sprintf("，第 %d/%d 页）", page+1, pageCount)
}

func renderRuntimeModelPickerPopupLines(state runtimeModelPickerState, currentModel, currentMatch, notice, warning string, selected int) []string {
	pageOptions, _, _, total := state.pageWindow()
	if total == 0 && strings.TrimSpace(warning) == "" {
		warning = "没有匹配的模型"
	}
	hint := "提示: ↑↓ 选择，回车确认；关键词搜索；n/p 翻页；c 清除搜索；编号按当前页；+模型名选择自定义模型"
	return renderSelectionPopupLines(
		runtimeModelPickerTitle(state),
		"模型",
		currentModel,
		pageOptions,
		currentMatch,
		"",
		hint,
		notice,
		warning,
		selected,
	)
}

func printRuntimeModelPickerLegacyPage(state runtimeModelPickerState, currentMatch string) {
	pageOptions, _, _, total := state.pageWindow()
	fmt.Printf("\n  %s\n", runtimeModelPickerTitle(state))
	if total == 0 {
		fmt.Println("  （无匹配模型；可继续搜索、输入 c 清除，或用 +模型名选择自定义模型）")
	} else {
		maxLen := 0
		for _, option := range pageOptions {
			maxLen = max(maxLen, ui.DisplayWidth(option))
		}
		for i, option := range pageOptions {
			label := padRuntimeSelectionOption(option, maxLen)
			if strings.EqualFold(option, currentMatch) {
				writeChatMutedSuffix(os.Stdout, fmt.Sprintf("  [%d] %s  ", i+1, label), "(当前)")
				continue
			}
			fmt.Printf("  [%d] %s\n", i+1, label)
		}
	}
	fmt.Println("  提示: 输入关键词或 /关键词搜索；n/p 翻页；c 清除搜索；编号按当前页；完整名称直接选择；+模型名选择自定义模型")
}

func runtimeModelPickerPopupPrompt() string {
	return "请输入选项 (回车确认高亮项；支持关键词搜索、n/p 翻页): "
}

func runtimeModelPickerLegacyPrompt() string {
	return "请输入选项 (回车保持当前；支持关键词搜索、n/p 翻页): "
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

	notice, restoreInput := prepareRuntimeSelectionInput(session, "reasoning_effort 选择")
	defer restoreInput()
	hint := "  提示: ↑↓ 选择，回车确认高亮项；也可输入编号/名称/自定义值，0 清空"
	prompt := reasoningEffortSelectionPrompt(currentValid, defaultOption)
	selectedIndex := initialRuntimeSelectionIndex(normalizedOptions, currentMatch, defaultOption)
	render := func(selected int, warning string) []string {
		return renderSelectionPopupLines("选择 reasoning_effort 值", "reasoning_effort", currentEffort, normalizedOptions, currentMatch, defaultOption, hint, notice, warning, selected)
	}
	handle := beginRuntimeSelectionPopup(session, render(selectedIndex, ""), prompt)
	defer clearRuntimeSelectionPopupHandle(session, handle)
	controller := newRuntimeSelectionController(session, handle, prompt, normalizedOptions, selectedIndex, render)

	for {
		text, err := chatInteractiveReadSelectionLine(session, prompt, controller)
		if err != nil {
			return "", true, err
		}
		text = strings.TrimSpace(normalizeQueuedInputLine(text))
		normalized := runtimetypes.NormalizeReasoningEffort(text)
		selected, ok := resolveRuntimeReasoningEffortInputWithCursor(normalized, currentMatch, currentValid, defaultOption, normalizedOptions, controller.Selected())
		if ok {
			return selected, true, nil
		}
		controller.SetWarning("  无效的选择，请重新输入")
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

	notice, restoreInput := prepareRuntimeSelectionInput(session, "reasoning_effort 选择")
	defer restoreInput()
	if notice != "" {
		fmt.Printf("\n%s\n", formatInteractiveSupplementPromptLine(notice))
	}

	ui.PrintSection("选择 reasoning_effort 值")
	switch {
	case currentEffort == "":
		fmt.Println("  当前 reasoning_effort: (无)")
	case currentValid:
		writeChatMutedSuffix(os.Stdout, "  当前 reasoning_effort: ", currentMatch, " (当前)")
	default:
		writeChatMutedSuffix(os.Stdout, "  当前 reasoning_effort: ", currentEffort, " (当前模型不支持)")
	}

	maxLen := 0
	for _, option := range normalizedOptions {
		if len(option) > maxLen {
			maxLen = len(option)
		}
	}
	for i, option := range normalizedOptions {
		primary := fmt.Sprintf("  [%d] %-*s  ", i+1, maxLen, option)
		switch {
		case option == currentMatch:
			writeChatMutedSuffix(os.Stdout, primary, "(当前)")
		case defaultOption != "" && option == defaultOption:
			writeChatMutedSuffix(os.Stdout, primary, "(默认)")
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
		case "0", "clear", "清空", "无":
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

		// The model card supplies picker options, not a request allowlist.
		// Keep a manually entered provider-specific value so it can be sent
		// upstream unchanged (apart from normalization).
		if normalized != "" {
			return normalized, false, nil
		}

		ui.PrintWarning("无效的选择，请重新输入")
	}
}

func useRuntimeSelectionPopup(session *ChatSession) bool {
	return newChatPromptOverlay(session).surfaceEnabled()
}

func showRuntimeSelectionPopup(session *ChatSession, lines []string, prompt string) {
	newChatPromptOverlay(session).showSelectionPopup(lines, prompt)
}

func beginRuntimeSelectionPopup(session *ChatSession, lines []string, prompt string) ui.PopupHandle {
	handle, _ := newChatPromptOverlay(session).beginSelectionPopup(lines, prompt)
	return handle
}

func updateRuntimeSelectionPopup(session *ChatSession, handle ui.PopupHandle, lines []string, prompt string) bool {
	return newChatPromptOverlay(session).updatePopupInput(handle, lines, prompt, false)
}

func clearRuntimeSelectionPopup(session *ChatSession) {
	newChatPromptOverlay(session).clearSelectionPopup()
}

func clearRuntimeSelectionPopupHandle(session *ChatSession, handle ui.PopupHandle) {
	newChatPromptOverlay(session).clearPopupHandle(handle)
}

func prepareRuntimeSelectionInput(session *ChatSession, promptKind string) (string, func()) {
	restoreMode := pushChatComposerInputMode(session, chatInputModeSelection)
	suspension, notice := suspendPendingInteractiveInputForPriorityPrompt(session, promptKind)
	return notice, func() {
		if suspension != nil {
			suspension.Restore()
		}
		restoreMode()
	}
}

func renderSelectionPopupLines(title, currentLabel, currentValue string, options []string, currentMatch, defaultOption, hint, notice, warning string, selectedIndex int) []string {
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
			if width := ui.DisplayWidth(option); width > maxLen {
				maxLen = width
			}
		}
		if selectedIndex < 0 || selectedIndex >= len(options) {
			selectedIndex = -1
		}
		for i, option := range options {
			marker := " "
			if i == selectedIndex {
				marker = ">"
			}
			paddedOption := padRuntimeSelectionOption(option, maxLen)
			switch {
			case strings.EqualFold(option, currentMatch):
				lines = append(lines, fmt.Sprintf(" %s[%d] %s  (当前)", marker, i+1, paddedOption))
			case defaultOption != "" && strings.EqualFold(option, defaultOption):
				lines = append(lines, fmt.Sprintf(" %s[%d] %s  (默认)", marker, i+1, paddedOption))
			default:
				lines = append(lines, fmt.Sprintf(" %s[%d] %s", marker, i+1, paddedOption))
			}
		}
	}
	if hint = strings.TrimSpace(hint); hint != "" {
		lines = append(lines, hint)
	}
	return lines
}

func padRuntimeSelectionOption(option string, width int) string {
	padding := width - ui.DisplayWidth(option)
	if padding <= 0 {
		return option
	}
	return option + strings.Repeat(" ", padding)
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
	case "0", "clear", "清空", "无":
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

	// 允许输入自定义值
	normalized := runtimetypes.NormalizeReasoningEffort(input)
	if normalized != "" {
		return normalized, true
	}
	return "", false
}

func reasoningEffortSelectionPrompt(currentValid bool, defaultOption string) string {
	switch {
	case currentValid:
		return "请输入选项 (回车保留当前 / 输入 0 清空 / 可输入自定义值): "
	case defaultOption != "":
		return fmt.Sprintf("请输入选项 (回车默认: %s / 输入 0 清空 / 可输入自定义值): ", defaultOption)
	default:
		return "请输入选项 (回车清空当前无效值 / 输入 0 清空 / 可输入自定义值): "
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
