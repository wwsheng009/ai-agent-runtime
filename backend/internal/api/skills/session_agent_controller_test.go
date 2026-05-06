package skills

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

func TestSessionAgentController_PathTargetsAndCloseSubtree(t *testing.T) {
	ctx := context.Background()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	defer sessionManager.Stop()
	defer handler.getSessionHub().StopAll()
	handler.SetSessionManager(sessionManager)

	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.Agents.MaxDepth = 2
	handler.SetRuntimeConfig(cfg, "")

	rootSession, err := sessionManager.Create(ctx, "user-session-agent-controller-path")
	require.NoError(t, err)

	controller := handler.getAgentSessionController()
	require.NotNil(t, controller)

	_, err = controller.Spawn(ctx, rootSession.ID, toolbroker.SpawnAgentArgs{ID: "api-path-parent"})
	require.NoError(t, err)
	_, err = controller.Spawn(ctx, "api-path-parent", toolbroker.SpawnAgentArgs{ID: "api-path-child"})
	require.NoError(t, err)
	_, err = controller.Spawn(ctx, rootSession.ID, toolbroker.SpawnAgentArgs{ID: "api-path-sibling"})
	require.NoError(t, err)

	messageResult, err := controller.SendMessage(ctx, rootSession.ID, toolbroker.AgentMessageArgs{
		Target:  "/root/api-path-parent/api-path-child",
		Message: "hello by path",
	})
	require.NoError(t, err)
	assert.Equal(t, "api-path-child", messageResult.TargetSessionID)

	waitResult, err := controller.Wait(ctx, toolbroker.WaitAgentArgs{
		ID:        "/root/api-path-parent/api-path-child",
		TimeoutMs: 100,
	})
	require.NoError(t, err)
	assert.Equal(t, "api-path-child", waitResult.MatchedSessionID)

	result, err := controller.Close(ctx, "/root/api-path-parent")
	require.NoError(t, err)
	assert.Equal(t, 2, result.ClosedCount)
	assert.Equal(t, []string{"api-path-parent", "api-path-child"}, result.ClosedSessionIDs)

	parent, err := sessionManager.Get(ctx, "api-path-parent")
	require.NoError(t, err)
	child, err := sessionManager.Get(ctx, "api-path-child")
	require.NoError(t, err)
	sibling, err := sessionManager.Get(ctx, "api-path-sibling")
	require.NoError(t, err)
	assert.Equal(t, chat.StateClosed, parent.State)
	assert.Equal(t, chat.StateClosed, child.State)
	assert.NotEqual(t, chat.StateClosed, sibling.State)

	listResult, err := controller.List(ctx, rootSession.ID, toolbroker.ListAgentsArgs{IncludeClosed: true})
	require.NoError(t, err)
	assert.Equal(t, 3, listResult.Count)
}

