package toolbroker

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateSpawnAgentArgTypesAcceptsDocumentedKinds(t *testing.T) {
	args := map[string]interface{}{
		"message":                "inspect repo",
		"agent_type":             "explorer",
		"difficulty":             "hard",
		"difficulty_rationale":   "cross-package change",
		"provider":               "local",
		"model":                  "coder",
		"reasoning_effort":       "high",
		"thinking_effort":        "high",
		"permission_mode":        "plan",
		"completion_requirement": "none",
		"isolation":              "worktree",
		"fork_turns":             "2",
		"read_only":              true,
		"fork_context":           false,
		"timeout_sec":            float64(600),
		"progress_timeout_sec":   json.Number("120"),
		"approval_timeout_sec":   int64(30),
		"cancel_grace_sec":       5,
	}
	if err := validateSpawnAgentArgTypes(args); err != nil {
		t.Fatalf("documented kinds must pass, got %v", err)
	}
}

func TestValidateSpawnAgentArgTypesRejectsKindMismatch(t *testing.T) {
	cases := []struct {
		name    string
		args    map[string]interface{}
		wantKey string
	}{
		{
			name:    "read_only as string",
			args:    map[string]interface{}{"read_only": "true"},
			wantKey: "read_only",
		},
		{
			name:    "fork_context as string",
			args:    map[string]interface{}{"fork_context": "false"},
			wantKey: "fork_context",
		},
		{
			name:    "fork_turns as number",
			args:    map[string]interface{}{"fork_turns": 2},
			wantKey: "fork_turns",
		},
		{
			name:    "isolation as number",
			args:    map[string]interface{}{"isolation": 5},
			wantKey: "isolation",
		},
		{
			name:    "permission_mode as number",
			args:    map[string]interface{}{"permission_mode": 1},
			wantKey: "permission_mode",
		},
		{
			name:    "difficulty as number",
			args:    map[string]interface{}{"difficulty": 42},
			wantKey: "difficulty",
		},
		{
			name:    "provider as number",
			args:    map[string]interface{}{"provider": 123},
			wantKey: "provider",
		},
		{
			name:    "timeout_sec as string",
			args:    map[string]interface{}{"timeout_sec": "600"},
			wantKey: "timeout_sec",
		},
		{
			name:    "completion_requirement as bool",
			args:    map[string]interface{}{"completion_requirement": true},
			wantKey: "completion_requirement",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateSpawnAgentArgTypes(tc.args)
			if err == nil {
				t.Fatalf("expected %s kind mismatch to fail closed", tc.wantKey)
			}
			if !strings.Contains(err.Error(), "\""+tc.wantKey+"\"") {
				t.Fatalf("error should name the offending key, got %v", err)
			}
			if !strings.Contains(err.Error(), "spawn_agent") {
				t.Fatalf("error should name the tool, got %v", err)
			}
		})
	}
}

func TestSpawnAgentIntArgCoercion(t *testing.T) {
	cases := []struct {
		name    string
		value   interface{}
		want    int64
		wantErr string
	}{
		{name: "int64", value: int64(90), want: 90},
		{name: "int", value: 30, want: 30},
		{name: "float64 integral", value: float64(600), want: 600},
		{name: "json.Number integral", value: json.Number("120"), want: 120},
		{name: "json.Number fractional", value: json.Number("1.5"), wantErr: "whole number"},
		{name: "float64 fractional", value: float64(1.5), wantErr: "whole number"},
		{name: "negative float", value: float64(-5), want: -5},
		{name: "string", value: "600", wantErr: "must be a JSON number"},
		{name: "bool", value: true, wantErr: "must be a JSON number"},
		{name: "uint64 overflow", value: uint64(1 << 63), wantErr: "out of range"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := map[string]interface{}{"timeout_sec": tc.value}
			got, ok, err := toolArgInt64("spawn_agent", args, "timeout_sec")
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("expected error containing %q, got value=%d ok=%v err=%v", tc.wantErr, got, ok, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !ok || got != tc.want {
				t.Fatalf("expected %d (ok), got %d ok=%v", tc.want, got, ok)
			}
		})
	}

	if _, ok, err := toolArgInt64("spawn_agent", map[string]interface{}{}, "timeout_sec"); err != nil || ok {
		t.Fatalf("absent key must report ok=false without error, got ok=%v err=%v", ok, err)
	}
}

