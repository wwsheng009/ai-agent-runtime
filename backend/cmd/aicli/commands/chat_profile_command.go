package commands

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
)

// chat_profile_command.go 实现 TUI `/profile` 命令面（设计文档 §17.1/§17.2，
// 实施方案 Batch 11a）。命令面只做三件事：只读报告（status/list/show/diff）、
// 显式切换（use/reload/off/save）与交互选择（pick）；所有切换都走 Batch 10 的
// 唯一执行核心 applyRuntimeProfileSwitch，命令层不复制任何失效逻辑。
//
// 失败模式（§17.2）：一律"解析不了就报错"，不猜、不隐式选第一个；失败路径必须
// 保持状态零改动（A6 由执行核心保证）。

// chatProfileUsageText 是 `/profile` 的用法文本（与 catalog spec 同源）。
func chatProfileUsageText() string {
	return strings.Join([]string{
		"用法:",
		"  /profile [status]                                  当前 profile：引用、来源、生效摘要",
		"  /profile list                                      可用 profile 列表（config/root/默认三层）",
		"  /profile show <name>                               只读预览：该 profile 将带来什么（不切换）",
		"  /profile diff [<name>]                             与当前对比：tools/skills/mcp/prompt 变化",
		"  /profile use <name>                                热切换（下一 turn 生效；含 cache_notice）",
		"  /profile pick                                      交互选择 profile",
		"  /profile reload                                    重新解析当前 profile（磁盘编辑后）",
		"  /profile off                                       回到无 profile 基线（完整失效）",
		"  /profile save [--to session|workspace|config] [--yes]   持久化默认 profile",
		"  /profile create <name> [--template coding|review|minimal|docs] [--to user|project] [--force]",
		"  /profile duplicate <ref> <name> [--to user|project]     复制（不覆盖同名）",
		"  /profile save-as <name> [--to user|project]        从当前会话固化差分（D24；不覆盖同名）",
		"  /profile edit [<ref>] [--open]                     打印 profile.yaml 路径；--open 拉起 $EDITOR",
		"  /profile rename <ref> <new-name>                   重命名（同层；含配置引用改写）",
		"  /profile move <ref> --to user|project              层级移动（跨层；同层拒绝）",
		"  /profile delete <ref> [--force]                    删除（引用检查 + 文件清单；--force 清空 default）",
		"  /profile export [<ref>] [--out <file|dir>]         导出 zip（默认 ./<name>.zip）",
		"  /profile import <包路径|目录> [--to user|project] [--name <名字>] [--dry-run]",
		"                                                     导入 zip/目录（不覆盖、不自动激活）",
		"  /profile help                                      显示本用法",
		"说明: 切换在下一个 turn 边界生效；profile 只能收窄安全基线，不会放宽权限",
		"      save-as 固化的是与内置默认面的差分（prompt/权限模式不在 profile.yaml 字段内，报告中逐项明示）",
		"      复杂编辑仍在前端 Profiles 页",
	}, "\n")
}

