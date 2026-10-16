package functions

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	runtimetools "github.com/wwsheng009/ai-agent-runtime/internal/tools"
)

type plainTestFunction struct {
	name   string
	output string
	args   map[string]interface{}
}

func (f *plainTestFunction) Name() string { return f.name }

func (f *plainTestFunction) Description() string { return "plain test function" }

func (f *plainTestFunction) Parameters() map[string]interface{} {
	return map[string]interface{}{"type": "object"}
}

func (f *plainTestFunction) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	f.args = args
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

func TestFunctionRegistry_ExecuteFunctionWithMeta_UnwrapsRawArgs(t *testing.T) {
	registry := NewFunctionRegistry()
	fn := &plainTestFunction{name: "plain", output: "ok"}
	registry.Register(fn)

	_, _, err := registry.ExecuteFunctionWithMeta(context.Background(), "plain", map[string]interface{}{
		"_raw": `{"command":"git status"}`,
	})
	if err != nil {
		t.Fatalf("ExecuteFunctionWithMeta failed: %v", err)
	}
	if fn.args["command"] != "git status" {
		t.Fatalf("expected normalized command arg, got %#v", fn.args)
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

func TestFunctionRegistry_GetFunctionSchemas_PreservesDefinitionMetadata(t *testing.T) {
	registry := NewFunctionRegistry()
	registry.Register(NewRuntimeToolFunction(&richRuntimeToolProvider{
		output: "ok",
	}, runtimetools.ToolDescriptor{
		Name:        "apply_patch",
		Description: "apply patch",
		Parameters:  map[string]interface{}{"type": "object"},
		Metadata: map[string]interface{}{
			"freeform": map[string]interface{}{
				"type":   "grammar",
				"syntax": "lark",
			},
		},
	}))

	schemas := registry.GetFunctionSchemas()
	if len(schemas) != 1 {
		t.Fatalf("expected 1 schema, got %d", len(schemas))
	}
	metadata, ok := schemas[0]["metadata"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected metadata map, got %#v", schemas[0]["metadata"])
	}
	freeform, ok := metadata["freeform"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected freeform metadata, got %#v", metadata)
	}
	if got := freeform["syntax"]; got != "lark" {
		t.Fatalf("expected freeform syntax=lark, got %#v", got)
	}
}

func TestFunctionCallBuilders_DefaultNilParametersToObjectSchema(t *testing.T) {
	schemas := []map[string]interface{}{
		{"name": "get_goal", "description": "read goal"},
	}

	for _, tc := range []struct {
		provider string
		builder  FunctionCallBuilder
	}{
		{provider: "openai", builder: &OpenAIFunctionCallBuilder{}},
		{provider: "anthropic", builder: &AnthropicFunctionCallBuilder{}},
		{provider: "gemini", builder: &GeminiFunctionCallBuilder{}},
		{provider: "codex", builder: &CodexFunctionCallBuilder{}},
	} {
		built := tc.builder.BuildFunctions(schemas)
		var params interface{}
		switch tc.provider {
		case "openai":
			tools, ok := built.([]interface{})
			if !ok || len(tools) != 1 {
				t.Fatalf("%s: unexpected tools %#v", tc.provider, built)
			}
			tool, _ := tools[0].(map[string]interface{})
			fn, _ := tool["function"].(map[string]interface{})
			params = fn["parameters"]
		case "anthropic":
			tools, ok := built.([]interface{})
			if !ok || len(tools) != 1 {
				t.Fatalf("%s: unexpected tools %#v", tc.provider, built)
			}
			tool, _ := tools[0].(map[string]interface{})
			params = tool["input_schema"]
		case "gemini":
			tools, ok := built.([]interface{})
			if !ok || len(tools) != 1 {
				t.Fatalf("%s: unexpected tools %#v", tc.provider, built)
			}
			tool, _ := tools[0].(map[string]interface{})
			params = tool["parameters"]
		case "codex":
			tools, ok := built.([]map[string]interface{})
			if !ok || len(tools) != 1 {
				t.Fatalf("%s: unexpected tools %#v", tc.provider, built)
			}
			if tools[0]["strict"] != false {
				t.Fatalf("%s: expected Codex-compatible strict=false default, got %#v", tc.provider, tools[0]["strict"])
			}
			params = tools[0]["parameters"]
		}

		schema, ok := params.(map[string]interface{})
		if !ok {
			t.Fatalf("%s: expected parameters map, got %#v", tc.provider, params)
		}
		if schema["type"] != "object" || schema["additionalProperties"] != false {
			t.Fatalf("%s: unexpected default parameters schema %#v", tc.provider, schema)
		}
	}
}

func TestValidateToolCalls_SkipsInvalidCalls(t *testing.T) {
	valid, err := ValidateToolCalls([]ToolCall{
		{
			ID:       "missing-args",
			Function: "shell",
			Raw:      map[string]interface{}{"function": map[string]interface{}{}},
		},
		{
			ID:       "partial-json",
			Function: "shell",
			Raw: map[string]interface{}{
				"function": map[string]interface{}{"arguments": `{"command":"git status"`},
			},
		},
		{
			ID:       "valid",
			Function: "shell",
			Raw: map[string]interface{}{
				"function": map[string]interface{}{"arguments": `{"command":"git status"}`},
			},
		},
	})
	if err != nil {
		t.Fatalf("ValidateToolCalls: %v", err)
	}
	if len(valid) != 1 || valid[0].ID != "valid" {
		t.Fatalf("valid calls = %#v, want only valid", valid)
	}
}

func TestOpenAIFunctionCallBuilder_ExtractToolCallsKeepsPartialArgumentsSilent(t *testing.T) {
	builder := &OpenAIFunctionCallBuilder{}
	calls := builder.ExtractToolCalls(map[string]interface{}{
		"choices": []map[string]interface{}{{
			"message": map[string]interface{}{
				"tool_calls": []map[string]interface{}{{
					"id": "partial",
					"function": map[string]interface{}{
						"name":      "execute_shell_command",
						"arguments": `{"command":"git status"`,
					},
				}},
			},
		}},
	})
	if len(calls) != 1 {
		t.Fatalf("calls = %#v, want one parsed raw call", calls)
	}
	if calls[0].ID != "partial" || calls[0].Function != "execute_shell_command" {
		t.Fatalf("unexpected call %#v", calls[0])
	}
	if len(calls[0].Args) != 0 {
		t.Fatalf("partial arguments must not be treated as parsed args: %#v", calls[0].Args)
	}
}

func TestFunctionCallBuilder_HasNoDirectTerminalWriter(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	source, err := os.ReadFile(filepath.Join(filepath.Dir(currentFile), "builder.go"))
	if err != nil {
		t.Fatalf("read builder.go: %v", err)
	}
	for _, forbidden := range []string{
		"fmt.Print",
		"os.Stdout",
		"os.Stderr",
		"ui.WriteTerminal",
		"io.WriteString",
	} {
		if strings.Contains(string(source), forbidden) {
			t.Fatalf("function builder must not write directly to a terminal: found %q", forbidden)
		}
	}
}

func TestCodexFunctionCallBuilderPreservesExplicitStrict(t *testing.T) {
	built := (&CodexFunctionCallBuilder{}).BuildFunctions([]map[string]interface{}{
		{
			"name":        "strict_lookup",
			"description": "lookup by id",
			"strict":      true,
			"parameters": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"id": map[string]interface{}{"type": "string"},
				},
				"required": []string{"id"},
			},
		},
	}).([]map[string]interface{})

	if len(built) != 1 || built[0]["strict"] != true {
		t.Fatalf("expected explicit strict=true to be preserved, got %#v", built)
	}
}

func TestNormalizeFunctionParameters_RepairsNullProperties(t *testing.T) {
	params := normalizeFunctionParameters(map[string]interface{}{
		"type":                 "object",
		"properties":           nil,
		"additionalProperties": false,
	})

	if params["type"] != "object" || params["additionalProperties"] != false {
		t.Fatalf("unexpected parameters schema %#v", params)
	}
	if props, ok := params["properties"].(map[string]interface{}); !ok || props == nil {
		t.Fatalf("expected null properties to become empty object, got %#v", params["properties"])
	}
}
