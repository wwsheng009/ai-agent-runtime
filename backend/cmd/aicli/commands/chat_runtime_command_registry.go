package commands

import (
	"sort"
	"strings"
)

// P2-1（方案 §3.8.2）：统一运行时交互注册表——「怎么交互」（Mode）与「何时生效」
// （Effect）两个正交维度 + 业务域（C1~C12）。本文件只负责**声明与解析**：
// 路由/宿主（P2-3）与三级开关（P2-2）在后续步骤消费它；未登记命令与未知子命令
// 一律返回 queue（INV-6，fail-safe，不丢输入）。

type runtimeInteractionMode uint8

const (
	runtimeModeInline runtimeInteractionMode = iota
	runtimeModeScreen
	runtimeModePrompt
	runtimeModeQueue
	runtimeModeBlock
)

func (m runtimeInteractionMode) String() string {
	switch m {
	case runtimeModeInline:
		return "inline"
	case runtimeModeScreen:
		return "screen"
	case runtimeModePrompt:
		return "prompt"
	case runtimeModeBlock:
		return "block"
	default:
		return "queue"
	}
}

// runtimeEffectScope 是生效域。read/live/next-turn/next-call 可运行时执行；
// session/process 只允许 queue/block（§3.8.1 不变式，T28 断言）。
type runtimeEffectScope uint8

const (
	runtimeEffectRead runtimeEffectScope = iota
	runtimeEffectLive
	runtimeEffectNextTurn
	runtimeEffectNextCall
	runtimeEffectSession
	runtimeEffectProcess
)

func (e runtimeEffectScope) String() string {
	switch e {
	case runtimeEffectRead:
		return "read"
	case runtimeEffectLive:
		return "live"
	case runtimeEffectNextTurn:
		return "next-turn"
	case runtimeEffectNextCall:
		return "next-call"
	case runtimeEffectProcess:
		return "process"
	default:
		return "session"
	}
}

type commandCategory uint8

const (
	categorySessionLifecycle   commandCategory = iota + 1 // C1
	categoryTurnsGoals                                    // C2
	categorySessionMeta                                   // C3
	categoryModelRouting                                  // C4
	categoryPermissionSecurity                            // C5
	categorySandboxWorkspace                              // C6
	categoryContextInput                                  // C7
	categorySkillsTools                                   // C8
	categoryDiagnostics                                   // C9
	categoryOutputAppearance                              // C10
	categoryNetworkLongTasks                              // C11
	categoryTasksTodos                                    // C12
)

func (c commandCategory) String() string {
	if c >= categorySessionLifecycle && c <= categoryTasksTodos {
		return commandCategoryNames[int(c)-1]
	}
	return "C?"
}

var commandCategoryNames = [...]string{"C1", "C2", "C3", "C4", "C5", "C6", "C7", "C8", "C9", "C10", "C11", "C12"}

// runtimeCommandSpec 是注册表的单条声明（§3.8.2）。
type runtimeCommandSpec struct {
	Command   string // 主命令名（别名已归并）
	Category  commandCategory
	Mode      runtimeInteractionMode
	Effect    runtimeEffectScope
	Confirm   bool   // prompt/screen 是否需要显式确认
	Notice    string // 生效域提示模板（如「下一回合生效」）
	SwitchKey string // 分类/命令级开关键（解析器默认按主命令生成）
	// Output 是输出类别（批次 5 单一事实源，方案 §5.2(4)）。零值表示未显式
	// 声明，按 Mode/Confirm 派生；screen 档必须显式声明（T11 守卫）。
	Output chatCommandOutputCategory
}

type runtimeCommandEntry struct {
	Bare     *runtimeCommandSpec           // 裸命令（无参数）
	Variants map[string]runtimeCommandSpec // 首 token（小写）→ spec
	Wildcard *runtimeCommandSpec           // 有参数且无 token 匹配时的兜底
}

type runtimeSpecOption func(*runtimeCommandSpec)

func rtConfirm() runtimeSpecOption {
	return func(spec *runtimeCommandSpec) { spec.Confirm = true }
}

func rtNotice(notice string) runtimeSpecOption {
	return func(spec *runtimeCommandSpec) { spec.Notice = notice }
}

// rtOutput 显式声明输出类别（批次 5：与 Mode/Effect 同源，见 chat_command_output_category.go）。
func rtOutput(category chatCommandOutputCategory) runtimeSpecOption {
	return func(spec *runtimeCommandSpec) { spec.Output = category }
}

func rtSpec(command string, category commandCategory, mode runtimeInteractionMode, effect runtimeEffectScope, opts ...runtimeSpecOption) runtimeCommandSpec {
	spec := runtimeCommandSpec{Command: command, Category: category, Mode: mode, Effect: effect}
	for _, opt := range opts {
		opt(&spec)
	}
	return spec
}

func rtBare(spec runtimeCommandSpec) *runtimeCommandSpec { return &spec }

func rtVariant(spec runtimeCommandSpec) runtimeCommandSpec { return spec }

// runtimeCommandSwitchKey 返回命令级开关键；分类级键由 categoryCovers 派生。
func runtimeCommandSwitchKey(command string) string {
	if command == "" {
		return ""
	}
	return `chat.runtime_interaction.commands."` + command + `"`
}

// canonicalRuntimeCommandName 把别名/legacy 入口归并到注册表主命令名。
// 优先走 catalog 别名索引；未命中时用 legacy 表补齐（F.2）。
func canonicalRuntimeCommandName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return ""
	}
	if strings.HasPrefix(name, "/mode:") {
		return "/permission-mode"
	}
	if spec, ok := chatSlashCommandCatalogMap()[name]; ok && spec.Name != "" {
		return spec.Name
	}
	if canonical, ok := runtimeLegacyAliasCommands[name]; ok {
		return canonical
	}
	return name
}

// runtimeLegacyAliasName 返回归并后剩余的参数部分（目前只有 `/mode:<name>`）。
func runtimeLegacyAliasArgs(name string, args []string) []string {
	lower := strings.ToLower(strings.TrimSpace(name))
	if strings.HasPrefix(lower, "/mode:") {
		value := strings.TrimSpace(strings.TrimPrefix(lower, "/mode:"))
		if value == "" {
			return args
		}
		return append([]string{value}, args...)
	}
	return args
}

var runtimeLegacyAliasCommands = map[string]string{
	"/?":                "/help",
	"/h":                "/history",
	"/q":                "/exit",
	"/quit":             "/exit",
	"/cls":              "/clear",
	"/n":                "/normal",
	"/cmd":              "/shell",
	"/rewind":           "/backtrack",
	"/describe":         "/function",
	"/catalog":          "/functions",
	"/rename":           "/title",
	"/reasoning-effort": "/reasoning_effort",
}