// tryExecuteStructuredProfileCommand 是 `/profile` 的结构化入口（与 /routing 同构）：
// 无条件接管，返回统一渲染器的 CommandResult。
func tryExecuteStructuredProfileCommand(session *ChatSession, command string) (CommandResult, bool) {
	if session == nil {
		return commandErrorResult(fmt.Errorf("当前没有活动会话")), true
	}
	args := splitChatProfileArgs(extractCommandArgument(command))
	flags, positional := chatProfileParseFlags(args)

	sub := "status"
	if len(positional) > 0 {
		sub = strings.ToLower(positional[0])
		positional = positional[1:]
	}

	switch sub {
	case "", "status":
		return commandTextResult(chatProfileStatusText(session)), true
	case "list":
		text, err := chatProfileListText(session)
		if err != nil {
			return commandErrorResult(err), true
		}
		return commandTextResult(text), true
	case "show":
		if len(positional) == 0 {
			return commandErrorResult(fmt.Errorf("用法: /profile show <name>")), true
		}
		text, err := chatProfilePreviewText(session, positional[0])
		if err != nil {
			return commandErrorResult(err), true
		}
		return commandTextResult(text), true
	case "diff":
		text, err := chatProfileDiffText(session, strings.Join(positional, " "))
		if err != nil {
			return commandErrorResult(err), true
		}
		return commandTextResult(text), true
	case "use":
		if len(positional) == 0 {
			return commandErrorResult(fmt.Errorf("用法: /profile use <name>；交互选择用 /profile pick")), true
		}
		return chatProfileUseResult(session, strings.Join(positional, " ")), true
	case "reload":
		return chatProfileReloadResult(session), true
	case "off":
		return chatProfileOffResult(session), true
	case "save":
		text, err := chatProfileSaveText(session, flags.Layer, flags.Confirm)
		if err != nil {
			return commandErrorResult(err), true
		}
		return commandTextResult(text), true
	case "pick":
		return chatProfilePickResult(session), true
	case "create":
		if len(positional) == 0 {
			return commandErrorResult(fmt.Errorf(
				"用法: /profile create <name> [--template coding|review|minimal|docs] [--to user|project] [--force]")), true
		}
		text, err := chatProfileCreateLifecycleText(session, positional[0], flags.Template, flags.Layer, flags.Force)
		if err != nil {
			return commandErrorResult(err), true
		}
		return commandTextResult(text), true
	case "duplicate":
		if len(positional) < 2 {
			return commandErrorResult(fmt.Errorf("用法: /profile duplicate <ref> <name> [--to user|project]")), true
		}
		text, err := chatProfileDuplicateLifecycleText(session, positional[0], positional[1], flags.Layer)
		if err != nil {
			return commandErrorResult(err), true
		}
		return commandTextResult(text), true
	case "rename":
		if len(positional) < 2 {
			return commandErrorResult(fmt.Errorf("用法: /profile rename <ref> <new-name>")), true
		}
		text, err := chatProfileRenameLifecycleText(session, positional[0], positional[1])
		if err != nil {
			return commandErrorResult(err), true
		}
		return commandTextResult(text), true
	case "move":
		if len(positional) == 0 {
			return commandErrorResult(fmt.Errorf("用法: /profile move <ref> --to user|project")), true
		}
		text, err := chatProfileMoveLifecycleText(session, positional[0], flags.Layer)
		if err != nil {
			return commandErrorResult(err), true
		}
		return commandTextResult(text), true
	case "delete":
		if len(positional) == 0 {
			return commandErrorResult(fmt.Errorf("用法: /profile delete <ref> [--force]")), true
		}
		text, err := chatProfileDeleteLifecycleText(session, positional[0], flags.Force)
		if err != nil {
			return commandErrorResult(err), true
		}
		return commandTextResult(text), true
	case "export":
		ref := ""
		if len(positional) > 0 {
			ref = positional[0]
		}
		text, err := chatProfileExportLifecycleText(session, ref, flags.Out)
		if err != nil {
			return commandErrorResult(err), true
		}
		return commandTextResult(text), true
	case "import":
		if len(positional) == 0 {
			return commandErrorResult(fmt.Errorf(
				"用法: /profile import <包路径|目录> [--to user|project] [--name <名字>] [--dry-run]")), true
		}
		text, err := chatProfileImportLifecycleText(session, positional[0], flags.Name, flags.Layer, flags.DryRun)
		if err != nil {
			return commandErrorResult(err), true
		}
		return commandTextResult(text), true
	case "edit":
		ref := ""
		if len(positional) > 0 {
			ref = positional[0]
		}
		text, err := chatProfileEditLifecycleText(session, ref, flags.Open)
		if err != nil {
			return commandErrorResult(err), true
		}
		return commandTextResult(text), true
	case "help", "--help", "-h":
		return commandTextResult(chatProfileUsageText()), true
	case "save-as":
		if len(positional) == 0 {
			return commandErrorResult(fmt.Errorf("用法: /profile save-as <name> [--to user|project]")), true
		}
		text, err := chatProfileSaveAsLifecycleText(session, positional[0], flags.Layer)
		if err != nil {
			return commandErrorResult(err), true
		}
		return commandTextResult(text), true
	default:
		return commandErrorResult(fmt.Errorf("未知子命令 /profile %s\n\n%s", sub, chatProfileUsageText())), true
	}
}

// chatProfileCommandText 提取 `/profile` 的纯文本结果（legacy/JSON 会话入口共用）。
func chatProfileCommandText(session *ChatSession, command string) (string, bool) {
	result, handled := tryExecuteStructuredProfileCommand(session, command)
	if !handled {
		return "", false
	}
	lines := make([]string, 0, len(result.Blocks))
	for _, block := range result.Blocks {
		if text := strings.TrimSpace(block.Document.PlainText()); text != "" {
			lines = append(lines, text)
		}
	}
	return strings.Join(lines, "\n"), true
}

// handleChatProfileCommand 是 legacy / JSON 输出会话的 `/profile` 入口：
// 复用结构化实现，把纯文本结果落到普通命令输出。
func handleChatProfileCommand(session *ChatSession, command string) {
	text, handled := chatProfileCommandText(session, command)
	if !handled || strings.TrimSpace(text) == "" {
		return
	}
	printChatCommandOutput(session, text)
}

