package toolcore

import (
	"encoding/json"
	"fmt"
)

// FilterSchemaParams removes unsupported optional parameters from a tool's
// input schema. It is the single place that makes "parameter exists" follow
// runtime capability; handlers never accept a parameter the surface cannot
// honor.
func FilterSchemaParams(schema json.RawMessage, unsupported ...OptionalParam) (json.RawMessage, error) {
	if len(schema) == 0 || len(unsupported) == 0 {
		return schema, nil
	}
	var document map[string]any
	if err := json.Unmarshal(schema, &document); err != nil {
		return nil, fmt.Errorf("decode tool schema: %w", err)
	}
	properties, _ := document["properties"].(map[string]any)
	if properties == nil {
		return schema, nil
	}
	removed := make(map[string]bool, len(unsupported))
	for _, param := range unsupported {
		name := string(param)
		delete(properties, name)
		removed[name] = true
	}
	document["properties"] = properties
	if required, ok := document["required"].([]any); ok {
		kept := make([]any, 0, len(required))
		for _, item := range required {
			name, _ := item.(string)
			if !removed[name] {
				kept = append(kept, item)
			}
		}
		document["required"] = kept
	}
	filtered, err := json.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("encode tool schema: %w", err)
	}
	return filtered, nil
}
