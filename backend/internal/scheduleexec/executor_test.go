package scheduleexec

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/runtimehost"
	"github.com/wwsheng009/ai-agent-runtime/internal/schedule"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
)

type fakeTurnExecutor struct {
	requests []runtimehost.TurnRequest
	result   runtimehost.TurnResult
	err      error
}

func (f *fakeTurnExecutor) SubmitTurn(_ context.Context, req runtimehost.TurnRequest) (runtimehost.TurnResult, error) {
	f.requests = append(f.requests, req)
	result := f.result
	if result.SessionID == "" {
		result.SessionID = req.SessionID
	}
	return result, f.err
}

type fakeSessionManager struct {
	nextID   int
	sessions map[string]*chat.Session
	users    []string
	archived []string
}

type fakeWaiterExecutor struct {
	waitCalled bool
}

func (f *fakeWaiterExecutor) SubmitTurn(context.Context, runtimehost.TurnRequest) (runtimehost.TurnResult, error) {
	return runtimehost.TurnResult{}, fmt.Errorf("SubmitTurn must not be used when TurnWaiter is available")
}

func (f *fakeWaiterExecutor) SubmitTurnWait(_ context.Context, req runtimehost.TurnRequest) (runtimehost.TurnResult, error) {
	f.waitCalled = true
	return runtimehost.TurnResult{
		SessionID: req.SessionID,
		Completed: true,
		Success:   true,
		Output:    "waited output",
	}, nil
}

func newFakeSessionManager() *fakeSessionManager {
	return &fakeSessionManager{sessions: map[string]*chat.Session{}}
}

func (f *fakeSessionManager) Create(_ context.Context, userID string) (*chat.Session, error) {
	f.nextID++
	session := chat.NewSession(userID)
	session.ID = fmt.Sprintf("session-%d", f.nextID)
	f.sessions[session.ID] = session
	f.users = append(f.users, userID)
	return session, nil
}

func (f *fakeSessionManager) Get(_ context.Context, sessionID string) (*chat.Session, error) {
	return f.sessions[sessionID], nil
}

func (f *fakeSessionManager) Update(_ context.Context, session *chat.Session) error {
	f.sessions[session.ID] = session
	return nil
}

func (f *fakeSessionManager) Archive(_ context.Context, sessionID string) error {
	f.archived = append(f.archived, sessionID)
	return nil
}

func TestExecutorHeadlessCreatesInitializesExecutesAndArchives(t *testing.T) {
	exec := &fakeTurnExecutor{result: runtimehost.TurnResult{
		Completed: true,
		Success:   true,
		Output:    "job output",
	}}
	sessions := newFakeSessionManager()
	adapter, err := New(Options{
		Executor:       exec,
		Sessions:       sessions,
		Profile:        "scheduled",
		HeadlessUserID: "scheduled-test",
	})
	require.NoError(t, err)

	result, err := adapter.ExecuteHeadlessTurn(context.Background(), schedule.HeadlessTurnRequest{
		JobID:           "job-1",
		Goal:            "keep the project healthy",
		Plan:            []string{"inspect", "report"},
		Prompt:          "run checks",
		TargetKind:      schedule.TargetWorkspace,
		Provider:        "provider-a",
		Model:           "model-a",
		ReasoningEffort: "high",
		Workspace:       "E:/projects/demo",
	})
	require.NoError(t, err)
	require.Equal(t, "session-1", result.SessionID)
	require.Equal(t, "job output", result.Output)
	require.Equal(t, []string{"scheduled-test"}, sessions.users)
	require.Equal(t, []string{"session-1"}, sessions.archived)

	session := sessions.sessions["session-1"]
	require.NotNil(t, session)
	workspace, ok := session.GetContext(sessionmeta.WorkspacePath)
	require.True(t, ok)
	require.Equal(t, "E:/projects/demo", workspace)
	provider, ok := session.GetContext(sessionmeta.ProviderName)
	require.True(t, ok)
	require.Equal(t, "provider-a", provider)
	model, ok := session.GetContext(sessionmeta.Model)
	require.True(t, ok)
	require.Equal(t, "model-a", model)

	require.Len(t, exec.requests, 1)
	req := exec.requests[0]
	require.Equal(t, "session-1", req.SessionID)
	require.Equal(t, "scheduled", req.Profile, "execution profile must be the fallback for headless jobs")
	require.Equal(t, "provider-a", req.Provider)
	require.Equal(t, "model-a", req.Model)
	require.Equal(t, "high", req.ReasoningEffort)
	require.Equal(t, "E:/projects/demo", req.Workspace)
	require.Contains(t, req.Prompt, "[SCHEDULE JOB] goal: keep the project healthy")
	require.Contains(t, req.Prompt, "1. inspect")
	require.Contains(t, req.Prompt, "run checks")
	require.NotNil(t, req.RunMeta)
	require.Equal(t, "schedule", req.RunMeta.Source)
	require.Equal(t, string(policy.ModeDontAsk), req.RunMeta.PermissionMode)
}

