package toolexec

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// ArgsDigest returns a stable fingerprint for tool_name + normalized arguments.
// The digest is schema-agnostic and never hard-codes tool or command names.
// Tool names are lower-cased so provider casing variants share one fingerprint.
func ArgsDigest(toolName string, args map[string]interface{}) string {
	name := strings.ToLower(strings.TrimSpace(toolName))
	normalized := normalizeForDigest(args)
	payload := map[string]interface{}{
		"tool": name,
		"args": normalized,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		// Fallback keeps the key usable even if args contain non-JSON values.
		sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%v", name, args)))
		return hex.EncodeToString(sum[:])
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func normalizeForDigest(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		if typed == nil {
			return map[string]interface{}{}
		}
		keys := make([]string, 0, len(typed))
		for key := range typed {
			// Provider parse diagnostics are not part of the semantic call fingerprint.
			if strings.HasPrefix(key, "_") {
				continue
			}
			keys = append(keys, key)
		}
		sort.Strings(keys)
		out := make(map[string]interface{}, len(keys))
		for _, key := range keys {
			normalized := normalizeForDigest(typed[key])
			// Drop pure noise so optional null / "" args do not split one semantic
			// call into multiple digests (models flip omit vs empty freely).
			if isDigestNoiseValue(normalized) {
				continue
			}
			out[key] = normalized
		}
		return out
	case map[string]string:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			if strings.HasPrefix(key, "_") {
				continue
			}
			keys = append(keys, key)
		}
		sort.Strings(keys)
		out := make(map[string]interface{}, len(keys))
		for _, key := range keys {
			if isDigestNoiseValue(typed[key]) {
				continue
			}
			out[key] = typed[key]
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(typed))
		for i, item := range typed {
			out[i] = normalizeForDigest(item)
		}
		return out
	case []string:
		out := make([]interface{}, len(typed))
		for i, item := range typed {
			out[i] = item
		}
		return out
	case json.Number:
		return typed.String()
	case string:
		return typed
	case bool, float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return typed
	case nil:
		return nil
	default:
		return fmt.Sprint(typed)
	}
}

// isDigestNoiseValue reports values that should not contribute to ArgsDigest.
// Underscore-prefixed keys are filtered at the map level; this covers value noise.
func isDigestNoiseValue(value interface{}) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case string:
		return typed == ""
	case map[string]interface{}:
		return len(typed) == 0
	default:
		return false
	}
}
