package chat

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	_ "github.com/wwsheng009/ai-agent-runtime/internal/sqlitedriver"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// newTerminalApprovalTestActor builds an actor whose durable state looks like a
// run that already ended in a terminal cancellation class while a tool approval
// was still pending: exactly what a late decision (web client, broker, CLI)
// encounters after the fact. terminalReason is the durable marker under test.
// The approval terminal guard keeps its production default (enabled).
func newTerminalApprovalTestActor(t *testing.T, ctx context.Context, terminalReason string) (*SessionActor, *InMemoryStorage, *InMemoryRuntimeStore, *capturingSequenceProvider, *Session) {
	t.Helper()
	return newTerminalApprovalTestActorWithGuard(t, ctx, terminalReason, nil)
}

// newTerminalApprovalTestActorWithGuard is newTerminalApprovalTestActor with an
// explicit ApprovalTerminalGuard override; nil keeps the production default
// (enabled, see supervision-approval-resume-past-deadline-fix-plan.md §8).
func newTerminalApprovalTestActorWithGuard(t *testing.T, ctx context.Context, terminalReason string, guard *bool) (*SessionActor, *InMemoryStorage, *InMemoryRuntimeStore, *capturingSequenceProvider, *Session) {
	t.Helper()

	storage := NewInMemoryStorage()
	manager := NewSessionManager(storage, nil)
	session, err := manager.CreateSession(ctx, "actor-terminal-approval-user")
	require.NoError(t, err)
	require.NotNil(t, session)

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultModel: "test-terminal-approval-model",
		MaxRetries:   0,
	})
	provider := &capturingSequenceProvider{
		name: "test-terminal-approval-model",
		responses: []*llm.LLMResponse{
			{Content: "This run must not be revived.", Model: "test-terminal-approval-model"},
			{Content: "Neither must this one.", Model: "test-terminal-approval-model"},
		},
	}
	require.NoError(t, runtime.RegisterProvider(provider.Name(), provider))

	apiAgent := agent.NewAgentWithLLM(&agent.Config{
		Name:         "actor-terminal-approval-test",
		Model:        provider.Name(),
		MaxSteps:     2,
		SystemPrompt: "You are a helpful assistant.",
	}, &simpleEchoMCPManager{}, runtime)

	stateStore := NewInMemoryRuntimeStore(64)
	require.NoError(t, stateStore.SaveState(ctx, &RuntimeState{
		SessionID:     session.ID,
		Status:        SessionWaitingApproval,
		CurrentTurnID: "",
		PendingTool: &PendingToolInvocation{
			ToolCallID: "tool_late_approval",
			ToolName:   "team_echo",
			ArgsJSON:   []byte(`{"message":"late"}`),
			CreatedAt:  time.Now().UTC(),
		},
		PendingApproval: &ApprovalRequest{
			ID:         "approval_late",
			SessionID:  session.ID,
			ToolCallID: "tool_late_approval",
			ToolName:   "team_echo",
			ArgsJSON:   []byte(`{"message":"late"}`),
			Reason:     "manual approval",
			ExpiresAt:  time.Now().UTC().Add(time.Hour),
		},
		LastRunTerminalReason: terminalReason,
		UpdatedAt:             time.Now().UTC(),
	}))

	actor, err := NewSessionActor(session.ID, SessionActorConfig{
		Agent:                 apiAgent,
		LLMRuntime:            runtime,
		SessionStore:          storage,
		StateStore:            stateStore,
		EventStore:            stateStore,
		ApprovalTerminalGuard: guard,
	})
	require.NoError(t, err)
	return actor, storage, stateStore, provider, session
}

// lastApprovalResolvedPayload returns the payload of the most recent
// approval_resolved event for the session.
func lastApprovalResolvedPayload(t *testing.T, store EventStore, sessionID string) map[string]interface{} {
	t.Helper()

	events, err := store.ListEvents(context.Background(), sessionID, 0, 0)
	require.NoError(t, err)

	var payload map[string]interface{}
	for i := range events {
		if events[i].Type == EventApprovalResolved {
			payload = events[i].Payload
		}
	}
	return payload
}

