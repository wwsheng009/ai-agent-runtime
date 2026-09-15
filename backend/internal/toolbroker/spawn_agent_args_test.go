package toolbroker

import (
	"context"
	"strings"
	"testing"
)

// TestBroker_Execute_SpawnAgentResolvesMessageAliases guards the regression that
// produced child sessions with an empty initial prompt: the parent sent `goal`
// (the spawn_subagents/spawn_team spelling) and spawn_agent ignored the key, so
// the child session had no task in context and reported "no task visible here".
func TestBroker_Execute_SpawnAgentResolvesMessageAliases(t *testing.T) {
	for _, alias := range []string{"goal", "task", "prompt"} {
		t.Run(alias, func(t *testing.T) {
			controller := &fakeAgentSessionController{}
			broker := &Broker{AgentSessions: controller}

			_, meta, err := broker.Execute(context.Background(), "parent-session", ToolSpawnAgent, map[string]interface{}{
				alias:        "  inspect the delegation bug  ",
				"agent_type": "explorer",
			})
			if err != nil {
				t.Fatalf("spawn_agent with %s alias failed: %v", alias, err)
			}
			if controller.lastSpawn.Message != "inspect the delegation bug" {
				t.Fatalf("alias %s was not mapped to message: %#v", alias, controller.lastSpawn)
			}
			aliases, ok := meta["arg_aliases"].([]string)
			if !ok || len(aliases) != 1 || aliases[0] != alias+"->message" {
				t.Fatalf("alias use must be observable, got %#v", meta["arg_aliases"])
			}
		})
	}
}

// TestBroker_Execute_SpawnAgentRejectsMissingTaskPrompt keeps the fail-closed
// contract: a spawn_agent call without a task prompt must not create a child.
func TestBroker_Execute_SpawnAgentRejectsMissingTaskPrompt(t *testing.T) {
	cases := map[string]map[string]interface{}{
		"absent": {"agent_type": "explorer"},
		"blank":  {"message": "   ", "goal": "\t"},
		"empty":  {},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			controller := &fakeAgentSessionController{}
			broker := &Broker{AgentSessions: controller}

			_, _, err := broker.Execute(context.Background(), "parent-session", ToolSpawnAgent, args)
			if err == nil || !strings.Contains(err.Error(), "message is required") {
				t.Fatalf("expected message-required error, got %v", err)
			}
			if controller.lastParent != "" {
				t.Fatalf("a prompt-less spawn_agent call must not reach the controller: %#v", controller.lastSpawn)
			}
		})
	}
}

// TestBroker_Execute_SpawnAgentRejectsUnsupportedArgs keeps spawn_agent honest:
// spawn_agent implements no tool whitelist, so a caller that asks for one must
// get an actionable error instead of a child that silently ignores the request.
func TestBroker_Execute_SpawnAgentRejectsUnsupportedArgs(t *testing.T) {
	controller := &fakeAgentSessionController{}
	broker := &Broker{AgentSessions: controller}

	_, _, err := broker.Execute(context.Background(), "parent-session", ToolSpawnAgent, map[string]interface{}{
		"message":         "inspect",
		"tools_whitelist": []string{"view", "grep"},
	})
	if err == nil {
		t.Fatal("expected spawn_agent to reject unsupported arguments")
	}
	if !strings.Contains(err.Error(), "tools_whitelist") || !strings.Contains(err.Error(), "read_only") {
		t.Fatalf("unsupported-argument error must name the key and the alternative, got %v", err)
	}
	if controller.lastParent != "" {
		t.Fatalf("unsupported arguments must not create a child session: %#v", controller.lastSpawn)
	}
}

// TestBroker_Definitions_SpawnAgentDocumentsTaskPrompt pins the model-facing
// contract: message is required, and the goal/task aliases are discoverable.
func TestBroker_Definitions_SpawnAgentDocumentsTaskPrompt(t *testing.T) {
	broker := &Broker{AgentSessions: &fakeAgentSessionController{}}
	for _, def := range broker.Definitions() {
		if def.Name != ToolSpawnAgent {
			continue
		}
		if !strings.Contains(def.Description, "Required: message") {
			t.Fatalf("spawn_agent description must state the required task prompt: %q", def.Description)
		}
		properties, ok := def.Parameters["properties"].(map[string]interface{})
		if !ok {
			t.Fatalf("spawn_agent schema missing properties: %#v", def.Parameters)
		}
		for _, alias := range []string{"goal", "task"} {
			if _, ok := properties[alias]; !ok {
				t.Fatalf("spawn_agent schema must document the %s alias: %#v", alias, properties)
			}
		}
		message, ok := properties["message"].(map[string]interface{})
		if !ok {
			t.Fatalf("spawn_agent schema missing message property: %#v", properties)
		}
		if description, _ := message["description"].(string); !strings.Contains(description, "Required") {
			t.Fatalf("message description must state the requirement: %q", description)
		}
		return
	}
	t.Fatal("spawn_agent definition not found")
}

// TestPrepareSpawnTaskSpecs_RequiresTaskGoal covers the same silent-drop class
// one level deeper: a spawn_team task built with spawn_agent's `message` key has
// no goal, and the teammate would receive no assignment.
func TestPrepareSpawnTaskSpecs_RequiresTaskGoal(t *testing.T) {
	broker := &Broker{}

	if _, err := broker.prepareSpawnTaskSpecs([]SpawnTaskSpec{{ID: "t1"}}); err == nil || !strings.Contains(err.Error(), "goal") {
		t.Fatalf("expected missing-goal error, got %v", err)
	}
	if _, err := broker.prepareSpawnTaskSpecs([]SpawnTaskSpec{{ID: "t1", Title: "title only"}}); err != nil {
		t.Fatalf("a title-only task stays valid: %v", err)
	}
	if _, err := broker.prepareSpawnTaskSpecs([]SpawnTaskSpec{{ID: "t1", Goal: "do the work"}}); err != nil {
		t.Fatalf("a goal task stays valid: %v", err)
	}
}
