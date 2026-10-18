// Package toolregistry 是内置工具身份的单一真源（single source of truth）。
//
// 背景：工具名知识此前散落在 11 处，每新增一个工具都要同步多处，漂移不可避免：
//
//	agent/tool_vocabulary.go      规范名常量 + 退役名建议
//	policy/capability.go          normalizeToolName 别名归一表
//	policy/tool_policy.go         essential / delegation 名单
//	policy/tool_policy.go         write-like / shell-like 子串判定
//	policy/plan_paths.go          plan-write 名单
//	policy/specifier.go           分组表 + read/edit/webfetch 集合
//	internal/toolnames            图像工具名
//	internal/tools/manager.go     真实注册名单
//	internal/aiclitools           暴露面注册
//
// 本包把它们收敛到一处。施工单见 docs/tools/unified-tool-registry-plan.md。
//
// 职责边界：
//   - 本包回答"这个工具的**身份**是什么"：规范名、别名、分组、暴露面、
//     控制面必需性，以及 write-like / shell-like / delegation / plan-write 类判定。
//   - 本包**不**回答"工具怎么执行"（internal/toolkit）
//     或"这次装配暴露了哪些工具"（internal/toolsurface）。
//
// 两条刻意的设计约束：
//
//  1. 叶子包：只依赖标准库。这样 policy / agent / tools / toolsurface / runtimeapi
//     都能 import 而不成环（对齐 internal/toolsurface 的既有策略）。
//
//  2. **不重复持有 name/description 全量名单**。名称与描述由 toolkit 在执行期派生
//     （toolkit.BaseTool），本包只补充不可派生的维度。若本包再抄一份名单，就等于
//     制造"第二份真源"——正是要消灭的病。本包与真实注册表的对账由
//     registry_contract_test.go 钉死。
package toolregistry

import "sort"

// Group 是工具的功能分组（profile.tools.disabled_groups 的取值域）。
//
// 组词汇沿用 docs/tools/profile-tool-groups-design.md §3.3 的定稿，
// 仅新增 control 一组承载"运行时必需的控制面工具"（todos / goal 等），
// 它们不属于 broker 编排面，但同样不可被 allowlist 裁剪。
type Group string

const (
	GroupRead          Group = "read"
	GroupEdit          Group = "edit"
	GroupShell         Group = "shell"
	GroupWebFetch      Group = "webfetch"
	GroupSearch        Group = "search"
	GroupIO            Group = "io"
	GroupOrchestration Group = "orchestration"
	GroupControl       Group = "control"
	GroupMesh          Group = "mesh"
	GroupLSP           Group = "lsp"
)

// Exposure 是工具在哪个宿主面上暴露。
//
// 取值域对齐 internal/aiclitools 的 ExposurePath（既有实现）。本包只做**声明**；
// 真正的暴露面门控仍由 aiclitools.Registry 执行，直到 docs/architecture/
// unified-tool-surface-registry.md §5 阶段 2 的 Builder 收敛落地。
type Exposure string

const (
	ExposureShared        Exposure = "shared"
	ExposureActor         Exposure = "actor"
	ExposureRuntimeServer Exposure = "runtime_server"
	ExposureTUILocal      Exposure = "tui_local"
)

