package toolschema

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
		if branch == nil {
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
