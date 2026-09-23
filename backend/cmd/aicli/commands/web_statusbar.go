package commands

// 状态栏 Web 端点（§4.2.6）
//
// GET /web/api/statusbar —— 返回当前会话底部状态栏的紧凑投影，
// 与 TUI 底部状态行（ChatBar）复用同一份段值构建函数（chatSurface*StatusSegment），
// 保证 micro web client 的底部状态栏与 aicli chat TUI Seen 同一份数据：
// balance / context used % / directory / git branch / window tokens / input|output tokens。
// 注意：provider / model / reasoning_effort 在 Web 客户端由底部 cfg-bar 实时
// 展示，此处不重复。
//
// 响应示例：
//
//	{
//	  "available": true,
//	  "session_id": "sess-abc",
//	  "balance": "Balance 170.52 USD",
//	  "context_used": "Context 0% used",
//	  "context_percent": 0,
//	  "used_tokens": 0,
//	  "window_tokens": 256000,
//	  "directory": "E:\\projects\\ai\\ai-agent-runtime",
//	  "project": "ai-agent-runtime",
//	  "git_branch": "main",
//	  "window": "256K window",
//	  "input_tokens": "128K in",
//	  "output_tokens": "42K out",
//	  "segments": [
//	    {"kind":"balance","text":"Balance 170.52 USD"},
//	    {"kind":"context_used","text":"Context 0% used"},
//	    {"kind":"directory","text":"E:\\projects\\ai"},
//	    {"kind":"git_branch","text":"main"},
//	    {"kind":"window","text":"256K window"}
//	  ],
//	  "full_line": "Balance 170.52 USD · Context 0% used · E:\\projects\\ai\\ai-agent-runtime · main · 256K window"
//	}

import (
	"net/http"
	"strings"
)

// chatWebStatusBarSegmentKind 是状态栏段的分类，用于前端渲染样式。
type chatWebStatusBarSegmentKind string

const (
	chatWebStatusBarSegBalance      chatWebStatusBarSegmentKind = "balance"
	chatWebStatusBarSegRouting      chatWebStatusBarSegmentKind = "routing"
	chatWebStatusBarSegContextUsed  chatWebStatusBarSegmentKind = "context_used"
	chatWebStatusBarSegDirectory    chatWebStatusBarSegmentKind = "directory"
	chatWebStatusBarSegProject      chatWebStatusBarSegmentKind = "project"
	chatWebStatusBarSegGitBranch    chatWebStatusBarSegmentKind = "git_branch"
	chatWebStatusBarSegWindow       chatWebStatusBarSegmentKind = "window"
	chatWebStatusBarSegInputTokens  chatWebStatusBarSegmentKind = "input_tokens"
	chatWebStatusBarSegOutputTokens chatWebStatusBarSegmentKind = "output_tokens"
)

// chatWebStatusBarSegment 是状态栏中的一个段。
type chatWebStatusBarSegment struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}

// chatWebStatusBarSnapshot 是 GET /web/api/statusbar 的 JSON 响应体。
type chatWebStatusBarSnapshot struct {
	Available      bool                      `json:"available"`
	Reason         string                    `json:"reason,omitempty"`
	SessionID      string                    `json:"session_id,omitempty"`
	Balance        string                    `json:"balance,omitempty"`
	Routing        string                    `json:"routing,omitempty"`
	ContextUsed    string                    `json:"context_used,omitempty"`
	ContextPercent int                       `json:"context_percent,omitempty"`
	UsedTokens     int                       `json:"used_tokens,omitempty"`
	WindowTokens   int                       `json:"window_tokens,omitempty"`
	Directory      string                    `json:"directory,omitempty"`
	Project        string                    `json:"project,omitempty"`
	GitBranch      string                    `json:"git_branch,omitempty"`
	Window         string                    `json:"window,omitempty"`
	InputTokens    string                    `json:"input_tokens,omitempty"`
	OutputTokens   string                    `json:"output_tokens,omitempty"`
	Segments       []chatWebStatusBarSegment `json:"segments"`
	FullLine       string                    `json:"full_line"`
}

// HandleChatWebAPIStatusLine 返回当前会话的底部状态栏快照（§4.2.6）。
// 无会话时返回 available=false 的轻量响应。
func HandleChatWebAPIStatusLine(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeWebAPIJSON(w, http.StatusMethodNotAllowed, map[string]string{
			"status": "rejected",
			"reason": "method not allowed",
		})
		return
	}
	snap := buildChatWebStatusBarSnapshot()
	writeWebAPIJSON(w, http.StatusOK, snap)
}

// buildChatWebStatusBarSnapshot 从当前活跃会话构建状态栏快照。
// 复用 TUI 的 chatSurface*StatusSegment 函数，保证与 aicli chat 底部状态行一致。
func buildChatWebStatusBarSnapshot() *chatWebStatusBarSnapshot {
	return buildChatWebStatusBarSnapshotForSession(chatWebSession())
}

