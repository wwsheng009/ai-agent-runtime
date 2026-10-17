package toolregistry

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestEntriesAreInternallyConsistent 钉死注册表的内部一致性。
//
// 这些断言的作用是让"新增工具时漏填某个维度"在 CI 里立刻失败，而不是等到
// 运行期的名单漂移。
func TestEntriesAreInternallyConsistent(t *testing.T) {
	seenCanonical := map[string]bool{}
	seenAlias := map[string]string{}

	for _, entry := range Entries() {
		require.NotEmpty(t, entry.Name, "entry name is required")
		require.NotEmpty(t, entry.Group, "entry %q must declare a group", entry.Name)
		require.NotEmpty(t, entry.Exposure, "entry %q must declare an exposure", entry.Name)

		require.False(t, seenCanonical[entry.Name], "duplicate canonical name %q", entry.Name)
		seenCanonical[entry.Name] = true

		// 分组必须在目录里，否则前端拿不到标签/保护位。
		_, ok := GroupLabel(entry.Group)
		require.True(t, ok, "entry %q uses group %q outside the group catalog", entry.Name, entry.Group)

		// plan-write 蕴含 write-like：能改文件内容却不被判为写类，是自相矛盾的声明。
		if entry.PlanWrite {
			require.True(t, entry.WriteLike, "entry %q is plan-write but not write-like", entry.Name)
		}
		// delegation 蕴含 essential：能创建执行节点的工具被 allowlist 裁掉会 brick 控制面。
		if entry.Delegation {
			require.True(t, entry.Essential, "entry %q is delegation but not essential", entry.Name)
		}
		// shell-like 与 write-like 互斥（shell 可执行只读命令，语义不同）。
		require.False(t, entry.ShellLike && entry.WriteLike,
			"entry %q cannot be both shell-like and write-like", entry.Name)

		for _, alias := range entry.Aliases {
			require.NotEmpty(t, alias, "entry %q has an empty alias", entry.Name)
			require.NotEqual(t, entry.Name, alias, "entry %q aliases itself", entry.Name)
			previous, dup := seenAlias[alias]
			require.False(t, dup, "alias %q is claimed by both %q and %q", alias, previous, entry.Name)
			seenAlias[alias] = entry.Name
		}
	}

	// 别名不得与任何规范名撞车，否则 Canonical 的解析顺序变得可观察。
	for alias, owner := range seenAlias {
		require.False(t, seenCanonical[alias],
			"alias %q (of %q) collides with a canonical name", alias, owner)
	}

	for replaced, canonical := range replacedNames {
		require.False(t, seenCanonical[replaced],
			"replaced name %q collides with a canonical name", replaced)
		require.True(t, seenCanonical[canonical],
			"replaced name %q points at unregistered canonical %q", replaced, canonical)
		// 退役名允许多对一（read_file / read_files / view_file 同指 view），
		// 但不能同时又是现役别名——那是两种不同语义。
		require.False(t, seenAlias[replaced] != "",
			"replaced name %q is also a live alias; pick one semantic", replaced)
	}
}

// TestCanonicalResolvesAliasesAndReplacedNames 钉死别名归一。
func TestCanonicalResolvesAliasesAndReplacedNames(t *testing.T) {
	cases := map[string]string{
		// 现役兼容别名（tools/manager.go 真实注册）
		"bash":                  "shell",
		"execute_shell_command": "shell",
		"read":                  "view",
		"rg":                    "grep",
		"image_generate":        "openai_image_generate",
		// 契约规范名必须是归一不动点（broker 命名契约 B1–B4）。
		"job_output":     "job_output",
		"job_start":      "job_start",
		"job_input":      "job_input",
		"subagent_spawn": "subagent_spawn",
		"subagent_wait":  "subagent_wait",
		"team_spawn":     "team_spawn",
		"team_mailbox":   "team_mailbox",
		"plan_enter":     "plan_enter",
		// 旧拼写只作兼容别名，一律归一到契约规范名（不再是规范名本身）。
		"background_task":     "job_start",
		"task_output":         "job_output",
		"write_stdin":         "job_input",
		"spawn_agent":         "subagent_spawn",
		"list_agents":         "subagent_list",
		"wait_agent":          "subagent_wait",
		"send_message":        "subagent_send",
		"spawn_team":          "team_spawn",
		"wait_team":           "team_wait",
		"report_task_outcome": "team_task_report",
		"enter_plan_mode":     "plan_enter",
		"exit_plan_mode":      "plan_exit",
		// 退役名（文档化替代）
		"read_file":  "view",
		"write_file": "write",
		"git_status": "shell",
		"web_fetch":  "fetch",
		"search_web": "web_search",
		// 大小写/连字符归一
		"Bash":       "shell",
		"Read-File":  "view",
		"web-search": "web_search",
		// 未登记的名字原样返回（**不**猜测）
		"chrome_devtools_navigate": "chrome_devtools_navigate",
		"mcp__foo__bar":            "mcp__foo__bar",
		"":                         "",
	}
	for input, want := range cases {
		require.Equal(t, want, Canonical(input), "Canonical(%q)", input)
	}
}