// splitChatProfileArgs 按空白切分参数，保留引号内的整体（profile 名允许含空格）。
func splitChatProfileArgs(args string) []string {
	fields := strings.Fields(args)
	result := make([]string, 0, len(fields))
	for _, field := range fields {
		trimmed := strings.TrimSpace(strings.Trim(field, `"`))
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

// chatProfileCommandFlags 是 `/profile` 的旗标解析结果（Batch 13 G4：create/duplicate/
// rename/move/delete/export/edit 的选项并入同一解析器，避免每子命令各写一套）。
//
// 说明: 导出路径用 `--out` 而不是 `--output`——CLI 侧 `--output` 已是"输出格式
// text|json"，同名会让用户在两条命令间混淆（与 `aicli profile export --out` 对齐）。
type chatProfileCommandFlags struct {
	Layer    string
	Template string
	Out      string
	Name     string
	Confirm  bool
	Force    bool
	Open     bool
	DryRun   bool
}

// chatProfileParseFlags 解析 `/profile` 的旗标，返回旗标集合与剩余位置参数。
// 与 `/routing` 同一守卫语义：层只接受 session|workspace|config；未知层由调用方
// 报错（不静默回退默认层）。
func chatProfileParseFlags(args []string) (chatProfileCommandFlags, []string) {
	flags := chatProfileCommandFlags{}
	positional := make([]string, 0, len(args))
	for index := 0; index < len(args); index++ {
		arg := args[index]
		lower := strings.ToLower(arg)
		switch lower {
		case "--to":
			if index+1 < len(args) {
				flags.Layer = strings.ToLower(strings.TrimSpace(args[index+1]))
				index++
			}
		case "--template":
			if index+1 < len(args) {
				flags.Template = strings.TrimSpace(args[index+1])
				index++
			}
		case "--out":
			if index+1 < len(args) {
				flags.Out = strings.TrimSpace(args[index+1])
				index++
			}
		case "--name":
			if index+1 < len(args) {
				flags.Name = strings.TrimSpace(args[index+1])
				index++
			}
		case "--dry-run":
			flags.DryRun = true
		case "--yes":
			flags.Confirm = true
		case "--force", "-f":
			flags.Force = true
		case "--open":
			flags.Open = true
		default:
			switch {
			case strings.HasPrefix(lower, "--to="):
				flags.Layer = strings.ToLower(strings.TrimSpace(arg[len("--to="):]))
			case strings.HasPrefix(lower, "--template="):
				flags.Template = strings.TrimSpace(arg[len("--template="):])
			case strings.HasPrefix(lower, "--out="):
				flags.Out = strings.TrimSpace(arg[len("--out="):])
			case strings.HasPrefix(lower, "--name="):
				flags.Name = strings.TrimSpace(arg[len("--name="):])
			default:
				positional = append(positional, arg)
			}
		}
	}
	return flags, positional
}

// chatProfileStatusText 渲染当前会话的 profile 绑定与生效摘要（只读）。
func chatProfileStatusText(session *ChatSession) string {
	if session == nil {
		return "错误: 当前没有活动会话"
	}
	ref := strings.TrimSpace(session.ProfileReference)
	if ref == "" {
		return strings.Join([]string{
			"profile: (无) — 当前会话运行在无 profile 基线",
			"提示: /profile list 查看可用 profile，/profile use <name> 切换",
		}, "\n")
	}
	lines := []string{fmt.Sprintf("profile: %s", firstNonEmptyChatValue(session.ProfileName, ref))}
	lines = append(lines, fmt.Sprintf("  引用: %s", ref))
	if source := strings.TrimSpace(session.AgentSource); source != "" {
		lines = append(lines, fmt.Sprintf("  来源: %s", source))
	}
	if root := strings.TrimSpace(session.ProfileRoot); root != "" {
		lines = append(lines, fmt.Sprintf("  根目录: %s", root))
	}
	if agent := strings.TrimSpace(session.ProfileAgent); agent != "" {
		lines = append(lines, fmt.Sprintf("  agent: %s", agent))
	}
	if path := strings.TrimSpace(session.AgentSourcePath); path != "" {
		lines = append(lines, fmt.Sprintf("  定义文件: %s", path))
	}
	if mode := strings.TrimSpace(session.ProfilePromptMode); mode != "" {
		lines = append(lines, fmt.Sprintf("  prompt 模式: %s", mode))
	}
	if prompt := strings.TrimSpace(session.SystemPromptText); prompt != "" {
		lines = append(lines, fmt.Sprintf("  prompt 字节数: %d", len(prompt)))
	}
	lines = append(lines, chatProfileToolPolicySummary(session)...)
	lines = append(lines, chatProfileSelectionSummary(session)...)
	if session.Config != nil && session.Config.Profiles != nil {
		if configured := strings.TrimSpace(session.Config.Profiles.DefaultProfile); configured != "" {
			lines = append(lines, fmt.Sprintf("  配置默认 profile: %s", configured))
		}
	}
	return strings.Join(lines, "\n")
}

// chatProfileToolPolicySummary 汇总工具面（只读，不触发任何解析）。
func chatProfileToolPolicySummary(session *ChatSession) []string {
	if session == nil || session.ToolPolicy == nil {
		return []string{"  工具面: (无策略限制)"}
	}
	allowed := session.ToolPolicy.AllowedToolNames()
	line := fmt.Sprintf("  工具面: %d 个允许", len(allowed))
	if session.ToolPolicy.ReadOnly {
		line += "（read_only）"
	}
	lines := []string{line}
	if len(allowed) > 0 && len(allowed) <= 12 {
		lines = append(lines, "    允许: "+strings.Join(allowed, ", "))
	}
	if denied := chatProfileDeniedToolNames(session.ToolPolicy); len(denied) > 0 && len(denied) <= 12 {
		lines = append(lines, "    拒绝: "+strings.Join(denied, ", "))
	}
	return lines
}

// chatProfileDeniedToolNames 汇总策略中的显式拒绝工具（稳定排序，只读）。
func chatProfileDeniedToolNames(policy *runtimepolicy.ToolExecutionPolicy) []string {
	if policy == nil {
		return nil
	}
	names := make([]string, 0, len(policy.DeniedTools))
	for name, denied := range policy.DeniedTools {
		if denied && strings.TrimSpace(name) != "" {
			names = append(names, name)
		}
	}
	return sortProfileNames(names)
}

// chatProfileSelectionSummary 汇总 skills/mcp 裁剪声明（只读）。
func chatProfileSelectionSummary(session *ChatSession) []string {
	if session == nil {
		return nil
	}
	var lines []string
	if allow := normalizeStringSet(session.ProfileSkillSelection.Allowlist); len(allow) > 0 {
		lines = append(lines, "  skills 允许: "+strings.Join(allow, ", "))
	}
	if deny := normalizeStringSet(session.ProfileSkillSelection.Denylist); len(deny) > 0 {
		lines = append(lines, "  skills 排除: "+strings.Join(deny, ", "))
	}
	if use := normalizeStringSet(session.ProfileMCPSelection.UseServers); len(use) > 0 {
		lines = append(lines, "  mcp 启用: "+strings.Join(use, ", "))
	}
	if exclude := normalizeStringSet(session.ProfileMCPSelection.ExcludeServers); len(exclude) > 0 {
		lines = append(lines, "  mcp 排除: "+strings.Join(exclude, ", "))
	}
	return lines
}

// chatProfileListText 列出可用 profile（复用 Batch 2 的三来源发现逻辑）。
func chatProfileListText(session *ChatSession) (string, error) {
	if session == nil {
		return "", fmt.Errorf("当前没有活动会话")
	}
	result, err := runProfileListCommand(session.Config, "")
	if err != nil {
		return "", err
	}
	lines := make([]string, 0, len(result.Profiles)+3)
	current := strings.TrimSpace(session.ProfileReference)
	if current == "" {
		lines = append(lines, "当前: (无 profile)")
	} else {
		lines = append(lines, fmt.Sprintf("当前: %s", firstNonEmptyChatValue(session.ProfileName, current)))
	}
	if result.DefaultProfile != "" {
		line := "配置默认: " + result.DefaultProfile
		if len(result.EnvVars) > 0 {
			line += fmt.Sprintf("（env: %s）", strings.Join(result.EnvVars, ", "))
		}
		lines = append(lines, line)
	}
	if len(result.Profiles) == 0 {
		lines = append(lines, "未发现任何 profile；可用 `aicli profile create <name> --template coding` 生成")
		return strings.Join(lines, "\n"), nil
	}
	for _, entry := range result.Profiles {
		marks := make([]string, 0, 3)
		marks = append(marks, entry.Source)
		if entry.IsDefault {
			marks = append(marks, "default")
		}
		if strings.EqualFold(entry.Name, current) {
			marks = append(marks, "当前")
		}
		if !entry.Exists {
			marks = append(marks, "缺失")
		}
		line := fmt.Sprintf("  %s [%s]", entry.Name, strings.Join(marks, ","))
		if entry.Description != "" {
			line += " — " + entry.Description
		}
		if entry.Error != "" {
			line += " ! " + entry.Error
		}
		lines = append(lines, line)
	}
	lines = append(lines, "提示: /profile show <name> 预览；/profile use <name> 切换")
	return strings.Join(lines, "\n"), nil
}

// resolveChatProfilePreviewState 只读解析目标 profile（不产生任何失效）。
func resolveChatProfilePreviewState(session *ChatSession, ref string) (*chatProfileState, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, fmt.Errorf("profile 名称不能为空")
	}
	if session == nil {
		return nil, fmt.Errorf("当前没有活动会话")
	}
	if session.Config == nil {
		return nil, fmt.Errorf("无法解析 profile %q：缺少配置上下文", ref)
	}
	state, err := resolveChatProfileState(session.Config, &chatCommandOptions{ProfileFlag: ref})
	if err != nil {
		// §17.2：先区分"名字不存在"与"存在但解析不了"，避免把解析错误说成
		// 未知 profile（只在错误路径做一次只读发现，不引入新的解析机制）。
		if names, listErr := chatProfileAvailableNames(session); listErr == nil && !chatProfileNameKnown(names, ref) {
			return nil, fmt.Errorf("未知 profile: %s（用 /profile list 查看可用项）", ref)
		}
		return nil, err
	}
	if state == nil || !state.Active() {
		return nil, fmt.Errorf("未知 profile: %s（用 /profile list 查看可用项）", ref)
	}
	return state, nil
}

// chatProfilePreviewText 渲染 `/profile show <name>` 的只读预览（绝不产生失效）。
func chatProfilePreviewText(session *ChatSession, ref string) (string, error) {
	state, err := resolveChatProfilePreviewState(session, ref)
	if err != nil {
		return "", err
	}
	lines := []string{fmt.Sprintf("profile: %s（只读预览，未切换）", state.Resolved.ProfileName)}
	lines = append(lines, fmt.Sprintf("  引用: %s", state.Reference))
	if source := strings.TrimSpace(state.AgentSource); source != "" {
		lines = append(lines, fmt.Sprintf("  来源: %s", source))
	}
	if root := strings.TrimSpace(state.Resolved.ProfileRoot); root != "" {
		lines = append(lines, fmt.Sprintf("  根目录: %s", root))
	}
	if agent := strings.TrimSpace(state.Resolved.AgentID); agent != "" {
		lines = append(lines, fmt.Sprintf("  agent: %s", agent))
	}
	if path := strings.TrimSpace(state.AgentSourcePath); path != "" {
		lines = append(lines, fmt.Sprintf("  定义文件: %s", path))
	}
	if mode := strings.TrimSpace(state.Resolved.PromptMode); mode != "" {
		lines = append(lines, fmt.Sprintf("  prompt 模式: %s", mode))
	}
	if prompt := strings.TrimSpace(state.PromptText); prompt != "" {
		lines = append(lines, fmt.Sprintf("  prompt 字节数: %d", len(prompt)))
	}
	if state.ToolPolicy != nil {
		line := fmt.Sprintf("  工具面: %d 个允许", len(state.ToolPolicy.AllowedToolNames()))
		if state.ToolPolicy.ReadOnly {
			line += "（read_only）"
		}
		lines = append(lines, line)
	} else {
		lines = append(lines, "  工具面: (无策略限制)")
	}
	if allow := normalizeStringSet(state.Resolved.Skills.Allowlist); len(allow) > 0 {
		lines = append(lines, "  skills 允许: "+strings.Join(allow, ", "))
	}
	if deny := normalizeStringSet(state.Resolved.Skills.Denylist); len(deny) > 0 {
		lines = append(lines, "  skills 排除: "+strings.Join(deny, ", "))
	}
	if use := normalizeStringSet(state.Resolved.MCPSelection.UseServers); len(use) > 0 {
		lines = append(lines, "  mcp 启用: "+strings.Join(use, ", "))
	}
	if exclude := normalizeStringSet(state.Resolved.MCPSelection.ExcludeServers); len(exclude) > 0 {
		lines = append(lines, "  mcp 排除: "+strings.Join(exclude, ", "))
	}
	if declaredProvider := strings.TrimSpace(firstNonEmptyChatValue(state.Resolved.Provider, state.Resolved.DefaultProvider)); declaredProvider != "" {
		lines = append(lines, fmt.Sprintf("  声明 provider: %s（需显式 /provider 应用）", declaredProvider))
	}
	if declaredModel := strings.TrimSpace(state.Resolved.Model); declaredModel != "" {
		lines = append(lines, fmt.Sprintf("  声明 model: %s（需显式 /model 应用）", declaredModel))
	}
	for _, warning := range state.SandboxWarnings {
		if text := strings.TrimSpace(warning); text != "" {
			lines = append(lines, "  ! "+text)
		}
	}
	lines = append(lines, "提示: /profile diff "+state.Reference+" 查看与当前会话的差异")
	return strings.Join(lines, "\n"), nil
}

// chatProfileDiffText 渲染 `/profile diff [<name>]`：与当前会话生效面的只读对比。
// 缺省目标为配置默认 profile（不猜"第一个可用项"）。
func chatProfileDiffText(session *ChatSession, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		if session != nil && session.Config != nil && session.Config.Profiles != nil {
			ref = strings.TrimSpace(session.Config.Profiles.DefaultProfile)
		}
		if ref == "" {
			return "", fmt.Errorf("用法: /profile diff <name>（当前无配置默认 profile 可对比）")
		}
	}
	state, err := resolveChatProfilePreviewState(session, ref)
	if err != nil {
		return "", err
	}
	current := firstNonEmptyChatValue(session.ProfileName, strings.TrimSpace(session.ProfileReference))
	if strings.TrimSpace(current) == "" {
		current = "(无 profile)"
	}
	changed := buildProfileSwitchChanged(snapshotChatProfileSurface(session), state)
	lines := []string{fmt.Sprintf("差异: %s → %s（只读预览）", current, state.Resolved.ProfileName)}
	lines = append(lines, formatProfileDiffSection("tools", changed.ToolsAdded, changed.ToolsRemoved)...)
	lines = append(lines, formatProfileDiffSection("skills", changed.SkillsAdded, changed.SkillsRemoved)...)
	lines = append(lines, formatProfileDiffSection("mcp", changed.MCPAdded, changed.MCPRemoved)...)
	if changed.PromptChanged {
		lines = append(lines, "  prompt: 将变化（下一次 compose 重建 head）")
	} else {
		lines = append(lines, "  prompt: 无变化")
	}
	lines = append(lines, "提示: 应用该差异用 /profile use "+state.Reference)
	return strings.Join(lines, "\n"), nil
}