// Entry 描述一个内置工具的身份。
//
// 零值 Group 表示"未登记"，Lookup 会返回 ok=false，调用方据此回退到
// 启发式判定（见 predicates.go），从而对 MCP / 第三方工具保持原有语义。
type Entry struct {
	// Name 是规范名（模型可见名）。
	Name string
	// Aliases 是**现役**兼容别名：已真实注册、执行语义与规范名相同。
	// 例：view←read、grep←rg、shell←bash（见 tools/manager.go 的注册注释）。
	Aliases []string
	// Group 是功能分组。
	Group Group
	// Exposure 是该工具允许出现的宿主面。
	Exposure []Exposure
	// Essential 为真表示"运行时自有工具"（会话控制、计划、协作、目录检索等）。
	// 它**不再**是 allowlist 豁免：自 2026-10-16 起 profile 必须能完全控制工具
	// 面，窄 allowlist 下未列出的 essential 工具同样被裁剪，需要用通配模式
	// （toolregistry.MatchPattern，如 "subagent_*"）或显式名单放回来。字段保留
	// 供观测面（toolsurface metadata.essential）、目录 API 与测试引用。
	Essential bool
	// Contractual 表示该工具是某条运行时契约的组成部分（当前仅
	// artifact_read：输出网关写进模型上下文的 `art_…` 指针必须有读取端）。
	// 契约性工具由运行时按契约授予，不要求 profile 在 allowlist 里逐名列出；
	// 但显式 deny 恒优先，仍然可以关闭（此时渲染层必须降级指针文案）。
	//
	// 与 Essential 的区别：Essential 不影响可用性；Contractual 影响可用性，
	// 但授权来源是契约而不是用户名单。
	Contractual bool
	// ToolkitBuiltin 为真表示该内置工具的**定义**由 internal/toolkit/tools 提供，
	// 且必须由 toolkit 装配点（internal/tools.registerBuiltinToolkitTools 的
	// builtinToolkitRegistrations 构造表）注册进运行时注册表。
	//
	// 零值 false = 本包**不**对"由谁提供"作声明：broker（编排面）、capability
	// （get_goal 等）、以及 agent 侧按需注册的 search_tool 都留零值，调用方据此
	// 保持"不认识"的语义，不猜。（ADR-0004 的 code.* 由 CodeTool 单独声明。）
	//
	// 为什么需要这一维：注册表知道"有哪些工具"，但**不**写"怎么构造"（构造参数
	// 由宿主注入）。没有这个正向声明，"注册表登记了却没人注册"（工具对模型不可见）
	// 与"注册了却不在注册表"（落回启发式判定）两种漂移都无法用测试钉死——前者正是
	// P1 修掉的那类真红 bug（声明面与请求面不一致）。
	ToolkitBuiltin bool
	// CodeTool 为真表示该工具的注册由 ADR-0004 的 code gate
	//（internal/tools.codeToolGate）按索引档位条件增删，"有哪些"与"怎么造"的
	// 对账面是 internal/tools.codeToolRegistrations；两侧由
	// internal/tools/builtin_registry_contract_test.go 双向钉死。
	//
	// 与 ToolkitBuiltin 互斥：code.* 不进 builtinToolkitRegistrations，因为它们的
	// 注册时机由档位（all / definitions / none）决定，不是装配期一次注册。
	CodeTool bool
	// WriteLike / ShellLike / Delegation / PlanWrite 是类判定的事实声明。
	// 对未登记的名字，predicates.go 退回原有子串启发式，语义不变。
	WriteLike  bool
	ShellLike  bool
	Delegation bool
	PlanWrite  bool
}

// sharedExposure 是内置工具的默认暴露面。
var sharedExposure = []Exposure{ExposureShared}

// controlPlaneExposure 是编排面工具的三面暴露（对齐架构文档 §4.1 阶段 1）。
var controlPlaneExposure = []Exposure{ExposureShared, ExposureActor, ExposureRuntimeServer}

// tuiLocalExposure 是仅存在于 aicli TUI 本地直连面的工具
// （架构文档 §3 非目标：plan_* / ask_user_question 不搬进 runtime-server）。
var tuiLocalExposure = []Exposure{ExposureTUILocal}

