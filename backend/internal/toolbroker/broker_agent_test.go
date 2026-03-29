package toolbroker

import (
	"context"
	"testing"
	"time"
)

type fakeAgentSessionController struct {
	lastParent string
	lastSpawn  SpawnAgentArgs
	lastInput  SendAgentInputArgs
	lastWait   WaitAgentArgs
	lastRead   ReadAgentEventsArgs
	lastClose  string
	lastResume string
}

func (f *fakeAgentSessionController) Spawn(ctx context.Context, parentSessionID string, args SpawnAgentArgs) (*AgentStatusResult, error) {
	f.lastParent = parentSessionID
	f.lastSpawn = args
	return &AgentStatusResult{ID: "child-1", SessionID: "child-1", ParentSessionID: parentSessionID, Status: "running", Exists: true, Created: true, Queued: true}, nil
}

func (f *fakeAgentSessionController) SendInput(ctx context.Context, args SendAgentInputArgs) (*AgentStatusResult, error) {
	f.lastInput = args
	return &AgentStatusResult{ID: "child-1", SessionID: "child-1", Status: "running", Exists: true, Queued: true}, nil
}

func (f *fakeAgentSessionController) Wait(ctx context.Context, args WaitAgentArgs) (*AgentWaitResult, error) {
	f.lastWait = args
	agent := AgentStatusResult{ID: "child-1", SessionID: "child-1", Status: "idle", Exists: true}
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
	return &AgentStatusResult{ID: sessionID, SessionID: sessionID, Status: "stopped", Exists: true}, nil
}

func (f *fakeAgentSessionController) Resume(ctx context.Context, sessionID string) (*AgentStatusResult, error) {
	f.lastResume = sessionID
	return &AgentStatusResult{ID: sessionID, SessionID: sessionID, Status: "idle", Exists: true}, nil
}

func TestBroker_Definitions_ExposeAgentToolsWhenControllerConfigured(t *testing.T) {
	broker := &Broker{AgentSessions: &fakeAgentSessionController{}}
	defs := broker.Definitions()

	seen := map[string]bool{}
	for _, def := range defs {
		seen[def.Name] = true
	}

	for _, name := range []string{ToolSpawnAgent, ToolSendInput, ToolWaitAgent, ToolReadAgentEvents, ToolCloseAgent, ToolResumeAgent} {
		if !seen[name] {
			t.Fatalf("expected %s in broker definitions", name)
		}
	}
}

func TestBroker_Execute_AgentToolsDelegateToController(t *testing.T) {
	controller := &fakeAgentSessionController{}
	broker := &Broker{AgentSessions: controller}

	result, meta, err := broker.Execute(context.Background(), "parent-session", ToolSpawnAgent, map[string]interface{}{
		"message":      "inspect repo",
		"agent_type":   "explorer",
		"fork_context": true,
	})
	if err != nil {
		t.Fatalf("spawn_agent failed: %v", err)
	}
	if controller.lastParent != "parent-session" {
		t.Fatalf("unexpected parent session: %s", controller.lastParent)
	}
	if controller.lastSpawn.Message != "inspect repo" || controller.lastSpawn.AgentType != "explorer" {
		t.Fatalf("unexpected spawn args: %#v", controller.lastSpawn)
	}
	if result == nil || meta["status"] != "running" {
		t.Fatalf("unexpected spawn result/meta: %#v %#v", result, meta)
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
