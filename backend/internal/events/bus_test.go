package events

import "testing"

func TestBus_SubscribeAndPublish(t *testing.T) {
	bus := NewBus()
	called := 0

	bus.Subscribe("tool.requested", func(event Event) {
		called++
		if event.Type != "tool.requested" {
			t.Fatalf("unexpected event type: %s", event.Type)
		}
	})

	bus.Publish(Event{Type: "tool.requested"})
	if called != 1 {
		t.Fatalf("expected handler to be called once, got %d", called)
	}
}

func TestBus_QueryByTraceID(t *testing.T) {
	bus := NewBusWithRetention(8)

	bus.Publish(Event{Type: "tool.requested", TraceID: "trace-1", SessionID: "session-a", Payload: map[string]interface{}{"team_id": "team-x"}})
	bus.Publish(Event{Type: "tool.completed", TraceID: "trace-1", SessionID: "session-a"})
	bus.Publish(Event{Type: "tool.requested", TraceID: "trace-2", SessionID: "session-b"})

	events := bus.Trace("trace-1", 10)
	if len(events) != 2 {
		t.Fatalf("expected 2 events for trace-1, got %d", len(events))
	}
	for _, event := range events {
		if event.TraceID != "trace-1" {
			t.Fatalf("expected trace-1, got %s", event.TraceID)
		}
	}
}

func TestBus_TeamIDFilterMatchesPayload(t *testing.T) {
	bus := NewBusWithRetention(8)

	bus.Publish(Event{Type: "tool.requested", TraceID: "trace-1", Payload: map[string]interface{}{"team_id": "team-a"}})
	bus.Publish(Event{Type: "tool.requested", TraceID: "trace-2", Payload: map[string]interface{}{"team_id": "team-b"}})
	bus.Publish(Event{Type: "tool.completed", TraceID: "trace-3"})

	traces := bus.RecentTraces(TraceFilter{TeamID: "team-a", Limit: 10})
	if len(traces) != 1 {
		t.Fatalf("expected 1 trace for team-a, got %d", len(traces))
	}
	if len(traces[0].TeamIDs) != 1 || traces[0].TeamIDs[0] != "team-a" {
		t.Fatalf("unexpected team IDs: %#v", traces[0].TeamIDs)
	}

	events := bus.Query(QueryFilter{TeamID: "team-b", Limit: 10})
	if len(events) != 1 || events[0].TraceID != "trace-2" {
		t.Fatalf("unexpected team filter result: %#v", events)
	}
}

func TestBus_QueryHonorsRetentionLimit(t *testing.T) {
	bus := NewBusWithRetention(2)

	bus.Publish(Event{Type: "tool.requested", TraceID: "trace-1"})
	bus.Publish(Event{Type: "tool.completed", TraceID: "trace-1"})
	bus.Publish(Event{Type: "subagent.started", TraceID: "trace-1"})

	events := bus.Recent(10)
	if len(events) != 2 {
		t.Fatalf("expected 2 retained events, got %d", len(events))
	}
	if events[0].Type != "tool.completed" || events[1].Type != "subagent.started" {
		t.Fatalf("unexpected retained event order: %#v", events)
	}
}

func TestBus_QueryFiltersByPrefixAndLimit(t *testing.T) {
	bus := NewBusWithRetention(8)

	bus.Publish(Event{Type: "tool.requested", TraceID: "trace-1"})
	bus.Publish(Event{Type: "tool.completed", TraceID: "trace-1"})
	bus.Publish(Event{Type: "tool.reduced", TraceID: "trace-1"})
	bus.Publish(Event{Type: "subagent.started", TraceID: "trace-1"})

	events := bus.Query(QueryFilter{
		TraceID:   "trace-1",
		EventType: "tool.",
		Limit:     2,
	})
	if len(events) != 2 {
		t.Fatalf("expected 2 filtered events, got %d", len(events))
	}
	if events[0].Type != "tool.completed" || events[1].Type != "tool.reduced" {
		t.Fatalf("unexpected filtered events: %#v", events)
	}
}

