package planmode

import (
	"errors"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/planstore"
)

const commentRevisionV1 = `# Plan

## 步骤
1. 写迁移
2. 跑测试

## 回滚
手工恢复。
`

func TestNewLineCommentCapturesExcerpt(t *testing.T) {
	comment, err := NewLineComment(LineCommentOptions{
		RecordID:  "proj/plan",
		Revision:  1,
		StartLine: 4,
		EndLine:   5,
		Content:   []byte(commentRevisionV1),
		Body:      "补上回滚风险",
		Author:    "user",
	})
	if err != nil {
		t.Fatalf("new comment: %v", err)
	}
	if comment.Excerpt != "1. 写迁移\n2. 跑测试" {
		t.Fatalf("unexpected excerpt %q", comment.Excerpt)
	}

	// 只给起始行＝单行区间；越界拒绝而不是夹取。
	single, err := NewLineComment(LineCommentOptions{Revision: 1, StartLine: 8, Content: []byte(commentRevisionV1), Body: "x"})
	if err != nil {
		t.Fatalf("single line comment: %v", err)
	}
	if single.StartLine != 8 || single.EndLine != 8 || single.Excerpt != "手工恢复。" {
		t.Fatalf("unexpected single-line comment %+v", single)
	}
	if _, err := NewLineComment(LineCommentOptions{Revision: 1, StartLine: 9, EndLine: 12, Content: []byte(commentRevisionV1), Body: "x"}); !errors.Is(err, ErrCommentRange) {
		t.Fatalf("expected ErrCommentRange for an out-of-range selection, got %v", err)
	}
}

func TestResolvePlanCommentsReplaysAnchors(t *testing.T) {
	comment, err := NewLineComment(LineCommentOptions{
		Revision:  1,
		StartLine: 4,
		EndLine:   5,
		Content:   []byte(commentRevisionV1),
		Body:      "补上回滚风险",
	})
	if err != nil {
		t.Fatalf("new comment: %v", err)
	}

	// 1) 正文未动：锚点原样命中。
	same := ResolvePlanComments([]planstore.ReviewComment{comment}, []byte(commentRevisionV1))
	if len(same) != 1 || same[0].Status != CommentAnchored || same[0].StartLine != 4 || same[0].EndLine != 5 {
		t.Fatalf("expected an anchored replay, got %+v", same)
	}

	// 2) 前面插入了两行：原文仍在，锚点迁移。
	shifted := "# Plan\n\n## 背景\n补充背景\n\n## 步骤\n1. 写迁移\n2. 跑测试\n\n## 回滚\n手工恢复。\n"
	moved := ResolvePlanComments([]planstore.ReviewComment{comment}, []byte(shifted))
	if len(moved) != 1 || moved[0].Status != CommentMoved || moved[0].StartLine != 7 || moved[0].EndLine != 8 {
		t.Fatalf("expected a moved replay to L7-8, got %+v", moved)
	}
	if moved[0].Comment.StartLine != 4 || moved[0].Comment.EndLine != 5 {
		t.Fatalf("the original anchor must stay readable, got %+v", moved[0].Comment)
	}

	// 3) 原文被改写：锚点失效，但正文摘要保留（不猜测新位置）。
	rewritten := "# Plan\n\n## 步骤\n1. 写迁移脚本\n2. 跑测试\n\n## 回滚\n手工恢复。\n"
	orphaned := ResolvePlanComments([]planstore.ReviewComment{comment}, []byte(rewritten))
	if len(orphaned) != 1 || orphaned[0].Status != CommentOrphaned {
		t.Fatalf("expected an orphaned replay, got %+v", orphaned)
	}
	if orphaned[0].StartLine != 4 || orphaned[0].EndLine != 5 || orphaned[0].Comment.Excerpt == "" {
		t.Fatalf("an orphaned comment must keep its original anchor and excerpt, got %+v", orphaned[0])
	}
}

