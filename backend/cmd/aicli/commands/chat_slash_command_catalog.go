package commands

import "strings"

type chatSlashCommandGroup string

const (
	chatSlashCommandGroupBasics     chatSlashCommandGroup = "basics"
	chatSlashCommandGroupSession    chatSlashCommandGroup = "session"
	chatSlashCommandGroupModel      chatSlashCommandGroup = "model"
	chatSlashCommandGroupContext    chatSlashCommandGroup = "context"
	chatSlashCommandGroupPermission chatSlashCommandGroup = "permission"
	chatSlashCommandGroupFunctions  chatSlashCommandGroup = "functions"
	chatSlashCommandGroupShell      chatSlashCommandGroup = "shell"
	chatSlashCommandGroupWeb        chatSlashCommandGroup = "web"
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
	specs := []chatSlashCommandSpec{
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
			Name:        "/usage",
			Usage:       "/usage [cache [requests [N] | trace <message_id>]] | tools [N] | subagents [N] [--failed] | errors [top N]",
			Summary:     "显示会话用量、缓存与工具/子代理/失败分析",
			Group:       string(chatSlashCommandGroupSession),
			AcceptsArgs: true,
			Args: []chatSlashCommandArgSpec{
				{Token: "cache", Summary: "缓存统计（默认视图）"},
				{Token: "requests", Summary: "最近 N 条 LLM 请求明细（默认 20，上限 100）"},
				{Token: "trace", Summary: "按消息 id 追溯（produced_by/consumed_by）"},
				{Token: "tools", Summary: "工具调用/失败/耗时表（N 默认 10，上限 50）"},
				{Token: "subagents", Summary: "子代理完成率/失败分类/重试/耗时（N 默认 10，上限 50）"},
				{Token: "--failed", Summary: "subagents 仅显示失败记录"},
				{Token: "errors", Summary: "失败模式 Top-N（top N，默认 10，上限 50）"},
			},
		},
		{
			Name:        "/debug",
			Usage:       "/debug [on|off|status|display|routing|export|zip]",
			Summary:     "控制 debug 模式、显示当前会话调试信息或打包调试文件",
			Group:       string(chatSlashCommandGroupSession),
			AcceptsArgs: true,
			Args: []chatSlashCommandArgSpec{
				{Token: "on", Summary: "开启会话 debug 模式"},
				{Token: "off", Summary: "关闭会话 debug 模式"},
				{Token: "status", Summary: "查看会话 debug 模式状态"},
				{Token: "display", Summary: "显示当前会话调试信息"},
				{Token: "routing", Summary: "显示子 Agent / Team routing 配置摘要"},
				{Token: "export", Summary: "将当前会话日志与 artifact 打包为 zip"},
				{Token: "--output", Summary: "指定 zip 输出文件"},
				{Token: "--dir", Summary: "指定 zip 输出目录"},
			},
		},
		{
			Name:        "/supervision",
			Usage:       "/supervision [status|audit|wake [--deliver]|list|ack|defer|resolve|control|help]",
			Summary:     "手动核查 supervision 生命周期（digest / descendants 矩阵 / durable wake 投递）",
			Group:       string(chatSlashCommandGroupSession),
			AcceptsArgs: true,
			Args: []chatSlashCommandArgSpec{
				{Token: "status", Summary: "显示手动核查开关与 turn_end_check 生效值"},
				{Token: "audit", Summary: "只读核查 digest 与 descendants 矩阵"},
				{Token: "wake", Summary: "查看或显式投递 durable wake（缺省只做预览）"},
				{Token: "--deliver", Summary: "wake 时真正投递（--apply 等价）"},
				{Token: "--dry-run", Summary: "显式声明只做预览，不投递"},
				{Token: "--team", Summary: "指定 team id（缺省沿用当前 scope）"},
				{Token: "--limit", Summary: "限制 descendants 矩阵行数"},
				{Token: "--json", Summary: "以 JSON 输出核查结果"},
				{Token: "list", Summary: "委托 /debug supervision list"},
				{Token: "ack", Summary: "委托 /debug supervision ack"},
				{Token: "defer", Summary: "委托 /debug supervision defer"},
				{Token: "resolve", Summary: "委托 /debug supervision resolve"},
				{Token: "control", Summary: "委托 /debug supervision control"},
				{Token: "help", Summary: "显示 /supervision 用法"},
			},
		},
		{
			Name:        "/routing",
			Usage:       "/routing [show|doctor [main|sub] [--json]|on|off|main|sub|reset|save] [--to session|workspace|config] [--yes]",
			Summary:     "查看与调整会话级难度路由（provider / model / reasoning_effort）",
			Group:       string(chatSlashCommandGroupSession),
			AcceptsArgs: true,
			Args: []chatSlashCommandArgSpec{
				{Token: "show", Summary: "只读摘要（可跟 main|sub；--json 输出投影 JSON）"},
				{Token: "doctor", Summary: "逐字段来源与回退阶梯诊断"},
				{Token: "on", Summary: "开启路由（可跟 main|sub）"},
				{Token: "off", Summary: "关闭路由（可跟 main|sub）"},
				{Token: "main", Summary: "主 Agent：main <key> <value> 或 main level <level> <field> <value>"},
				{Token: "sub", Summary: "子 Agent：sub <key> <value> 或 sub level <level> <field> <value>"},
				{Token: "reset", Summary: "清除覆盖（[main|sub] [<level>] [--to session|workspace|config]）"},
				{Token: "save", Summary: "保存到目标层（[session|workspace|config]；session 层随会话持久化）"},
				{Token: "--to", Summary: "写入层选择（session|workspace|config）"},
				{Token: "--yes", Summary: "config 层写入/清除的二次确认"},
				{Token: "--json", Summary: "show 的 JSON 输出"},
			},
		},
		{
			Name:        "/profile",
			Usage:       "/profile [status|list|show <name>|diff [<name>]|use <name>|pick|reload|off|save] [--to session|workspace|config] [--yes]",
			Summary:     "查看与热切换会话 profile（场景化上下文裁剪；下一 turn 生效）",
			Group:       string(chatSlashCommandGroupSession),
			AcceptsArgs: true,
			Args: []chatSlashCommandArgSpec{
				{Token: "status", Summary: "当前 profile：引用、来源与生效摘要（只读）"},
				{Token: "list", Summary: "可用 profile 列表（config/root/默认三层）"},
				{Token: "show", Summary: "只读预览：show <name> 将带来什么（不切换）"},
				{Token: "diff", Summary: "与当前对比：diff [<name>] 的 tools/skills/mcp/prompt 变化"},
				{Token: "use", Summary: "热切换：use <name>（下一 turn 生效，含 cache_notice）"},
				{Token: "pick", Summary: "交互选择 profile（无选择器面时退化为只读列表）"},
				{Token: "reload", Summary: "重新解析当前 profile（磁盘编辑 profile.yaml 后）"},
				{Token: "off", Summary: "回到无 profile 基线（等价启动时不带 --profile）"},
				{Token: "save", Summary: "持久化默认 profile（--to session|workspace|config）"},
				{Token: "--to", Summary: "save 的写入层选择（session|workspace|config）"},
				{Token: "--yes", Summary: "config 层写入的二次确认"},
			},
		},
		{
			Name:        "/agents",
			Usage:       "/agents [panel [full|follow|target <target>|next|prev|close]|pick|target <target>|view [target]|send [target] <message>|followup [target] <message>|routing test [--scope auto|subagent|team] --role <role> --difficulty <level>|cleanup [--dry-run] [--idle <duration>]]",
			Summary:     "显示、选择或发送 agent 协作消息",
			Group:       string(chatSlashCommandGroupSession),
			AcceptsArgs: true,
			Args: []chatSlashCommandArgSpec{
				{Token: "panel", Summary: "显示多 agent 摘要面板"},
				{Token: "full", Summary: "显示 mailbox 与 timeline 完整详情"},
				{Token: "close", Summary: "关闭当前固定面板"},
				{Token: "pane", Summary: "panel 的别名"},
				{Token: "dashboard", Summary: "panel 的别名"},
				{Token: "view", Summary: "只读查看子 agent transcript（默认当前选中 target）"},
				{Token: "open", Summary: "view 的别名"},
				{Token: "transcript", Summary: "view 的别名"},
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
				{Token: "routing", Summary: "预览子 Agent / Team difficulty route"},
				{Token: "test", Summary: "routing test 子命令"},
				{Token: "--scope", Summary: "选择 auto、subagent 或 team 路由范围"},
				{Token: "cleanup", Summary: "回收 root scope 内可安全关闭的子 agent，释放 agents.maxThreads 配额"},
				{Token: "prune", Summary: "cleanup 的别名"},
				{Token: "gc", Summary: "cleanup 的别名"},
				{Token: "--dry-run", Summary: "只预览可回收对象，不执行回收"},
				{Token: "--idle", Summary: "额外回收空闲超过给定时长的子 agent（如 30m）"},
			},
		},
		{
			Name:        "/agent",
			Usage:       "/agent [target] [limit=N]",
			Summary:     "只读查看子 agent 的会话 transcript（/agents view 的等价入口）",
			Group:       string(chatSlashCommandGroupSession),
			AcceptsArgs: true,
			Args: []chatSlashCommandArgSpec{
				{Token: "target", Summary: "agent path 或 session id；缺省用 /agents target 选中的目标"},
				{Token: "limit=N", Summary: "只显示最近 N 条事件（默认 200，上限 2000）"},
				{Token: "close", Summary: "关闭固定 transcript 面板"},
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
			Usage:       "/resume [latest|<session-id>] [--cwd]",
			Summary:     "打开历史会话列表或恢复指定会话",
			Group:       string(chatSlashCommandGroupSession),
			AcceptsArgs: true,
			Args: []chatSlashCommandArgSpec{
				{Token: "latest", Summary: "直接恢复最近的其他会话"},
				{Token: "--cwd", Summary: "显式仅显示并恢复当前工作目录的会话（默认行为）"},
				{Token: "<session-id>", Summary: "恢复指定会话"},
			},
		},
		{
			Name:        "/export",
			Usage:       chatExportUsage,
			Summary:     "导出当前或历史会话",
			Group:       string(chatSlashCommandGroupSession),
			AcceptsArgs: true,
			Args: []chatSlashCommandArgSpec{
				{Token: "current", Summary: "导出当前会话"},
				{Token: "latest", Summary: "导出最近可恢复历史会话"},
				{Token: "<session-id>", Summary: "导出指定会话"},
				{Token: "--full", Summary: "导出完整 JSON，包含 tool_calls、tool 结果和 metadata"},
				{Token: "--body", Summary: "仅导出用户/助手正文 Markdown"},
				{Token: "--tools", Summary: "导出 Markdown，并附带工具调用名称与输入参数"},
				{Token: "--trace", Summary: "导出 Markdown，并附带工具调用输入与输出结果"},
				{Token: "--output", Summary: "指定输出文件"},
				{Token: "--dir", Summary: "指定输出目录"},
			},
		},
		{
			Name:         "/title",
			Aliases:      []string{"/rename"},
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
			Name:        "/goal",
			Usage:       "/goal [status|clear|pause|resume|complete|<objective>]",
			Summary:     "查看或管理当前会话长期目标",
			Group:       string(chatSlashCommandGroupSession),
			AcceptsArgs: true,
			Args: []chatSlashCommandArgSpec{
				{Token: "status", Summary: "显示当前 goal"},
				{Token: "clear", Summary: "清除当前 goal"},
				{Token: "pause", Summary: "暂停当前 goal"},
				{Token: "resume", Summary: "恢复当前 goal"},
				{Token: "complete", Summary: "标记当前 goal 完成"},
				{Token: "--json", Summary: "以 JSON 输出当前 goal"},
				{Token: "<objective>", Summary: "设置或替换当前 goal"},
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
			Name:        "/fast",
			Usage:       "/fast [on|off|toggle|status]",
			Summary:     "查看或切换 Codex Fast 模式（service_tier=priority）",
			Group:       string(chatSlashCommandGroupModel),
			AcceptsArgs: true,
			Args: []chatSlashCommandArgSpec{
				{Token: "on", Summary: "开启 Fast（priority）"},
				{Token: "off", Summary: "关闭 Fast"},
				{Token: "toggle", Summary: "切换 Fast 状态"},
				{Token: "status", Summary: "查看当前状态"},
			},
		},
		{
			Name:        "/theme",
			Usage:       "/theme [mode|palette|list|status|preview|select]",
			Summary:     "查看或切换终端主题（明暗 + 配色）",
			Group:       string(chatSlashCommandGroupBasics),
			AcceptsArgs: true,
			Interactive: true,
			Args: []chatSlashCommandArgSpec{
				{Token: "auto", Summary: "自动明暗（跟随终端）"},
				{Token: "dark", Summary: "暗色模式"},
				{Token: "light", Summary: "亮色模式"},
				{Token: "classic", Summary: "经典配色"},
				{Token: "focus", Summary: "聚焦配色（默认）"},
				{Token: "contrast", Summary: "高对比配色"},
				{Token: "mono", Summary: "单色配色"},
				{Token: "list", Summary: "列出明暗与配色（含预览）"},
				{Token: "status", Summary: "查看当前主题"},
				{Token: "preview", Summary: "预览当前与各配色样例"},
				{Token: "select", Summary: "交互选择主题"},
			},
		},
		{
			Name:        "/reasoning",
			Usage:       "/reasoning [on|off|status]",
			Summary:     "查看或切换 console reasoning 输出",
			Group:       string(chatSlashCommandGroupModel),
			AcceptsArgs: true,
			Args: []chatSlashCommandArgSpec{
				{Token: "on", Summary: "输出 reasoning 内容"},
				{Token: "off", Summary: "只保留 thinking 状态，不输出 reasoning 内容"},
				{Token: "status", Summary: "查看当前状态"},
			},
		},
		{
			Name:        "/reasoning_effort",
			Aliases:     []string{"/reasoning-effort"},
			Usage:       "/reasoning_effort [status|select|clear|<value>]",
			Summary:     "查看或切换 reasoning_effort",
			Group:       string(chatSlashCommandGroupModel),
			AcceptsArgs: true,
			Args: []chatSlashCommandArgSpec{
				{Token: "status", Summary: "查看当前 reasoning_effort"},
				{Token: "select", Summary: "交互选择当前模型支持的 reasoning_effort"},
				{Token: "clear", Summary: "清空 reasoning_effort"},
				{Token: "<value>", Summary: "直接设置 reasoning_effort"},
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
			Name:        "/provider",
			Usage:       "/provider [name|status|--provider ... --reasoning-effort ...]",
			Summary:     "查看或切换 provider（及其模型/reasoning_effort）",
			Group:       string(chatSlashCommandGroupModel),
			AcceptsArgs: true,
		},
		{
			Name:        "/model",
			Usage:       "/model [name|status|clear-reasoning|--reasoning-effort ...]",
			Summary:     "查看或切换当前 provider 下的模型/reasoning_effort",
			Group:       string(chatSlashCommandGroupModel),
			AcceptsArgs: true,
		},
		{
			Name:        "/account",
			Usage:       "/account [provider] [show|detect] [--save] [--no-refresh] [--json] [--timeout 15s]",
			Summary:     "刷新并显示单个 provider 账户余额（独立屏幕，默认当前 provider）",
			Group:       string(chatSlashCommandGroupModel),
			AcceptsArgs: true,
			Args: []chatSlashCommandArgSpec{
				{Token: "<provider>", Summary: "目标 provider，缺省为当前会话 provider"},
				{Token: "refresh", Summary: "实时拉取余额并显示（默认行为）"},
				{Token: "sync", Summary: "refresh 的别名"},
				{Token: "show", Summary: "只显示缓存账户快照，不发起网络请求"},
				{Token: "status", Summary: "show 的别名"},
				{Token: "detect", Summary: "只探测站点类型，不拉取余额"},
				{Token: "site", Summary: "detect 的别名"},
				{Token: "--save", Summary: "refresh 成功后把账户快照写回 config.yaml"},
				{Token: "--no-refresh", Summary: "等价 show：只显示缓存快照"},
				{Token: "--json", Summary: "以 JSON 输出（供脚本使用）"},
				{Token: "--timeout", Summary: "实时拉取超时（默认 15s）"},
			},
		},
		{
			Name:        "/accounts",
			Usage:       "/accounts [refresh|display] [--wait] [--enabled-only] [--no-refresh] [--json] [--timeout 15s]",
			Summary:     "显示全部 provider 账户余额（独立屏幕；默认提交后台刷新，不阻塞）",
			Group:       string(chatSlashCommandGroupModel),
			AcceptsArgs: true,
			Args: []chatSlashCommandArgSpec{
				{Token: "refresh", Summary: "只提交后台刷新请求，立即返回"},
				{Token: "sync", Summary: "refresh 的别名"},
				{Token: "display", Summary: "只显示缓存快照，不发起刷新"},
				{Token: "status", Summary: "display 的别名"},
				{Token: "--wait", Summary: "阻塞等待刷新完成后显示（旧的同步行为）"},
				{Token: "--enabled-only", Summary: "只显示已启用 provider"},
				{Token: "--no-refresh", Summary: "等价 display：只显示缓存快照"},
				{Token: "--json", Summary: "以 JSON 输出（默认隐含 --wait；display 时只输出缓存）"},
				{Token: "--timeout", Summary: "实时拉取超时（默认 15s）"},
			},
		},
		{
			Name:        "/login",
			Usage:       "/login [provider|--provider ... --base-url ... --api-key ... --model-cards ...]",
			Summary:     "新增或更新 provider 登录凭证并刷新 models",
			Group:       string(chatSlashCommandGroupModel),
			AcceptsArgs: true,
			Args: []chatSlashCommandArgSpec{
				{Token: "--provider", Summary: "provider 名称"},
				{Token: "--protocol", Summary: strings.Join(loginProtocolOptions(), "|")},
				{Token: "--base-url", Summary: "provider base URL"},
				{Token: "--api-key", Summary: "API key"},
				{Token: "--model-cards", Summary: "额外模型卡片 catalog 文件"},
				{Token: "--no-model-cards", Summary: "禁用模型卡片补齐"},
				{Token: "--model-cards-strict", Summary: "模型卡片错误时中止登录"},
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
			Name:        "/backtrack",
			Aliases:     []string{"/rewind"},
			Usage:       "/backtrack [list|select|audit|<index> --apply|--both|--edit \"text\"|--submit]",
			Summary:     "回退到历史 user turn（Esc 空输入 / select 交互选择；audit 查看 tombstone；可预览/截断/可选恢复文件）",
			Group:       string(chatSlashCommandGroupSession),
			AcceptsArgs: true,
			Args: []chatSlashCommandArgSpec{
				{Token: "list", Summary: "列出 user turns"},
				{Token: "select", Summary: "打开交互选择器（Esc 空输入等价）"},
				{Token: "audit", Summary: "列出 durable tombstone 审计摘要"},
				{Token: "<index>", Summary: "0-based user turn 序号"},
				{Token: "--apply", Summary: "执行截断（默认仅预览）"},
				{Token: "--both", Summary: "同时恢复文件变更"},
				{Token: "--code", Summary: "仅代码恢复（高级）"},
				{Token: "--edit", Summary: "编辑后的提示文本"},
				{Token: "--submit", Summary: "截断后自动发送"},
				{Token: "--include-anchor", Summary: "保留锚点 user 消息"},
			},
		},
		{
			Name:        "/memory",
			Usage:       "/memory [status|add <text>|list [n]|search <query>]",
			Summary:     "查看或写入项目级持久记忆（.aicli/memory）",
			Group:       string(chatSlashCommandGroupContext),
			AcceptsArgs: true,
			Args: []chatSlashCommandArgSpec{
				{Token: "status", Summary: "查看记忆路径与最近笔记"},
				{Token: "add", Summary: "追加一条笔记"},
				{Token: "note", Summary: "add 的别名"},
				{Token: "list", Summary: "列出最近笔记"},
				{Token: "search", Summary: "关键词搜索"},
				{Token: "flush", Summary: "add 的别名"},
			},
		},
		{
			Name:        "/attach",
			Usage:       "/attach [path|clear|remove <序号>]",
			Summary:     "查看、添加或清空待发送图片附件",
			Group:       string(chatSlashCommandGroupContext),
			AcceptsArgs: true,
			Args: []chatSlashCommandArgSpec{
				{Token: "clear", Summary: "清空待发送图片附件"},
				{Token: "remove <序号>", Summary: "移除指定图片附件"},
			},
		},
		{
			Name:        "/image",
			Usage:       "/image [prompt] [--provider <name>] [--model <name>] [--path auto|api|codex_native]",
			Summary:     "调用 openai_image_generate 生成图片",
			Group:       string(chatSlashCommandGroupContext),
			AcceptsArgs: true,
			Args: []chatSlashCommandArgSpec{
				{Token: "--prompt", Summary: "图片提示词"},
				{Token: "--provider", Summary: "指定图片生成 provider"},
				{Token: "--model", Summary: "指定图像模型"},
				{Token: "--path", Summary: "图片生成路径：auto|api|codex_native"},
				{Token: "--n", Summary: "生成图片数量"},
				{Token: "--size", Summary: "图片尺寸"},
				{Token: "--quality", Summary: "图片质量"},
				{Token: "--background", Summary: "背景模式"},
				{Token: "--output-format", Summary: "图片输出格式"},
				{Token: "--output-dir", Summary: "生成图片保存目录"},
				{Token: "--json", Summary: "输出 JSON"},
				{Token: "--debug", Summary: "输出调试信息"},
			},
		},
		{
			Name:        "/retry",
			Usage:       "/retry",
			Summary:     "将上一条失败或中断消息恢复为可编辑草稿",
			Group:       string(chatSlashCommandGroupContext),
			AcceptsArgs: false,
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
			Name:        "/trust",
			Usage:       "/trust [status|grant]",
			Summary:     "查看或授予当前工作区 folder trust（项目 plugins/hooks/MCP）",
			Group:       string(chatSlashCommandGroupPermission),
			AcceptsArgs: true,
			Args: []chatSlashCommandArgSpec{
				{Token: "status", Summary: "查看 folder trust 状态"},
				{Token: "grant", Summary: "信任当前工作区并写入 durable store"},
			},
		},
		{
			Name:        "/plan",
			Usage:       "/plan [status|enter [path]|exit <approve|request_changes|quit>]",
			Summary:     "进入/退出 plan mode，或查看计划写路径状态",
			Group:       string(chatSlashCommandGroupPermission),
			AcceptsArgs: true,
			Args: []chatSlashCommandArgSpec{
				{Token: "status", Summary: "查看 plan mode 状态"},
				{Token: "enter", Summary: "进入 plan mode"},
				{Token: "exit", Summary: "退出 plan mode"},
				{Token: "approve", Summary: "批准计划并退出"},
				{Token: "request_changes", Summary: "请求修改并保持 plan mode"},
				{Token: "quit", Summary: "放弃计划并退出"},
			},
		},
		{
			Name:        "/approval-reuse",
			Usage:       "/approval-reuse [status|clear|off|session_readonly_shell|team_readonly_shell]",
			Summary:     "查看、撤销或切换审批复用策略",
			Group:       string(chatSlashCommandGroupPermission),
			AcceptsArgs: true,
			Args: []chatSlashCommandArgSpec{
				{Token: "status", Summary: "查看当前复用授权及剩余时间"},
				{Token: "clear", Summary: "撤销当前会话中的全部复用授权"},
				{Token: "off", Summary: "关闭审批复用"},
				{Token: "session_readonly_shell", Summary: "当前会话复用只读审批"},
				{Token: "team_readonly_shell", Summary: "当前团队复用只读审批"},
			},
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
			Usage:        "/skill [--direct] <name> <prompt>",
			Summary:      "提交 skill 回合：注入说明与程序清单，由模型自选程序；--direct 直接执行",
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
			Name:        "/mcp",
			Usage:       "/mcp [list|status <name>|add <name> <url> [options]|enable|disable|remove <name>|reload|help]",
			Summary:     "管理 MCP Server（列表/新增/启停/删除/热重载，与 aicli mcp 同源）",
			Group:       string(chatSlashCommandGroupFunctions),
			AcceptsArgs: true,
			Args: []chatSlashCommandArgSpec{
				{Token: "list", Summary: "列出全部 MCP 与连接状态（默认）"},
				{Token: "status", Summary: "查看单个 MCP 的配置与运行状态（status <name>）"},
				{Token: "add", Summary: "新增：add <name> <url> 或 add <name> --command <cmd> [--arg <arg>]..."},
				{Token: "--type", Summary: "显式指定传输类型 streamable|sse|websocket（URL 传输）"},
				{Token: "--header", Summary: "URL 传输的请求头，可重复（--header \"Name: Value\"）"},
				{Token: "--env", Summary: "环境变量，可重复（--env KEY=VALUE）"},
				{Token: "enable", Summary: "启用并热重载（enable <name>）"},
				{Token: "disable", Summary: "停用并热重载（disable <name>）"},
				{Token: "remove", Summary: "删除并热重载（remove <name>）"},
				{Token: "reload", Summary: "重新加载配置并重连"},
			},
		},
		{
			Name:        "/web",
			Usage:       "/web [token|endpoints|open|status]",
			Summary:     "管理微型 Web 客户端（显示 URL/令牌、列出端点、打开浏览器）",
			Group:       string(chatSlashCommandGroupWeb),
			AcceptsArgs: true,
			Args: []chatSlashCommandArgSpec{
				{Token: "token", Summary: "显示当前 Web 写令牌"},
				{Token: "endpoints", Summary: "显示全部 Web 调试端点清单"},
				{Token: "open", Summary: "在浏览器中打开 Web 客户端页面（非回环模式则附加令牌参数）"},
				{Token: "status", Summary: "显示 Web 服务器状态（默认）"},
			},
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
	if chatRoutingUIDisabled() {
		specs = chatSlashCommandCatalogHideRouting(specs)
	}
	return specs
}

// chatSlashCommandCatalogHideRouting 在 §8.3 紧急开关（U-6）开启时从目录中移除
// `/routing` 入口：帮助与 Tab 补全不再提示；直接输入仍由命令入口给出关闭说明。
func chatSlashCommandCatalogHideRouting(specs []chatSlashCommandSpec) []chatSlashCommandSpec {
	out := make([]chatSlashCommandSpec, 0, len(specs))
	for _, spec := range specs {
		if spec.Name == "/routing" {
			continue
		}
		out = append(out, spec)
	}
	return out
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