// runtimeCommandRegistry 是附录 F.1 的代码化定稿（含 F.2 别名归并后的主命令）。
// 未列出的子命令与未知参数 → queue（INV-6）；/exit 是唯一 block。
var runtimeCommandRegistry = map[string]runtimeCommandEntry{
	// C1 会话生命周期与破坏类
	"/exit":  {Bare: rtBare(rtSpec("/exit", categorySessionLifecycle, runtimeModeBlock, runtimeEffectProcess, rtNotice("忙时不可退出；请等待回合结束")))},
	"/clear": {Bare: rtBare(rtSpec("/clear", categorySessionLifecycle, runtimeModeQueue, runtimeEffectSession, rtNotice("已排队，回合结束后执行")))},
	"/new":   {Bare: rtBare(rtSpec("/new", categorySessionLifecycle, runtimeModeQueue, runtimeEffectSession, rtNotice("已排队，回合结束后执行")))},
	"/load":  {Bare: rtBare(rtSpec("/load", categorySessionLifecycle, runtimeModeQueue, runtimeEffectSession, rtNotice("已排队，回合结束后执行")))},
	"/resume": {Bare: rtBare(rtSpec("/resume", categorySessionLifecycle, runtimeModeQueue, runtimeEffectSession, rtNotice("已排队，回合结束后执行"))),
		Wildcard: ptrRuntimeSpec(rtSpec("/resume", categorySessionLifecycle, runtimeModeQueue, runtimeEffectSession, rtNotice("已排队，回合结束后执行")))},
	"/compact": {Bare: rtBare(rtSpec("/compact", categorySessionLifecycle, runtimeModeQueue, runtimeEffectSession, rtNotice("已排队，回合结束后执行")))},
	"/backtrack": {
		Bare: rtBare(rtSpec("/backtrack", categoryTurnsGoals, runtimeModeQueue, runtimeEffectSession, rtNotice("已排队，回合结束后执行"))),
		Variants: map[string]runtimeCommandSpec{
			"list":  rtSpec("/backtrack", categoryTurnsGoals, runtimeModeInline, runtimeEffectRead),
			"audit": rtSpec("/backtrack", categoryTurnsGoals, runtimeModeInline, runtimeEffectRead),
			"apply": rtSpec("/backtrack", categoryTurnsGoals, runtimeModeQueue, runtimeEffectSession, rtNotice("已排队，回合结束后执行")),
		},
	},

	// C2 回合、目标与计划
	"/goal": {
		Bare: rtBare(rtSpec("/goal", categoryTurnsGoals, runtimeModeInline, runtimeEffectRead)),
		Variants: map[string]runtimeCommandSpec{
			"status":   rtSpec("/goal", categoryTurnsGoals, runtimeModeInline, runtimeEffectRead),
			"clear":    rtSpec("/goal", categoryTurnsGoals, runtimeModePrompt, runtimeEffectNextTurn, rtConfirm()),
			"pause":    rtSpec("/goal", categoryTurnsGoals, runtimeModePrompt, runtimeEffectNextTurn, rtConfirm()),
			"resume":   rtSpec("/goal", categoryTurnsGoals, runtimeModePrompt, runtimeEffectNextTurn, rtConfirm()),
			"complete": rtSpec("/goal", categoryTurnsGoals, runtimeModePrompt, runtimeEffectNextTurn, rtConfirm()),
			"set":      rtSpec("/goal", categoryTurnsGoals, runtimeModeQueue, runtimeEffectSession, rtNotice("已排队，回合结束后执行")),
		},
		// `/goal <objective>` 会派生新 turn → queue。
		Wildcard: ptrRuntimeSpec(rtSpec("/goal", categoryTurnsGoals, runtimeModeQueue, runtimeEffectSession, rtNotice("已排队，回合结束后执行"))),
	},
	"/plan": {
		Bare: rtBare(rtSpec("/plan", categoryTurnsGoals, runtimeModeInline, runtimeEffectRead)),
		Variants: map[string]runtimeCommandSpec{
			"status":          rtSpec("/plan", categoryTurnsGoals, runtimeModeInline, runtimeEffectRead),
			"review":          rtSpec("/plan", categoryTurnsGoals, runtimeModeInline, runtimeEffectRead),
			"comment":         rtSpec("/plan", categoryTurnsGoals, runtimeModeInline, runtimeEffectRead),
			"enter":           rtSpec("/plan", categoryTurnsGoals, runtimeModePrompt, runtimeEffectNextCall, rtConfirm()),
			"exit":            rtSpec("/plan", categoryTurnsGoals, runtimeModePrompt, runtimeEffectNextCall, rtConfirm()),
			"approve":         rtSpec("/plan", categoryTurnsGoals, runtimeModePrompt, runtimeEffectNextCall, rtConfirm()),
			"request_changes": rtSpec("/plan", categoryTurnsGoals, runtimeModeQueue, runtimeEffectSession, rtNotice("已排队，回合结束后执行")),
		},
	},
	"/plans": {
		Bare: rtBare(rtSpec("/plans", categoryTurnsGoals, runtimeModeInline, runtimeEffectRead)),
		Variants: map[string]runtimeCommandSpec{
			"list":   rtSpec("/plans", categoryTurnsGoals, runtimeModeInline, runtimeEffectRead),
			"detail": rtSpec("/plans", categoryTurnsGoals, runtimeModeScreen, runtimeEffectRead, rtNotice("备用屏不可用时降级为内联文档"), rtOutput(chatOutputScreenDocument)),
			"diff":   rtSpec("/plans", categoryTurnsGoals, runtimeModeInline, runtimeEffectRead),
			"reopen": rtSpec("/plans", categoryTurnsGoals, runtimeModeQueue, runtimeEffectSession, rtNotice("已排队，回合结束后执行")),
		},
	},
	"/retry": {Bare: rtBare(rtSpec("/retry", categoryTurnsGoals, runtimeModePrompt, runtimeEffectRead, rtConfirm(), rtNotice("只会回填草稿，不会自动发送")))},
	"/shell": {Bare: rtBare(rtSpec("/shell", categoryTurnsGoals, runtimeModeQueue, runtimeEffectSession, rtNotice("已排队，回合结束后执行"))),
		Wildcard: ptrRuntimeSpec(rtSpec("/shell", categoryTurnsGoals, runtimeModeQueue, runtimeEffectSession, rtNotice("已排队，回合结束后执行")))},
	"/call": {Bare: rtBare(rtSpec("/call", categorySkillsTools, runtimeModeQueue, runtimeEffectSession, rtNotice("已排队，回合结束后执行"))),
		Wildcard: ptrRuntimeSpec(rtSpec("/call", categorySkillsTools, runtimeModeQueue, runtimeEffectSession, rtNotice("已排队，回合结束后执行")))},
	"/skill": {Bare: rtBare(rtSpec("/skill", categorySkillsTools, runtimeModeQueue, runtimeEffectSession, rtNotice("已排队，回合结束后执行"))),
		Wildcard: ptrRuntimeSpec(rtSpec("/skill", categorySkillsTools, runtimeModeQueue, runtimeEffectSession, rtNotice("已排队，回合结束后执行")))},
	// C3 会话元数据与历史
	"/session":  {Bare: rtBare(rtSpec("/session", categorySessionMeta, runtimeModeInline, runtimeEffectRead))},
	"/sessions": {Bare: rtBare(rtSpec("/sessions", categorySessionMeta, runtimeModeScreen, runtimeEffectRead, rtNotice("备用屏不可用时降级为内联文档"), rtOutput(chatOutputScreenDocument)))},
	"/history":  {Bare: rtBare(rtSpec("/history", categorySessionMeta, runtimeModeScreen, runtimeEffectRead, rtNotice("备用屏不可用时降级为内联文档"), rtOutput(chatOutputScreenDocument)))},
	"/title": {
		Bare:     rtBare(rtSpec("/title", categorySessionMeta, runtimeModePrompt, runtimeEffectNextTurn, rtConfirm(), rtNotice("下一回合生效"))),
		Wildcard: ptrRuntimeSpec(rtSpec("/title", categorySessionMeta, runtimeModeInline, runtimeEffectNextTurn, rtNotice("下一回合生效"))),
	},
	"/export": {Bare: rtBare(rtSpec("/export", categorySessionMeta, runtimeModeScreen, runtimeEffectRead, rtConfirm(), rtNotice("确认后写外部文件，不写会话"), rtOutput(chatOutputScreenInteractive)))},
	"/agent":  {Bare: rtBare(rtSpec("/agent", categoryDiagnostics, runtimeModeInline, runtimeEffectRead))},

	// C4 模型与路由
	"/model": {
		Bare:     rtBare(rtSpec("/model", categoryModelRouting, runtimeModeScreen, runtimeEffectNextTurn, rtConfirm(), rtNotice("下一回合生效"), rtOutput(chatOutputScreenInteractive))),
		Variants: map[string]runtimeCommandSpec{"status": rtSpec("/model", categoryModelRouting, runtimeModeScreen, runtimeEffectRead, rtNotice("备用屏不可用时降级为内联文档"), rtOutput(chatOutputScreenDocument))},
		Wildcard: ptrRuntimeSpec(rtSpec("/model", categoryModelRouting, runtimeModeScreen, runtimeEffectNextTurn, rtConfirm(), rtNotice("下一回合生效"), rtOutput(chatOutputScreenInteractive))),
	},
	"/provider": {
		Bare:     rtBare(rtSpec("/provider", categoryModelRouting, runtimeModeScreen, runtimeEffectNextTurn, rtConfirm(), rtNotice("下一回合生效"), rtOutput(chatOutputScreenInteractive))),
		Variants: map[string]runtimeCommandSpec{"status": rtSpec("/provider", categoryModelRouting, runtimeModeScreen, runtimeEffectRead, rtNotice("备用屏不可用时降级为内联文档"), rtOutput(chatOutputScreenDocument))},
		Wildcard: ptrRuntimeSpec(rtSpec("/provider", categoryModelRouting, runtimeModeScreen, runtimeEffectNextTurn, rtConfirm(), rtNotice("下一回合生效"), rtOutput(chatOutputScreenInteractive))),
	},
	"/routing": {
		Bare: rtBare(rtSpec("/routing", categoryModelRouting, runtimeModeInline, runtimeEffectRead)),
		Variants: map[string]runtimeCommandSpec{
			"show":   rtSpec("/routing", categoryModelRouting, runtimeModeInline, runtimeEffectRead),
			"doctor": rtSpec("/routing", categoryModelRouting, runtimeModeInline, runtimeEffectRead),
			"panel":  rtSpec("/routing", categoryModelRouting, runtimeModeScreen, runtimeEffectNextTurn, rtConfirm(), rtNotice("面板内写动作逐项确认"), rtOutput(chatOutputScreenInteractive)),
			"on":     rtSpec("/routing", categoryModelRouting, runtimeModePrompt, runtimeEffectNextTurn, rtConfirm(), rtNotice("下一回合生效")),
			"off":    rtSpec("/routing", categoryModelRouting, runtimeModePrompt, runtimeEffectNextTurn, rtConfirm(), rtNotice("下一回合生效")),
			"reset":  rtSpec("/routing", categoryModelRouting, runtimeModePrompt, runtimeEffectNextTurn, rtConfirm(), rtNotice("下一回合生效")),
			"save":   rtSpec("/routing", categoryModelRouting, runtimeModePrompt, runtimeEffectNextTurn, rtConfirm(), rtNotice("下一回合生效")),
		},
	},
	"/profile": {
		Bare: rtBare(rtSpec("/profile", categoryModelRouting, runtimeModeInline, runtimeEffectRead)),
		Variants: map[string]runtimeCommandSpec{
			"status": rtSpec("/profile", categoryModelRouting, runtimeModeScreen, runtimeEffectRead, rtNotice("备用屏不可用时降级为内联文档"), rtOutput(chatOutputScreenDocument)),
			"list":   rtSpec("/profile", categoryModelRouting, runtimeModeScreen, runtimeEffectRead, rtNotice("备用屏不可用时降级为内联文档"), rtOutput(chatOutputScreenDocument)),
			"show":   rtSpec("/profile", categoryModelRouting, runtimeModeScreen, runtimeEffectRead, rtNotice("备用屏不可用时降级为内联文档"), rtOutput(chatOutputScreenDocument)),
			"diff":   rtSpec("/profile", categoryModelRouting, runtimeModeScreen, runtimeEffectRead, rtNotice("备用屏不可用时降级为内联文档"), rtOutput(chatOutputScreenDocument)),
			"pick":   rtSpec("/profile", categoryModelRouting, runtimeModeScreen, runtimeEffectNextTurn, rtConfirm(), rtNotice("下一回合生效"), rtOutput(chatOutputScreenInteractive)),
			"reload": rtSpec("/profile", categoryModelRouting, runtimeModeQueue, runtimeEffectNextTurn, rtNotice("已排队，回合结束后执行")),
			"save":   rtSpec("/profile", categoryModelRouting, runtimeModeQueue, runtimeEffectNextTurn, rtNotice("已排队，回合结束后执行")),
			"import": rtSpec("/profile", categoryModelRouting, runtimeModeQueue, runtimeEffectNextTurn, rtNotice("已排队，回合结束后执行")),
		},
	},

	// C5 权限与安全
	"/permission-mode": {
		Bare:     rtBare(rtSpec("/permission-mode", categoryPermissionSecurity, runtimeModeInline, runtimeEffectRead)),
		Variants: map[string]runtimeCommandSpec{"status": rtSpec("/permission-mode", categoryPermissionSecurity, runtimeModeInline, runtimeEffectRead), "set": rtSpec("/permission-mode", categoryPermissionSecurity, runtimeModePrompt, runtimeEffectNextCall, rtConfirm(), rtNotice("对后续工具调用生效"))},
		Wildcard: ptrRuntimeSpec(rtSpec("/permission-mode", categoryPermissionSecurity, runtimeModePrompt, runtimeEffectNextCall, rtConfirm(), rtNotice("对后续工具调用生效"))),
	},
	"/trust": {
		Bare:     rtBare(rtSpec("/trust", categoryPermissionSecurity, runtimeModeInline, runtimeEffectRead)),
		Variants: map[string]runtimeCommandSpec{"status": rtSpec("/trust", categoryPermissionSecurity, runtimeModeInline, runtimeEffectRead), "grant": rtSpec("/trust", categoryPermissionSecurity, runtimeModePrompt, runtimeEffectNextCall, rtConfirm(), rtNotice("对后续工具调用生效"))},
	},
	"/grants": {
		Bare:     rtBare(rtSpec("/grants", categoryPermissionSecurity, runtimeModeInline, runtimeEffectRead)),
		Variants: map[string]runtimeCommandSpec{"list": rtSpec("/grants", categoryPermissionSecurity, runtimeModeInline, runtimeEffectRead), "revoke": rtSpec("/grants", categoryPermissionSecurity, runtimeModePrompt, runtimeEffectNextCall, rtConfirm(), rtNotice("对后续工具调用生效"))},
	},
	"/approval-reuse": {
		Bare:     rtBare(rtSpec("/approval-reuse", categoryPermissionSecurity, runtimeModeInline, runtimeEffectRead)),
		Variants: map[string]runtimeCommandSpec{"status": rtSpec("/approval-reuse", categoryPermissionSecurity, runtimeModeInline, runtimeEffectRead), "list": rtSpec("/approval-reuse", categoryPermissionSecurity, runtimeModeInline, runtimeEffectRead), "set": rtSpec("/approval-reuse", categoryPermissionSecurity, runtimeModePrompt, runtimeEffectNextCall, rtConfirm(), rtNotice("对后续工具调用生效"))},
	},
	"/yolo": {Bare: rtBare(rtSpec("/yolo", categoryPermissionSecurity, runtimeModePrompt, runtimeEffectNextCall, rtConfirm(), rtNotice("提权：对后续工具调用生效")))},
	"/add-dir": {
		Bare:     rtBare(rtSpec("/add-dir", categorySandboxWorkspace, runtimeModeInline, runtimeEffectRead)),
		Variants: map[string]runtimeCommandSpec{"list": rtSpec("/add-dir", categorySandboxWorkspace, runtimeModeInline, runtimeEffectRead), "add": rtSpec("/add-dir", categorySandboxWorkspace, runtimeModePrompt, runtimeEffectNextTurn, rtConfirm(), rtNotice("下一回合生效")), "remove": rtSpec("/add-dir", categorySandboxWorkspace, runtimeModePrompt, runtimeEffectNextTurn, rtConfirm(), rtNotice("下一回合生效"))},
		Wildcard: ptrRuntimeSpec(rtSpec("/add-dir", categorySandboxWorkspace, runtimeModePrompt, runtimeEffectNextTurn, rtConfirm(), rtNotice("下一回合生效"))),
	},

	// C7 上下文与输入
	"/memory": {
		Bare: rtBare(rtSpec("/memory", categoryContextInput, runtimeModeInline, runtimeEffectRead)),
		Variants: map[string]runtimeCommandSpec{
			"status": rtSpec("/memory", categoryContextInput, runtimeModeInline, runtimeEffectRead),
			"list":   rtSpec("/memory", categoryContextInput, runtimeModeInline, runtimeEffectRead),
			"search": rtSpec("/memory", categoryContextInput, runtimeModeInline, runtimeEffectRead),
			"add":    rtSpec("/memory", categoryContextInput, runtimeModePrompt, runtimeEffectNextTurn, rtConfirm(), rtNotice("下一回合生效")),
			"note":   rtSpec("/memory", categoryContextInput, runtimeModePrompt, runtimeEffectNextTurn, rtConfirm(), rtNotice("下一回合生效")),
			"flush":  rtSpec("/memory", categoryContextInput, runtimeModePrompt, runtimeEffectNextTurn, rtConfirm(), rtNotice("下一回合生效")),
		},
		Wildcard: ptrRuntimeSpec(rtSpec("/memory", categoryContextInput, runtimeModePrompt, runtimeEffectNextTurn, rtConfirm(), rtNotice("下一回合生效"))),
	},
	"/attach": {
		Bare: rtBare(rtSpec("/attach", categoryContextInput, runtimeModeInline, runtimeEffectRead)),
		Variants: map[string]runtimeCommandSpec{
			"list":   rtSpec("/attach", categoryContextInput, runtimeModeInline, runtimeEffectRead),
			"add":    rtSpec("/attach", categoryContextInput, runtimeModePrompt, runtimeEffectNextCall, rtConfirm()),
			"clear":  rtSpec("/attach", categoryContextInput, runtimeModePrompt, runtimeEffectNextCall, rtConfirm()),
			"remove": rtSpec("/attach", categoryContextInput, runtimeModePrompt, runtimeEffectNextCall, rtConfirm()),
			"paste":  rtSpec("/attach", categoryContextInput, runtimeModeQueue, runtimeEffectNextCall, rtNotice("已排队，回合结束后执行")),
		},
		Wildcard: ptrRuntimeSpec(rtSpec("/attach", categoryContextInput, runtimeModePrompt, runtimeEffectNextCall, rtConfirm())),
	},
	"/queue": {
		Bare:     rtBare(rtSpec("/queue", categoryContextInput, runtimeModeInline, runtimeEffectRead)),
		Variants: map[string]runtimeCommandSpec{"status": rtSpec("/queue", categoryContextInput, runtimeModeInline, runtimeEffectRead), "clear": rtSpec("/queue", categoryContextInput, runtimeModePrompt, runtimeEffectNextTurn, rtConfirm(), rtNotice("确认后丢弃待提交输入"))},
	},
	"/todos": {
		Bare: rtBare(rtSpec("/todos", categoryTasksTodos, runtimeModeScreen, runtimeEffectRead, rtNotice("备用屏不可用时降级为内联文档"), rtOutput(chatOutputScreenDocument))),
		Variants: map[string]runtimeCommandSpec{
			"all":    rtSpec("/todos", categoryTasksTodos, runtimeModeScreen, runtimeEffectRead, rtNotice("备用屏不可用时降级为内联文档"), rtOutput(chatOutputScreenDocument)),
			"active": rtSpec("/todos", categoryTasksTodos, runtimeModeScreen, runtimeEffectRead, rtNotice("备用屏不可用时降级为内联文档"), rtOutput(chatOutputScreenDocument)),
			"done":   rtSpec("/todos", categoryTasksTodos, runtimeModeScreen, runtimeEffectRead, rtNotice("备用屏不可用时降级为内联文档"), rtOutput(chatOutputScreenDocument)),
			"brief":  rtSpec("/todos", categoryTasksTodos, runtimeModeScreen, runtimeEffectRead, rtNotice("备用屏不可用时降级为内联文档"), rtOutput(chatOutputScreenDocument)),
		},
	},
	"/image": {Bare: rtBare(rtSpec("/image", categoryNetworkLongTasks, runtimeModeQueue, runtimeEffectSession, rtNotice("已排队，回合结束后执行")))},
	"/login": {Bare: rtBare(rtSpec("/login", categoryNetworkLongTasks, runtimeModeQueue, runtimeEffectSession, rtNotice("已排队，回合结束后执行")))},

	// C8 技能与工具
	"/functions": {Bare: rtBare(rtSpec("/functions", categorySkillsTools, runtimeModeScreen, runtimeEffectRead, rtNotice("备用屏不可用时降级为内联文档"), rtOutput(chatOutputScreenDocument)))},
	"/function": {Bare: rtBare(rtSpec("/function", categorySkillsTools, runtimeModeInline, runtimeEffectRead)),
		Wildcard: ptrRuntimeSpec(rtSpec("/function", categorySkillsTools, runtimeModeInline, runtimeEffectRead))},
	"/skills": {
		Bare: rtBare(rtSpec("/skills", categorySkillsTools, runtimeModeScreen, runtimeEffectRead, rtNotice("备用屏不可用时降级为内联列表"), rtOutput(chatOutputScreenDocument))),
		Variants: map[string]runtimeCommandSpec{
			"list":    rtSpec("/skills", categorySkillsTools, runtimeModeScreen, runtimeEffectRead, rtNotice("备用屏不可用时降级为内联列表"), rtOutput(chatOutputScreenDocument)),
			"select":  rtSpec("/skills", categorySkillsTools, runtimeModeScreen, runtimeEffectRead, rtOutput(chatOutputScreenInteractive)),
			"enable":  rtSpec("/skills", categorySkillsTools, runtimeModePrompt, runtimeEffectLive, rtConfirm(), rtNotice("热刷新，对后续请求生效")),
			"disable": rtSpec("/skills", categorySkillsTools, runtimeModePrompt, runtimeEffectLive, rtConfirm(), rtNotice("热刷新，对后续请求生效")),
			// 重载会重建 registry 与函数面：忙时排队到回合结束后执行，
			// 避免与进行中的 turn/工具面读取并发（空闲时照常立即执行）。
			"reload":  rtSpec("/skills", categorySkillsTools, runtimeModeQueue, runtimeEffectLive, rtNotice("已排队，回合结束后重扫技能目录并热刷新函数面")),
			"refresh": rtSpec("/skills", categorySkillsTools, runtimeModeQueue, runtimeEffectLive, rtNotice("已排队，回合结束后重扫技能目录并热刷新函数面")),
		},
	},
	"/mcp": {
		Bare: rtBare(rtSpec("/mcp", categorySkillsTools, runtimeModeScreen, runtimeEffectRead, rtNotice("备用屏不可用时降级为内联列表"), rtOutput(chatOutputScreenDocument))),
		Variants: map[string]runtimeCommandSpec{
			"list":    rtSpec("/mcp", categorySkillsTools, runtimeModeScreen, runtimeEffectRead, rtNotice("备用屏不可用时降级为内联列表"), rtOutput(chatOutputScreenDocument)),
			"status":  rtSpec("/mcp", categorySkillsTools, runtimeModeScreen, runtimeEffectRead, rtNotice("备用屏不可用时降级为内联列表"), rtOutput(chatOutputScreenDocument)),
			"select":  rtSpec("/mcp", categorySkillsTools, runtimeModeScreen, runtimeEffectNextCall, rtConfirm(), rtOutput(chatOutputScreenInteractive)),
			"add":     rtSpec("/mcp", categorySkillsTools, runtimeModeQueue, runtimeEffectNextCall, rtNotice("已排队，回合结束后执行")),
			"remove":  rtSpec("/mcp", categorySkillsTools, runtimeModeQueue, runtimeEffectNextCall, rtNotice("已排队，回合结束后执行")),
			"enable":  rtSpec("/mcp", categorySkillsTools, runtimeModeQueue, runtimeEffectNextCall, rtNotice("已排队，回合结束后执行")),
			"disable": rtSpec("/mcp", categorySkillsTools, runtimeModeQueue, runtimeEffectNextCall, rtNotice("已排队，回合结束后执行")),
			"reload":  rtSpec("/mcp", categorySkillsTools, runtimeModeQueue, runtimeEffectNextCall, rtNotice("已排队，回合结束后执行")),
			"auth":    rtSpec("/mcp", categorySkillsTools, runtimeModeQueue, runtimeEffectNextCall, rtNotice("已排队，回合结束后执行")),
		},
	},
	// /lsp：语言服务器池的用户侧入口（docs/lsp 02 §2.2 / 03 W7）。
	// 只读长文档（status/list/servers/diagnostics）按 §5.1 迁入副屏
	// （screen+read，正文超内联预算时开 ScreenDocument，短输出与无副屏场景
	// 内联降级）；help 是短用法卡保持 inline+read；restart/start 影响在途
	// 编辑所依赖的进程，声明为 queue+live——忙时排队到回合结束后执行
	// （手动恢复入口，见 lsp.restart_limit 语义）。
	"/lsp": {
		Bare: rtBare(rtSpec("/lsp", categorySkillsTools, runtimeModeScreen, runtimeEffectRead, rtNotice("备用屏不可用时降级为内联文档"), rtOutput(chatOutputScreenDocument))),
		Variants: map[string]runtimeCommandSpec{
			"status":      rtSpec("/lsp", categorySkillsTools, runtimeModeScreen, runtimeEffectRead, rtNotice("备用屏不可用时降级为内联文档"), rtOutput(chatOutputScreenDocument)),
			"list":        rtSpec("/lsp", categorySkillsTools, runtimeModeScreen, runtimeEffectRead, rtNotice("备用屏不可用时降级为内联文档"), rtOutput(chatOutputScreenDocument)),
			"ls":          rtSpec("/lsp", categorySkillsTools, runtimeModeScreen, runtimeEffectRead, rtNotice("备用屏不可用时降级为内联文档"), rtOutput(chatOutputScreenDocument)),
			"servers":     rtSpec("/lsp", categorySkillsTools, runtimeModeScreen, runtimeEffectRead, rtNotice("备用屏不可用时降级为内联文档"), rtOutput(chatOutputScreenDocument)),
			"show":        rtSpec("/lsp", categorySkillsTools, runtimeModeScreen, runtimeEffectRead, rtNotice("备用屏不可用时降级为内联文档"), rtOutput(chatOutputScreenDocument)),
			"diagnostics": rtSpec("/lsp", categorySkillsTools, runtimeModeScreen, runtimeEffectRead, rtNotice("备用屏不可用时降级为内联文档"), rtOutput(chatOutputScreenDocument)),
			"diag":        rtSpec("/lsp", categorySkillsTools, runtimeModeScreen, runtimeEffectRead, rtNotice("备用屏不可用时降级为内联文档"), rtOutput(chatOutputScreenDocument)),
			"check":       rtSpec("/lsp", categorySkillsTools, runtimeModeScreen, runtimeEffectRead, rtNotice("备用屏不可用时降级为内联文档"), rtOutput(chatOutputScreenDocument)),
			"baseline":    rtSpec("/lsp", categorySkillsTools, runtimeModeScreen, runtimeEffectRead, rtNotice("备用屏不可用时降级为内联文档"), rtOutput(chatOutputScreenDocument)),
			"help":        rtSpec("/lsp", categorySkillsTools, runtimeModeInline, runtimeEffectRead),
			"restart":     rtSpec("/lsp", categorySkillsTools, runtimeModeQueue, runtimeEffectLive, rtNotice("已排队，回合结束后执行")),
			"start":       rtSpec("/lsp", categorySkillsTools, runtimeModeQueue, runtimeEffectLive, rtNotice("已排队，回合结束后执行")),
		},
	},

	// C9 诊断与状态
	"/help": {Bare: rtBare(rtSpec("/help", categoryDiagnostics, runtimeModeScreen, runtimeEffectRead, rtNotice("备用屏不可用时降级为内联文档"), rtOutput(chatOutputScreenDocument)))},
	"/hotkeys": {
		Bare:     rtBare(rtSpec("/hotkeys", categoryDiagnostics, runtimeModeScreen, runtimeEffectRead, rtNotice("备用屏不可用时降级为内联文档"), rtOutput(chatOutputScreenDocument))),
		Variants: map[string]runtimeCommandSpec{"reload": rtSpec("/hotkeys", categoryDiagnostics, runtimeModePrompt, runtimeEffectNextTurn, rtConfirm(), rtNotice("写 keymap 配置，下一回合生效"))},
	},
	"/status": {Bare: rtBare(rtSpec("/status", categoryDiagnostics, runtimeModeScreen, runtimeEffectRead, rtNotice("备用屏不可用时降级为内联文档"), rtOutput(chatOutputScreenDocument)))},
	"/usage":  {Bare: rtBare(rtSpec("/usage", categoryDiagnostics, runtimeModeScreen, runtimeEffectRead, rtNotice("备用屏不可用时降级为内联文档"), rtOutput(chatOutputScreenDocument)))},
	"/debug": {
		Bare: rtBare(rtSpec("/debug", categoryDiagnostics, runtimeModeInline, runtimeEffectRead)),
		Variants: map[string]runtimeCommandSpec{
			"status":  rtSpec("/debug", categoryDiagnostics, runtimeModeInline, runtimeEffectRead),
			"routing": rtSpec("/debug", categoryDiagnostics, runtimeModeInline, runtimeEffectRead),
			"state":   rtSpec("/debug", categoryDiagnostics, runtimeModeInline, runtimeEffectRead),
			"display": rtSpec("/debug", categoryDiagnostics, runtimeModeScreen, runtimeEffectRead, rtNotice("备用屏不可用时降级为内联文档"), rtOutput(chatOutputScreenDocument)),
			"on":      rtSpec("/debug", categoryDiagnostics, runtimeModePrompt, runtimeEffectNextTurn, rtConfirm(), rtNotice("下一回合生效")),
			"off":     rtSpec("/debug", categoryDiagnostics, runtimeModePrompt, runtimeEffectNextTurn, rtConfirm(), rtNotice("下一回合生效")),
			"export":  rtSpec("/debug", categoryDiagnostics, runtimeModeQueue, runtimeEffectNextTurn, rtNotice("已排队，回合结束后执行")),
		},
	},
	"/supervision": {
		Bare: rtBare(rtSpec("/supervision", categoryDiagnostics, runtimeModeInline, runtimeEffectRead)),
		Variants: map[string]runtimeCommandSpec{
			"status":  rtSpec("/supervision", categoryDiagnostics, runtimeModeInline, runtimeEffectRead),
			"ack":     rtSpec("/supervision", categoryDiagnostics, runtimeModePrompt, runtimeEffectLive, rtConfirm(), rtNotice("控制面写立即持久化")),
			"resolve": rtSpec("/supervision", categoryDiagnostics, runtimeModePrompt, runtimeEffectLive, rtConfirm(), rtNotice("控制面写立即持久化")),
			"control": rtSpec("/supervision", categoryDiagnostics, runtimeModePrompt, runtimeEffectLive, rtConfirm(), rtNotice("控制面写立即持久化")),
			"wake":    rtSpec("/supervision", categoryDiagnostics, runtimeModeQueue, runtimeEffectLive, rtNotice("已排队，回合结束后执行")),
			"deliver": rtSpec("/supervision", categoryDiagnostics, runtimeModeQueue, runtimeEffectLive, rtNotice("已排队，回合结束后执行")),
		},
	},
	"/agents": {
		// bare /agents 在 unified 交互出口是 A 族列表副屏（可选 agent 查看输出）；
		// 能力不足/忙/嵌套时按框架契约降级为主屏内联文档（DegradeDoc 为
		// 既有 "Agent Graph:" 文本）。Output 为 screen-interactive：忙时经
		// 只读交互列表白名单（chatBusyScreenInteractiveCommands）走 S 档，
		// 不再被降级入队。
		Bare: rtBare(rtSpec("/agents", categoryDiagnostics, runtimeModeScreen, runtimeEffectRead, rtNotice("备用屏不可用时降级为内联文档"), rtOutput(chatOutputScreenInteractive))),
		Variants: map[string]runtimeCommandSpec{
			"status":   rtSpec("/agents", categoryDiagnostics, runtimeModeInline, runtimeEffectRead),
			"list":     rtSpec("/agents", categoryDiagnostics, runtimeModeInline, runtimeEffectRead),
			"pick":     rtSpec("/agents", categoryDiagnostics, runtimeModeScreen, runtimeEffectLive, rtConfirm(), rtOutput(chatOutputScreenInteractive)),
			"panel":    rtSpec("/agents", categoryDiagnostics, runtimeModeScreen, runtimeEffectLive, rtConfirm(), rtOutput(chatOutputScreenInteractive)),
			"approve":  rtSpec("/agents", categoryDiagnostics, runtimeModePrompt, runtimeEffectLive, rtConfirm()),
			"deny":     rtSpec("/agents", categoryDiagnostics, runtimeModePrompt, runtimeEffectLive, rtConfirm()),
			"answer":   rtSpec("/agents", categoryDiagnostics, runtimeModePrompt, runtimeEffectLive, rtConfirm()),
			"send":     rtSpec("/agents", categoryDiagnostics, runtimeModePrompt, runtimeEffectLive, rtConfirm()),
			"followup": rtSpec("/agents", categoryDiagnostics, runtimeModePrompt, runtimeEffectLive, rtConfirm()),
			"cleanup":  rtSpec("/agents", categoryDiagnostics, runtimeModePrompt, runtimeEffectLive, rtConfirm()),
		},
	},
	"/timeline": {Bare: rtBare(rtSpec("/timeline", categoryDiagnostics, runtimeModeScreen, runtimeEffectRead, rtNotice("备用屏不可用时降级为内联文档"), rtOutput(chatOutputScreenDocument)))},
	"/collab":   {Bare: rtBare(rtSpec("/collab", categoryDiagnostics, runtimeModeScreen, runtimeEffectRead, rtNotice("备用屏不可用时降级为内联文档"), rtOutput(chatOutputScreenDocument)))},
	"/web": {
		Bare: rtBare(rtSpec("/web", categoryDiagnostics, runtimeModeInline, runtimeEffectRead)),
		Variants: map[string]runtimeCommandSpec{
			"status":    rtSpec("/web", categoryDiagnostics, runtimeModeInline, runtimeEffectRead),
			"token":     rtSpec("/web", categoryDiagnostics, runtimeModeInline, runtimeEffectRead),
			"endpoints": rtSpec("/web", categoryDiagnostics, runtimeModeScreen, runtimeEffectRead, rtNotice("备用屏不可用时降级为内联文档"), rtOutput(chatOutputScreenDocument)),
			"open":      rtSpec("/web", categoryDiagnostics, runtimeModeQueue, runtimeEffectSession, rtNotice("已排队，回合结束后执行")),
		},
	},

	// C10 输出与外观
	"/stream": {
		Bare: rtBare(rtSpec("/stream", categoryOutputAppearance, runtimeModeInline, runtimeEffectRead)),
		Variants: map[string]runtimeCommandSpec{
			"status": rtSpec("/stream", categoryOutputAppearance, runtimeModeInline, runtimeEffectRead),
			"on":     rtSpec("/stream", categoryOutputAppearance, runtimeModePrompt, runtimeEffectLive, rtConfirm(), rtNotice("显示面立即生效")),
			"off":    rtSpec("/stream", categoryOutputAppearance, runtimeModePrompt, runtimeEffectLive, rtConfirm(), rtNotice("显示面立即生效")),
		},
	},
	"/fast": {
		Bare: rtBare(rtSpec("/fast", categoryOutputAppearance, runtimeModeInline, runtimeEffectRead)),
		Variants: map[string]runtimeCommandSpec{
			"status": rtSpec("/fast", categoryOutputAppearance, runtimeModeInline, runtimeEffectRead),
			"on":     rtSpec("/fast", categoryOutputAppearance, runtimeModePrompt, runtimeEffectLive, rtConfirm(), rtNotice("立即生效并持久化")),
			"off":    rtSpec("/fast", categoryOutputAppearance, runtimeModePrompt, runtimeEffectLive, rtConfirm(), rtNotice("立即生效并持久化")),
		},
	},
	"/reasoning": {
		Bare: rtBare(rtSpec("/reasoning", categoryOutputAppearance, runtimeModeInline, runtimeEffectRead)),
		Variants: map[string]runtimeCommandSpec{
			"status": rtSpec("/reasoning", categoryOutputAppearance, runtimeModeInline, runtimeEffectRead),
			"on":     rtSpec("/reasoning", categoryOutputAppearance, runtimeModePrompt, runtimeEffectLive, rtConfirm(), rtNotice("显示面立即生效")),
			"off":    rtSpec("/reasoning", categoryOutputAppearance, runtimeModePrompt, runtimeEffectLive, rtConfirm(), rtNotice("显示面立即生效")),
		},
	},
	"/reasoning_effort": {
		Bare: rtBare(rtSpec("/reasoning_effort", categoryOutputAppearance, runtimeModeInline, runtimeEffectRead)),
		Variants: map[string]runtimeCommandSpec{
			"status": rtSpec("/reasoning_effort", categoryOutputAppearance, runtimeModeInline, runtimeEffectRead),
			"select": rtSpec("/reasoning_effort", categoryOutputAppearance, runtimeModeScreen, runtimeEffectNextTurn, rtConfirm(), rtNotice("下一回合生效"), rtOutput(chatOutputScreenInteractive)),
			"set":    rtSpec("/reasoning_effort", categoryOutputAppearance, runtimeModePrompt, runtimeEffectNextTurn, rtConfirm(), rtNotice("下一回合生效")),
		},
		Wildcard: ptrRuntimeSpec(rtSpec("/reasoning_effort", categoryOutputAppearance, runtimeModePrompt, runtimeEffectNextTurn, rtConfirm(), rtNotice("下一回合生效"))),
	},
	"/theme": {
		Bare: rtBare(rtSpec("/theme", categoryOutputAppearance, runtimeModeInline, runtimeEffectRead)),
		Variants: map[string]runtimeCommandSpec{
			"status":  rtSpec("/theme", categoryOutputAppearance, runtimeModeScreen, runtimeEffectRead, rtNotice("备用屏不可用时降级为内联文档"), rtOutput(chatOutputScreenDocument)),
			"list":    rtSpec("/theme", categoryOutputAppearance, runtimeModeScreen, runtimeEffectRead, rtNotice("备用屏不可用时降级为内联文档"), rtOutput(chatOutputScreenDocument)),
			"preview": rtSpec("/theme", categoryOutputAppearance, runtimeModeScreen, runtimeEffectRead, rtNotice("备用屏不可用时降级为内联文档"), rtOutput(chatOutputScreenDocument)),
			"select":  rtSpec("/theme", categoryOutputAppearance, runtimeModeScreen, runtimeEffectLive, rtConfirm(), rtNotice("视觉立即生效"), rtOutput(chatOutputScreenInteractive)),
			"set":     rtSpec("/theme", categoryOutputAppearance, runtimeModeScreen, runtimeEffectLive, rtConfirm(), rtNotice("视觉立即生效"), rtOutput(chatOutputScreenInteractive)),
		},
	},
	"/s":      {Bare: rtBare(rtSpec("/s", categoryOutputAppearance, runtimeModePrompt, runtimeEffectLive, rtConfirm(), rtNotice("显示面立即生效")))},
	"/normal": {Bare: rtBare(rtSpec("/normal", categoryOutputAppearance, runtimeModePrompt, runtimeEffectLive, rtConfirm(), rtNotice("显示面立即生效")))},

	// C11 网络与长任务（固定 queue）
	// /account 裸形式是「刷新当前 provider」的网络长任务（P1 队列契约不变），
	// 批次 1 迁入副屏的是 show/--no-refresh 两个只读快照变体；批次 4 起这两个
	// 变体走忙时副屏通道。
	"/account": {
		// 裸形式 = refresh（网络长任务）：显式登记 Bare 让目录全集都能解析到
		// 类别声明（T11），忙时行为与注册表兜底（queue）逐字一致。
		Bare: rtBare(rtSpec("/account", categoryNetworkLongTasks, runtimeModeQueue, runtimeEffectRead, rtNotice("已排队，回合结束后执行"))),
		Variants: map[string]runtimeCommandSpec{
			"show":         rtSpec("/account", categoryNetworkLongTasks, runtimeModeScreen, runtimeEffectRead, rtNotice("备用屏不可用时降级为内联文档"), rtOutput(chatOutputScreenDocument)),
			"--no-refresh": rtSpec("/account", categoryNetworkLongTasks, runtimeModeScreen, runtimeEffectRead, rtNotice("备用屏不可用时降级为内联文档"), rtOutput(chatOutputScreenDocument)),
			"refresh":      rtSpec("/account", categoryNetworkLongTasks, runtimeModeQueue, runtimeEffectRead, rtNotice("已排队，回合结束后执行")),
		},
	},
	// /accounts 默认路径立即读缓存（chat_account_async_test.go 锁定），属只读快照
	// → 批次 4 声明为 screen；refresh 仍是队列。
	"/accounts": {
		Bare: rtBare(rtSpec("/accounts", categoryNetworkLongTasks, runtimeModeScreen, runtimeEffectRead, rtNotice("备用屏不可用时降级为内联文档"), rtOutput(chatOutputScreenDocument))),
		Variants: map[string]runtimeCommandSpec{
			"display": rtSpec("/accounts", categoryNetworkLongTasks, runtimeModeScreen, runtimeEffectRead, rtNotice("备用屏不可用时降级为内联文档"), rtOutput(chatOutputScreenDocument)),
			"refresh": rtSpec("/accounts", categoryNetworkLongTasks, runtimeModeQueue, runtimeEffectRead, rtNotice("已排队，回合结束后执行")),
		},
	},
}

