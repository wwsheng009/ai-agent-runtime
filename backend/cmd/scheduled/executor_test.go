package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/runtimehost"
	"github.com/wwsheng009/ai-agent-runtime/internal/schedule"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// fakeExecutor 记录提交的 turn（E3.3 适配器单测替身）。
type fakeExecutor struct {
	reqs       []runtimehost.TurnRequest
	submitHook func(runtimehost.TurnRequest)
}

func (f *fakeExecutor) SubmitTurn(_ context.Context, req runtimehost.TurnRequest) (runtimehost.TurnResult, error) {
	if f.submitHook != nil {
		f.submitHook(req)
	}
	f.reqs = append(f.reqs, req)
	return runtimehost.TurnResult{SessionID: req.SessionID, Completed: true}, nil
}
func (f *fakeExecutor) SubmitAsync(context.Context, runtimehost.TurnRequest) error { return nil }
func (f *fakeExecutor) Hub() *chat.SessionHub                                      { return nil }
func (f *fakeExecutor) Close(context.Context) error                                { return nil }

// TestScheduleExecutorAdapterMapsTurn 验证适配器把 ExecuteTurn 映射为一次
// runtimehost turn，并带无人值守（dont_ask）权限模式与 profile。
func TestScheduleExecutorAdapterMapsTurn(t *testing.T) {
	fake := &fakeExecutor{}
	adapter := scheduleExecutorAdapter{exec: fake, profile: "scheduled"}
	if err := adapter.ExecuteTurn(context.Background(), "session-1", "hello"); err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if len(fake.reqs) != 1 {
		t.Fatalf("expected exactly one turn, got %d", len(fake.reqs))
	}
	req := fake.reqs[0]
	if req.SessionID != "session-1" || req.Prompt != "hello" || req.Profile != "scheduled" {
		t.Fatalf("unexpected request: %+v", req)
	}
	if req.RunMeta == nil || req.RunMeta.PermissionMode != "dont_ask" {
		t.Fatalf("unattended run must use dont_ask permission mode, got %+v", req.RunMeta)
	}
}

// TestScheduleExecutorAdapterFailsClosedWithoutExecutor 验证未装配执行核时 fail-closed。
func TestScheduleExecutorAdapterFailsClosedWithoutExecutor(t *testing.T) {
	adapter := scheduleExecutorAdapter{}
	if err := adapter.ExecuteTurn(context.Background(), "s", "p"); err == nil {
		t.Fatal("nil executor must error (never silently succeed)")
	}
}

// TestScheduleExecutorAdapterHeadlessCreatesSession 验证 J2：headless 执行先创建
// 临时会话，再提交带 goal/plan 的 turn（无人值守权限模式）。
func TestScheduleExecutorAdapterHeadlessCreatesSession(t *testing.T) {
	fake := &fakeExecutor{}
	createdUser := ""
	adapter := scheduleExecutorAdapter{
		exec:    fake,
		profile: "scheduled",
		createSession: func(_ context.Context, userID string) (string, error) {
			createdUser = userID
			return "session-headless-1", nil
		},
		initializeSession: func(context.Context, string, schedule.HeadlessTurnRequest) error {
			return nil
		},
	}
	_, err := adapter.ExecuteHeadlessTurn(context.Background(), schedule.HeadlessTurnRequest{
		JobID: "job-1", Goal: "做系统检查", Plan: []string{"检查磁盘"},
		Prompt: "p", Profile: "scheduled", TargetKind: schedule.TargetHeadless,
	})
	if err != nil {
		t.Fatalf("ExecuteHeadlessTurn: %v", err)
	}
	if createdUser != headlessSessionUserID {
		t.Fatalf("headless session must use the daemon user, got %q", createdUser)
	}
	if len(fake.reqs) != 1 {
		t.Fatalf("expected exactly one headless turn, got %d", len(fake.reqs))
	}
	req := fake.reqs[0]
	if req.SessionID != "session-headless-1" || req.Profile != "scheduled" {
		t.Fatalf("unexpected headless request: %+v", req)
	}
	if !strings.Contains(req.Prompt, "做系统检查") || !strings.Contains(req.Prompt, "检查磁盘") {
		t.Fatalf("headless prompt must carry goal and plan, got %q", req.Prompt)
	}
	if req.RunMeta == nil || req.RunMeta.PermissionMode != "dont_ask" {
		t.Fatalf("headless run must use dont_ask, got %+v", req.RunMeta)
	}
}

