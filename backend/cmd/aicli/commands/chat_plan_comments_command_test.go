package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
)

// planCommentFixture 建一个处于 plan mode、工作区落在临时目录的会话，并把计划
// 正文写到工作区（/plan comment 会据此归档第一轮）。
func planCommentFixture(t *testing.T, content string) (*ChatSession, string) {
	t.Helper()
	withPlanArtifactStore(t)
	workspace := t.TempDir()
	session := newPlanCommandSession(runtimepolicy.ModeDefault)
	session.RuntimeSession.Metadata.Context[chatPlanWorkspacePathKey] = workspace
	if err := enterChatPlanMode(session, "plan.md"); err != nil {
		t.Fatalf("enter plan mode: %v", err)
	}
	writePlanCommentFixtureFile(t, workspace, "plan.md", content)
	return session, workspace
}

func writePlanCommentFixtureFile(t *testing.T, workspace, planPath, content string) {
	t.Helper()
	absolute := filepath.Join(workspace, filepath.FromSlash(planPath))
	if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(absolute, []byte(content), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}
}

func runPlanCommandText(t *testing.T, session *ChatSession, command string) string {
	t.Helper()
	stdout, _ := captureStdoutStderr(t, func() {
		_ = handlePlanCommand(session, command)
	})
	return stdout
}

func TestPlanCommentArchivesFirstRoundAndLists(t *testing.T) {
	session, workspace := planCommentFixture(t, "# Plan\n\n1. ship it\n2. roll back\n")

	// 首次评论：没有归档轮次，先按 decision=comment 登记这一轮快照，再锚定 L3-4。
	out := runPlanCommandText(t, session, "/plan comment L3-4 补上回滚风险")
	if !strings.Contains(out, "已记录行级评论") || !strings.Contains(out, "锚定 v1") {
		t.Fatalf("unexpected comment confirmation:\n%s", out)
	}

	store := chatPlanStore()
	record, ok, err := store.Get(chatPlanCommentRecordID(session))
	if err != nil || !ok {
		t.Fatalf("record missing after comment: ok=%v err=%v", ok, err)
	}
	if record.Version != 1 {
		t.Fatalf("expected the first comment to archive round v1, got version %d", record.Version)
	}
	if len(record.Rounds) != 1 || record.Rounds[0].Decision != "comment" {
		t.Fatalf("expected a comment round, got %+v", record.Rounds)
	}

	comments, err := store.Comments(record.ID)
	if err != nil || len(comments) != 1 {
		t.Fatalf("expected one stored comment, got %+v err=%v", comments, err)
	}
	if comments[0].StartLine != 3 || comments[0].EndLine != 4 || comments[0].Revision != 1 {
		t.Fatalf("unexpected anchor: %+v", comments[0])
	}
	if comments[0].Excerpt != "1. ship it\n2. roll back" {
		t.Fatalf("unexpected excerpt: %q", comments[0].Excerpt)
	}

	// 列表：同一轮重放 → 与当前正文一致。
	listed := runPlanCommandText(t, session, "/plan comments")
	for _, expected := range []string{"行级评论（1 条，按最新轮 v1 重放锚点）", "L3-4", "[与当前正文一致]", "1. ship it 2. roll back", "补上回滚风险"} {
		if !strings.Contains(listed, expected) {
			t.Fatalf("comment list missing %q:\n%s", expected, listed)
		}
	}
	_ = workspace
}

