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

func TestSimpleGoalProjectionCollapsesSurface(t *testing.T) {
	// Direct unit: simple goal projection still collapses to tiny surface.
	tools := []types.ToolDefinition{
		{Name: "ls"},
		{Name: "glob"},
		{Name: "view"},
		{Name: "extra_remote_helper", Metadata: map[string]interface{}{toolkit.MetaDeferLoading: true}},
	}
	// Pad the catalog; there is no directory-size projection any more.
	for i := 0; len(tools) < 40; i++ {
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

func TestExecuteSearchTool_FindsTools(t *testing.T) {
	catalog := []types.ToolDefinition{
		{Name: "view", Description: "Read local files"},
		{
			Name:        "sourcegraph",
			Description: "Search public code repositories with Sourcegraph syntax",
			Metadata:    map[string]interface{}{toolkit.MetaDeferLoading: true, "mcp_name": "devtools"},
		},
		{Name: toolkit.ToolSearchName, Description: "meta"},
	}

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

func TestReActLoop_GetAvailableTools_ListsAuthorizedToolsWithoutProjection(t *testing.T) {
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

	// Non-simple goal: no goal projection is applied.
	tools, err := loop.getAvailableTools(context.Background(), "investigate repository architecture and tooling options", nil)
	require.NoError(t, err)
	names := toolDefinitionNames(tools)

	assert.Contains(t, names, "view")
	// All enabled MCP tools are listed directly now, including non-core ones.
	assert.Contains(t, names, "deferred_helper")
	assert.Contains(t, names, "filler_00")
	// search_tool is not injected automatically when nothing is hidden.
	assert.NotContains(t, names, toolkit.ToolSearchName)
	assert.NotContains(t, names, "hidden_tool")
	assert.NotContains(t, names, "team_only_tool")

	// Simple goal projection is still applied for turn-local surfaces.
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

	tools, frozen, _, err := loop.resolveAvailableTools(ctx, "ls files", nil)
	require.NoError(t, err)
	require.False(t, frozen)
	names := toolDefinitionNames(tools)
	assert.Contains(t, names, "ls")
	assert.Contains(t, names, "glob")
	assert.Contains(t, names, "view")
	assert.Contains(t, names, "grep")
	assert.NotContains(t, names, toolkit.ToolSearchName)
	assert.Contains(t, names, "deferred_helper")
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

	tools, frozen, _, err := loop.resolveAvailableTools(ctx, "analyze and fix the renderer", nil)
	require.NoError(t, err)
	require.False(t, frozen)
	names := toolDefinitionNames(tools)
	assert.Contains(t, names, "view")
	assert.Contains(t, names, "grep")
	assert.NotContains(t, names, toolkit.ToolSearchName)

	snapshot.refreshable = false
	tools, frozen, _, err = loop.resolveAvailableTools(ctx, "analyze and fix the renderer", nil)
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
	for i := 0; i < 40; i++ {
		tools = append(tools, skill.ToolInfo{
			Name:        fmt.Sprintf("filler_%02d", i),
			Description: "filler non-core tool " + strings.Repeat("x", 8),
			Enabled:     true,
		})
	}
	return tools
}

// mcpToolListStub 可变 MCP 工具目录：模拟异步建连完成后工具才出现在 ListTools 的场景。
type mcpToolListStub struct {
	tools []skill.ToolInfo
}

func (m *mcpToolListStub) FindTool(toolName string) (skill.ToolInfo, error) {
	return skill.ToolInfo{Name: toolName, Enabled: true}, nil
}

func (m *mcpToolListStub) CallTool(ctx interface{}, mcpName, toolName string, args map[string]interface{}) (interface{}, error) {
	return "ok", nil
}

func (m *mcpToolListStub) ListTools() []skill.ToolInfo {
	return append([]skill.ToolInfo(nil), m.tools...)
}

// TestReActLoop_ResolveAvailableTools_RefreshesFrozenSurfaceWhenMCPToolsAppear 验证
// MCP 延迟建连场景：冻结面生成时目录里没有 MCP 工具，建连完成后在 turn 边界重建，
// 让 list_pages 等工具进入工具面。
func TestReActLoop_ResolveAvailableTools_RefreshesFrozenSurfaceWhenMCPToolsAppear(t *testing.T) {
	manager := &mcpToolListStub{tools: []skill.ToolInfo{
		{Name: "view", Enabled: true},
		{Name: "shell", Enabled: true},
	}}
	agent := &Agent{
		config:     &Config{Name: "test-agent", Model: "test-provider", MaxSteps: 1},
		mcpManager: manager,
	}
	loop := NewReActLoop(agent, llm.NewLLMRuntime(nil), &LoopReActConfig{EnableToolCalls: true})
	snapshot := &testSessionStableToolSurfaceSnapshot{
		set:         true,
		refreshable: true,
		tools: []types.ToolDefinition{
			{Name: "view"},
			{Name: "shell"},
		},
	}
	ctx := WithTurnToolSurfaceSnapshot(context.Background(), snapshot)

	// 目录没有新增能力：沿用冻结面，保持 prompt cache 前缀。
	tools, frozen, _, err := loop.resolveAvailableTools(ctx, "analyze repository", nil)
	require.NoError(t, err)
	require.True(t, frozen)
	require.ElementsMatch(t, []string{"view", "shell"}, toolDefinitionNames(tools))

	// MCP 建连完成（如 chrome-devtools 发布 list_pages）→ turn 边界重建工具面。
	manager.tools = append(manager.tools, skill.ToolInfo{
		Name:     "list_pages",
		Enabled:  true,
		MCPName:  "chrome-devtools",
		Metadata: map[string]interface{}{"mcp_name": "chrome-devtools"},
	})
	tools, frozen, _, err = loop.resolveAvailableTools(ctx, "analyze repository and list pages", nil)
	require.NoError(t, err)
	require.False(t, frozen)
	names := toolDefinitionNames(tools)
	assert.Contains(t, names, "list_pages")
	assert.Contains(t, names, "view")
	assert.Contains(t, names, "shell")
}