// TestPredicatesPreserveHeuristicFallback 钉死"未知名字回退启发式"的契约。
//
// 这是本次收敛的安全边界：已知工具从"猜测"升级为"声明"，而 MCP / 第三方
// 名字的判定必须与合并前的 policy 实现逐字一致。
func TestPredicatesPreserveHeuristicFallback(t *testing.T) {
	// 已登记：走声明。
	require.True(t, IsWriteLike("download"))
	require.False(t, IsWriteLike("view"))
	require.True(t, IsShellLike("shell"))
	require.True(t, IsShellLike("bash"))
	require.False(t, IsShellLike("view"))

	// 未登记：回退子串启发式（与旧 policy 实现一致）。
	require.True(t, IsWriteLike("mcp__filesystem__write_file"))
	require.True(t, IsWriteLike("acme_move_thing"))
	require.True(t, IsWriteLike("vendor_apply_plan"))
	require.False(t, IsWriteLike("chrome_devtools_navigate"))
	require.True(t, IsShellLike("acme_exec"))
	require.True(t, IsShellLike("remote_bash"))
	require.False(t, IsShellLike("chrome_devtools_navigate"))
	require.False(t, IsShellLike("   "))
	require.False(t, IsWriteLike(""))

	// 未登记的名字不参与 essential / delegation / plan-write（与旧实现同样是精确匹配）。
	require.False(t, IsEssential("mcp__foo__spawn_agent"))
	require.False(t, IsDelegation("mcp__foo__spawn_agent"))
	require.False(t, IsPlanWrite("mcp__foo__write"))
}

// TestGroupCatalogIsStableForFrontend 钉死分组目录的对外契约。
func TestGroupCatalogIsStableForFrontend(t *testing.T) {
	groups := Groups()
	require.NotEmpty(t, groups)

	byID := map[Group]GroupInfo{}
	for _, info := range groups {
		require.NotEmpty(t, info.ID)
		require.NotEmpty(t, info.Label, "group %q must have a frontend label", info.ID)
		require.False(t, byID[info.ID].ID != "", "duplicate group id %q", info.ID)
		byID[info.ID] = info
	}

	// profile-tool-groups-design.md §3.3：工作区读写基本盘不可整组关闭。
	for _, protected := range []Group{GroupRead, GroupEdit, GroupShell} {
		info, ok := byID[protected]
		require.True(t, ok, "group %q missing from catalog", protected)
		require.True(t, info.Protected, "group %q must be protected", protected)
	}

	// 每个非空分组都应能在目录里找到。
	for _, info := range groups {
		entries := ByGroup(info.ID)
		require.NotNil(t, entries)
	}
}

// TestByGroupAndGroupOf 钉死分组查询。
func TestByGroupAndGroupOf(t *testing.T) {
	read := ByGroup(GroupRead)
	require.NotEmpty(t, read)
	names := make([]string, 0, len(read))
	for _, entry := range read {
		names = append(names, entry.Name)
	}
	require.Contains(t, names, "view")
	require.Contains(t, names, "grep")
	require.NotContains(t, names, "write")

	require.Equal(t, Group(""), GroupOf("write_file"))
	require.Equal(t, GroupEdit, GroupOf("write"))
	require.Equal(t, GroupRead, GroupOf("read"))
	require.Equal(t, Group(""), GroupOf("mcp__foo__bar"))
}

