package toolbroker

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
)

// spawn_agent argument kinds.
//
// spawn_agent is the fail-closed delegation tool: an unknown key is rejected
// outright (spawnAgentToolArgKeys) instead of being dropped. The decode path
// still read every known key with a direct type assertion, which folded a
// mismatched JSON kind into the zero value *silently* - the same class of bug
// that spawn_team had:
//
//   - `read_only: "true"` / `fork_context: "false"` were ignored, so a child
//     that the caller asked to sandbox ran with the parent's write surface.
//   - `permission_mode: 1`, `isolation: 5`, `difficulty: 42` and
//     `provider: 123` were dropped, so routing/execution options silently fell
//     back to defaults.
//   - `fork_turns: 2` was dropped, which also silently re-enabled
//     fork_context even though fork_turns is documented to override it.
//   - the four supervision timeout keys were asserted as int64 only. A tool
//     call decoded from JSON never carries int64 (float64, or json.Number on
//     the toolexec preflight path), so `timeout_sec`, `progress_timeout_sec`,
//     `approval_timeout_sec` and `cancel_grace_sec` were *always* ignored for
//     real callers - and the "must be non-negative" check below them was
//     unreachable for those calls.
//
// Validating the kind keeps the existing leniency where it is intentional
// (numbers are accepted in any JSON spelling, then coerced with
// toolArgInt64) and turns the remaining mismatches into actionable errors.
const (
	// Shared JSON-kind labels for the delegation/message tool argument checks.
	toolArgFieldString       = "JSON string"
	toolArgFieldBool         = "JSON boolean"
	toolArgFieldNumber       = "JSON number"
	toolArgFieldObject       = "JSON object"
	toolArgFieldStringOrList = "JSON string or array of strings"
)

var spawnAgentFieldKinds = []struct {
	key  string
	want string
}{
	{"id", toolArgFieldString},
	{"session_id", toolArgFieldString},
	{"agent_type", toolArgFieldString},
	{"difficulty", toolArgFieldString},
	{"difficulty_rationale", toolArgFieldString},
	{"task_type", toolArgFieldString},
	{"task_subject", toolArgFieldString},
	{"provider", toolArgFieldString},
	{"model", toolArgFieldString},
	{"reasoning_effort", toolArgFieldString},
	{"thinking_effort", toolArgFieldString},
	{"permission_mode", toolArgFieldString},
	{"completion_requirement", toolArgFieldString},
	{"completionRequirement", toolArgFieldString},
	{"isolation", toolArgFieldString},
	// fork_turns is declared as a string ("none", "all" or a positive
	// integer); a bare number must not silently cancel the override.
	{"fork_turns", toolArgFieldString},
	{"read_only", toolArgFieldBool},
	{"fork_context", toolArgFieldBool},
	{"timeout_sec", toolArgFieldNumber},
	{"progress_timeout_sec", toolArgFieldNumber},
	{"approval_timeout_sec", toolArgFieldNumber},
	{"cancel_grace_sec", toolArgFieldNumber},
}

// validateSpawnAgentArgTypes checks the known spawn_agent arguments for
// JSON-kind mismatches. Message aliases (message/goal/task/prompt) are handled
// by normalizeSpawnAgentToolArgs, which reports an empty prompt on its own.
func validateSpawnAgentArgTypes(args map[string]interface{}) error {
	for _, field := range spawnAgentFieldKinds {
		value, exists := args[field.key]
		if !exists || value == nil {
			continue
		}
		if spawnAgentValueMatchesKind(value, field.want) {
			continue
		}
		return fmt.Errorf("spawn_agent argument %q must be a %s, got %T (%v); omit it to use the runtime default",
			field.key, field.want, value, value)
	}
	return nil
}

func spawnAgentValueMatchesKind(value interface{}, want string) bool {
	switch want {
	case toolArgFieldString:
		_, ok := value.(string)
		return ok
	case toolArgFieldBool:
		_, ok := value.(bool)
		return ok
	case toolArgFieldNumber:
		return isToolJSONNumber(value)
	default:
		return true
	}
}