// buildChatWebStatusBarSnapshotForSession 是快照构建的会话入口（可测）。
// 无会话时返回 available=false 的轻量响应。
func buildChatWebStatusBarSnapshotForSession(session *ChatSession) *chatWebStatusBarSnapshot {
	if session == nil {
		return &chatWebStatusBarSnapshot{
			Available: false,
			Reason:    "no active chat session",
		}
	}

	snap := &chatWebStatusBarSnapshot{
		Available: true,
		SessionID: currentRuntimeSessionID(session),
	}

	// provider  / model / reasoning_effort 已在 cfg-bar 实时展示，
	// 此处状态栏仅显示运行时状态：balance / context / directory / git / window / tokens。

	// routing（§6.4：与 TUI 状态栏段同源；零配置时不显示，§6.2 可见性）
	if seg := chatSurfaceRoutingStatusSegment(session); seg.full != "" {
		snap.Routing = seg.full
		snap.Segments = append(snap.Segments, chatWebStatusBarSegment{
			Kind: string(chatWebStatusBarSegRouting),
			Text: seg.full,
		})
	}

	// balance
	if seg := chatSurfaceAccountBalanceStatusSegment(session); seg.full != "" {
		snap.Balance = seg.full
		snap.Segments = append(snap.Segments, chatWebStatusBarSegment{
			Kind: string(chatWebStatusBarSegBalance),
			Text: seg.full,
		})
	}

	// context used
	if seg := chatSurfaceContextUsedStatusSegment(session); seg.full != "" {
		snap.ContextUsed = seg.full
		snap.ContextPercent, _ = parseContextUsedPercent(seg.full)
		// also resolve raw token counts for finer-grained display
		snap.UsedTokens = resolveChatStatusContextUsedTokens(session)
		snap.WindowTokens = resolveChatStatusContextWindowTokens(session)
		snap.Segments = append(snap.Segments, chatWebStatusBarSegment{
			Kind: string(chatWebStatusBarSegContextUsed),
			Text: seg.full,
		})
	}

	// directory
	if seg := chatSurfaceDirectoryStatusSegment(session); seg.full != "" {
		snap.Directory = seg.full
		snap.Segments = append(snap.Segments, chatWebStatusBarSegment{
			Kind: string(chatWebStatusBarSegDirectory),
			Text: seg.full,
		})
	}

	// project (only if not redundant with directory)
	if seg := chatSurfaceProjectStatusSegment(session); seg.full != "" {
		cwdSeg := chatSurfaceDirectoryStatusSegment(session)
		if !chatStatusProjectRedundantWithDirectory(cwdSeg, seg) {
			snap.Project = seg.full
			snap.Segments = append(snap.Segments, chatWebStatusBarSegment{
				Kind: string(chatWebStatusBarSegProject),
				Text: seg.full,
			})
		}
	}

	// git branch
	if seg := chatSurfaceGitBranchStatusSegment(session); seg.full != "" {
		snap.GitBranch = seg.full
		snap.Segments = append(snap.Segments, chatWebStatusBarSegment{
			Kind: string(chatWebStatusBarSegGitBranch),
			Text: seg.full,
		})
	}

	// window
	if seg := chatSurfaceWindowStatusSegment(session); seg.full != "" {
		snap.Window = seg.full
		snap.Segments = append(snap.Segments, chatWebStatusBarSegment{
			Kind: string(chatWebStatusBarSegWindow),
			Text: seg.full,
		})
	}

	// input tokens
	if seg := chatSurfaceInputTokensStatusSegment(session); seg.full != "" {
		snap.InputTokens = seg.full
		snap.Segments = append(snap.Segments, chatWebStatusBarSegment{
			Kind: string(chatWebStatusBarSegInputTokens),
			Text: seg.full,
		})
	}

	// output tokens
	if seg := chatSurfaceOutputTokensStatusSegment(session); seg.full != "" {
		snap.OutputTokens = seg.full
		snap.Segments = append(snap.Segments, chatWebStatusBarSegment{
			Kind: string(chatWebStatusBarSegOutputTokens),
			Text: seg.full,
		})
	}

	// Build the full-line text (segments joined by " · ", matching TUI).
	parts := make([]string, 0, len(snap.Segments))
	for _, seg := range snap.Segments {
		parts = append(parts, seg.Text)
	}
	snap.FullLine = strings.Join(parts, chatSurfaceStatusSeparator)

	return snap
}

// parseContextUsedPercent 从 "Context 42% used" 中提取百分比。
func parseContextUsedPercent(text string) (int, bool) {
	text = strings.TrimSpace(text)
	idx := strings.Index(text, "%")
	if idx < 1 {
		return 0, false
	}
	// 向前扫描数字
	start := idx
	for start > 0 && text[start-1] >= '0' && text[start-1] <= '9' {
		start--
	}
	numStr := text[start:idx]
	if numStr == "" {
		return 0, false
	}
	var n int
	for _, c := range numStr {
		n = n*10 + int(c-'0')
	}
	return n, true
}
