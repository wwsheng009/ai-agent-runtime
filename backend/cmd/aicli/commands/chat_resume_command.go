package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

// handleResumeCommand implements the /resume slash command.
//
// Behavior:
//   - /resume                  -> interactive history list, newest session first.
//     Falls back to the latest resumable session when interaction is unavailable.
//   - /resume latest           -> legacy "resume latest" behavior, resolved as the latest resumable session.
//   - /resume <session-id>     -> load that session by ID (alias of /load).
//   - /resume [latest] --cwd   -> explicitly keep the default current-working-directory filter.
//
// The function never exits the chat loop, mirroring the rest of the slash commands.
func handleResumeCommand(session *ChatSession, command string) bool {
	if session == nil {
		fmt.Println("错误: 当前没有活动会话")
		return false
	}
	if session.SessionManager == nil {
		fmt.Println("错误: 会话管理未启用")
		return false
	}

	arg, filter, err := parseResumeCommandArgument(extractCommandArgument(command), session.SessionFilter, session)
	if err != nil {
		fmt.Printf("错误: %v\n", err)
		return false
	}
	previousFilter := session.SessionFilter
	session.SessionFilter = filter
	defer func() { session.SessionFilter = previousFilter }()

	switch strings.ToLower(arg) {
	case "":
		return resumeInteractiveSelect(session)
	case "latest", "last", "--latest", "-l":
		return resumeLatestAndPrint(session)
	}

	if currentID := currentRuntimeSessionID(session); currentID != "" && strings.EqualFold(currentID, arg) {
		fmt.Println("当前已经在该会话中，无需恢复")
		return false
	}
	if err := loadRuntimeConversation(session, arg); err != nil {
		fmt.Printf("错误: %v\n", err)
		return false
	}
	printResumeSuccess(session)
	return false
}

func parseResumeCommandArgument(argument string, filter ChatSessionListFilter, session *ChatSession) (string, ChatSessionListFilter, error) {
	var target string
	for _, token := range splitChatCommandFields(strings.TrimSpace(argument)) {
		switch strings.ToLower(strings.TrimSpace(token)) {
		case "--cwd":
			workspace := ""
			if session != nil {
				workspace = strings.TrimSpace(resolveLocalWorkspacePath(loadRuntimeToolConfig(session.Config, nil), nil))
			}
			if workspace == "" {
				currentDir, err := os.Getwd()
				if err != nil {
					return "", filter, fmt.Errorf("获取当前工作目录失败: %w", err)
				}
				workspace = currentDir
			}
			filter.Workspace = normalizeChatSessionWorkspace(workspace)
		default:
			if target != "" {
				return "", filter, fmt.Errorf("/resume 最多接受一个会话目标；可选参数为 --cwd")
			}
			target = strings.TrimSpace(token)
		}
	}
	return target, filter, nil
}

func resumeLatestAndPrint(session *ChatSession) bool {
	if err := resumeLatestRuntimeConversation(session); err != nil {
		if errors.Is(err, runtimechat.ErrSessionNotFound) {
			fmt.Println("当前没有其他可恢复的历史会话")
			return false
		}
		fmt.Printf("错误: %v\n", err)
		return false
	}
	printResumeSuccess(session)
	return false
}

func resumeInteractiveSelect(session *ChatSession) bool {
	// Non-interactive contexts (JSON output, no-interactive mode) keep the
	// legacy "resume latest" behavior so scripts are unaffected, but the
	// selection still skips system-only placeholder sessions.
	if session.NoInteractive || session.JSONOutput {
		return resumeLatestAndPrint(session)
	}

	currentID := currentRuntimeSessionID(session)
	current := currentRuntimeSessionForResumeList(session)
	sessions, err := listResumeCandidateChatSessions(session.SessionManager, session.SessionUserID, session.SessionFilter, currentID)
	if err != nil {
		fmt.Printf("错误: %v\n", err)
		return false
	}
	if len(sessions) == 0 && current == nil {
		fmt.Println("当前没有其他可恢复的历史会话")
		return false
	}

	beginDirectInteractiveOutput(session)
	picked, err := readResumeSessionPick(session, sessions, current)
	if err != nil {
		fmt.Printf("错误: %v\n", err)
		return false
	}
	if picked == nil {
		if len(sessions) == 0 {
			fmt.Println("当前没有其他可恢复的历史会话")
		} else {
			fmt.Println("已取消恢复，当前会话保持不变")
		}
		return false
	}
	if err := loadRuntimeConversation(session, picked.ID); err != nil {
		fmt.Printf("错误: %v\n", err)
		return false
	}
	printResumeSuccess(session)
	return false
}

