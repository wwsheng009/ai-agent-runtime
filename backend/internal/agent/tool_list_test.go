package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestFilterToolDefinitionsByShouldList(t *testing.T) {
	tools := []types.ToolDefinition{
		{Name: "view", Description: "read"},
		{Name: "hidden", Metadata: map[string]interface{}{toolkit.MetaShouldList: false}},
		{Name: "team_only", Metadata: map[string]interface{}{toolkit.MetaListWhen: toolkit.ListWhenTeamActive}},
	}

	filtered := filterToolDefinitionsByShouldList(tools, toolkit.ListToolsContext{})
	require.Equal(t, []string{"view"}, toolDefinitionNames(filtered))

	filtered = filterToolDefinitionsByShouldList(tools, toolkit.ListToolsContext{TeamActive: true})
	require.Equal(t, []string{"view", "team_only"}, toolDefinitionNames(filtered))
}

func TestProjectToolSurfaceWithSearch_InjectsSearchAndDropsNonCore(t *testing.T) {
	tools := make([]types.ToolDefinition, 0, toolkit.DefaultToolSearchThreshold+2)
	tools = append(tools,
		types.ToolDefinition{Name: "view", Description: "core read"},
		types.ToolDefinition{Name: "grep", Description: "core search"},
		types.ToolDefinition{
			Name:        "niche_analyzer",
			Description: "specialized analysis",
			Metadata:    map[string]interface{}{toolkit.MetaDeferLoading: true},
		},
		types.ToolDefinition{
			Name:        "always_listed_helper",
			Description: "non-core but force list",
			Metadata: map[string]interface{}{
				toolkit.MetaDeferLoading: false,
			},
		},
	)
	for i := 0; len(tools) < toolkit.DefaultToolSearchThreshold+1; i++ {
		tools = append(tools, types.ToolDefinition{
			Name:        fmt.Sprintf("extra_tool_%02d", i),
			Description: "filler non-core tool",
		})
	}

	// Below threshold: no projection / no search injection.
	small := projectToolSurfaceWithSearch(tools[:toolkit.DefaultToolSearchThreshold-1], toolkit.DefaultToolSearchThreshold)
	require.Len(t, small, toolkit.DefaultToolSearchThreshold-1)
	require.NotContains(t, toolDefinitionNames(small), toolkit.ToolSearchName)

	projected := projectToolSurfaceWithSearch(tools, toolkit.DefaultToolSearchThreshold)
	names := toolDefinitionNames(projected)
	assert.Contains(t, names, "view")
	assert.Contains(t, names, "grep")
	assert.Contains(t, names, toolkit.ToolSearchName)
	assert.Contains(t, names, "always_listed_helper")
	assert.NotContains(t, names, "niche_analyzer")
	assert.NotContains(t, names, "extra_tool_00")
	require.Less(t, len(projected), len(tools))
}

func TestProjectToolSurfaceWithSearch_SimpleGoalWinsInComputePath(t *testing.T) {
	// Direct unit: simple goal projection still collapses to tiny surface.
	tools := []types.ToolDefinition{
		{Name: "ls"},
		{Name: "glob"},
		{Name: "view"},
		{Name: "extra_remote_helper", Metadata: map[string]interface{}{toolkit.MetaDeferLoading: true}},
	}
	// Pad so search projection would otherwise trigger if applied.
	for i := 0; len(tools) < toolkit.DefaultToolSearchThreshold+1; i++ {
		tools = append(tools, types.ToolDefinition{Name: fmt.Sprintf("pad_%02d", i)})
	}

	// Simulate computeAvailableTools policy: simple-goal projection first.
	if names := simpleGoalToolNames("ls file"); len(names) == 0 {
		t.Fatal("expected simple goal tools for ls file")
	}
	projected := projectSimpleGoalToolSurface("ls file", tools)
	require.ElementsMatch(t, []string{"ls", "glob"}, toolDefinitionNames(projected))
	require.NotContains(t, toolDefinitionNames(projected), toolkit.ToolSearchName)
}

