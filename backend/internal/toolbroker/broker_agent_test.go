package toolbroker

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

type fakeAgentSessionController struct {
	lastParent string
	lastSpawn  SpawnAgentArgs
	lastList   ListAgentsArgs
	lastMsg    AgentMessageArgs
	lastFollow AgentMessageArgs
	lastInput  SendAgentInputArgs
	lastWait   WaitAgentArgs
	lastRead   ReadAgentEventsArgs
	lastClose  string
	lastResume string
	agents     []AgentStatusResult
}

func (f *fakeAgentSessionController) Spawn(ctx context.Context, parentSessionID string, args SpawnAgentArgs) (*AgentStatusResult, error) {
	f.lastParent = parentSessionID
	f.lastSpawn = args
	return &AgentStatusResult{
		ID:                "child-1",
		SessionID:         "child-1",
		ParentSessionID:   parentSessionID,
		Status:            "running",
		Exists:            true,
		Created:           true,
		Queued:            true,
		CurrentTurnID:     "turn-dynamic-123",
		PendingToolCallID: "toolcall-dynamic-456",
	}, nil
}

func (f *fakeAgentSessionController) List(ctx context.Context, parentSessionID string, args ListAgentsArgs) (*AgentListResult, error) {
	f.lastParent = parentSessionID
	f.lastList = args
	if len(f.agents) > 0 {
		agents := append([]AgentStatusResult(nil), f.agents...)
		return &AgentListResult{
			Count:  len(agents),
			Agents: agents,
		}, nil
	}
	return &AgentListResult{
		Count: 1,
		Agents: []AgentStatusResult{{
			ID:              "child-1",
			SessionID:       "child-1",
			ParentSessionID: parentSessionID,
			Path:            "/root/child-1",
			Depth:           1,
			Status:          "idle",
			Exists:          true,
		}},
	}, nil
}

func (f *fakeAgentSessionController) SendMessage(ctx context.Context, fromSessionID string, args AgentMessageArgs) (*AgentMessageResult, error) {
	f.lastParent = fromSessionID
	f.lastMsg = args
	status := &AgentStatusResult{ID: args.SessionID, SessionID: args.SessionID, Status: "idle", Exists: true}
	return &AgentMessageResult{TargetSessionID: args.SessionID, Delivered: true, Status: status}, nil
}

func (f *fakeAgentSessionController) FollowupTask(ctx context.Context, fromSessionID string, args AgentMessageArgs) (*AgentMessageResult, error) {
	f.lastParent = fromSessionID
	f.lastFollow = args
	status := &AgentStatusResult{ID: args.SessionID, SessionID: args.SessionID, Status: "running", Exists: true, Queued: true}
	return &AgentMessageResult{TargetSessionID: args.SessionID, Delivered: true, Triggered: true, Status: status}, nil
}

func (f *fakeAgentSessionController) SendInput(ctx context.Context, args SendAgentInputArgs) (*AgentStatusResult, error) {
	f.lastInput = args
	return &AgentStatusResult{
		ID:                "child-1",
		SessionID:         "child-1",
		Status:            "running",
		Exists:            true,
		Queued:            true,
		CurrentTurnID:     "turn-dynamic-234",
		PendingToolCallID: "toolcall-dynamic-567",
	}, nil
}

func (f *fakeAgentSessionController) Wait(ctx context.Context, args WaitAgentArgs) (*AgentWaitResult, error) {
	f.lastWait = args
	agent := AgentStatusResult{ID: "child-1", SessionID: "child-1", Status: "idle", Exists: true, CurrentTurnID: "turn-dynamic-345", PendingToolCallID: "toolcall-dynamic-678"}
	return &AgentWaitResult{
		Agent:            &agent,
		Agents:           []AgentStatusResult{agent},
		MatchedID:        "child-1",
		MatchedSessionID: "child-1",
		ReadyCount:       1,
	}, nil
}

