package types

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// DeterministicToolCallID returns a stable tool call id for the given tool payload.
func DeterministicToolCallID(prefix string, index int, name string, args map[string]interface{}) string {
	argsJSON, err := json.Marshal(args)
	if err != nil || len(argsJSON) == 0 || string(argsJSON) == "null" {
		argsJSON = []byte("{}")
	}
	return DeterministicToolCallIDFromJSON(prefix, index, name, argsJSON)
}

// DeterministicToolCallIDFromJSON returns a stable tool call id for the given tool payload.
func DeterministicToolCallIDFromJSON(prefix string, index int, name string, argsJSON json.RawMessage) string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		prefix = "toolcall_"
	}
	canonicalArgs := canonicalToolCallArgsJSON(argsJSON)
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d:%s:%s", index, strings.TrimSpace(name), canonicalArgs)))
	return prefix + hex.EncodeToString(sum[:8])
}

func canonicalToolCallArgsJSON(argsJSON json.RawMessage) string {
	trimmed := strings.TrimSpace(string(argsJSON))
	if trimmed == "" || strings.EqualFold(trimmed, "null") {
		return "{}"
	}
	var decoded interface{}
	if err := json.Unmarshal(argsJSON, &decoded); err != nil {
		return trimmed
	}
	canonical, err := json.Marshal(decoded)
	if err != nil || len(canonical) == 0 || string(canonical) == "null" {
		return "{}"
	}
	return string(canonical)
}
