package toolargs

import (
	"encoding/json"
	"strings"
)

const maxNormalizeDepth = 5

// Normalize unwraps provider fallback argument shapes such as
// {"_raw":"{\"file_path\":\"...\"}"} into the object the tool schema
// expects. Invalid raw JSON is preserved so callers can still surface parse
// diagnostics such as _parse_error.
func Normalize(args map[string]interface{}) map[string]interface{} {
	return normalizeMap(args, 0)
}

func normalizeMap(args map[string]interface{}, depth int) map[string]interface{} {
	if args == nil {
		return map[string]interface{}{}
	}
	if depth >= maxNormalizeDepth {
		return cloneMap(args)
	}
	raw, hasRaw := args["_raw"]
	if hasRaw && !hasNonMetaKeys(args) {
		if decoded, ok := decodeRawMap(raw, depth+1); ok {
			return normalizeMap(decoded, depth+1)
		}
	}
	return cloneMap(args)
}

func hasNonMetaKeys(args map[string]interface{}) bool {
	for key := range args {
		switch key {
		case "_raw", "_parse_error":
			continue
		default:
			return true
		}
	}
	return false
}

func decodeRawMap(raw interface{}, depth int) (map[string]interface{}, bool) {
	if depth >= maxNormalizeDepth {
		return nil, false
	}
	switch typed := raw.(type) {
	case map[string]interface{}:
		return typed, true
	case string:
		text := strings.TrimSpace(typed)
		if text == "" {
			return nil, false
		}
		var decoded interface{}
		if err := json.Unmarshal([]byte(text), &decoded); err != nil {
			return nil, false
		}
		switch value := decoded.(type) {
		case map[string]interface{}:
			return value, true
		case string:
			return decodeRawMap(value, depth+1)
		default:
			return nil, false
		}
	default:
		return nil, false
	}
}

func cloneMap(args map[string]interface{}) map[string]interface{} {
	cloned := make(map[string]interface{}, len(args))
	for key, value := range args {
		cloned[key] = value
	}
	return cloned
}