func (f *fakeAgentSessionController) ReadEvents(ctx context.Context, args ReadAgentEventsArgs) (*AgentEventsResult, error) {
	f.lastRead = args
	return &AgentEventsResult{
		SessionID: "child-1",
		Events: []AgentEventItem{{
			Seq:       7,
			Type:      "assistant_message",
			SessionID: "child-1",
			Timestamp: time.Now().UTC(),
			Payload:   map[string]interface{}{"content": "done"},
		}},
		Count:     1,
		LatestSeq: 7,
	}, nil
}

func (f *fakeAgentSessionController) Close(ctx context.Context, sessionID string) (*AgentStatusResult, error) {
	f.lastClose = sessionID
	return &AgentStatusResult{ID: sessionID, SessionID: sessionID, Status: "stopped", Exists: true, CurrentTurnID: "turn-dynamic-456"}, nil
}

func (f *fakeAgentSessionController) Resume(ctx context.Context, sessionID string) (*AgentStatusResult, error) {
	f.lastResume = sessionID
	return &AgentStatusResult{ID: sessionID, SessionID: sessionID, Status: "idle", Exists: true, CurrentTurnID: "turn-dynamic-567"}, nil
}

func TestBroker_Definitions_ExposeAgentToolsWhenControllerConfigured(t *testing.T) {
	broker := &Broker{AgentSessions: &fakeAgentSessionController{}}
	defs := broker.Definitions()

	seen := map[string]bool{}
	for _, def := range defs {
		seen[def.Name] = true
	}

	for _, name := range []string{ToolSpawnAgent, ToolListAgents, ToolSendMessage, ToolFollowupTask, ToolSendInput, ToolWaitAgent, ToolReadAgentEvents, ToolCloseAgent, ToolResumeAgent} {
		if !seen[name] {
			t.Fatalf("expected %s in broker definitions", name)
		}
	}
}

func TestBuildAgentMailboxMessageUsesAgentControlEnvelope(t *testing.T) {
	message := BuildAgentMailboxMessage("parent-1", "child-1", " inspect docs ", false)
	if message.Kind != AgentMailboxMessageKind || message.Body != "inspect docs" || message.FromAgent != "parent-1" || message.ToAgent != "child-1" {
		t.Fatalf("unexpected agent mailbox message: %#v", message)
	}
	if message.Metadata["message_type"] != AgentMailboxMessageType ||
		message.Metadata["control_action"] != AgentMailboxMessageAction ||
		message.Metadata["workflow"] != AgentMailboxWorkflow ||
		message.Metadata["mailbox_delivery"] != AgentMailboxDeliverySessionStore ||
		message.Metadata["mailbox_kind"] != AgentMailboxMessageKind ||
		message.Metadata["trigger_turn"] != false {
		t.Fatalf("unexpected agent message envelope metadata: %#v", message.Metadata)
	}

	followup := BuildAgentMailboxMessage("parent-1", "child-1", " continue ", true)
	if followup.Kind != AgentMailboxFollowupKind || followup.Body != "continue" {
		t.Fatalf("unexpected followup mailbox message: %#v", followup)
	}
	if followup.Metadata["message_type"] != AgentMailboxFollowupMessageType ||
		followup.Metadata["control_action"] != AgentMailboxFollowupAction ||
		followup.Metadata["mailbox_kind"] != AgentMailboxFollowupKind ||
		followup.Metadata["trigger_turn"] != true {
		t.Fatalf("unexpected followup envelope metadata: %#v", followup.Metadata)
	}
}