// isToolJSONNumber reports whether value is any JSON numeric spelling the
// delegation tools accept: Go-native ints for internal callers, float64 for
// decoded JSON, and json.Number on the toolexec preflight path.
func isToolJSONNumber(value interface{}) bool {
	switch value.(type) {
	case int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64, json.Number:
		return true
	default:
		return false
	}
}

// toolArgInt64 reads a numeric delegation argument in every JSON spelling and
// reports whether the key was present. owner names the tool in error messages.
//
// Every numeric read in the delegation tools must go through this helper: a
// direct `args[key].(float64)` (or `.(int64)`) assertion silently drops the
// value for the spellings it does not cover.
func toolArgInt64(owner string, args map[string]interface{}, key string) (int64, bool, error) {
	value, exists := args[key]
	if !exists || value == nil {
		return 0, false, nil
	}
	return toolValueInt64(owner, key, value)
}

// toolValueInt64 applies the same JSON-number rules to one already-read value,
// for the call sites that hold a value instead of an args map (nested objects
// such as background_task startup_acceptance, and the supervision tools).
func toolValueInt64(owner string, key string, value interface{}) (int64, bool, error) {
	switch typed := value.(type) {
	case int:
		return int64(typed), true, nil
	case int8:
		return int64(typed), true, nil
	case int16:
		return int64(typed), true, nil
	case int32:
		return int64(typed), true, nil
	case int64:
		return typed, true, nil
	case uint:
		return int64(typed), true, nil
	case uint8:
		return int64(typed), true, nil
	case uint16:
		return int64(typed), true, nil
	case uint32:
		return int64(typed), true, nil
	case uint64:
		if typed > math.MaxInt64 {
			return 0, false, fmt.Errorf("%s argument %q is out of range: %d", owner, key, typed)
		}
		return int64(typed), true, nil
	case float32:
		return toolArgWholeNumber(owner, key, float64(typed))
	case float64:
		return toolArgWholeNumber(owner, key, typed)
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			if float, floatErr := typed.Float64(); floatErr != nil {
				return 0, false, fmt.Errorf("%s argument %q must be a %s, got json.Number (%v)", owner, key, toolArgFieldNumber, typed)
			} else {
				return toolArgWholeNumber(owner, key, float)
			}
		}
		return parsed, true, nil
	default:
		return 0, false, fmt.Errorf("%s argument %q must be a %s, got %T (%v)", owner, key, toolArgFieldNumber, value, value)
	}
}

func toolArgWholeNumber(owner, key string, value float64) (int64, bool, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, false, fmt.Errorf("%s argument %q must be a finite number, got %v", owner, key, value)
	}
	if value != math.Trunc(value) {
		return 0, false, fmt.Errorf("%s argument %q must be a whole number, got %s", owner, key, strconv.FormatFloat(value, 'f', -1, 64))
	}
	// float64(math.MaxInt64) is exactly 2^63, so the upper bound must be
	// inclusive: `value > math.MaxInt64` lets 2^63 through and int64(value)
	// then silently wraps to a negative number.
	if value >= math.MaxInt64 || value < math.MinInt64 {
		return 0, false, fmt.Errorf("%s argument %q is out of range: %v", owner, key, value)
	}
	return int64(value), true, nil
}

// brokerToolArgInt narrows toolArgInt64 to int for the broker options that are
// declared as plain ints (limits, timeouts, offsets, priorities).
//
// Every numeric read outside the delegation tools must go through this helper
// as well: `args["limit"].(float64)` (or `.(int)`) silently dropped the value
// for every other JSON spelling, and a dropped limit/timeout/offset changes the
// call's meaning instead of failing it.
func brokerToolArgInt(owner string, args map[string]interface{}, key string) (int, bool, error) {
	value, ok, err := toolArgInt64(owner, args, key)
	if err != nil || !ok {
		return 0, ok, err
	}
	if int64(int(value)) != value {
		return 0, false, fmt.Errorf("%s argument %q is out of range: %d", owner, key, value)
	}
	return int(value), true, nil
}
