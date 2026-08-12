package toolschema

import (
	"reflect"
	"testing"
)

func TestCanonicalizeRepairsAndDoesNotMutateInput(t *testing.T) {
	input := map[string]interface{}{
		"properties": map[string]interface{}{
			"z": map[string]interface{}{"type": "string"},
			"a": map[string]interface{}{"type": "integer"},
		},
		"required": true,
	}

	canonical, warnings, err := CanonicalizeAndValidate(input)
	if err != nil {
		t.Fatalf("CanonicalizeAndValidate failed: %v", err)
	}
	if canonical["type"] != "object" {
		t.Fatalf("expected object root, got %#v", canonical["type"])
	}
	if canonical["additionalProperties"] != false {
		t.Fatalf("expected closed object root, got %#v", canonical["additionalProperties"])
	}
	if len(warnings) < 2 {
		t.Fatalf("expected compatibility warnings, got %#v", warnings)
	}
	if _, exists := input["type"]; exists {
		t.Fatal("canonicalization mutated caller schema")
	}
	if _, exists := input["additionalProperties"]; exists {
		t.Fatal("canonicalization mutated caller schema")
	}
	properties := canonical["properties"].(map[string]interface{})
	if _, ok := properties["a"]; !ok {
		t.Fatalf("canonical properties lost field: %#v", properties)
	}
}

func TestCanonicalizePreservesKeywordNamedPropertiesAndInstanceData(t *testing.T) {
	canonical, _, err := CanonicalizeAndValidate(map[string]interface{}{
		"properties": map[string]interface{}{
			"required":          map[string]interface{}{"type": "string"},
			"dependentRequired": map[string]interface{}{"type": "boolean"},
			"choice": map[string]interface{}{
				"enum": []interface{}{map[string]interface{}{"required": true}},
			},
		},
		"dependentRequired": map[string]interface{}{
			"required": []interface{}{"z", "a"},
		},
	})
	if err != nil {
		t.Fatalf("CanonicalizeAndValidate failed: %v", err)
	}

	properties := canonical["properties"].(map[string]interface{})
	for _, name := range []string{"required", "dependentRequired"} {
		if _, exists := properties[name]; !exists {
			t.Fatalf("property named %q was treated as a schema keyword: %#v", name, properties)
		}
	}
	choice := properties["choice"].(map[string]interface{})
	enumValue := choice["enum"].([]interface{})[0].(map[string]interface{})
	if enumValue["required"] != true {
		t.Fatalf("instance data was rewritten during schema traversal: %#v", enumValue)
	}
	dependent := canonical["dependentRequired"].(map[string]interface{})
	if got := dependent["required"]; !reflect.DeepEqual(got, []interface{}{"a", "z"}) && !reflect.DeepEqual(got, []string{"a", "z"}) {
		t.Fatalf("dependentRequired was not normalized: %#v", got)
	}
}

func TestCanonicalizeSortsRequiredAndHashIsStable(t *testing.T) {
	left := map[string]interface{}{
		"required":   []interface{}{"z", "a", "z"},
		"properties": map[string]interface{}{},
	}
	right := map[string]interface{}{
		"properties": map[string]interface{}{},
		"required":   []interface{}{"a", "z"},
	}
	leftRaw, _, err := CanonicalBytes(left)
	if err != nil {
		t.Fatal(err)
	}
	rightRaw, _, err := CanonicalBytes(right)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(leftRaw, rightRaw) {
		t.Fatalf("canonical bytes differ:\n%s\n%s", leftRaw, rightRaw)
	}
	leftHash, err := Hash(left)
	if err != nil {
		t.Fatal(err)
	}
	rightHash, err := Hash(right)
	if err != nil {
		t.Fatal(err)
	}
	if leftHash != rightHash {
		t.Fatalf("canonical hashes differ: %s != %s", leftHash, rightHash)
	}
}

func TestValidateRejectsNonObjectAndExternalRefs(t *testing.T) {
	if _, _, err := CanonicalizeAndValidate(map[string]interface{}{
		"type":       "array",
		"items":      map[string]interface{}{"type": "string"},
		"properties": map[string]interface{}{},
	}); err == nil {
		t.Fatal("expected non-object root to be rejected")
	}
	if _, _, err := CanonicalizeAndValidate(map[string]interface{}{
		"$ref":       "https://example.invalid/schema.json",
		"properties": map[string]interface{}{},
	}); err == nil {
		t.Fatal("expected external ref to be rejected")
	}
}

func TestValidateAllowsLocalRefs(t *testing.T) {
	_, _, err := CanonicalizeAndValidate(map[string]interface{}{
		"$defs": map[string]interface{}{
			"message": map[string]interface{}{"type": "string"},
		},
		"properties": map[string]interface{}{
			"message": map[string]interface{}{"$ref": "#/$defs/message"},
		},
	})
	if err != nil {
		t.Fatalf("expected local ref to validate: %v", err)
	}
}

func TestValidateDefaultsUndeclaredSchemasToDraft7(t *testing.T) {
	_, _, err := CanonicalizeAndValidate(map[string]interface{}{
		"properties": map[string]interface{}{
			"tuple": map[string]interface{}{
				"type": "array",
				"items": []interface{}{
					map[string]interface{}{"type": "string"},
					map[string]interface{}{"type": "integer"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("legacy draft-07 tuple schema was rejected: %v", err)
	}
}
