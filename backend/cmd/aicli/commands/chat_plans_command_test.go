package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/planstore"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
)

func withPlanStore(t *testing.T) *planstore.Store {
	t.Helper()
	previous := chatPlanArtifactStore
	store := planstore.NewStore(t.TempDir())
	chatPlanArtifactStore = store
	t.Cleanup(func() { chatPlanArtifactStore = previous })
	return store
}

func TestPlansCommandListsAndOpensArchivedPlans(t *testing.T) {
	store := withPlanStore(t)
	record, err := store.Record(planstore.RecordOptions{
		SessionID:   "session-1",
		ProjectPath: "/work/demo",
		PlanPath:    "docs/plan.md",
		Title:       "demo plan",
	})
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	record, err = store.Snapshot(planstore.SnapshotOptions{
		ID:       record.ID,
		Decision: "approve",
		Source:   "user",
		Content:  []byte("# Plan\n\nship it\n"),
		Status:   planstore.StatusApproved,
	})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	list := captureStdout(t, func() { handlePlansCommand(nil, "/plans") })
	for _, expected := range []string{record.ID, "approved", "docs/plan.md", "已归档计划: 1"} {
		if !strings.Contains(list, expected) {
			t.Fatalf("plan list missing %q: %q", expected, list)
		}
	}

	detail := captureStdout(t, func() { handlePlansCommand(nil, "/plans "+record.ID) })
	for _, expected := range []string{"ship it", "v1: approve (user)", "status: approved"} {
		if !strings.Contains(detail, expected) {
			t.Fatalf("plan detail missing %q: %q", expected, detail)
		}
	}

	missing := captureStdout(t, func() { handlePlansCommand(nil, "/plans demo/ghost") })
	if !strings.Contains(missing, "未找到计划") {
		t.Fatalf("expected not-found hint, got %q", missing)
	}
}

func TestPlansCommandEmptyStore(t *testing.T) {
	withPlanStore(t)
	out := captureStdout(t, func() { handlePlansCommand(nil, "/plans") })
	if !strings.Contains(out, "尚无已归档计划") {
		t.Fatalf("expected empty-archive hint, got %q", out)
	}
}

func TestPlanReviewCommandShowsRevisionAndVerdicts(t *testing.T) {
	withPlanArtifactStore(t)
	workspace := t.TempDir()
	planPath := "docs/plan.md"
	planAbs := filepath.Join(workspace, filepath.FromSlash(planPath))
	if err := os.MkdirAll(filepath.Dir(planAbs), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(planAbs, []byte("# Plan\n\nreviewable body\n"), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}

	session := newPlanCommandSession(runtimepolicy.ModeDefault)
	session.RuntimeSession.Metadata.Context[chatPlanWorkspacePathKey] = workspace
	if err := enterChatPlanMode(session, planPath); err != nil {
		t.Fatalf("enter: %v", err)
	}

	review := captureStdout(t, func() { _ = handlePlanCommand(session, "/plan review") })
	for _, expected := range []string{"计划评审", "reviewable body", "/plan request_changes", "/plan quit"} {
		if !strings.Contains(review, expected) {
			t.Fatalf("plan review missing %q: %q", expected, review)
		}
	}

	status := captureStdout(t, func() { _ = handlePlanCommand(session, "/plan status") })
	if !strings.Contains(status, "计划已就绪待评审") {
		t.Fatalf("expected ready-to-review hint in status, got %q", status)
	}

	if err := enterChatPlanMode(session, "docs/empty-plan.md"); err != nil {
		t.Fatalf("re-enter: %v", err)
	}
	empty := captureStdout(t, func() { _ = handlePlanCommand(session, "/plan review") })
	if !strings.Contains(empty, "尚无可读内容") {
		t.Fatalf("expected empty-plan hint, got %q", empty)
	}
}

func TestPlanReviewCommandWithoutActivePlan(t *testing.T) {
	session := newPlanCommandSession(runtimepolicy.ModeDefault)
	out := captureStdout(t, func() { _ = handlePlanCommand(session, "/plan review") })
	if !strings.Contains(out, "当前没有生效的 plan 模式") {
		t.Fatalf("expected inactive hint, got %q", out)
	}
}
