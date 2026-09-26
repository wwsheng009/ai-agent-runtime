package planstore

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func commentFixtureStore(t *testing.T, id string) *Store {
	t.Helper()
	store := NewStore(t.TempDir())
	if _, err := store.Record(RecordOptions{ID: id, ProjectPath: "/tmp/proj", PlanPath: "plan.md"}); err != nil {
		t.Fatalf("record: %v", err)
	}
	return store
}

func TestAppendCommentFillsDefaultsAndPreservesOrder(t *testing.T) {
	const id = "proj/plan"
	store := commentFixtureStore(t, id)

	first, err := store.AppendComment(id, ReviewComment{Revision: 2, StartLine: 3, EndLine: 4, Body: " 补上回滚风险 "})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if first.ID == "" || !strings.HasPrefix(first.ID, "c-") {
		t.Fatalf("expected a generated comment id, got %q", first.ID)
	}
	if first.CreatedAt == "" {
		t.Fatalf("expected created_at to be filled")
	}
	if first.RecordID != id {
		t.Fatalf("expected record id to be backfilled, got %q", first.RecordID)
	}
	if first.Body != "补上回滚风险" {
		t.Fatalf("expected the body to be trimmed, got %q", first.Body)
	}

	second, err := store.AppendComment(id, ReviewComment{
		ID: first.ID, Revision: 2, StartLine: 9, EndLine: 9, Body: "顺序不对", Author: " user ",
	})
	if err != nil {
		t.Fatalf("append second: %v", err)
	}
	if second.ID == first.ID {
		t.Fatalf("a duplicate id must be suffixed, both are %q", second.ID)
	}
	if second.Author != "user" {
		t.Fatalf("expected author to be trimmed, got %q", second.Author)
	}

	comments, err := store.Comments(id)
	if err != nil {
		t.Fatalf("comments: %v", err)
	}
	if len(comments) != 2 || comments[0].ID != first.ID || comments[1].ID != second.ID {
		t.Fatalf("expected append order to be preserved, got %+v", comments)
	}
}

func TestAppendCommentRejectsInvalidShape(t *testing.T) {
	const id = "proj/plan"
	store := commentFixtureStore(t, id)

	cases := []struct {
		name    string
		comment ReviewComment
	}{
		{"empty body", ReviewComment{Revision: 1, StartLine: 1, EndLine: 1, Body: "   "}},
		{"zero revision", ReviewComment{StartLine: 1, EndLine: 1, Body: "x"}},
		{"zero start", ReviewComment{Revision: 1, StartLine: 0, EndLine: 1, Body: "x"}},
		{"reversed range", ReviewComment{Revision: 1, StartLine: 5, EndLine: 4, Body: "x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := store.AppendComment(id, tc.comment); !errors.Is(err, ErrInvalidComment) {
				t.Fatalf("expected ErrInvalidComment, got %v", err)
			}
		})
	}
}

func TestCommentsMissingLogIsEmptyAndUnknownRecordStillReads(t *testing.T) {
	const id = "proj/plan"
	store := commentFixtureStore(t, id)

	comments, err := store.Comments(id)
	if err != nil {
		t.Fatalf("comments on a blank record: %v", err)
	}
	if len(comments) != 0 {
		t.Fatalf("expected no comments, got %+v", comments)
	}
	// 未知记录与无评论记录一样是「空」，不是错误——调用方据此区分「没有评论」与
	// 「读取失败」。
	if comments, err := store.Comments("other/missing"); err != nil || len(comments) != 0 {
		t.Fatalf("expected an empty result for an unknown record, got %+v err=%v", comments, err)
	}
	if _, err := store.Comments("  "); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for an empty id, got %v", err)
	}
}

func TestDeleteCommentIsIdempotent(t *testing.T) {
	const id = "proj/plan"
	store := commentFixtureStore(t, id)
	comment, err := store.AppendComment(id, ReviewComment{Revision: 1, StartLine: 1, EndLine: 2, Body: "x"})
	if err != nil {
		t.Fatalf("append: %v", err)
	}

	deleted, err := store.DeleteComment(id, comment.ID)
	if err != nil || !deleted {
		t.Fatalf("expected the comment to be deleted, got deleted=%v err=%v", deleted, err)
	}
	again, err := store.DeleteComment(id, comment.ID)
	if err != nil || again {
		t.Fatalf("delete must be idempotent, got deleted=%v err=%v", again, err)
	}
	if comments, err := store.Comments(id); err != nil || len(comments) != 0 {
		t.Fatalf("expected an empty log, got %+v err=%v", comments, err)
	}
	// 其它评论不受影响。
	keptA, _ := store.AppendComment(id, ReviewComment{Revision: 1, StartLine: 1, EndLine: 1, Body: "a"})
	keptB, _ := store.AppendComment(id, ReviewComment{Revision: 1, StartLine: 2, EndLine: 2, Body: "b"})
	if _, err := store.DeleteComment(id, keptA.ID); err != nil {
		t.Fatalf("delete first: %v", err)
	}
	comments, err := store.Comments(id)
	if err != nil || len(comments) != 1 || comments[0].ID != keptB.ID {
		t.Fatalf("expected only %s to survive, got %+v err=%v", keptB.ID, comments, err)
	}
}

func TestDeleteRecordRemovesCommentLog(t *testing.T) {
	const id = "proj/plan"
	store := commentFixtureStore(t, id)
	if _, err := store.AppendComment(id, ReviewComment{Revision: 1, StartLine: 1, EndLine: 1, Body: "x"}); err != nil {
		t.Fatalf("append: %v", err)
	}
	rel := commentsRelPath(id)
	abs := filepath.Join(store.Root(), filepath.FromSlash(rel))
	if _, err := os.Stat(abs); err != nil {
		t.Fatalf("expected the comment log at %s: %v", rel, err)
	}

	if err := store.Delete(id); err != nil {
		t.Fatalf("delete record: %v", err)
	}
	if _, err := os.Stat(abs); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected the comment log to be removed with the record, stat err=%v", err)
	}
}

func TestCommentsDecodeFailureNamesTheLine(t *testing.T) {
	const id = "proj/plan"
	store := commentFixtureStore(t, id)
	if _, err := store.AppendComment(id, ReviewComment{Revision: 1, StartLine: 1, EndLine: 1, Body: "x"}); err != nil {
		t.Fatalf("append: %v", err)
	}
	abs := filepath.Join(store.Root(), filepath.FromSlash(commentsRelPath(id)))
	data, err := os.ReadFile(abs)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := os.WriteFile(abs, append(data, []byte("{not json\n")...), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err = store.Comments(id)
	if err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("expected a decode error naming the line, got %v", err)
	}
}
