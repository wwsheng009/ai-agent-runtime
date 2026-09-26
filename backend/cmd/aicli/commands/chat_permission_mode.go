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
	case runtimepolicy.ModeDontAsk, "dont-ask":
		return runtimepolicy.ModeDontAsk, nil
	default:
		return "", fmt.Errorf("无效的 permission-mode: %s（可选值: default|accept_edits|plan|bypass_permissions|dont_ask）", raw)
	}
}

// chatPermissionModeCycleOrder 是权限模式循环键（shift+tab / alt+m）的顺序，
// 对齐 CommandCode Interactive Mode：default → accept_edits → plan →
// bypass_permissions → default。
var chatPermissionModeCycleOrder = []runtimepolicy.Mode{
	runtimepolicy.ModeDefault,
	runtimepolicy.ModeAcceptEdits,
	runtimepolicy.ModePlan,
	runtimepolicy.ModeBypassPermissions,
}

// nextChatPermissionMode 返回循环中的下一档模式；未知/空值回到第一档。
func nextChatPermissionMode(current runtimepolicy.Mode) runtimepolicy.Mode {
	for index, mode := range chatPermissionModeCycleOrder {
		if mode == current {
			return chatPermissionModeCycleOrder[(index+1)%len(chatPermissionModeCycleOrder)]
		}
	}
	return chatPermissionModeCycleOrder[0]
}

// cycleChatPermissionMode 执行一次权限模式循环切换。进入 bypass_permissions
// 仍复用 /permission-mode 的二次确认；返回 true 表示按键已被消费（无论是否
// 真的切换，避免按键漏进编辑器）。
func cycleChatPermissionMode(session *ChatSession) bool {
	if session == nil {
		return false
	}
	current := chatSessionPermissionMode(session)
	next := nextChatPermissionMode(current)
	if next == runtimepolicy.ModeBypassPermissions && !confirmBypassPermissionModeChange(session, "shift+tab（权限模式循环）") {
		return true
	}
	setChatPermissionMode(session, next)
	warnIfChatSessionSyncFails(session, "cycle permission mode", syncRuntimeSessionFromChat(session))
	printfChatCommandOutput(session, "提示: 已切换到 permission-mode=%s", next)
	return true
}