func TestExecutorHeadlessUsesJobProfileWhenPresent(t *testing.T) {
	exec := &fakeTurnExecutor{result: runtimehost.TurnResult{Completed: true, Success: true}}
	sessions := newFakeSessionManager()
	adapter, err := New(Options{Executor: exec, Sessions: sessions, Profile: "fallback"})
	require.NoError(t, err)

	_, err = adapter.ExecuteHeadlessTurn(context.Background(), schedule.HeadlessTurnRequest{
		Prompt:     "p",
		TargetKind: schedule.TargetHeadless,
		Profile:    "job-profile",
	})
	require.NoError(t, err)
	require.Len(t, exec.requests, 1)
	require.Equal(t, "job-profile", exec.requests[0].Profile)
}

func TestExecutorHeadlessPrefersTurnWaiter(t *testing.T) {
	exec := &fakeWaiterExecutor{}
	sessions := newFakeSessionManager()
	adapter, err := New(Options{Executor: exec, Sessions: sessions})
	require.NoError(t, err)

	result, err := adapter.ExecuteHeadlessTurn(context.Background(), schedule.HeadlessTurnRequest{
		Prompt:     "p",
		TargetKind: schedule.TargetHeadless,
	})
	require.NoError(t, err)
	require.True(t, exec.waitCalled)
	require.Equal(t, "waited output", result.Output)
}

func TestExecutorHeadlessMapsTurnFailureAndKeepsSession(t *testing.T) {
	exec := &fakeTurnExecutor{result: runtimehost.TurnResult{
		Completed: true,
		Success:   false,
		Error:     "model failed",
	}}
	sessions := newFakeSessionManager()
	adapter, err := New(Options{Executor: exec, Sessions: sessions})
	require.NoError(t, err)

	result, err := adapter.ExecuteHeadlessTurn(context.Background(), schedule.HeadlessTurnRequest{
		Prompt:     "p",
		TargetKind: schedule.TargetHeadless,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "model failed")
	require.Equal(t, "session-1", result.SessionID)
	require.Empty(t, sessions.archived, "failed headless sessions must stay available for diagnosis")
}

func TestExecutorSessionTurnUsesSharedRunMetaAndProfile(t *testing.T) {
	exec := &fakeTurnExecutor{result: runtimehost.TurnResult{Completed: true, Success: true}}
	adapter, err := New(Options{Executor: exec, Profile: "scheduled"})
	require.NoError(t, err)

	require.NoError(t, adapter.ExecuteTurn(context.Background(), "session-1", "hello"))
	require.Len(t, exec.requests, 1)
	req := exec.requests[0]
	require.Equal(t, "session-1", req.SessionID)
	require.Equal(t, "hello", req.Prompt)
	require.Equal(t, "scheduled", req.Profile)
	require.Equal(t, "schedule", req.RunMeta.Source)
	require.Equal(t, string(policy.ModeDontAsk), req.RunMeta.PermissionMode)
}

func TestNewRequiresExecutor(t *testing.T) {
	_, err := New(Options{})
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "executor is required"))
}
