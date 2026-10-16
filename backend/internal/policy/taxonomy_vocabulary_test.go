package policy

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolregistry"
)

// TestTaxonomyCarriesOnlyCanonicalNames 钉住"一份词表"这一不变量：
// knownToolTaxonomy 只登记**规范名**，即 `Canonical(name) == name`。
//
// 旧拼写（spawn_agent / task_output / spawn_team / enter_plan_mode /
// ack_lifecycle …）与别名（read / rg / bash / execute_shell_command …）都不需要、
// 也不允许再各占一行：LookupToolTaxonomy 会先经 toolregistry.Canonical 归一，
// 旧行是永远不可达的第二份名单——正是本项目要消灭的"多入口"。
//
// 断言取 Canonical 而非 Normalize：Normalize 只折叠拼写（camelCase / 退役名），
// 别名由别名表解析，因此旧写法 `Normalize(name) == name` 恰好会放行 read / rg /
// bash 这类别名行——execute_shell_command 当时就是这么被漏掉的。
func TestTaxonomyCarriesOnlyCanonicalNames(t *testing.T) {
	names := KnownToolTaxonomyNames()
	require.NotEmpty(t, names, "taxonomy 不能为空")

	for _, name := range names {
		require.Equal(t, name, canonicalToolName(name),
			"taxonomy 登记了非规范名 %q：别名与旧拼写都只应存在于 toolregistry 的那一张表里", name)
	}
}

// TestToolAliasesResolveToTheirCanonicalRow 钉住别名解析：别名必须命中规范名那一行，
// 拿到与规范名逐字一致的 taxonomy（Kind / ReadOnly / MutatesFS / RequiresNet）。
func TestToolAliasesResolveToTheirCanonicalRow(t *testing.T) {
	for alias, canonical := range map[string]string{
		"bash":                  "shell",
		"execute_shell_command": "shell",
		"rg":                    "grep",
		"read":                  "view",
	} {
		want, ok := LookupToolTaxonomy(canonical)
		require.True(t, ok, "规范名 %q 必须在 taxonomy 里登记", canonical)

		got, ok := LookupToolTaxonomy(alias)
		require.True(t, ok, "别名 %q 必须解析到 %q 的 taxonomy 行", alias, canonical)
		require.Equal(t, want, got, "别名 %q 与规范名 %q 的 taxonomy 必须逐字一致", alias, canonical)
	}
}

// TestRetiredSpellingsValidateThroughTheSingleVocabulary 钉住删行的**前提**：
// 已写下的 profile / grant 里的旧拼写不会因为删掉旧行而校验失败。
//
// 这不是"额外的兼容层"，而是删除旧行的合法性证明：归一先行，所以旧拼写命中
// 的始终是规范名那一行。
func TestRetiredSpellingsValidateThroughTheSingleVocabulary(t *testing.T) {
	retired := []string{
		// job 家族
		"background_task", "task_output", "task_kill", "task_monitor",
		"task_control", "write_stdin", "task_input",
		// subagent 家族
		"spawn_agent", "list_agents", "send_message", "followup_task",
		"send_input", "wait_agent", "read_agent_events", "close_agent",
		"resume_agent", "resolve_agent_approval",
		// 巡检控制面
		"ack_lifecycle", "control_descendant",
		// team 家族
		"spawn_team", "wait_team", "send_team_message", "read_mailbox_digest",
		"read_task_spec", "read_task_context", "report_task_outcome",
		"block_current_task",
		// plan 家族
		"enter_plan_mode", "exit_plan_mode",
	}

	for _, legacy := range retired {
		tax, ok := LookupToolTaxonomy(legacy)
		require.True(t, ok,
			"旧拼写 %q 必须仍能解析到规范名行；否则已落盘的 profile / grant 会校验失败", legacy)
		require.Equal(t, normalizeToolName(legacy), tax.Name,
			"旧拼写 %q 必须解析到它的规范名", legacy)

		canonical := toolregistry.Canonical(legacy)
		require.NotEqual(t, legacy, canonical,
			"旧拼写 %q 不应再是自己的规范名", legacy)
		require.Equal(t, canonical, tax.Name,
			"旧拼写 %q 的 taxonomy 行必须与 toolregistry 的规范名一致", legacy)
	}
}

// TestBrokerCanonicalFamiliesAreRegisteredInTaxonomy 反向闭合：broker 命名契约
// 的每个规范名都必须在 taxonomy 里登记，否则 profile validate 拒绝一个**正确**
// 的名字（`subagent_spawn` 曾经就落在这个盲区里）。
func TestBrokerCanonicalFamiliesAreRegisteredInTaxonomy(t *testing.T) {
	canonical := []string{
		"ask_user_question", "plan_enter", "plan_exit", "plan_review",
		"job_start", "job_output", "job_cancel", "job_monitor", "job_control",
		"job_input", "job_list", "job_search",
		"subagent_spawn", "subagent_list", "subagent_send", "subagent_followup",
		"subagent_input", "subagent_approve", "subagent_wait", "subagent_events",
		"subagent_close", "subagent_resume", "subagent_apply_worktree",
		"subagent_discard_worktree", "subagent_status", "subagent_inspect_task",
		"subagent_ack_lifecycle", "subagent_control",
		"team_spawn", "team_wait", "team_send", "team_mailbox", "team_task_spec",
		"team_task_context", "team_task_report", "team_task_block",
		"schedule_create", "schedule_list", "schedule_update", "schedule_delete",
		"supervision_snapshot", "supervision_descendants", "read_agent_result",
		"todos", "artifact_read", "get_goal", "update_goal",
	}

	for _, name := range canonical {
		_, ok := LookupToolTaxonomy(name)
		require.True(t, ok, "规范名 %q 必须在 taxonomy 里登记（profile validate 按此清单校验）", name)
	}
}