func currentRuntimeSessionForResumeList(session *ChatSession) *runtimechat.Session {
	if session == nil || session.RuntimeSession == nil {
		return nil
	}
	// Prefer the live in-memory session so a just-renamed title is visible
	// immediately without requiring process restart or a re-list from storage.
	return session.RuntimeSession
}

func readResumeSessionPick(session *ChatSession, sessions []*runtimechat.Session, current *runtimechat.Session) (*runtimechat.Session, error) {
	if terminal := resumeFullScreenTerminal(session); ui.CanUseFullScreenList(terminal) {
		picked, err := readResumeSessionPickFullScreen(session, terminal, sessions, current)
		if err == nil || !errors.Is(err, ui.ErrFullScreenUnavailable) {
			return picked, err
		}
	}
	return readResumePlainSessionPick(
		session,
		sessions,
		current,
	)
}

func resumeFullScreenTerminal(session *ChatSession) *ui.Terminal {
	if session == nil || session.Layout == nil {
		return nil
	}
	return session.Layout.Terminal()
}

func readResumeSessionPickFullScreen(session *ChatSession, terminal *ui.Terminal, sessions []*runtimechat.Session, current *runtimechat.Session) (*runtimechat.Session, error) {
	items, selectable := buildResumeFullScreenItems(sessions, current, time.Now())
	if len(items) == 0 {
		// Still allow opening the picker when only the current session is present so
		// users can verify the live title after /rename or /title without restarting.
		return nil, nil
	}

	surfaceEnabled := session != nil && session.Surface != nil && session.Surface.Enabled()
	if session != nil && session.Interaction != nil {
		session.Interaction.ClearPrompt()
		session.Interaction.ResetPromptState()
	}
	if surfaceEnabled {
		session.Surface.Disable()
	}
	defer func() {
		if surfaceEnabled {
			session.Surface.Enable()
		}
		if session != nil && session.Interaction != nil {
			session.Interaction.ResetPromptState()
			session.Interaction.RefreshStatus("")
		}
	}()

	selectableCount := 0
	for _, item := range items {
		if !item.Disabled {
			selectableCount++
		}
	}
	result, err := ui.SelectFullScreenList(context.Background(), terminal, ui.FullScreenListOptions{
		Title:        "恢复历史会话",
		Subtitle:     formatResumePickerSubtitle(selectableCount, current != nil),
		EmptyMessage: "没有匹配的历史会话",
		ConfirmLabel: "恢复选中会话",
		Items:        items,
	})
	if err != nil {
		return nil, err
	}
	if result.Cancelled || result.Index < 0 || result.Index >= len(selectable) {
		return nil, nil
	}
	if selectable[result.Index] == nil {
		return nil, nil
	}
	return selectable[result.Index], nil
}

func formatResumePickerSubtitle(selectableCount int, includesCurrent bool) string {
	if includesCurrent {
		return fmt.Sprintf("最近更新优先，共 %d 个可恢复会话 · 当前会话仅展示", selectableCount)
	}
	return fmt.Sprintf("最近更新优先，共 %d 个可恢复会话", selectableCount)
}

func buildResumeFullScreenItems(sessions []*runtimechat.Session, current *runtimechat.Session, now time.Time) ([]ui.FullScreenListItem, []*runtimechat.Session) {
	capacity := len(sessions)
	if current != nil {
		capacity++
	}
	items := make([]ui.FullScreenListItem, 0, capacity)
	// selectable is index-aligned with items so FullScreenListResult.Index maps
	// directly back to a session pointer; disabled/current rows store nil.
	selectable := make([]*runtimechat.Session, 0, capacity)
	if current != nil {
		items = append(items, buildResumeFullScreenItem(current, now, true))
		selectable = append(selectable, nil)
	}
	for _, item := range sessions {
		if item == nil {
			continue
		}
		items = append(items, buildResumeFullScreenItem(item, now, false))
		selectable = append(selectable, item)
	}
	return items, selectable
}

