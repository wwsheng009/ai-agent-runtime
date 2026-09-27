package toolschema

import (
	"encoding/json"
	"testing"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v5"
)

// TestScalarizeUnionsKeepsValueSetsSatisfiable pins finding H12: folding a
// branch that contradicts a sibling const/enum (or the sibling bounds) produced
// a schema with no legal value. The fold must either pick a compatible branch or
// leave the union in place.
func TestScalarizeUnionsKeepsValueSetsSatisfiable(t *testing.T) {
	cases := []struct {
		name  string
		raw   string
		value interface{}
	}{
		{
			name:  "const_vs_branch_enum",
			raw:   `{"type":"object","required":["value"],"properties":{"value":{"const":"second","anyOf":[{"type":"string","enum":["first"]},{"type":"string","enum":["second"]}]}}}`,
			value: "second",
		},
		{
			name:  "length_constraints",
			raw:   `{"type":"object","required":["value"],"properties":{"value":{"maxLength":1,"anyOf":[{"type":"string","minLength":2},{"type":"string"}]}}}`,
			value: "x",
		},
		{
			name:  "numeric_bounds",
			raw:   `{"type":"object","required":["value"],"properties":{"value":{"maximum":1,"anyOf":[{"type":"integer","minimum":5},{"type":"integer"}]}}}`,
			value: 1.0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before, err := jsonschema.CompileString("before.json", tc.raw)
			if err != nil {
				t.Fatal(err)
			}
			payload := map[string]interface{}{"value": tc.value}
			if err := before.Validate(payload); err != nil {
				t.Fatalf("fixture must be valid before folding: %v", err)
			}
			var raw map[string]interface{}
			if err := json.Unmarshal([]byte(tc.raw), &raw); err != nil {
				t.Fatal(err)
			}
			data, err := json.Marshal(ScalarizeUnions(raw))
			if err != nil {
				t.Fatal(err)
			}
			after, err := jsonschema.CompileString("after.json", string(data))
			if err != nil {
				t.Fatalf("folded schema does not compile: %v (%s)", err, data)
			}
			if err := after.Validate(payload); err != nil {
				t.Fatalf("fold made a satisfiable value impossible: %v (%s)", err, data)
			}
		})
	}
}

// TestScalarizeUnionsStillFoldsTheScalarShorthand: the provider-facing reason for
// the fold (string|array with sibling keys) must keep working.
func TestScalarizeUnionsStillFoldsTheScalarShorthand(t *testing.T) {
	raw := map[string]interface{}{
		"type":        "object",
		"description": "read many",
		"properties": map[string]interface{}{
			"paths": map[string]interface{}{
				"description": "one path or many",
				"anyOf": []interface{}{
					map[string]interface{}{"type": "string"},
					map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
				},
			},
		},
	}
	folded := ScalarizeUnions(raw)
	paths := folded["properties"].(map[string]interface{})["paths"].(map[string]interface{})
	if _, exists := paths["anyOf"]; exists {
		t.Fatalf("scalar shorthand must still fold, got %#v", paths)
	}
	if paths["type"] != "string" {
		t.Fatalf("expected the string branch, got %#v", paths)
	}
}

// TestScalarizeUnionsFallsBackToACompatibleBranch: when the preferred string
// branch contradicts the sibling enum but another branch does not, the fold must
// choose the compatible branch instead of refusing or fabricating a conflict.
func TestScalarizeUnionsFallsBackToACompatibleBranch(t *testing.T) {
	raw := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"value": map[string]interface{}{
				"enum": []interface{}{float64(2)},
				"anyOf": []interface{}{
					map[string]interface{}{"type": "string", "enum": []interface{}{"first"}},
					map[string]interface{}{"type": "integer"},
				},
			},
		},
	}
	folded := ScalarizeUnions(raw)
	value := folded["properties"].(map[string]interface{})["value"].(map[string]interface{})
	if _, exists := value["anyOf"]; exists {
		t.Fatalf("a compatible branch exists, folding should have happened: %#v", value)
	}
	if value["type"] != "integer" {
		t.Fatalf("expected the integer branch, got %#v", value)
	}
	data, err := json.Marshal(folded)
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := jsonschema.CompileString("compat.json", string(data))
	if err != nil {
		t.Fatal(err)
	}
	if err := compiled.Validate(map[string]interface{}{"value": 2.0}); err != nil {
		t.Fatalf("the original value must stay valid: %v (%s)", err, data)
	}
}
