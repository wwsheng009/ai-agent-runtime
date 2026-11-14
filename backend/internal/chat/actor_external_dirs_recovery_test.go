package chat

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestSessionActorExternalDirApprovalPersistsAdmissionSet(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store, session := NewInMemoryStorage(), NewSession("user")
	require.NoError(t, store.Save(ctx, session))
	states := NewInMemoryRuntimeStore(8)
	a := agent.NewAgent(&agent.Config{Name: "test"}, nil)
	actor, err := NewSessionActor(session.ID, SessionActorConfig{Agent: a, SessionStore: store, StateStore: states})
	require.NoError(t, err)
	defer actor.Stop()
	outside := t.TempDir()
	done := make(chan error, 1)
	go func() {
		_, err := actor.RequestApproval(ctx, runtimepolicy.ApprovalRequest{
			ID: "admit", SessionID: session.ID, ToolCallID: "read-1", ToolName: "view",
			ArgsJSON: json.RawMessage(`{"file_path":"A.java"}`), ExternalDirs: []string{outside},
		})
		done <- err
	}()
	require.Eventually(t, func() bool { return actor.State().PendingApproval != nil }, time.Second, time.Millisecond)
	state := actor.State()
	require.Equal(t, []string{outside}, state.PendingApproval.ExternalDirs)
	state.PendingApproval.ExternalDirs[0] = "must-not-alias"
	require.Equal(t, []string{outside}, actor.State().PendingApproval.ExternalDirs)
	persisted, err := states.LoadState(ctx, session.ID)
	require.NoError(t, err)
	raw, err := json.Marshal(persisted)
	require.NoError(t, err)
	var restored RuntimeState
	require.NoError(t, json.Unmarshal(raw, &restored))
	require.Equal(t, []string{outside}, restored.PendingApproval.ExternalDirs)
	cancel()
	require.Error(t, <-done)
}

func TestSessionActorExternalDirRecoveredApprovalRemembersBeforeRemainingBatch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	workspace, outside := t.TempDir(), t.TempDir()
	store, session := NewInMemoryStorage(), NewSession("user")
	args := map[string]interface{}{"command": "git status --short", "workdir": outside}
	argsJSON, err := json.Marshal(args)
	require.NoError(t, err)
	session.AddMessage(*types.NewUserMessage("inspect"))
	assistant := types.NewAssistantMessage("")
	assistant.ToolCalls = []types.ToolCall{
		{ID: "read-1", Name: "run_shell_command", Args: args},
		{ID: "read-2", Name: "run_shell_command", Args: args},
	}
	session.AddMessage(*assistant)
	require.NoError(t, store.Save(ctx, session))
	states := NewInMemoryRuntimeStore(32)
	require.NoError(t, states.SaveState(ctx, &RuntimeState{
		SessionID: session.ID, Status: SessionWaitingApproval, CurrentTurnID: "recovered",
		CurrentRunMeta: &team.RunMeta{PermissionMode: "plan"},
		PendingTool:    &PendingToolInvocation{ToolCallID: "read-1", ToolName: "run_shell_command", ArgsJSON: argsJSON},
		PendingApproval: &ApprovalRequest{ID: "admit", SessionID: session.ID, ToolCallID: "read-1",
			ToolName: "run_shell_command", ArgsJSON: argsJSON, Reason: "external_dir:admit", ExternalDirs: []string{outside}},
	}))
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: "recovery-test", MaxRetries: 0})
	provider := &runMetaSequenceProvider{name: "recovery-test", responses: []*llm.LLMResponse{{Model: "recovery-test", Content: "done"}}}
	require.NoError(t, runtime.RegisterProvider(provider.Name(), provider))
	tools := &shellLikeCapturingMCPManager{}
	a := agent.NewAgentWithLLM(&agent.Config{Name: "child", Model: provider.Name(), MaxSteps: 2,
		Options: map[string]interface{}{"tool_base_path": workspace}}, tools, runtime)
	policy := runtimepolicy.NewToolExecutionPolicy(nil, true)
	policy.SetPathAnchorRoot(workspace)
	a.SetToolExecutionPolicy(policy)
	approvals := &externalDirApprovalRecorder{allow: true}
	a.SetPermissionEngine(&runtimepolicy.Engine{Policy: policy, Mode: runtimepolicy.ModePlan, AskHandler: approvals,
		ExternalDirTempRoots: []string{filepath.Join(workspace, "not-temp")}})
	actor, err := NewSessionActor(session.ID, SessionActorConfig{Agent: a, LLMRuntime: runtime,
		SessionStore: store, StateStore: states, EventStore: states})
	require.NoError(t, err)
	defer actor.Stop()
	// This new actor has no approval waiter. The first call is replayed once,
	// and the second must use the recovered session grant without a new ask.
	require.NoError(t, actor.ApproveTool(ctx, "admit", true))
	require.Eventually(t, func() bool {
		state := actor.State()
		return state.Status == SessionIdle && state.PendingApproval == nil && state.PendingTool == nil
	}, 5*time.Second, 10*time.Millisecond)
	actor.Stop()
	require.Equal(t, 2, tools.callCount)
	require.Zero(t, approvals.calls)
	stored, err := store.Load(ctx, session.ID)
	require.NoError(t, err)
	require.Equal(t, []string{outside}, sessionAllowedRoots(stored))
}

