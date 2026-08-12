package toolschema

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

// Warning records a compatibility repair applied while canonicalizing a schema.
type Warning struct {
	Code    string `json:"code"`
	Path    string `json:"path,omitempty"`
	Message string `json:"message"`
}

// Clone returns a deep JSON-compatible copy without applying schema repairs.
func Clone(schema map[string]interface{}) (map[string]interface{}, error) {
	return cloneSchemaMap(schema)
}

// Canonicalize returns an isolated, provider-safe object schema. It repairs
// common MCP omissions without mutating the caller's map.
func Canonicalize(schema map[string]interface{}) (map[string]interface{}, []Warning, error) {
	cloned, err := cloneSchemaMap(schema)
	if err != nil {
		return nil, nil, err
	}
	warnings := make([]Warning, 0)
	canonicalizeNode(cloned, "$", &warnings)

	typ, hasType := cloned["type"]
	if !hasType || typ == nil {
		cloned["type"] = "object"
		warnings = append(warnings, Warning{
			Code:    "root_type_defaulted",
			Path:    "$.type",
			Message: "missing root type was defaulted to object",
		})
	} else if typeName, ok := typ.(string); !ok || typeName != "object" {
		return nil, warnings, fmt.Errorf("tool schema root type must be object, got %v", typ)
	}

	properties, hasProperties := cloned["properties"]
	if !hasProperties || properties == nil {
		cloned["properties"] = map[string]interface{}{}
	} else if _, ok := properties.(map[string]interface{}); !ok {
		return nil, warnings, fmt.Errorf("tool schema root properties must be an object")
	}
	if _, ok := cloned["additionalProperties"]; !ok {
		cloned["additionalProperties"] = false
	}
	return cloned, warnings, nil
}

// CanonicalBytes returns the stable JSON representation used for hashing and
// contract snapshots.
func CanonicalBytes(schema map[string]interface{}) ([]byte, []Warning, error) {
	canonical, warnings, err := Canonicalize(schema)
	if err != nil {
		return nil, warnings, err
	}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return nil, warnings, fmt.Errorf("marshal canonical tool schema: %w", err)
	}
	return raw, warnings, nil
}

// Hash returns a SHA-256 hash of the canonical schema.
func Hash(schema map[string]interface{}) (string, error) {
	raw, _, err := CanonicalBytes(schema)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// RawHash fingerprints the supplied JSON-compatible schema without applying
// compatibility repairs. It is used to identify quarantined definitions.
func RawHash(schema map[string]interface{}) (string, error) {
	raw, err := json.Marshal(schema)
	if err != nil {
		return "", fmt.Errorf("marshal raw tool schema: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func cloneSchemaMap(schema map[string]interface{}) (map[string]interface{}, error) {
	if schema == nil {
		return map[string]interface{}{}, nil
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		return nil, fmt.Errorf("marshal tool schema: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var cloned map[string]interface{}
	if err := decoder.Decode(&cloned); err != nil {
		return nil, fmt.Errorf("decode tool schema: %w", err)
	}
	if cloned == nil {
		cloned = map[string]interface{}{}
	}
	return cloned, nil
}

func canonicalizeNode(node interface{}, path string, warnings *[]Warning) {
	schema, ok := node.(map[string]interface{})
	if !ok {
		return
	}

	if required, exists := schema["required"]; exists {
		schema["required"] = canonicalizeStringSet(required, path+".required", warnings)
		if schema["required"] == nil {
			delete(schema, "required")
		}
	}
	if dependentRequired, exists := schema["dependentRequired"]; exists {
		canonicalizeDependentRequired(schema, "dependentRequired", dependentRequired, path+".dependentRequired", warnings)
	}

	for _, key := range schemaValueKeywords {
		if value, exists := schema[key]; exists {
			canonicalizeSchemaValue(value, path+"."+key, warnings)
		}
	}
	for _, key := range schemaMapKeywords {
		if value, exists := schema[key]; exists {
			canonicalizeSchemaMap(value, path+"."+key, warnings)
		}
	}
	if dependencies, exists := schema["dependencies"]; exists {
		canonicalizeDependencies(dependencies, path+".dependencies", warnings)
	}
}

var schemaValueKeywords = []string{
	"additionalItems",
	"additionalProperties",
	"allOf",
	"anyOf",
	"contains",
	"contentSchema",
	"else",
	"if",
	"items",
	"not",
	"oneOf",
	"prefixItems",
	"propertyNames",
	"then",
	"unevaluatedItems",
	"unevaluatedProperties",
}

var schemaMapKeywords = []string{
	"$defs",
	"definitions",
	"dependentSchemas",
	"patternProperties",
	"properties",
}

func canonicalizeSchemaValue(value interface{}, path string, warnings *[]Warning) {
	switch typed := value.(type) {
	case map[string]interface{}:
		canonicalizeNode(typed, path, warnings)
	case []interface{}:
		for index, child := range typed {
			canonicalizeNode(child, fmt.Sprintf("%s[%d]", path, index), warnings)
		}
	}
}

func canonicalizeSchemaMap(value interface{}, path string, warnings *[]Warning) {
	children, ok := value.(map[string]interface{})
	if !ok {
		return
	}
	for name, child := range children {
		canonicalizeNode(child, path+"."+name, warnings)
	}
}

func canonicalizeDependencies(value interface{}, path string, warnings *[]Warning) {
	dependencies, ok := value.(map[string]interface{})
	if !ok {
		return
	}
	for name, dependency := range dependencies {
		if _, isSchema := dependency.(map[string]interface{}); isSchema {
			canonicalizeNode(dependency, path+"."+name, warnings)
		}
	}
}

func canonicalizeStringSet(value interface{}, path string, warnings *[]Warning) interface{} {
	values, ok := value.([]interface{})
	if !ok {
		*warnings = append(*warnings, Warning{
			Code:    "invalid_required_removed",
			Path:    path,
			Message: "required must be an array of property names and was removed",
		})
		return nil
	}

	seen := make(map[string]struct{}, len(values))
	normalized := make([]string, 0, len(values))
	for _, item := range values {
		name, ok := item.(string)
		if !ok {
			// Keep the invalid value so schema compilation rejects it instead of
			// silently widening the accepted argument set.
			return value
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		normalized = append(normalized, name)
	}
	sort.Strings(normalized)
	return normalized
}

func canonicalizeDependentRequired(parent map[string]interface{}, key string, value interface{}, path string, warnings *[]Warning) {
	dependencies, ok := value.(map[string]interface{})
	if !ok {
		delete(parent, key)
		*warnings = append(*warnings, Warning{
			Code:    "invalid_dependent_required_removed",
			Path:    path,
			Message: "dependentRequired must be an object and was removed",
		})
		return
	}
	for property, required := range dependencies {
		normalized := canonicalizeStringSet(required, path+"."+property, warnings)
		if normalized == nil {
			delete(dependencies, property)
			continue
		}
		dependencies[property] = normalized
	}
}
