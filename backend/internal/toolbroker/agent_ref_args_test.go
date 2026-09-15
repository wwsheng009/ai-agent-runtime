package toolbroker

import (
	"context"
	"strings"
	"testing"
)

// TestStringValueRendersNilAsEmpty pins the defect that created agent sessions
// literally named "<nil>": fmt.Sprintf("%v", nil) prints "<nil>", and the tools
// that act on an existing child session read their id argument through this
// helper. An absent or null argument must read as empty instead.
func TestStringValueRendersNilAsEmpty(t *testing.T) {
	var typedNil *int
	cases := map[string]struct {
		value interface{}
		want  string
	}{
		"nil interface":     {nil, ""},
		"typed nil pointer": {typedNil, ""},
		"nil map":           {map[string]interface{}(nil), ""},
		"string":            {"child-1", "child-1"},
		"number":            {float64(30), "30"},
		"bool":              {true, "true"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := stringValue(tc.value); got != tc.want {
				t.Fatalf("stringValue(%#v) = %q, want %q", tc.value, got, tc.want)
			}
		})
	}
}

// TestBroker_Execute_ResumeAgentTreatsNullIDAsMissing guards the reported
// regression: resume_agent called with a JSON null id returned
// id/session_id = "<nil>" and materialized a session row named "<nil>".
func TestBroker_Execute_ResumeAgentTreatsNullIDAsMissing(t *testing.T) {
	controller := &fakeAgentSessionController{}
	broker := &Broker{AgentSessions: controller}

	_, _, err := broker.Execute(context.Background(), "parent-session", ToolResumeAgent, map[string]interface{}{
		"id":         nil,
		"session_id": nil,
	})
	if err == nil || !strings.Contains(err.Error(), "id is required") {
		t.Fatalf("a null id must fail closed with id is required, got %v", err)
	}
	if controller.lastResume != "" {
		t.Fatalf("a null id must never be resolved into a session reference, got %q", controller.lastResume)
	}
}

// TestBroker_Execute_RejectsRenderedPlaceholderSessionRef keeps every tool that
// acts on an existing child session from accepting the "<nil>" text a model
// copies out of a rendered tool result. close_agent is destructive, so an
// unusable reference must not reach the controller at all.
func TestBroker_Execute_RejectsRenderedPlaceholderSessionRef(t *testing.T) {
	cases := map[string]struct {
		tool string
		key  string
	}{
		"resume id":         {ToolResumeAgent, "id"},
		"resume session_id": {ToolResumeAgent, "session_id"},
		"close id":          {ToolCloseAgent, "id"},
		"close session_id":  {ToolCloseAgent, "session_id"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			for _, placeholder := range []string{"<nil>", "null", "nil", "undefined", " NULL "} {
				controller := &fakeAgentSessionController{}
				broker := &Broker{AgentSessions: controller}

				_, _, err := broker.Execute(context.Background(), "parent-session", tc.tool, map[string]interface{}{
					tc.key: placeholder,
				})
				if err == nil || !strings.Contains(err.Error(), "id is required") {
					t.Fatalf("%s=%q must fail closed with id is required, got %v", tc.key, placeholder, err)
				}
				if controller.lastResume != "" || controller.lastClose != "" {
					t.Fatalf("%s=%q must not reach the controller: resume=%q close=%q",
						tc.key, placeholder, controller.lastResume, controller.lastClose)
				}
			}
		})
	}
}

// TestHandleAliasSet_RejectsRenderedPlaceholderReference covers the aliased
// path: when a session context store is configured, references are resolved
// through the handle registry, which passes unknown ids through as real session
// ids. A rendered placeholder must never be passed through.
func TestHandleAliasSet_RejectsRenderedPlaceholderReference(t *testing.T) {
	set := &handleAliasSet{}

	for _, placeholder := range []string{"<nil>", "null", "NIL"} {
		actual, alias, err := set.resolve(placeholder, agentSessionAliasPrefix, "agent session")
		if err == nil {
			t.Fatalf("%q must be rejected, got actual=%q alias=%q", placeholder, actual, alias)
		}
		if !strings.Contains(err.Error(), "placeholder") || !strings.Contains(err.Error(), agentSessionAliasPrefix) {
			t.Fatalf("placeholder error must be actionable, got %v", err)
		}
	}

	actual, _, err := set.resolve("session_20260915071706_NPxQlxCs", agentSessionAliasPrefix, "agent session")
	if err != nil || actual != "session_20260915071706_NPxQlxCs" {
		t.Fatalf("a real session id must stay resolvable, got actual=%q err=%v", actual, err)
	}
}