func TestBuildSubagentCompletionMailboxMessageUsesAgentControlEnvelope(t *testing.T) {
	message := BuildSubagentCompletionMailboxMessage(" parent-1 ", " child-1 ", " /root/child-1 ", " worker ", "session.end", map[string]interface{}{
		"status":  "idle",
		"success": true,
		"seq":     int64(9),
	})
	if message.Kind != SubagentCompletionMailboxKind ||
		message.FromAgent != "child-1" ||
		message.ToAgent != "parent" ||
		message.Body != "Subagent child-1 completed with status idle." ||
		message.CreatedAt.IsZero() {
		t.Fatalf("unexpected completion mailbox message: %#v", message)
	}
	if message.Metadata["message_type"] != SubagentCompletionMessageType ||
		message.Metadata["control_action"] != SubagentCompletionAction ||
		message.Metadata["workflow"] != AgentMailboxWorkflow ||
		message.Metadata["mailbox_delivery"] != AgentMailboxDeliverySessionStore ||
		message.Metadata["mailbox_kind"] != SubagentCompletionMailboxKind ||
		message.Metadata["session_id"] != "child-1" ||
		message.Metadata["parent_session_id"] != "parent-1" ||
		message.Metadata["path"] != "/root/child-1" ||
		message.Metadata["agent_type"] != "worker" ||
		message.Metadata["source_event_type"] != "session.end" ||
		message.Metadata["event_seq"] != int64(9) ||
		message.Metadata["success"] != true {
		t.Fatalf("unexpected completion envelope metadata: %#v", message.Metadata)
	}
}

