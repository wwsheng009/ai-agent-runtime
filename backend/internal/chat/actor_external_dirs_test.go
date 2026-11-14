package chat

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type externalDirApprovalRecorder struct {
	allow bool
	calls int
}

func (h *externalDirApprovalRecorder) RequestApproval(_ context.Context, req runtimepolicy.ApprovalRequest) (runtimepolicy.ApprovalResponse, error) {
	h.calls++
	return runtimepolicy.ApprovalResponse{Allowed: h.allow}, nil
}

func newExternalDirTestActor(t *testing.T, store SessionStorage, session *Session, workspace string, mode runtimepolicy.Mode, approvals *externalDirApprovalRecorder) (*SessionActor, *runtimepolicy.Engine) {
	t.Helper()
	apiAgent := agent.NewAgentWithLLM(&agent.Config{
		Name:    "external-dir-child",
		Options: map[string]interface{}{"tool_base_path": workspace},
	}, nil, nil)
	policy := runtimepolicy.NewToolExecutionPolicy(nil, true)
	policy.SetPathAnchorRoot(workspace)
	apiAgent.SetToolExecutionPolicy(policy)
	engine := &runtimepolicy.Engine{
		Mode: mode, AskHandler: approvals, Policy: policy,
		ExternalDirTempRoots: []string{filepath.Join(workspace, "not-the-temp-root")},
	}
	apiAgent.SetPermissionEngine(engine)
	actor, err := NewSessionActor(session.ID, SessionActorConfig{Agent: apiAgent, SessionStore: store})
	require.NoError(t, err)
	actor.prepareExternalDirectoryAdmission(context.Background(), session)
	return actor, engine
}

func externalDirRead(sessionID, path string) runtimepolicy.EvalRequest {
	return runtimepolicy.EvalRequest{
		SessionID: sessionID, ToolName: "view",
		Args: map[string]interface{}{"file_path": path},
	}
}

func TestSessionActorExternalDirAdmissionRemembersAndRestores(t *testing.T) {
	for _, mode := range []runtimepolicy.Mode{runtimepolicy.ModePlan, runtimepolicy.ModeDefault, runtimepolicy.ModeBypassPermissions} {
		t.Run(string(mode), func(t *testing.T) {
			ctx := context.Background()
			workspace, outside := t.TempDir(), t.TempDir()
			ctx = toolctx.WithWorkspaceRoot(ctx, workspace)
			store := NewInMemoryStorage()
			child, parent, sibling := NewSession("user"), NewSession("user"), NewSession("user")
			child.SetContext("agent_parent_session_id", parent.ID)
			for _, session := range []*Session{child, parent, sibling} {
				require.NoError(t, store.Save(ctx, session))
			}
			// Preserve unsaved live history when an approval persists metadata.
			child.AddMessage(*types.NewUserMessage("live unsaved history"))
			handler := &externalDirApprovalRecorder{allow: true}
			actor, engine := newExternalDirTestActor(t, store, child, workspace, mode, handler)
			req := externalDirRead(child.ID, filepath.Join(outside, "a.java"))
			for i := 0; i < 2; i++ {
				decision, err := engine.Evaluate(ctx, req)
				require.NoError(t, err)
				require.Equal(t, runtimepolicy.DecisionAllow, decision.Type)
			}
			wantAsks := 1
			if mode == runtimepolicy.ModeBypassPermissions {
				wantAsks = 0
			}
			require.Equal(t, wantAsks, handler.calls)
			persisted, err := store.Load(ctx, child.ID)
			require.NoError(t, err)
			require.Len(t, sessionAllowedRoots(persisted), 1)
			require.Equal(t, child.GetMessages(), persisted.GetMessages())
			for _, other := range []*Session{parent, sibling} {
				unchanged, err := store.Load(ctx, other.ID)
				require.NoError(t, err)
				require.Empty(t, sessionAllowedRoots(unchanged), "child admission must not widen parent or sibling")
			}
			// A fresh actor (restart/follow-up) must recover its own admissions.
			restoredHandler := &externalDirApprovalRecorder{allow: true}
			_, restored := newExternalDirTestActor(t, store, persisted, workspace, mode, restoredHandler)
			decision, err := restored.Evaluate(ctx, req)
			require.NoError(t, err)
			require.Equal(t, runtimepolicy.DecisionAllow, decision.Type)
			require.Zero(t, restoredHandler.calls)
			// Read-only is a hard boundary even when mode=bypass and the root
			// was admitted: admission never grants write capability.
			decision, err = engine.Evaluate(ctx, runtimepolicy.EvalRequest{
				SessionID: child.ID, ToolName: "write",
				Args: map[string]interface{}{"file_path": filepath.Join(outside, "a.java"), "content": "no"},
			})
			require.NoError(t, err)
			require.Equal(t, runtimepolicy.DecisionDeny, decision.Type)
			require.Equal(t, wantAsks, handler.calls)
			require.NoError(t, actor.persistSession(ctx, child))
		})
	}
}