// formatProfileDiffSection 渲染单类差异（added/removed）。
func formatProfileDiffSection(name string, added, removed []string) []string {
	if len(added) == 0 && len(removed) == 0 {
		return []string{fmt.Sprintf("  %s: 无变化", name)}
	}
	lines := make([]string, 0, 3)
	if len(added) > 0 {
		lines = append(lines, fmt.Sprintf("  %s + %s", name, strings.Join(added, ", ")))
	}
	if len(removed) > 0 {
		lines = append(lines, fmt.Sprintf("  %s - %s", name, strings.Join(removed, ", ")))
	}
	return lines
}

// chatProfileUseResult 执行 `/profile use <name>` 并渲染 Switch Report。
func chatProfileUseResult(session *ChatSession, ref string) CommandResult {
	report, err := applyRuntimeProfileSwitch(session, ref)
	if err != nil {
		return commandErrorResult(err)
	}
	return commandTextResult(chatProfileSwitchReportText(report))
}

// chatProfileReloadResult 重新解析当前 profile_ref（§17.2）：失败时保留旧状态
// （执行核心在解析阶段就返回错误，会话字段未被触碰）。
func chatProfileReloadResult(session *ChatSession) CommandResult {
	if session == nil {
		return commandErrorResult(fmt.Errorf("当前没有活动会话"))
	}
	ref := strings.TrimSpace(session.ProfileReference)
	if ref == "" {
		return commandErrorResult(fmt.Errorf("当前会话未绑定 profile；用 /profile use <name> 切换"))
	}
	report, err := applyRuntimeProfileSwitch(session, ref)
	if err != nil {
		return commandErrorResult(fmt.Errorf("重新解析 profile %q 失败（旧状态保持不变）: %w", ref, err))
	}
	text := chatProfileSwitchReportText(report)
	text = "已重新解析 profile: " + ref + "\n" + text
	return commandTextResult(text)
}