func TestBus_QueryFiltersByProfileResourceKind(t *testing.T) {
	bus := NewBusWithRetention(16)
	bus.Publish(Event{
		Type:    "context.profile.injected",
		TraceID: "trace-memory",
		Payload: map[string]interface{}{
			"profile_source_refs": []interface{}{
				"profile-resource:memory:E:/profiles/dev/agents/tester/memory/memory.json",
			},
		},
	})
	bus.Publish(Event{
		Type:    "recall.performed",
		TraceID: "trace-notes",
		Payload: map[string]interface{}{
			"source_refs": []interface{}{
				"profile-resource:notes:E:/profiles/dev/agents/tester/context/notes.md",
			},
		},
	})

	events := bus.Query(QueryFilter{ProfileResourceKind: "notes", Limit: 10})
	if len(events) != 1 {
		t.Fatalf("expected 1 notes event, got %d", len(events))
	}
	if events[0].TraceID != "trace-notes" {
		t.Fatalf("expected trace-notes, got %s", events[0].TraceID)
	}
}

func TestBus_RecentTraces_GroupsAndOrdersByLatestEvent(t *testing.T) {
	bus := NewBusWithRetention(16)

	bus.Publish(Event{Type: "tool.requested", TraceID: "trace-1", AgentName: "agent-a", SessionID: "session-a", ToolName: "read_logs"})
	bus.Publish(Event{Type: "tool.completed", TraceID: "trace-1", AgentName: "agent-a", SessionID: "session-a", ToolName: "read_logs", Payload: map[string]interface{}{"mcp_name": "mcp-a", "transport_type": "stdio"}})
	bus.Publish(Event{Type: "tool.denied", TraceID: "trace-1", AgentName: "agent-a", ToolName: "write_file", Payload: map[string]interface{}{"policy": "read_only"}})
	bus.Publish(Event{Type: "patch.decision", TraceID: "trace-1", AgentName: "agent-a", ToolName: "spawn_subagents", Payload: map[string]interface{}{"patch_decision": "approved_override", "patch_decision_policy": "strict", "patch_approval": map[string]interface{}{"ticket_id": "CAB-1"}}})
	bus.Publish(Event{Type: "mcp.transport.connected", TraceID: "trace-2", AgentName: "mcp-manager", Payload: map[string]interface{}{"mcp_name": "mcp-b", "transport_type": "websocket"}})

	traces := bus.RecentTraces(TraceFilter{Limit: 10})
	if len(traces) != 2 {
		t.Fatalf("expected 2 traces, got %d", len(traces))
	}
	if traces[0].TraceID != "trace-2" {
		t.Fatalf("expected most recent trace first, got %s", traces[0].TraceID)
	}
	if traces[1].TraceID != "trace-1" {
		t.Fatalf("expected trace-1 second, got %s", traces[1].TraceID)
	}
	if traces[1].EventCount != 4 {
		t.Fatalf("expected 4 events in trace-1, got %d", traces[1].EventCount)
	}
	if len(traces[1].MCPNames) != 1 || traces[1].MCPNames[0] != "mcp-a" {
		t.Fatalf("expected mcp-a summary, got %#v", traces[1].MCPNames)
	}
	if traces[1].Governance.DeniedEvents != 1 || traces[1].Governance.ToolDenied != 1 {
		t.Fatalf("expected governance denial summary, got %#v", traces[1].Governance)
	}
	if traces[1].Governance.Policies["read_only"] != 1 {
		t.Fatalf("expected read_only policy count, got %#v", traces[1].Governance.Policies)
	}
	if traces[1].Governance.PatchDecisions != 1 || traces[1].Governance.PatchApprovedOverride != 1 {
		t.Fatalf("expected patch governance summary, got %#v", traces[1].Governance)
	}
	if traces[1].Governance.PatchApprovalsWithTicket != 1 {
		t.Fatalf("expected patch approval ticket summary, got %#v", traces[1].Governance)
	}
	if len(traces[1].PatchApprovalTickets) != 1 || traces[1].PatchApprovalTickets[0] != "CAB-1" {
		t.Fatalf("expected patch approval tickets, got %#v", traces[1].PatchApprovalTickets)
	}
	if len(traces[0].TransportTypes) != 1 || traces[0].TransportTypes[0] != "websocket" {
		t.Fatalf("expected websocket transport summary, got %#v", traces[0].TransportTypes)
	}
}