func TestScheduleExecutorAdapterHeadlessUsesConfiguredProfileFallback(t *testing.T) {
	fake := &fakeExecutor{}
	adapter := scheduleExecutorAdapter{
		exec:    fake,
		profile: "scheduled",
		createSession: func(context.Context, string) (string, error) {
			return "session-headless-profile", nil
		},
		initializeSession: func(context.Context, string, schedule.HeadlessTurnRequest) error {
			return nil
		},
	}
	_, err := adapter.ExecuteHeadlessTurn(context.Background(), schedule.HeadlessTurnRequest{
		Prompt: "p", TargetKind: schedule.TargetHeadless,
	})
	if err != nil {
		t.Fatalf("ExecuteHeadlessTurn: %v", err)
	}
	if len(fake.reqs) != 1 || fake.reqs[0].Profile != "scheduled" {
		t.Fatalf("headless run must fall back to execution.profile, got %+v", fake.reqs)
	}
}

// TestScheduleExecutorAdapterHeadlessMapsExecutionContext 验证初始化顺序以及
// provider/model/reasoning/workspace 映射。
func TestScheduleExecutorAdapterHeadlessMapsExecutionContext(t *testing.T) {
	events := []string{}
	fake := &fakeExecutor{
		submitHook: func(runtimehost.TurnRequest) {
			events = append(events, "submit")
		},
	}
	adapter := scheduleExecutorAdapter{
		exec:    fake,
		profile: "adapter-profile",
		createSession: func(context.Context, string) (string, error) {
			events = append(events, "create")
			return "session-workspace-1", nil
		},
		initializeSession: func(_ context.Context, sessionID string, req schedule.HeadlessTurnRequest) error {
			if sessionID != "session-workspace-1" {
				t.Fatalf("initialize received session %q", sessionID)
			}
			if req.TargetKind != schedule.TargetWorkspace {
				t.Fatalf("initialize received target kind %q", req.TargetKind)
			}
			events = append(events, "initialize")
			return nil
		},
	}
	_, err := adapter.ExecuteHeadlessTurn(context.Background(), schedule.HeadlessTurnRequest{
		JobID:           "job-workspace-1",
		Goal:            "检查工作区",
		TargetKind:      schedule.TargetWorkspace,
		Profile:         "scheduled",
		Provider:        " provider-a ",
		Model:           " model-a ",
		ReasoningEffort: " high ",
		Workspace:       "E:/projects/demo",
	})
	if err != nil {
		t.Fatalf("ExecuteHeadlessTurn: %v", err)
	}
	if got, want := strings.Join(events, ","), "create,initialize,submit"; got != want {
		t.Fatalf("execution order = %q, want %q", got, want)
	}
	if len(fake.reqs) != 1 {
		t.Fatalf("expected exactly one headless turn, got %d", len(fake.reqs))
	}
	req := fake.reqs[0]
	if req.Profile != "scheduled" ||
		req.Provider != " provider-a " ||
		req.Model != " model-a " ||
		req.ReasoningEffort != " high " ||
		req.Workspace != "E:/projects/demo" {
		t.Fatalf("execution context was not mapped: %+v", req)
	}
}

// TestScheduleExecutorAdapterHeadlessInitializationFailure 验证初始化失败时
// 不会构造/提交 actor turn。
func TestScheduleExecutorAdapterHeadlessInitializationFailure(t *testing.T) {
	fake := &fakeExecutor{}
	adapter := scheduleExecutorAdapter{
		exec: fake,
		createSession: func(context.Context, string) (string, error) {
			return "session-init-failure", nil
		},
		initializeSession: func(context.Context, string, schedule.HeadlessTurnRequest) error {
			return errors.New("metadata write failed")
		},
	}
	result, err := adapter.ExecuteHeadlessTurn(context.Background(), schedule.HeadlessTurnRequest{})
	if err == nil || !strings.Contains(err.Error(), "initialize headless session") {
		t.Fatalf("initialization failure must be returned, got result=%+v err=%v", result, err)
	}
	if result.SessionID != "session-init-failure" {
		t.Fatalf("failed initialization should preserve session id, got %q", result.SessionID)
	}
	if len(fake.reqs) != 0 {
		t.Fatalf("SubmitTurn must not run after initialization failure, got %d requests", len(fake.reqs))
	}
}

