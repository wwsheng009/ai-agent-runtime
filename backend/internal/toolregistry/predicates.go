package toolregistry

import "strings"

// Normalize 是工具名的唯一归一函数（原先散在 policy/capability.go 的
// normalizeToolName，逐字移植，语义不变）：
//
//   - 小写化并去空白；
//   - `-` 折叠为 `_`；
//   - 把 camelCase / 旧拼写折叠到**契约规范名**（joboutput→job_start、
//     spawnagent→subagent_spawn 等）。契约规范名由 internal/toolbroker 的
//     tool_naming_contract_test.go 钉死：`<domain>_<verb>`，域闭集
//     {job, schedule, team, subagent, plan}。此前的 policy 口径（background_task、
//     spawn_agent…）已不再是规范名。
//
// 迁移后 policy.normalizeToolName 退化为对本函数的转发，两侧不再各持一份表。
func Normalize(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.ReplaceAll(name, "-", "_")
	switch name {
	case "askuserquestion":
		return "ask_user_question"
	// Plan 家族（命名契约 B4）：旧 *_plan_mode 拼写归一到契约名。
	case "enterplanmode", "enter_plan_mode", "planenter":
		return "plan_enter"
	case "exitplanmode", "exit_plan_mode", "planexit":
		return "plan_exit"
	case "planreview":
		return "plan_review"
	// Job 家族（命名契约 B1）
	case "backgroundtask", "background_task", "jobstart", "task_start":
		return "job_start"
	case "taskoutput", "task_output":
		return "job_output"
	case "taskkill", "task_kill":
		return "job_cancel"
	case "taskmonitor", "task_monitor":
		return "job_monitor"
	case "taskcontrol", "task_control":
		return "job_control"
	case "writestdin", "write_stdin", "taskinput", "task_input":
		return "job_input"
	// Subagent 家族（命名契约 B3）
	case "spawnagent", "spawn_agent":
		return "subagent_spawn"
	// 批量委派入口（命名契约 B3，形状 <domain>_<verb>_<object>）：规范名为
	// subagent_spawn_batch；旧拼写 spawn_subagents 保留为现役别名（迁移期
	// ≥2 个版本，见 docs/automation-tasks/tool-surface-inventory-and-naming-review.md §5）。
	// 归一到这里之后 `subagent_*` 通配模式才会覆盖它——此前 Normalize 是恒等，
	// 该入口对 subagent_* 是隐形的。
	case "spawnsubagents", "spawn_subagents":
		return "subagent_spawn_batch"
	case "listagents", "list_agents":
		return "subagent_list"
	case "sendmessage", "send_message":
		return "subagent_send"
	case "followuptask", "followup_task":
		return "subagent_followup"
	case "sendinput", "send_input":
		return "subagent_input"
	case "waitagent", "wait_agent":
		return "subagent_wait"
	case "readagentevents", "read_agent_events":
		return "subagent_events"
	case "closeagent", "close_agent":
		return "subagent_close"
	case "resumeagent", "resume_agent":
		return "subagent_resume"
	case "resolveagentapproval", "resolve_agent_approval", "approveagenttool", "approve_agent_tool", "approvechildtool", "approve_child_tool":
		return "subagent_approve"
	case "agentstatus", "agent_status", "subagentstatus", "subagent_status":
		return "subagent_status"
	case "subagentinspecttask", "subagentinspect", "subagent_inspect", "inspecttask":
		return "subagent_inspect_task"
	case "acklifecycle", "ack_lifecycle":
		return "subagent_ack_lifecycle"
	case "subagentacklifecycle":
		return "subagent_ack_lifecycle"
	case "controldescendant", "control_descendant":
		return "subagent_control"
	case "subagentcontrol":
		return "subagent_control"
	case "subagentapplyworktree", "subagent_apply_worktree", "applyagentworktree", "apply_agent_worktree", "applyworktree", "apply_worktree":
		return "subagent_apply_worktree"
	case "subagentdiscardworktree", "subagent_discard_worktree", "discardagentworktree", "discard_agent_worktree", "discardworktree", "discard_worktree":
		return "subagent_discard_worktree"
	case "readagentresult", "read_agent_result", "agentresult":
		return "read_agent_result"
	case "supervisionsnapshot":
		return "supervision_snapshot"
	case "supervisiondescendants":
		return "supervision_descendants"
	// Team 家族（命名契约 B2）
	case "spawnteam", "spawn_team":
		return "team_spawn"
	case "waitteam", "wait_team":
		return "team_wait"
	case "sendteammessage", "send_team_message":
		return "team_send"
	case "readmailboxdigest", "read_mailbox_digest":
		return "team_mailbox"
	case "readtaskspec", "read_task_spec":
		return "team_task_spec"
	case "readtaskcontext", "read_task_context":
		return "team_task_context"
	case "reporttaskoutcome", "report_task_outcome":
		return "team_task_report"
	case "blockcurrenttask", "block_current_task":
		return "team_task_block"
	default:
		return name
	}
}

