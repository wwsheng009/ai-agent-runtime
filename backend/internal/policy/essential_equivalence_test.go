package policy

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolregistry"
)

// 本文件是 P1（`Essential` 名单去重）的**迁移审计**，不是又一份名单。
//
// 事实（2026-10-16 实测，见 docs/tools/unified-tool-registry-plan.md §8）：
//
//	P1 前 policy.IsRuntimeOwnedEssentialTool 自持一张 switch 表，且用
//	normalizeToolName 先把名字折向**旧拼写**（job_start→background_task、
//	enter_plan_mode→enter_plan_mode…），再按旧拼写匹配；而 toolbroker 广播的
//	是**规范名**（subagent_spawn / team_spawn / subagent_list …）。
//	两张表的方向相反，于是「规范化之后的名字」在 essential 表里全部落空：
//	窄 allowlist 下 subagent_spawn / team_spawn / spawn_subagents 这些委派入口
//	会被 filterPolicyBlockedToolDefinitions 静默摘掉定义，而 prompt 仍在教
//	模型去委派——正是 supervision_tool_registry_test.go 记载的那类假接入。
//
// 所以 P1 不是"换个地方查表"，而是**修掉一个真红 bug**。本文件把这个事实
// 钉成可回归的断言：注册表声明为 Essential 的工具，其规范名必须判定为必需。
//
// 语义注记（2026-10-16）：Entry.Essential 已不再是 allowlist 豁免（policy
// 移除了 bypass），本文件审计的"必需判定"现在只服务观测面（metadata.
// essential）与目录 API；集合等价断言原样保留。
//
// 为让"迁移前后的差异"可审计而不是自说自话，下面把 P1 前的实现**逐字冻结**
// 为基准（legacyP1Normalize / legacyP1Essential），再从 toolregistry 的登记表
// 派生语料做集合级比对。冻结副本只作审计基准，**不得**跟随真源演进。

// legacyP1Normalize 是 P1 前 policy.normalizeToolName 的逐字副本（取自
// git HEAD:backend/internal/policy/capability.go）。注意方向：它折向旧拼写。
func legacyP1Normalize(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.ReplaceAll(name, "-", "_")
	switch name {
	case "askuserquestion":
		return "ask_user_question"
	case "enterplanmode":
		return "enter_plan_mode"
	case "exitplanmode":
		return "exit_plan_mode"
	case "backgroundtask", "jobstart", "job_start":
		return "background_task"
	case "joboutput", "job_output", "taskoutput":
		return "task_output"
	case "jobcancel", "job_cancel", "taskkill":
		return "task_kill"
	case "jobmonitor", "job_monitor", "taskmonitor":
		return "task_monitor"
	case "jobcontrol", "job_control", "taskcontrol":
		return "task_control"
	case "jobinput", "job_input", "writestdin", "taskinput", "write_stdin", "task_input":
		return "write_stdin"
	case "spawnagent":
		return "spawn_agent"
	case "listagents":
		return "list_agents"
	case "sendmessage":
		return "send_message"
	case "followuptask":
		return "followup_task"
	case "sendinput":
		return "send_input"
	case "waitagent":
		return "wait_agent"
	case "readagentevents":
		return "read_agent_events"
	case "closeagent":
		return "close_agent"
	case "resumeagent":
		return "resume_agent"
	case "resolveagentapproval":
		return "resolve_agent_approval"
	case "spawnteam":
		return "spawn_team"
	case "waitteam":
		return "wait_team"
	case "sendteammessage":
		return "send_team_message"
	case "readmailboxdigest":
		return "read_mailbox_digest"
	case "readtaskspec":
		return "read_task_spec"
	case "readtaskcontext":
		return "read_task_context"
	case "reporttaskoutcome":
		return "report_task_outcome"
	case "blockcurrenttask":
		return "block_current_task"
	case "readagentresult", "read_agent_result", "agentresult":
		return "read_agent_result"
	case "supervisionsnapshot":
		return "supervision_snapshot"
	case "supervisiondescendants":
		return "supervision_descendants"
	case "subagentstatus", "agentstatus", "agent_status":
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
	default:
		return name
	}
}

// legacyP1EssentialSpellings 是 P1 前 essential switch 的逐字操作数
// （取自 git HEAD:backend/internal/policy/tool_policy.go）。
var legacyP1EssentialSpellings = []string{
	"search_tool",
	"ask_user_question",
	"plan_enter", "plan_exit", "plan_review",
	"todos", "get_goal", "read_goal", "update_goal",
	"background_task", "job_start", "task_output", "job_output", "job_list", "job_search",
	"spawn_agent", "spawn_subagents", "spawn_team",
	"list_agents", "wait_agent", "read_agent_events",
	"send_message", "send_input", "followup_task",
	"close_agent", "resume_agent",
	"apply_agent_worktree", "discard_agent_worktree",
	"resolve_agent_approval",
	"wait_team", "send_team_message",
	"read_mailbox_digest", "read_task_spec", "read_task_context",
	"report_task_outcome", "block_current_task",
	"supervision_snapshot", "supervision_descendants", "read_agent_result",
	"subagent_status", "subagent_inspect_task", "subagent_ack_lifecycle", "subagent_control",
	"ack_lifecycle", "control_descendant",
}