func TestSessionAgentController_SendMessagePersistsMailboxWithoutTargetActor(t *testing.T) {
	ctx := context.Background()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	defer sessionManager.Stop()
	defer handler.getSessionHub().StopAll()
	handler.SetSessionManager(sessionManager)
	handler.SetRuntimeConfig(runtimecfg.DefaultRuntimeConfig(), "")

	rootSession, err := sessionManager.Create(ctx, "user-session-agent-controller-mailbox")
	require.NoError(t, err)
	controller := handler.getAgentSessionController()
	require.NotNil(t, controller)

	_, err = controller.Spawn(ctx, rootSession.ID, toolbroker.SpawnAgentArgs{ID: "api-mailbox-child"})
	require.NoError(t, err)
	handler.getSessionHub().Stop("api-mailbox-child")

	result, err := controller.SendMessage(ctx, rootSession.ID, toolbroker.AgentMessageArgs{
		Target:  "/root/api-mailbox-child",
		Message: "durable api hello",
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "api-mailbox-child", result.TargetSessionID)
	assert.True(t, result.Delivered)
	assert.False(t, result.Triggered)
	if _, ok := handler.getSessionHub().Get("api-mailbox-child"); ok {
		t.Fatal("send_message should persist mailbox event without starting target actor")
	}

	events, err := handler.getSessionEventStore().ListEvents(ctx, "api-mailbox-child", 0, 10)
	require.NoError(t, err)
	require.Len(t, events, 1)
	event := events[0]
	assert.Equal(t, chat.EventMailboxReceived, event.Type)
	assert.Equal(t, "agent_message", event.Payload["kind"])
	assert.Equal(t, "durable api hello", event.Payload["body"])
	metadata, ok := event.Payload["metadata"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "api-mailbox-child", metadata["target_session_id"])
	assert.Equal(t, false, metadata["trigger_turn"])
	assert.Equal(t, toolbroker.AgentMailboxMessageType, metadata["message_type"])
	assert.Equal(t, toolbroker.AgentMailboxMessageAction, metadata["control_action"])
	assert.Equal(t, toolbroker.AgentMailboxWorkflow, metadata["workflow"])
	assert.Equal(t, toolbroker.AgentMailboxMessageKind, metadata["mailbox_kind"])
}

func TestSessionAgentController_FollowupTaskPersistsMailboxWhenTargetBusy(t *testing.T) {
	ctx := context.Background()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	defer sessionManager.Stop()
	defer handler.getSessionHub().StopAll()
	handler.SetSessionManager(sessionManager)
	handler.SetRuntimeConfig(runtimecfg.DefaultRuntimeConfig(), "")

	rootSession, err := sessionManager.Create(ctx, "user-session-agent-controller-busy-followup")
	require.NoError(t, err)
	controller := handler.getAgentSessionController()
	require.NotNil(t, controller)

	_, err = controller.Spawn(ctx, rootSession.ID, toolbroker.SpawnAgentArgs{ID: "api-busy-followup-child"})
	require.NoError(t, err)
	_, err = handler.getSessionHub().GetOrCreate("api-busy-followup-child")
	require.NoError(t, err)
	require.NoError(t, handler.getSessionRuntimeStore().SaveState(ctx, &chat.RuntimeState{
		SessionID: "api-busy-followup-child",
		Status:    chat.SessionRunning,
		UpdatedAt: time.Now().UTC(),
	}))

	result, err := controller.FollowupTask(ctx, rootSession.ID, toolbroker.AgentMessageArgs{
		Target:  "/root/api-busy-followup-child",
		Message: "queue api while busy",
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "api-busy-followup-child", result.TargetSessionID)
	assert.True(t, result.Delivered)
	assert.False(t, result.Triggered)

	events, err := handler.getSessionEventStore().ListEvents(ctx, "api-busy-followup-child", 0, 10)
	require.NoError(t, err)
	require.Len(t, events, 1)
	event := events[0]
	assert.Equal(t, chat.EventMailboxReceived, event.Type)
	assert.Equal(t, "followup_task", event.Payload["kind"])
	assert.Equal(t, "queue api while busy", event.Payload["body"])
	metadata, ok := event.Payload["metadata"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "api-busy-followup-child", metadata["target_session_id"])
	assert.Equal(t, true, metadata["trigger_turn"])
	assert.Equal(t, toolbroker.AgentMailboxFollowupMessageType, metadata["message_type"])
	assert.Equal(t, toolbroker.AgentMailboxFollowupAction, metadata["control_action"])
	assert.Equal(t, toolbroker.AgentMailboxWorkflow, metadata["workflow"])
	assert.Equal(t, toolbroker.AgentMailboxFollowupKind, metadata["mailbox_kind"])
}

func TestSessionAgentController_WaitUsesRuntimeEventWakeup(t *testing.T) {
	ctx := context.Background()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	defer sessionManager.Stop()
	defer handler.getSessionHub().StopAll()
	handler.SetSessionManager(sessionManager)
	handler.SetRuntimeConfig(runtimecfg.DefaultRuntimeConfig(), "")

	provider := newSessionAgentBlockingProvider("test-session-agent-event-model")
	defer provider.releaseCall()
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultModel: provider.Name(),
		MaxRetries:   0,
	})
	require.NoError(t, runtime.RegisterProvider(provider.Name(), provider))
	handler.SetLLMRuntime(runtime)

	rootSession, err := sessionManager.Create(ctx, "user-session-agent-controller-event")
	require.NoError(t, err)
	controller := handler.getAgentSessionController()
	require.NotNil(t, controller)

	_, err = controller.Spawn(ctx, rootSession.ID, toolbroker.SpawnAgentArgs{ID: "api-event-child"})
	require.NoError(t, err)
	_, err = controller.SendInput(ctx, toolbroker.SendAgentInputArgs{
		ID:      "/root/api-event-child",
		Message: "finish after release",
	})
	require.NoError(t, err)

	select {
	case <-provider.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("child agent did not enter provider call")
	}

	resultCh := make(chan *toolbroker.AgentWaitResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, waitErr := controller.Wait(context.Background(), toolbroker.WaitAgentArgs{
			ID:        "/root/api-event-child",
			TimeoutMs: 2000,
		})
		if waitErr != nil {
			errCh <- waitErr
			return
		}
		resultCh <- result
	}()

	time.Sleep(100 * time.Millisecond)
	provider.releaseCall()

	select {
	case waitErr := <-errCh:
		t.Fatalf("wait failed: %v", waitErr)
	case result := <-resultCh:
		require.NotNil(t, result)
		assert.Equal(t, "api-event-child", result.MatchedSessionID)
		assert.Equal(t, 1, result.ReadyCount)
	case <-time.After(450 * time.Millisecond):
		t.Fatal("wait did not wake from runtime event")
	}
}