// TestSessionActorApproveToolAfterTerminalRunDoesNotResume reproduces the defect
// this suite exists for: a run ended under its execution deadline while a tool
// approval was still pending, and the decision arrives afterwards. The decision
// must be retired durably instead of reviving the dead run (P0-1/P0-4).
func TestSessionActorApproveToolAfterTerminalRunDoesNotResume(t *testing.T) {
	cases := []struct {
		name  string
		allow bool
	}{
		{name: "approved", allow: true},
		{name: "denied", allow: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			actor, storage, stateStore, provider, session := newTerminalApprovalTestActor(t, ctx, runCancelSourceRunTimeout)

			before, err := storage.Load(ctx, session.ID)
			require.NoError(t, err)
			require.NotNil(t, before)
			historyBefore := len(before.GetMessages())

			require.NoError(t, actor.ApproveTool(ctx, "approval_late", tc.allow))

			state := actor.State()
			require.NotNil(t, state)
			assert.Nil(t, state.PendingApproval, "late decision must retire the stale approval")
			assert.Nil(t, state.PendingTool)
			assert.Equal(t, SessionStopped, state.Status, "a terminal run must stay stopped")
			assert.Equal(t, runCancelSourceRunTimeout, state.LastRunTerminalReason)
			assert.Equal(t, "", state.CurrentTurnID)

			resolution, resumed := actor.ApprovalOutcome("approval_late")
			assert.Equal(t, ApprovalResolutionRunTerminalNoResume, resolution)
			assert.False(t, resumed, "the decision must report that nothing was resumed")

			persisted, err := stateStore.LoadState(ctx, session.ID)
			require.NoError(t, err)
			require.NotNil(t, persisted)
			assert.Nil(t, persisted.PendingApproval, "the retirement must be durable, not just in memory")
			assert.Nil(t, persisted.PendingTool)
			assert.Equal(t, runCancelSourceRunTimeout, persisted.LastRunTerminalReason)

			// No resume may start: give a would-be recovery run time to show up.
			time.Sleep(200 * time.Millisecond)
			after, err := storage.Load(ctx, session.ID)
			require.NoError(t, err)
			require.NotNil(t, after)
			assert.Equal(t, historyBefore, len(after.GetMessages()), "no message may be appended by a late decision")
			assert.Equal(t, 0, provider.callCount, "the model must never be called again")
			assert.NotEqual(t, SessionRunning, actor.State().Status)

			payload := lastApprovalResolvedPayload(t, stateStore, session.ID)
			require.NotNil(t, payload, "hosts still need an approval_resolved event")
			assert.Equal(t, ApprovalResolutionRunTerminalNoResume, payload["resolution"])
			assert.Equal(t, false, payload["resumed"])
			assert.Equal(t, runCancelSourceRunTimeout, payload["run_terminal_reason"])
		})
	}
}