func ptrRuntimeSpec(spec runtimeCommandSpec) *runtimeCommandSpec { return &spec }

// resolveRuntimeCommandSpec 把一行输入解析到注册表声明（§3.8.4 宿主第一步）。
// ok=false 表示未登记命令或未知子命令——调用方必须 enqueue（INV-6，fail-safe）；
// 此时返回的 spec.Mode 恒为 queue。别名与 legacy 入口先归并到主命令。
func resolveRuntimeCommandSpec(line string) (runtimeCommandSpec, bool) {
	name, args, ok := chatBusyCommandNameAndArgs(line)
	if !ok {
		return runtimeQueueFallbackSpec(""), false
	}
	canonical := canonicalRuntimeCommandName(name)
	args = runtimeLegacyAliasArgs(name, args)
	if entry, found := runtimeCommandRegistry[canonical]; found {
		if spec, matched := matchRuntimeCommandEntry(entry, args); matched {
			return runtimeSpecWithSwitchKey(spec), true
		}
	}
	return runtimeQueueFallbackSpec(canonical), false
}

func matchRuntimeCommandEntry(entry runtimeCommandEntry, args []string) (runtimeCommandSpec, bool) {
	if len(args) == 0 {
		if entry.Bare != nil {
			return *entry.Bare, true
		}
		if entry.Wildcard != nil {
			return *entry.Wildcard, true
		}
		return runtimeCommandSpec{}, false
	}
	token := strings.ToLower(strings.TrimSpace(args[0]))
	if entry.Variants != nil {
		if spec, ok := entry.Variants[token]; ok {
			return spec, true
		}
	}
	if entry.Wildcard != nil {
		return *entry.Wildcard, true
	}
	return runtimeCommandSpec{}, false
}

