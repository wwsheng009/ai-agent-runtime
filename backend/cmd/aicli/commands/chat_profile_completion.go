package commands

import (
	"sort"
	"strings"

	profilesys "github.com/wwsheng009/ai-agent-runtime/internal/profile"
)

// chat_profile_completion.go 为 `/profile` 提供逐段 Tab 补全（Batch 13 G4 可达性）：
// 一级给子命令（status/list/show/.../help），二级给引用位候选（profile 名，来自与
// `/profile list` 同一只读发现），其后给各子命令的旗标与枚举值。
//
// 纪律（与 `/routing` 补全同源）：
//   - 候选只影响**输入**，不改变 `/profile` 的解析、校验与执行语义（不猜、不隐式选第一个）；
//   - 发现失败或目录为空时不伪造候选：返回 nil 让弹窗关闭，用户仍可手写 ref；
//   - 引用位只给**可解析**的 profile（与前端 profile 菜单同一纪律：把解析失败的
//     profile 放进候选，等于承诺一次注定失败的切换）；
//   - 自由文本位（新建名、导入路径、`--out`/`--name` 取值）不做枚举。

// chatProfileCompletionSubcommands 与 tryExecuteStructuredProfileCommand 的 switch
// 分支一一对应（新增子命令时两处同步，见 chat_profile_command.go）。
func chatProfileCompletionSubcommands() []chatSlashCompletionCandidate {
	group := string(chatSlashCommandGroupSession)
	return []chatSlashCompletionCandidate{
		{Command: "status", Summary: "当前 profile：引用、来源与生效摘要（只读）", Group: group},
		{Command: "list", Summary: "可用 profile 列表（config/root/user|project 层/默认）", Group: group},
		{Command: "show", Summary: "只读预览：show <name> 将带来什么（不切换）", Group: group, AcceptsArgs: true},
		{Command: "diff", Summary: "与当前对比：diff [<name>] 的 tools/skills/mcp/prompt 变化", Group: group, AcceptsArgs: true},
		{Command: "use", Summary: "热切换：use <name>（下一 turn 生效，含 cache_notice）", Group: group, AcceptsArgs: true},
		{Command: "pick", Summary: "交互选择 profile（无选择器面时退化为只读列表）", Group: group},
		{Command: "reload", Summary: "重新解析当前 profile（磁盘编辑 profile.yaml 后）", Group: group},
		{Command: "off", Summary: "回到无 profile 基线（等价启动时不带 --profile）", Group: group},
		{Command: "save", Summary: "持久化默认 profile（--to session|workspace|config）", Group: group, AcceptsArgs: true},
		{Command: "save-as", Summary: "从当前会话固化差分（不覆盖同名）", Group: group, AcceptsArgs: true},
		{Command: "create", Summary: "按模板新建 profile（--template coding|review|minimal|docs）", Group: group, AcceptsArgs: true},
		{Command: "duplicate", Summary: "复制 profile（不覆盖同名）", Group: group, AcceptsArgs: true},
		{Command: "rename", Summary: "重命名 profile（同层；含配置引用改写）", Group: group, AcceptsArgs: true},
		{Command: "move", Summary: "层级移动（--to user|project；同层拒绝）", Group: group, AcceptsArgs: true},
		{Command: "delete", Summary: "删除 profile（引用检查 + 文件清单；--force 清空 default）", Group: group, AcceptsArgs: true},
		{Command: "export", Summary: "导出 zip（默认 ./<name>.zip）", Group: group, AcceptsArgs: true},
		{Command: "import", Summary: "导入 zip/目录（不覆盖、不自动激活）", Group: group, AcceptsArgs: true},
		{Command: "edit", Summary: "打印 profile.yaml 路径；--open 拉起 $EDITOR", Group: group, AcceptsArgs: true},
		{Command: "help", Summary: "显示 /profile 用法", Group: group},
	}
}

// chatProfileCompletionRefSubcommands 是引用位（第二个 token）取 profile 名的子命令；
// 其余子命令的首个位置是名字/包路径等自由文本，只给旗标提示。
var chatProfileCompletionRefSubcommands = map[string]bool{
	"show":      true,
	"diff":      true,
	"use":       true,
	"export":    true,
	"edit":      true,
	"delete":    true,
	"rename":    true,
	"duplicate": true,
	"move":      true,
}

