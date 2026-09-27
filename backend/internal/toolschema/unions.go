package toolschema

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"
)

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
		branch := compatibleUnionBranch(node, branches)
		if branch == nil {
			continue
		}
		delete(node, unionKey)
		for key, value := range branch {
			if _, exists := node[key]; exists {
				continue
			}
			// A sibling const/enum already pins the value set; copying the
			// branch's sibling constraint would only repeat it (or, when the
			// two disagree, fabricate an impossible intersection — the
			// compatibility check below guarantees they agree when copied).
			if key == "enum" || key == "const" {
				if _, hasConst := node["const"]; hasConst {
					continue
				}
				if _, hasEnum := node["enum"]; hasEnum {
					continue
				}
			}
			node[key] = value
		}
	}
}

// compatibleUnionBranch returns the branch to fold into node: the string branch
// when it is compatible, otherwise the first compatible branch. Every branch is
// checked against the surviving sibling constraints (type, value sets and
// bounds); nil means no branch can be folded safely.
func compatibleUnionBranch(node map[string]interface{}, branches []interface{}) map[string]interface{} {
	var first, stringBranch map[string]interface{}
	for _, raw := range branches {
		branch, ok := raw.(map[string]interface{})
		if !ok {
			return nil
		}
		if _, hasRef := branch["$ref"]; hasRef {
			return nil
		}
		if !unionBranchCompatibleWithSiblings(node, branch) {
			continue
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

// preferredUnionBranch prefers the string branch: models overwhelmingly send
// the scalar spelling, and the runtime coerces scalars into arrays during
// argument normalization. Non-map branches or $ref make the rewrite unsafe.
//
// Deprecated: use compatibleUnionBranch, which also verifies that the chosen
// branch does not contradict the surviving sibling constraints.
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
	// Value sets (const/enum) on both sides must share at least one value;
	// otherwise the folded node can never validate (const:"second" + branch
	// enum:["first"]).
	if !unionValueSetsIntersect(node, branch) {
		return false
	}
	// The surviving bounds (sibling wins per key) must still admit a value:
	// maxLength:1 + branch minLength:2, or maximum:1 + branch minimum:2.
	return !mergedBoundsConflict(node, branch)
}

// unionValueSetsIntersect reports whether the const/enum value sets of both
// schemas share at least one value. A missing set on either side imposes no
// restriction.
func unionValueSetsIntersect(node, branch map[string]interface{}) bool {
	nodeValues, nodePinned := unionPinnedValues(node)
	branchValues, branchPinned := unionPinnedValues(branch)
	if !nodePinned || !branchPinned {
		return true
	}
	for _, left := range nodeValues {
		for _, right := range branchValues {
			if jsonValuesEqual(left, right) {
				return true
			}
		}
	}
	return false
}

func unionPinnedValues(schema map[string]interface{}) ([]interface{}, bool) {
	if raw, ok := schema["const"]; ok {
		return []interface{}{raw}, true
	}
	if raw, ok := schema["enum"]; ok {
		if list, ok := raw.([]interface{}); ok {
			return list, true
		}
	}
	return nil, false
}

func jsonValuesEqual(left, right interface{}) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	return bytes.Equal(leftJSON, rightJSON)
}

// numberBound is one effective numeric bound after the merge (sibling keys win
// over branch keys, matching the copy loop).
type numberBound struct {
	value     float64
	present   bool
	exclusive bool
}

func mergedLowerBound(node, branch map[string]interface{}) numberBound {
	if bound := lowerBoundOf(node); bound.present {
		return bound
	}
	return lowerBoundOf(branch)
}

func mergedUpperBound(node, branch map[string]interface{}) numberBound {
	if bound := upperBoundOf(node); bound.present {
		return bound
	}
	return upperBoundOf(branch)
}

func lowerBoundOf(schema map[string]interface{}) numberBound {
	if value, ok := schemaNumber(schema, "exclusiveMinimum"); ok {
		return numberBound{value: value, present: true, exclusive: true}
	}
	if value, ok := schemaNumber(schema, "minimum"); ok {
		return numberBound{value: value, present: true}
	}
	return numberBound{}
}

func upperBoundOf(schema map[string]interface{}) numberBound {
	if value, ok := schemaNumber(schema, "exclusiveMaximum"); ok {
		return numberBound{value: value, present: true, exclusive: true}
	}
	if value, ok := schemaNumber(schema, "maximum"); ok {
		return numberBound{value: value, present: true}
	}
	return numberBound{}
}

func mergedIntBound(node, branch map[string]interface{}, key string) (int, bool) {
	if value, ok := schemaInt(node, key); ok {
		return value, true
	}
	return schemaInt(branch, key)
}

// mergedBoundsConflict reports whether the bounds that survive the fold admit no
// value at all.
func mergedBoundsConflict(node, branch map[string]interface{}) bool {
	minLength, hasMinLength := mergedIntBound(node, branch, "minLength")
	maxLength, hasMaxLength := mergedIntBound(node, branch, "maxLength")
	if hasMinLength && hasMaxLength && minLength > maxLength {
		return true
	}
	minItems, hasMinItems := mergedIntBound(node, branch, "minItems")
	maxItems, hasMaxItems := mergedIntBound(node, branch, "maxItems")
	if hasMinItems && hasMaxItems && minItems > maxItems {
		return true
	}
	lower := mergedLowerBound(node, branch)
	upper := mergedUpperBound(node, branch)
	if lower.present && upper.present {
		if lower.value > upper.value {
			return true
		}
		if lower.value == upper.value && (lower.exclusive || upper.exclusive) {
			return true
		}
	}
	return false
}

func schemaInt(schema map[string]interface{}, key string) (int, bool) {
	switch value := schema[key].(type) {
	case int:
		return value, true
	case int64:
		return int(value), true
	case json.Number:
		// ScalarizeUnions works on Clone's output, which decodes with
		// UseNumber: every schema number is a json.Number, not a float64.
		if parsed, err := strconv.ParseInt(value.String(), 10, 64); err == nil {
			return int(parsed), true
		}
		if parsed, err := strconv.ParseFloat(value.String(), 64); err == nil && parsed == math.Trunc(parsed) && !math.IsInf(parsed, 0) {
			return int(parsed), true
		}
	case float64:
		if !math.IsNaN(value) && !math.IsInf(value, 0) && value == math.Trunc(value) {
			return int(value), true
		}
	}
	return 0, false
}

func schemaNumber(schema map[string]interface{}, key string) (float64, bool) {
	switch value := schema[key].(type) {
	case int:
		return float64(value), true
	case int64:
		return float64(value), true
	case json.Number:
		if parsed, err := strconv.ParseFloat(value.String(), 64); err == nil {
			return parsed, true
		}
	case float64:
		return value, true
	}
	return 0, false
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
		case json.Number:
			parsed, err := strconv.ParseFloat(number.String(), 64)
			return err == nil && !math.IsNaN(parsed) && !math.IsInf(parsed, 0) && parsed == math.Trunc(parsed)
		}
		return false
	case "number":
		switch value.(type) {
		case float64, int, int64:
			return true
		case json.Number:
			_, err := strconv.ParseFloat(value.(json.Number).String(), 64)
			return err == nil
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
