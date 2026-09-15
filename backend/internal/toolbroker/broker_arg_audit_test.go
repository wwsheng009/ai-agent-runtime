package toolbroker

import (
	"context"
	"strings"
	"testing"
)

// TestBrokerToolArgKeys_CoversEveryBrokerTool keeps the audit table complete: a
// broker tool without an entry would silently drop unsupported keys again, which
// is the failure class (`goal` on spawn_agent, `tools_whitelist` on tools that
// never restrict their surface) this package already fixed at the tool level.
func TestBrokerToolArgKeys_CoversEveryBrokerTool(t *testing.T) {
	tools := []string{
		ToolAskUserQuestion, ToolEnterPlanMode, ToolExitPlanMode, ToolBackgroundTask,
		ToolTaskOutput, ToolSpawnAgent, ToolListAgents, ToolSendMessage, ToolFollowupTask,
		ToolSendInput, ToolResolveAgentApproval, ToolWaitAgent, ToolReadAgentEvents,
		ToolCloseAgent, ToolResumeAgent, ToolApplyAgentWorktree, ToolDiscardAgentWorktree,
		ToolSpawnTeam, ToolWaitTeam, ToolSendTeamMessage, ToolReadMailboxDigest,
		ToolReadTaskSpec, ToolReadTaskContext, ToolReportTaskOutcome, ToolBlockCurrentTask,
		ToolSupervisionSnapshot, ToolAckLifecycle, ToolControlDescendant,
	}
	for _, tool := range tools {
		if !(&Broker{}).IsBrokerTool(tool) {
			t.Fatalf("%s is not a broker tool; this list must track the broker tool set", tool)
		}
		keys, ok := brokerToolArgKeys[tool]
		if !ok || len(keys) == 0 {
			t.Fatalf("%s has no argument allowlist: unsupported keys would be dropped silently", tool)
		}
		for _, key := range keys {
			if strings.TrimSpace(key) == "" {
				t.Fatalf("%s allowlist contains an empty key", tool)
			}
		}
	}
}

// TestBrokerToolArgKeys_MatchToolDefinitions catches schema/implementation drift:
// every property the model can see in a tool definition must be a key the broker
// actually consumes. A property that no code reads is a silent intent drop.
func TestBrokerToolArgKeys_MatchToolDefinitions(t *testing.T) {
	broker := &Broker{
		AgentSessions: &fakeAgentSessionController{},
		TeamStore:     newTeamStore(t),
		PlanMode:      &capturingPlanModeController{},
		Supervision:   &fakeSupervisionController{},
	}
	defs := append(broker.Definitions(), supervisionToolDefinitions()...)
	if len(defs) < 10 {
		t.Fatalf("expected the full broker tool set, got %d definitions", len(defs))
	}
	for _, def := range defs {
		allowed, ok := brokerToolArgKeys[normalizeToolName(def.Name)]
		if !ok {
			continue // completeness is enforced by TestBrokerToolArgKeys_CoversEveryBrokerTool
		}
		allowedSet := make(map[string]struct{}, len(allowed))
		for _, key := range allowed {
			allowedSet[key] = struct{}{}
		}
		properties, ok := def.Parameters["properties"].(map[string]interface{})
		if !ok {
			continue
		}
		for key := range properties {
			if _, ok := allowedSet[key]; !ok {
				t.Fatalf("%s advertises %q in its schema but the broker never reads it", def.Name, key)
			}
		}
	}
}

