package commands

import (
	"fmt"
	"strings"
)

type chatApprovalReuseMode string

const (
	chatApprovalReuseOff               chatApprovalReuseMode = "off"
	chatApprovalReuseTeamReadOnlyShell chatApprovalReuseMode = "team_readonly_shell"
)

func parseChatApprovalReuseMode(raw string) (chatApprovalReuseMode, error) {
	switch chatApprovalReuseMode(strings.ToLower(strings.TrimSpace(raw))) {
	case "", chatApprovalReuseTeamReadOnlyShell:
		return chatApprovalReuseTeamReadOnlyShell, nil
	case chatApprovalReuseOff:
		return chatApprovalReuseOff, nil
	default:
		return "", fmt.Errorf("无效的 approval-reuse: %s（可选值: off|team_readonly_shell）", raw)
	}
}

func formatChatApprovalReuseMode(mode chatApprovalReuseMode) string {
	switch mode {
	case chatApprovalReuseOff:
		return "off"
	case chatApprovalReuseTeamReadOnlyShell:
		return "team_readonly_shell"
	default:
		return string(chatApprovalReuseTeamReadOnlyShell)
	}
}
