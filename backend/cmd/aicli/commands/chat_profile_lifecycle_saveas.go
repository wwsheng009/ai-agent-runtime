package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	profilesys "github.com/wwsheng009/ai-agent-runtime/internal/profile"
)

// chat_profile_lifecycle_saveas.go 承载"从会话固化"子命令（Batch 13 slice 5 /
// §23 G1 / D24 差分固化 + D35 落地口径）。
//
// 语义：固化的是**与内置默认面的差分**，不是全量快照——全量快照会随基线演进
// 腐化（基线新增工具不会进入快照 → 用户以为"没生效"）；差分则天然继承基线变化。
// 产物是**独立可用**的 profile（重开 `--profile <name>` 即复现会话生效面）。
//
// 只写 profile.yaml 能表达的字段（工具面 / skills / MCP）；会话里存在但
// profile.yaml 无字段的生效项（prompt、权限模式、skills 目录）在报告中逐项明示，
// 绝不静默丢弃（D35）。无差分时明确报错且不产 profile（A9）。
func chatProfileSaveAsLifecycleText(session *ChatSession, name, layer string) (string, error) {
	if err := chatProfileLifecycleWriteGuard(session); err != nil {
		return "", err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("用法: /profile save-as <name> [--to user|project]")
	}
	if err := validateProfileCreateName(name); err != nil {
		return "", err
	}
	surface, notes, err := buildChatProfileSaveAsSurface(session)
	if err != nil {
		return "", err
	}
	if surface.Empty() {
		// A9：无差分时报错，不生成空 profile。
		return "", fmt.Errorf(
			"当前无差异，无需固化：会话生效面与内置默认面一致（无工具/skills/MCP 收窄，read_only 未开启）；未生成任何文件")
	}
	layer = strings.TrimSpace(strings.ToLower(layer))
	if layer == "" {
		layer = chatProfileLayerUser
	}
	base, err := chatProfileLayerBase(layer)
	if err != nil {
		return "", err
	}
	root := filepath.Join(base, name)
	if dirExists(root) {
		// save-as 不提供 --force：差分合并进既有 profile 会造成"半覆盖"
		// （profile.yaml 被换掉、agents/ 与 prompts/ 仍是旧的）。
		return "", fmt.Errorf("目标已存在：%s（save-as 不覆盖既有 profile；请换名或先 /profile delete %s --force）", root, name)
	}
	agent := chatProfileSaveAsAgent(session)
	content, err := profilesys.RenderSaveAsProfile(name, chatProfileSaveAsDescription(session), agent, surface)
	if err != nil {
		return "", err
	}
	files := map[string][]byte{"profile.yaml": content}
	relPaths := sortedProfileTemplatePaths(files)
	if err := writeProfileTemplateFiles(root, files, relPaths); err != nil {
		_ = os.RemoveAll(root)
		return "", err
	}

	lines := []string{
		fmt.Sprintf("已固化 profile: %s（layer: %s，来源: 当前会话生效面差分）", name, layer),
		"  root:  " + root,
		fmt.Sprintf("  文件（%d）:", len(relPaths)),
	}
	lines = append(lines, chatProfileFormatPathList(relPaths, "    ")...)
	lines = append(lines, "  差分声明: "+strings.Join(chatProfileSaveAsSurfaceSummary(surface), "；"))
	lines = append(lines, fmt.Sprintf("  agent: %s（内联空声明：解析器要求 profile 可解析出 agent；prompt/tools 不随 agent 固化）", agent))
	lines = append(lines, chatProfileSaveAsBaselineLines(session)...)
	for _, note := range notes {
		lines = append(lines, "  未包含: "+note)
	}
	lines = append(lines, chatProfilePostWriteNote(session, root)...)
	lines = append(lines,
		"下一步: /profile use "+name+"（立即切换）；继续编辑用 /profile edit "+name)
	return strings.Join(lines, "\n"), nil
}

// chatProfileSaveAsAgent 选择固化产物的 agent id：沿用会话当前生效的 agent；会话
// 未绑定 profile 时用内置默认 agent（与模板同一常量，不引入第二套默认值）。
// 产物只做**空声明**（不复制 agent.yaml / prompts / tools），保持"仅含差分"。
func chatProfileSaveAsAgent(session *ChatSession) string {
	if session == nil {
		return profilesys.TemplateDefaultAgent
	}
	if agent := strings.TrimSpace(session.ProfileAgent); agent != "" {
		return agent
	}
	return profilesys.TemplateDefaultAgent
}

// buildChatProfileSaveAsSurface 把会话的**实际生效面**折算为可声明差分。
// 取值全部来自会话自身权威字段（与 chat_setup 的启动投影同源），不重新解析 profile。
// 形态保持（allowlist 形态仍写 allowlist、deny 形态仍写 denylist）是可复现的前提：
// 重开后按同一形态解析，得到的允许集与固化时一致。
func buildChatProfileSaveAsSurface(session *ChatSession) (profilesys.SaveAsSurface, []string, error) {
	if session == nil {
		return profilesys.SaveAsSurface{}, nil, errChatProfileNoSession
	}
	surface := profilesys.SaveAsSurface{
		SkillAllowlist:    normalizeStringSet(session.ProfileSkillSelection.Allowlist),
		SkillDenylist:     normalizeStringSet(session.ProfileSkillSelection.Denylist),
		MCPUseServers:     normalizeStringSet(session.ProfileMCPSelection.UseServers),
		MCPExcludeServers: normalizeStringSet(session.ProfileMCPSelection.ExcludeServers),
	}
	if policy := session.ToolPolicy; policy != nil {
		surface.ReadOnly = policy.ReadOnly
		switch {
		case policy.AllowlistEnabled:
			names := policy.AllowedToolNames()
			if len(names) == 0 {
				// 空 allowlist 会被 ValidateProfileSpec 判为非法（"不得变成隐式全禁"），
				// 与其写出一个解析不了的 profile，不如显式报错。
				return profilesys.SaveAsSurface{}, nil, fmt.Errorf(
					"当前工具面是空 allowlist（全部禁用）：profile.yaml 无法表达该形态（空 allowlist 会被校验拒绝）；未生成 profile")
			}
			surface.ToolAllowlist = names
		default:
			surface.ToolDenylist = chatProfileSaveAsToolNames(policy.DeniedTools)
		}
	}
	return surface, chatProfileSaveAsOmittedNotes(session, surface), nil
}