func TestSimpleGoalToolNamesSeparatesContentAndFileNameSearch(t *testing.T) {
	tests := []struct {
		goal string
		want string
	}{
		{goal: "grep Popover", want: "grep"},
		{goal: "search Popover", want: "grep"},
		{goal: "搜索 export Popover", want: "grep"},
		{goal: "search files for Popover", want: "grep"},
		{goal: "find references DialogTrigger", want: "grep"},
		{goal: "搜索文件中包含 Popover", want: "grep"},
		{goal: "查找包含 DialogContent 的文件", want: "grep"},
		{goal: "find file named popover.tsx", want: "glob"},
		{goal: "search files", want: "glob"},
		{goal: "search files matching *.tsx", want: "glob"},
		{goal: "search *.tsx", want: "glob"},
		{goal: "搜索文件 *.tsx", want: "glob"},
		{goal: "搜索文件", want: "glob"},
		{goal: "查找文件路径 apps/portal-modern", want: "glob"},
	}

	for _, tt := range tests {
		t.Run(tt.goal, func(t *testing.T) {
			got := simpleGoalToolNames(tt.goal)
			require.Equal(t, map[string]bool{tt.want: true}, got)
		})
	}
}

func TestExecuteSearchTool_FindsProjectedTools(t *testing.T) {
	catalog := []types.ToolDefinition{
		{Name: "view", Description: "Read local files"},
		{
			Name:        "sourcegraph",
			Description: "Search public code repositories with Sourcegraph syntax",
			Metadata:    map[string]interface{}{toolkit.MetaDeferLoading: true, "mcp_name": "devtools"},
		},
		{Name: toolkit.ToolSearchName, Description: "meta"},
	}
	// Project model surface: only core + search.
	surface := projectToolSurfaceWithSearch(append(catalog,
		// pad to threshold so projection actually hides non-core
		func() []types.ToolDefinition {
			extra := make([]types.ToolDefinition, 0, toolkit.DefaultToolSearchThreshold)
			for i := 0; i < toolkit.DefaultToolSearchThreshold; i++ {
				extra = append(extra, types.ToolDefinition{Name: fmt.Sprintf("pad_%02d", i)})
			}
			return extra
		}()...,
	), toolkit.DefaultToolSearchThreshold)
	require.NotContains(t, toolDefinitionNames(surface), "sourcegraph")
	require.Contains(t, toolDefinitionNames(surface), toolkit.ToolSearchName)

	output, meta, err := executeSearchTool(map[string]interface{}{
		"query": "sourcegraph code",
		"limit": 5,
	}, catalog)
	require.NoError(t, err)
	require.NotEmpty(t, output)
	require.Equal(t, true, meta["is_ready"])

	var snapshot toolkit.SearchSnapshot
	require.NoError(t, json.Unmarshal([]byte(output), &snapshot))
	require.NotEmpty(t, snapshot.Results)
	require.Equal(t, "sourcegraph", snapshot.Results[0].Name)
	require.Equal(t, "devtools", snapshot.Results[0].ServerName)
}

func TestReActLoop_GetAvailableTools_AppliesShouldListAndSearchProjection(t *testing.T) {
	manager := &mockSearchCatalogMCPManager{}
	agent := &Agent{
		config: &Config{
			Name:     "test-agent",
			Model:    "test-provider",
			MaxSteps: 1,
		},
		mcpManager: manager,
	}
	loop := NewReActLoop(agent, llm.NewLLMRuntime(nil), &LoopReActConfig{EnableToolCalls: true})

	// Non-simple goal so search projection can run.
	tools, err := loop.getAvailableTools(context.Background(), "investigate repository architecture and tooling options", nil)
	require.NoError(t, err)
	names := toolDefinitionNames(tools)

	assert.Contains(t, names, "view")
	assert.Contains(t, names, toolkit.ToolSearchName)
	assert.NotContains(t, names, "hidden_tool")
	assert.NotContains(t, names, "team_only_tool")
	assert.NotContains(t, names, "deferred_helper")
	// Filler non-core tools should be projected out once catalog is large.
	assert.NotContains(t, names, "filler_00")

	// Team-active context should re-list team_only_tool only if it is core or defer_loading=false.
	// team_only_tool is non-core with default defer, so it stays projected; filter alone is covered above.
	// Simple goal still wins and must not force search_tool.
	simpleTools, err := loop.getAvailableTools(context.Background(), "ls file", nil)
	require.NoError(t, err)
	simpleNames := toolDefinitionNames(simpleTools)
	assert.NotContains(t, simpleNames, toolkit.ToolSearchName)
	assert.Subset(t, []string{"ls", "glob"}, simpleNames)
}

func TestReActLoop_GetAvailableTools_SessionStableSurfaceIgnoresSimpleGoalProjection(t *testing.T) {
	agent := &Agent{
		config:     &Config{Name: "test-agent", Model: "test-provider", MaxSteps: 1},
		mcpManager: &mockSearchCatalogMCPManager{},
	}
	loop := NewReActLoop(agent, llm.NewLLMRuntime(nil), &LoopReActConfig{EnableToolCalls: true})
	snapshot := &testSessionStableToolSurfaceSnapshot{refreshable: true}
	ctx := WithTurnToolSurfaceSnapshot(context.Background(), snapshot)

	tools, frozen, err := loop.resolveAvailableTools(ctx, "ls files", nil)
	require.NoError(t, err)
	require.False(t, frozen)
	names := toolDefinitionNames(tools)
	assert.Contains(t, names, "ls")
	assert.Contains(t, names, "glob")
	assert.Contains(t, names, "view")
	assert.Contains(t, names, "grep")
	assert.Contains(t, names, toolkit.ToolSearchName)
}

