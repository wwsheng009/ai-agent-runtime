// 观测 payload 共享构建点的单测（§2.2-A/§2.2-C）。
//
// 关注三件事：metadata 事实只来自 registry + 账本；不覆写账本、不改写调用方输入；
// groups[]/filtered[] 的形状与取值域（可加 JSON 契约）。

package toolsurface

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolregistry"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestEnrichAddsRegistryMetadataOnTopOfLedgerSource(t *testing.T) {
	input := []runtimetypes.ToolDefinition{
		{Name: "shell", Description: "run"},
		{Name: "job_start", Metadata: map[string]interface{}{"surface_source": string(SourceOrchestration)}},
	}
	tools, ok := Build(input, Ledger{Broker: fakeBroker{}})
	require.True(t, ok)

	observation := Enrich(tools, Ledger{Broker: fakeBroker{}}, nil)
	byName := indexObservationTools(observation.Tools)

	// 账本已给的 surface_source 保留（Frozen/请求面账本优先），缺失的补 core。
	require.Equal(t, string(SourceOrchestration), byName["job_start"].Metadata[MetadataSurfaceSource])
	require.Equal(t, string(SourceCore), byName["shell"].Metadata[MetadataSurfaceSource])

	// registry 事实：group/essential 与 toolregistry 的现役条目一致。
	require.Equal(t, string(toolregistry.GroupShell), byName["shell"].Metadata[MetadataGroup])
	require.Equal(t, string(toolregistry.GroupOrchestration), byName["job_start"].Metadata[MetadataGroup])
	require.Equal(t, true, byName["job_start"].Metadata[MetadataEssential])
	require.Equal(t, false, byName["shell"].Metadata[MetadataEssential])
	// 暴露层是数组（shared/actor/runtime_server 全列）。
	exposure, ok := byName["job_start"].Metadata[MetadataExposure].([]string)
	require.True(t, ok)
	require.Contains(t, exposure, string(toolregistry.ExposureRuntimeServer))

	// 输入（含共享 metadata map）不被改写：观测补齐只作用于副本。
	require.Nil(t, input[0].Metadata)
	require.Equal(t, map[string]interface{}{"surface_source": string(SourceOrchestration)}, input[1].Metadata)
}

func TestEnrichMarksAliasSpellingsWithCanonicalName(t *testing.T) {
	observation := Enrich([]runtimetypes.ToolDefinition{{Name: "read"}}, Ledger{}, nil)
	require.Len(t, observation.Tools, 1)
	// read 是 view 的现役别名：分组按 view 的条目，并标注 canonical。
	require.Equal(t, string(toolregistry.GroupRead), observation.Tools[0].Metadata[MetadataGroup])
	require.Equal(t, "view", observation.Tools[0].Metadata[MetadataAliasOf])

	// 规范名自身 alias_of 为空串（键恒存在，§2.2-A 形状）。
	canonical := Enrich([]runtimetypes.ToolDefinition{{Name: "view"}}, Ledger{}, nil)
	require.Equal(t, "", canonical.Tools[0].Metadata[MetadataAliasOf])
}

// 退役名不是现役登记：外部来源（MCP）恰好暴露同名工具时不得被套上替代工具的类别。
// surface_source 仍按 mcp_name 归一（与 Build 共用 Classify）。
func TestEnrichDoesNotBorrowFactsFromRetiredNames(t *testing.T) {
	observation := Enrich([]runtimetypes.ToolDefinition{
		{Name: "run_tests", Metadata: map[string]interface{}{"mcp_name": "third-party"}},
	}, Ledger{}, nil)
	require.Len(t, observation.Tools, 1)
	metadata := observation.Tools[0].Metadata
	require.NotContains(t, metadata, MetadataGroup)
	require.NotContains(t, metadata, MetadataEssential)
	require.NotContains(t, metadata, MetadataAliasOf)
	require.Equal(t, "mcp:third-party", metadata[MetadataSurfaceSource])
}

