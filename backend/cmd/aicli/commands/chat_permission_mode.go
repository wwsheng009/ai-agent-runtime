package commands

import (
	"fmt"
	"strings"

	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
)

func parseChatPermissionMode(raw string, yolo bool) (runtimepolicy.Mode, error) {
	if yolo {
		return runtimepolicy.ModeBypassPermissions, nil
	}
	switch runtimepolicy.Mode(strings.ToLower(strings.TrimSpace(raw))) {
	case "", runtimepolicy.ModeDefault:
		return runtimepolicy.ModeDefault, nil
	case runtimepolicy.ModeAcceptEdits:
		return runtimepolicy.ModeAcceptEdits, nil
	case runtimepolicy.ModePlan:
		return runtimepolicy.ModePlan, nil
	case runtimepolicy.ModeBypassPermissions:
		return runtimepolicy.ModeBypassPermissions, nil
	default:
		return "", fmt.Errorf("无效的 permission-mode: %s（可选值: default|accept_edits|plan|bypass_permissions）", raw)
	}
}
