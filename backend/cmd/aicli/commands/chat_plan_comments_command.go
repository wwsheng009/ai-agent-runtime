package commands

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/planmode"
	"github.com/wwsheng009/ai-agent-runtime/internal/planstore"
)

// 行级评论的 CLI 面（§4.4）：
//
//	/plan comment <L12|L12-14> <正文>   在当前计划上留一条行级评论
//	/plan comments                      列出评论（按最新归档轮重放锚点）
//
// 与 `/plans diff` 一样直接读写归档存储（不经 HTTP），口径与 HTTP 面一致：
// 摘录由 planmode.NewLineComment 从锚定轮正文截取，展示走 ResolvePlanComments
// 的重放结果；交付给模型时并入 pending_review_notes 一次性通道（见
// chatPlanReviewNotesWithComments），轮次记录里仍只存用户自己写的 notes。

// chatPlanCommentsCommandText 处理 `comment` / `comments` 两个子命令。
func chatPlanCommentsCommandText(session *ChatSession, verb, rest string) string {
	switch strings.ToLower(strings.TrimSpace(verb)) {
	case "comment", "annotate", "note":
		return addChatPlanComment(session, rest)
	default:
		return renderChatPlanComments(session)
	}
}

// chatPlanCommentRecordID 解析当前会话计划对应的归档 id；空表示会话还没有计划
// 上下文（既没 /plan enter 过，也没有可解析的计划路径）。
func chatPlanCommentRecordID(session *ChatSession) string {
	if session == nil || session.RuntimeSession == nil {
		return ""
	}
	state := planmode.Load(session.RuntimeSession)
	if strings.TrimSpace(state.PlanPath) == "" {
		return ""
	}
	return planstore.IDFor(chatPlanWorkspacePath(session), state.PlanPath)
}

func addChatPlanComment(session *ChatSession, rest string) string {
	if session == nil || session.RuntimeSession == nil {
		return "错误: 当前没有活动会话"
	}
	if chatPlanCommentRecordID(session) == "" {
		return "错误: 当前会话没有计划上下文；先 /plan enter（或 /mode plan）"
	}
	rangeToken, body := splitFirstToken(rest)
	body = strings.TrimSpace(body)
	if strings.TrimSpace(rangeToken) == "" || body == "" {
		return "用法: /plan comment <L12|L12-14> <正文>"
	}
	start, end, err := parseChatPlanCommentRange(rangeToken)
	if err != nil {
		return "错误: " + err.Error()
	}
	store := chatPlanStore()
	if store == nil {
		return "错误: plan 归档存储不可用"
	}

	record, err := ensureChatPlanCommentRevision(session, store, body)
	if err != nil {
		return "错误: " + err.Error()
	}
	content, err := store.ReadVersion(record.ID, record.Version)
	if err != nil {
		return "错误: 读取该轮正文失败: " + err.Error()
	}
	comment, err := planmode.NewLineComment(planmode.LineCommentOptions{
		RecordID:  record.ID,
		Revision:  record.Version,
		StartLine: start,
		EndLine:   end,
		Content:   content,
		Body:      body,
		Author:    "user",
	})
	if err != nil {
		return "错误: " + err.Error()
	}
	stored, err := store.AppendComment(record.ID, comment)
	if err != nil {
		return "错误: 写入行级评论失败: " + err.Error()
	}
	return fmt.Sprintf("已记录行级评论 %s：%s（锚定 v%d，%s）\n提示: /plan comments 查看；/plan request_changes <notes> 时一并交给模型",
		stored.ID,
		planmode.FormatCommentRange(stored.StartLine, stored.EndLine),
		stored.Revision,
		planmode.CompressCommentExcerpt(stored.Excerpt, 60),
	)
}

// ensureChatPlanCommentRevision 保证记录至少有一轮已归档正文——锚点必须指向用户
// 真正看到的正文。没有轮次时先按 decision=comment 归档当前计划（只登记快照：
// archiveStatus 对 comment 保持原状态），已有轮次则直接复用最新轮。
func ensureChatPlanCommentRevision(session *ChatSession, store *planstore.Store, notes string) (planstore.Record, error) {
	state := planmode.Load(session.RuntimeSession)
	record, ok, err := store.Get(chatPlanCommentRecordID(session))
	if err != nil {
		return planstore.Record{}, err
	}
	if ok && record.Version > 0 {
		return record, nil
	}
	archived, err := planmode.ArchivePlan(context.Background(), planmode.ArchiveOptions{
		Store:     store,
		SessionID: session.RuntimeSession.ID,
		Workspace: chatPlanWorkspacePath(session),
		PlanPath:  state.PlanPath,
		Decision:  "comment",
		Source:    string(planmode.ExitSourceUser),
		Notes:     notes,
	})
	if err != nil {
		return planstore.Record{}, err
	}
	if archived.Version == 0 {
		return planstore.Record{}, fmt.Errorf("计划正文还没写入工作区（%s），无法锚定行号", state.PlanPath)
	}
	return archived, nil
}

