package toolschema

import "math"

// Provider-facing union squashing (analysis §3.12).
//
// The runtime advertises a handful of `string|array` (and `string|integer`)
// properties for ergonomics: grep's patterns/paths/include/exclude and the
// broker's plan_path. Some providers (the Gemini family in particular) reject
// or silently mis-handle `anyOf`/`oneOf` properties that carry sibling keys
// such as `description`, and the failure surfaces as an opaque whole-request
// 400 — only in production, only for some models.
//
// Internal descriptors keep the union (the executor and the argument-repair
// layer accept both shapes, and preflight reads every branch). Right before a
// provider request is built, non-Codex protocols pass through ScalarizeUnions
// so the wire schema advertises a single scalar branch. Codex keeps its own
// sanitizer and is deliberately excluded.

// ScalarizeUnions returns a deep copy of schema in which unions with sibling
// keys are collapsed to one scalar branch. Nodes the walker cannot safely
// rewrite (non-map branches, $ref, unions with no map branch) are left as-is.
func ScalarizeUnions(schema map[string]interface{}) map[string]interface{} {
	if len(schema) == 0 {
		return schema
	}
	cloned := schema
	if copy, err := Clone(schema); err == nil && copy != nil {
		cloned = copy
	} else {
		// Clone only fails for non-JSON-compatible values. Mutating the
		// caller's map in that case would silently corrupt a shared internal
		// descriptor, so refuse the rewrite instead of aliasing it.
		return schema
	}
	scalarizeUnionsNode(cloned)
	return cloned
}

var unionChildKeys = []string{
	"items",
	"additionalItems",
	"additionalProperties",
	"contains",
	"contentSchema",
	"not",
	"if",
	"then",
	"else",
	"propertyNames",
	"unevaluatedItems",
	"unevaluatedProperties",
}

var unionMapKeys = []string{
	"properties",
	"$defs",
	"definitions",
	"dependencies",
	"patternProperties",
	"dependentSchemas",
}

var unionListKeys = []string{
	"allOf",
	"anyOf",
	"oneOf",
	"prefixItems",
}

func scalarizeUnionsNode(node map[string]interface{}) {
	// 节点自身是 $ref 时任何改写都可能改变语义（draft-07 里 $ref 与兄弟键
	// 的关系按实现而不同），保守跳过；分支内部的 $ref 由
	// preferredUnionBranch 单独把关。
	if _, hasRef := node["$ref"]; hasRef {
		return
	}
	for _, key := range unionMapKeys {
		children, ok := node[key].(map[string]interface{})
		if !ok {
			continue
		}
		for _, child := range children {
			if childNode, ok := child.(map[string]interface{}); ok {
				scalarizeUnionsNode(childNode)
			}
		}
	}
	for _, key := range unionChildKeys {
		switch child := node[key].(type) {
		case map[string]interface{}:
			scalarizeUnionsNode(child)
		case []interface{}:
			// draft-07 tuple 形态（items: [ {...}, {...} ]）每个元素都是 schema，
			// 必须逐元素下钻，否则元组里的 anyOf/oneOf 会原样发往 provider。
			for _, entry := range child {
				if childNode, ok := entry.(map[string]interface{}); ok {
					scalarizeUnionsNode(childNode)
				}
			}
		}
	}
	for _, key := range unionListKeys {
		children, ok := node[key].([]interface{})
		if !ok {
			continue
		}
		for _, child := range children {
			if childNode, ok := child.(map[string]interface{}); ok {
				scalarizeUnionsNode(childNode)
			}
		}
	}
	scalarizeNodeUnion(node)
}