func TestBus_RecentTraces_FiltersByPrefixAndEventType(t *testing.T) {
	bus := NewBusWithRetention(16)

	bus.Publish(Event{Type: "tool.requested", TraceID: "trace-1"})
	bus.Publish(Event{Type: "mcp.transport.connected", TraceID: "trace-2"})
	bus.Publish(Event{Type: "mcp.client.session.connected", TraceID: "ops-1"})

	traces := bus.RecentTraces(TraceFilter{
		TraceIDPrefix: "trace-",
		EventType:     "mcp.",
		Limit:         10,
	})
	if len(traces) != 1 {
		t.Fatalf("expected 1 filtered trace, got %d", len(traces))
	}
	if traces[0].TraceID != "trace-2" {
		t.Fatalf("expected trace-2, got %s", traces[0].TraceID)
	}
	if _, ok := traces[0].EventTypes["mcp.transport.connected"]; !ok {
		t.Fatalf("expected mcp.transport.connected summary, got %#v", traces[0].EventTypes)
	}
}

func TestBus_RecentTraces_ExecutionSummaryAndToolFilter(t *testing.T) {
	bus := NewBusWithRetention(32)

	bus.Publish(Event{Type: "tool.requested", TraceID: "trace-1", ToolName: "read_logs"})
	bus.Publish(Event{Type: "tool.completed", TraceID: "trace-1", ToolName: "read_logs"})
	bus.Publish(Event{Type: "tool.reduced", TraceID: "trace-1", ToolName: "read_logs", Payload: map[string]interface{}{"reducer": "text_truncation", "artifact_ref_count": 1}})
	bus.Publish(Event{Type: "subagent.batch.started", TraceID: "trace-1", ToolName: "spawn_subagents"})
	bus.Publish(Event{Type: "subagent.started", TraceID: "trace-1", Payload: map[string]interface{}{"role": "researcher"}})
	bus.Publish(Event{Type: "subagent.completed", TraceID: "trace-1", Payload: map[string]interface{}{"role": "researcher"}})
	bus.Publish(Event{Type: "subagent.batch.completed", TraceID: "trace-1", ToolName: "spawn_subagents"})
	bus.Publish(Event{Type: "patch.applied", TraceID: "trace-1", ToolName: "spawn_subagents", Payload: map[string]interface{}{"applied_by": []interface{}{"writer-1"}, "artifact_ref_count": 2}})
	bus.Publish(Event{Type: "tool.reduced", TraceID: "trace-2", ToolName: "run_tests", Payload: map[string]interface{}{"reducer": "go_test_json"}})

	traces := bus.RecentTraces(TraceFilter{ToolName: "read_logs", Limit: 10})
	if len(traces) != 1 {
		t.Fatalf("expected 1 filtered trace, got %d", len(traces))
	}
	trace := traces[0]
	if trace.TraceID != "trace-1" {
		t.Fatalf("expected trace-1, got %s", trace.TraceID)
	}
	if trace.Execution.ToolRequested != 1 || trace.Execution.ToolCompleted != 1 || trace.Execution.ToolReduced != 1 {
		t.Fatalf("unexpected tool execution summary: %#v", trace.Execution)
	}
	if trace.Execution.ArtifactRefs != 3 {
		t.Fatalf("expected artifact_refs=3, got %#v", trace.Execution)
	}
	if trace.Execution.Reducers["text_truncation"] != 1 {
		t.Fatalf("expected reducer summary, got %#v", trace.Execution.Reducers)
	}
	if trace.Execution.SubagentBatches != 1 || trace.Execution.SubagentBatchDone != 1 {
		t.Fatalf("unexpected subagent batch summary: %#v", trace.Execution)
	}
	if trace.Execution.SubagentStarted != 1 || trace.Execution.SubagentCompleted != 1 {
		t.Fatalf("unexpected subagent lifecycle summary: %#v", trace.Execution)
	}
	if trace.Execution.SubagentRoles["researcher"] != 1 {
		t.Fatalf("unexpected subagent role summary: %#v", trace.Execution.SubagentRoles)
	}
	if trace.Execution.PatchApplied != 1 || trace.Execution.AppliedBy["writer-1"] != 1 {
		t.Fatalf("unexpected patch apply summary: %#v", trace.Execution)
	}
}

