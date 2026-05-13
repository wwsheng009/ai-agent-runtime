package aiclitools

import (
	"context"
	"testing"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeskill "github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

func TestRegistryListForPath(t *testing.T) {
	registry := NewRegistry(
		Capability{Name: "shared_only", Exposure: []ExposurePath{ExposureShared}},
		Capability{Name: "actor_only", Exposure: []ExposurePath{ExposureActor}},
		Capability{Name: "both", Exposure: []ExposurePath{ExposureShared, ExposureActor}},
	)

	shared := registry.ListForPath(ExposureShared)
	if len(shared) != 2 || shared[0].Name != "shared_only" || shared[1].Name != "both" {
		t.Fatalf("unexpected shared capabilities: %+v", shared)
	}
	actor := registry.ListForPath(ExposureActor)
	if len(actor) != 2 || actor[0].Name != "actor_only" || actor[1].Name != "both" {
		t.Fatalf("unexpected actor capabilities: %+v", actor)
	}
}

func TestFunctionFromCapabilityExecutesWithSessionContext(t *testing.T) {
	session := &fakeToolSessionContext{sessionID: "session-1", path: ExposureShared}
	fn := FunctionFromCapability(Capability{
		Name:        "cap",
		Description: "test capability",
		Parameters:  map[string]interface{}{"type": "object"},
		Metadata:    map[string]interface{}{"source": "test"},
		Execute: func(ctx context.Context, got ToolSessionContext, args map[string]interface{}) (ToolResult, error) {
			if got == nil || got.SessionID() != "session-1" || got.ExecutorPath() != ExposureShared {
				t.Fatalf("unexpected session context: %#v", got)
			}
			return ToolResult{Output: "ok", Metadata: map[string]interface{}{"done": true}}, nil
		},
	}, func(ctx context.Context) ToolSessionContext { return session })

	output, metadata, err := fn.(interface {
		ExecuteWithMeta(context.Context, map[string]interface{}) (string, map[string]interface{}, error)
	}).ExecuteWithMeta(context.Background(), nil)
	if err != nil {
		t.Fatalf("ExecuteWithMeta failed: %v", err)
	}
	if output != "ok" || metadata["done"] != true {
		t.Fatalf("unexpected function output=%q metadata=%#v", output, metadata)
	}
}

func TestCapabilityMCPManagerFindAndCall(t *testing.T) {
	session := &fakeToolSessionContext{sessionID: "session-1", path: ExposureActor}
	manager := &CapabilityMCPManager{
		Registry: NewRegistry(Capability{
			Name:        "cap",
			Description: "test capability",
			Parameters:  map[string]interface{}{"type": "object"},
			Metadata:    map[string]interface{}{"source": "test"},
			Exposure:    []ExposurePath{ExposureActor},
			Execute: func(ctx context.Context, got ToolSessionContext, args map[string]interface{}) (ToolResult, error) {
				if got == nil || got.SessionID() != "session-1" || got.ExecutorPath() != ExposureActor {
					t.Fatalf("unexpected session context: %#v", got)
				}
				return ToolResult{Output: "ok"}, nil
			},
		}),
		ContextFactory: func(ctx context.Context) ToolSessionContext { return session },
		Path:           ExposureActor,
		MCPName:        "test_mcp",
	}

	info, err := manager.FindTool("cap")
	if err != nil {
		t.Fatalf("FindTool failed: %v", err)
	}
	if info.Name != "cap" || info.MCPName != "test_mcp" || info.Metadata["source"] != "test" {
		t.Fatalf("unexpected tool info: %+v", info)
	}
	output, err := manager.CallTool(context.Background(), "", "cap", nil)
	if err != nil {
		t.Fatalf("CallTool failed: %v", err)
	}
	if output != "ok" {
		t.Fatalf("unexpected output: %#v", output)
	}
}

func TestCapabilityMCPManagerCanBeDisabled(t *testing.T) {
	manager := &CapabilityMCPManager{
		Registry: NewRegistry(Capability{Name: "cap", Exposure: []ExposurePath{ExposureActor}}),
		Path:     ExposureActor,
		Enabled:  func() bool { return false },
	}
	if tools := manager.ListTools(); len(tools) != 0 {
		t.Fatalf("expected disabled manager to hide tools, got %+v", tools)
	}
	if _, err := manager.FindTool("cap"); err == nil {
		t.Fatal("expected disabled manager to hide cap")
	}
}

type fakeToolSessionContext struct {
	sessionID string
	path      ExposurePath
}

func (f *fakeToolSessionContext) SessionID() string {
	return f.sessionID
}

func (f *fakeToolSessionContext) RuntimeSession() *runtimechat.Session {
	return nil
}

func (f *fakeToolSessionContext) SessionStorage() runtimechat.SessionStorage {
	return nil
}

func (f *fakeToolSessionContext) RefreshRuntimeSession(ctx context.Context, updated *runtimechat.Session) error {
	return nil
}

func (f *fakeToolSessionContext) ExecutorPath() ExposurePath {
	return f.path
}

var _ runtimeskill.MCPManager = (*CapabilityMCPManager)(nil)
