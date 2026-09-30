package agent

import runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"

// roleToolDefaults 是三个角色族的默认工具表：必须与
// runtimepolicy.RoleFamilyForTask 的家族划分一致，否则"默认工具为空 +
// 能力面按家族推导"两条路径会互相打架。
var (
	roleToolDefaultsResearch = []string{
		toolNameView,
		toolNameGrep,
		toolNameGlob,
		toolNameLs,
		toolNameShell,
		toolNameFetch,
		toolNameWebSearch,
		toolNameArtifactRead,
	}
	roleToolDefaultsTest = []string{
		toolNameView,
		toolNameGrep,
		toolNameGlob,
		toolNameLs,
		toolNameShell,
		toolNameArtifactRead,
	}
	roleToolDefaultsWrite = []string{
		toolNameView,
		toolNameGrep,
		toolNameGlob,
		toolNameLs,
		toolNameShell,
		toolNameWrite,
		toolNameEdit,
		toolNameMultiEdit,
		toolNameApplyPatch,
		toolNameAppendWrite,
		toolNameArtifactRead,
	}
)

// DefaultToolsForRole 返回子代理 role 的默认工具建议。
//
// 名字必须与运行时工具注册表（internal/tools）一致，否则父会话启用
// allowlist 时交集为空，子代理会拿到"允许 0 个工具"的执行策略而彻底失去
// 工具能力。契约测试 tool_vocabulary_contract_test.go 会校验这里的每个名字。
//
// 角色词表按 runtimepolicy.RoleFamilyForTask 归一：模型实际使用的是
// spawn_agent 的 agent_type（explore/general/plan）与 spawn_subagents 的
// task_type（config/explore/generate/implement/...），旧的精确角色名只是其中
// 一部分；未归一的 role 会落到 nil（"继承父策略"），其能力面也会丢掉
// exec_shell/network，子代理因此静默失去 shell。
func DefaultToolsForRole(role string) []string {
	switch runtimepolicy.RoleFamilyForTask(role) {
	case runtimepolicy.RoleFamilyResearch:
		return append([]string(nil), roleToolDefaultsResearch...)
	case runtimepolicy.RoleFamilyTest:
		return append([]string(nil), roleToolDefaultsTest...)
	case runtimepolicy.RoleFamilyWrite:
		return append([]string(nil), roleToolDefaultsWrite...)
	default:
		return nil
	}
}