// builtinEntries 是内置工具身份表。
//
// 顺序不承载语义（对外一律按 Name 排序输出），仅按分组聚拢便于人工审阅。
// 每一条都必须能通过 registry_contract_test.go 与 toolkit 真实注册表对账。
var builtinEntries = []Entry{
	// ---- 命令执行（shell 组）----
	{Name: "shell", Aliases: []string{"bash", "execute_shell_command"}, Group: GroupShell, Exposure: sharedExposure, ShellLike: true, ToolkitBuiltin: true},
	{Name: "aicli_exec", Group: GroupShell, Exposure: sharedExposure, ShellLike: true, ToolkitBuiltin: true},

	// ---- 读取（read 组）----
	{Name: "view", Aliases: []string{"read"}, Group: GroupRead, Exposure: sharedExposure, ToolkitBuiltin: true},
	{Name: "glob", Group: GroupRead, Exposure: sharedExposure, ToolkitBuiltin: true},
	{Name: "grep", Aliases: []string{"rg"}, Group: GroupRead, Exposure: sharedExposure, ToolkitBuiltin: true},
	{Name: "ls", Group: GroupRead, Exposure: sharedExposure, ToolkitBuiltin: true},

	// ---- 写入（edit 组，全部受 plan-write 豁免约束）----
	{Name: "edit", Group: GroupEdit, Exposure: sharedExposure, WriteLike: true, PlanWrite: true, ToolkitBuiltin: true},
	{Name: "write", Group: GroupEdit, Exposure: sharedExposure, WriteLike: true, PlanWrite: true, ToolkitBuiltin: true},
	{Name: "append_write", Group: GroupEdit, Exposure: sharedExposure, WriteLike: true, PlanWrite: true, ToolkitBuiltin: true},
	{Name: "multiedit", Group: GroupEdit, Exposure: sharedExposure, WriteLike: true, PlanWrite: true, ToolkitBuiltin: true},
	{Name: "apply_patch", Group: GroupEdit, Exposure: sharedExposure, WriteLike: true, PlanWrite: true, ToolkitBuiltin: true},

	// ---- 网络取回（webfetch 组；download 落盘故同时是 write-like）----
	{Name: "fetch", Group: GroupWebFetch, Exposure: sharedExposure, ToolkitBuiltin: true},
	{Name: "download", Group: GroupWebFetch, Exposure: sharedExposure, WriteLike: true, ToolkitBuiltin: true},

	// ---- 检索（search 组）----
	{Name: "web_search", Group: GroupSearch, Exposure: sharedExposure, ToolkitBuiltin: true},
	{Name: "sourcegraph", Group: GroupSearch, Exposure: sharedExposure, ToolkitBuiltin: true},
	{Name: "search_tool", Group: GroupSearch, Exposure: sharedExposure, Essential: true},

	// ---- 产物与 IO（io 组）----
	{Name: "artifact_read", Group: GroupIO, Exposure: sharedExposure, Contractual: true, ToolkitBuiltin: true},
	{Name: "openai_image_generate", Aliases: []string{"image_generate"}, Group: GroupIO, Exposure: sharedExposure, ToolkitBuiltin: true},

	// ---- 符号索引（code.*，由 code gate 按档位条件注册；随 grep 语义归 read 组）----
	{Name: "code_search", Group: GroupRead, Exposure: sharedExposure, CodeTool: true},
	{Name: "code_inspect", Group: GroupRead, Exposure: sharedExposure, CodeTool: true},
	{Name: "code_navigate", Group: GroupRead, Exposure: sharedExposure, CodeTool: true},
	{Name: "code_references", Group: GroupRead, Exposure: sharedExposure, CodeTool: true},
	{Name: "code_callers", Group: GroupRead, Exposure: sharedExposure, CodeTool: true},

	// ---- 控制面（control 组；运行时自有工具，allowlist 可裁剪，见 Entry.Essential 注释）----
	{Name: "todos", Group: GroupControl, Exposure: sharedExposure, Essential: true, ToolkitBuiltin: true},
	{Name: "get_goal", Group: GroupControl, Exposure: sharedExposure, Essential: true},
	{Name: "read_goal", Group: GroupControl, Exposure: sharedExposure, Essential: true},
	{Name: "update_goal", Group: GroupControl, Exposure: sharedExposure, Essential: true},

	// ---- 编排面（orchestration 组；由 broker 在运行期暴露）----
	//
	// 规范名**必须**等于 broker 的定义名（broker.go 的 Tool* 常量），即
	// internal/toolbroker/tool_naming_contract_test.go 钉死的
	// `<domain>_<verb>` 契约（域闭集 {job,schedule,team,subagent,plan}）。
	// 这里此前误用 policy 口径（background_task / spawn_agent / ...）当规范名，
	// 与 broker 契约冲突——同一个工具出现两个"规范名"正是漂移的来源。
	// 旧拼写一律只作兼容别名（可调用、不对外暴露）。
	{Name: "ask_user_question", Group: GroupOrchestration, Exposure: tuiLocalExposure, Essential: true},
	{Name: "plan_enter", Group: GroupOrchestration, Exposure: tuiLocalExposure, Essential: true},
	{Name: "plan_exit", Group: GroupOrchestration, Exposure: tuiLocalExposure, Essential: true},
	{Name: "plan_review", Group: GroupOrchestration, Exposure: tuiLocalExposure, Essential: true},

	// job 家族（命名契约 B1）
	{Name: "job_start", Aliases: []string{"background_task"}, Group: GroupOrchestration, Exposure: controlPlaneExposure, Essential: true},
	{Name: "job_output", Aliases: []string{"task_output"}, Group: GroupOrchestration, Exposure: controlPlaneExposure, Essential: true},
	{Name: "job_list", Group: GroupOrchestration, Exposure: controlPlaneExposure, Essential: true},
	{Name: "job_search", Group: GroupOrchestration, Exposure: controlPlaneExposure, Essential: true},
	// 注意：下面四个**不是** essential。原 IsRuntimeOwnedEssentialTool 不含它们，
	// 它们只在 AllowTool 的 read-only 分支被显式拦截。（Essential 自 2026-10-16
	// 起不再是 allowlist 豁免，此处注释仅描述历史差异。）
	{Name: "job_cancel", Aliases: []string{"task_kill"}, Group: GroupOrchestration, Exposure: controlPlaneExposure},
	{Name: "job_monitor", Aliases: []string{"task_monitor"}, Group: GroupOrchestration, Exposure: controlPlaneExposure},
	{Name: "job_control", Aliases: []string{"task_control"}, Group: GroupOrchestration, Exposure: controlPlaneExposure},
	{Name: "job_input", Aliases: []string{"write_stdin", "task_input"}, Group: GroupOrchestration, Exposure: controlPlaneExposure},

	// schedule 家族（提醒；契约域之一，非控制面必需）
	{Name: "schedule_create", Group: GroupOrchestration, Exposure: controlPlaneExposure},
	{Name: "schedule_list", Group: GroupOrchestration, Exposure: controlPlaneExposure},
	{Name: "schedule_update", Group: GroupOrchestration, Exposure: controlPlaneExposure},
	{Name: "schedule_delete", Group: GroupOrchestration, Exposure: controlPlaneExposure},

	// subagent 家族（命名契约 B3）
	{Name: "subagent_spawn", Aliases: []string{"spawn_agent"}, Group: GroupOrchestration, Exposure: controlPlaneExposure, Essential: true, Delegation: true},
	{Name: "subagent_spawn_batch", Aliases: []string{"spawn_subagents"}, Group: GroupOrchestration, Exposure: controlPlaneExposure, Essential: true, Delegation: true},
	{Name: "subagent_list", Aliases: []string{"list_agents"}, Group: GroupOrchestration, Exposure: controlPlaneExposure, Essential: true},
	{Name: "subagent_wait", Aliases: []string{"wait_agent"}, Group: GroupOrchestration, Exposure: controlPlaneExposure, Essential: true},
	{Name: "subagent_events", Aliases: []string{"read_agent_events"}, Group: GroupOrchestration, Exposure: controlPlaneExposure, Essential: true},
	{Name: "subagent_send", Aliases: []string{"send_message"}, Group: GroupOrchestration, Exposure: controlPlaneExposure, Essential: true},
	{Name: "subagent_input", Aliases: []string{"send_input"}, Group: GroupOrchestration, Exposure: controlPlaneExposure, Essential: true},
	{Name: "subagent_followup", Aliases: []string{"followup_task"}, Group: GroupOrchestration, Exposure: controlPlaneExposure, Essential: true},
	{Name: "subagent_close", Aliases: []string{"close_agent"}, Group: GroupOrchestration, Exposure: controlPlaneExposure, Essential: true},
	{Name: "subagent_resume", Aliases: []string{"resume_agent"}, Group: GroupOrchestration, Exposure: controlPlaneExposure, Essential: true},
	{Name: "subagent_apply_worktree", Aliases: []string{"apply_agent_worktree"}, Group: GroupOrchestration, Exposure: controlPlaneExposure, Essential: true},
	{Name: "subagent_discard_worktree", Aliases: []string{"discard_agent_worktree"}, Group: GroupOrchestration, Exposure: controlPlaneExposure, Essential: true},
	{Name: "subagent_approve", Aliases: []string{"resolve_agent_approval"}, Group: GroupOrchestration, Exposure: controlPlaneExposure, Essential: true},
	{Name: "subagent_status", Aliases: []string{"agent_status"}, Group: GroupOrchestration, Exposure: controlPlaneExposure, Essential: true},
	{Name: "subagent_inspect_task", Aliases: []string{"subagent_inspect"}, Group: GroupOrchestration, Exposure: controlPlaneExposure, Essential: true},
	{Name: "subagent_ack_lifecycle", Aliases: []string{"ack_lifecycle"}, Group: GroupOrchestration, Exposure: controlPlaneExposure, Essential: true},
	{Name: "subagent_control", Aliases: []string{"control_descendant"}, Group: GroupOrchestration, Exposure: controlPlaneExposure, Essential: true},
	// supervision_* / read_agent_result 的规范名即其自身（broker 定义名同形）。
	{Name: "read_agent_result", Group: GroupOrchestration, Exposure: controlPlaneExposure, Essential: true},
	{Name: "supervision_snapshot", Group: GroupOrchestration, Exposure: controlPlaneExposure, Essential: true},
	{Name: "supervision_descendants", Group: GroupOrchestration, Exposure: controlPlaneExposure, Essential: true},

	// team 家族（命名契约 B2）
	{Name: "team_spawn", Aliases: []string{"spawn_team"}, Group: GroupOrchestration, Exposure: controlPlaneExposure, Essential: true, Delegation: true},
	{Name: "team_wait", Aliases: []string{"wait_team"}, Group: GroupOrchestration, Exposure: controlPlaneExposure, Essential: true},
	{Name: "team_send", Aliases: []string{"send_team_message"}, Group: GroupOrchestration, Exposure: controlPlaneExposure, Essential: true},
	{Name: "team_mailbox", Aliases: []string{"read_mailbox_digest"}, Group: GroupOrchestration, Exposure: controlPlaneExposure, Essential: true},
	{Name: "team_task_spec", Aliases: []string{"read_task_spec"}, Group: GroupOrchestration, Exposure: controlPlaneExposure, Essential: true},
	{Name: "team_task_context", Aliases: []string{"read_task_context"}, Group: GroupOrchestration, Exposure: controlPlaneExposure, Essential: true},
	{Name: "team_task_report", Aliases: []string{"report_task_outcome"}, Group: GroupOrchestration, Exposure: controlPlaneExposure, Essential: true},
	{Name: "team_task_block", Aliases: []string{"block_current_task"}, Group: GroupOrchestration, Exposure: controlPlaneExposure, Essential: true},
}