// completeProfileSlashArgs 是 `/profile` 的参数补全入口（CompleteSlashArgs 的 case）。
func completeProfileSlashArgs(session *ChatSession, argsText string, cursor int) []chatSlashCompletionCandidate {
	ctx := parseSlashArgumentContext(argsText, cursor)
	query := activeSlashArgumentQuery(ctx)
	index := chatProfileCompletionTokenIndex(ctx)
	if index <= 0 {
		return matchSlashArgumentCandidates(chatProfileCompletionSubcommands(), query)
	}

	sub := strings.ToLower(slashArgumentTokenText(ctx, 0))
	if flag, ok := chatProfileCompletionCurrentFlag(ctx, index); ok {
		if values := chatProfileCompletionFlagValues(sub, flag); len(values) > 0 {
			return matchSlashArgumentCandidates(values, query)
		}
		// 自由文本取值位（--out/--name/--reason 之外的未知旗标）：不做枚举，弹窗关闭。
		return nil
	}

	if index == 1 && chatProfileCompletionRefSubcommands[sub] {
		return matchSlashArgumentCandidates(chatProfileCompletionRefCandidates(session), query)
	}
	// 其余位置只可能继续旗标（引用位已消费或本就是自由文本位）。
	return matchSlashArgumentCandidates(chatProfileCompletionFlags(sub), query)
}

// chatProfileCompletionTokenIndex 返回补全位的 token 下标：光标在 token 内时是该
// token 的下标；光标位于行尾新 token 位（`/profile use ` 之后）时等于 token 总数。
func chatProfileCompletionTokenIndex(ctx slashArgumentContext) int {
	if ctx.CurrentOK {
		for i, token := range ctx.Tokens {
			if token.Start == ctx.Current.Start && token.End == ctx.Current.End {
				return i
			}
		}
	}
	return len(ctx.Tokens)
}

// chatProfileCompletionCurrentFlag 判定当前位是否是旗标取值位：前一个 token 是旗标，
// 或当前 token 形如 `--flag=<prefix>`（与 activeSlashArgumentQuery 的等号语义同源）。
func chatProfileCompletionCurrentFlag(ctx slashArgumentContext, index int) (string, bool) {
	if ctx.CurrentOK {
		token := strings.TrimSpace(ctx.Current.Text)
		if eq := strings.Index(token, "="); eq > 0 && strings.HasPrefix(token, "--") {
			return strings.ToLower(token[:eq]), true
		}
	}
	if index <= 0 {
		return "", false
	}
	prev := strings.ToLower(slashArgumentTokenText(ctx, index-1))
	if strings.HasPrefix(prev, "--") && !strings.Contains(prev, "=") {
		return prev, true
	}
	return "", false
}

// chatProfileCompletionFlags 给出子命令接受的旗标（与 chatProfileParseFlags 对齐；
// 旗标可出现在任意位置，这里只按"当前位置能写什么"投影）。
func chatProfileCompletionFlags(sub string) []chatSlashCompletionCandidate {
	group := string(chatSlashCommandGroupSession)
	switch sub {
	case "save":
		return []chatSlashCompletionCandidate{
			{Command: "--to", Summary: "写入层选择（session|workspace|config）", Group: group, AcceptsArgs: true},
			{Command: "--yes", Summary: "config 层写入的二次确认", Group: group},
		}
	case "create":
		return []chatSlashCompletionCandidate{
			{Command: "--template", Summary: "模板 coding|review|minimal|docs", Group: group, AcceptsArgs: true},
			{Command: "--to", Summary: "写入层 user|project", Group: group, AcceptsArgs: true},
			{Command: "--force", Summary: "覆盖同名 profile（谨慎）", Group: group},
		}
	case "duplicate", "save-as":
		return []chatSlashCompletionCandidate{
			{Command: "--to", Summary: "写入层 user|project", Group: group, AcceptsArgs: true},
		}
	case "move":
		return []chatSlashCompletionCandidate{
			{Command: "--to", Summary: "目标层 user|project（同层拒绝）", Group: group, AcceptsArgs: true},
		}
	case "import":
		return []chatSlashCompletionCandidate{
			{Command: "--to", Summary: "导入层 user|project", Group: group, AcceptsArgs: true},
			{Command: "--name", Summary: "导入后的 profile 名", Group: group, AcceptsArgs: true},
			{Command: "--dry-run", Summary: "只预览不落盘", Group: group},
		}
	case "delete":
		return []chatSlashCompletionCandidate{
			{Command: "--force", Summary: "引用检查后强制删除（含清空 default）", Group: group},
		}
	case "export":
		return []chatSlashCompletionCandidate{
			{Command: "--out", Summary: "输出文件或目录（默认 ./<name>.zip）", Group: group, AcceptsArgs: true},
		}
	case "edit":
		return []chatSlashCompletionCandidate{
			{Command: "--open", Summary: "拉起 $EDITOR 打开 profile.yaml", Group: group},
		}
	}
	return nil
}

