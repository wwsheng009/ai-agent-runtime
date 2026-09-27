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
	if strings.TrimSpace(raw) == "" {
		return runtimepolicy.ModeDefault, nil
	}
	// 统一走 policy.ParseMode：CLI 与 runtime API / ACP 接受同一套值（含
	// §4.12 的兼容别名），避免两侧对「什么是合法模式」出现分歧。
	if mode, ok := runtimepolicy.ParseMode(raw); ok {
		return mode, nil
	}
	return "", fmt.Errorf(
		"无效的 permission-mode: %s（可选值: default|accept_edits|plan|bypass_permissions|dont_ask；兼容别名: manual|standard|auto-accept|acceptEdits|bypass|dontAsk）",
		raw)
}

// resolveChatPermissionModeFlags 统一解析 --permission-mode 与三个快捷旗标
// （--yolo / --accept-edits / --plan，§4.12）。多个来源冲突时报错而不是静默取
// 其中一个：权限模式是安全语义，静默选择会让用户以为生效的是另一个模式。
func resolveChatPermissionModeFlags(modeFlag string, modeFlagChanged, yolo, acceptEdits, plan bool) (runtimepolicy.Mode, error) {
	type shorthand struct {
		flag string
		mode runtimepolicy.Mode
	}
	set := make([]shorthand, 0, 3)
	if yolo {
		set = append(set, shorthand{"--yolo", runtimepolicy.ModeBypassPermissions})
	}
	if acceptEdits {
		set = append(set, shorthand{"--accept-edits", runtimepolicy.ModeAcceptEdits})
	}
	if plan {
		set = append(set, shorthand{"--plan", runtimepolicy.ModePlan})
	}
	if len(set) > 1 {
		names := make([]string, 0, len(set))
		for _, item := range set {
			names = append(names, item.flag)
		}
		return "", fmt.Errorf("参数冲突：%s 只能指定一个（权限模式入口，§4.12）", strings.Join(names, "、"))
	}
	if len(set) == 1 {
		if modeFlagChanged {
			explicit, err := parseChatPermissionMode(modeFlag, false)
			if err != nil {
				return "", err
			}
			if explicit != set[0].mode {
				return "", fmt.Errorf("参数冲突：--permission-mode=%s 与 %s（=%s）不一致", explicit, set[0].flag, set[0].mode)
			}
		}
		return set[0].mode, nil
	}
	return parseChatPermissionMode(modeFlag, false)
}

// permissionModeColonShorthand 只匹配冒号简写（`/mode:<name>`、
// `/permission-mode:<name>`，§4.12）。基础命令名仍由字面量 commandMatches
// 判定——`TestChatSlashCommandCatalogMatchesHandleCommandRoutes` 会扫描源码里
// 的路由字面量，全部收敛进 helper 会让该护栏看不到这两条路由。
func permissionModeColonShorthand(commandLower string) bool {
	commandLower = strings.TrimSpace(strings.ToLower(commandLower))
	return strings.HasPrefix(commandLower, "/mode:") || strings.HasPrefix(commandLower, "/permission-mode:")
}

// permissionModeCommandMatches 匹配 `/mode`、`/permission-mode` 及其冒号简写。
func permissionModeCommandMatches(commandLower string) bool {
	if commandMatches(commandLower, "/mode") || commandMatches(commandLower, "/permission-mode") {
		return true
	}
	return permissionModeColonShorthand(commandLower)
}

// permissionModeCommandArgument 取 `/mode`、`/permission-mode`、`/mode:<name>`
// 三种写法的参数（冒号形只在命令名之后，不改变其它命令的参数解析）。
func permissionModeCommandArgument(command string) string {
	trimmed := strings.TrimSpace(command)
	// 先确认是权限模式命令族，避免把其它命令的参数误当成模式值（该 helper 只在
	// 权限模式处理入口使用，这里做一次自包含校验以防复用出错）。
	if !permissionModeCommandMatches(strings.ToLower(trimmed)) {
		return ""
	}
	if value := strings.TrimSpace(extractCommandArgument(trimmed)); value != "" {
		return value
	}
	lower := strings.ToLower(trimmed)
	for _, prefix := range []string{"/mode:", "/permission-mode:"} {
		if strings.HasPrefix(lower, prefix) {
			return strings.TrimSpace(trimmed[len(prefix):])
		}
	}
	return ""
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
