package toolbroker

import (
	"encoding/json"
	"fmt"
)

// spawn_team argument kinds, mirroring the fail-closed contract that
// spawn_subagents applies in internal/agent/loop.go (decodeSubagentTasks).
//
// The spawn_team decode path reads these keys with direct type assertions (and
// coerceStringSlice for list fields), which fold a mismatched JSON kind into the
// zero value. That is not harmless for a delegating tool:
//
//   - `auto_start: "false"` left request.AutoStart nil, so the team started its
//     teammates anyway, and `allow_existing: "false"` fell back to the default
//     "reuse the team".
//   - `max_teammates: "3"` / `max_writers` were ignored.
//   - a `write_paths` value that is not an array of strings was dropped before
//     lease path claims were built; team/orchestrator.go and team/sqlite_store.go
//     derive conflict fencing from task.WritePaths, so the task lost its declared
//     write scope instead of failing the call.
//
// The same function already fails closed for tasks[].difficulty and for a
// non-object teammates[]/tasks[] entry, so this only extends an existing
// contract to the remaining keys.
const (
	spawnTeamFieldString       = "JSON string"
	spawnTeamFieldNumber       = "JSON number"
	spawnTeamFieldBool         = "JSON boolean"
	spawnTeamFieldStringOrList = "JSON string or array of strings"
)

var spawnTeamTopLevelFieldKinds = []struct {
	key  string
	want string
}{
	{"team_id", spawnTeamFieldString},
	{"workspace_id", spawnTeamFieldString},
	{"lead_session_id", spawnTeamFieldString},
	{"strategy", spawnTeamFieldString},
	{"status", spawnTeamFieldString},
	{"max_teammates", spawnTeamFieldNumber},
	{"max_writers", spawnTeamFieldNumber},
	{"allow_existing", spawnTeamFieldBool},
	{"auto_start", spawnTeamFieldBool},
}

var spawnTeamTeammateFieldKinds = []struct {
	key  string
	want string
}{
	{"id", spawnTeamFieldString},
	{"name", spawnTeamFieldString},
	{"profile", spawnTeamFieldString},
	{"session_id", spawnTeamFieldString},
	{"state", spawnTeamFieldString},
	{"capabilities", spawnTeamFieldStringOrList},
}

var spawnTeamTaskFieldKinds = []struct {
	key  string
	want string
}{
	{"id", spawnTeamFieldString},
	{"title", spawnTeamFieldString},
	{"goal", spawnTeamFieldString},
	{"difficulty", spawnTeamFieldString},
	{"difficulty_rationale", spawnTeamFieldString},
	{"task_type", spawnTeamFieldString},
	{"task_subject", spawnTeamFieldString},
	{"assignee", spawnTeamFieldString},
	{"priority", spawnTeamFieldNumber},
	{"inputs", spawnTeamFieldStringOrList},
	{"read_paths", spawnTeamFieldStringOrList},
	{"write_paths", spawnTeamFieldStringOrList},
	{"deliverables", spawnTeamFieldStringOrList},
	{"depends_on", spawnTeamFieldStringOrList},
}

// validateSpawnTeamArgTypes checks the top-level spawn_team arguments.
func validateSpawnTeamArgTypes(args map[string]interface{}) error {
	for _, field := range spawnTeamTopLevelFieldKinds {
		if err := validateSpawnTeamFieldType("spawn_team", field.key, field.want, args[field.key]); err != nil {
			return err
		}
	}
	return nil
}

// validateSpawnTeamTeammateTypes checks one teammates[] entry.
func validateSpawnTeamTeammateTypes(index int, entry map[string]interface{}) error {
	for _, field := range spawnTeamTeammateFieldKinds {
		owner := fmt.Sprintf("spawn_team teammates[%d]", index)
		if err := validateSpawnTeamFieldType(owner, field.key, field.want, entry[field.key]); err != nil {
			return err
		}
	}
	return nil
}

// validateSpawnTeamTaskTypes checks one tasks[] entry.
func validateSpawnTeamTaskTypes(index int, entry map[string]interface{}) error {
	for _, field := range spawnTeamTaskFieldKinds {
		owner := fmt.Sprintf("spawn_team tasks[%d]", index)
		if err := validateSpawnTeamFieldType(owner, field.key, field.want, entry[field.key]); err != nil {
			return err
		}
	}
	return nil
}

func validateSpawnTeamFieldType(owner, key, want string, value interface{}) error {
	if value == nil {
		return nil
	}
	if spawnTeamValueMatchesKind(value, want) {
		return nil
	}
	return fmt.Errorf("%s field %q must be a %s, got %T (%v); omit it to use the runtime default",
		owner, key, want, value, value)
}

func spawnTeamValueMatchesKind(value interface{}, want string) bool {
	switch want {
	case spawnTeamFieldString:
		_, ok := value.(string)
		return ok
	case spawnTeamFieldNumber:
		switch value.(type) {
		case int, int8, int16, int32, int64,
			uint, uint8, uint16, uint32, uint64,
			float32, float64, json.Number:
			return true
		default:
			return false
		}
	case spawnTeamFieldBool:
		_, ok := value.(bool)
		return ok
	case spawnTeamFieldStringOrList:
		switch typed := value.(type) {
		case string:
			// coerceStringSlice intentionally accepts a bare string and wraps it;
			// that leniency stays, only other kinds are rejected.
			return true
		case []string:
			return true
		case []interface{}:
			for _, item := range typed {
				if _, ok := item.(string); !ok {
					return false
				}
			}
			return true
		default:
			return false
		}
	default:
		return true
	}
}
