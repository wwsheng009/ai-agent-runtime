package commands

type chatSlashCommandGroup string

const (
	chatSlashCommandGroupBasics     chatSlashCommandGroup = "basics"
	chatSlashCommandGroupSession    chatSlashCommandGroup = "session"
	chatSlashCommandGroupModel      chatSlashCommandGroup = "model"
	chatSlashCommandGroupContext    chatSlashCommandGroup = "context"
	chatSlashCommandGroupPermission chatSlashCommandGroup = "permission"
	chatSlashCommandGroupFunctions  chatSlashCommandGroup = "functions"
	chatSlashCommandGroupShell      chatSlashCommandGroup = "shell"
	chatSlashCommandGroupHelp       chatSlashCommandGroup = "help"
)

type chatSlashCommandSpec struct {
	Name         string
	Aliases      []string
	Usage        string
	Summary      string
	Group        string
	Args         []chatSlashCommandArgSpec
	Interactive  bool
	Hidden       bool
	AcceptsArgs  bool
	RequiresArgs bool
	ShortcutOf   string
}

type chatSlashCommandArgSpec struct {
	Token   string
	Summary string
}

func chatSlashCommandCatalog() []chatSlashCommandSpec {
	return []chatSlashCommandSpec{
		{
			Name:        "/help",
			Aliases:     []string{"/?"},
			Usage:       "/help",
			Summary:     "显示命令帮助",
			Group:       string(chatSlashCommandGroupHelp),
			AcceptsArgs: false,
			Args: []chatSlashCommandArgSpec{
				{Token: "/?", Summary: "显示命令帮助"},
			},
		},
		{
			Name:        "/exit",
			Aliases:     []string{"/quit", "/q"},
			Usage:       "/exit",
			Summary:     "退出聊天",
			Group:       string(chatSlashCommandGroupBasics),
			AcceptsArgs: false,
		},
		{
			Name:        "/clear",
			Aliases:     []string{"/cls"},
			Usage:       "/clear",
			Summary:     "清空当前会话历史",
			Group:       string(chatSlashCommandGroupBasics),
			AcceptsArgs: false,
		},
		{
			Name:        "/new",
			Usage:       "/new",
			Summary:     "创建新会话",
			Group:       string(chatSlashCommandGroupSession),
			AcceptsArgs: false,
		},
		{
			Name:        "/session",
			Usage:       "/session",
			Summary:     "显示当前会话信息",
			Group:       string(chatSlashCommandGroupSession),
			AcceptsArgs: false,
		},
		{
			Name:        "/status",
			Usage:       "/status",
			Summary:     "显示当前会话状态",
			Group:       string(chatSlashCommandGroupSession),
			AcceptsArgs: false,
		},
		{
			Name:        "/debug",
			Usage:       "/debug",
			Summary:     "显示当前会话调试信息",
			Group:       string(chatSlashCommandGroupSession),
			AcceptsArgs: false,
		},
		{
			Name:        "/sessions",
			Usage:       "/sessions [query]",
			Summary:     "列出或筛选可恢复会话",
			Group:       string(chatSlashCommandGroupSession),
			AcceptsArgs: true,
			Args: []chatSlashCommandArgSpec{
				{Token: "<query>", Summary: "按关键字筛选"},
			},
		},
		{
			Name:         "/load",
			Usage:        "/load <session-id>",
			Summary:      "加载指定会话",
			Group:        string(chatSlashCommandGroupSession),
			AcceptsArgs:  true,
			RequiresArgs: true,
			Args: []chatSlashCommandArgSpec{
				{Token: "<session-id>", Summary: "会话 ID"},
			},
		},
		{
			Name:        "/resume",
			Usage:       "/resume [latest|<session-id>]",
			Summary:     "恢复最近可恢复会话或弹出恢复菜单",
			Group:       string(chatSlashCommandGroupSession),
			AcceptsArgs: true,
			Args: []chatSlashCommandArgSpec{
				{Token: "latest", Summary: "直接恢复最近可恢复会话"},
				{Token: "<session-id>", Summary: "恢复指定会话"},
			},
		},
		{
			Name:         "/title",
			Usage:        "/title <title>",
			Summary:      "更新当前会话标题",
			Group:        string(chatSlashCommandGroupSession),
			AcceptsArgs:  true,
			RequiresArgs: true,
			Args: []chatSlashCommandArgSpec{
				{Token: "<title>", Summary: "会话标题"},
			},
		},
		{
			Name:        "/history",
			Aliases:     []string{"/h"},
			Usage:       "/history",
			Summary:     "显示当前会话历史",
			Group:       string(chatSlashCommandGroupSession),
			AcceptsArgs: false,
		},
		{
			Name:        "/stream",
			Usage:       "/stream [on|off|toggle|status]",
			Summary:     "查看或切换流式输出",
			Group:       string(chatSlashCommandGroupModel),
			AcceptsArgs: true,
			Args: []chatSlashCommandArgSpec{
				{Token: "on", Summary: "开启流式输出"},
				{Token: "off", Summary: "关闭流式输出"},
				{Token: "toggle", Summary: "切换流式状态"},
				{Token: "status", Summary: "查看当前状态"},
			},
		},
		{
			Name:        "/s",
			Usage:       "/s",
			Summary:     "流式开启快捷（等价 /stream on）",
			Group:       string(chatSlashCommandGroupModel),
			AcceptsArgs: false,
			ShortcutOf:  "/stream",
		},
		{
			Name:        "/normal",
			Aliases:     []string{"/n"},
			Usage:       "/normal",
			Summary:     "流式关闭快捷（等价 /stream off）",
			Group:       string(chatSlashCommandGroupModel),
			AcceptsArgs: false,
			ShortcutOf:  "/stream",
		},
		{
			Name:        "/model",
			Usage:       "/model [name|status|clear-reasoning|--provider ...]",
			Summary:     "查看或切换 provider/model/thinking_effort",
			Group:       string(chatSlashCommandGroupModel),
			AcceptsArgs: true,
		},
		{
			Name:        "/compact",
			Usage:       "/compact [auto|local|remote]",
			Summary:     "手动触发会话压缩",
			Group:       string(chatSlashCommandGroupModel),
			AcceptsArgs: true,
			Args: []chatSlashCommandArgSpec{
				{Token: "auto", Summary: "自动模式"},
				{Token: "local", Summary: "本地压缩"},
				{Token: "remote", Summary: "远端压缩"},
			},
		},
		{
			Name:        "/image",
			Usage:       "/image [path|clear]",
			Summary:     "查看、添加或清空图片附件",
			Group:       string(chatSlashCommandGroupContext),
			AcceptsArgs: true,
			Args: []chatSlashCommandArgSpec{
				{Token: "clear", Summary: "清空待发送图片附件"},
			},
		},
		{
			Name:        "/queue",
			Usage:       "/queue [status|clear]",
			Summary:     "查看或清空排队输入",
			Group:       string(chatSlashCommandGroupContext),
			AcceptsArgs: true,
			Args: []chatSlashCommandArgSpec{
				{Token: "status", Summary: "查看当前状态"},
				{Token: "clear", Summary: "清空排队输入"},
			},
		},
		{
			Name:        "/permission-mode",
			Aliases:     []string{"/mode"},
			Usage:       "/permission-mode [default|accept_edits|plan|bypass_permissions]",
			Summary:     "查看或切换权限模式",
			Group:       string(chatSlashCommandGroupPermission),
			AcceptsArgs: true,
			Args: []chatSlashCommandArgSpec{
				{Token: "default", Summary: "默认权限"},
				{Token: "accept_edits", Summary: "允许编辑"},
				{Token: "plan", Summary: "计划模式"},
				{Token: "bypass_permissions", Summary: "绕过权限"},
			},
		},
		{
			Name:        "/approval-reuse",
			Usage:       "/approval-reuse [off|session_readonly_shell|team_readonly_shell]",
			Summary:     "查看或切换审批复用策略",
			Group:       string(chatSlashCommandGroupPermission),
			AcceptsArgs: true,
		},
		{
			Name:        "/yolo",
			Usage:       "/yolo",
			Summary:     "切换到 bypass_permissions",
			Group:       string(chatSlashCommandGroupPermission),
			AcceptsArgs: false,
		},
		{
			Name:        "/functions",
			Aliases:     []string{"/catalog"},
			Usage:       "/functions [prompt|--json]",
			Summary:     "查看或预览 function catalog",
			Group:       string(chatSlashCommandGroupFunctions),
			AcceptsArgs: true,
		},
		{
			Name:         "/function",
			Aliases:      []string{"/describe"},
			Usage:        "/function <name> [--json]",
			Summary:      "查看单个 function 描述",
			Group:        string(chatSlashCommandGroupFunctions),
			AcceptsArgs:  true,
			RequiresArgs: true,
		},
		{
			Name:         "/call",
			Aliases:      []string{"/tool"},
			Usage:        "/call <name> [args-json]",
			Summary:      "直接执行 function/tool",
			Group:        string(chatSlashCommandGroupFunctions),
			AcceptsArgs:  true,
			RequiresArgs: true,
		},
		{
			Name:         "/skill",
			Usage:        "/skill <name> <prompt>",
			Summary:      "直接执行指定 skill",
			Group:        string(chatSlashCommandGroupFunctions),
			AcceptsArgs:  true,
			RequiresArgs: true,
		},
		{
			Name:         "/shell",
			Aliases:      []string{"/cmd"},
			Usage:        "/shell [--output-bytes-cap <bytes> | --disable-output-cap] <command>",
			Summary:      "执行 shell 命令并把输出分享给 AI",
			Group:        string(chatSlashCommandGroupShell),
			AcceptsArgs:  true,
			RequiresArgs: true,
		},
	}
}

func chatSlashCommandCatalogMap() map[string]chatSlashCommandSpec {
	specs := chatSlashCommandCatalog()
	index := make(map[string]chatSlashCommandSpec, len(specs)*2)
	for _, spec := range specs {
		index[spec.Name] = spec
		for _, alias := range spec.Aliases {
			index[alias] = spec
		}
	}
	return index
}

func (s chatSlashCommandSpec) allNames() []string {
	names := make([]string, 0, 1+len(s.Aliases))
	if s.Name != "" {
		names = append(names, s.Name)
	}
	names = append(names, s.Aliases...)
	return names
}
