package planmode

import (
	"errors"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/planstore"
)

// 行级评论（§4.4）的纯逻辑层：锚点创建、跨修订重放、交付文本。
//
// 契约（v1）：
//
//   - 锚点 = 修订号 + 行区间 + **当时的正文摘录**。摘录是重放的唯一依据，
//     因此正文改写后评论不会被静默指向别的行；
//   - 重放优先级与 Web/CLI 的「读法」一致地保守：原区间仍是原文（anchored）
//     > 原文仍存在但已移动（moved，取与原始行号最近的匹配）> 原文已改写/删除
//     （orphaned，保留原锚点与当时内容供读者判断，绝不丢弃）；
//   - 交付给模型的文本由 FormatPlanCommentsForReview 渲染，走既有的
//     pending_review_notes 一次性通道（不新开通道）。
//
// 存储形状见 internal/planstore/comments.go。

// ErrCommentRange reports a comment range that does not exist in the given plan
// revision.
var ErrCommentRange = errors.New("planmode: comment line range outside plan revision")

// CommentAnchorStatus is the replay result of one comment against a revision.
type CommentAnchorStatus string

const (
	// CommentAnchored marks a comment whose range still holds the same text.
	CommentAnchored CommentAnchorStatus = "anchored"
	// CommentMoved marks a comment whose text still exists, elsewhere.
	CommentMoved CommentAnchorStatus = "moved"
	// CommentOrphaned marks a comment whose anchored text is gone.
	CommentOrphaned CommentAnchorStatus = "orphaned"
)

// ResolvedComment is a stored comment plus its replay result against one
// revision: StartLine/EndLine are the *current* location (for orphaned
// comments: the original, no longer matching one).
type ResolvedComment struct {
	// Comment 保留存储时的原始锚点与摘录：重放只报告当前位置，绝不改写历史，
	// 否则「原文已移动」就成了无从对照的断言。
	Comment   planstore.ReviewComment
	Status    CommentAnchorStatus
	StartLine int // 当前定位（orphaned 时与原锚点一致）
	EndLine   int
}

// LineCommentOptions describes a comment about one line range of a plan
// revision.
type LineCommentOptions struct {
	RecordID  string
	Revision  int
	StartLine int
	EndLine   int
	Content   []byte
	Body      string
	Author    string
}

// NewLineComment builds a comment whose excerpt is taken from the same content
// the range was selected in, so a later replay has something to match. An
// out-of-range selection is refused instead of being clamped: a comment must
// always name text the reviewer actually saw.
func NewLineComment(opts LineCommentOptions) (planstore.ReviewComment, error) {
	lines := commentLines(string(opts.Content))
	start, end := opts.StartLine, opts.EndLine
	if end == 0 {
		end = start
	}
	if start < 1 || end < start || end > len(lines) {
		return planstore.ReviewComment{}, fmt.Errorf("%w: L%d-%d of %d lines", ErrCommentRange, start, end, len(lines))
	}
	return planstore.ReviewComment{
		RecordID:  strings.TrimSpace(opts.RecordID),
		Revision:  opts.Revision,
		StartLine: start,
		EndLine:   end,
		Excerpt:   strings.Join(lines[start-1:end], "\n"),
		Body:      strings.TrimSpace(opts.Body),
		Author:    strings.TrimSpace(opts.Author),
	}, nil
}

// ResolvePlanComments replays every comment against content (usually the newest
// revision of the same record) and reports where each one now points.
func ResolvePlanComments(comments []planstore.ReviewComment, content []byte) []ResolvedComment {
	lines := commentLines(string(content))
	resolved := make([]ResolvedComment, 0, len(comments))
	for _, comment := range comments {
		resolved = append(resolved, resolvePlanComment(comment, lines))
	}
	return resolved
}