func TestBus_RecentTraces_AggregatesProfileProvenance(t *testing.T) {
	bus := NewBusWithRetention(16)

	bus.Publish(Event{
		Type:    "context.profile.injected",
		TraceID: "trace-profile",
		Payload: map[string]interface{}{
			"profile_source_refs": []interface{}{
				"profile-resource:memory:E:/profiles/dev/agents/tester/memory/memory.json",
				"profile-resource:notes:E:/profiles/dev/agents/tester/context/notes.md",
			},
		},
	})
	bus.Publish(Event{
		Type:    "recall.performed",
		TraceID: "trace-profile",
		Payload: map[string]interface{}{
			"source_refs": []interface{}{
				"profile-resource:memory:E:/profiles/dev/agents/tester/memory/memory.json",
			},
		},
	})

	traces := bus.RecentTraces(TraceFilter{TraceIDPrefix: "trace-", Limit: 10})
	if len(traces) != 1 {
		t.Fatalf("expected 1 trace, got %d", len(traces))
	}
	trace := traces[0]
	if trace.Provenance.ProfileContextInjected != 1 {
		t.Fatalf("expected profile_context_injected=1, got %#v", trace.Provenance)
	}
	if trace.Provenance.RecallWithSourceRefs != 1 {
		t.Fatalf("expected recall_with_source_refs=1, got %#v", trace.Provenance)
	}
	if len(trace.Provenance.ProfileResourceRefs) != 2 {
		t.Fatalf("expected 2 unique profile refs, got %#v", trace.Provenance.ProfileResourceRefs)
	}
	if trace.Provenance.ProfileResourceCount != 2 || trace.Provenance.ProfileMemoryCount != 1 || trace.Provenance.ProfileNotesCount != 1 {
		t.Fatalf("unexpected provenance display counts: %#v", trace.Provenance)
	}
	if len(trace.Provenance.ProfileResourceLabels) != 2 {
		t.Fatalf("expected compact labels, got %#v", trace.Provenance.ProfileResourceLabels)
	}
	if trace.Provenance.ProfileResourceKinds["memory"] != 2 {
		t.Fatalf("expected memory kind count 2, got %#v", trace.Provenance.ProfileResourceKinds)
	}
	if trace.Provenance.ProfileResourceKinds["notes"] != 1 {
		t.Fatalf("expected notes kind count 1, got %#v", trace.Provenance.ProfileResourceKinds)
	}
}

