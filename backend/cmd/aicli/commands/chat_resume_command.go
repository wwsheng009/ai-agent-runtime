package commands

import (
	"context"
	"errors"
	"fmt"
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

	sessions, err := listResumeCandidateChatSessions(session.SessionManager, session.SessionUserID, session.SessionFilter, currentRuntimeSessionID(session))
	if err != nil {
		fmt.Printf("错误: %v\n", err)
		return false
	}
	if len(sessions) == 0 {
		fmt.Println("当前没有其他可恢复的历史会话")
		return false
	}

	beginDirectInteractiveOutput(session)
	picked, err := readResumeSessionPick(session, sessions)
	if err != nil {
		fmt.Printf("错误: %v\n", err)
		return false
	}
	if picked == nil {
		fmt.Println("已取消恢复，当前会话保持不变")
		return false
	}
	if err := loadRuntimeConversation(session, picked.ID); err != nil {
		fmt.Printf("错误: %v\n", err)
		return false
	}
	printResumeSuccess(session)
	return false
}

func readResumeSessionPick(session *ChatSession, sessions []*runtimechat.Session) (*runtimechat.Session, error) {
	return readHistoricalSessionPick(
		session,
		sessions,
		fmt.Sprintf("恢复历史会话（最近更新优先，共 %d 个）:", len(sessions)),
		"选择会话 (回车恢复 1，q 取消): ",
	)
}

func readHistoricalSessionPick(session *ChatSession, sessions []*runtimechat.Session, header, prompt string) (*runtimechat.Session, error) {
	usePopup := useRuntimeSelectionPopup(session)
	if usePopup {
		defer clearRuntimeSelectionPopup(session)
	}
	now := time.Now()
	lines := []string{strings.TrimSpace(header)}
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
		fmt.Printf("已恢复历史会话: %s（%d轮/%d条消息）\n",
			runtimeResumeSessionTitle(session.RuntimeSession),
			turnCount,
			messageCount,
		)
	}
	printCurrentRuntimeSession(session)
	if hasVisibleChatHistory(session) {
		fmt.Println()
		printVisibleChatHistory(session, "已加载历史会话")
	}
}