func TestSessionActorExternalDirAdmissionFollowsSymlink(t *testing.T) {
	workspace, outside := t.TempDir(), t.TempDir()
	link := filepath.Join(workspace, "java")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	target := filepath.Join(outside, "A.java")
	require.NoError(t, os.WriteFile(target, []byte("class A {}"), 0600))
	ctx := toolctx.WithWorkspaceRoot(context.Background(), workspace)
	store, session := NewInMemoryStorage(), NewSession("user")
	require.NoError(t, store.Save(ctx, session))
	handler := &externalDirApprovalRecorder{allow: true}
	_, engine := newExternalDirTestActor(t, store, session, workspace, runtimepolicy.ModePlan, handler)
	for _, path := range []string{filepath.Join(link, "A.java"), target, filepath.Join(link, "A.java")} {
		decision, err := engine.Evaluate(ctx, externalDirRead(session.ID, path))
		require.NoError(t, err)
		require.Equal(t, runtimepolicy.DecisionAllow, decision.Type)
	}
	require.Equal(t, 1, handler.calls, "link and real path must share one admission")
}

func TestSessionActorExternalDirAdmissionDeniedOrDontAskDoesNotRemember(t *testing.T) {
	for _, mode := range []runtimepolicy.Mode{runtimepolicy.ModePlan, runtimepolicy.ModeDontAsk} {
		t.Run(string(mode), func(t *testing.T) {
			workspace, outside := t.TempDir(), t.TempDir()
			ctx := toolctx.WithWorkspaceRoot(context.Background(), workspace)
			store, session := NewInMemoryStorage(), NewSession("user")
			require.NoError(t, store.Save(ctx, session))
			handler := &externalDirApprovalRecorder{allow: false}
			_, engine := newExternalDirTestActor(t, store, session, workspace, mode, handler)
			for i := 0; i < 2; i++ {
				decision, err := engine.Evaluate(ctx, externalDirRead(session.ID, filepath.Join(outside, "A.java")))
				require.NoError(t, err)
				require.Equal(t, runtimepolicy.DecisionDeny, decision.Type)
			}
			wantAsks := 2
			if mode == runtimepolicy.ModeDontAsk {
				wantAsks = 0
			}
			require.Equal(t, wantAsks, handler.calls)
			persisted, err := store.Load(ctx, session.ID)
			require.NoError(t, err)
			require.Empty(t, sessionAllowedRoots(persisted))
			require.Empty(t, engine.ExternalAllowedRootsSource())
		})
	}
}

func TestSessionActorExternalDirAdmissionRevocationSurvivesStaleCheckpoint(t *testing.T) {
	workspace, outside := t.TempDir(), t.TempDir()
	ctx := toolctx.WithWorkspaceRoot(context.Background(), workspace)
	store, session := NewInMemoryStorage(), NewSession("user")
	session.SetContext(AllowedRootsContextKey, []string{outside})
	require.NoError(t, store.Save(ctx, session))
	handler := &externalDirApprovalRecorder{allow: true}
	actor, engine := newExternalDirTestActor(t, store, session, workspace, runtimepolicy.ModePlan, handler)
	// /add-dir remove writes an explicit empty set while the run has a stale copy.
	revoked, err := store.Load(ctx, session.ID)
	require.NoError(t, err)
	revoked.SetContext(AllowedRootsContextKey, []string{})
	require.NoError(t, store.Update(ctx, revoked))
	require.NoError(t, actor.persistSession(ctx, session))
	persisted, err := store.Load(ctx, session.ID)
	require.NoError(t, err)
	require.Empty(t, sessionAllowedRoots(persisted))
	actor.prepareExternalDirectoryAdmission(ctx, persisted)
	decision, err := engine.Evaluate(ctx, externalDirRead(session.ID, filepath.Join(outside, "A.java")))
	require.NoError(t, err)
	require.Equal(t, runtimepolicy.DecisionAllow, decision.Type)
	require.Equal(t, 1, handler.calls, "revoked roots must be admitted again on the next run")
}

