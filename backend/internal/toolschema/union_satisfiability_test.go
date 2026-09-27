package toolschema

import (
	"encoding/json"
	"testing"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v5"
)

func scalarizedSchema(t *testing.T, raw string) (map[string]interface{}, *jsonschema.Schema) {
	t.Helper()
	var schema map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &schema); err != nil {
		t.Fatal(err)
	}
	out := ScalarizeUnions(schema)
	data, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := jsonschema.CompileString("union.json", string(data))
	if err != nil {
		t.Fatalf("scalarized schema does not compile: %s: %v", data, err)
	}
	return out, compiled
}

func propertySchema(t *testing.T, out map[string]interface{}, name string) map[string]interface{} {
	t.Helper()
	properties, _ := out["properties"].(map[string]interface{})
	property, _ := properties[name].(map[string]interface{})
	if property == nil {
		t.Fatalf("property %q missing from %#v", name, out)
	}
	return property
}

// TestScalarizeUnionsKeepsSiblingEnumSatisfiable pins the reported regression:
// enum [1,2] next to a string|integer union was folded to type=string while the
// numeric enum survived, leaving a required property with no legal value.
func TestScalarizeUnionsKeepsSiblingEnumSatisfiable(t *testing.T) {
	out, compiled := scalarizedSchema(t,
		`{"type":"object","required":["n"],"properties":{"n":{"enum":[1,2],"anyOf":[{"type":"integer"},{"type":"string"}]}}}`)
	if err := compiled.Validate(map[string]interface{}{"n": float64(1)}); err != nil {
		t.Fatalf("originally valid numeric enum value stopped validating: %s (%v)", mustJSON(t, out), err)
	}
	if err := compiled.Validate(map[string]interface{}{"n": float64(2)}); err != nil {
		t.Fatalf("originally valid numeric enum value stopped validating: %s (%v)", mustJSON(t, out), err)
	}
}

// TestScalarizeUnionsStillFoldsPlainStringUnion keeps the intended shorthand:
// without conflicting siblings the string branch is still advertised alone.
func TestScalarizeUnionsStillFoldsPlainStringUnion(t *testing.T) {
	out, compiled := scalarizedSchema(t,
		`{"type":"object","properties":{"q":{"anyOf":[{"type":"string"},{"type":"array","items":{"type":"string"}}]}}}`)
	property := propertySchema(t, out, "q")
	if _, exists := property["anyOf"]; exists {
		t.Fatalf("a plain string|array union should still fold, got %s", mustJSON(t, out))
	}
	if err := compiled.Validate(map[string]interface{}{"q": "text"}); err != nil {
		t.Fatalf("folded scalar schema rejects the scalar spelling: %v", err)
	}
}

// TestScalarizeUnionsKeepsConflictingSiblingType: the preferred string branch
// contradicts a sibling type of integer, so it must not be folded in. A
// compatible branch may still be folded (2026-09-27 review H12 prefers the
// narrowest safe shorthand); what must never happen is rejecting the integers
// the advertised property legitimately accepts.
func TestScalarizeUnionsKeepsConflictingSiblingType(t *testing.T) {
	out, compiled := scalarizedSchema(t,
		`{"type":"object","properties":{"n":{"type":"integer","anyOf":[{"type":"string"},{"type":"integer"}]}}}`)
	property := propertySchema(t, out, "n")
	if _, exists := property["anyOf"]; !exists {
		if property["type"] != "integer" {
			t.Fatalf("only the integer branch is compatible with the sibling type, got %s", mustJSON(t, out))
		}
	}
	if err := compiled.Validate(map[string]interface{}{"n": float64(3)}); err != nil {
		t.Fatalf("integer sibling stopped validating: %v", err)
	}
}

// TestScalarizeUnionsKeepsConstConflict: const carries the same restriction as
// enum and must be considered before folding; a branch consistent with the
// const may be folded, a conflicting branch may not (2026-09-27 review H12).
func TestScalarizeUnionsKeepsConstConflict(t *testing.T) {
	out, compiled := scalarizedSchema(t,
		`{"type":"object","required":["n"],"properties":{"n":{"const":1,"anyOf":[{"type":"integer"},{"type":"string"}]}}}`)
	property := propertySchema(t, out, "n")
	if _, exists := property["anyOf"]; !exists {
		if property["type"] != "integer" {
			t.Fatalf("only the integer branch is consistent with const 1, got %s", mustJSON(t, out))
		}
	}
	if err := compiled.Validate(map[string]interface{}{"n": float64(1)}); err != nil {
		t.Fatalf("const value stopped validating: %s (%v)", mustJSON(t, out), err)
	}
}

func mustJSON(t *testing.T, value interface{}) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
