package toolkit_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

type metadataListTool struct {
	*toolkit.BaseTool
	meta map[string]interface{}
}

func (t *metadataListTool) DefinitionMetadata() map[string]interface{} {
	return t.meta
}

func (t *metadataListTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	_ = ctx
	_ = params
	return &toolkit.ToolResult{Success: true, Content: "ok"}, nil
}

type conditionalListTool struct {
	*toolkit.BaseTool
}

func (t *conditionalListTool) ShouldList(listCtx toolkit.ListToolsContext) bool {
	return listCtx.TeamActive
}

func (t *conditionalListTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	_ = ctx
	_ = params
	return &toolkit.ToolResult{Success: true, Content: "ok"}, nil
}

func TestShouldListMetadata_ExplicitAndListWhen(t *testing.T) {
	if !toolkit.ShouldListMetadata(nil, toolkit.ListToolsContext{}) {
		t.Fatal("empty metadata should list by default")
	}
	if toolkit.ShouldListMetadata(map[string]interface{}{toolkit.MetaShouldList: false}, toolkit.ListToolsContext{}) {
		t.Fatal("should_list=false must hide tool")
	}
	if !toolkit.ShouldListMetadata(map[string]interface{}{toolkit.MetaShouldList: true}, toolkit.ListToolsContext{}) {
		t.Fatal("should_list=true must list tool")
	}
	if toolkit.ShouldListMetadata(map[string]interface{}{toolkit.MetaListWhen: toolkit.ListWhenNever}, toolkit.ListToolsContext{}) {
		t.Fatal("list_when=never must hide tool")
	}
	if toolkit.ShouldListMetadata(map[string]interface{}{toolkit.MetaListWhen: toolkit.ListWhenTeamActive}, toolkit.ListToolsContext{}) {
		t.Fatal("list_when=team_active must hide without team")
	}
	if !toolkit.ShouldListMetadata(map[string]interface{}{toolkit.MetaListWhen: toolkit.ListWhenTeamActive}, toolkit.ListToolsContext{TeamActive: true}) {
		t.Fatal("list_when=team_active must list with team")
	}
}

func TestShouldList_PrefersListableToolOverMetadata(t *testing.T) {
	tool := &conditionalListTool{BaseTool: toolkit.NewBaseTool("team_only", "desc", "1.0.0", map[string]interface{}{"type": "object"}, true)}
	if toolkit.ShouldList(tool, toolkit.ListToolsContext{}) {
		t.Fatal("ListableTool should hide when team inactive")
	}
	if !toolkit.ShouldList(tool, toolkit.ListToolsContext{TeamActive: true}) {
		t.Fatal("ListableTool should list when team active")
	}
}

func TestRegistryListForContext_HidesTools(t *testing.T) {
	registry := toolkit.NewRegistry()
	always := toolkit.NewBaseTool("always_tool", "always listed", "1.0.0", map[string]interface{}{"type": "object"}, true)
	hidden := &metadataListTool{
		BaseTool: toolkit.NewBaseTool("hidden_tool", "hidden", "1.0.0", map[string]interface{}{"type": "object"}, true),
		meta:     map[string]interface{}{toolkit.MetaShouldList: false},
	}
	teamOnly := &metadataListTool{
		BaseTool: toolkit.NewBaseTool("team_tool", "team", "1.0.0", map[string]interface{}{"type": "object"}, true),
		meta:     map[string]interface{}{toolkit.MetaListWhen: toolkit.ListWhenTeamActive},
	}
	for _, tool := range []toolkit.Tool{always, hidden, teamOnly} {
		if err := registry.Register(tool); err != nil {
			t.Fatalf("register %s: %v", tool.Name(), err)
		}
	}

	listed := registry.ListForContext(toolkit.ListToolsContext{})
	names := toolNames(listed)
	if len(names) != 1 || names[0] != "always_tool" {
		t.Fatalf("expected only always_tool, got %v", names)
	}

	listed = registry.ListForContext(toolkit.ListToolsContext{TeamActive: true})
	names = toolNames(listed)
	if len(names) != 2 || names[0] != "always_tool" || names[1] != "team_tool" {
		t.Fatalf("expected always_tool + team_tool, got %v", names)
	}

	schemas := registry.GetToolSchemasForContext(toolkit.ListToolsContext{})
	if len(schemas) != 1 || schemas[0]["name"] != "always_tool" {
		t.Fatalf("expected schema for always_tool only, got %#v", schemas)
	}
}