// TestSQLiteRuntimeStorePersistsLastRunTerminalReason proves the durable marker
// survives a restart on the SQLite store: both the decision-point guard (P0-1)
// and the host preflight (P0-3) read it after a reload, so it must be a real
// column, not in-memory-only state.
func TestSQLiteRuntimeStorePersistsLastRunTerminalReason(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteRuntimeStore(&RuntimeStoreConfig{
		Path: filepath.Join(t.TempDir(), "runtime.sqlite"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	require.NoError(t, store.SaveState(ctx, &RuntimeState{
		SessionID:             "session-terminal-marker",
		Status:                SessionStopped,
		LastRunTerminalReason: runCancelSourceRunTimeout,
		UpdatedAt:             time.Now().UTC(),
	}))

	var columnType string
	require.NoError(t, store.db.QueryRowContext(ctx,
		`SELECT type FROM pragma_table_info('session_runtime_state') WHERE name = 'last_run_terminal_reason'`,
	).Scan(&columnType))
	assert.Equal(t, "TEXT", columnType)

	loaded, err := store.LoadState(ctx, "session-terminal-marker")
	require.NoError(t, err)
	require.NotNil(t, loaded)
	assert.Equal(t, runCancelSourceRunTimeout, loaded.LastRunTerminalReason)

	// Clearing it must round-trip too: a new run invalidates the marker.
	loaded.LastRunTerminalReason = ""
	loaded.Status = SessionIdle
	require.NoError(t, store.SaveState(ctx, loaded))

	reloaded, err := store.LoadState(ctx, "session-terminal-marker")
	require.NoError(t, err)
	require.NotNil(t, reloaded)
	assert.Equal(t, "", reloaded.LastRunTerminalReason)
}

// TestSessionActorTerminalRunCancelRetiresPendingApproval covers the live half
// of the defect (P0-4): the run ends in a terminal cancellation class while a
// tool approval is still pending. The approval must be terminated with the run
// instead of being detached across it, and a late decision must never revive
// work.
func TestSessionActorTerminalRunCancelRetiresPendingApproval(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	storage := NewInMemoryStorage()
	manager := NewSessionManager(storage, nil)
	session, err := manager.CreateSession(ctx, "actor-terminal-run-cancel-user")
	require.NoError(t, err)
	require.NotNil(t, session)

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{
		DefaultModel: "test-terminal-run-cancel-model",
		MaxRetries:   0,
	})
	provider := &capturingSequenceProvider{
		name: "test-terminal-run-cancel-model",
		responses: []*llm.LLMResponse{
			{
				Content: "I need approval.",
				Model:   "test-terminal-run-cancel-model",
				ToolCalls: []types.ToolCall{
					{
						ID:   "tool_terminal_cancel",
						Name: "team_echo",
						Args: map[string]interface{}{"message": "hello"},
					},
				},
			},
			{Content: "This must never be produced.", Model: "test-terminal-run-cancel-model"},
		},
	}
	require.NoError(t, runtime.RegisterProvider(provider.Name(), provider))

	apiAgent := agent.NewAgentWithLLM(&agent.Config{
		Name:         "actor-terminal-run-cancel-test",
		Model:        provider.Name(),
		MaxSteps:     3,
		SystemPrompt: "You are a helpful assistant.",
	}, &simpleEchoMCPManager{}, runtime)
	apiAgent.SetPermissionEngine(&agent.PermissionEngine{
		Callback: func(ctx context.Context, req runtimepolicy.EvalRequest) (runtimepolicy.Decision, string, error) {
			if req.ToolName == "team_echo" {
				return runtimepolicy.Decision{Type: runtimepolicy.DecisionAsk}, "manual approval", nil
			}
			return runtimepolicy.Decision{Type: runtimepolicy.DecisionAllow}, "", nil
		},
	})

	stateStore := NewInMemoryRuntimeStore(64)
	actor, err := NewSessionActor(session.ID, SessionActorConfig{
		Agent:        apiAgent,
		LLMRuntime:   runtime,
		SessionStore: storage,
		StateStore:   stateStore,
		EventStore:   stateStore,
	})
	require.NoError(t, err)

	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()

	submitDone := make(chan error, 1)
	go func() {
		_, submitErr := actor.SubmitPrompt(runCtx, "Start the approval flow.", nil)
		submitDone <- submitErr
	}()

	var requestID string
	require.Eventually(t, func() bool {
		state := actor.State()
		if state == nil || state.Status != SessionWaitingApproval ||
			state.PendingApproval == nil || state.PendingTool == nil {
			return false
		}
		requestID = state.PendingApproval.ID
		return requestID != ""
	}, 5*time.Second, 20*time.Millisecond)

	// The run ends under an external cancel / execution deadline class.
	runCancel()
	select {
	case <-submitDone:
	case <-time.After(10 * time.Second):
		t.Fatal("run did not exit after its context was canceled")
	}

	require.Eventually(t, func() bool {
		state := actor.State()
		return state != nil && state.PendingApproval == nil && state.PendingTool == nil
	}, 5*time.Second, 20*time.Millisecond)

	state := actor.State()
	require.NotNil(t, state)
	assert.Equal(t, SessionStopped, state.Status, "a canceled run must not fall back to waiting_approval")
	assert.True(t, sessionRunTerminalCancelSource(state.LastRunTerminalReason),
		"the terminal class must be recorded, got %q", state.LastRunTerminalReason)

	persisted, err := stateStore.LoadState(ctx, session.ID)
	require.NoError(t, err)
	require.NotNil(t, persisted)
	assert.Nil(t, persisted.PendingApproval, "the retirement must be durable")
	assert.Equal(t, state.LastRunTerminalReason, persisted.LastRunTerminalReason)

	payload := lastApprovalResolvedPayload(t, stateStore, session.ID)
	require.NotNil(t, payload)
	assert.Equal(t, ApprovalResolutionRunTerminated, payload["resolution"])
	assert.Equal(t, false, payload["resumed"])

	// A late decision must not revive the dead run.
	callsBefore := provider.callCount
	require.NoError(t, actor.ApproveTool(ctx, requestID, true))
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, callsBefore, provider.callCount, "the model must not be called again")
	assert.Equal(t, SessionStopped, actor.State().Status)
	assert.Equal(t, ApprovalResolutionRunTerminated, func() string {
		resolution, _ := actor.ApprovalOutcome(requestID)
		return resolution
	}())
}

// TestSessionActorLateApprovalAfterRestartReportsNotApplied covers the host
// contract (P0-3) when the decision reaches a *fresh* actor instance, i.e. after
// a server restart: only durable state is available, and it must still make the
// retirement visible so hosts report the decision as resolved but not applied.
func TestSessionActorLateApprovalAfterRestartReportsNotApplied(t *testing.T) {
	ctx := context.Background()
	actor, _, stateStore, provider, session := newTerminalApprovalTestActor(t, ctx, runCancelSourceRunTimeout)

	// Drop the pending approval the way the terminal finalization does, then
	// rebuild the actor: the new instance knows nothing beyond the store.
	require.NoError(t, actor.updateState(ctx, func(state *RuntimeState) error {
		state.PendingApproval = nil
		state.PendingTool = nil
		state.Status = SessionStopped
		state.UpdatedAt = time.Now().UTC()
		return nil
	}))

	restarted, err := NewSessionActor(session.ID, SessionActorConfig{
		Agent:        actor.agent,
		LLMRuntime:   actor.llmRuntime,
		SessionStore: actor.sessionStore,
		StateStore:   stateStore,
		EventStore:   stateStore,
	})
	require.NoError(t, err)

	persisted, err := stateStore.LoadState(ctx, session.ID)
	require.NoError(t, err)
	require.NotNil(t, persisted)
	require.Equal(t, runCancelSourceRunTimeout, persisted.LastRunTerminalReason)

	require.NoError(t, restarted.ApproveTool(ctx, "approval_late", true))

	resolution, resumed := restarted.ApprovalOutcome("approval_late")
	assert.Equal(t, ApprovalResolutionRunTerminalNoResume, resolution,
		"a restarted host must still report the decision as unapplied")
	assert.False(t, resumed)

	time.Sleep(150 * time.Millisecond)
	assert.Equal(t, 0, provider.callCount, "a restarted host must not revive the run either")
	assert.Equal(t, SessionStopped, restarted.State().Status)
}

// TestPendingApprovalRunTerminalGuardRequiresDurableMarkerAndNoWaiter pins the
// guard's two conditions: the verdict must come from the durable terminal
// marker (never from "no active run"), and a live approval waiter vetoes it so
// the ordinary resume path keeps working (P0-1).
func TestPendingApprovalRunTerminalGuardRequiresDurableMarkerAndNoWaiter(t *testing.T) {
	ctx := context.Background()
	actor, _, _, _, _ := newTerminalApprovalTestActor(t, ctx, runCancelSourceDeadline)

	reason, terminal := actor.pendingApprovalRunTerminal(&RuntimeState{LastRunTerminalReason: runCancelSourceDeadline}, "approval_late")
	assert.True(t, terminal)
	assert.Equal(t, runCancelSourceDeadline, reason)

	_, terminal = actor.pendingApprovalRunTerminal(&RuntimeState{}, "approval_late")
	assert.False(t, terminal, "a missing marker must not be guessed from run ownership")

	_, terminal = actor.pendingApprovalRunTerminal(nil, "approval_late")
	assert.False(t, terminal)

	// A registered waiter proves a run is still blocked on this very decision.
	actor.registerApprovalWaiter("approval_live")
	defer actor.unregisterApprovalWaiter("approval_live")
	_, terminal = actor.pendingApprovalRunTerminal(&RuntimeState{LastRunTerminalReason: runCancelSourceRunTimeout}, "approval_live")
	assert.False(t, terminal, "a live waiter must keep the normal resume path intact")
}

// TestResumePendingBatchAfterCurrentResultRefusesTerminalRun covers the second
// line of defense: even if a caller reaches the resume entry directly, a run
// that already ended in a terminal cancellation class must not restart (P0-2).
func TestResumePendingBatchAfterCurrentResultRefusesTerminalRun(t *testing.T) {
	ctx := context.Background()
	actor, storage, _, provider, session := newTerminalApprovalTestActor(t, ctx, runCancelSourceDeadline)

	loaded, err := storage.Load(ctx, session.ID)
	require.NoError(t, err)
	require.NotNil(t, loaded)

	state := actor.State()
	require.NotNil(t, state)
	require.NotNil(t, state.PendingTool)
	pending := clonePendingToolInvocation(state.PendingTool, false)

	err = actor.resumePendingBatchAfterCurrentResult(ctx, state.Clone(), pending, loaded, false)
	require.ErrorIs(t, err, errSessionRunTerminal)
	assert.Contains(t, err.Error(), runCancelSourceDeadline)

	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, 0, provider.callCount)

	after := actor.State()
	require.NotNil(t, after)
	assert.Equal(t, SessionWaitingApproval, after.Status, "the refusal must leave the state untouched")
	assert.Equal(t, runCancelSourceDeadline, after.LastRunTerminalReason)
	assert.NotNil(t, after.PendingApproval)
}