func TestSessionAgentController_WaitUsesEventStoreWakeup(t *testing.T) {
	ctx := context.Background()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	defer sessionManager.Stop()
	defer handler.getSessionHub().StopAll()
	handler.SetSessionManager(sessionManager)
	handler.SetRuntimeConfig(runtimecfg.DefaultRuntimeConfig(), "")

	rootSession, err := sessionManager.Create(ctx, "user-session-agent-controller-event-store-wait")
	require.NoError(t, err)
	controller := handler.getAgentSessionController()
	require.NotNil(t, controller)

	_, err = controller.Spawn(ctx, rootSession.ID, toolbroker.SpawnAgentArgs{ID: "api-event-store-wait-child"})
	require.NoError(t, err)
	require.NoError(t, handler.getSessionRuntimeStore().SaveState(ctx, &chat.RuntimeState{
		SessionID: "api-event-store-wait-child",
		Status:    chat.SessionRunning,
		UpdatedAt: time.Now().UTC(),
	}))

	resultCh := make(chan *toolbroker.AgentWaitResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, waitErr := controller.Wait(context.Background(), toolbroker.WaitAgentArgs{
			ID:        "/root/api-event-store-wait-child",
			TimeoutMs: 2000,
		})
		if waitErr != nil {
			errCh <- waitErr
			return
		}
		resultCh <- result
	}()

	time.Sleep(100 * time.Millisecond)
	require.NoError(t, handler.getSessionRuntimeStore().SaveState(ctx, &chat.RuntimeState{
		SessionID: "api-event-store-wait-child",
		Status:    chat.SessionIdle,
		UpdatedAt: time.Now().UTC(),
	}))
	_, err = handler.getSessionEventStore().AppendEvent(ctx, runtimeevents.Event{
		Type:      chat.EventSessionEnd,
		SessionID: "api-event-store-wait-child",
		Payload:   map[string]interface{}{"success": true},
	})
	require.NoError(t, err)

	select {
	case readErr := <-errCh:
		t.Fatalf("wait failed: %v", readErr)
	case result := <-resultCh:
		require.NotNil(t, result)
		assert.Equal(t, "api-event-store-wait-child", result.MatchedSessionID)
		assert.Equal(t, 1, result.ReadyCount)
	case <-time.After(450 * time.Millisecond):
		t.Fatal("wait_agent did not wake from event store append")
	}
}

