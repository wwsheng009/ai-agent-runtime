package main

import (
	"context"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/runtimehost"
	"github.com/wwsheng009/ai-agent-runtime/internal/schedule"
)

// fakeExecutor 记录提交的 turn（E3.3 适配器单测替身）。
type fakeExecutor struct {
	reqs []runtimehost.TurnRequest
}

func (f *fakeExecutor) SubmitTurn(_ context.Context, req runtimehost.TurnRequest) (runtimehost.TurnResult, error) {
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

// TestScheduleExecutorAdapterHeadlessFailsClosed 验证无会话工厂时 fail-closed。
func TestScheduleExecutorAdapterHeadlessFailsClosed(t *testing.T) {
	adapter := scheduleExecutorAdapter{exec: &fakeExecutor{}}
	if _, err := adapter.ExecuteHeadlessTurn(context.Background(), schedule.HeadlessTurnRequest{}); err == nil {
		t.Fatal("headless without a session factory must error")
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