// TestSessionRunCancelSourceClassifiesTerminalClasses documents which cancel
// sources are treated as terminal for the approval lifecycle (P0-4): a clean
// completion or an explicit user interrupt must not be classified as one.
func TestSessionRunCancelSourceClassifiesTerminalClasses(t *testing.T) {
	for _, source := range []string{
		runCancelSourceRunTimeout,
		runCancelSourceDeadline,
		runCancelSourceExecutionContext,
		runCancelSourceParentDeadline,
		runCancelSourceParentContext,
	} {
		assert.True(t, sessionRunTerminalCancelSource(source), "source %q must be terminal", source)
	}
	for _, source := range []string{"", runCancelSourceUserInterrupt} {
		assert.False(t, sessionRunTerminalCancelSource(source), "source %q must not be terminal", source)
	}
}

// TestSessionActorApprovalTerminalGuardDisabledRestoresLegacyResume pins the
// kill-switch semantics of §8: with supervision.approval_terminal_guard=false
// the decision point, the resume entry and the terminal finalizer must all fall
// back to the pre-fix behavior — a late decision on a deadline-terminated run
// still revives the run. A switch that silently kept the guard active would be
// worse than no switch at all, so this test asserts the legacy outcome directly.
func TestSessionActorApprovalTerminalGuardDisabledRestoresLegacyResume(t *testing.T) {
	ctx := context.Background()
	guardOff := false
	actor, storage, _, provider, session := newTerminalApprovalTestActorWithGuard(
		t, ctx, runCancelSourceDeadline, &guardOff)

	callsBefore := provider.callCount
	require.NoError(t, actor.ApproveTool(ctx, "approval_late", true))

	// 旧行为：决议放行 → 恢复 pending 工具批次 → 模型被再次调用并追加助手消息。
	require.Eventually(t, func() bool {
		updated, loadErr := storage.Load(ctx, session.ID)
		if loadErr != nil || updated == nil {
			return false
		}
		for _, message := range updated.GetMessages() {
			if message.Role == "assistant" && strings.Contains(message.Content, "revived") {
				return true
			}
		}
		return false
	}, 5*time.Second, 20*time.Millisecond)

	assert.Greater(t, provider.callCount, callsBefore,
		"guard disabled: the late decision must take the legacy resume path and call the model again")
	resolution, resumed := actor.ApprovalOutcome("approval_late")
	assert.Equal(t, ApprovalResolutionAllowed, resolution,
		"guard disabled: the legacy path reports allowed, never run_terminal_no_resume")
	assert.True(t, resumed, "guard disabled: the legacy path resumes the run")
}
