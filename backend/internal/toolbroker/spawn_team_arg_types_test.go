package toolbroker

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSpawnTeamArgTypeValidation covers the decode contract that
// spawn_team_arg_types.go adds on top of the fail-closed unknown-field check:
// a known field with a mismatched JSON kind must fail the call instead of
// silently folding into its zero value.
func TestSpawnTeamArgTypeValidation(t *testing.T) {
	accepted := map[string]interface{}{
		"team_id":        "team-a",
		"workspace_id":   "ws-1",
		"max_teammates":  json.Number("2"),
		"max_writers":    int64(1),
		"allow_existing": false,
		"auto_start":     false,
	}
	require.NoError(t, validateSpawnTeamArgTypes(accepted))

	require.NoError(t, validateSpawnTeamTeammateTypes(0, map[string]interface{}{
		"id":           "mate-a",
		"name":         "planner",
		"capabilities": []interface{}{"read", "plan"},
	}))
	// A bare string stays lenient for the list fields: coerceStringSlice wraps it.
	require.NoError(t, validateSpawnTeamTeammateTypes(0, map[string]interface{}{
		"capabilities": "read",
	}))

	require.NoError(t, validateSpawnTeamTaskTypes(0, map[string]interface{}{
		"id":          "task-a",
		"title":       "draft plan",
		"goal":        "create task plan",
		"difficulty":  "hard",
		"priority":    json.Number("3"),
		"write_paths": []interface{}{"internal/toolbroker"},
		"depends_on":  []interface{}{"task-b"},
	}))

	topLevel := []struct {
		name    string
		args    map[string]interface{}
		wantKey string
	}{
		{name: "auto_start as string", args: map[string]interface{}{"auto_start": "false"}, wantKey: "auto_start"},
		{name: "allow_existing as string", args: map[string]interface{}{"allow_existing": "false"}, wantKey: "allow_existing"},
		{name: "max_teammates as string", args: map[string]interface{}{"max_teammates": "3"}, wantKey: "max_teammates"},
		{name: "max_writers as bool", args: map[string]interface{}{"max_writers": true}, wantKey: "max_writers"},
		{name: "strategy as number", args: map[string]interface{}{"strategy": 5}, wantKey: "strategy"},
	}
	for _, tc := range topLevel {
		t.Run(tc.name, func(t *testing.T) {
			err := validateSpawnTeamArgTypes(tc.args)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "\""+tc.wantKey+"\"")
			assert.Contains(t, err.Error(), "spawn_team")
		})
	}

	teammate := []struct {
		name    string
		entry   map[string]interface{}
		wantKey string
	}{
		{name: "name as number", entry: map[string]interface{}{"name": 7}, wantKey: "name"},
		{name: "capabilities as number", entry: map[string]interface{}{"capabilities": 5}, wantKey: "capabilities"},
		{name: "capabilities with non-string item", entry: map[string]interface{}{"capabilities": []interface{}{"read", 2}}, wantKey: "capabilities"},
	}
	for _, tc := range teammate {
		t.Run(tc.name, func(t *testing.T) {
			err := validateSpawnTeamTeammateTypes(0, tc.entry)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "\""+tc.wantKey+"\"")
			assert.Contains(t, err.Error(), "teammates[0]")
		})
	}

	task := []struct {
		name    string
		entry   map[string]interface{}
		wantKey string
	}{
		{name: "priority as string", entry: map[string]interface{}{"priority": "high"}, wantKey: "priority"},
		{name: "write_paths flipped", entry: map[string]interface{}{"write_paths": []interface{}{1}}, wantKey: "write_paths"},
		{name: "deliverables as object", entry: map[string]interface{}{"deliverables": map[string]interface{}{"a": 1}}, wantKey: "deliverables"},
		{name: "goal as number", entry: map[string]interface{}{"goal": 42}, wantKey: "goal"},
	}
	for _, tc := range task {
		t.Run(tc.name, func(t *testing.T) {
			err := validateSpawnTeamTaskTypes(0, tc.entry)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "\""+tc.wantKey+"\"")
			assert.Contains(t, err.Error(), "tasks[0]")
		})
	}
}

// TestBrokerSpawnTeamRejectsKindMismatchBeforeCreatingTeam proves the validation
// runs before any team row is written, so a rejected call leaves no partial team.
func TestBrokerSpawnTeamRejectsKindMismatchBeforeCreatingTeam(t *testing.T) {
	store := newTeamStore(t)
	ctx := context.Background()
	broker := &Broker{TeamStore: store}

	_, _, err := broker.Execute(ctx, "session-1", ToolSpawnTeam, map[string]interface{}{
		"team_id":    "team-kind-mismatch",
		"auto_start": "false",
		"tasks":      []interface{}{map[string]interface{}{"goal": "do the work"}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "auto_start")

	record, loadErr := store.GetTeam(ctx, "team-kind-mismatch")
	require.NoError(t, loadErr)
	assert.Nil(t, record, "rejected spawn_team must not create a team")
}

// TestBrokerSpawnTeamReadsEveryJSONNumberSpelling guards the sibling regression
// of the spawn_agent timeout bug: validateSpawnTeamArgTypes accepts json.Number,
// int64 and uint spellings, so the numeric reads must accept them too instead of
// dropping the value through a float64-only assertion.
func TestBrokerSpawnTeamReadsEveryJSONNumberSpelling(t *testing.T) {
	store := newTeamStore(t)
	ctx := context.Background()
	broker := &Broker{TeamStore: store}

	raw, _, err := broker.Execute(ctx, "session-1", ToolSpawnTeam, map[string]interface{}{
		"team_id":       "team-number-spellings",
		"auto_start":    false,
		"max_teammates": json.Number("2"),
		"max_writers":   int64(1),
		"teammates": []interface{}{
			map[string]interface{}{"id": "mate-a", "name": "planner", "capabilities": []interface{}{"read"}},
		},
		"tasks": []interface{}{
			map[string]interface{}{"id": "task-a", "goal": "create task plan", "priority": json.Number("3"), "assignee": "mate-a"},
		},
	})
	require.NoError(t, err)
	result, ok := raw.(SpawnTeamResult)
	require.True(t, ok)
	assert.Equal(t, "team-number-spellings", result.TeamID)

	record, err := store.GetTeam(ctx, "team-number-spellings")
	require.NoError(t, err)
	require.NotNil(t, record)
	assert.Equal(t, 2, record.MaxTeammates, "max_teammates json.Number must reach the team record")
	assert.Equal(t, 1, record.MaxWriters, "max_writers int64 must reach the team record")

	task, err := store.GetTask(ctx, "task-a")
	require.NoError(t, err)
	require.NotNil(t, task)
	assert.Equal(t, 3, task.Priority, "tasks[].priority json.Number must reach the task record")
}

// TestBrokerSpawnTeamRejectsFractionalLimit keeps the whole-number contract that
// toolArgInt64 enforces now that the numeric reads go through it.
func TestBrokerSpawnTeamRejectsFractionalLimit(t *testing.T) {
	store := newTeamStore(t)
	ctx := context.Background()
	broker := &Broker{TeamStore: store}

	_, _, err := broker.Execute(ctx, "session-1", ToolSpawnTeam, map[string]interface{}{
		"team_id":       "team-fractional",
		"max_teammates": 2.5,
	})
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "whole number"), "expected whole-number rejection, got %v", err)

	record, loadErr := store.GetTeam(ctx, "team-fractional")
	require.NoError(t, loadErr)
	assert.Nil(t, record)
}