// chatProfileSwitchReportText 渲染 Switch Report（D23 投影的文本形态，TUI 与
// 前端共用同一份字段语义）。
func chatProfileSwitchReportText(report *ProfileSwitchReport) string {
	if report == nil {
		return "错误: 切换未产生报告"
	}
	from := strings.TrimSpace(report.From)
	if from == "" {
		from = "(无 profile)"
	}
	lines := []string{fmt.Sprintf("已切换 profile: %s → %s", from, report.To)}
	lines = append(lines, formatProfileDiffSection("tools", report.Changed.ToolsAdded, report.Changed.ToolsRemoved)...)
	lines = append(lines, formatProfileDiffSection("skills", report.Changed.SkillsAdded, report.Changed.SkillsRemoved)...)
	lines = append(lines, formatProfileDiffSection("mcp", report.Changed.MCPAdded, report.Changed.MCPRemoved)...)
	if report.Changed.PromptChanged {
		lines = append(lines, "  prompt: 已重建（下一轮 compose 生效）")
	} else {
		lines = append(lines, "  prompt: 无变化")
	}
	lines = append(lines, fmt.Sprintf("  生效时点: %s", report.EffectiveAt))
	if report.CacheNotice != "" {
		lines = append(lines, "  "+report.CacheNotice)
	}
	if report.InFlightTurn {
		lines = append(lines, "  提示: 检测到在途 turn；其冻结前缀保持不变，新 profile 从下一轮起生效")
	}
	lines = append(lines, fmt.Sprintf("  失效: 锚点=%t 工具面=%s(%t) token计数=%t",
		report.AnchorCleared, firstNonEmptyChatValue(report.ToolSurfaceScope, profileSwitchSurfaceScopeNone),
		report.ToolSurfaceInvalidated, report.ContextTokenCountReset))
	for _, warning := range report.Warnings {
		if text := strings.TrimSpace(warning); text != "" {
			lines = append(lines, "  ! "+text)
		}
	}
	return strings.Join(lines, "\n")
}

