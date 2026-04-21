package commands

import (
	"fmt"
	"strings"
)

type chatApprovalReuseMode string

const (
	chatApprovalReuseOff                  chatApprovalReuseMode = "off"
	chatApprovalReuseSessionReadOnlyShell chatApprovalReuseMode = "session_readonly_shell"
	chatApprovalReuseTeamReadOnlyShell    chatApprovalReuseMode = "team_readonly_shell"
)

func parseChatApprovalReuseMode(raw string) (chatApprovalReuseMode, error) {
	switch chatApprovalReuseMode(strings.ToLower(strings.TrimSpace(raw))) {
	case "", chatApprovalReuseSessionReadOnlyShell:
		return chatApprovalReuseSessionReadOnlyShell, nil
	case chatApprovalReuseTeamReadOnlyShell:
		return chatApprovalReuseTeamReadOnlyShell, nil
	case chatApprovalReuseOff:
		return chatApprovalReuseOff, nil
	default:
		return "", fmt.Errorf("无效的 approval-reuse: %s（可选值: off|session_readonly_shell|team_readonly_shell）", raw)
	}
}

func formatChatApprovalReuseMode(mode chatApprovalReuseMode) string {
	switch mode {
	case chatApprovalReuseOff:
		return "off"
	case chatApprovalReuseSessionReadOnlyShell:
		return "session_readonly_shell"
	case chatApprovalReuseTeamReadOnlyShell:
		return "team_readonly_shell"
	default:
		return string(chatApprovalReuseSessionReadOnlyShell)
	}
}
