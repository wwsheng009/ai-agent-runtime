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

func TestSessionAgentController_ReadEventsUsesRuntimeEventWakeup(t *testing.T) {
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
	handler.getRuntimeEventBus().Publish(event)

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
		t.Fatal("read_agent_events did not wake from runtime event")
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
	require.Len(t, events, 1)
	event := events[0]
	assert.Equal(t, "subagent.completed", event.Type)
	assert.Equal(t, rootSession.ID, event.SessionID)
	assert.Equal(t, "api-completion-child", event.Payload["session_id"])
	assert.Equal(t, "/root/api-completion-child", event.Payload["path"])
	assert.Equal(t, "worker", event.Payload["agent_type"])
	assert.Equal(t, string(chat.SessionIdle), event.Payload["status"])
	assert.Equal(t, true, event.Payload["success"])
	assert.Equal(t, 5, event.Payload["steps"])
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