func TestBus_TraceStats_AggregatesRecentTraces(t *testing.T) {
	bus := NewBusWithRetention(16)

	bus.Publish(Event{
		Type:      "tool.requested",
		TraceID:   "trace-1",
		AgentName: "agent-a",
		SessionID: "session-a",
		ToolName:  "read_logs",
	})
	bus.Publish(Event{
		Type:      "mcp.transport.connected",
		TraceID:   "trace-1",
		AgentName: "mcp-manager",
		Payload: map[string]interface{}{
			"mcp_name":       "echo-test",
			"transport_type": "websocket",
		},
	})
	bus.Publish(Event{
		Type:      "tool.completed",
		TraceID:   "trace-2",
		AgentName: "agent-b",
		SessionID: "session-b",
		ToolName:  "run_tests",
	})
	bus.Publish(Event{
		Type:    "subagent.denied",
		TraceID: "trace-2",
		Payload: map[string]interface{}{
			"policy": "single_writer",
		},
	})
	bus.Publish(Event{
		Type:    "patch.decision",
		TraceID: "trace-2",
		Payload: map[string]interface{}{
			"patch_decision":        "blocked",
			"patch_decision_policy": "warn",
		},
	})
	bus.Publish(Event{
		Type:     "tool.reduced",
		TraceID:  "trace-2",
		ToolName: "run_tests",
		Payload: map[string]interface{}{
			"reducer":            "go_test_json",
			"artifact_ref_count": 1,
		},
	})
	bus.Publish(Event{
		Type:    "subagent.batch.started",
		TraceID: "trace-2",
	})
	bus.Publish(Event{
		Type:    "subagent.started",
		TraceID: "trace-2",
		Payload: map[string]interface{}{
			"role": "verifier",
		},
	})

	stats := bus.TraceStats(TraceFilter{TraceIDPrefix: "trace-", Limit: 10})
	if stats.TraceCount != 2 {
		t.Fatalf("expected 2 traces, got %d", stats.TraceCount)
	}
	if stats.EventCount != 8 {
		t.Fatalf("expected 8 events, got %d", stats.EventCount)
	}
	if stats.EventTypes["tool.requested"] != 1 || stats.EventTypes["tool.completed"] != 1 {
		t.Fatalf("unexpected event type stats: %#v", stats.EventTypes)
	}
	if stats.Agents["agent-a"] != 1 || stats.Agents["agent-b"] != 1 {
		t.Fatalf("unexpected agent stats: %#v", stats.Agents)
	}
	if stats.MCPNames["echo-test"] != 1 {
		t.Fatalf("unexpected mcp stats: %#v", stats.MCPNames)
	}
	if stats.TransportTypes["websocket"] != 1 {
		t.Fatalf("unexpected transport stats: %#v", stats.TransportTypes)
	}
	if stats.Governance.DeniedEvents != 1 || stats.Governance.SubagentDenied != 1 {
		t.Fatalf("unexpected governance stats: %#v", stats.Governance)
	}
	if stats.Governance.Policies["single_writer"] != 1 {
		t.Fatalf("unexpected governance policy stats: %#v", stats.Governance.Policies)
	}
	if stats.Governance.PatchDecisions != 1 || stats.Governance.PatchBlocked != 1 {
		t.Fatalf("unexpected patch governance stats: %#v", stats.Governance)
	}
	if stats.Governance.PatchPolicies["warn"] != 1 {
		t.Fatalf("unexpected patch policy stats: %#v", stats.Governance.PatchPolicies)
	}
	if stats.Execution.ToolReduced != 1 || stats.Execution.Reducers["go_test_json"] != 1 {
		t.Fatalf("unexpected execution reducer stats: %#v", stats.Execution)
	}
	if stats.Execution.ArtifactRefs != 1 {
		t.Fatalf("unexpected artifact ref stats: %#v", stats.Execution)
	}
	if stats.Execution.SubagentBatches != 1 || stats.Execution.SubagentStarted != 1 {
		t.Fatalf("unexpected subagent execution stats: %#v", stats.Execution)
	}
	if stats.Execution.SubagentRoles["verifier"] != 1 {
		t.Fatalf("unexpected subagent roles: %#v", stats.Execution.SubagentRoles)
	}
	if stats.Provenance.ProfileContextInjected != 0 || stats.Provenance.RecallWithSourceRefs != 0 {
		t.Fatalf("unexpected provenance defaults: %#v", stats.Provenance)
	}
	if len(stats.LatestTraceIDs) != 2 || stats.LatestTraceIDs[0] != "trace-2" {
		t.Fatalf("unexpected latest trace order: %#v", stats.LatestTraceIDs)
	}
}

func TestBus_TraceStats_AggregatesProfileProvenance(t *testing.T) {
	bus := NewBusWithRetention(16)

	bus.Publish(Event{
		Type:    "context.profile.injected",
		TraceID: "trace-one",
		Payload: map[string]interface{}{
			"profile_source_refs": []interface{}{
				"profile-resource:memory:E:/profiles/dev/agents/tester/memory/memory.json",
			},
		},
	})
	bus.Publish(Event{
		Type:    "recall.performed",
		TraceID: "trace-two",
		Payload: map[string]interface{}{
			"source_refs": []interface{}{
				"profile-resource:notes:E:/profiles/dev/agents/tester/context/notes.md",
			},
		},
	})

	stats := bus.TraceStats(TraceFilter{TraceIDPrefix: "trace-", Limit: 10})
	if stats.Provenance.ProfileContextInjected != 1 {
		t.Fatalf("expected 1 profile context event, got %#v", stats.Provenance)
	}
	if stats.Provenance.RecallWithSourceRefs != 1 {
		t.Fatalf("expected 1 recall provenance event, got %#v", stats.Provenance)
	}
	if len(stats.Provenance.ProfileResourceRefs) != 2 {
		t.Fatalf("expected 2 unique profile refs, got %#v", stats.Provenance.ProfileResourceRefs)
	}
	if stats.Provenance.ProfileResourceCount != 2 || stats.Provenance.ProfileMemoryCount != 1 || stats.Provenance.ProfileNotesCount != 1 {
		t.Fatalf("unexpected provenance display counts: %#v", stats.Provenance)
	}
	if stats.Provenance.ProfileResourceKinds["memory"] != 1 || stats.Provenance.ProfileResourceKinds["notes"] != 1 {
		t.Fatalf("unexpected profile resource kinds: %#v", stats.Provenance.ProfileResourceKinds)
	}
}