func scalarizeNodeUnion(node map[string]interface{}) {
	// 两个 union 键都要处理：同节点同时带 anyOf/oneOf 时只改一个，另一个
	// 仍会把 union 带上 provider 请求。
	for _, unionKey := range []string{"anyOf", "oneOf"} {
		raw, exists := node[unionKey]
		if !exists {
			continue
		}
		branches, _ := raw.([]interface{})
		branch := preferredUnionBranch(branches)
		if branch == nil || !unionBranchCompatibleWithSiblings(node, branch) {
			continue
		}
		delete(node, unionKey)
		for key, value := range branch {
			if _, exists := node[key]; exists {
				continue
			}
			node[key] = value
		}
	}
}

// preferredUnionBranch prefers the string branch: models overwhelmingly send
// the scalar spelling, and the runtime coerces scalars into arrays during
// argument normalization. Non-map branches or $ref make the rewrite unsafe.
func preferredUnionBranch(branches []interface{}) map[string]interface{} {
	if len(branches) == 0 {
		return nil
	}
	var first, stringBranch map[string]interface{}
	for _, raw := range branches {
		branch, ok := raw.(map[string]interface{})
		if !ok {
			return nil
		}
		if _, hasRef := branch["$ref"]; hasRef {
			return nil
		}
		if first == nil {
			first = branch
		}
		if typ, _ := branch["type"].(string); typ == "string" && stringBranch == nil {
			stringBranch = branch
		}
	}
	if stringBranch != nil {
		return stringBranch
	}
	return first
}

// unionBranchCompatibleWithSiblings reports whether folding branch into node
// (where existing sibling keys win) can still accept every value the original
// constraints allowed.
//
// Folding used to copy the branch's `type` next to an existing sibling `enum`/
// `const` without looking at either: `enum:[1,2]` + `anyOf:[integer,string]`
// became `enum:[1,2], type:string`, a property with no legal value, while the
// original accepted 1 and 2 (2026-09-27 review). An incompatible branch is left
// in place: providers lose the scalar shorthand for that corner, but the
// advertised tool can still be called.
func unionBranchCompatibleWithSiblings(node, branch map[string]interface{}) bool {
	if node == nil || branch == nil {
		return false
	}
	branchType, _ := branch["type"].(string)
	nodeType, _ := node["type"].(string)
	if branchType != "" && nodeType != "" && branchType != nodeType {
		return false
	}
	// A sibling enum/const restricts the values the folded type must accept.
	for _, key := range []string{"enum", "const"} {
		raw, ok := node[key]
		if !ok || branchType == "" {
			continue
		}
		values := []interface{}{raw}
		if key == "enum" {
			list, ok := raw.([]interface{})
			if !ok {
				continue
			}
			values = list
		}
		for _, value := range values {
			if !jsonValueFitsSchemaType(value, branchType) {
				return false
			}
		}
	}
	// The branch's own enum/const is copied only when the sibling does not
	// already define the key; it must still fit the surviving sibling type.
	if nodeType != "" {
		for _, key := range []string{"enum", "const"} {
			raw, ok := branch[key]
			if !ok {
				continue
			}
			values := []interface{}{raw}
			if key == "enum" {
				list, ok := raw.([]interface{})
				if !ok {
					continue
				}
				values = list
			}
			for _, value := range values {
				if !jsonValueFitsSchemaType(value, nodeType) {
					return false
				}
			}
		}
	}
	return true
}

// jsonValueFitsSchemaType reports whether a decoded JSON value satisfies a
// single JSON Schema type keyword. Unknown keywords are treated as compatible so
// provider-specific extensions never block the rewrite.
func jsonValueFitsSchemaType(value interface{}, typ string) bool {
	switch typ {
	case "string":
		_, ok := value.(string)
		return ok
	case "integer":
		switch number := value.(type) {
		case float64:
			return !math.IsNaN(number) && !math.IsInf(number, 0) && number == math.Trunc(number)
		case int, int64:
			return true
		}
		return false
	case "number":
		switch value.(type) {
		case float64, int, int64:
			return true
		}
		return false
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "array":
		_, ok := value.([]interface{})
		return ok
	case "object":
		_, ok := value.(map[string]interface{})
		return ok
	case "null":
		return value == nil
	}
	return true
}
