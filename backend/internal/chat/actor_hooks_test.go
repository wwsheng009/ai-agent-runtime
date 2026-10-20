package chat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	runtimehooks "github.com/wwsheng009/ai-agent-runtime/internal/hooks"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
)

func TestSessionActorUserPromptSubmitHookBlocksBeforeProvider(t *testing.T) {
	hookServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"decision":"block","reason":"blocked by policy"}`))
	}))
	defer hookServer.Close()

	ctx := context.Background()
	storage := NewInMemoryStorage()
	manager := NewSessionManager(storage, nil)
	session, err := manager.CreateSession(ctx, "hook-block-user")
	require.NoError(t, err)

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: "gpt-4", MaxRetries: 0})
	provider := NewMockLLMProviderForChat()
	require.NoError(t, runtime.RegisterProvider(provider.Name(), provider))
	require.NoError(t, runtime.RegisterProviderAlias("gpt-4", provider.Name()))

	apiAgent := agent.NewAgentWithLLM(&agent.Config{
		Name:         "hook-block-agent",
		Model:        "gpt-4",
		MaxSteps:     2,
		SystemPrompt: "You are a helpful assistant.",
	}, nil, runtime)
	apiAgent.SetHookManager(runtimehooks.NewManager([]runtimehooks.HookConfig{{
		ID:    "prompt-block",
		Event: runtimehooks.EventUserPromptSubmit,
		Exec:  runtimehooks.ExecConfig{Type: "http", URL: hookServer.URL, Method: http.MethodPost},
	}}))

	runtimeStore := NewInMemoryRuntimeStore(64)
	actor, err := NewSessionActor(session.ID, SessionActorConfig{
		Agent:        apiAgent,
		LLMRuntime:   runtime,
		SessionStore: storage,
		StateStore:   runtimeStore,
		EventStore:   runtimeStore,
	})
	require.NoError(t, err)
	actor.Start()
	defer actor.Stop()

	_, err = actor.SubmitPrompt(ctx, "please do the thing", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "blocked by policy")
	require.Equal(t, 0, provider.index)
}

func TestSessionActorSessionStartHookRunsOncePerSession(t *testing.T) {
	var startHits int32
	hookServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if payload["hook_event_name"] == "SessionStart" {
			atomic.AddInt32(&startHits, 1)
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer hookServer.Close()

	ctx := context.Background()
	storage := NewInMemoryStorage()
	manager := NewSessionManager(storage, nil)
	session, err := manager.CreateSession(ctx, "hook-start-once-user")
	require.NoError(t, err)

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: "gpt-4", MaxRetries: 0})
	provider := NewMockLLMProviderForChat()
	require.NoError(t, runtime.RegisterProvider(provider.Name(), provider))
	require.NoError(t, runtime.RegisterProviderAlias("gpt-4", provider.Name()))

	apiAgent := agent.NewAgentWithLLM(&agent.Config{
		Name:         "hook-start-agent",
		Model:        "gpt-4",
		MaxSteps:     2,
		SystemPrompt: "You are a helpful assistant.",
	}, nil, runtime)
	apiAgent.SetHookManager(runtimehooks.NewManager([]runtimehooks.HookConfig{{
		ID:    "start",
		Event: runtimehooks.EventSessionStart,
		Exec:  runtimehooks.ExecConfig{Type: "http", URL: hookServer.URL, Method: http.MethodPost},
	}}))

	runtimeStore := NewInMemoryRuntimeStore(64)
	actor, err := NewSessionActor(session.ID, SessionActorConfig{
		Agent:        apiAgent,
		LLMRuntime:   runtime,
		SessionStore: storage,
		StateStore:   runtimeStore,
		EventStore:   runtimeStore,
	})
	require.NoError(t, err)
	actor.Start()
	defer actor.Stop()

	_, err = actor.SubmitPrompt(ctx, "first", nil)
	require.NoError(t, err)
	_, err = actor.SubmitPrompt(ctx, "second", nil)
	require.NoError(t, err)

	require.Equal(t, int32(1), atomic.LoadInt32(&startHits))
}