func TestSessionAgentController_ReadEventsUsesEventStoreWakeup(t *testing.T) {
	ctx := context.Background()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	defer sessionManager.Stop()
	defer handler.getSessionHub().StopAll()
	handler.SetSessionManager(sessionManager)
	handler.SetRuntimeConfig(runtimecfg.DefaultRuntimeConfig(), "")

	rootSession, err := sessionManager.Create(ctx, "user-session-agent-controller-read-event")
	require.NoError(t, err)
	controller := handler.getAgentSessionController()
	require.NotNil(t, controller)

	_, err = controller.Spawn(ctx, rootSession.ID, toolbroker.SpawnAgentArgs{ID: "api-read-event-child"})
	require.NoError(t, err)

	resultCh := make(chan *toolbroker.AgentEventsResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, readErr := controller.ReadEvents(context.Background(), toolbroker.ReadAgentEventsArgs{
			ID:       "/root/api-read-event-child",
			AfterSeq: 0,
			Limit:    20,
			WaitMs:   2000,
		})
		if readErr != nil {
			errCh <- readErr
			return
		}
		resultCh <- result
	}()

	time.Sleep(100 * time.Millisecond)
	store := handler.getSessionEventStore()
	require.NotNil(t, store)
	event := runtimeevents.Event{
		Type:      chat.EventAssistantMessage,
		SessionID: "api-read-event-child",
		Payload:   map[string]interface{}{"content": "event read done"},
	}
	_, err = store.AppendEvent(ctx, event)
	require.NoError(t, err)

	select {
	case readErr := <-errCh:
		t.Fatalf("read events failed: %v", readErr)
	case result := <-resultCh:
		require.NotNil(t, result)
		assert.Equal(t, "api-read-event-child", result.SessionID)
		assert.Equal(t, 1, result.Count)
		if assert.Len(t, result.Events, 1) {
			assert.Equal(t, chat.EventAssistantMessage, result.Events[0].Type)
		}
	case <-time.After(450 * time.Millisecond):
		t.Fatal("read_agent_events did not wake from event store append")
	}
}

func TestSessionAgentController_WaitWithoutTargetUsesParentMailbox(t *testing.T) {
	ctx := context.Background()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	defer sessionManager.Stop()
	defer handler.getSessionHub().StopAll()
	handler.SetSessionManager(sessionManager)
	handler.SetRuntimeConfig(runtimecfg.DefaultRuntimeConfig(), "")

	rootSession, err := sessionManager.Create(ctx, "user-session-agent-controller-parent-mailbox")
	require.NoError(t, err)
	controller := handler.getAgentSessionController()
	require.NotNil(t, controller)

	resultCh := make(chan *toolbroker.AgentWaitResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, waitErr := controller.Wait(context.Background(), toolbroker.WaitAgentArgs{
			SessionID:   rootSession.ID,
			MailboxOnly: true,
			TimeoutMs:   2000,
		})
		if waitErr != nil {
			errCh <- waitErr
			return
		}
		resultCh <- result
	}()

	time.Sleep(100 * time.Millisecond)
	err = controller.deliverAgentMailboxEvent(ctx, rootSession.ID, team.MailMessage{
		FromAgent: "child-1",
		ToAgent:   "parent",
		Kind:      "agent_message",
		Body:      "api parent mailbox hello",
	})
	require.NoError(t, err)

	select {
	case waitErr := <-errCh:
		t.Fatalf("wait failed: %v", waitErr)
	case result := <-resultCh:
		require.NotNil(t, result)
		require.NotNil(t, result.Event)
		assert.Equal(t, chat.EventMailboxReceived, result.Event.Type)
		assert.Equal(t, int64(1), result.LatestSeq)
		assert.Equal(t, int64(1), result.Event.Seq)
		assert.Equal(t, int64(1), result.Event.Payload["mailbox_seq"])
		assert.Equal(t, "api parent mailbox hello", result.Event.Payload["body"])
	case <-time.After(450 * time.Millisecond):
		t.Fatal("wait_agent did not wake from parent mailbox event")
	}
}