// chatProfileCompletionFlagValues 给出旗标的枚举取值；自由文本取值返回 nil（不枚举）。
func chatProfileCompletionFlagValues(sub, flag string) []chatSlashCompletionCandidate {
	group := string(chatSlashCommandGroupSession)
	switch strings.ToLower(strings.TrimSpace(flag)) {
	case "--to":
		// save 的层是会话/工作区/配置；生命周期命令（create/duplicate/save-as/
		// move/import）的层是 user/project（见 profilesys.LayerRoot 与
		// chat_profile_lifecycle.go）。
		if sub == "save" {
			return []chatSlashCompletionCandidate{
				{Command: chatRoutingLayerSession, Summary: "会话层（随 sessionmeta 持久化，resume 沿用）", Group: group},
				{Command: chatRoutingLayerWorkspace, Summary: "工作区偏好文件", Group: group},
				{Command: chatRoutingLayerConfig, Summary: "可写配置层（需 --yes 二次确认）", Group: group},
			}
		}
		return []chatSlashCompletionCandidate{
			{Command: "user", Summary: "用户层（~/.aicli/profiles）", Group: group},
			{Command: "project", Summary: "项目层（随仓库分发）", Group: group},
		}
	case "--template":
		names := profilesys.TemplateNames()
		values := make([]chatSlashCompletionCandidate, 0, len(names))
		for _, name := range names {
			values = append(values, chatSlashCompletionCandidate{
				Command: name,
				Summary: "profile 模板",
				Group:   group,
			})
		}
		return values
	}
	return nil
}

// chatProfileCompletionRefCandidates 给出引用位候选：与 `/profile list` 同源的只读发现
// （runProfileListCommand 是唯一发现逻辑），只保留可解析项（Exists 且无解析错误）。
func chatProfileCompletionRefCandidates(session *ChatSession) []chatSlashCompletionCandidate {
	if session == nil {
		return nil
	}
	result, err := runProfileListCommand(session.Config, "")
	if err != nil {
		return nil
	}
	candidates := make([]chatSlashCompletionCandidate, 0, len(result.Profiles))
	seen := make(map[string]struct{}, len(result.Profiles))
	for _, entry := range result.Profiles {
		name := strings.TrimSpace(entry.Name)
		if name == "" || !entry.Exists || strings.TrimSpace(entry.Error) != "" {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		candidates = append(candidates, chatSlashCompletionCandidate{
			Command: name,
			Summary: chatProfileCompletionRefSummary(entry),
			Group:   string(chatSlashCommandGroupSession),
		})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return strings.ToLower(candidates[i].Command) < strings.ToLower(candidates[j].Command)
	})
	return candidates
}

// chatProfileCompletionRefSummary 组装引用位候选摘要（来源 + 默认/当前标记 + 描述）。
func chatProfileCompletionRefSummary(entry profileListEntry) string {
	parts := make([]string, 0, 3)
	if source := strings.TrimSpace(entry.Source); source != "" {
		parts = append(parts, source)
	}
	if entry.IsDefault {
		parts = append(parts, "默认")
	}
	if description := strings.TrimSpace(entry.Description); description != "" {
		parts = append(parts, description)
	}
	return strings.Join(parts, " · ")
}
