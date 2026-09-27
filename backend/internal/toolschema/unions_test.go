package toolschema

import (
	"testing"
)

func anyOfStringArray(description string) map[string]interface{} {
	return map[string]interface{}{
		"anyOf": []interface{}{
			map[string]interface{}{"type": "string"},
			map[string]interface{}{
				"type":  "array",
				"items": map[string]interface{}{"type": "string"},
			},
		},
		"description": description,
	}
}

func TestScalarizeUnionsPrefersStringBranch(t *testing.T) {
	schema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"patterns": anyOfStringArray("批量模式"),
			"paths":    anyOfStringArray("批量路径"),
		},
	}
	got := ScalarizeUnions(schema)
	properties, _ := got["properties"].(map[string]interface{})
	patterns, _ := properties["patterns"].(map[string]interface{})
	if _, exists := patterns["anyOf"]; exists {
		t.Fatalf("anyOf must be removed, got %#v", patterns)
	}
	if patterns["type"] != "string" {
		t.Fatalf("expected the string branch, got %#v", patterns)
	}
	if patterns["description"] != "批量模式" {
		t.Fatalf("sibling description must survive, got %#v", patterns)
	}
}

func TestScalarizeUnionsPrefersStringOverInteger(t *testing.T) {
	schema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"head": map[string]interface{}{
				"anyOf": []interface{}{
					map[string]interface{}{"type": "integer"},
					map[string]interface{}{"type": "string"},
				},
				"description": "行数",
			},
		},
	}
	got := ScalarizeUnions(schema)
	properties, _ := got["properties"].(map[string]interface{})
	head, _ := properties["head"].(map[string]interface{})
	if head["type"] != "string" || head["description"] != "行数" {
		t.Fatalf("expected string branch with description, got %#v", head)
	}
}

func TestScalarizeUnionsDescendsIntoNestedSchemas(t *testing.T) {
	schema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"files": map[string]interface{}{
				"type": "array",
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"file_path": map[string]interface{}{"type": "string"},
						"glob":      anyOfStringArray("深层 union"),
					},
				},
			},
		},
	}
	got := ScalarizeUnions(schema)
	files, _ := got["properties"].(map[string]interface{})["files"].(map[string]interface{})
	items, _ := files["items"].(map[string]interface{})
	glob, _ := items["properties"].(map[string]interface{})["glob"].(map[string]interface{})
	if _, exists := glob["anyOf"]; exists {
		t.Fatalf("nested union must be scalarized, got %#v", glob)
	}
	if glob["type"] != "string" {
		t.Fatalf("expected nested string branch, got %#v", glob)
	}
}

// TestScalarizeUnionsDescendsIntoTupleItems: draft-07 keeps tuple-form items as
// `items: [ {...}, {...} ]`. Every entry is a schema and must be walked, while
// the list shape itself must survive (it cannot be represented as a map).
func TestScalarizeUnionsDescendsIntoTupleItems(t *testing.T) {
	schema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"pair": map[string]interface{}{
				"type": "array",
				"items": []interface{}{
					anyOfStringArray("元组第一项"),
					map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"nested": anyOfStringArray("元组第二项 union"),
						},
					},
				},
			},
		},
	}
	got := ScalarizeUnions(schema)
	pair, _ := got["properties"].(map[string]interface{})["pair"].(map[string]interface{})
	items, ok := pair["items"].([]interface{})
	if !ok {
		t.Fatalf("tuple items must stay a list, got %#v", pair["items"])
	}
	if len(items) != 2 {
		t.Fatalf("tuple items must keep both entries, got %#v", items)
	}
	first, _ := items[0].(map[string]interface{})
	if _, exists := first["anyOf"]; exists {
		t.Fatalf("first tuple entry union must be folded, got %#v", first)
	}
	if first["type"] != "string" || first["description"] != "元组第一项" {
		t.Fatalf("expected the scalar branch with description in the first entry, got %#v", first)
	}
	second, _ := items[1].(map[string]interface{})
	nested, _ := second["properties"].(map[string]interface{})["nested"].(map[string]interface{})
	if _, exists := nested["anyOf"]; exists {
		t.Fatalf("union nested in the second tuple entry must be folded, got %#v", nested)
	}
	if nested["type"] != "string" {
		t.Fatalf("expected the scalar branch in the nested union, got %#v", nested)
	}
}