func TestSessionAgentController_ReadEventsWithoutTargetUsesParentMailbox(t *testing.T) {
	ctx := context.Background()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	defer sessionManager.Stop()
	defer handler.getSessionHub().StopAll()
	handler.SetSessionManager(sessionManager)
	handler.SetRuntimeConfig(runtimecfg.DefaultRuntimeConfig(), "")

	rootSession, err := sessionManager.Create(ctx, "user-session-agent-controller-parent-events")
	require.NoError(t, err)
	controller := handler.getAgentSessionController()
	require.NotNil(t, controller)

	resultCh := make(chan *toolbroker.AgentEventsResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, readErr := controller.ReadEvents(context.Background(), toolbroker.ReadAgentEventsArgs{
			SessionID:   rootSession.ID,
			MailboxOnly: true,
			AfterSeq:    0,
			Limit:       20,
			WaitMs:      2000,
		})
		if readErr != nil {
			errCh <- readErr
			return
		}
		resultCh <- result
	}()

	time.Sleep(100 * time.Millisecond)
	store := handler.getSessionEventStore()
	require.NotNil(t, store)
	_, err = store.AppendEvent(ctx, runtimeevents.Event{
		Type:      chat.EventAssistantMessage,
		SessionID: rootSession.ID,
		Payload:   map[string]interface{}{"content": "not mailbox"},
	})
	require.NoError(t, err)
	err = controller.deliverAgentMailboxEvent(ctx, rootSession.ID, team.MailMessage{
		FromAgent: "child-1",
		ToAgent:   "parent",
		Kind:      "agent_message",
		Body:      "api parent mailbox event read hello",
	})
	require.NoError(t, err)

	select {
	case readErr := <-errCh:
		t.Fatalf("read failed: %v", readErr)
	case result := <-resultCh:
		require.NotNil(t, result)
		assert.Equal(t, rootSession.ID, result.SessionID)
		assert.Equal(t, 1, result.Count)
		assert.Equal(t, int64(1), result.LatestSeq)
		if assert.Len(t, result.Events, 1) {
			assert.Equal(t, chat.EventMailboxReceived, result.Events[0].Type)
			assert.Equal(t, int64(1), result.Events[0].Seq)
			assert.Equal(t, int64(1), result.Events[0].Payload["mailbox_seq"])
			assert.Equal(t, "api parent mailbox event read hello", result.Events[0].Payload["body"])
		}
	case <-time.After(450 * time.Millisecond):
		t.Fatal("read_agent_events did not wake from parent mailbox event")
	}
}

func TestSessionAgentController_MirrorsChildCompletionToParentEvents(t *testing.T) {
	ctx := context.Background()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	defer sessionManager.Stop()
	defer handler.getSessionHub().StopAll()
	handler.SetSessionManager(sessionManager)
	handler.SetRuntimeConfig(runtimecfg.DefaultRuntimeConfig(), "")

	rootSession, err := sessionManager.Create(ctx, "user-session-agent-controller-completion")
	require.NoError(t, err)
	controller := handler.getAgentSessionController()
	require.NotNil(t, controller)

	_, err = controller.Spawn(ctx, rootSession.ID, toolbroker.SpawnAgentArgs{
		ID:        "api-completion-child",
		AgentType: "worker",
	})
	require.NoError(t, err)

	childEnd := runtimeevents.Event{
		Type:      chat.EventSessionEnd,
		SessionID: "api-completion-child",
		TraceID:   "trace-api-child-complete",
		Payload: map[string]interface{}{
			"success": true,
			"steps":   5,
		},
	}
	handler.getRuntimeEventBus().Publish(childEnd)

	store := handler.getSessionEventStore()
	require.NotNil(t, store)
	events, err := store.ListEvents(ctx, rootSession.ID, 0, 20)
	require.NoError(t, err)
	require.Len(t, events, 2)
	event := events[0]
	assert.Equal(t, "subagent.completed", event.Type)
	assert.Equal(t, rootSession.ID, event.SessionID)
	assert.Equal(t, "api-completion-child", event.Payload["session_id"])
	assert.Equal(t, "/root/api-completion-child", event.Payload["path"])
	assert.Equal(t, "worker", event.Payload["agent_type"])
	assert.Equal(t, string(chat.SessionIdle), event.Payload["status"])
	assert.Equal(t, true, event.Payload["success"])
	assert.Equal(t, 5, event.Payload["steps"])
	mailboxEvent := events[1]
	assert.Equal(t, chat.EventMailboxReceived, mailboxEvent.Type)
	assert.Equal(t, rootSession.ID, mailboxEvent.SessionID)
	assert.Equal(t, "subagent.completed", mailboxEvent.Payload["kind"])
	assert.Equal(t, "api-completion-child", mailboxEvent.Payload["from_agent"])
	assert.Equal(t, "parent", mailboxEvent.Payload["to_agent"])
	metadata, ok := mailboxEvent.Payload["metadata"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "api-completion-child", metadata["session_id"])
	assert.Equal(t, "/root/api-completion-child", metadata["path"])
	assert.Equal(t, "worker", metadata["agent_type"])
}

