package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/planmode"
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

func reopenFixtureRecord(t *testing.T, store *planstore.Store, workspace string) planstore.Record {
	t.Helper()
	record, err := store.Record(planstore.RecordOptions{
		SessionID:   "session-1",
		ProjectPath: workspace,
		PlanPath:    "docs/plan.md",
		Title:       "reopen fixture",
	})
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	record, err = store.Snapshot(planstore.SnapshotOptions{
		ID:       record.ID,
		Decision: "quit",
		Source:   "user",
		Content:  []byte("# Plan\n\narchived body\n"),
		Status:   planstore.StatusNotImplemented,
	})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	return record
}

func TestPlansReopenRestoresArchiveAndEntersPlanMode(t *testing.T) {
	store := withPlanStore(t)
	workspace := t.TempDir()
	record := reopenFixtureRecord(t, store, workspace)

	session := newPlanCommandSession(runtimepolicy.ModeDefault)
	session.RuntimeSession.Metadata.Context[chatPlanWorkspacePathKey] = workspace

	out := captureStdout(t, func() { handlePlansCommand(session, "/plans reopen "+record.ID) })
	for _, expected := range []string{"已从归档恢复计划", "docs/plan.md", "version: v1", "已按快照创建", "已进入 plan mode", "/plan request_changes"} {
		if !strings.Contains(out, expected) {
			t.Fatalf("reopen output missing %q: %q", expected, out)
		}
	}

	restored, err := os.ReadFile(filepath.Join(workspace, filepath.FromSlash("docs/plan.md")))
	if err != nil {
		t.Fatalf("read restored plan: %v", err)
	}
	if string(restored) != "# Plan\n\narchived body\n" {
		t.Fatalf("unexpected restored body: %q", string(restored))
	}

	state := loadChatPlanMode(session)
	if !planmode.IsActive(state) {
		t.Fatalf("expected plan mode to be active after reopen: %+v", state)
	}
	if state.ReopenedFrom != record.ID || state.ReopenedVersion != 1 {
		t.Fatalf("expected reopen provenance on the state, got %q v%d", state.ReopenedFrom, state.ReopenedVersion)
	}
	if got := planmode.ReopenProvenance(state); got != record.ID+" v1" {
		t.Fatalf("unexpected provenance text: %q", got)
	}
}

func TestPlansReopenRefusesDivergedFileUntilForced(t *testing.T) {
	store := withPlanStore(t)
	workspace := t.TempDir()
	record := reopenFixtureRecord(t, store, workspace)
	target := filepath.Join(workspace, filepath.FromSlash("docs/plan.md"))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(target, []byte("locally edited\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	session := newPlanCommandSession(runtimepolicy.ModeDefault)
	session.RuntimeSession.Metadata.Context[chatPlanWorkspacePathKey] = workspace

	refused := captureStdout(t, func() { handlePlansCommand(session, "/plans reopen "+record.ID) })
	if !strings.Contains(refused, "未重新评审") || !strings.Contains(refused, "--force") {
		t.Fatalf("expected an actionable refusal, got %q", refused)
	}
	untouched, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(untouched) != "locally edited\n" {
		t.Fatalf("refused reopen must not rewrite the file: %q", string(untouched))
	}

	forced := captureStdout(t, func() { handlePlansCommand(session, "/plans reopen "+record.ID+" --force") })
	if !strings.Contains(forced, "已按快照覆盖工作区文件") {
		t.Fatalf("expected a forced-overwrite note, got %q", forced)
	}
	overwritten, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(overwritten) != "# Plan\n\narchived body\n" {
		t.Fatalf("forced reopen must restore the snapshot: %q", string(overwritten))
	}
}

func TestPlansReopenSelectsVersionAndNeedsSession(t *testing.T) {
	store := withPlanStore(t)
	workspace := t.TempDir()
	record := reopenFixtureRecord(t, store, workspace)
	if _, err := store.Snapshot(planstore.SnapshotOptions{
		ID:       record.ID,
		Decision: "request_changes",
		Source:   "user",
		Notes:    "widen the scope",
		Content:  []byte("# Plan v2\n\nsecond body\n"),
	}); err != nil {
		t.Fatalf("snapshot v2: %v", err)
	}

	session := newPlanCommandSession(runtimepolicy.ModeDefault)
	session.RuntimeSession.Metadata.Context[chatPlanWorkspacePathKey] = workspace

	out := captureStdout(t, func() { handlePlansCommand(session, "/plans reopen "+record.ID+" v1") })
	if !strings.Contains(out, "version: v1") {
		t.Fatalf("expected v1 in the reopen output, got %q", out)
	}
	restored, err := os.ReadFile(filepath.Join(workspace, filepath.FromSlash("docs/plan.md")))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(restored) != "# Plan\n\narchived body\n" {
		t.Fatalf("expected the v1 body, got %q", string(restored))
	}

	// Without a session the action explains itself instead of panicking.
	sessionless := captureStdout(t, func() { handlePlansCommand(nil, "/plans reopen "+record.ID) })
	if !strings.Contains(sessionless, "需要活动会话") {
		t.Fatalf("expected a session hint, got %q", sessionless)
	}

	// A bare keyword stays a usage hint, and browsing still works.
	usage := captureStdout(t, func() { handlePlansCommand(nil, "/plans reopen") })
	if !strings.Contains(usage, "未找到计划") {
		t.Fatalf("a lone keyword must fall back to the id lookup path, got %q", usage)
	}
}

func TestParsePlansReopenArgs(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		wantID      string
		wantVersion int
		wantForce   bool
		wantErr     bool
	}{
		{name: "id only", args: []string{"demo/plan"}, wantID: "demo/plan"},
		{name: "version shorthand", args: []string{"demo/plan", "v3"}, wantID: "demo/plan", wantVersion: 3},
		{name: "version flag", args: []string{"--version", "4", "demo/plan"}, wantID: "demo/plan", wantVersion: 4},
		{name: "version flag inline", args: []string{"demo/plan", "--version=5"}, wantID: "demo/plan", wantVersion: 5},
		{name: "force", args: []string{"demo/plan", "v2", "--force"}, wantID: "demo/plan", wantVersion: 2, wantForce: true},
		{name: "missing version value", args: []string{"demo/plan", "--version"}, wantErr: true},
		{name: "bad version", args: []string{"demo/plan", "vX"}, wantID: "demo/plan vX"},
		{name: "zero version flag", args: []string{"demo/plan", "--version=0"}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			id, version, force, err := parsePlansReopenArgs(test.args)
			if test.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got id=%q version=%d", id, version)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if id != test.wantID || version != test.wantVersion || force != test.wantForce {
				t.Fatalf("got id=%q version=%d force=%v", id, version, force)
			}
		})
	}
}