func TestSessionActorExternalDirRecoveredApprovalOneShotOrRejected(t *testing.T) {
	for _, kind := range []string{"denied", "expired", "terminal", "patched", "legacy", "started", "has_result"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			workspace, outside := t.TempDir(), t.TempDir()
			store, session := NewInMemoryStorage(), NewSession("user")
			args := map[string]interface{}{"command": "git status --short", "workdir": outside}
			argsJSON, err := json.Marshal(args)
			require.NoError(t, err)
			session.AddMessage(*types.NewUserMessage("inspect"))
			assistant := types.NewAssistantMessage("")
			assistant.ToolCalls = []types.ToolCall{{ID: "call", Name: "run_shell_command", Args: args}}
			session.AddMessage(*assistant)
			state := &RuntimeState{
				SessionID: session.ID, Status: SessionWaitingApproval, CurrentTurnID: "recovered",
				PendingTool: &PendingToolInvocation{ToolCallID: "call", ToolName: "run_shell_command", ArgsJSON: argsJSON},
				PendingApproval: &ApprovalRequest{ID: "admit", SessionID: session.ID, ToolCallID: "call",
					ToolName: "run_shell_command", ArgsJSON: argsJSON, ExternalDirs: []string{outside}},
			}
			var patched json.RawMessage
			wantCalls := 0
			switch kind {
			case "expired":
				state.PendingApproval.ExpiresAt = time.Now().Add(-time.Minute)
			case "terminal":
				state.LastRunTerminalReason = runCancelSourceRunTimeout
			case "patched":
				patched, err = json.Marshal(map[string]interface{}{"command": "git status --short", "workdir": t.TempDir()})
				require.NoError(t, err)
				wantCalls = 1
			case "legacy":
				state.PendingApproval.ExternalDirs = nil
				wantCalls = 1
			case "started":
				state.PendingTool.ExecutionState = PendingToolExecutionStarted
			case "has_result":
				session.AddMessage(*types.NewToolMessage("call", "already executed"))
			}
			require.NoError(t, store.Save(ctx, session))
			states := NewInMemoryRuntimeStore(32)
			require.NoError(t, states.SaveState(ctx, state))
			runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: "recovery-test", MaxRetries: 0})
			provider := &runMetaSequenceProvider{name: "recovery-test", responses: []*llm.LLMResponse{{Model: "recovery-test", Content: "done"}}}
			require.NoError(t, runtime.RegisterProvider(provider.Name(), provider))
			tools := &shellLikeCapturingMCPManager{}
			a := agent.NewAgentWithLLM(&agent.Config{Name: "child", Model: provider.Name(), MaxSteps: 2,
				Options: map[string]interface{}{"tool_base_path": workspace}}, tools, runtime)
			policy := runtimepolicy.NewToolExecutionPolicy(nil, true)
			policy.SetPathAnchorRoot(workspace)
			a.SetToolExecutionPolicy(policy)
			a.SetPermissionEngine(&runtimepolicy.Engine{Policy: policy, Mode: runtimepolicy.ModePlan,
				ExternalDirTempRoots: []string{filepath.Join(workspace, "not-temp")}})
			actor, err := NewSessionActor(session.ID, SessionActorConfig{Agent: a, LLMRuntime: runtime,
				SessionStore: store, StateStore: states, EventStore: states})
			require.NoError(t, err)
			defer actor.Stop()
			err = actor.ApproveToolWithArgs(ctx, "admit", kind != "denied", patched)
			if kind == "expired" {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Eventually(t, func() bool {
				state := actor.State()
				return state.Status != SessionRunning && state.PendingApproval == nil && state.PendingTool == nil
			}, 5*time.Second, 10*time.Millisecond)
			actor.Stop()
			require.Equal(t, wantCalls, tools.callCount)
			stored, err := store.Load(ctx, session.ID)
			require.NoError(t, err)
			require.Empty(t, sessionAllowedRoots(stored), "one-shot and rejected decisions must not remember roots")
		})
	}
}

func TestSessionActorExternalDirRecoveredApprovalNeverInfersGrants(t *testing.T) {
	for _, kind := range []string{"legacy", "other_session", "other_call", "other_tool", "patched", "persist_failed"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			store, session := NewInMemoryStorage(), NewSession("user")
			require.NoError(t, store.Save(ctx, session))
			actor := &SessionActor{id: session.ID, sessionStore: store}
			req := &ApprovalRequest{SessionID: session.ID, ToolCallID: "call", ToolName: "view", ExternalDirs: []string{t.TempDir()}}
			pending := &PendingToolInvocation{ToolCallID: "call", ToolName: "view"}
			switch kind {
			case "legacy":
				req.ExternalDirs = nil
			case "other_session":
				req.SessionID = "other"
			case "other_call":
				req.ToolCallID = "other"
			case "other_tool":
				req.ToolName = "shell"
			case "persist_failed":
				actor.sessionStore = externalDirFailingStorage{store}
			}
			actor.rememberResumedExternalDirectoryAdmission(ctx, session, req, pending, kind == "patched")
			require.Empty(t, sessionAllowedRoots(session))
			actor.sessionStore = store
			require.NoError(t, actor.persistSession(ctx, session))
			stored, err := store.Load(ctx, session.ID)
			require.NoError(t, err)
			require.Empty(t, sessionAllowedRoots(stored))
		})
	}
}