func TestResolvePlanCommentsPrefersNearestWindowOnDuplicates(t *testing.T) {
	comment, err := NewLineComment(LineCommentOptions{
		Revision:  1,
		StartLine: 9,
		EndLine:   9,
		Content:   []byte("a\nb\nc\nd\ne\nf\ng\nh\n重复行\n"),
		Body:      "就近迁移",
	})
	if err != nil {
		t.Fatalf("new comment: %v", err)
	}

	// 原位置（L9）已被改写，正文里另有 L2 与 L5 两个同样的窗口：应迁移到离原锚点
	// 最近的 L5，而不是第一个匹配（L2）。
	content := "a\n重复行\nb\nc\n重复行\nd\ne\nf\n已改写\n"
	resolved := ResolvePlanComments([]planstore.ReviewComment{comment}, []byte(content))
	if len(resolved) != 1 || resolved[0].Status != CommentMoved || resolved[0].StartLine != 5 {
		t.Fatalf("expected a move to the nearest duplicate at L5, got %+v", resolved)
	}
}

func TestResolvePlanCommentsWithoutExcerptFallsBackToRange(t *testing.T) {
	comment := planstore.ReviewComment{Revision: 1, StartLine: 3, EndLine: 9, Body: "legacy"}
	resolved := ResolvePlanComments([]planstore.ReviewComment{comment}, []byte("a\nb\nc\nd\n"))
	if len(resolved) != 1 || resolved[0].Status != CommentAnchored {
		t.Fatalf("a range that still starts inside the body is anchored, got %+v", resolved)
	}
	if resolved[0].EndLine != 4 {
		t.Fatalf("expected the end to be clamped to the body, got %+v", resolved[0])
	}

	gone := ResolvePlanComments([]planstore.ReviewComment{comment}, []byte("a\nb\n"))
	if len(gone) != 1 || gone[0].Status != CommentOrphaned {
		t.Fatalf("a range past the body is orphaned, got %+v", gone)
	}
}

func TestFormatPlanCommentsForReview(t *testing.T) {
	content := "# 计划\n\n## 背景\n补充背景\n\n## 步骤\n1. 写迁移\n2. 跑测试\n\n## 回滚\n自动回滚。\n"

	// 锚定在目标正文自身的 L7-8：重放原样命中。
	anchored, err := NewLineComment(LineCommentOptions{Revision: 2, StartLine: 7, EndLine: 8, Content: []byte(content), Body: "补上回滚风险"})
	if err != nil {
		t.Fatalf("new comment: %v", err)
	}
	// 摘录来自「插入背景段」之前的正文（步骤标题当时在 L3），重放后迁移到 L6。
	beforeInsert := "# 计划\n\n## 步骤\n1. 写迁移\n2. 跑测试\n\n## 回滚\n手工恢复。\n"
	moved, err := NewLineComment(LineCommentOptions{Revision: 2, StartLine: 3, EndLine: 3, Content: []byte(beforeInsert), Body: "步骤标题改一下"})
	if err != nil {
		t.Fatalf("new comment: %v", err)
	}
	// 摘录「手工恢复。」在目标正文里已被「自动回滚。」取代：锚点失效。
	orphaned, err := NewLineComment(LineCommentOptions{Revision: 2, StartLine: 8, EndLine: 8, Content: []byte(commentRevisionV1), Body: "改成自动回滚"})
	if err != nil {
		t.Fatalf("new comment: %v", err)
	}

	text := FormatPlanCommentsForReview(ResolvePlanComments([]planstore.ReviewComment{anchored, moved, orphaned}, []byte(content)))
	for _, expected := range []string{
		"行级评论",
		"L7-8: 补上回滚风险",
		"（原文已移动，原锚定 L3）: 步骤标题改一下",
		"（锚点失效",
		"改成自动回滚",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("review text missing %q:\n%s", expected, text)
		}
	}
	if strings.HasSuffix(text, "\n") {
		t.Fatalf("review text must not end with a blank line: %q", text)
	}
	if got := FormatPlanCommentsForReview(nil); got != "" {
		t.Fatalf("no comments should render nothing, got %q", got)
	}
}

func TestCompressCommentExcerptFlattensAndTruncatesOnRuneBoundary(t *testing.T) {
	if got := compressCommentExcerpt("a\n  b\tc ", 80); got != "a b c" {
		t.Fatalf("unexpected flatten: %q", got)
	}
	got := compressCommentExcerpt(strings.Repeat("好", 10), 4)
	if got != "好好好好…" {
		t.Fatalf("expected a rune-safe truncation, got %q", got)
	}
}