// replacedNames 是**退役**工具名 → 现役规范名的迁移表。
//
// 与 Entry.Aliases 的区别是语义而非解析：Aliases 是"同义现役名"（已真实注册，
// 如 read/rg/bash），replacedNames 是"运行时已不再提供、只在诊断里给出替代建议"
// 的历史名（如 read_file/git_log）。两者都参与 Canonical 解析，但只有后者会进
// 入 agent 侧的"请求了一个不可能被授予的名字"告警。
var replacedNames = map[string]string{
	// 读取类
	"read_file":      "view",
	"read_files":     "view",
	"view_file":      "view",
	"open_file":      "view",
	"read_logs":      "view",
	"list_directory": "ls",
	"list_files":     "ls",
	"list_dir":       "ls",
	"ls_dir":         "ls",
	"search_code":    "grep",
	"search_repo":    "grep",
	"grep_repo":      "grep",
	"find_in_files":  "grep",
	// 写入类
	"write_file":         "write",
	"create_file":        "write",
	"overwrite_file":     "write",
	"edit_file":          "edit",
	"update_file":        "edit",
	"str_replace":        "edit",
	"str_replace_editor": "edit",
	"multi_edit":         "multiedit",
	"append_file":        "append_write",
	"applypatch":         "apply_patch",
	"apply_diff":         "apply_patch",
	"patch_file":         "apply_patch",
	// 命令执行类（bash 是现役别名，见 ShellTool）
	"run_tests":   "shell",
	"run_command": "shell",
	"git_log":     "shell",
	"git_status":  "shell",
	"git_diff":    "shell",
	// 网络类
	"web_fetch":  "fetch",
	"http_get":   "fetch",
	"search_web": "web_search",
}

// LegacyReplacement 返回退役名对应的现役替代工具。
//
// 与 agent 侧 validateRequestedToolVocabulary 的既有契约一致：只报告、不改写请求。
func LegacyReplacement(name string) (string, bool) {
	replacement, ok := replacedNames[Normalize(name)]
	return replacement, ok
}

// lookupLive 只按"现役"名字解析条目：规范名与 Entry.Aliases。
//
// 刻意不查 replacedNames（退役名 → 替代名）：那张表的语义是"你请求的名字运行时
// 已不再提供，建议改用 X"（见该表文档），它不代表被请求的名字**就是** X。若据它
// 取类别事实，外部来源（MCP / 第三方）恰好暴露了同名工具时就会被误判成替代工具的
// 类别——例如名为 run_tests 的 MCP 工具会被判成 shell-like，进而套上 shell 的
// 只读命令白名单语义。原 policy.IsXxxToolName 的启发式只看被请求的名字本身，
// 本函数让注册表口径与之一致。
//
// 与 Lookup 的分工：Lookup 回答"这个名字归一到哪个工具"（执行 / 诊断用，含退役名），
// lookupLive 回答"这个名字是否是某工具的现役名字"（取类别事实用）。
func lookupLive(name string) (Entry, bool) {
	normalized := Normalize(name)
	if _, retired := replacedNames[normalized]; retired {
		return Entry{}, false
	}
	if entry, ok := byCanonical[normalized]; ok {
		return entry, true
	}
	if canonical, ok := byAlias[normalized]; ok {
		return byCanonical[canonical], true
	}
	return Entry{}, false
}

// LookupLive 是 lookupLive 的导出形式，供观测面（internal/toolsurface 的
// metadata 补全）取"现役名字"的类别事实（Group/Exposure/Essential/别名）。
//
// 与 Lookup 的分工见 lookupLive 文档：取类别事实必须走本函数，退役名不算命中。
func LookupLive(name string) (Entry, bool) {
	return lookupLive(name)
}

// IsEssential 报告该工具是否属于"运行时自有工具"。policy 的 essential
// 豁免层已于 2026-10-16 移除（profile 必须能完全控制工具面），字段保留，
// 供观测面（metadata.essential）、目录 API 与测试引用。
//
// 与新语义的关系：essential 不再让工具绕过 allowlist；窄 allowlist 下
// 未列出的 essential 工具会被裁掉，需要用通配模式（见 MatchPattern）
// 或显式名单把它们放回来。未登记的名字返回 false。
func IsEssential(name string) bool {
	entry, ok := lookupLive(name)
	return ok && entry.Essential
}