func TestSessionActorExternalDirAdmissionMergesLatestRoots(t *testing.T) {
	ctx := context.Background()
	store, session := NewInMemoryStorage(), NewSession("user")
	require.NoError(t, store.Save(ctx, session))
	latest, err := store.Load(ctx, session.ID)
	require.NoError(t, err)
	preAdmitted, newlyAdmitted := t.TempDir(), t.TempDir()
	latest.SetContext(AllowedRootsContextKey, []string{preAdmitted})
	require.NoError(t, store.Update(ctx, latest))
	actor := &SessionActor{id: session.ID, sessionStore: store}
	require.NoError(t, actor.persistSessionWithExternalDirs(ctx, session, []string{newlyAdmitted, newlyAdmitted}))
	stored, err := store.Load(ctx, session.ID)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{preAdmitted, newlyAdmitted}, sessionAllowedRoots(stored))
}

type externalDirFailingStorage struct{ SessionStorage }

func (s externalDirFailingStorage) Update(context.Context, *Session) error {
	return errors.New("admission persistence failed")
}

func TestSessionActorExternalDirAdmissionPersistenceFailureDoesNotCache(t *testing.T) {
	workspace, outside := t.TempDir(), t.TempDir()
	ctx := toolctx.WithWorkspaceRoot(context.Background(), workspace)
	base, session := NewInMemoryStorage(), NewSession("user")
	require.NoError(t, base.Save(ctx, session))
	handler := &externalDirApprovalRecorder{allow: true}
	actor, engine := newExternalDirTestActor(t, externalDirFailingStorage{base}, session, workspace, runtimepolicy.ModePlan, handler)
	for i := 0; i < 2; i++ {
		decision, err := engine.Evaluate(ctx, externalDirRead(session.ID, filepath.Join(outside, "A.java")))
		require.NoError(t, err)
		require.Equal(t, runtimepolicy.DecisionAllow, decision.Type, "approval permits only this call")
		_, present := session.GetContext(AllowedRootsContextKey)
		require.False(t, present, "a failed admission must not mutate the live session")
	}
	require.Equal(t, 2, handler.calls)
	require.Empty(t, engine.ExternalAllowedRootsSource())
	stored, err := base.Load(ctx, session.ID)
	require.NoError(t, err)
	require.Empty(t, sessionAllowedRoots(stored))
	// Storage recovers before an ordinary checkpoint. It must not turn a
	// previously failed admission into a durable grant.
	actor.sessionStore = base
	require.NoError(t, actor.persistSession(ctx, session))
	stored, err = base.Load(ctx, session.ID)
	require.NoError(t, err)
	require.Empty(t, sessionAllowedRoots(stored))
	actor.prepareExternalDirectoryAdmission(ctx, stored)
	decision, err := engine.Evaluate(ctx, externalDirRead(session.ID, filepath.Join(outside, "A.java")))
	require.NoError(t, err)
	require.Equal(t, runtimepolicy.DecisionAllow, decision.Type)
	require.Equal(t, 3, handler.calls, "failed persistence must require approval after checkpoint/rebind too")
}

func TestSessionActorExternalDirAdmissionFailurePreservesPriorRootValue(t *testing.T) {
	for name, raw := range map[string]interface{}{
		"null": nil, "empty_strings": []string{}, "empty_json": []interface{}{}, "existing": []string{t.TempDir()},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			base, session := NewInMemoryStorage(), NewSession("user")
			session.SetContext(AllowedRootsContextKey, raw)
			require.NoError(t, base.Save(ctx, session))
			actor := &SessionActor{id: session.ID, sessionStore: externalDirFailingStorage{base}}
			require.Error(t, actor.persistSessionWithExternalDirs(ctx, session, []string{t.TempDir()}))
			got, present := session.GetContext(AllowedRootsContextKey)
			require.True(t, present)
			require.Equal(t, raw, got, "rollback must preserve nil/empty/existing authorization exactly")
			actor.sessionStore = base
			require.NoError(t, actor.persistSession(ctx, session))
			stored, err := base.Load(ctx, session.ID)
			require.NoError(t, err)
			require.Equal(t, normalizeSessionAllowedRoots(raw), sessionAllowedRoots(stored))
		})
	}
}