// TestBroker_Execute_SpawnAgentRejectsRenderedPlaceholderID keeps the creation
// path from materializing a session literally named "<nil>": when the model asks
// for an explicit child id, that id must be a real value, and the error teaches
// the caller that omitting the id is the way to get a generated one.
func TestBroker_Execute_SpawnAgentRejectsRenderedPlaceholderID(t *testing.T) {
	for _, field := range []string{"id", "session_id"} {
		t.Run(field, func(t *testing.T) {
			for _, placeholder := range []string{"<nil>", "null", " NULL "} {
				controller := &fakeAgentSessionController{}
				broker := &Broker{AgentSessions: controller}

				_, _, err := broker.Execute(context.Background(), "parent-session", ToolSpawnAgent, map[string]interface{}{
					"message": "inspect the delegation path",
					field:     placeholder,
				})
				if err == nil || !strings.Contains(err.Error(), "placeholder") {
					t.Fatalf("%s=%q must fail closed, got %v", field, placeholder, err)
				}
				if controller.lastParent != "" {
					t.Fatalf("%s=%q must not create a child session: %#v", field, placeholder, controller.lastSpawn)
				}
			}
		})
	}
}

// TestBroker_Execute_SpawnAgentGeneratesIDForNullID guards the other half: a JSON
// null id is not an explicit id, so the runtime generates one instead of failing.
func TestBroker_Execute_SpawnAgentGeneratesIDForNullID(t *testing.T) {
	controller := &fakeAgentSessionController{}
	broker := &Broker{AgentSessions: controller}

	_, _, err := broker.Execute(context.Background(), "parent-session", ToolSpawnAgent, map[string]interface{}{
		"message":    "inspect",
		"id":         nil,
		"session_id": nil,
	})
	if err != nil {
		t.Fatalf("a null id must not fail the call: %v", err)
	}
	if controller.lastSpawn.ID != "" || controller.lastSpawn.SessionID != "" {
		t.Fatalf("a null id must not be forwarded as a session id: %#v", controller.lastSpawn)
	}
	if controller.lastSpawn.Message != "inspect" {
		t.Fatalf("the task prompt must still reach the controller: %#v", controller.lastSpawn)
	}
}

// TestBroker_Execute_SpawnTeamNormalizesPlaceholderIDs covers the team path:
// placeholder team ids fail closed, while the optional/generated per-entity ids
// (teammate id, teammate session_id, task id) are dropped so the runtime creates
// real ones instead of entities named "<nil>".
func TestBroker_Execute_SpawnTeamNormalizesPlaceholderIDs(t *testing.T) {
	store := newTeamStore(t)
	broker := &Broker{TeamStore: store}

	if _, _, err := broker.Execute(context.Background(), "parent-session", ToolSpawnTeam, map[string]interface{}{
		"team_id": "<nil>",
		"tasks":   []interface{}{map[string]interface{}{"goal": "do the work"}},
	}); err == nil || !strings.Contains(err.Error(), "placeholder") {
		t.Fatalf("a placeholder team_id must fail closed, got %v", err)
	}

	raw, _, err := broker.Execute(context.Background(), "parent-session", ToolSpawnTeam, map[string]interface{}{
		"auto_start": false,
		"teammates": []interface{}{
			map[string]interface{}{"id": "<nil>", "session_id": "null", "name": "planner"},
		},
		"tasks": []interface{}{
			map[string]interface{}{"id": " <nil> ", "title": "draft plan", "goal": "create task plan"},
		},
	})
	if err != nil {
		t.Fatalf("optional placeholder ids must be dropped, not fail the call: %v", err)
	}
	result, ok := raw.(SpawnTeamResult)
	if !ok {
		t.Fatalf("unexpected spawn_team result: %#v", raw)
	}
	if len(result.TeammateIDs) != 1 || len(result.TaskIDs) != 1 {
		t.Fatalf("expected one teammate and one task, got %#v", result)
	}
	for _, id := range append(append([]string(nil), result.TeammateIDs...), result.TaskIDs...) {
		if strings.TrimSpace(id) == "" || isRenderedPlaceholderReference(id) {
			t.Fatalf("placeholder ids must be regenerated, got %q", id)
		}
	}
}
