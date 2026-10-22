package toolkit

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
)

type runtimeToolTestHandler struct {
	def toolcore.Definition
	got toolcore.Runtime
}

func (h *runtimeToolTestHandler) Definition() toolcore.Definition { return h.def }

func (h *runtimeToolTestHandler) Execute(_ context.Context, call *toolcore.Call) (*toolcore.Result, error) {
	h.got = call.Runtime
	return &toolcore.Result{
		Success:    true,
		OutputKind: toolcore.KindText,
		Content:    "ok",
		Metadata:   map[string]any{"x": "y"},
	}, nil
}

type runtimeToolTestRuntime struct{}

func (runtimeToolTestRuntime) Paths() toolcore.PathResolver            { return nil }
func (runtimeToolTestRuntime) Permissions() toolcore.PermissionChecker { return nil }
func (runtimeToolTestRuntime) Commands() toolcore.CommandPolicy        { return nil }
func (runtimeToolTestRuntime) Env() toolcore.EnvProvider               { return nil }
func (runtimeToolTestRuntime) Output() toolcore.OutputShaper           { return nil }
func (runtimeToolTestRuntime) Artifacts() toolcore.ArtifactSink        { return nil }
func (runtimeToolTestRuntime) Sessions() toolcore.SessionState         { return nil }
func (runtimeToolTestRuntime) Clock() toolcore.Clock                   { return nil }
func (runtimeToolTestRuntime) IDs() toolcore.IDGenerator               { return nil }

// TestRuntimeToolInjectsPerCallRuntime pins the Phase 6b bridge: a core
// Handler receives a fresh Runtime for every Execute, and the result is mapped
// back onto the legacy ToolResult shape.
func TestRuntimeToolInjectsPerCallRuntime(t *testing.T) {
	def := toolcore.Definition{
		Name:        "probe",
		Version:     "1.0.0",
		InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
	}
	handler := &runtimeToolTestHandler{def: def}
	factoryCalls := 0
	tool, err := NewRuntimeTool(handler, func(context.Context) (toolcore.Runtime, error) {
		factoryCalls++
		return runtimeToolTestRuntime{}, nil
	})
	if err != nil {
		t.Fatalf("NewRuntimeTool: %v", err)
	}
	result, err := tool.Execute(context.Background(), map[string]interface{}{"a": "b"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if factoryCalls != 1 {
		t.Fatalf("runtime factory calls = %d, want 1", factoryCalls)
	}
	if handler.got == nil {
		t.Fatal("handler did not receive the injected Runtime")
	}
	if result == nil || !result.Success || result.Content != "ok" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if result.Metadata["x"] != "y" {
		t.Fatalf("metadata not preserved: %+v", result.Metadata)
	}
	if tool.DefinitionMetadata()["contract_version"] != toolcore.ContractVersion {
		t.Fatalf("RuntimeTool must expose contract_version metadata")
	}
}
