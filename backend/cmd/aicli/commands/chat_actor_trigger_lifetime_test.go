package commands

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	runtimellm "github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// delegationLifetimeProvider reproduces the incident shape at the registry
// layer: the caller turn starts a delegated turn on another session and then
// finishes cleanly while the delegated run is still inside the provider.
// If the delegated run inherits the caller's run context, the caller's clean
// completion cancels it before the provider is released.
type delegationLifetimeProvider struct {
	entered    chan context.Context
	release    chan struct{}
	targetDone chan error

	once sync.Once
	// trigger runs on the caller's run context, exactly like a tool call would.
	trigger    func(context.Context) error
	triggerErr error
}

func (p *delegationLifetimeProvider) Name() string { return "test-provider" }

func (p *delegationLifetimeProvider) Call(ctx context.Context, req *runtimellm.LLMRequest) (*runtimellm.LLMResponse, error) {
	if requestHasUserText(req, "TARGET-DELIVERABLE") {
		select {
		case p.entered <- ctx:
		default:
		}
		select {
		case <-p.release:
			p.targetDone <- nil
			return &runtimellm.LLMResponse{Content: "deliverable complete", Model: req.Model}, nil
		case <-ctx.Done():
			p.targetDone <- ctx.Err()
			return nil, ctx.Err()
		}
	}
	var callErr error
	p.once.Do(func() {
		if p.trigger != nil {
			p.triggerErr = p.trigger(ctx)
		}
		callErr = p.triggerErr
	})
	if callErr != nil {
		return nil, callErr
	}
	return &runtimellm.LLMResponse{Content: "delegated", Model: req.Model}, nil
}

func (p *delegationLifetimeProvider) Stream(ctx context.Context, req *runtimellm.LLMRequest) (<-chan runtimellm.StreamChunk, error) {
	ch := make(chan runtimellm.StreamChunk, 2)
	go func() {
		defer close(ch)
		resp, err := p.Call(ctx, req)
		if err != nil {
			ch <- runtimellm.StreamChunk{Type: runtimellm.EventTypeError, Error: err.Error()}
			return
		}
		ch <- runtimellm.StreamChunk{Type: runtimellm.EventTypeText, Content: resp.Content}
		ch <- runtimellm.StreamChunk{Type: runtimellm.EventTypeDone, Done: true}
	}()
	return ch, nil
}

func (p *delegationLifetimeProvider) CountTokens(text string) int { return len(text) }

func (p *delegationLifetimeProvider) GetCapabilities() *runtimellm.ModelCapabilities {
	return &runtimellm.ModelCapabilities{
		MaxContextTokens:  128000,
		MaxOutputTokens:   4096,
		SupportsTools:     true,
		SupportsStreaming: false,
	}
}

func (p *delegationLifetimeProvider) CheckHealth(ctx context.Context) error { return nil }

func requestHasUserText(req *runtimellm.LLMRequest, marker string) bool {
	if req == nil {
		return false
	}
	for _, message := range req.Messages {
		if message.Role == "user" && strings.Contains(strings.ToUpper(message.Content), marker) {
			return true
		}
	}
	return false
}

// TestLocalActorRegistry_TriggeredTurnSurvivesCallerCleanCompletion pins the A3
// follow-up: FollowupTask/send_message(trigger) start a turn on another session;
// that turn must survive the caller's clean completion (explicit cancellation
// still propagates, which the existing parent-cancel tests cover).
func TestLocalActorRegistry_TriggeredTurnSurvivesCallerCleanCompletion(t *testing.T) {
	ctx := context.Background()
	manager, userID, _, err := newChatSessionManager(t.TempDir())
	require.NoError(t, err)
	defer manager.Stop()
	callerSession, err := manager.Create(ctx, userID)
	require.NoError(t, err)

	provider := &delegationLifetimeProvider{
		entered:    make(chan context.Context, 1),
		release:    make(chan struct{}),
		targetDone: make(chan error, 1),
	}
	llmRuntime := runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{DefaultModel: "test-model"})
	require.NoError(t, llmRuntime.RegisterProvider(provider.Name(), provider))
	require.NoError(t, llmRuntime.RegisterProviderAlias("test-model", provider.Name()))

	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: t.TempDir() + "/team.db"})
	require.NoError(t, err)
	defer teamStore.Close()

	host := newLocalOrchestrationTestHost(t, manager, userID, llmRuntime, teamStore)
	host.BaseSession = &ChatSession{RuntimeSession: callerSession, SessionUserID: userID}
	registry := host.ActorRegistry
	if _, err := registry.Spawn(ctx, callerSession.ID, toolbroker.SpawnAgentArgs{ID: "delegated-target"}); err != nil {
		t.Fatalf("spawn delegated target: %v", err)
	}
	t.Cleanup(func() { host.SessionHub.Stop("delegated-target") })

	provider.trigger = func(runCtx context.Context) error {
		_, triggerErr := registry.FollowupTask(runCtx, callerSession.ID, toolbroker.AgentMessageArgs{
			Target:  "delegated-target",
			Message: "TARGET-DELIVERABLE",
		})
		return triggerErr
	}

	callerDone := make(chan error, 1)
	go func() {
		_, submitErr := registry.SubmitPrompt(ctx, callerSession.ID, "delegate to the target session", nil)
		callerDone <- submitErr
	}()

	var targetCtx context.Context
	select {
	case targetCtx = <-provider.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("the triggered target run never reached the provider")
	}
	select {
	case submitErr := <-callerDone:
		require.NoError(t, submitErr)
		require.NoError(t, provider.triggerErr)
	case <-time.After(3 * time.Second):
		t.Fatal("the caller turn did not finish")
	}

	// The caller turn has now completed cleanly. The delegated run must not be
	// canceled by that completion.
	require.NoError(t, targetCtx.Err(), "caller clean completion must not cancel the triggered run")
	select {
	case runErr := <-provider.targetDone:
		t.Fatalf("triggered run ended with the caller turn: %v", runErr)
	case <-time.After(80 * time.Millisecond):
	}

	close(provider.release)
	select {
	case runErr := <-provider.targetDone:
		require.NoError(t, runErr, "the triggered run must complete normally")
	case <-time.After(3 * time.Second):
		t.Fatal("the triggered run did not finish after being released")
	}
}