func resolvePlanComment(comment planstore.ReviewComment, lines []string) ResolvedComment {
	out := ResolvedComment{
		Comment:   comment,
		Status:    CommentAnchored,
		StartLine: comment.StartLine,
		EndLine:   comment.EndLine,
	}
	excerpt := commentLines(comment.Excerpt)

	// 没有摘录（历史数据、或调用方只给了行号）：退化为「区间是否仍存在」，
	// 不在没有证据的情况下宣称原文一致。
	if len(excerpt) == 0 {
		if comment.StartLine >= 1 && comment.StartLine <= len(lines) {
			if comment.EndLine > len(lines) {
				out.EndLine = len(lines)
			}
			return out
		}
		out.Status = CommentOrphaned
		return out
	}

	if windowMatches(lines, comment.StartLine, excerpt) {
		return out
	}
	if start, ok := nearestWindow(lines, excerpt, comment.StartLine); ok {
		out.Status = CommentMoved
		out.StartLine = start
		out.EndLine = start + len(excerpt) - 1
		return out
	}
	// 原文已改写/删除：保留原锚点与当时内容，不猜测新位置。
	out.Status = CommentOrphaned
	return out
}

// FormatPlanCommentsForReview renders resolved comments for the one-shot review
// reminder that drives the revision turn.
func FormatPlanCommentsForReview(resolved []ResolvedComment) string {
	if len(resolved) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("行级评论（已按当前计划正文重放锚点）：\n")
	for _, comment := range resolved {
		rangeText := formatCommentRange(comment.StartLine, comment.EndLine)
		switch comment.Status {
		case CommentMoved:
			fmt.Fprintf(&b, "- %s（原文已移动，原锚定 %s）: %s\n",
				rangeText, formatCommentRange(comment.Comment.StartLine, comment.Comment.EndLine), comment.Comment.Body)
		case CommentOrphaned:
			fmt.Fprintf(&b, "- %s（锚点失效：该处正文已被改写；当时内容「%s」）: %s\n",
				rangeText, compressCommentExcerpt(comment.Comment.Excerpt, 80), comment.Comment.Body)
		default:
			fmt.Fprintf(&b, "- %s: %s\n", rangeText, comment.Comment.Body)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// commentLines splits text into lines with trailing whitespace trimmed and
// outer blank lines dropped, which is the normalization both excerpt creation
// and replay compare on. Line numbers in this package are always 1-based into
// this shape.
func commentLines(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	raw := strings.Split(text, "\n")
	out := make([]string, len(raw))
	for i, line := range raw {
		out[i] = strings.TrimRight(line, " \t\r")
	}
	start, end := 0, len(out)
	for start < end && strings.TrimSpace(out[start]) == "" {
		start++
	}
	for end > start && strings.TrimSpace(out[end-1]) == "" {
		end--
	}
	return out[start:end]
}

func windowMatches(lines []string, startLine int, excerpt []string) bool {
	if startLine < 1 || startLine-1+len(excerpt) > len(lines) {
		return false
	}
	for i, want := range excerpt {
		if lines[startLine-1+i] != want {
			return false
		}
	}
	return true
}

// nearestWindow finds every window equal to excerpt and returns the one closest
// to preferLine (ties resolved toward the earlier window), so a moved comment
// lands where the reviewer would look for it instead of at an arbitrary match.
func nearestWindow(lines []string, excerpt []string, preferLine int) (int, bool) {
	if len(excerpt) == 0 || len(excerpt) > len(lines) {
		return 0, false
	}
	best := 0
	bestDistance := 0
	for start := 1; start+len(excerpt)-1 <= len(lines); start++ {
		if !windowMatches(lines, start, excerpt) {
			continue
		}
		distance := start - preferLine
		if distance < 0 {
			distance = -distance
		}
		if best == 0 || distance < bestDistance {
			best, bestDistance = start, distance
		}
	}
	return best, best != 0
}

func formatCommentRange(start, end int) string {
	if end <= start {
		return fmt.Sprintf("L%d", start)
	}
	return fmt.Sprintf("L%d-%d", start, end)
}

// compressCommentExcerpt flattens a (possibly multi-line) excerpt into one line
// and truncates it on a rune boundary for the review text.
func compressCommentExcerpt(excerpt string, limit int) string {
	flat := strings.Join(strings.Fields(strings.ReplaceAll(excerpt, "\n", " ")), " ")
	runes := []rune(flat)
	if len(runes) <= limit {
		return flat
	}
	return string(runes[:limit]) + "…"
}