func TestEnrichGroupsCatalogCarriesCountsAndFlags(t *testing.T) {
	observation := Enrich([]runtimetypes.ToolDefinition{
		{Name: "view"},
		{Name: "shell"},
		{Name: "job_start"},
		{Name: "mcp_echo", Metadata: map[string]interface{}{"mcp_name": "chrome-devtools"}},
	}, Ledger{}, nil)

	groups := make(map[string]Group, len(observation.Groups))
	for _, group := range observation.Groups {
		groups[group.ID] = group
	}
	require.Len(t, observation.Groups, len(toolregistry.Groups()))
	require.Equal(t, 1, groups[string(toolregistry.GroupRead)].ToolCount)
	require.Equal(t, 1, groups[string(toolregistry.GroupShell)].ToolCount)
	require.Equal(t, 1, groups[string(toolregistry.GroupOrchestration)].ToolCount)
	// 未登记的外部工具不虚构分组，也不计入任何组的计数。
	require.Equal(t, 0, groups[string(toolregistry.GroupMesh)].ToolCount)
	for _, group := range observation.Groups {
		require.NotEmpty(t, group.ID)
		require.NotEmpty(t, group.Label)
		// P0：disabled_groups 未落地前 Enabled 恒 true（不编造关闭态）。
		require.True(t, group.Enabled)
	}
	require.Equal(t, 3, observationTotalGroupCounts(observation.Groups))
}

func TestEnrichNormalizesFilteredEntries(t *testing.T) {
	observation := Enrich([]runtimetypes.ToolDefinition{{Name: "shell"}}, Ledger{}, []FilteredTool{
		{Name: " mcp:chrome ", Reason: FilterMCPDisabled},
		{Name: "", Reason: FilterProfileDeny},
		{Name: "mcp:chrome", Reason: FilterMCPDisabled},
		{Name: "aicli_exec", Reason: FilterProfileDeny},
	})

	require.Equal(t, []FilteredTool{
		{Name: "aicli_exec", Reason: FilterProfileDeny},
		{Name: "mcp:chrome", Reason: FilterMCPDisabled},
	}, observation.Filtered)

	// 无裁剪时是可加契约要求的空数组而非 null。
	empty := Enrich([]runtimetypes.ToolDefinition{{Name: "shell"}}, Ledger{}, nil)
	require.NotNil(t, empty.Filtered)
	require.Empty(t, empty.Filtered)
}

// group_disabled 事实（P3 落地）已能反映到 groups[].enabled，P0 不产生该事实。
func TestEnrichMarksGroupDisabledFromFilteredFacts(t *testing.T) {
	observation := Enrich([]runtimetypes.ToolDefinition{{Name: "shell"}}, Ledger{}, []FilteredTool{
		{Name: string(toolregistry.GroupMesh), Reason: FilterGroupDisabled},
	})
	for _, group := range observation.Groups {
		if group.ID == string(toolregistry.GroupMesh) {
			require.False(t, group.Enabled)
			continue
		}
		require.True(t, group.Enabled)
	}
}

func TestObserveBuildsFromCatalogWithRealLedger(t *testing.T) {
	observation, ok := Observe([]runtimetypes.ToolDefinition{
		{Name: "job_start"},
		{Name: "shell"},
	}, Ledger{Broker: fakeBroker{}}, []FilteredTool{{Name: "mcp:x", Reason: FilterMCPDisabled}})
	require.True(t, ok)
	require.Len(t, observation.Tools, 2)
	byName := indexObservationTools(observation.Tools)
	require.Equal(t, string(SourceOrchestration), byName["job_start"].Metadata[MetadataSurfaceSource])
	require.Equal(t, string(toolregistry.GroupOrchestration), byName["job_start"].Metadata[MetadataGroup])

	// 空目录不虚构 payload。
	_, ok = Observe(nil, Ledger{}, nil)
	require.False(t, ok)
}

func indexObservationTools(tools []runtimetypes.ToolDefinition) map[string]runtimetypes.ToolDefinition {
	byName := make(map[string]runtimetypes.ToolDefinition, len(tools))
	for _, tool := range tools {
		byName[tool.Name] = tool
	}
	return byName
}

func observationTotalGroupCounts(groups []Group) int {
	total := 0
	for _, group := range groups {
		total += group.ToolCount
	}
	return total
}