func TestBroker_Execute_AgentToolsDelegateToController(t *testing.T) {
	controller := &fakeAgentSessionController{}
	broker := &Broker{AgentSessions: controller}

	result, meta, err := broker.Execute(context.Background(), "parent-session", ToolSpawnAgent, map[string]interface{}{
		"message":      "inspect repo",
		"agent_type":   "explorer",
		"fork_turns":   "1",
		"fork_context": true,
	})
	if err != nil {
		t.Fatalf("spawn_agent failed: %v", err)
	}
	if controller.lastParent != "parent-session" {
		t.Fatalf("unexpected parent session: %s", controller.lastParent)
	}
	if controller.lastSpawn.Message != "inspect repo" || controller.lastSpawn.AgentType != "explorer" || controller.lastSpawn.ForkTurns != "1" {
		t.Fatalf("unexpected spawn args: %#v", controller.lastSpawn)
	}
	if result == nil || meta["status"] != "running" {
		t.Fatalf("unexpected spawn result/meta: %#v %#v", result, meta)
	}

	rawList, meta, err := broker.Execute(context.Background(), "parent-session", ToolListAgents, map[string]interface{}{
		"path_prefix":    "/root",
		"include_closed": true,
	})
	if err != nil {
		t.Fatalf("list_agents failed: %v", err)
	}
	if controller.lastParent != "parent-session" || controller.lastList.PathPrefix != "/root" || !controller.lastList.IncludeClosed {
		t.Fatalf("unexpected list_agents args: parent=%q args=%#v", controller.lastParent, controller.lastList)
	}
	listResult, ok := rawList.(*AgentListResult)
	if !ok || listResult.Count != 1 {
		t.Fatalf("unexpected list_agents result/meta: %#v %#v", rawList, meta)
	}

	rawMsg, meta, err := broker.Execute(context.Background(), "parent-session", ToolSendMessage, map[string]interface{}{
		"target":  "child-1",
		"message": "note only",
	})
	if err != nil {
		t.Fatalf("send_message failed: %v", err)
	}
	if controller.lastParent != "parent-session" || controller.lastMsg.SessionID != "child-1" || controller.lastMsg.Message != "note only" {
		t.Fatalf("unexpected send_message args: parent=%q args=%#v", controller.lastParent, controller.lastMsg)
	}
	msgResult, ok := rawMsg.(*AgentMessageResult)
	if !ok || !msgResult.Delivered || msgResult.Triggered || meta["delivered"] != true {
		t.Fatalf("unexpected send_message result/meta: %#v %#v", rawMsg, meta)
	}

	rawFollow, meta, err := broker.Execute(context.Background(), "parent-session", ToolFollowupTask, map[string]interface{}{
		"target":  "child-1",
		"message": "new task",
	})
	if err != nil {
		t.Fatalf("followup_task failed: %v", err)
	}
	if controller.lastParent != "parent-session" || controller.lastFollow.SessionID != "child-1" || controller.lastFollow.Message != "new task" {
		t.Fatalf("unexpected followup_task args: parent=%q args=%#v", controller.lastParent, controller.lastFollow)
	}
	followResult, ok := rawFollow.(*AgentMessageResult)
	if !ok || !followResult.Delivered || !followResult.Triggered || meta["triggered"] != true {
		t.Fatalf("unexpected followup_task result/meta: %#v %#v", rawFollow, meta)
	}

	_, _, err = broker.Execute(context.Background(), "parent-session", ToolSendInput, map[string]interface{}{
		"id":        "child-1",
		"message":   "continue",
		"interrupt": true,
	})
	if err != nil {
		t.Fatalf("send_input failed: %v", err)
	}
	if controller.lastInput.ID != "child-1" || controller.lastInput.Message != "continue" || controller.lastInput.Interrupt == nil || !*controller.lastInput.Interrupt {
		t.Fatalf("unexpected send_input args: %#v", controller.lastInput)
	}

	_, _, err = broker.Execute(context.Background(), "parent-session", ToolWaitAgent, map[string]interface{}{
		"id":         "child-1",
		"timeout_ms": float64((5 * time.Second).Milliseconds()),
	})
	if err != nil {
		t.Fatalf("wait_agent failed: %v", err)
	}
	if controller.lastWait.ID != "child-1" || controller.lastWait.TimeoutMs != 5000 {
		t.Fatalf("unexpected wait args: %#v", controller.lastWait)
	}

	_, _, err = broker.Execute(context.Background(), "parent-session", ToolCloseAgent, map[string]interface{}{"id": "child-1"})
	if err != nil {
		t.Fatalf("close_agent failed: %v", err)
	}
	if controller.lastClose != "child-1" {
		t.Fatalf("unexpected close id: %s", controller.lastClose)
	}

	_, _, err = broker.Execute(context.Background(), "parent-session", ToolCloseAgent, map[string]interface{}{"id": "/root/child-1"})
	if err != nil {
		t.Fatalf("close_agent path failed: %v", err)
	}
	if controller.lastClose != "/root/child-1" {
		t.Fatalf("unexpected close path: %s", controller.lastClose)
	}

	_, _, err = broker.Execute(context.Background(), "parent-session", ToolResumeAgent, map[string]interface{}{"id": "child-1"})
	if err != nil {
		t.Fatalf("resume_agent failed: %v", err)
	}
	if controller.lastResume != "child-1" {
		t.Fatalf("unexpected resume id: %s", controller.lastResume)
	}
}

func TestBroker_Execute_WaitAgentAcceptsBatchIDs(t *testing.T) {
	controller := &fakeAgentSessionController{}
	broker := &Broker{AgentSessions: controller}

	rawResult, meta, err := broker.Execute(context.Background(), "parent-session", ToolWaitAgent, map[string]interface{}{
		"ids":         []interface{}{"child-1", "child-2"},
		"session_ids": []interface{}{"child-2", "child-3"},
		"timeout_ms":  1000,
	})
	if err != nil {
		t.Fatalf("wait_agent failed: %v", err)
	}
	if len(controller.lastWait.IDs) != 2 || controller.lastWait.IDs[0] != "child-1" || controller.lastWait.IDs[1] != "child-2" {
		t.Fatalf("unexpected batch ids: %#v", controller.lastWait)
	}
	if len(controller.lastWait.SessionIDs) != 2 || controller.lastWait.SessionIDs[0] != "child-2" || controller.lastWait.SessionIDs[1] != "child-3" {
		t.Fatalf("unexpected batch session_ids: %#v", controller.lastWait)
	}
	result, ok := rawResult.(*AgentWaitResult)
	if !ok || result == nil {
		t.Fatalf("expected AgentWaitResult, got %#v", rawResult)
	}
	if result.MatchedSessionID != "child-1" || result.ReadyCount != 1 {
		t.Fatalf("unexpected wait result: %#v", result)
	}
	if meta["status"] != "idle" {
		t.Fatalf("unexpected wait meta: %#v", meta)
	}
}

