package chat

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
)

// Unlike wait-only tests, this starts a real child execution from a live parent
// run context, then lets that parent return while the child's provider is busy.
func TestSessionActorChildSurvivesParentRunCompletion(t *testing.T) {
	for _, parked := range []bool{false, true} {
		name := "completed"
		if parked {
			name = "parked"
		}
		t.Run(name, func(t *testing.T) {
			parent := newResumeEpisodeHarness(t)
			child, entered, childContexts := newChildLifetimeTestActor(t)
			if parked {
				parent.parkOnPrepare = func(turnID string) {
					require.NoError(t, parent.batches.ParkTurnSuspension(context.Background(), &subagentbatch.TurnSuspension{
						TurnID:        turnID,
						SessionID:     parent.session.ID,
						RootScopeID:   parent.session.ID,
						ObligationIDs: []string{"batch-child-lifetime"},
						ParkedAt:      time.Now().UTC(),
					}))
				}
			}
			prepare := parent.actor.prepareRun
			parent.actor.prepareRun = func(ctx context.Context, session *Session, resume bool) error {
				if err := prepare(ctx, session, resume); err != nil {
					return err
				}
				// Delegation must use the child-lifetime submission path; the
				// plain async submit intentionally keeps the parent's cancel
				// chain and is covered by the parent-cancel tests.
				if err := child.SubmitChildPromptAsync(ctx, "keep working", nil); err != nil {
					return err
				}
				select {
				case <-entered:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(3 * time.Second):
					return context.DeadlineExceeded
				}
			}

			result, err := parent.actor.SubmitPrompt(context.Background(), "delegate", nil)
			require.NoError(t, err)
			require.NotNil(t, result)
			childCtx := <-childContexts
			require.NoError(t, childCtx.Err(), "parent cleanup must not cancel a delegated execution")
			if parked {
				require.NotEmpty(t, parent.actor.StateForInspection().SuspendedTurnID)
			}
			require.Never(t, func() bool { return childCtx.Err() != nil },
				50*time.Millisecond, 5*time.Millisecond)

			// Detaching clean completion must not turn a child into an
			// uncancellable background job.
			require.NoError(t, child.Interrupt(context.Background()))
			require.Eventually(t, func() bool { return childCtx.Err() != nil },
				2*time.Second, 5*time.Millisecond)
		})
	}
}

func newChildLifetimeTestActor(t *testing.T) (*SessionActor, <-chan struct{}, <-chan context.Context) {
	t.Helper()
	ctx := context.Background()
	storage := NewInMemoryStorage()
	manager := NewSessionManager(storage, nil)
	t.Cleanup(manager.Stop)
	session, err := manager.CreateSession(ctx, "child-lifetime")
	require.NoError(t, err)
	provider := &cancelBlockingLLMProvider{
		name:    "child-lifetime-provider",
		entered: make(chan struct{}, 1),
	}
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: "test-model", MaxRetries: 1})
	require.NoError(t, runtime.RegisterProvider(provider.Name(), provider))
	apiAgent := agent.NewAgentWithLLM(&agent.Config{
		Name:     "child-lifetime-agent",
		Provider: provider.Name(),
		Model:    "test-model",
		MaxSteps: 3,
	}, nil, runtime)
	store := NewInMemoryRuntimeStore(64)
	contexts := make(chan context.Context, 1)
	actor, err := NewSessionActor(session.ID, SessionActorConfig{
		Agent:        apiAgent,
		LLMRuntime:   runtime,
		SessionStore: storage,
		StateStore:   store,
		EventStore:   store,
		PrepareRun: func(ctx context.Context, _ *Session, _ bool) error {
			contexts <- ctx
			return nil
		},
	})
	require.NoError(t, err)
	t.Cleanup(actor.Stop)
	return actor, provider.entered, contexts
}
