package chat

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
)

type capturingTurnDigestSink struct {
	mu      sync.Mutex
	digests []TurnDigest
}

func (s *capturingTurnDigestSink) RecordTurnDigest(_ context.Context, digest TurnDigest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.digests = append(s.digests, digest)
	return nil
}

func TestSessionActorRecordsOneTurnDigestAtTerminalBoundary(t *testing.T) {
	ctx := context.Background()
	storage := NewInMemoryStorage()
	manager := NewSessionManager(storage, nil)
	session, err := manager.CreateSession(ctx, "turn-digest-user")
	require.NoError(t, err)

	provider := &capturingSequenceProvider{
		name: "turn-digest-provider",
		responses: []*llm.LLMResponse{{
			Content: "final digest answer",
			Model:   "turn-digest-model",
		}},
	}
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultProvider: provider.Name(),
		DefaultModel:    "turn-digest-model",
		MaxRetries:      1,
	})
	require.NoError(t, runtime.RegisterProvider(provider.Name(), provider))
	apiAgent := agent.NewAgentWithLLM(&agent.Config{
		Name: "turn-digest-agent", Provider: provider.Name(), Model: "turn-digest-model",
		MaxSteps: 2, SystemPrompt: "You are a helpful assistant.",
	}, nil, runtime)
	runtimeStore := NewInMemoryRuntimeStore(64)
	sink := &capturingTurnDigestSink{}
	actor, err := NewSessionActor(session.ID, SessionActorConfig{
		Agent:          apiAgent,
		LLMRuntime:     runtime,
		SessionStore:   storage,
		StateStore:     runtimeStore,
		EventStore:     runtimeStore,
		TurnDigestSink: sink,
	})
	require.NoError(t, err)
	t.Cleanup(actor.Stop)

	result, err := actor.SubmitPrompt(ctx, "what should be remembered?", nil)
	require.NoError(t, err)
	require.NotNil(t, result)

	sink.mu.Lock()
	defer sink.mu.Unlock()
	require.Len(t, sink.digests, 1, "one turn must produce one digest, not one per LLM step")
	digest := sink.digests[0]
	require.Equal(t, session.ID, digest.SessionID)
	require.Equal(t, result.TurnID, digest.TurnID)
	require.Equal(t, "what should be remembered?", digest.Prompt)
	require.Equal(t, "final digest answer", digest.AssistantOutput)
	require.Equal(t, "success", digest.Outcome)
	require.NotEmpty(t, digest.UserMessageID)
	require.NotEmpty(t, digest.AssistantMessageID)
}
