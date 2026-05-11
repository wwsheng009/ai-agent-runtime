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
			Usage:       "/debug [export|zip]",
			Summary:     "显示当前会话调试信息或打包调试文件",
			Group:       string(chatSlashCommandGroupSession),
			AcceptsArgs: true,
			Args: []chatSlashCommandArgSpec{
				{Token: "export", Summary: "将当前 /debug 展示的会话日志与 artifact 打包为 zip"},
				{Token: "--output", Summary: "指定 zip 输出文件"},
				{Token: "--dir", Summary: "指定 zip 输出目录"},
			},
		},
		{
			Name:        "/agents",
			Usage:       "/agents [panel [follow|target <target>|next|prev]|pick|target <target>|send [target] <message>|followup [target] <message>]",
			Summary:     "显示、选择或发送 agent 协作消息",
			Group:       string(chatSlashCommandGroupSession),
			AcceptsArgs: true,
			Args: []chatSlashCommandArgSpec{
				{Token: "panel", Summary: "显示多 agent 富交互面板"},
				{Token: "pane", Summary: "panel 的别名"},
				{Token: "dashboard", Summary: "panel 的别名"},
				{Token: "follow", Summary: "进入 fixed-bottom 面板跟随模式，legacy 终端等待 mailbox 更新后刷新一次"},
				{Token: "watch", Summary: "follow 的别名"},
				{Token: "next", Summary: "切换 panel 到下一个 agent target"},
				{Token: "prev", Summary: "切换 panel 到上一个 agent target"},
				{Token: "previous", Summary: "prev 的别名"},
				{Token: "↑↓", Summary: "panel follow 中移动 agent 游标"},
				{Token: "←→", Summary: "panel follow 中切换 agents/mailbox/timeline pane"},
				{Token: "Enter", Summary: "panel follow 中将游标 agent 设为默认 target"},
				{Token: "Esc", Summary: "退出 panel follow"},
				{Token: "pick", Summary: "弹出 agent picker"},
				{Token: "select", Summary: "pick 的别名"},
				{Token: "target", Summary: "设置默认 agent 消息目标"},
				{Token: "clear", Summary: "清空默认 agent 消息目标"},
				{Token: "none", Summary: "清空默认 agent 消息目标"},
				{Token: "send", Summary: "向目标 agent 投递 mailbox 消息"},
				{Token: "followup", Summary: "向目标 agent 投递或触发 follow-up task"},
				{Token: "task", Summary: "followup 的别名"},
			},
		},
		{
			Name:        "/timeline",
			Usage:       "/timeline [team|active] [limit] [filter=<text>]",
			Summary:     "显示指定或 active team 协作事件",
			Group:       string(chatSlashCommandGroupSession),
			AcceptsArgs: true,
			Args: []chatSlashCommandArgSpec{
				{Token: "active", Summary: "显示当前 active team"},
				{Token: "<team_id>", Summary: "显示指定 team"},
				{Token: "<limit>", Summary: "最多显示事件数"},
				{Token: "filter=<text>", Summary: "按事件行文本过滤"},
			},
		},
		{
			Name:        "/collab",
			Usage:       "/collab [follow] [target|selected|parent|all] [limit] [filter=<text>] [timeout=10s]",
			Summary:     "显示 parent 或 agent mailbox 协作事件",
			Group:       string(chatSlashCommandGroupSession),
			AcceptsArgs: true,
			Args: []chatSlashCommandArgSpec{
				{Token: "follow", Summary: "等待下一次 mailbox 更新并刷新视图"},
				{Token: "selected", Summary: "显示当前选中 agent 的 mailbox"},
				{Token: "target", Summary: "显示当前选中 agent 的 mailbox"},
				{Token: "parent", Summary: "显示 parent mailbox"},
				{Token: "all", Summary: "聚合显示 parent 和所有 child agent mailbox"},
				{Token: "<target>", Summary: "显示指定 session 或 agent path 的 mailbox"},
				{Token: "<limit>", Summary: "最多显示事件数"},
				{Token: "filter=<text>", Summary: "按事件行文本过滤"},
				{Token: "timeout=<duration>", Summary: "follow 等待时长"},
			},
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
			Name:        "/export",
			Usage:       "/export [current|latest|<session-id>] [--full|--body] [--output <path>|--dir <dir>]",
			Summary:     "导出当前或历史会话",
			Group:       string(chatSlashCommandGroupSession),
			AcceptsArgs: true,
			Args: []chatSlashCommandArgSpec{
				{Token: "current", Summary: "导出当前会话"},
				{Token: "latest", Summary: "导出最近可恢复历史会话"},
				{Token: "<session-id>", Summary: "导出指定会话"},
				{Token: "--full", Summary: "导出完整 JSON，包含 tool_calls、tool 结果和 metadata"},
				{Token: "--body", Summary: "仅导出用户/助手正文 Markdown"},
				{Token: "--output", Summary: "指定输出文件"},
				{Token: "--dir", Summary: "指定输出目录"},
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
			Summary:     "查看或切换 provider/model/reasoning_effort",
			Group:       string(chatSlashCommandGroupModel),
			AcceptsArgs: true,
		},
		{
			Name:        "/login",
			Usage:       "/login [provider|--provider ... --base-url ... --api-key ...]",
			Summary:     "新增或更新 provider 登录凭证并刷新 models",
			Group:       string(chatSlashCommandGroupModel),
			AcceptsArgs: true,
			Args: []chatSlashCommandArgSpec{
				{Token: "--provider", Summary: "provider 名称"},
				{Token: "--protocol", Summary: "openai|anthropic|gemini|codex-apikey|codex-oauth"},
				{Token: "--base-url", Summary: "provider base URL"},
				{Token: "--api-key", Summary: "API key"},
				{Token: "--switch", Summary: "登录成功后切换当前会话"},
			},
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
			Summary:      "直接执行 function/tool；openai_image_generate 可直接传 prompt",
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
			Name:        "/skills",
			Usage:       "/skills [query]",
			Summary:     "列出并选择执行 skill",
			Group:       string(chatSlashCommandGroupFunctions),
			AcceptsArgs: true,
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