// TestAnnotateIgnoredBrokerToolArgs_ReportsDroppedKeys pins the advisory
// contract: an unused key is reported with a hint and merged into next_action,
// which the agent runtime renders back to the model.
func TestAnnotateIgnoredBrokerToolArgs_ReportsDroppedKeys(t *testing.T) {
	metadata := annotateIgnoredBrokerToolArgs(ToolWaitAgent, map[string]interface{}{
		"goal":       "wait for the child",
		"timeout_ms": 5,
	}, map[string]interface{}{"next_action": "existing guidance"})

	ignored, ok := metadata["ignored_args"].([]string)
	if !ok || len(ignored) != 1 || ignored[0] != "goal" {
		t.Fatalf("expected ignored_args=[goal], got %#v", metadata["ignored_args"])
	}
	next, _ := metadata["next_action"].(string)
	if !strings.Contains(next, "existing guidance") {
		t.Fatalf("existing next_action must be preserved, got %q", next)
	}
	if !strings.Contains(next, "goal") || !strings.Contains(next, "spawn_agent") {
		t.Fatalf("next_action must explain the dropped key and the tool that takes it, got %q", next)
	}

	supported := annotateIgnoredBrokerToolArgs(ToolWaitAgent, map[string]interface{}{"timeout_ms": 5}, nil)
	if _, ok := supported["ignored_args"]; ok {
		t.Fatalf("supported keys must not be reported: %#v", supported)
	}
}

// TestBroker_Execute_ReportsIgnoredArgumentsInsteadOfDroppingThem covers the live
// path: wait_agent without a target silently falls back to mailbox mode, so a
// caller that meant to wait for a child must learn that its key was ignored.
func TestBroker_Execute_ReportsIgnoredArgumentsInsteadOfDroppingThem(t *testing.T) {
	controller := &fakeAgentSessionController{}
	broker := &Broker{AgentSessions: controller}

	_, metadata, err := broker.Execute(context.Background(), "parent-session", ToolWaitAgent, map[string]interface{}{
		"goal":       "wait for the child",
		"timeout_ms": 10,
	})
	if err != nil {
		t.Fatalf("wait_agent must still succeed with an unsupported extra key: %v", err)
	}
	if !controller.lastWait.MailboxOnly {
		t.Fatalf("the call still falls back to mailbox mode, got %#v", controller.lastWait)
	}
	ignored, ok := metadata["ignored_args"].([]string)
	if !ok || len(ignored) != 1 || ignored[0] != "goal" {
		t.Fatalf("expected ignored_args=[goal], got %#v", metadata["ignored_args"])
	}
	if next, _ := metadata["next_action"].(string); !strings.Contains(next, "spawn_agent") {
		t.Fatalf("expected an actionable hint, got %q", next)
	}
}

// TestBroker_Execute_SupportedArgumentsAreNotReported guards against false
// positives: a canonical call of each agent tool must stay free of ignored_args.
func TestBroker_Execute_SupportedArgumentsAreNotReported(t *testing.T) {
	controller := &fakeAgentSessionController{}
	broker := &Broker{AgentSessions: controller}

	calls := []struct {
		tool string
		args map[string]interface{}
	}{
		{ToolResumeAgent, map[string]interface{}{"id": "child-1"}},
		{ToolCloseAgent, map[string]interface{}{"session_id": "child-1"}},
		{ToolListAgents, map[string]interface{}{"include_closed": true}},
		{ToolSpawnAgent, map[string]interface{}{"message": "inspect", "read_only": true, "difficulty": "normal"}},
		{ToolApplyAgentWorktree, map[string]interface{}{"id": "child-1", "paths": []interface{}{"a.go"}, "keep": true}},
		{ToolDiscardAgentWorktree, map[string]interface{}{"id": "child-1"}},
		{ToolReadAgentEvents, map[string]interface{}{"id": "child-1", "after_seq": float64(1), "limit": float64(5), "view": "tool_progress"}},
		{ToolResolveAgentApproval, map[string]interface{}{"id": "child-1", "request_id": "req-1", "allow": true}},
		{ToolSendInput, map[string]interface{}{"id": "child-1", "message": "go on", "interrupt": true}},
		{ToolSendMessage, map[string]interface{}{"target": "child-1", "message": "status?"}},
	}
	for _, call := range calls {
		_, metadata, err := broker.Execute(context.Background(), "parent-session", call.tool, call.args)
		if err != nil {
			t.Fatalf("%s rejected a canonical call: %v", call.tool, err)
		}
		if ignored, ok := metadata["ignored_args"]; ok {
			t.Fatalf("%s reported supported keys as ignored: %#v", call.tool, ignored)
		}
	}
}
