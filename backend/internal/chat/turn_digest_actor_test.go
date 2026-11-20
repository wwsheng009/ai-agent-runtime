package chat

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	sqlite3 "github.com/ncruces/go-sqlite3"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
)

type capturingTurnDigestSink struct {
	mu          sync.Mutex
	digests     []TurnDigest
	started     chan struct{}
	release     chan struct{}
	finished    chan struct{}
	startOnce   sync.Once
	releaseOnce sync.Once
	finishOnce  sync.Once
}

func (s *capturingTurnDigestSink) RecordTurnDigest(_ context.Context, digest TurnDigest) error {
	if s.started != nil {
		s.startOnce.Do(func() { close(s.started) })
	}
	if s.release != nil {
		<-s.release
	}
	s.mu.Lock()
	s.digests = append(s.digests, digest)
	s.mu.Unlock()
	if s.finished != nil {
		s.finishOnce.Do(func() { close(s.finished) })
	}
	return nil
}

type retryingTurnDigestSink struct {
	failures int
	err      error
	calls    int
	digests  []TurnDigest
}

func (s *retryingTurnDigestSink) RecordTurnDigest(_ context.Context, digest TurnDigest) error {
	s.calls++
	if s.calls <= s.failures {
		return s.err
	}
	s.digests = append(s.digests, digest)
	return nil
}

func TestRecordTurnDigestRetriesBusyOnce(t *testing.T) {
	sink := &retryingTurnDigestSink{failures: 1, err: sqlite3.BUSY}
	actor := &SessionActor{id: "s1", turnDigestSink: sink}

	actor.recordTurnDigest(TurnDigest{SessionID: "s1", TurnID: "t1"})

	require.Equal(t, 2, sink.calls)
	require.Len(t, sink.digests, 1)
}

func TestRecordTurnDigestDoesNotRetryNonBusyError(t *testing.T) {
	sink := &retryingTurnDigestSink{failures: 1, err: errors.New("identity mismatch")}
	actor := &SessionActor{id: "s1", turnDigestSink: sink}

	actor.recordTurnDigest(TurnDigest{SessionID: "s1", TurnID: "t1"})

	require.Equal(t, 1, sink.calls)
	require.Empty(t, sink.digests)
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
	sink := &capturingTurnDigestSink{
		started:  make(chan struct{}),
		release:  make(chan struct{}),
		finished: make(chan struct{}),
	}
	defer sink.releaseOnce.Do(func() { close(sink.release) })
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

	type submitResult struct {
		result *agent.Result
		err    error
	}
	submitted := make(chan submitResult, 1)
	go func() {
		result, err := actor.SubmitPrompt(ctx, "what should be remembered?", nil)
		submitted <- submitResult{result: result, err: err}
	}()
	select {
	case <-sink.started:
	case <-time.After(time.Second):
		t.Fatal("turn-end materialization handoff did not start")
	}
	var submit submitResult
	select {
	case submit = <-submitted:
	case <-time.After(time.Second):
		t.Fatal("turn completion waited for cross-store materialization")
	}
	require.NoError(t, submit.err)
	require.NotNil(t, submit.result)
	sink.releaseOnce.Do(func() { close(sink.release) })
	select {
	case <-sink.finished:
	case <-time.After(time.Second):
		t.Fatal("turn-end materialization handoff did not finish")
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	require.Len(t, sink.digests, 1, "one turn must produce one digest, not one per LLM step")
	digest := sink.digests[0]
	require.Equal(t, session.ID, digest.SessionID)
	require.Equal(t, submit.result.TurnID, digest.TurnID)
	require.Equal(t, "what should be remembered?", digest.Prompt)
	require.Equal(t, "final digest answer", digest.AssistantOutput)
	require.Equal(t, "success", digest.Outcome)
	require.NotEmpty(t, digest.UserMessageID)
	require.NotEmpty(t, digest.AssistantMessageID)
}