func TestPlanCommentReplaysAcrossRoundsAndFeedsReviewNotes(t *testing.T) {
	session, workspace := planCommentFixture(t, "# Plan\n\n1. ship it\n2. roll back\n")
	if out := runPlanCommandText(t, session, "/plan comment L3-4 补上回滚风险"); !strings.Contains(out, "已记录行级评论") {
		t.Fatalf("comment failed:\n%s", out)
	}

	// 正文改写（步骤段前插入背景段）后再裁决一轮：评论随之移动到 L5-6。
	writePlanCommentFixtureFile(t, workspace, "plan.md", "# Plan\n\n## 背景\n补充背景\n\n1. ship it\n2. roll back\n")
	verdict := runPlanCommandText(t, session, "/plan request_changes 顺便补一句背景")
	if !strings.Contains(verdict, "已请求修改计划") {
		t.Fatalf("verdict missing:\n%s", verdict)
	}

	// 行级评论并入一次性评审提醒（原 notes 仍在前面）。
	state := loadChatPlanMode(session)
	if !strings.Contains(state.PendingReviewNotes, "顺便补一句背景") {
		t.Fatalf("user notes must stay in the reminder, got %q", state.PendingReviewNotes)
	}
	if !strings.Contains(state.PendingReviewNotes, "行级评论（已按当前计划正文重放锚点）") ||
		!strings.Contains(state.PendingReviewNotes, "补上回滚风险") {
		t.Fatalf("comments must ride the one-shot reminder, got %q", state.PendingReviewNotes)
	}

	// 轮次记录里不重复评论正文，只保留用户自己写的 notes。
	record, ok, err := chatPlanStore().Get(chatPlanCommentRecordID(session))
	if err != nil || !ok {
		t.Fatalf("record: ok=%v err=%v", ok, err)
	}
	if record.Version != 2 {
		t.Fatalf("expected a second round, got version %d", record.Version)
	}
	last := record.Rounds[len(record.Rounds)-1]
	if last.Notes != "顺便补一句背景" || strings.Contains(last.Notes, "行级评论") {
		t.Fatalf("round notes must not duplicate the comment block, got %q", last.Notes)
	}

	// 列表中该评论显示为「已移动」，并保留原锚点。
	listed := runPlanCommandText(t, session, "/plan comments")
	if !strings.Contains(listed, "[原文已移动（原 L3-4）]") || !strings.Contains(listed, "按最新轮 v2 重放锚点") {
		t.Fatalf("expected a moved anchor after the rewrite:\n%s", listed)
	}
}

func TestPlanCommentRejectsBadInput(t *testing.T) {
	session, _ := planCommentFixture(t, "# Plan\n\n1. ship it\n")

	cases := []struct {
		name    string
		command string
		expect  string
	}{
		{"missing body", "/plan comment L3", "用法: /plan comment <L12|L12-14> <正文>"},
		{"missing range", "/plan comment", "用法: /plan comment <L12|L12-14> <正文>"},
		{"bad range", "/plan comment Lx 说明", "行号格式"},
		{"reversed range", "/plan comment L5-2 说明", "行号区间无效"},
		{"range past the body", "/plan comment L90-95 说明", "outside plan revision"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if out := runPlanCommandText(t, session, tc.command); !strings.Contains(out, tc.expect) {
				t.Fatalf("expected %q, got:\n%s", tc.expect, out)
			}
		})
	}
	// 越界与格式错误都不应写进存储。
	store := chatPlanStore()
	record, ok, err := store.Get(chatPlanCommentRecordID(session))
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if ok {
		if comments, err := store.Comments(record.ID); err != nil || len(comments) != 0 {
			t.Fatalf("rejected comments must not be stored, got %+v err=%v", comments, err)
		}
	}
}

func TestPlanCommentWithoutPlanContextIsRefused(t *testing.T) {
	withPlanArtifactStore(t)
	session := newPlanCommandSession(runtimepolicy.ModeDefault)
	session.RuntimeSession.Metadata.Context[chatPlanWorkspacePathKey] = t.TempDir()

	for _, command := range []string{"/plan comment L1 说明", "/plan comments"} {
		if out := runPlanCommandText(t, session, command); !strings.Contains(out, "当前会话没有计划上下文") {
			t.Fatalf("expected a plan-context error for %q, got:\n%s", command, out)
		}
	}
}

func TestPlanCommentsHintWhenNothingArchived(t *testing.T) {
	withPlanArtifactStore(t)
	session := newPlanCommandSession(runtimepolicy.ModeDefault)
	session.RuntimeSession.Metadata.Context[chatPlanWorkspacePathKey] = t.TempDir()
	if err := enterChatPlanMode(session, "plan.md"); err != nil {
		t.Fatalf("enter: %v", err)
	}

	// 已登记但正文还没写入工作区：列表给提示，评论给出可读错误。
	if out := runPlanCommandText(t, session, "/plan comments"); !strings.Contains(out, "还没有归档轮次") {
		t.Fatalf("expected the empty-state hint, got:\n%s", out)
	}
	if out := runPlanCommandText(t, session, "/plan comment L1 说明"); !strings.Contains(out, "还没写入工作区") {
		t.Fatalf("expected the missing-body error, got:\n%s", out)
	}
}
