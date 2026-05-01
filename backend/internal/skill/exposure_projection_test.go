package skill

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestBuildToolSurfaceSummary(t *testing.T) {
	summary := BuildToolSurfaceSummary([]types.ToolDefinition{
		{
			Name: "alpha",
			Metadata: map[string]interface{}{
				toolresult.SourceKey: toolresult.SourceMCP,
			},
		},
		{
			Name: "beta",
			Metadata: map[string]interface{}{
				toolresult.SourceKey: toolresult.SourceBroker,
			},
		},
		{
			Name: "   ",
		},
	})

	require.NotNil(t, summary)
	require.Equal(t, 2, summary["count"])
	require.Equal(t, []string{"alpha", "beta"}, summary["names"])

	sources, ok := summary["sources"].(map[string]int)
	require.True(t, ok)
	require.Equal(t, 1, sources[toolresult.SourceMCP])
	require.Equal(t, 1, sources[toolresult.SourceBroker])
}

func TestBuildToolSurfaceSummary_ReturnsNilForBlankNames(t *testing.T) {
	summary := BuildToolSurfaceSummary([]types.ToolDefinition{{Name: "   "}})
	require.Nil(t, summary)
}

func TestBuildSkillExposureMetadata(t *testing.T) {
	metadata := BuildSkillExposureMetadata(ExposureProjection{
		Mode:                  "prefer",
		IncludeBuiltin:        true,
		BuiltinFunctions:      []string{"builtin__search"},
		SkillFunctions:        []string{"skill__alpha"},
		FinalFunctionNames:    []string{"builtin__search", "skill__alpha"},
		TopK:                  3,
		RoutedSkills:          []string{"skill__alpha"},
		ExplicitMentions:      []string{"alpha"},
		PreviouslyCalled:      []string{"skill__beta"},
		CatalogTotalFunctions: 9,
		CatalogBuiltinTools:   4,
		CatalogSkillFunctions: 5,
		BuiltinFunctionCount:  1,
		SkillFunctionCount:    1,
		FinalFunctionCount:    2,
		RoutedSkillCount:      1,
		CandidateCount:        1,
		Candidates: []ExposureCandidate{
			{
				FunctionName: "skill__alpha",
				SkillName:    "alpha",
				Score:        0.9,
				MatchedBy:    "explicit",
				Details:      "matched by name",
			},
		},
	})

	require.NotNil(t, metadata)
	summary, ok := metadata["skill_exposure"].(map[string]interface{})
	require.True(t, ok)
	require.Equal(t, "prefer", summary["mode"])
	require.Equal(t, true, summary["include_builtin"])
	require.Equal(t, 3, summary["top_k"])
	require.Equal(t, []string{"builtin__search"}, summary["builtin_functions"])
	require.Equal(t, []string{"skill__alpha"}, summary["skill_functions"])
	require.Equal(t, []string{"builtin__search", "skill__alpha"}, summary["final_function_names"])
	require.Equal(t, []string{"skill__alpha"}, summary["routed_skills"])
	require.Equal(t, []string{"alpha"}, summary["explicit_mentions"])
	require.Equal(t, []string{"skill__beta"}, summary["previously_called"])
	require.Equal(t, 9, summary["catalog_total_functions"])
	require.Equal(t, 4, summary["catalog_builtin_tools"])
	require.Equal(t, 5, summary["catalog_skill_functions"])
	require.Equal(t, 1, summary["builtin_function_count"])
	require.Equal(t, 1, summary["skill_function_count"])
	require.Equal(t, 2, summary["final_function_count"])
	require.Equal(t, 1, summary["routed_skill_count"])
	require.Equal(t, 1, summary["candidate_count"])

	candidates, ok := summary["candidates"].([]ExposureCandidate)
	require.True(t, ok)
	require.Len(t, candidates, 1)
	require.Equal(t, "skill__alpha", candidates[0].FunctionName)
	require.Equal(t, "alpha", candidates[0].SkillName)
}