func TestScalarizeUnionsLeavesUnsafeUnionsAlone(t *testing.T) {
	schema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"ref": map[string]interface{}{
				"anyOf": []interface{}{
					map[string]interface{}{"$ref": "#/$defs/thing"},
					map[string]interface{}{"type": "null"},
				},
				"description": "带 $ref 的 union",
			},
		},
	}
	got := ScalarizeUnions(schema)
	ref, _ := got["properties"].(map[string]interface{})["ref"].(map[string]interface{})
	if _, exists := ref["anyOf"]; !exists {
		t.Fatalf("$ref unions must not be rewritten, got %#v", ref)
	}
}

func TestScalarizeUnionsDoesNotMutateInput(t *testing.T) {
	schema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"patterns": anyOfStringArray("原样"),
		},
	}
	_ = ScalarizeUnions(schema)
	patterns, _ := schema["properties"].(map[string]interface{})["patterns"].(map[string]interface{})
	if _, exists := patterns["anyOf"]; !exists {
		t.Fatalf("input schema must stay untouched, got %#v", patterns)
	}
}

// TestScalarizeUnionsHandlesBothUnionKeys: a node carrying anyOf and oneOf must
// not leave the second union behind.
func TestScalarizeUnionsHandlesBothUnionKeys(t *testing.T) {
	schema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"target": map[string]interface{}{
				"anyOf": []interface{}{
					map[string]interface{}{"type": "string"},
					map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
				},
				"oneOf": []interface{}{
					map[string]interface{}{"type": "string"},
					map[string]interface{}{"type": "null"},
				},
				"description": "双 union",
			},
		},
	}
	got := ScalarizeUnions(schema)
	target, _ := got["properties"].(map[string]interface{})["target"].(map[string]interface{})
	if _, exists := target["anyOf"]; exists {
		t.Fatalf("anyOf must be folded, got %#v", target)
	}
	if _, exists := target["oneOf"]; exists {
		t.Fatalf("oneOf must not survive alongside anyOf, got %#v", target)
	}
	if target["type"] != "string" || target["description"] != "双 union" {
		t.Fatalf("expected the scalar branch with description, got %#v", target)
	}
}

// TestScalarizeUnionsDescendsIntoDependenciesAndContentSchema: the draft-07
// containers must be walked too, otherwise a union can still reach providers.
func TestScalarizeUnionsDescendsIntoDependenciesAndContentSchema(t *testing.T) {
	schema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"attachment": map[string]interface{}{
				"type":          "object",
				"contentSchema": anyOfStringArray("附件内容"),
			},
			"mode": map[string]interface{}{"type": "string"},
		},
		"dependencies": map[string]interface{}{
			"mode": anyOfStringArray("依赖分支"),
		},
	}
	got := ScalarizeUnions(schema)
	properties, _ := got["properties"].(map[string]interface{})
	attachment, _ := properties["attachment"].(map[string]interface{})
	contentSchema, _ := attachment["contentSchema"].(map[string]interface{})
	if _, exists := contentSchema["anyOf"]; exists {
		t.Fatalf("contentSchema union must be folded, got %#v", contentSchema)
	}
	dependencies, _ := got["dependencies"].(map[string]interface{})
	mode, _ := dependencies["mode"].(map[string]interface{})
	if _, exists := mode["anyOf"]; exists {
		t.Fatalf("dependencies union must be folded, got %#v", mode)
	}
}

// TestScalarizeUnionsLeavesNodeLevelRefAlone: a node that is itself a $ref must
// not be rewritten even when siblings claim a union.
func TestScalarizeUnionsLeavesNodeLevelRefAlone(t *testing.T) {
	schema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"thing": map[string]interface{}{
				"$ref": "#/$defs/thing",
				"anyOf": []interface{}{
					map[string]interface{}{"type": "string"},
				},
			},
		},
	}
	got := ScalarizeUnions(schema)
	thing, _ := got["properties"].(map[string]interface{})["thing"].(map[string]interface{})
	if _, exists := thing["anyOf"]; !exists {
		t.Fatalf("node-level $ref must be left untouched, got %#v", thing)
	}
}