func TestBus_GovernanceStats_FocusesOnDeniedEvents(t *testing.T) {
	bus := NewBusWithRetention(16)

	bus.Publish(Event{
		Type:      "tool.denied",
		TraceID:   "trace-1",
		AgentName: "agent-a",
		SessionID: "session-a",
		ToolName:  "write_file",
		Payload: map[string]interface{}{
			"policy":   "read_only",
			"reason":   "read-only policy blocks write-like tool: write_file",
			"mcp_name": "local-mcp",
		},
	})
	bus.Publish(Event{
		Type:      "subagent.denied",
		TraceID:   "trace-2",
		AgentName: "agent-b",
		Payload: map[string]interface{}{
			"policy": "single_writer",
			"reason": "single-writer policy violation: 2 writer subagents requested",
		},
	})
	bus.Publish(Event{
		Type:    "tool.completed",
		TraceID: "trace-3",
	})
	bus.Publish(Event{
		Type:    "patch.decision",
		TraceID: "trace-3",
		Payload: map[string]interface{}{
			"patch_decision":        "approved_override",
			"patch_decision_policy": "strict",
			"patch_approval": map[string]interface{}{
				"ticket_id": "CAB-9",
			},
		},
	})

	stats := bus.GovernanceStats(TraceFilter{TraceIDPrefix: "trace-", Limit: 10})
	if stats.TraceCount != 3 {
		t.Fatalf("expected 3 governance traces, got %d", stats.TraceCount)
	}
	if stats.DeniedEvents != 2 || stats.ToolDenied != 1 || stats.SubagentDenied != 1 {
		t.Fatalf("unexpected governance totals: %#v", stats)
	}
	if stats.PatchDecisions != 1 || stats.PatchApprovedOverride != 1 || stats.PatchApprovalsWithTicket != 1 {
		t.Fatalf("unexpected patch governance totals: %#v", stats)
	}
	if stats.Policies["read_only"] != 1 || stats.Policies["single_writer"] != 1 {
		t.Fatalf("unexpected policy stats: %#v", stats.Policies)
	}
	if stats.PatchPolicies["strict"] != 1 {
		t.Fatalf("unexpected patch policy stats: %#v", stats.PatchPolicies)
	}
	if stats.Reasons["read-only policy blocks write-like tool: write_file"] != 1 {
		t.Fatalf("unexpected reason stats: %#v", stats.Reasons)
	}
	if stats.Tools["write_file"] != 1 {
		t.Fatalf("unexpected tool stats: %#v", stats.Tools)
	}
	if stats.Agents["agent-a"] != 1 || stats.Agents["agent-b"] != 1 {
		t.Fatalf("unexpected agent stats: %#v", stats.Agents)
	}
	if stats.MCPNames["local-mcp"] != 1 {
		t.Fatalf("unexpected mcp stats: %#v", stats.MCPNames)
	}
}

func TestBus_GovernanceStats_AggregatesProvenance(t *testing.T) {
	bus := NewBusWithRetention(16)
	bus.Publish(Event{
		Type:    "patch.decision",
		TraceID: "trace-governance-prov",
		Payload: map[string]interface{}{
			"patch_decision": "blocked",
		},
	})
	bus.Publish(Event{
		Type:    "checkpoint_created",
		TraceID: "trace-governance-prov",
		Payload: map[string]interface{}{
			"profile_source_refs": []interface{}{
				"profile-resource:memory:E:/profiles/dev/agents/tester/memory/memory.json",
			},
		},
	})

	stats := bus.GovernanceStats(TraceFilter{TraceIDPrefix: "trace-", ProfileResourceKind: "memory", Limit: 10})
	if stats.TraceCount != 1 {
		t.Fatalf("expected 1 trace, got %d", stats.TraceCount)
	}
	if stats.Provenance.ProfileResourceCount != 1 || stats.Provenance.ProfileMemoryCount != 1 {
		t.Fatalf("unexpected provenance summary: %#v", stats.Provenance)
	}
}