func TestSessionAgentController_PersistsCompletionMailboxWithoutParentActor(t *testing.T) {
	ctx := context.Background()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetSessionManager(chat.NewSessionManager(chat.NewInMemoryStorage(), nil))
	defer handler.getSessionHub().StopAll()

	controller := &sessionAgentController{handler: handler}
	controller.deliverSubagentCompletionMailbox(ctx, "parent-session", "api-child-session", "/root/api-child-session", "worker", chat.EventSessionEnd, map[string]interface{}{
		"status":  string(chat.SessionIdle),
		"success": true,
		"seq":     int64(11),
	})

	store := handler.getSessionEventStore()
	require.NotNil(t, store)
	events, err := store.ListEvents(ctx, "parent-session", 0, 10)
	require.NoError(t, err)
	require.Len(t, events, 1)
	event := events[0]
	assert.Equal(t, chat.EventMailboxReceived, event.Type)
	assert.Equal(t, "subagent.completed", event.Payload["kind"])
	assert.Equal(t, "api-child-session", event.Payload["from_agent"])
	metadata, ok := event.Payload["metadata"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "api-child-session", metadata["session_id"])
	assert.Equal(t, int64(11), metadata["event_seq"])
}

type sessionAgentBlockingProvider struct {
	name        string
	entered     chan struct{}
	release     chan struct{}
	enterOnce   sync.Once
	releaseOnce sync.Once
}

func newSessionAgentBlockingProvider(name string) *sessionAgentBlockingProvider {
	return &sessionAgentBlockingProvider{
		name:    name,
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (p *sessionAgentBlockingProvider) Name() string {
	return p.name
}

func (p *sessionAgentBlockingProvider) Call(ctx context.Context, req *llm.LLMRequest) (*llm.LLMResponse, error) {
	p.enterOnce.Do(func() {
		close(p.entered)
	})
	select {
	case <-p.release:
		return &llm.LLMResponse{
			Content: "event done",
			Model:   p.name,
		}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (p *sessionAgentBlockingProvider) Stream(ctx context.Context, req *llm.LLMRequest) (<-chan llm.StreamChunk, error) {
	return nil, nil
}

func (p *sessionAgentBlockingProvider) CountTokens(text string) int {
	return len(text) / 4
}

func (p *sessionAgentBlockingProvider) GetCapabilities() *llm.ModelCapabilities {
	return &llm.ModelCapabilities{
		MaxContextTokens:  128000,
		MaxOutputTokens:   4096,
		SupportsTools:     true,
		SupportsStreaming: true,
		SupportsJSONMode:  true,
	}
}

func (p *sessionAgentBlockingProvider) CheckHealth(ctx context.Context) error {
	return nil
}

func (p *sessionAgentBlockingProvider) releaseCall() {
	p.releaseOnce.Do(func() {
		close(p.release)
	})
}

var _ llm.Provider = (*sessionAgentBlockingProvider)(nil)