func TestBroker_Execute_WaitAgentWithoutTargetWaitsParentMailbox(t *testing.T) {
	controller := &fakeAgentSessionController{}
	broker := &Broker{AgentSessions: controller}

	rawResult, meta, err := broker.Execute(context.Background(), "parent-session", ToolWaitAgent, map[string]interface{}{
		"timeout_ms": 1000,
		"after_seq":  12,
	})
	if err != nil {
		t.Fatalf("wait_agent failed: %v", err)
	}
	if controller.lastWait.SessionID != "parent-session" || !controller.lastWait.MailboxOnly || controller.lastWait.AfterSeq != 12 {
		t.Fatalf("unexpected mailbox wait args: %#v", controller.lastWait)
	}
	result, ok := rawResult.(*AgentWaitResult)
	if !ok || result == nil {
		t.Fatalf("expected AgentWaitResult, got %#v", rawResult)
	}
	if result.ReadyCount != 1 || meta["ready_count"] != 1 {
		t.Fatalf("unexpected wait result/meta: %#v %#v", result, meta)
	}
}

func TestBroker_Execute_FollowupTaskRejectsCurrentSession(t *testing.T) {
	controller := &fakeAgentSessionController{}
	broker := &Broker{AgentSessions: controller}

	_, _, err := broker.Execute(context.Background(), "parent-session", ToolFollowupTask, map[string]interface{}{
		"target":  "parent-session",
		"message": "do this",
	})
	if err == nil || !strings.Contains(err.Error(), "current/root session") {
		t.Fatalf("expected current/root rejection, got %v", err)
	}
	if controller.lastFollow.Message != "" {
		t.Fatalf("expected controller not to receive followup_task, got %#v", controller.lastFollow)
	}
}

func TestBroker_Execute_ReadAgentEventsDelegatesToController(t *testing.T) {
	controller := &fakeAgentSessionController{}
	broker := &Broker{AgentSessions: controller}

	rawResult, meta, err := broker.Execute(context.Background(), "parent-session", ToolReadAgentEvents, map[string]interface{}{
		"id":        "child-1",
		"after_seq": float64(5),
		"limit":     float64(10),
		"wait_ms":   float64(250),
	})
	if err != nil {
		t.Fatalf("read_agent_events failed: %v", err)
	}
	if controller.lastRead.ID != "child-1" || controller.lastRead.AfterSeq != 5 || controller.lastRead.Limit != 10 || controller.lastRead.WaitMs != 250 {
		t.Fatalf("unexpected read_agent_events args: %#v", controller.lastRead)
	}
	result, ok := rawResult.(*AgentEventsResult)
	if !ok || result == nil || result.Count != 1 || result.LatestSeq != 7 {
		t.Fatalf("unexpected read_agent_events result: %#v", rawResult)
	}
	if meta["latest_seq"] != int64(7) && meta["latest_seq"] != 7 {
		t.Fatalf("unexpected read_agent_events meta: %#v", meta)
	}
}

func TestBroker_Execute_ReadAgentEventsWithoutTargetReadsParentMailbox(t *testing.T) {
	controller := &fakeAgentSessionController{}
	broker := &Broker{AgentSessions: controller}

	_, _, err := broker.Execute(context.Background(), "parent-session", ToolReadAgentEvents, map[string]interface{}{
		"after_seq": float64(5),
		"limit":     float64(10),
		"wait_ms":   float64(250),
	})
	if err != nil {
		t.Fatalf("read_agent_events failed: %v", err)
	}
	if controller.lastRead.SessionID != "parent-session" || !controller.lastRead.MailboxOnly || controller.lastRead.AfterSeq != 5 || controller.lastRead.Limit != 10 || controller.lastRead.WaitMs != 250 {
		t.Fatalf("unexpected parent mailbox read args: %#v", controller.lastRead)
	}
}

