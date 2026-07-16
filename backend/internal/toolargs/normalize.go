package toolargs

import (
	"encoding/json"
	"fmt"
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

// DecodeJSON decodes provider-emitted tool arguments and conservatively
// completes missing object or array delimiters. It never repairs content that
// ends inside a JSON string, so a truncated command or file mutation cannot be
// executed as if it were complete.
func DecodeJSON(raw string) map[string]interface{} {
	text := strings.TrimSpace(raw)
	if text == "" {
		return map[string]interface{}{}
	}

	args, err := decodeJSONObject(text)
	if err == nil {
		return Normalize(args)
	}
	if repaired, ok := completeStructuralJSON(text); ok {
		if args, repairErr := decodeJSONObject(repaired); repairErr == nil {
			return Normalize(args)
		}
	}
	return map[string]interface{}{
		"_raw":         text,
		"_parse_error": err.Error(),
	}
}

func decodeJSONObject(text string) (map[string]interface{}, error) {
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(text), &args); err != nil {
		return nil, err
	}
	if args == nil && !strings.EqualFold(strings.TrimSpace(text), "null") {
		return nil, fmt.Errorf("tool arguments must be a JSON object")
	}
	return args, nil
}

func completeStructuralJSON(text string) (string, bool) {
	stack := make([]byte, 0, 4)
	inString := false
	escaped := false
	for index := 0; index < len(text); index++ {
		char := text[index]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if char == '\\' {
				escaped = true
				continue
			}
			if char == '"' {
				inString = false
			}
			continue
		}

		switch char {
		case '"':
			inString = true
		case '{', '[':
			stack = append(stack, char)
		case '}', ']':
			if len(stack) == 0 || !matchingJSONDelimiters(stack[len(stack)-1], char) {
				return text, false
			}
			stack = stack[:len(stack)-1]
		}
	}
	if inString || len(stack) == 0 {
		return text, false
	}

	var suffix strings.Builder
	suffix.Grow(len(stack))
	for index := len(stack) - 1; index >= 0; index-- {
		if stack[index] == '{' {
			suffix.WriteByte('}')
		} else {
			suffix.WriteByte(']')
		}
	}
	return text + suffix.String(), true
}

func matchingJSONDelimiters(open, close byte) bool {
	return open == '{' && close == '}' || open == '[' && close == ']'
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