// TestScheduleExecutorAdapterHeadlessFailsClosed 验证缺少会话工厂或初始化回调时
// fail-closed。
func TestScheduleExecutorAdapterHeadlessFailsClosed(t *testing.T) {
	adapter := scheduleExecutorAdapter{exec: &fakeExecutor{}}
	if _, err := adapter.ExecuteHeadlessTurn(context.Background(), schedule.HeadlessTurnRequest{}); err == nil {
		t.Fatal("headless without a session factory/initializer must error")
	}
}

// TestScheduleExecutorAdapterHeadlessArchivesSession 验证 J2 收尾：headless turn
// 完成后归档临时会话（避免会话表膨胀）。
func TestScheduleExecutorAdapterHeadlessArchivesSession(t *testing.T) {
	fake := &fakeExecutor{}
	archived := ""
	adapter := scheduleExecutorAdapter{
		exec:    fake,
		profile: "scheduled",
		createSession: func(context.Context, string) (string, error) {
			return "session-ephemeral", nil
		},
		initializeSession: func(context.Context, string, schedule.HeadlessTurnRequest) error {
			return nil
		},
		archiveSession: func(_ context.Context, sessionID string) error {
			archived = sessionID
			return nil
		},
	}
	result, err := adapter.ExecuteHeadlessTurn(context.Background(), schedule.HeadlessTurnRequest{
		JobID: "job-1", Goal: "g", TargetKind: schedule.TargetHeadless,
	})
	if err != nil {
		t.Fatalf("ExecuteHeadlessTurn: %v", err)
	}
	if result.SessionID != "session-ephemeral" {
		t.Fatalf("result must carry the ephemeral session id, got %q", result.SessionID)
	}
	if archived != "session-ephemeral" {
		t.Fatalf("ephemeral session must be archived after a completed turn, got %q", archived)
	}
}

func TestInitializeScheduledSessionPersistsExecutionContext(t *testing.T) {
	ctx := context.Background()
	manager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	t.Cleanup(manager.Stop)

	tests := []struct {
		name          string
		targetKind    schedule.TargetKind
		requestedPath string
		wantPath      string
	}{
		{
			name:          "workspace keeps workspace path",
			targetKind:    schedule.TargetWorkspace,
			requestedPath: " E:/projects/demo ",
			wantPath:      "E:/projects/demo",
		},
		{
			name:          "headless clears workspace path",
			targetKind:    schedule.TargetHeadless,
			requestedPath: "E:/projects/should-not-leak",
			wantPath:      "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			session, err := manager.Create(ctx, "scheduled-test-user")
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			err = initializeScheduledSession(ctx, manager, session.ID, schedule.HeadlessTurnRequest{
				TargetKind:      tt.targetKind,
				Workspace:       tt.requestedPath,
				Provider:        " provider-a ",
				Model:           " model-a ",
				ReasoningEffort: " high ",
			})
			if err != nil {
				t.Fatalf("initializeScheduledSession: %v", err)
			}
			persisted, err := manager.Get(ctx, session.ID)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			assertSessionContextString(t, persisted, sessionmeta.WorkspacePath, tt.wantPath)
			assertSessionContextString(t, persisted, sessionmeta.ProviderName, "provider-a")
			assertSessionContextString(t, persisted, sessionmeta.Model, "model-a")
			assertSessionContextString(t, persisted, toolbroker.AgentSessionContextRequestedModel, "model-a")
			assertSessionContextString(t, persisted, sessionmeta.ReasoningEffort, "high")
		})
	}
}

func assertSessionContextString(t *testing.T, session *chat.Session, key, want string) {
	t.Helper()
	value, ok := session.GetContext(key)
	if !ok {
		if want == "" {
			return
		}
		t.Fatalf("session context %q is missing, want %q", key, want)
	}
	got, ok := value.(string)
	if !ok {
		t.Fatalf("session context %q has type %T, want string", key, value)
	}
	if got != want {
		t.Fatalf("session context %q = %q, want %q", key, got, want)
	}
}