// sortProfileNames 是 pick/list 的稳定排序口径（大小写不敏感）。
func sortProfileNames(names []string) []string {
	result := append([]string(nil), names...)
	sort.SliceStable(result, func(i, j int) bool {
		return strings.ToLower(result[i]) < strings.ToLower(result[j])
	})
	return result
}

// chatProfileOffResult 执行 `/profile off`：回到无 profile 基线。
func chatProfileOffResult(session *ChatSession) CommandResult {
	if session == nil {
		return commandErrorResult(fmt.Errorf("当前没有活动会话"))
	}
	if strings.TrimSpace(session.ProfileReference) == "" && strings.TrimSpace(session.ProfileName) == "" {
		return commandTextResult("当前会话未绑定 profile，已处于无 profile 基线")
	}
	report, err := applyRuntimeProfileDetach(session)
	if err != nil {
		return commandErrorResult(err)
	}
	text := chatProfileSwitchReportText(report)
	return commandTextResult("已回到无 profile 基线（等价启动时不带 --profile）\n" + text)
}

// chatProfileSaveText 执行 `/profile save [--to session|workspace|config] [--yes]`
// （D22；层语义与 `/routing save` 对齐，见 V17 结论）。
//
//   - session：会话层绑定随 sessionmeta 持久化（⑩，resume 沿用），无需额外保存；
//   - workspace：把 `profiles.default_profile` 写入工作区偏好文件；
//   - config：写入可写配置层（影响所有会话，需 --yes 二次确认）。
func chatProfileSaveText(session *ChatSession, layer string, confirm bool) (string, error) {
	if session == nil {
		return "", fmt.Errorf("当前没有活动会话")
	}
	// M16/INV-A3：save 会真实落盘，子会话必须挡在写入之前（写入不生效就不能说成功）。
	if chatRoutingSessionIsChildAgent(session) {
		return "", fmt.Errorf("%s", chatRoutingChildSessionReadOnlyNote)
	}
	ref := strings.TrimSpace(session.ProfileReference)
	if ref == "" {
		return "", fmt.Errorf("当前会话未绑定 profile，无可保存内容；先 /profile use <name>")
	}
	if strings.TrimSpace(layer) == "" {
		layer = chatRoutingLayerSession
	}
	switch layer {
	case chatRoutingLayerSession:
		return fmt.Sprintf("会话层绑定随会话持久化（sessionmeta ⑩，resume 沿用），无需额外保存\n当前: %s",
			firstNonEmptyChatValue(session.ProfileName, ref)), nil
	case chatRoutingLayerWorkspace:
		workspacePath := strings.TrimSpace(chatSessionRoutingWorkspacePath(session))
		if workspacePath == "" {
			return "", fmt.Errorf("当前会话未绑定工作区，无法写入 workspace 层")
		}
		target := strings.TrimSpace(agentconfig.WorkspacePrefsPathForPath(workspacePath))
		if target == "" {
			return "", fmt.Errorf("无法解析工作区偏好文件路径（%s）", workspacePath)
		}
		if err := agentconfig.UpdateProfilesConfig(target, agentconfig.ProfilesConfigUpdate{DefaultProfile: ref}); err != nil {
			return "", err
		}
		note := ""
		if !chatProfileFileDeclaresDefaultProfile(target, ref) {
			note = "\n说明: 配置分层把 profiles 段路由到了归属层，本次写入未落在上述路径；用 `aicli config path` 确认实际文件"
		}
		return fmt.Sprintf("已保存默认 profile 到工作区层（目标: %s，本工作区新会话生效，需 --yes 之外的显式调用方可覆盖）%s", target, note), nil
	case chatRoutingLayerConfig:
		configPath, err := ensureWritableAICLIConfigPath(session.Config, "")
		if err != nil {
			return "", err
		}
		if !confirm {
			return "", fmt.Errorf("config 层保存影响所有会话，需二次确认：追加 --yes（目标: %s）", configPath)
		}
		if err := agentconfig.UpdateProfilesConfig(configPath, agentconfig.ProfilesConfigUpdate{DefaultProfile: ref}); err != nil {
			return "", err
		}
		note := ""
		if !chatProfileFileDeclaresDefaultProfile(configPath, ref) {
			note = "\n说明: 配置分层把 profiles 段路由到了归属层，本次写入未落在上述路径；用 `aicli config path` 确认实际文件"
		}
		return fmt.Sprintf("已保存默认 profile 到配置层（目标: %s，影响所有会话，下次启动生效）%s\n说明: 会话层绑定仍优先，当前会话无需重新 /profile use",
			configPath, note), nil
	default:
		return "", fmt.Errorf("未知保存目标 %s（可用 session|workspace|config）", layer)
	}
}