func buildResumeFullScreenItem(session *runtimechat.Session, now time.Time, current bool) ui.FullScreenListItem {
	turnCount, messageCount := runtimeSessionConversationCounts(session)
	title := runtimeResumeSessionTitle(session)
	if current {
		title = formatCurrentResumeSessionTitle(title)
	}
	preview := session.BuildPreview()
	summary := ""
	if preview != nil {
		summary = strings.TrimSpace(preview.Summary)
	}
	if summary == "" || strings.EqualFold(summary, runtimeResumeSessionTitle(session)) {
		summary = fmt.Sprintf("%d 轮对话，%d 条消息", turnCount, messageCount)
	}
	if current {
		summary = "当前会话（不可选） · " + summary
	}
	generation := runtimeSessionCompactGeneration(session)
	detailParts := []string{
		// Resume picker shows relative age only; absolute timestamps clutter the list.
		formatSessionRelativeTime(session.UpdatedAt, now),
		fmt.Sprintf("%d轮/%d条", turnCount, messageCount),
	}
	workspacePath := runtimeSessionWorkspacePath(session)
	if workspacePath != "" {
		detailParts = append(detailParts, workspacePath)
	}
	if generation > 0 {
		detailParts = append(detailParts, fmt.Sprintf("compact #%d", generation))
	}
	if current {
		detailParts = append(detailParts, "当前 · 不可选")
	}
	detail := strings.Join(detailParts, "  ")
	searchText := strings.Join([]string{
		session.ID,
		title,
		runtimeResumeSessionTitle(session),
		runtimeSessionContextString(session, runtimechat.ContextCompactRootTitle),
		runtimeSessionContextString(session, chatRuntimeContextProtocol),
		runtimeSessionContextString(session, chatRuntimeContextProviderName),
		runtimeSessionContextString(session, chatRuntimeContextModel),
		workspacePath,
		"当前",
	}, " ")
	return ui.FullScreenListItem{
		Title:      title,
		Detail:     detail,
		Preview:    summary,
		SearchText: searchText,
		Disabled:   current,
	}
}

func formatCurrentResumeSessionTitle(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "(untitled)"
	}
	return fmt.Sprintf("当前 · %s（不可选）", title)
}

func readResumePlainSessionPick(session *ChatSession, sessions []*runtimechat.Session, current *runtimechat.Session) (*runtimechat.Session, error) {
	header := formatResumePlainPickerHeader(len(sessions), current != nil)
	prompt := "选择会话 (回车恢复 1，q 取消): "
	if len(sessions) == 0 {
		prompt = "没有其他可恢复会话，输入 q 返回: "
	}
	return readHistoricalSessionPickWithCurrent(session, sessions, current, header, prompt)
}

func formatResumePlainPickerHeader(selectableCount int, includesCurrent bool) string {
	if includesCurrent {
		return fmt.Sprintf("恢复历史会话（最近更新优先，共 %d 个可恢复 · 当前会话仅展示）:", selectableCount)
	}
	return fmt.Sprintf("恢复历史会话（最近更新优先，共 %d 个）:", selectableCount)
}

func readHistoricalSessionPick(session *ChatSession, sessions []*runtimechat.Session, header, prompt string) (*runtimechat.Session, error) {
	return readHistoricalSessionPickWithCurrent(session, sessions, nil, header, prompt)
}