func TestSessionActorExternalDirAdmissionWithCloningPersistHook(t *testing.T) {
	ctx := context.Background()
	workspace, outside := t.TempDir(), t.TempDir()
	ctx = toolctx.WithWorkspaceRoot(ctx, workspace)
	store, session := NewInMemoryStorage(), NewSession("user")
	require.NoError(t, store.Save(ctx, session))
	handler := &externalDirApprovalRecorder{allow: true}
	actor, engine := newExternalDirTestActor(t, store, session, workspace, runtimepolicy.ModePlan, handler)
	actor.persistHook = func(_ context.Context, input *Session) (*Session, error) {
		return input.CloneForMetadataUpdate(), nil
	}
	for i := 0; i < 2; i++ {
		decision, err := engine.Evaluate(ctx, externalDirRead(session.ID, filepath.Join(outside, "A.java")))
		require.NoError(t, err)
		require.Equal(t, runtimepolicy.DecisionAllow, decision.Type)
	}
	require.Equal(t, 1, handler.calls, "successful hook clones must publish admission back to the live run")
	require.Len(t, sessionAllowedRoots(session), 1)
	require.Len(t, engine.ExternalAllowedRootsSource(), 1)
}

// This exercises the shared actor startup used by CLI and API children. There
// is deliberately no host PrepareRun hook and no manual admission binding.
func TestSessionActorExternalDirAdmissionWiredWithoutHostPrepareRun(t *testing.T) {
	for _, mode := range []string{"plan", "bypass_permissions"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			workspace, outside := t.TempDir(), t.TempDir()
			store, session := NewInMemoryStorage(), NewSession("user")
			session.SetContext("agent_parent_session_id", "parent")
			require.NoError(t, store.Save(ctx, session))
			llmRuntime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: "admission-test", MaxRetries: 0})
			provider := &runMetaSequenceProvider{name: "admission-test", responses: []*llm.LLMResponse{
				{Model: "admission-test", ToolCalls: []types.ToolCall{{ID: "read-1", Name: "run_shell_command", Args: map[string]interface{}{"command": "git status --short", "workdir": outside}}}},
				{Model: "admission-test", ToolCalls: []types.ToolCall{{ID: "read-2", Name: "run_shell_command", Args: map[string]interface{}{"command": "git log -1 --oneline", "workdir": outside}}}},
				{Model: "admission-test", Content: "done"},
			}}
			require.NoError(t, llmRuntime.RegisterProvider(provider.Name(), provider))
			tools := &shellLikeCapturingMCPManager{}
			apiAgent := agent.NewAgentWithLLM(&agent.Config{Name: "child", Model: provider.Name(), MaxSteps: 4,
				Options: map[string]interface{}{"tool_base_path": workspace, "allowed_roots": []string{outside}}}, tools, llmRuntime)
			policy := runtimepolicy.NewToolExecutionPolicy(nil, true)
			policy.SetPathAnchorRoot(workspace)
			apiAgent.SetToolExecutionPolicy(policy)
			handler := &externalDirApprovalRecorder{allow: true}
			// Both snapshots deliberately carry a different session's grant.
			// The owning actor's empty initial source must override them.
			apiAgent.SetPermissionEngine(&runtimepolicy.Engine{Policy: policy, AskHandler: handler, ExternalAllowedRoots: []string{outside},
				ExternalDirTempRoots: []string{filepath.Join(workspace, "no-temp-exemption")}})
			runtimeStore := NewInMemoryRuntimeStore(64)
			actor, err := NewSessionActor(session.ID, SessionActorConfig{Agent: apiAgent, LLMRuntime: llmRuntime,
				SessionStore: store, StateStore: runtimeStore, EventStore: runtimeStore})
			require.NoError(t, err)
			t.Cleanup(actor.Stop)
			result, err := actor.SubmitPrompt(ctx, "inspect files", &team.RunMeta{PermissionMode: mode})
			require.NoError(t, err)
			require.True(t, result.Success)
			require.Equal(t, 2, tools.callCount)
			wantAsks := 1
			if mode == "bypass_permissions" {
				wantAsks = 0
			}
			require.Equal(t, wantAsks, handler.calls)
			persisted, err := store.Load(ctx, session.ID)
			require.NoError(t, err)
			require.Len(t, sessionAllowedRoots(persisted), 1)
		})
	}
}