// chatProfileFileDeclaresDefaultProfile 只读校验写入结果（不解析 YAML，避免为一次
// 回显引入新的解析路径）：文件里出现 default_profile 与目标名即视为已落盘。
func chatProfileFileDeclaresDefaultProfile(path, ref string) bool {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	text := string(raw)
	index := strings.Index(text, "default_profile")
	if index < 0 {
		return false
	}
	tail := text[index:]
	if newline := strings.Index(tail, "\n"); newline >= 0 {
		tail = tail[:newline]
	}
	return strings.Contains(tail, ref)
}

// chatProfileAvailableNames 返回可用 profile 名（只读；发现失败显式返回错误）。
func chatProfileAvailableNames(session *ChatSession) ([]string, error) {
	if session == nil {
		return nil, fmt.Errorf("当前没有活动会话")
	}
	result, err := runProfileListCommand(session.Config, "")
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(result.Profiles))
	for _, entry := range result.Profiles {
		if name := strings.TrimSpace(entry.Name); name != "" {
			names = append(names, name)
		}
	}
	return sortProfileNames(names), nil
}

// chatProfileNameKnown 判定引用名是否在可用清单中（只读；与引用语义一致的大小写敏感比较）。
func chatProfileNameKnown(names []string, ref string) bool {
	ref = strings.TrimSpace(ref)
	for _, name := range names {
		if strings.TrimSpace(name) == ref {
			return true
		}
	}
	return false
}

// chatProfilePickerAvailable 判定能否打开交互选择器：与其它 lease-bound picker
// 同一守卫语义（非交互会话、JSON 输出、无终端面时退化为只读列表 + 提示）。
func chatProfilePickerAvailable(session *ChatSession) bool {
	if session == nil || session.NoInteractive || session.JSONOutput {
		return false
	}
	return session.TerminalSession != nil
}