func readHistoricalSessionPickWithCurrent(session *ChatSession, sessions []*runtimechat.Session, current *runtimechat.Session, header, prompt string) (*runtimechat.Session, error) {
	usePopup := useRuntimeSelectionPopup(session)
	if usePopup {
		defer clearRuntimeSelectionPopup(session)
	}
	now := time.Now()
	lines := []string{strings.TrimSpace(header)}
	titleSessions := make([]*runtimechat.Session, 0, len(sessions)+1)
	if current != nil {
		titleSessions = append(titleSessions, current)
	}
	titleSessions = append(titleSessions, sessions...)
	titleWidth := maxRuntimeResumeSessionTitleWidth(titleSessions)
	if current != nil {
		// Account for the "当前 · …（不可选）" wrapper so history columns still align.
		currentTitleWidth := ui.DisplayWidth(formatCurrentResumeSessionTitle(runtimeResumeSessionTitle(current)))
		if currentTitleWidth > titleWidth {
			titleWidth = currentTitleWidth
		}
		if titleWidth > resumeSessionTitleColumnMaxWidth {
			titleWidth = resumeSessionTitleColumnMaxWidth
		}
		// Keep the current row unnumbered so the history numbering stays 1..N.
		currentLine := renderRuntimeResumeCurrentSessionLine(current, now, titleWidth)
		if currentLine != "" {
			lines = append(lines, truncateStatusValue("  [·] "+currentLine, ui.GetTerminalWidth()))
		}
	}
	for index, item := range sessions {
		if item == nil {
			continue
		}
		itemLine := renderRuntimeResumeSessionLine(item, now, titleWidth)
		if strings.TrimSpace(itemLine) == "" {
			continue
		}
		itemLine = truncateStatusValue(fmt.Sprintf("  [%-2d] %s", index+1, strings.TrimSpace(itemLine)), ui.GetTerminalWidth())
		lines = append(lines, itemLine)
	}
	if !usePopup {
		for _, line := range lines {
			fmt.Println(line)
		}
	}

	warning := ""
	for {
		if usePopup {
			popupLines := append([]string(nil), lines...)
			if warning != "" {
				popupLines = append(popupLines, warning)
			}
			showRuntimeSelectionPopup(session, popupLines, prompt)
		} else {
			fmt.Print(prompt)
		}

		text, err := chatInteractiveReadPriorityLineWithPrompt(session, context.Background(), prompt)
		if !usePopup {
			fmt.Println()
		}
		if err != nil {
			return nil, err
		}
		choice := strings.TrimSpace(normalizeQueuedInputLine(text))
		warning = ""
		if choice == "" || choice == "1" {
			if len(sessions) == 0 {
				return nil, nil
			}
			return sessions[0], nil
		}
		if choice == "q" || choice == "quit" || choice == "cancel" || choice == "exit" {
			return nil, nil
		}

		if index, err := strconv.Atoi(choice); err == nil {
			if index >= 1 && index <= len(sessions) {
				return sessions[index-1], nil
			}
			message := fmt.Sprintf("请输入 1-%d 之间的编号，或输入 q 取消", len(sessions))
			if usePopup {
				warning = "  " + message
			} else {
				ui.PrintWarning("%s", message)
			}
			continue
		}

		for _, item := range sessions {
			if item != nil && item.ID == choice {
				return item, nil
			}
		}
		if usePopup {
			warning = "  未找到该会话，请输入列表编号或 q 取消"
		} else {
			ui.PrintWarning("未找到该会话，请输入列表编号或 q 取消")
		}
	}
}

func printResumeSuccess(session *ChatSession) {
	if session != nil && session.RuntimeSession != nil {
		turnCount, messageCount := runtimeSessionConversationCounts(session.RuntimeSession)
		title := runtimeResumeSessionTitle(session.RuntimeSession)
		var line string
		if generation := runtimeSessionCompactGeneration(session.RuntimeSession); generation > 0 {
			line = fmt.Sprintf("已恢复历史会话: %s（compact #%d · %d轮/%d条消息）\n",
				title,
				generation,
				turnCount,
				messageCount,
			)
		} else {
			line = fmt.Sprintf("已恢复历史会话: %s（%d轮/%d条消息）\n",
				title,
				turnCount,
				messageCount,
			)
		}
		// Prefer surface WriteOutput so ClearPrompt shrink debt flushes here
		// instead of attaching to the first history content block.
		if !writeDirectInteractiveOutput(session, line) {
			fmt.Print(line)
		}
	}
	printCurrentRuntimeSession(session)
	if hasVisibleChatHistory(session) {
		// No raw fmt.Println: history settles layout then owns spacing via header.
		printVisibleChatHistory(session, "已加载历史会话")
	}
}