// MatchPattern 报告 name 是否命中一条工具名单模式。工具名单
// （allowlist / denylist / skills / MCP 名单）的统一通配语义：
//
//   - "*"          —— 匹配一切；
//   - "prefix_*"   —— 前缀匹配（以 prefix_ 开头，如 "subagent_*"）；
//   - 其余          —— 归一化后精确匹配（走 Normalize，接受别名/大小写/连字符变体）。
//
// 这是 skill 层 nameMatchesSelection（profile/merge.go，裸 * + 大小写折叠）
// 的工具面超集：两者并存，本函数供工具策略相关名单使用。调用方约定
// "deny 恒优先于 allow"：通配 allow 放进来的工具，通配 deny 仍能拦住。
func MatchPattern(pattern string, name string) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return false
	}
	normalizedPattern := Normalize(pattern)
	if strings.HasSuffix(normalizedPattern, "*") {
		prefix := strings.TrimSuffix(normalizedPattern, "*")
		return strings.HasPrefix(Normalize(name), prefix)
	}
	return normalizedPattern == Normalize(name)
}

// MatchesAnyPattern 报告 name 是否命中名单中的任一模式。名单元素为空时
// 整体返回 false（空名单必须表示"未声明"而不是"全匹配"）。
func MatchesAnyPattern(list []string, name string) bool {
	for _, pattern := range list {
		if MatchPattern(pattern, name) {
			return true
		}
	}
	return false
}

// ListDeclaresWildcard 报告名单是否声明过通配模式（"*" 或 "prefix_*"）。
// 调用方用它区分"全量放行"（需要展开）与"纯显式名单"（可保持 map 语义）。
func ListDeclaresWildcard(list []string) bool {
	for _, pattern := range list {
		if strings.HasSuffix(strings.TrimSpace(pattern), "*") {
			return true
		}
	}
	return false
}

// PatternHitsRegisteredName 报告一条模式能否命中至少一个登记中的现役工具名。
// 校验器用它区分"有效的域级通配"与"大概率拼错的模式"。
func PatternHitsRegisteredName(pattern string) bool {
	for _, entry := range Entries() {
		if MatchPattern(pattern, entry.Name) {
			return true
		}
	}
	return false
}

// IsDelegation 报告该工具能否创建新的执行节点。
func IsDelegation(name string) bool {
	if entry, ok := lookupLive(name); ok {
		return entry.Delegation
	}
	return false
}

// IsPlanWrite 报告该工具是否通过可被 plan allowlist 检查的路径改写文件内容。
func IsPlanWrite(name string) bool {
	if entry, ok := lookupLive(name); ok {
		return entry.PlanWrite
	}
	return false
}

// writeLikeMarkers 是原 policy.IsWriteLikeToolName 的子串标记表。
var writeLikeMarkers = []string{"write", "edit", "patch", "apply", "delete", "remove", "rename", "move", "download"}

// shellLikeMarkers 是原 policy.IsShellLikeToolName 的子串标记表。
var shellLikeMarkers = []string{"shell", "bash", "exec"}

// IsWriteLike 报告工具名是否隐含写入。
//
// 语义严格保持原 policy.IsWriteLikeToolName：**先查注册表**给出精确事实，
// 未登记的名字（MCP / 第三方）回退到原有子串启发式。这样已知工具的判定从
// "猜测"升级为"声明"，而未知工具的行为一个字节都没变。
// 注意：退役名（replacedNames）不算现役登记，按未登记走下面的启发式。
func IsWriteLike(name string) bool {
	if entry, ok := lookupLive(name); ok {
		return entry.WriteLike
	}
	// 注意：原实现不做 TrimSpace，这里同样不做。
	lower := strings.ToLower(name)
	for _, marker := range writeLikeMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// IsShellLike 报告工具名是否可能执行 shell 命令。
//
// 回退语义同原 policy.IsShellLikeToolName（含 TrimSpace 与空串短路）。
// 注意：退役名（replacedNames）不算现役登记，按未登记走下面的启发式。
func IsShellLike(name string) bool {
	if entry, ok := lookupLive(name); ok {
		return entry.ShellLike
	}
	lower := strings.ToLower(strings.TrimSpace(name))
	if lower == "" {
		return false
	}
	for _, marker := range shellLikeMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// IsReadLike 报告工具是否是读取面工具（read 组）。供装配期的读取面折叠复用。
func IsReadLike(name string) bool {
	entry, ok := lookupLive(name)
	return ok && entry.Group == GroupRead
}

// GroupOf 返回工具所属分组；未登记返回空串（**不**猜）。
func GroupOf(name string) Group {
	entry, ok := lookupLive(name)
	if !ok {
		return ""
	}
	return entry.Group
}