func TestInMemoryToolSearchIndex_RanksAndEmptyQuery(t *testing.T) {
	idx := toolkit.NewInMemoryToolSearchIndex([]toolkit.ToolSearchEntry{
		{Name: "view", Description: "Read a local file from disk"},
		{Name: "web_search", Description: "Search the public internet for pages"},
		{Name: "apply_patch", Description: "Apply a structured multi-hunk code patch"},
	})

	snapshot := idx.SearchSnapshot("web internet", 5)
	if len(snapshot.Results) == 0 {
		t.Fatal("expected ranked results")
	}
	if snapshot.Results[0].Name != "web_search" {
		t.Fatalf("expected web_search first, got %s (score=%v)", snapshot.Results[0].Name, snapshot.Results[0].Score)
	}
	if snapshot.TotalTools != 3 {
		t.Fatalf("expected total_tools=3, got %d", snapshot.TotalTools)
	}

	empty := idx.SearchSnapshot("", 2)
	if len(empty.Results) != 2 {
		t.Fatalf("empty query should return alphabetical prefix, got %d", len(empty.Results))
	}
	if empty.Results[0].Name != "apply_patch" || empty.Results[1].Name != "view" {
		t.Fatalf("expected alphabetical apply_patch, view; got %s, %s", empty.Results[0].Name, empty.Results[1].Name)
	}
}

func TestSearchTool_ExecuteJSONSnapshotAndEmptyOutcome(t *testing.T) {
	idx := toolkit.NewInMemoryToolSearchIndex([]toolkit.ToolSearchEntry{
		{Name: "view", Description: "Read local files"},
		{Name: "fetch", Description: "Fetch remote URL content"},
	})
	tool := toolkit.NewSearchTool(idx)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"query": "fetch url",
		"limit": 5,
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result == nil || !result.Success {
		t.Fatalf("expected success result, got %#v", result)
	}
	var snapshot toolkit.SearchSnapshot
	if err := json.Unmarshal([]byte(result.Content), &snapshot); err != nil {
		t.Fatalf("unmarshal snapshot: %v\ncontent=%s", err, result.Content)
	}
	if snapshot.Query != "fetch url" || len(snapshot.Results) == 0 {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}
	if snapshot.Results[0].Name != "fetch" {
		t.Fatalf("expected fetch first, got %s", snapshot.Results[0].Name)
	}
	if got := result.Metadata["result_count"]; got != 1 && got != 2 {
		// at least one hit; tolerate extra weak hits
		if n, ok := got.(int); !ok || n < 1 {
			t.Fatalf("expected result_count >= 1, got %#v", got)
		}
	}

	empty, err := tool.Execute(context.Background(), map[string]interface{}{
		"query": "zzzz-no-such-capability",
	})
	if err != nil {
		t.Fatalf("empty execute: %v", err)
	}
	if empty.Metadata[toolresult.MetadataOutcomeKey] != toolresult.OutcomeEmpty {
		t.Fatalf("expected empty outcome, got %#v", empty.Metadata)
	}
	if empty.Metadata[toolresult.MetadataEmptyResultKey] != true {
		t.Fatalf("expected empty_result metadata, got %#v", empty.Metadata)
	}
	if !strings.Contains(empty.Content, `"results"`) {
		t.Fatalf("expected JSON content with results, got %s", empty.Content)
	}

	_, err = tool.Execute(context.Background(), map[string]interface{}{"query": "  "})
	if err == nil {
		t.Fatal("blank query should fail")
	}
}

func TestIsCoreTool_MetadataAndBuiltin(t *testing.T) {
	if !toolkit.IsCoreTool(nil, "view") {
		t.Fatal("view should be builtin core")
	}
	if toolkit.IsCoreTool(nil, "obscure_mcp_helper") {
		t.Fatal("unknown tool should not be core by default")
	}
	if !toolkit.IsCoreTool(map[string]interface{}{toolkit.MetaCoreTool: true}, "obscure_mcp_helper") {
		t.Fatal("core_tool metadata should force core")
	}
	if toolkit.IsCoreTool(map[string]interface{}{toolkit.MetaCoreTool: false}, "view") {
		t.Fatal("core_tool=false should override builtin")
	}
}

func toolNames(tools []toolkit.Tool) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name())
	}
	return names
}
