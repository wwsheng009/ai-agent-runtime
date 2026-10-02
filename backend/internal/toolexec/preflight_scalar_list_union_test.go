package toolexec

import (
	"strings"
	"testing"
)

// scalarListUnionSchema mirrors grep's `patterns`: internally a string|array
// union, advertised to the provider as a collapsed scalar branch
// (toolschema.ScalarizeUnions). Built with []map[string]interface{} so the raw
// descriptor form and the JSON round-tripped []interface{} form both matter.
func scalarListUnionSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"patterns": map[string]interface{}{
				"anyOf": []map[string]interface{}{
					{"type": "string"},
					{"type": "array", "items": map[string]interface{}{"type": "string"}},
				},
				"description": "批量搜索多个模式",
			},
			"path": map[string]interface{}{"type": "string"},
		},
	}
}

// TestApplyPreflightUnwrapsSingleKeyObjectWrapper pins the live residual
// (2026-10-02): a model shown `patterns: {type: string}` for a batch parameter
// wrapped the list in an object, and preflight rejected the call verbatim twice
// with `patterns expected string|array, got object`. The runtime already accepts
// the array, so the wrapper must be unwrapped rather than denied.
func TestApplyPreflightUnwrapsSingleKeyObjectWrapper(t *testing.T) {
	for _, wrapperKey := range []string{"item", "items", "value", "values"} {
		args := map[string]interface{}{
			"patterns": map[string]interface{}{wrapperKey: []interface{}{"alpha", "beta"}},
		}
		decision := ApplyPreflight(NewMemory(2), PreflightRequest{
			ToolName:    "grep",
			Args:        args,
			InputSchema: scalarListUnionSchema(),
		})
		if !decision.Allow {
			t.Fatalf("wrapper key %q: expected preflight to allow the unwrapped call, got %q", wrapperKey, decision.Error)
		}
		list, ok := args["patterns"].([]interface{})
		if !ok || len(list) != 2 || list[0] != "alpha" || list[1] != "beta" {
			t.Fatalf("wrapper key %q: expected patterns rewritten to the wrapped array, got %#v", wrapperKey, args["patterns"])
		}
	}
}

// TestApplyPreflightUnwrapsStringWrapper covers the same guess with a scalar:
// {"item":"alpha"} for a string|array property collapses to the string itself.
func TestApplyPreflightUnwrapsStringWrapper(t *testing.T) {
	args := map[string]interface{}{
		"patterns": map[string]interface{}{"item": "alpha"},
	}
	decision := ApplyPreflight(NewMemory(2), PreflightRequest{
		ToolName:    "grep",
		Args:        args,
		InputSchema: scalarListUnionSchema(),
	})
	if !decision.Allow {
		t.Fatalf("expected preflight to allow the unwrapped call, got %q", decision.Error)
	}
	if got, ok := args["patterns"].(string); !ok || got != "alpha" {
		t.Fatalf("expected patterns rewritten to the wrapped string, got %#v", args["patterns"])
	}
}

// TestApplyPreflightKeepsDeclaredObjectArgs guards the rewrite boundary: when the
// property really does declare an object, the caller's object is legitimate and
// must survive untouched.
func TestApplyPreflightKeepsDeclaredObjectArgs(t *testing.T) {
	schema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"selector": map[string]interface{}{
				"anyOf": []interface{}{
					map[string]interface{}{"type": "string"},
					map[string]interface{}{
						"type":       "object",
						"properties": map[string]interface{}{"item": map[string]interface{}{"type": "string"}},
					},
				},
			},
		},
	}
	args := map[string]interface{}{
		"selector": map[string]interface{}{"item": "alpha"},
	}
	decision := ApplyPreflight(NewMemory(2), PreflightRequest{
		ToolName:    "grep",
		Args:        args,
		InputSchema: schema,
	})
	if !decision.Allow {
		t.Fatalf("expected declared-object arg to stay valid, got %q", decision.Error)
	}
	obj, ok := args["selector"].(map[string]interface{})
	if !ok || obj["item"] != "alpha" {
		t.Fatalf("declared-object arg must not be unwrapped, got %#v", args["selector"])
	}
}

// TestApplyPreflightLeavesAmbiguousObjectWrapper keeps the rewrite conservative:
// a multi-key object has no single defensible reading, so it must still be denied.
func TestApplyPreflightLeavesAmbiguousObjectWrapper(t *testing.T) {
	args := map[string]interface{}{
		"patterns": map[string]interface{}{
			"item":  []interface{}{"alpha"},
			"regex": true,
		},
	}
	decision := ApplyPreflight(NewMemory(2), PreflightRequest{
		ToolName:    "grep",
		Args:        args,
		InputSchema: scalarListUnionSchema(),
	})
	if decision.Allow {
		t.Fatal("expected an ambiguous multi-key object to stay denied")
	}
	if decision.Preflight != "arg_types" {
		t.Fatalf("expected arg_types preflight, got %q (%s)", decision.Preflight, decision.Error)
	}
	if _, ok := args["patterns"].(map[string]interface{}); !ok {
		t.Fatalf("ambiguous wrapper must not be rewritten, got %#v", args["patterns"])
	}
}

// TestScalarListUnionErrorSpellsOutAcceptedSpellings pins the message half of
// the fix. The model is shown `patterns: {type: string}` but is judged against
// the internal union, so the error has to state both accepted spellings
// explicitly — otherwise it is judged by a contract it was never shown.
func TestScalarListUnionErrorSpellsOutAcceptedSpellings(t *testing.T) {
	args := map[string]interface{}{
		"patterns": map[string]interface{}{"item": []interface{}{"alpha"}, "regex": true},
	}
	decision := ApplyPreflight(NewMemory(2), PreflightRequest{
		ToolName:    "grep",
		Args:        args,
		InputSchema: scalarListUnionSchema(),
	})
	if decision.Allow {
		t.Fatal("expected the denied call to stay denied")
	}
	if !strings.Contains(decision.Error, "string or an array of strings") {
		t.Fatalf("error must name both accepted spellings, got %q", decision.Error)
	}
	if !strings.Contains(decision.Error, `["a","b"]`) {
		t.Fatalf("error must show the bare-array spelling, got %q", decision.Error)
	}
	if strings.Contains(decision.Error, "string|array") {
		t.Fatalf("error must not restate the internal union spelling, got %q", decision.Error)
	}
	if !strings.Contains(decision.Error, "do not wrap them in an object") {
		t.Fatalf("error must name the failure mode it is repairing, got %q", decision.Error)
	}
}

// TestNonUnionTypeErrorUnchanged keeps the generic message intact for properties
// that are not the string|array ergonomics union.
func TestNonUnionTypeErrorUnchanged(t *testing.T) {
	schema := map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{"literal": map[string]interface{}{"type": "boolean"}},
	}
	decision := ApplyPreflight(NewMemory(2), PreflightRequest{
		ToolName:    "grep",
		Args:        map[string]interface{}{"literal": map[string]interface{}{"item": []interface{}{"a"}}},
		InputSchema: schema,
	})
	if decision.Allow {
		t.Fatal("expected a boolean property fed an object to stay denied")
	}
	if decision.Error != "invalid argument type(s): literal expected boolean, got object" {
		t.Fatalf("unexpected generic error: %q", decision.Error)
	}
}