func TestBroker_Execute_AgentToolsRejectTeamTeammateIDs(t *testing.T) {
	store := newTeamStore(t)
	ctx := context.Background()
	teamID, err := store.CreateTeam(ctx, team.Team{Status: team.TeamStatusActive})
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	if _, err := store.UpsertTeammate(ctx, team.Teammate{
		ID:        "member-1",
		TeamID:    teamID,
		SessionID: "team-session-member-1",
		State:     team.TeammateStateIdle,
	}); err != nil {
		t.Fatalf("UpsertTeammate: %v", err)
	}

	controller := &fakeAgentSessionController{}
	broker := &Broker{AgentSessions: controller, TeamStore: store}

	_, _, err = broker.Execute(ctx, "parent-session", ToolWaitAgent, map[string]interface{}{
		"id": "member-1",
	})
	if err == nil || !strings.Contains(err.Error(), "spawn_team teammate id") {
		t.Fatalf("expected wait_agent teammate id error, got %v", err)
	}
	if controller.lastWait.ID != "" {
		t.Fatalf("expected wait_agent not to reach controller, got %#v", controller.lastWait)
	}

	_, _, err = broker.Execute(ctx, "parent-session", ToolReadAgentEvents, map[string]interface{}{
		"id": "member-1",
	})
	if err == nil || !strings.Contains(err.Error(), "spawn_team teammate id") {
		t.Fatalf("expected read_agent_events teammate id error, got %v", err)
	}
	if controller.lastRead.ID != "" {
		t.Fatalf("expected read_agent_events not to reach controller, got %#v", controller.lastRead)
	}
}

func TestBroker_Execute_AgentToolsPreferCurrentSpawnAgentWhenIDCollidesWithTeammate(t *testing.T) {
	store := newTeamStore(t)
	ctx := context.Background()
	teamID, err := store.CreateTeam(ctx, team.Team{Status: team.TeamStatusActive})
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	if _, err := store.UpsertTeammate(ctx, team.Teammate{
		ID:        "agent-a",
		TeamID:    teamID,
		SessionID: "historical-team-session-agent-a",
		State:     team.TeammateStateIdle,
	}); err != nil {
		t.Fatalf("UpsertTeammate: %v", err)
	}

	controller := &fakeAgentSessionController{
		agents: []AgentStatusResult{{
			ID:              "agent-a",
			SessionID:       "agent-a",
			ParentSessionID: "parent-session",
			Path:            "/root/agent-a",
			AgentType:       "child",
			Status:          "idle",
			Exists:          true,
		}},
	}
	broker := &Broker{AgentSessions: controller, TeamStore: store}

	_, _, err = broker.Execute(ctx, "parent-session", ToolWaitAgent, map[string]interface{}{
		"id": "agent-a",
	})
	if err != nil {
		t.Fatalf("wait_agent should prefer current spawn_agent child over stale teammate id: %v", err)
	}
	if controller.lastWait.ID != "agent-a" {
		t.Fatalf("expected wait_agent to reach controller with child id, got %#v", controller.lastWait)
	}

	_, _, err = broker.Execute(ctx, "parent-session", ToolReadAgentEvents, map[string]interface{}{
		"id": "/root/agent-a",
	})
	if err != nil {
		t.Fatalf("read_agent_events should prefer current spawn_agent path over stale teammate id: %v", err)
	}
	if controller.lastRead.ID != "/root/agent-a" {
		t.Fatalf("expected read_agent_events to reach controller with child path, got %#v", controller.lastRead)
	}
}