// legacyP1Essential 是 P1 前 IsRuntimeOwnedEssentialTool 的逐字副本：
// 先 legacyP1Normalize，再按旧拼写 switch。
func legacyP1Essential(toolName string) bool {
	switch legacyP1Normalize(toolName) {
	case "search_tool",
		"ask_user_question",
		"plan_enter", "plan_exit", "plan_review",
		"todos", "get_goal", "read_goal", "update_goal",
		"background_task", "job_start", "task_output", "job_output", "job_list", "job_search",
		"spawn_agent", "spawn_subagents", "spawn_team",
		"list_agents", "wait_agent", "read_agent_events",
		"send_message", "send_input", "followup_task",
		"close_agent", "resume_agent",
		"apply_agent_worktree", "discard_agent_worktree",
		"resolve_agent_approval",
		"wait_team", "send_team_message",
		"read_mailbox_digest", "read_task_spec", "read_task_context",
		"report_task_outcome", "block_current_task",
		"supervision_snapshot", "supervision_descendants", "read_agent_result",
		"subagent_status", "subagent_inspect_task", "subagent_ack_lifecycle", "subagent_control",
		"ack_lifecycle", "control_descendant":
		return true
	default:
		return false
	}
}

// essentialAuditCorpus 从注册表登记表派生审计语料：规范名 + 现役别名，
// 再补上旧 switch 独有的拼写（那些只是 Normalize 的 case，不在 Entry.Aliases
// 里），以及负对照。**不含**任何手写的"必需名单"。
func essentialAuditCorpus() []string {
	var names []string
	for _, e := range toolregistry.Entries() {
		names = append(names, e.Name)
		names = append(names, e.Aliases...)
	}
	// 旧表的独有拼写（Normalize-only case，非 Entry.Alias）。
	names = append(names,
		"background_task", "task_output", "task_kill", "task_monitor", "task_control",
		"write_stdin", "task_input", "spawn_team", "ack_lifecycle", "control_descendant",
		"subagent_inspect", "agent_status", "inspecttask", "agentresult",
	)
	// 负对照：非必需内置工具、外部来源名、空名与不存在的名字。
	names = append(names,
		"view", "grep", "job_cancel", "job_monitor", "job_control", "job_input",
		"mcp__foo__spawn_agent", "", "no_such_tool",
	)
	return names
}

// TestEssentialSetMatchesPreMigrationBaseline 做集合级等价审计：
//
//  1. 名字级**不得丢项**：旧表判定为必需的每个名字，今天仍必须为必需
//     （丢项 = 窄 allowlist 下定义被静默摘掉，是本战役最危险的方向）。
//  2. 身份级**集合相等**：两侧折到规范名后，工具身份集合必须逐一相同
//     （即"没有多出/少掉任何一个工具"）。
//  3. 名字级放宽必须**全部来自注册表声明**：允许旧表漏掉的规范名/别名胜出
//     （那正是被修的 bug），但每一处放宽都必须能解析到 Entry.Essential=true，
//     不允许注册表之外冒出来的放宽。
func TestEssentialSetMatchesPreMigrationBaseline(t *testing.T) {
	corpus := essentialAuditCorpus()

	legacyByName := map[string]bool{}
	currentByName := map[string]bool{}
	legacyIdentities := map[string]bool{}
	currentIdentities := map[string]bool{}
	var lostItems, widenings []string

	for _, name := range corpus {
		legacy := legacyP1Essential(name)
		current := toolregistry.IsEssential(name)
		legacyByName[name], currentByName[name] = legacy, current
		if legacy {
			legacyIdentities[toolregistry.Canonical(name)] = true
		}
		if current {
			currentIdentities[toolregistry.Canonical(name)] = true
		}
		if legacy && !current {
			lostItems = append(lostItems, name)
		}
		if current && !legacy {
			widenings = append(widenings, name)
		}
	}

	// 1. 最危险的方向：任何旧表列为必需的名字都不许在今天落空。
	require.Empty(t, lostItems,
		"这些名字在迁移前是 runtime-owned essential，迁移后却不是了：窄 allowlist 会把它们的定义从 provider 请求里摘掉")

	// 2. 身份级集合相等：迁移改变的是**名字可达性**，不是工具集合。
	require.Equal(t, legacyIdentities, currentIdentities,
		"essential 的工具身份集合必须与迁移前逐一相同")

	// 3. 放宽只能是注册表声明过的（不是临时补名单）。
	for _, name := range widenings {
		entry, ok := toolregistry.LookupLive(name)
		require.True(t, ok, "%q 的名字级放宽必须能解析到现役条目", name)
		require.True(t, entry.Essential,
			"%q 的名字级放宽必须由 Entry.Essential 声明，而不是注册表之外的名单", name)
	}

	// 这个 bug 的可见症状必须被反向钉住：委派/巡检入口在窄 allowlist 下
	// 必须存活。P1 之前这里对规范名是红的。
	require.Contains(t, widenings, toolregistry.Canonical("subagent_spawn"),
		"subagent_spawn 的规范名必须在放宽之列——这正是 P1 修掉的定义静默摘除")
}

// TestEssentialFollowsRegistryDeclaration 把"委托给真源"这件事本身钉住：
// policy 的必需判定必须与 Entry.Essential 完全同向，正反两个方向都断言。
// 若将来有人在 policy 里重新手抄一份名单，这条会立刻变红。
func TestEssentialFollowsRegistryDeclaration(t *testing.T) {
	entries := toolregistry.Entries()
	require.NotEmpty(t, entries)

	for _, entry := range entries {
		require.Equal(t, entry.Essential, toolregistry.IsEssential(entry.Name),
			"%s: policy 的必需判定必须等于 Entry.Essential", entry.Name)
		for _, alias := range entry.Aliases {
			require.Equal(t, entry.Essential, toolregistry.IsEssential(alias),
				"%s（%s 的现役别名）: 别名必须与规范名同向", alias, entry.Name)
		}
	}

	// 负对照：非必需与外部来源名不得被顺手放行。
	for _, name := range []string{"view", "grep", "job_cancel", "job_monitor", "mcp__foo__spawn_agent", "no_such_tool", ""} {
		require.False(t, toolregistry.IsEssential(name),
			"%q 不是 runtime-owned essential", name)
	}
}