// TestPredicatesIgnoreRetiredNames 钉死"退役名不提供类别事实"这一不变量。
//
// replacedNames 是**诊断**表（"这个名字运行时已不再提供，建议改用 X"），不代表被
// 请求的名字就是 X。若谓词据它取事实，外部来源（MCP / 第三方）恰好暴露了同名工具时
// 会被误判：例如名为 run_tests 的 MCP 工具会被判成 shell-like，进而落进
// policy/engine.go 的只读命令白名单分支被拒，验证者 subagent 失败，PatchDecision 从
// approved 变成 blocked（见 agent/orchestrator_test.go 的
// TestAgent_Orchestrate_PlannerPreferredCanExecuteSubagents）。
func TestPredicatesIgnoreRetiredNames(t *testing.T) {
	// 退役名：不提供类别事实，回落原有子串启发式。
	require.False(t, IsShellLike("run_tests"), "退役名不得被当成替代工具")
	require.False(t, IsShellLike("git_status"))
	require.True(t, IsWriteLike("write_file"), "启发式仍认 write 子串")
	require.Equal(t, Group(""), GroupOf("read_file"))

	// 现役名字（规范名与别名）仍提供注册表事实。
	require.True(t, IsShellLike("shell"))
	require.True(t, IsShellLike("bash"), "现役别名同规范名")
	require.True(t, IsWriteLike("apply_patch"))
	require.True(t, IsEssential("job_start"))
	require.False(t, IsEssential("job_cancel"), "job_cancel 不在必需名单")
	require.True(t, IsDelegation("spawn_agent"), "现役别名 spawn_agent 仍是委派工具")
	require.Equal(t, GroupRead, GroupOf("read"))

	// 诊断面不变：退役名仍可归一到替代工具、仍能给出替代建议。
	entry, ok := Lookup("run_tests")
	require.True(t, ok)
	require.Equal(t, "shell", entry.Name)
	replacement, ok := LegacyReplacement("run_tests")
	require.True(t, ok)
	require.Equal(t, "shell", replacement)
}

func TestCallableNames(t *testing.T) {
	// 规范名展开为 规范名 + 现役别名（顺序：规范名在前）。
	require.Equal(t, []string{"shell", "bash", "execute_shell_command"}, CallableNames("shell"))
	// 用别名输入与用规范名输入等价：得到同一个集合。
	require.Equal(t, []string{"view", "read"}, CallableNames("read"))

	// 多名字合并去重，保留首次出现顺序。
	// 批量委派入口：规范名在前，旧拼写 spawn_subagents 作为现役别名随后展开。
	require.Equal(t, []string{"subagent_spawn", "spawn_agent", "subagent_spawn_batch", "spawn_subagents", "team_spawn", "spawn_team"},
		CallableNames("subagent_spawn", "subagent_spawn_batch", "team_spawn"))
	require.Equal(t, []string{"grep", "rg"}, CallableNames("rg", "grep"))

	// 退役名**不**展开成替代工具，否则拒绝表会把替代工具一起禁掉。
	require.Equal(t, []string{"run_tests"}, CallableNames("run_tests"))
	require.NotContains(t, CallableNames("run_tests"), "shell")

	// 空白丢弃；未登记的外部/MCP 名字原样保留。
	require.Equal(t, []string{"my_mcp_tool"}, CallableNames("", "  ", "my_mcp_tool"))
	require.Empty(t, CallableNames())
}

func TestSurfaceRank(t *testing.T) {
	// 规范名最高，别名按声明顺序递减——必须与 agent/loop.go 压缩工具面时
	// 原先手写的 3/2/1、2/1 排名完全一致。
	require.Equal(t, 3, SurfaceRank("shell", "shell"))
	require.Equal(t, 2, SurfaceRank("shell", "bash"))
	require.Equal(t, 1, SurfaceRank("shell", "execute_shell_command"))
	require.Equal(t, 2, SurfaceRank("grep", "grep"))
	require.Equal(t, 1, SurfaceRank("grep", "rg"))
	require.Equal(t, 2, SurfaceRank("view", "view"))
	require.Equal(t, 1, SurfaceRank("view", "read"))

	// 非该家族、退役名、未登记名字、未知家族一律 0（不猜）。
	require.Zero(t, SurfaceRank("shell", "grep"))
	require.Zero(t, SurfaceRank("shell", "run_tests"))
	require.Zero(t, SurfaceRank("shell", "my_mcp_tool"))
	require.Zero(t, SurfaceRank("no_such_tool", "shell"))

	require.True(t, IsSpellingOf("shell", "bash"))
	require.False(t, IsSpellingOf("shell", "run_tests"))
	require.False(t, IsSpellingOf("shell", "my_mcp_tool"))
}