// GroupInfo 是分组目录条目，供 /api/runtime/tools 的 groups[] 与前端
// profile 编辑器渲染"可裁剪的组"清单。
type GroupInfo struct {
	ID    Group
	Label string
	// Protected 为真的组不可被 profile 整组关闭
	// （对齐 profile-tool-groups-design.md §3.3：read/edit/shell 是工作区读写的基本盘）。
	Protected bool
}

// groupCatalog 是分组目录（顺序即展示顺序）。
var groupCatalog = []GroupInfo{
	{ID: GroupRead, Label: "读取", Protected: true},
	{ID: GroupEdit, Label: "编辑", Protected: true},
	{ID: GroupShell, Label: "命令执行", Protected: true},
	{ID: GroupWebFetch, Label: "网络取回", Protected: false},
	{ID: GroupSearch, Label: "检索", Protected: false},
	{ID: GroupIO, Label: "产物与资源", Protected: false},
	{ID: GroupOrchestration, Label: "编排面", Protected: false},
	{ID: GroupControl, Label: "控制面", Protected: true},
	{ID: GroupMesh, Label: "Mesh", Protected: false},
	{ID: GroupLSP, Label: "语言服务", Protected: false},
}

// byCanonical / byAlias 是 builtinEntries 的倒排索引，init 期构建一次。
var (
	byCanonical = map[string]Entry{}
	byAlias     = map[string]string{}
)

