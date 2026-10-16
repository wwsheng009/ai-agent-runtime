package policy

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolregistry"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// 本文件把 toolregistry 与 policy 的两张声明表焊在一起。之前它们是可以漂移的：
// 注册表里有的条目，policy 两条解析路径都不认识，能力就落到名字子串启发式。
// 这条缝已经造成两起真事故（2026-10-16 探针实测，见 §8 日志）：
//
//   - spawn_subagents（agent 侧注册的批量委派入口，agent.SpawnSubagentsToolName）：
//     taxonomy 无行、控制面表也无项 ⇒ 只解析出 read_only ⇒
//     `disabled_capabilities: [agent_management]` 关得住 subagent_spawn / team_spawn，
//     却关不住它（授权逃逸，与 capability.go 记录的 ack_lifecycle 同类）。
//   - code_search（条件注册的 workspace 本地索引读取）：无行、无 DefinitionMetadata
//     ⇒ 命中 CapabilityResolver 里的 Contains("search") ⇒ 纯本地读取被判为需要
//     network ⇒ 禁网 profile 下派生子能力面被 AllowCapabilities 误拒（那条拒绝
//     路径由 profileinput/disabled_capabilities_e2e_test.go 钉住）。
//
// 两起事故的根因相同，因此收口方式也相同：注册表条目必须被**声明**覆盖，
// 不许由名字碰运气。

func containsCapability(caps []Capability, want Capability) bool {
	for _, cap := range caps {
		if cap == want {
			return true
		}
	}
	return false
}

// TestRegistryEntriesAreAllDeclaredInPolicyTables 钉住声明覆盖：注册表每个条目都要
// 命中 taxonomy 行或控制面表之一。用 toolregistry.Entries() 作语料，所以"注册表新增
// 条目而 policy 没跟上"会在这里立刻变红，而不是等运行期由名字子串决定。
func TestRegistryEntriesAreAllDeclaredInPolicyTables(t *testing.T) {
	entries := toolregistry.Entries()
	require.NotEmpty(t, entries, "vacuum guard：注册表为空会让本用例恒绿")

	undeclared := make([]string, 0)
	for _, entry := range entries {
		if _, ok := LookupToolTaxonomy(entry.Name); ok {
			continue
		}
		if _, ok := controlPlaneToolCapabilities(normalizeToolName(entry.Name)); ok {
			continue
		}
		undeclared = append(undeclared, entry.Name)
	}
	require.Empty(t, undeclared,
		"以下注册表条目既无 taxonomy 行、也不在控制面表：其能力由名字子串决定，两边都会错——"+
			"授权逃逸（像 spawn_subagents）或误拒（像 code_search）")
}

// TestRegistryEntriesNeverInferNetworkFromName 钉住"网络能力必须声明"：解析出的
// CapNetwork 必须与该条目的声明（RequiresNet 或 Kind=network）一致。名字里带
// "search" / "web" / "fetch" / "download" / "http" / "sourcegraph" 只是启发式，
// code_search 就是被它误判的本地读取。
func TestRegistryEntriesNeverInferNetworkFromName(t *testing.T) {
	entries := toolregistry.Entries()
	require.NotEmpty(t, entries, "vacuum guard：注册表为空会让本用例恒绿")

	resolver := DefaultCapabilityResolver{}
	for _, entry := range entries {
		declared := false
		if row, ok := LookupToolTaxonomy(entry.Name); ok {
			declared = row.RequiresNet || row.Kind == types.ToolKindNetwork
		} else if caps, ok := controlPlaneToolCapabilities(normalizeToolName(entry.Name)); ok {
			declared = containsCapability(caps, CapNetwork)
		}

		got := containsCapability(resolver.Resolve(EvalRequest{ToolName: entry.Name}), CapNetwork)
		require.Equal(t, declared, got,
			"%s：CapNetwork 必须由声明决定，而不是名字子串（declared=%v got=%v）", entry.Name, declared, got)
	}
}

// TestFixedDefectsStayPinned 钉住两处修复的解析结果本身，防止表被后来的编辑改回去：
// 本地索引读取与 read_goal 必须是纯只读（不带 network/write/exec），批量委派入口
// 必须与 subagent_spawn / team_spawn 同级授权。
func TestFixedDefectsStayPinned(t *testing.T) {
	resolver := DefaultCapabilityResolver{}

	for _, name := range []string{
		"code_search", "code_inspect", "code_navigate", "code_references", "code_callers",
		"read_goal",
	} {
		require.Equal(t, []Capability{CapReadOnly}, resolver.Resolve(EvalRequest{ToolName: name}),
			"%s 是只读查询：既不得带 network，也不得带 write/exec", name)
	}

	require.Equal(t, []Capability{CapReadOnly, CapAgentManagement},
		resolver.Resolve(EvalRequest{ToolName: "spawn_subagents"}),
		"agent 侧批量委派入口必须与 subagent_spawn / team_spawn 同级授权，否则 agent_management 门控关不住它")
	for _, sibling := range []string{"subagent_spawn", "team_spawn"} {
		require.Equal(t, []Capability{CapReadOnly, CapAgentManagement},
			resolver.Resolve(EvalRequest{ToolName: sibling}),
			"%s 是 spawn_subagents 的对照兄弟，三者必须同级", sibling)
	}
}