// chatProfileSaveAsOmittedNotes 逐项明示"会话生效面里有、但 profile.yaml 无字段"的
// 内容（D35）：prompt（D24 明确不固化）、非默认权限模式、skills 目录。
func chatProfileSaveAsOmittedNotes(session *ChatSession, surface profilesys.SaveAsSurface) []string {
	notes := []string{
		"prompt（会话 prompt 可能来自临时上下文，D24 明确不固化）；需要时在 profile 里另加 prompt 文件",
	}
	if mode := strings.TrimSpace(string(session.PermissionMode)); mode != "" && mode != string(runtimepolicy.ModeDefault) {
		notes = append(notes, fmt.Sprintf(
			"权限模式 %q（profile.yaml 无 permission_mode 字段，见 D35）；重开时用 --permission-mode 或会话控件指定", mode))
	}
	if len(surface.SkillAllowlist) > 0 || len(surface.SkillDenylist) > 0 {
		if dirs := normalizeStringSet(session.ResolvedSkillDirs); len(dirs) > 0 {
			notes = append(notes, "skills 目录（profile.yaml 无 dirs 字段；重开时按配置解析技能目录）")
		}
	}
	return notes
}

// chatProfileSaveAsDescription 生成 profile.description：说明来源与基线，便于
// `profile list/show` 一眼看出这不是模板生成物。
func chatProfileSaveAsDescription(session *ChatSession) string {
	if ref := strings.TrimSpace(session.ProfileReference); ref != "" {
		return fmt.Sprintf("从会话固化（save-as；基线 profile: %s；仅含差分声明）", ref)
	}
	return "从会话固化（save-as；基线: 内置默认；仅含差分声明）"
}

// chatProfileSaveAsSurfaceSummary 渲染"写了哪些字段、各几项"（报告用）。
func chatProfileSaveAsSurfaceSummary(surface profilesys.SaveAsSurface) []string {
	parts := make([]string, 0, 6)
	add := func(path string, count int) {
		if count > 0 {
			parts = append(parts, fmt.Sprintf("%s %d 项", path, count))
		}
	}
	add("tools.allowlist", len(surface.ToolAllowlist))
	add("tools.denylist", len(surface.ToolDenylist))
	if surface.ReadOnly {
		parts = append(parts, "tools.read_only")
	}
	add("skills.allowlist", len(surface.SkillAllowlist))
	add("skills.denylist", len(surface.SkillDenylist))
	add("mcp.use_servers", len(surface.MCPUseServers))
	add("mcp.exclude_servers", len(surface.MCPExcludeServers))
	return parts
}

// chatProfileSaveAsBaselineLines 渲染"相对基线 profile 的差异"（复用 §17.2 的
// 只读差分核心 buildProfileSwitchChanged，不另写比较逻辑）。changed 的语义是
// "当前生效面 → 基线"：ToolsAdded = 基线有而当前没有（即当前收窄掉的）。
func chatProfileSaveAsBaselineLines(session *ChatSession) []string {
	ref := strings.TrimSpace(session.ProfileReference)
	if ref == "" {
		return []string{"  基线: 无（未绑定 profile；差分相对内置默认面）"}
	}
	lines := []string{"  基线: " + ref}
	state, err := resolveChatProfilePreviewState(session, ref)
	if err != nil {
		return append(lines, "  ! 基线 profile 解析失败（产物仍按当前生效面生成）: "+err.Error())
	}
	changed := buildProfileSwitchChanged(snapshotChatProfileSurface(session), state)
	if len(changed.ToolsAdded) == 0 && len(changed.ToolsRemoved) == 0 &&
		len(changed.SkillsAdded) == 0 && len(changed.SkillsRemoved) == 0 &&
		len(changed.MCPAdded) == 0 && len(changed.MCPRemoved) == 0 && !changed.PromptChanged {
		return append(lines, "  相对基线: 无变化（当前生效面与基线 profile 一致）")
	}
	lines = append(lines, "  相对基线（切回基线的变化）:")
	lines = append(lines, formatProfileDiffSection("tools", changed.ToolsAdded, changed.ToolsRemoved)...)
	lines = append(lines, formatProfileDiffSection("skills", changed.SkillsAdded, changed.SkillsRemoved)...)
	lines = append(lines, formatProfileDiffSection("mcp", changed.MCPAdded, changed.MCPRemoved)...)
	if changed.PromptChanged {
		lines = append(lines, "  prompt: 与基线不同（未固化）")
	}
	return lines
}

// chatProfileSaveAsToolNames 把策略里的工具名集合（map[string]bool）折算为升序列表。
func chatProfileSaveAsToolNames(flags map[string]bool) []string {
	names := make([]string, 0, len(flags))
	for name, enabled := range flags {
		if enabled && strings.TrimSpace(name) != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}