func init() {
	for _, entry := range builtinEntries {
		byCanonical[entry.Name] = entry
		for _, alias := range entry.Aliases {
			byAlias[alias] = entry.Name
		}
	}
}

// Entries 返回全部内置工具条目，按规范名排序。
func Entries() []Entry {
	out := make([]Entry, len(builtinEntries))
	copy(out, builtinEntries)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ContractualNames returns the normalized names of tools declared Contractual.
// Policy derivation reads this single list; do not duplicate it elsewhere.
func ContractualNames() []string {
	names := make([]string, 0, 2)
	for _, entry := range builtinEntries {
		if entry.Contractual {
			names = append(names, Normalize(entry.Name))
		}
	}
	sort.Strings(names)
	return names
}

// Lookup 按名字（规范名或别名，均先经 Normalize）查条目。
func Lookup(name string) (Entry, bool) {
	canonical := Canonical(name)
	entry, ok := byCanonical[canonical]
	return entry, ok
}

// Canonical 把任意别名归一到规范名。
//
// 解析顺序：归一化 → 规范名直命 → 现役别名 → 退役名（文档化替代）。
// 未登记的名字原样返回（**不**猜测），调用方据此走启发式回退。
func Canonical(name string) string {
	normalized := Normalize(name)
	if normalized == "" {
		return ""
	}
	if _, ok := byCanonical[normalized]; ok {
		return normalized
	}
	if canonical, ok := byAlias[normalized]; ok {
		return canonical
	}
	if canonical, ok := replacedNames[normalized]; ok {
		return canonical
	}
	return normalized
}

// CallableNames 把每个请求的工具名展开成调用方可能使用的**全部现役拼写**：
// 规范名 + Entry.Aliases。
//
// 刻意不展开退役名（replacedNames）：它们已不可被请求，不需要出现在匹配集里。
// 未登记的输入原样（归一化后）返回——绝不替换成另一个工具，否则给拒绝表喂一个
// 退役名会误禁替代工具（例如 "run_tests" 会禁掉 "shell"）。
//
// 适用场景：任何需要按"模型可能请求的任意拼写"匹配的集合（拒绝表、可见性检测）。
// 拼写表只留在 builtinEntries 一份，调用点写规范名表达意图，不再手抄别名。
func CallableNames(names ...string) []string {
	out := make([]string, 0, len(names))
	seen := make(map[string]bool, len(names))
	add := func(name string) {
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		out = append(out, name)
	}
	for _, name := range names {
		entry, ok := lookupLive(name)
		if !ok {
			add(Normalize(name))
			continue
		}
		add(entry.Name)
		for _, alias := range entry.Aliases {
			add(alias)
		}
	}
	return out
}

// SurfaceRank 报告 name 在"以 canonical 为规范名的那个工具家族"内的暴露面偏好排名：
// 规范名最高（len(Aliases)+1），其后按 Entry.Aliases 的声明顺序逐个递减；
// 不属于该家族的名字返回 0。
//
// 用途：模型工具面压缩时从"同一族的多份 schema"里挑出唯一要暴露的那一份
// （见 agent/loop.go 的 optimizeModelToolSurface）。排名完全由别名表推导，
// 新增别名会自动进入该族排名，不必再去改一处 switch。
func SurfaceRank(canonical, name string) int {
	entry, ok := byCanonical[Normalize(canonical)]
	if !ok {
		return 0
	}
	normalized := Normalize(name)
	if normalized == entry.Name {
		return len(entry.Aliases) + 1
	}
	for i, alias := range entry.Aliases {
		if normalized == alias {
			return len(entry.Aliases) - i
		}
	}
	return 0
}

// IsSpellingOf 报告 name 是否是以 canonical 为规范名的那个工具的现役拼写
// （规范名或别名）。退役名与未登记的外部同名工具一律返回 false——调用方
// 据此保持"不猜"的语义。
func IsSpellingOf(canonical, name string) bool {
	return SurfaceRank(canonical, name) > 0
}

// Groups 返回分组目录。
func Groups() []GroupInfo {
	out := make([]GroupInfo, len(groupCatalog))
	copy(out, groupCatalog)
	return out
}

// ByGroup 返回某组下的全部内置工具条目，按规范名排序。
func ByGroup(group Group) []Entry {
	out := make([]Entry, 0, 8)
	for _, entry := range builtinEntries {
		if entry.Group == group {
			out = append(out, entry)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// GroupCatalogEntry 是 groupCatalog 的只读投影，供调用方按 id 取标签/保护位。
func GroupLabel(group Group) (GroupInfo, bool) {
	for _, info := range groupCatalog {
		if info.ID == group {
			return info, true
		}
	}
	return GroupInfo{}, false
}
