package functions

import (
	"context"
	"testing"

	runtimetools "github.com/wwsheng009/ai-agent-runtime/internal/tools"
)

type plainTestFunction struct {
	name   string
	output string
}

func (f *plainTestFunction) Name() string { return f.name }

func (f *plainTestFunction) Description() string { return "plain test function" }

func (f *plainTestFunction) Parameters() map[string]interface{} {
	return map[string]interface{}{"type": "object"}
}

func (f *plainTestFunction) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	return f.output, nil
}

type richRuntimeToolProvider struct {
	output   string
	metadata map[string]interface{}
}

func (p *richRuntimeToolProvider) Execute(ctx context.Context, name string, args map[string]interface{}) (string, error) {
	return p.output, nil
}

func (p *richRuntimeToolProvider) ExecuteWithMeta(ctx context.Context, name string, args map[string]interface{}) (string, map[string]interface{}, error) {
	return p.output, p.metadata, nil
}

func TestFunctionRegistry_ExecuteFunctionWithMeta_FallsBackForPlainFunction(t *testing.T) {
	registry := NewFunctionRegistry()
	registry.Register(&plainTestFunction{name: "plain", output: "ok"})

	output, metadata, err := registry.ExecuteFunctionWithMeta(context.Background(), "plain", nil)
	if err != nil {
		t.Fatalf("ExecuteFunctionWithMeta failed: %v", err)
	}
	if output != "ok" {
		t.Fatalf("expected output ok, got %q", output)
	}
	if metadata != nil {
		t.Fatalf("expected nil metadata for plain function, got %#v", metadata)
	}
}

func TestRuntimeToolFunction_ExecuteWithMeta_PreservesProviderMetadata(t *testing.T) {
	fn := NewRuntimeToolFunction(&richRuntimeToolProvider{
		output: "job queued",
		metadata: map[string]interface{}{
			"tool_source": "broker",
			"output_kind": "text",
		},
	}, runtimetools.ToolDescriptor{
		Name:        "background_task",
		Description: "background task",
		Parameters:  map[string]interface{}{"type": "object"},
	})

	output, metadata, err := fn.ExecuteWithMeta(context.Background(), map[string]interface{}{"command": "git status"})
	if err != nil {
		t.Fatalf("ExecuteWithMeta failed: %v", err)
	}
	if output != "job queued" {
		t.Fatalf("expected output job queued, got %q", output)
	}
	if got := metadata["tool_source"]; got != "broker" {
		t.Fatalf("expected tool_source=broker, got %#v", got)
	}
	if got := metadata["output_kind"]; got != "text" {
		t.Fatalf("expected output_kind=text, got %#v", got)
	}
}
