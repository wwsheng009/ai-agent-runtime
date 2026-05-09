package commands

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

// handleResumeCommand implements the /resume slash command.
//
// Behavior:
//   - /resume                  -> interactive menu (latest / pick from list / cancel).
//     Falls back to the latest resumable session when interaction is unavailable.
//   - /resume latest           -> legacy "resume latest" behavior, resolved as the latest resumable session.
//   - /resume <session-id>     -> load that session by ID (alias of /load).
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

	arg := strings.TrimSpace(extractCommandArgument(command))
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

func resumeLatestAndPrint(session *ChatSession) bool {
	if err := resumeLatestRuntimeConversation(session); err != nil {
		fmt.Printf("错误: %v\n", err)
		return false
	}
	fmt.Println("已恢复最近可恢复会话")
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

	sessions, err := listResumeCandidateChatSessions(session.SessionManager, session.SessionUserID, session.SessionFilter, currentRuntimeSessionID(session))
	if err != nil {
		fmt.Printf("错误: %v\n", err)
		return false
	}
	if len(sessions) == 0 {
		fmt.Println("当前没有可恢复的历史会话")
		return false
	}

	beginDirectInteractiveOutput(session)
	uiPrintSessionSelectionSummary(len(sessions), session.SessionFilter)
	optionWidth := startupSessionOptionLabelWidth()

	choice, err := readResumeMenuChoice(session, optionWidth)
	if err != nil {
		fmt.Printf("错误: %v\n", err)
		return false
	}
	switch choice {
	case resumeChoiceLatest:
		return resumeLatestAndPrint(session)
	case resumeChoiceCancel:
		fmt.Println("已取消恢复，可继续在新会话中输入或使用 /new、/sessions、/load")
		return false
	case resumeChoicePick:
		picked, err := readResumeSessionPick(session, sessions)
		if err != nil {
			fmt.Printf("错误: %v\n", err)
			return false
		}
		if picked == nil {
			fmt.Println("已取消恢复")
			return false
		}
		if err := loadRuntimeConversation(session, picked.ID); err != nil {
			fmt.Printf("错误: %v\n", err)
			return false
		}
		printResumeSuccess(session)
		return false
	}
	return false
}

type resumeMenuChoice int

const (
	resumeChoiceLatest resumeMenuChoice = iota
	resumeChoicePick
	resumeChoiceCancel
)

func readResumeMenuChoice(session *ChatSession, optionWidth int) (resumeMenuChoice, error) {
	prompt := "选项 (回车=1): "
	usePopup := useRuntimeSelectionPopup(session)
	if usePopup {
		defer clearRuntimeSelectionPopup(session)
	}
	warning := ""
	for {
		lines := []string{
			fmt.Sprintf("  %-*s %s", optionWidth, "[1]", "恢复最近可恢复会话"),
			fmt.Sprintf("  %-*s %s", optionWidth, "[2]", "选择历史会话"),
			fmt.Sprintf("  %-*s %s", optionWidth, "[3]", "取消（返回当前会话）"),
		}
		if usePopup {
			popupLines := append([]string(nil), lines...)
			if warning != "" {
				popupLines = append(popupLines, warning)
			}
			showRuntimeSelectionPopup(session, popupLines, prompt)
		} else {
			for _, line := range lines {
				fmt.Println(line)
			}
			fmt.Print(prompt)
		}

		text, err := chatInteractiveReadPriorityLineWithPrompt(session, context.Background(), prompt)
		if !usePopup {
			fmt.Println()
		}
		if err != nil {
			return resumeChoiceCancel, err
		}
		choice := strings.TrimSpace(normalizeQueuedInputLine(text))
		warning = ""
		switch choice {
		case "", "1":
			return resumeChoiceLatest, nil
		case "2":
			return resumeChoicePick, nil
		case "3", "q", "quit", "cancel", "exit":
			return resumeChoiceCancel, nil
		default:
			if usePopup {
				warning = "  无效的选择，请重新输入"
			} else {
				ui.PrintWarning("无效的选择，请重新输入")
			}
		}
	}
}

func readResumeSessionPick(session *ChatSession, sessions []*runtimechat.Session) (*runtimechat.Session, error) {
	prompt := "编号 (回车=1, q取消): "
	usePopup := useRuntimeSelectionPopup(session)
	if usePopup {
		defer clearRuntimeSelectionPopup(session)
	}
	now := time.Now()
	lines := []string{"历史会话:"}
	for index, item := range sessions {
		if item == nil {
			continue
		}
		itemLine := renderRuntimeResumeSessionLine(item, now)
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
			return sessions[0], nil
		}
		if choice == "q" || choice == "quit" || choice == "cancel" || choice == "exit" {
			return nil, nil
		}

		var index int
		if _, err := fmt.Sscanf(choice, "%d", &index); err == nil {
			if index >= 1 && index <= len(sessions) {
				return sessions[index-1], nil
			}
			if usePopup {
				warning = "  无效的选择，请重新输入"
			} else {
				ui.PrintWarning("无效的选择，请重新输入")
			}
			continue
		}

		for _, item := range sessions {
			if item != nil && item.ID == choice {
				return item, nil
			}
		}
		if usePopup {
			warning = "  未找到会话，请重新输入"
		} else {
			ui.PrintWarning("未找到会话，请重新输入")
		}
	}
}

func printResumeSuccess(session *ChatSession) {
	printCurrentRuntimeSession(session)
	if hasVisibleChatHistory(session) {
		fmt.Println()
		printVisibleChatHistory(session, "已加载历史会话")
	}
}