// TestBroker_SpawnAgentJSONNumberTimeoutsReachRequest guards the regression that
// made the four supervision budgets unreadable from real tool calls: JSON
// numbers arrive as float64 (or json.Number), never int64.
func TestBroker_SpawnAgentJSONNumberTimeoutsReachRequest(t *testing.T) {
	controller := &fakeAgentSessionController{}
	broker := &Broker{AgentSessions: controller}

	_, _, err := broker.Execute(context.Background(), "parent-session", ToolSpawnAgent, map[string]interface{}{
		"message":              "run a bounded task",
		"timeout_sec":          float64(600),
		"progress_timeout_sec": json.Number("120"),
		"approval_timeout_sec": float64(30),
		"cancel_grace_sec":     5,
	})
	if err != nil {
		t.Fatalf("spawn_agent failed: %v", err)
	}
	if controller.lastSpawn.TimeoutSec != 600 {
		t.Fatalf("timeout_sec float64 must reach the request, got %d", controller.lastSpawn.TimeoutSec)
	}
	if controller.lastSpawn.ProgressTimeoutSec != 120 {
		t.Fatalf("progress_timeout_sec json.Number must reach the request, got %d", controller.lastSpawn.ProgressTimeoutSec)
	}
	if controller.lastSpawn.ApprovalTimeoutSec != 30 {
		t.Fatalf("approval_timeout_sec float64 must reach the request, got %d", controller.lastSpawn.ApprovalTimeoutSec)
	}
	if controller.lastSpawn.CancelGraceSec != 5 {
		t.Fatalf("cancel_grace_sec int must reach the request, got %d", controller.lastSpawn.CancelGraceSec)
	}
}

// TestBroker_SpawnAgentJSONNumberNegativeTimeoutFailsClosed covers the check
// that used to be unreachable: a negative JSON number was dropped before the
// non-negative validation ran.
func TestBroker_SpawnAgentJSONNumberNegativeTimeoutFailsClosed(t *testing.T) {
	controller := &fakeAgentSessionController{}
	broker := &Broker{AgentSessions: controller}

	_, _, err := broker.Execute(context.Background(), "parent-session", ToolSpawnAgent, map[string]interface{}{
		"message":     "run a bounded task",
		"timeout_sec": float64(-5),
	})
	if err == nil || !strings.Contains(err.Error(), "non-negative") {
		t.Fatalf("expected non-negative rejection, got %v", err)
	}
	if controller.lastSpawn.Message != "" {
		t.Fatalf("rejected spawn must not reach the controller, got %#v", controller.lastSpawn)
	}
}

func TestBroker_SpawnAgentKindMismatchFailsClosed(t *testing.T) {
	controller := &fakeAgentSessionController{}
	broker := &Broker{AgentSessions: controller}

	_, _, err := broker.Execute(context.Background(), "parent-session", ToolSpawnAgent, map[string]interface{}{
		"message":   "inspect repo",
		"read_only": "true",
	})
	if err == nil || !strings.Contains(err.Error(), "read_only") {
		t.Fatalf("expected read_only kind mismatch to fail closed, got %v", err)
	}
	if controller.lastSpawn.Message != "" {
		t.Fatalf("rejected spawn must not reach the controller, got %#v", controller.lastSpawn)
	}
}

// TestBroker_SpawnAgentForkTurnsStringHonorsOverride keeps the documented
// behavior that fork_turns overrides fork_context when both are supplied.
func TestBroker_SpawnAgentForkTurnsStringHonorsOverride(t *testing.T) {
	controller := &fakeAgentSessionController{}
	broker := &Broker{AgentSessions: controller}

	_, _, err := broker.Execute(context.Background(), "parent-session", ToolSpawnAgent, map[string]interface{}{
		"message":      "inspect repo",
		"fork_turns":   "2",
		"fork_context": true,
	})
	if err != nil {
		t.Fatalf("spawn_agent failed: %v", err)
	}
	if controller.lastSpawn.ForkTurns != "2" {
		t.Fatalf("fork_turns must reach the request, got %q", controller.lastSpawn.ForkTurns)
	}
	if controller.lastSpawn.ForkContext == nil || !*controller.lastSpawn.ForkContext {
		t.Fatalf("fork_context must still be recorded, got %#v", controller.lastSpawn.ForkContext)
	}
}