func TestReActLoop_GetAvailableTools_UpgradesLegacySimpleSessionSurfaceAtTurnBoundary(t *testing.T) {
	agent := &Agent{
		config:     &Config{Name: "test-agent", Model: "test-provider", MaxSteps: 1},
		mcpManager: &mockSearchCatalogMCPManager{},
	}
	loop := NewReActLoop(agent, llm.NewLLMRuntime(nil), &LoopReActConfig{EnableToolCalls: true})
	snapshot := &testSessionStableToolSurfaceSnapshot{
		set:         true,
		refreshable: true,
		tools: []types.ToolDefinition{
			{Name: "glob"},
			{Name: "ls"},
		},
	}
	ctx := WithTurnToolSurfaceSnapshot(context.Background(), snapshot)

	tools, frozen, err := loop.resolveAvailableTools(ctx, "analyze and fix the renderer", nil)
	require.NoError(t, err)
	require.False(t, frozen)
	names := toolDefinitionNames(tools)
	assert.Contains(t, names, "view")
	assert.Contains(t, names, "grep")
	assert.Contains(t, names, toolkit.ToolSearchName)

	snapshot.refreshable = false
	tools, frozen, err = loop.resolveAvailableTools(ctx, "analyze and fix the renderer", nil)
	require.NoError(t, err)
	require.True(t, frozen)
	require.ElementsMatch(t, []string{"glob", "ls"}, toolDefinitionNames(tools))
}

type testSessionStableToolSurfaceSnapshot struct {
	tools       []types.ToolDefinition
	set         bool
	refreshable bool
}

func (s *testSessionStableToolSurfaceSnapshot) LoadTurnToolSurface(ctx context.Context) ([]types.ToolDefinition, bool, error) {
	if err := contextErr(ctx); err != nil {
		return nil, false, err
	}
	return cloneToolDefinitions(s.tools), s.set, nil
}

func (s *testSessionStableToolSurfaceSnapshot) SaveTurnToolSurface(ctx context.Context, tools []types.ToolDefinition) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	s.tools = cloneToolDefinitions(tools)
	s.set = true
	return nil
}

func (s *testSessionStableToolSurfaceSnapshot) StableAcrossTurns() bool {
	return true
}

func (s *testSessionStableToolSurfaceSnapshot) CanRefreshStableToolSurface() bool {
	return s.refreshable
}

type mockSearchCatalogMCPManager struct{}

func (m *mockSearchCatalogMCPManager) FindTool(toolName string) (skill.ToolInfo, error) {
	return skill.ToolInfo{Name: toolName, Enabled: true}, nil
}

func (m *mockSearchCatalogMCPManager) CallTool(ctx interface{}, mcpName, toolName string, args map[string]interface{}) (interface{}, error) {
	return "ok", nil
}

func (m *mockSearchCatalogMCPManager) ListTools() []skill.ToolInfo {
	tools := []skill.ToolInfo{
		{Name: "view", Description: "Read local files", Enabled: true},
		{Name: "grep", Description: "Search file contents", Enabled: true},
		{Name: "ls", Description: "List directory", Enabled: true},
		{Name: "glob", Description: "Match file paths", Enabled: true},
		{
			Name:        "hidden_tool",
			Description: "should never list",
			Enabled:     true,
			Metadata:    map[string]interface{}{toolkit.MetaShouldList: false},
		},
		{
			Name:        "team_only_tool",
			Description: "team scoped",
			Enabled:     true,
			Metadata:    map[string]interface{}{toolkit.MetaListWhen: toolkit.ListWhenTeamActive},
		},
		{
			Name:        "deferred_helper",
			Description: "specialized deferred capability",
			Enabled:     true,
			Metadata:    map[string]interface{}{toolkit.MetaDeferLoading: true},
		},
	}
	for i := 0; i < toolkit.DefaultToolSearchThreshold; i++ {
		tools = append(tools, skill.ToolInfo{
			Name:        fmt.Sprintf("filler_%02d", i),
			Description: "filler non-core tool " + strings.Repeat("x", 8),
			Enabled:     true,
		})
	}
	return tools
}