// chatProfilePickResult 执行 `/profile pick`：交互选择 profile（选中即热切换）。
// 无选择器面时退化为只读列表，绝不静默切换（§17.2 不猜）。
func chatProfilePickResult(session *ChatSession) CommandResult {
	if session == nil {
		return commandErrorResult(fmt.Errorf("当前没有活动会话"))
	}
	names, err := chatProfileAvailableNames(session)
	if err != nil {
		return commandErrorResult(err)
	}
	if len(names) == 0 {
		return commandTextResult("未发现任何 profile；可用 `aicli profile create <name> --template coding` 生成")
	}
	if !chatProfilePickerAvailable(session) || !useRuntimeSelectionPopup(session) {
		lines := []string{"无法打开交互选择器（非交互会话 / JSON 输出 / 无终端面）；可用 profile:"}
		for _, name := range names {
			lines = append(lines, "  "+name)
		}
		lines = append(lines, "提示: 用 /profile use <name> 直接切换")
		return commandTextResult(strings.Join(lines, "\n"))
	}
	selected, ok, err := promptChatProfileSelection(session, names)
	if err != nil {
		return commandErrorResult(err)
	}
	if !ok || strings.TrimSpace(selected) == "" {
		return commandTextResult("已取消 profile 选择")
	}
	return chatProfileUseResult(session, selected)
}

// promptChatProfileSelection 复用 runtimeModelPickerState + 统一选择弹层框架
// （与 /model 选择器同一条迁移路径）：过滤/分页/取消语义一致，且**不新增任何
// direct terminal writer**——TestChatInteractiveDirectWriterInventory 是 P0 门禁，
// 新交互功能必须走统一弹层而不是 fmt.Print（见该测试注释）。
// 调用方（chatProfilePickResult）已保证弹层面可用。返回 (选中项, 是否确认, 错误)。
func promptChatProfileSelection(session *ChatSession, names []string) (string, bool, error) {
	if session == nil {
		return "", false, fmt.Errorf("当前没有活动会话")
	}
	if !useRuntimeSelectionPopup(session) {
		return "", false, fmt.Errorf("当前界面不支持交互选择；用 /profile use <name> 直接切换")
	}

	current := firstNonEmptyChatValue(session.ProfileName, strings.TrimSpace(session.ProfileReference))
	preferred, _ := matchCaseInsensitive(names, current)
	state := newRuntimeModelPickerState(names, preferred, runtimeModelSelectionPageSize)
	notice, restoreInput := prepareRuntimeSelectionInput(session, "profile 选择")
	defer restoreInput()
	prompt := chatProfilePickerPopupPrompt()
	pageOptions, _, _, _ := state.pageWindow()
	selectedIndex := initialRuntimeSelectionIndex(pageOptions, preferred, "")
	render := func(selected int, warning string) []string {
		return renderChatProfilePickerPopupLines(state, current, preferred, notice, warning, selected)
	}
	handle := beginRuntimeSelectionPopup(session, render(selectedIndex, ""), prompt)
	defer clearRuntimeSelectionPopupHandle(session, handle)
	controller := newRuntimeSelectionController(session, handle, prompt, pageOptions, selectedIndex, render)

	for {
		text, err := chatInteractiveReadSelectionLine(session, prompt, controller)
		if err != nil {
			if errors.Is(err, errChatInteractivePromptCancelled) {
				return "", false, nil
			}
			return "", false, err
		}
		blankSelection, _ := controller.SelectedOption()
		nextState, result := applyRuntimeModelPickerInput(state, text, blankSelection)
		state = nextState
		if result.Done {
			return result.Selected, true, nil
		}
		if result.Redraw {
			pageOptions, _, _, _ = state.pageWindow()
			selectedIndex = initialRuntimeSelectionIndex(pageOptions, preferred, "")
			render = func(selected int, warning string) []string {
				return renderChatProfilePickerPopupLines(state, current, preferred, notice, warning, selected)
			}
			controller = newRuntimeSelectionController(session, handle, prompt, pageOptions, selectedIndex, render)
		}
		controller.SetWarning(result.Message)
	}
}

// renderChatProfilePickerPopupLines 生成统一选择弹层内容（标题/当前值/候选/提示）。
func renderChatProfilePickerPopupLines(state runtimeModelPickerState, current, currentMatch, notice, warning string, selected int) []string {
	pageOptions, page, pageCount, filteredTotal := state.pageWindow()
	title := fmt.Sprintf("选择 profile（共 %d", len(state.Options))
	if strings.TrimSpace(state.Filter) != "" {
		title += fmt.Sprintf("，搜索 %q 匹配 %d", state.Filter, filteredTotal)
	}
	title += fmt.Sprintf("，第 %d/%d 页）", page+1, pageCount)
	if filteredTotal == 0 && strings.TrimSpace(warning) == "" {
		warning = "没有匹配的 profile"
	}
	hint := "提示: ↑↓ 选择，回车确认；关键词搜索；n/p 翻页；c 清除搜索；编号按当前页"
	return renderSelectionPopupLines(title, "profile", current, pageOptions, currentMatch, "", hint, notice, warning, selected)
}

// chatProfilePickerPopupPrompt 与 /model 弹层提示同构（回车确认高亮项）。
func chatProfilePickerPopupPrompt() string {
	return "选择 profile（关键词搜索，回车确认，q 取消）: "
}
