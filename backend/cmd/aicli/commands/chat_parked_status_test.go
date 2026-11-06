package commands

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

// §6.12 动态状态采样：父 turn 挂起后，动态栏必须能报告子代理的计数与最近
// 事件，而不是永远停留在静态的 "Waiting for subagents"。

func TestFormatChatParkedSubagentDetail(t *testing.T) {
	now := time.Date(2026, 11, 6, 5, 50, 0, 0, time.UTC)
	cases := []struct {
		name     string
		activity chatParkedSubagentActivity
		want     string
	}{
		{
			name: "running with latest live event",
			activity: chatParkedSubagentActivity{
				Known: true, Running: 1, Total: 1,
				LatestLabel: "dOOBEERM", LatestEvent: "cargo   build\n  --release",
				LatestAt: now.Add(-12 * time.Second),
			},
			want: "1 running · dOOBEERM cargo build --release (12s ago)",
		},
		{
			name: "running and done counts",
			activity: chatParkedSubagentActivity{
				Known: true, Running: 2, Done: 1, Total: 3,
			},
			want: "2 running · 1 done",
		},
		{
			name: "event without counts still reports the child",
			activity: chatParkedSubagentActivity{
				Known: true, LatestLabel: "dOOBEERM", LatestEvent: "apply_patch",
				LatestAt: now.Add(-3 * time.Second),
			},
			want: "dOOBEERM apply_patch (3s ago)",
		},
		{
			name:     "unknown projection does not overwrite the previous line",
			activity: chatParkedSubagentActivity{},
			want:     "",
		},
		{
			name:     "known empty projection",
			activity: chatParkedSubagentActivity{Known: true},
			want:     "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatChatParkedSubagentDetail(tc.activity, now); got != tc.want {
				t.Fatalf("detail = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBuildChatParkedSubagentActivityAgentSession(t *testing.T) {
	now := time.Date(2026, 11, 6, 5, 50, 0, 0, time.UTC)
	obligations := []string{"agent_session:child-active", "agent_session:child-done"}
	states := []supervision.DescendantState{
		{Kind: supervision.SubjectAgentSession, ID: "child-active", ExecutionStatus: "active", SupervisionState: supervision.SupervisionRunning},
		{Kind: supervision.SubjectAgentSession, ID: "child-done", ExecutionStatus: "closed", SupervisionState: supervision.SupervisionTerminated},
	}
	latest := func(childID string) (string, string, time.Time, bool) {
		if childID != "child-active" {
			return "", "", time.Time{}, false
		}
		return "progress", "cargo test --all", now.Add(-5 * time.Second), true
	}

	activity := buildChatParkedSubagentActivity(obligations, states, nil, latest)
	if activity.Running != 1 || activity.Done != 1 || activity.Total != 2 {
		t.Fatalf("counts = running:%d done:%d total:%d, want 1/1/2", activity.Running, activity.Done, activity.Total)
	}
	if activity.LatestLabel != "child-active" || activity.LatestEvent != "cargo test --all" || !activity.LatestAt.Equal(now.Add(-5*time.Second)) {
		t.Fatalf("latest = %+v, want child-active/cargo test --all/-5s", activity)
	}
}

func TestBuildChatParkedSubagentActivityBatchCountsOnce(t *testing.T) {
	now := time.Date(2026, 11, 6, 5, 50, 0, 0, time.UTC)
	obligations := []string{"batch-1", "task-1"}
	groups := []supervision.ProgressGroup{{
		GroupID: "batch-1", Total: 3, Running: 2, Completed: 1,
		RunningTasks: []supervision.ProgressTask{{
			TaskID: "task-1", ChildSessionID: "child-1", State: "running",
			LastProgressAt: now.Add(-2 * time.Second), LastMessage: "apply_patch",
		}},
	}}

	activity := buildChatParkedSubagentActivity(obligations, nil, groups, nil)
	if activity.Running != 2 || activity.Done != 1 || activity.Total != 3 {
		t.Fatalf("counts = running:%d done:%d total:%d, want 2/1/3", activity.Running, activity.Done, activity.Total)
	}
	if activity.LatestLabel != "child-1" || activity.LatestEvent != "apply_patch" {
		t.Fatalf("latest = %+v, want child-1/apply_patch", activity)
	}
}

func TestBuildChatParkedSubagentActivityTaskWithoutBatchRowCountsOnce(t *testing.T) {
	now := time.Date(2026, 11, 6, 5, 50, 0, 0, time.UTC)
	obligations := []string{"task-1", "task-2"}
	groups := []supervision.ProgressGroup{{
		GroupID: "batch-1",
		RunningTasks: []supervision.ProgressTask{
			{TaskID: "task-1", ChildSessionID: "child-1", State: "running", LastProgressAt: now, LastMessage: "go test"},
			{TaskID: "task-2", ChildSessionID: "child-2", State: "running", LastProgressAt: now.Add(-time.Second), LastMessage: "go build"},
		},
	}}

	activity := buildChatParkedSubagentActivity(obligations, nil, groups, nil)
	if activity.Running != 2 || activity.Done != 0 {
		t.Fatalf("counts = running:%d done:%d, want 2/0", activity.Running, activity.Done)
	}
}

func TestChatDynamicStatusActionParkedIncludesSubagentDetail(t *testing.T) {
	action, role, interruptible := chatDynamicStatusAction(chatSurfaceStatus{
		kind:   chatSurfaceStatusParked,
		detail: "1 running · dOOBEERM cargo build (12s ago)",
	}, chatInputModeChat)
	if action != "Waiting for subagents · 1 running · dOOBEERM cargo build (12s ago)" {
		t.Fatalf("action = %q", action)
	}
	if role != style.RoleInfo || interruptible {
		t.Fatalf("role=%v interruptible=%v, want RoleInfo and non-interruptible", role, interruptible)
	}

	model := buildChatDynamicStatusModelForWidthInputModeCompletionAndEsc(
		chatSurfaceStatus{kind: chatSurfaceStatusParked, detail: "1 running · dOOBEERM cargo build (12s ago)"},
		160, chatInputModeChat, 0, false, false,
	)
	if model == nil {
		t.Fatal("parked status with detail must produce a dynamic model")
	}
	if !strings.Contains(model.StateText, "1 running") {
		t.Fatalf("state text = %q, want subagent detail", model.StateText)
	}
}

func TestWebDynamicStatusPayloadParkedPrefersSubagentDetail(t *testing.T) {
	coord := &chatInteractionCoordinator{
		dynamicStatusCompleted:        true,
		dynamicStatusCompletedElapsed: 5 * time.Minute,
	}
	payload := coord.webDynamicStatusPayloadLocked(chatSurfaceStatus{
		kind:   chatSurfaceStatusParked,
		detail: "1 running · dOOBEERM cargo build (12s ago)",
	}, time.Now())
	if got := payload["text"]; got != "◦ Waiting for subagents · 1 running · dOOBEERM cargo build (12s ago)" {
		t.Fatalf("web parked text = %v", got)
	}
	if payload["active"] != true {
		t.Fatalf("web parked payload active = %v, want true", payload["active"])
	}
	if payload["interruptible"] != false {
		t.Fatalf("web parked payload interruptible = %v, want false", payload["interruptible"])
	}
}

func TestParkedSubagentStatusWatchUpdatesDynamicStatus(t *testing.T) {
	oldInterval := chatParkedSubagentRefreshInterval
	oldTimeout := chatParkedSubagentRefreshTimeout
	oldLoader := chatParkedSubagentActivityLoader
	chatParkedSubagentRefreshInterval = 10 * time.Millisecond
	chatParkedSubagentRefreshTimeout = 500 * time.Millisecond
	chatParkedSubagentActivityLoader = func(context.Context, *ChatSession, string) chatParkedSubagentActivity {
		return chatParkedSubagentActivity{
			Known: true, Running: 1, Total: 1,
			LatestLabel: "dOOBEERM", LatestEvent: "cargo build",
			LatestAt: time.Now().Add(-time.Second),
		}
	}
	t.Cleanup(func() {
		chatParkedSubagentRefreshInterval = oldInterval
		chatParkedSubagentRefreshTimeout = oldTimeout
		chatParkedSubagentActivityLoader = oldLoader
	})

	session := &ChatSession{
		RuntimeSession:   &runtimechat.Session{ID: "parent-session"},
		LocalRuntimeHost: &localChatRuntimeHost{},
	}
	coord := &chatInteractionCoordinator{session: session}
	coord.mu.Lock()
	coord.updateSurfaceStatusLocked(chatSurfaceStatus{kind: chatSurfaceStatusParked})
	coord.mu.Unlock()

	deadline := time.Now().Add(2 * time.Second)
	for {
		coord.mu.Lock()
		model := coord.dynamicStatusModel
		done := coord.parkedSubagentWatchDone
		coord.mu.Unlock()
		if model != nil && strings.Contains(model.StateText, "1 running") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("parked sampler did not update the dynamic status; model=%+v", model)
		}
		time.Sleep(10 * time.Millisecond)
		if done == nil {
			t.Fatal("parked watch stopped before publishing a sample")
		}
	}

	coord.mu.Lock()
	done := coord.parkedSubagentWatchDone
	coord.mu.Unlock()
	if done == nil {
		t.Fatal("parked watch should own a done channel while active")
	}

	coord.mu.Lock()
	coord.updateSurfaceStatusLocked(chatSurfaceStatus{kind: chatSurfaceStatusIdle})
	coord.mu.Unlock()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("parked watch did not stop after leaving parked")
	}
}