func runtimeSpecWithSwitchKey(spec runtimeCommandSpec) runtimeCommandSpec {
	if spec.SwitchKey == "" {
		spec.SwitchKey = runtimeCommandSwitchKey(spec.Command)
	}
	return spec
}

func runtimeQueueFallbackSpec(command string) runtimeCommandSpec {
	return runtimeSpecWithSwitchKey(runtimeCommandSpec{Command: command, Mode: runtimeModeQueue, Effect: runtimeEffectRead})
}

// runtimeCommandRegistryViolations 报告注册表违反 §3.8.1 不变式的条目：
// effect 为 session/process 时 mode 只允许 queue/block（T28 静态断言，宿主层
// 还会在执行前再次拒绝）。
func runtimeCommandRegistryViolations() []string {
	var violations []string
	check := func(spec runtimeCommandSpec) {
		if spec.Effect != runtimeEffectSession && spec.Effect != runtimeEffectProcess {
			return
		}
		if spec.Mode == runtimeModeQueue || spec.Mode == runtimeModeBlock {
			return
		}
		violations = append(violations, spec.Command+" mode="+spec.Mode.String()+" effect="+spec.Effect.String())
	}
	for _, entry := range runtimeCommandRegistry {
		if entry.Bare != nil {
			check(*entry.Bare)
		}
		if entry.Wildcard != nil {
			check(*entry.Wildcard)
		}
		for _, spec := range entry.Variants {
			check(spec)
		}
	}
	sort.Strings(violations)
	return violations
}
