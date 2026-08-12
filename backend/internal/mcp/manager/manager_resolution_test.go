package manager

import (
	"context"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/protocol"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/registry"
)

type resolvingClient struct {
	*fakeClient
	calledTool string
}

func (c *resolvingClient) CallTool(_ context.Context, name string, _ map[string]interface{}) (*protocol.CallToolResult, error) {
	c.calledTool = name
	return &protocol.CallToolResult{}, nil
}

func TestManagerFindToolRequiresCanonicalNameForCollision(t *testing.T) {
	reg := registry.NewRegistry()
	for _, server := range []string{"docs", "issues"} {
		if err := reg.RegisterTool(server, &protocol.Tool{Name: "search"}, true); err != nil {
			t.Fatal(err)
		}
	}
	mgr := &manager{registry: reg}

	if _, err := mgr.FindTool("search"); !registry.IsAmbiguousToolError(err) {
		t.Fatalf("expected ambiguous alias to fail closed, got %v", err)
	}
	info, err := mgr.FindTool("mcp__issues__search")
	if err != nil {
		t.Fatalf("canonical lookup failed: %v", err)
	}
	if info.MCPName != "issues" {
		t.Fatalf("canonical lookup selected %q", info.MCPName)
	}
}

func TestManagerCallToolConvertsCanonicalNameToRawName(t *testing.T) {
	reg := registry.NewRegistry()
	if err := reg.RegisterTool("issues", &protocol.Tool{Name: "search"}, true); err != nil {
		t.Fatal(err)
	}
	client := &resolvingClient{fakeClient: &fakeClient{name: "issues"}}
	reg.RegisterClient("issues", client)
	mgr := &manager{registry: reg}

	if _, err := mgr.CallTool(context.Background(), "issues", "mcp__issues__search", map[string]interface{}{}); err != nil {
		t.Fatalf("canonical call failed: %v", err)
	}
	if client.calledTool != "search" {
		t.Fatalf("client received %q, want raw tool name search", client.calledTool)
	}
}

func TestManagerCallToolDoesNotBypassDisabledRegistration(t *testing.T) {
	reg := registry.NewRegistry()
	if err := reg.RegisterTool("issues", &protocol.Tool{Name: "search"}, false); err != nil {
		t.Fatal(err)
	}
	client := &resolvingClient{fakeClient: &fakeClient{name: "issues"}}
	reg.RegisterClient("issues", client)
	mgr := &manager{registry: reg}

	if _, err := mgr.CallTool(context.Background(), "issues", "search", nil); err == nil {
		t.Fatal("expected disabled registered tool call to fail closed")
	}
	if client.calledTool != "" {
		t.Fatalf("disabled tool reached MCP client: %q", client.calledTool)
	}
}

func TestManagerLoadToolsQuarantinesOnlyInvalidDefinitions(t *testing.T) {
	reg := registry.NewRegistry()
	mgr := &manager{registry: reg}
	var quarantineEvent *LifecycleEvent
	mgr.AddLifecycleObserver(func(event LifecycleEvent) {
		if event.Type == "mcp.tool.quarantined" {
			copied := event
			quarantineEvent = &copied
		}
	})
	client := &fakeClient{
		name: "docs",
		tools: []*protocol.Tool{
			{Name: "valid", InputSchema: map[string]interface{}{"type": "object"}},
			{Name: "invalid", InputSchema: map[string]interface{}{"type": "array"}},
		},
	}

	mgr.loadTools(context.Background(), client, "docs")
	tools := mgr.ListTools()
	if len(tools) != 1 || tools[0].Tool.Name != "valid" {
		t.Fatalf("valid tools were not isolated from invalid definitions: %#v", tools)
	}
	quarantined := mgr.ListQuarantinedTools()
	if len(quarantined) != 1 || quarantined[0].ToolName != "invalid" {
		t.Fatalf("unexpected quarantine report: %#v", quarantined)
	}
	if quarantineEvent == nil || quarantineEvent.Payload["schema_hash"] == "" || quarantineEvent.Payload["canonical_name"] != "mcp__docs__invalid" {
		t.Fatalf("quarantine lifecycle event lacks identity diagnostics: %#v", quarantineEvent)
	}
}