func renderChatPlanComments(session *ChatSession) string {
	if session == nil || session.RuntimeSession == nil {
		return "错误: 当前没有活动会话"
	}
	if chatPlanCommentRecordID(session) == "" {
		return "错误: 当前会话没有计划上下文；先 /plan enter（或 /mode plan）"
	}
	store := chatPlanStore()
	if store == nil {
		return "错误: plan 归档存储不可用"
	}
	record, ok, err := store.Get(chatPlanCommentRecordID(session))
	if err != nil {
		return "错误: " + err.Error()
	}
	if !ok || record.Version == 0 {
		return "当前计划还没有归档轮次；用 /plan comment <L12|L12-14> <正文> 添加第一条"
	}
	comments, err := store.Comments(record.ID)
	if err != nil {
		return "错误: " + err.Error()
	}
	if len(comments) == 0 {
		return "（暂无行级评论；用 /plan comment <L12|L12-14> <正文> 添加）"
	}
	content, err := store.ReadVersion(record.ID, record.Version)
	if err != nil {
		return "错误: 读取最新轮正文失败: " + err.Error()
	}
	resolved := planmode.ResolvePlanComments(comments, content)
	lines := []string{fmt.Sprintf("行级评论（%d 条，按最新轮 v%d 重放锚点）:", len(resolved), record.Version)}
	for _, comment := range resolved {
		lines = append(lines, "- "+formatChatPlanCommentLine(comment))
	}
	return strings.Join(lines, "\n")
}

// formatChatPlanCommentLine 渲染一条评论：当前定位 + 重放状态 + 锚定时的正文摘要
// + 评论正文。失效/移动的评论同样列出——评论是用户的输入，绝不因为锚点漂移而
// 从列表里消失。
func formatChatPlanCommentLine(comment planmode.ResolvedComment) string {
	status := "与当前正文一致"
	switch comment.Status {
	case planmode.CommentMoved:
		status = fmt.Sprintf("原文已移动（原 %s）",
			planmode.FormatCommentRange(comment.Comment.StartLine, comment.Comment.EndLine))
	case planmode.CommentOrphaned:
		status = "锚点失效（该处正文已改写，保留当时内容）"
	}
	excerpt := planmode.CompressCommentExcerpt(comment.Comment.Excerpt, 40)
	if excerpt == "" {
		excerpt = "（无摘录）"
	}
	return fmt.Sprintf("%s [%s]「%s」: %s",
		planmode.FormatCommentRange(comment.StartLine, comment.EndLine),
		status,
		excerpt,
		comment.Comment.Body,
	)
}

// chatPlanReviewCommentsText 渲染交给模型的行级评论块；空串表示没有评论（或评论
// 无法读取——此时宁可少送，也不送一份没有定位依据的文本）。
func chatPlanReviewCommentsText(session *ChatSession) string {
	id := chatPlanCommentRecordID(session)
	if id == "" {
		return ""
	}
	store := chatPlanStore()
	if store == nil {
		return ""
	}
	record, ok, err := store.Get(id)
	if err != nil || !ok || record.Version == 0 {
		return ""
	}
	comments, err := store.Comments(id)
	if err != nil || len(comments) == 0 {
		return ""
	}
	content, err := store.ReadVersion(record.ID, record.Version)
	if err != nil {
		return ""
	}
	return planmode.FormatPlanCommentsForReview(planmode.ResolvePlanComments(comments, content))
}

// chatPlanReviewNotesWithComments 把用户的 notes 与行级评论合成一次性评审提醒。
func chatPlanReviewNotesWithComments(session *ChatSession, notes string) string {
	body := strings.TrimSpace(notes)
	comments := chatPlanReviewCommentsText(session)
	switch {
	case comments == "":
		return body
	case body == "":
		return comments
	default:
		return body + "\n\n" + comments
	}
}

// parseChatPlanCommentRange 接受 L12 / L12-14 / 12 / 12-14（大小写不限）。
func parseChatPlanCommentRange(token string) (int, int, error) {
	raw := strings.TrimSpace(strings.ToLower(token))
	raw = strings.TrimPrefix(raw, "l")
	parts := strings.SplitN(raw, "-", 2)
	start, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil || start < 1 {
		return 0, 0, fmt.Errorf("行号格式: %q（应为 L12 或 L12-14）", token)
	}
	end := start
	if len(parts) == 2 {
		end, err = strconv.Atoi(strings.TrimSpace(parts[1]))
		if err != nil || end < start {
			return 0, 0, fmt.Errorf("行号区间无效: %q", token)
		}
	}
	return start, end, nil
}
