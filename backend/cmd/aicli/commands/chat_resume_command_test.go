package commands

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestParseResumeCommandArgumentCurrentDirectory(t *testing.T) {
	currentDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}

	target, filter, err := parseResumeCommandArgument("latest --cwd", ChatSessionListFilter{Query: "keep"}, nil)
	if err != nil {
		t.Fatalf("parseResumeCommandArgument: %v", err)
	}
	if target != "latest" {
		t.Fatalf("target = %q, want latest", target)
	}
	if !sameChatSessionWorkspace(filter.Workspace, currentDir) {
		t.Fatalf("workspace = %q, want current dir %q", filter.Workspace, currentDir)
	}
	if filter.Query != "keep" {
		t.Fatalf("query = %q, want existing filter preserved", filter.Query)
	}

	if _, _, err := parseResumeCommandArgument("one two --cwd", ChatSessionListFilter{}, nil); err == nil {
		t.Fatal("expected multiple resume targets to fail")
	}
}

func TestRenderRuntimeSessionSummaryLinesIncludesProtocolUpdateTimeAndCounts(t *testing.T) {
	now := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)
	workspace := t.TempDir()
	session := runtimechat.NewSession("tester")
	session.ID = "resume-1"
	session.State = runtimechat.StateActive
	session.UpdatedAt = now.Add(-3 * time.Minute)
	session.Metadata.Context = map[string]interface{}{
		chatRuntimeContextProtocol:     "openai",
		chatRuntimeContextProviderName: "openai",
		chatRuntimeContextModel:        "gpt-4.1",
		sessionmeta.WorkspacePath:      workspace,
	}
	session.ReplaceHistory([]runtimetypes.Message{
		{Role: "system", Content: "instructions", Metadata: runtimetypes.NewMetadata()},
		{Role: "user", Content: "hello", Metadata: runtimetypes.NewMetadata()},
		{Role: "assistant", Content: "hi", Metadata: runtimetypes.NewMetadata()},
		{Role: "user", Content: "continue", Metadata: runtimetypes.NewMetadata()},
	})
	session.UpdatedAt = now.Add(-3 * time.Minute)

	lines := renderRuntimeSessionSummaryLines(session, now)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "协议=openai") {
		t.Fatalf("expected protocol to be rendered, got %q", joined)
	}
	if strings.Contains(joined, "【当前】") {
		t.Fatalf("did not expect current session marker, got %q", joined)
	}
	// Title is the first column; session ID must not lead the row.
	if !strings.Contains(joined, "hello [active]") && !strings.Contains(joined, "hello") {
		t.Fatalf("expected title-first summary row, got %q", joined)
	}
	if strings.Contains(joined, "resume-1 [active]") {
		t.Fatalf("did not expect session ID as first column, got %q", joined)
	}
	if strings.Contains(joined, "标题:") {
		t.Fatalf("did not expect separate title line after title-first header, got %q", joined)
	}
	if !strings.Contains(joined, "最后更新=2026-05-02 11:57 (3分钟前)") {
		t.Fatalf("expected exact and relative update time, got %q", joined)
	}
	if !strings.Contains(joined, "轮次=2 消息=4") {
		t.Fatalf("expected conversation counts, got %q", joined)
	}
	if !strings.Contains(joined, "工作目录: "+workspace) {
		t.Fatalf("expected workspace path, got %q", joined)
	}
}

func TestReadResumeSessionPickRendersUpdateTimeCountsAndTitle(t *testing.T) {
	session := &ChatSession{
		RuntimeSession: runtimechat.NewSession("resume-1"),
		InputReader:    bufio.NewReader(strings.NewReader("\n")),
	}
	session.RuntimeSession.ID = "resume-1"
	session.RuntimeSession.State = runtimechat.StateActive
	session.RuntimeSession.Metadata.Title = "First session"
	session.RuntimeSession.Metadata.Context = map[string]interface{}{
		chatRuntimeContextProtocol:     "openai",
		chatRuntimeContextProviderName: "openai",
		chatRuntimeContextModel:        "gpt-4.1",
	}
	session.RuntimeSession.ReplaceHistory([]runtimetypes.Message{
		{Role: "system", Content: "instructions", Metadata: runtimetypes.NewMetadata()},
		{Role: "user", Content: "hello", Metadata: runtimetypes.NewMetadata()},
		{Role: "assistant", Content: "hi", Metadata: runtimetypes.NewMetadata()},
		{Role: "user", Content: "continue", Metadata: runtimetypes.NewMetadata()},
	})
	session.RuntimeSession.UpdatedAt = time.Date(2026, 5, 2, 11, 57, 0, 0, time.Local)

	history := runtimechat.NewSession("history")
	history.ID = "history-1"
	history.State = runtimechat.StateActive
	history.Metadata.Title = "First session"
	history.Metadata.Context = map[string]interface{}{
		chatRuntimeContextProtocol:     "openai",
		chatRuntimeContextProviderName: "openai",
		chatRuntimeContextModel:        "gpt-4.1",
	}
	history.ReplaceHistory([]runtimetypes.Message{
		{Role: "system", Content: "instructions", Metadata: runtimetypes.NewMetadata()},
		{Role: "user", Content: "hello", Metadata: runtimetypes.NewMetadata()},
		{Role: "assistant", Content: "hi", Metadata: runtimetypes.NewMetadata()},
		{Role: "user", Content: "continue", Metadata: runtimetypes.NewMetadata()},
	})
	history.UpdatedAt = time.Date(2026, 5, 2, 11, 57, 0, 0, time.Local)

	var picked *runtimechat.Session
	lines := captureResumeStdout(t, func() {
		picked, _ = readResumeSessionPick(session, []*runtimechat.Session{history}, nil)
	})
	if picked != history {
		t.Fatalf("expected Enter to restore the first session, got %#v", picked)
	}

	if !strings.Contains(lines, "First session") {
		t.Fatalf("expected title in resume list, got %q", lines)
	}
	if !strings.Contains(lines, "2轮/4条消息") {
		t.Fatalf("expected conversation counts in resume list, got %q", lines)
	}
	// Resume list should show relative age only (e.g. "3分钟前" / "N天前"), not absolute timestamps.
	if strings.Contains(lines, "2026-05-02 11:57") {
		t.Fatalf("did not expect absolute update timestamp in resume list, got %q", lines)
	}
	if !strings.Contains(lines, "前") && !strings.Contains(lines, "刚刚") {
		t.Fatalf("expected relative update time in resume list, got %q", lines)
	}
	if strings.Contains(lines, "resume-1") || strings.Contains(lines, "history-1") {
		t.Fatalf("did not expect session id in resume list, got %q", lines)
	}
	if strings.Contains(lines, "协议=") || strings.Contains(lines, "provider=") || strings.Contains(lines, "【当前】") {
		t.Fatalf("did not expect session metadata in compact resume list, got %q", lines)
	}
	if !strings.Contains(lines, "选择会话 (回车恢复 1，q 取消):") {
		t.Fatalf("expected visible resume pick prompt, got %q", lines)
	}
}

func TestBuildResumeFullScreenItemsIncludesHistoryDetailsAndSearchMetadata(t *testing.T) {
	now := time.Date(2026, 7, 17, 16, 0, 0, 0, time.Local)
	workspace := t.TempDir()
	session := runtimechat.NewSession("tester")
	session.ID = "resume-fullscreen"
	session.Metadata.Title = "Resume full-screen picker · compact #1"
	session.Metadata.Context = map[string]interface{}{
		chatRuntimeContextProtocol:           "anthropic",
		chatRuntimeContextProviderName:       "provider-a",
		chatRuntimeContextModel:              "model-a",
		runtimechat.ContextCompactRootTitle:  "Resume full-screen picker",
		runtimechat.ContextCompactGeneration: 1,
		sessionmeta.WorkspacePath:            workspace,
	}
	session.ReplaceHistory([]runtimetypes.Message{
		{Role: "user", Content: "improve resume", Metadata: runtimetypes.NewMetadata()},
		{Role: "assistant", Content: "working", Metadata: runtimetypes.NewMetadata()},
	})
	session.UpdatedAt = now.Add(-5 * time.Minute)

	items, selectable := buildResumeFullScreenItems([]*runtimechat.Session{nil, session}, nil, now)
	if len(items) != 1 || len(selectable) != 1 || selectable[0] != session {
		t.Fatalf("expected nil sessions to be skipped while preserving selection mapping, got items=%#v selectable=%#v", items, selectable)
	}
	item := items[0]
	if item.Title != "Resume full-screen picker · compact #1" {
		t.Fatalf("unexpected full-screen title: %q", item.Title)
	}
	if item.Disabled {
		t.Fatalf("did not expect history row to be disabled")
	}
	for _, expected := range []string{"5分钟前", "1轮/2条", "compact #1", workspace} {
		if !strings.Contains(item.Detail, expected) {
			t.Fatalf("expected detail to contain %q, got %q", expected, item.Detail)
		}
	}
	for _, expected := range []string{"resume-fullscreen", "anthropic", "provider-a", "model-a", "Resume full-screen picker", workspace} {
		if !strings.Contains(item.SearchText, expected) {
			t.Fatalf("expected search metadata to contain %q, got %q", expected, item.SearchText)
		}
	}
}

func TestBuildResumeFullScreenItemsIncludesCurrentAsDisabled(t *testing.T) {
	now := time.Date(2026, 7, 17, 16, 0, 0, 0, time.Local)
	current := runtimechat.NewSession("tester")
	current.ID = "current-live"
	current.Metadata.Title = "Renamed live title"
	current.ReplaceHistory([]runtimetypes.Message{
		{Role: "user", Content: "live", Metadata: runtimetypes.NewMetadata()},
	})
	current.UpdatedAt = now.Add(-time.Minute)

	history := runtimechat.NewSession("tester")
	history.ID = "history-live"
	history.Metadata.Title = "History session"
	history.ReplaceHistory([]runtimetypes.Message{
		{Role: "user", Content: "history", Metadata: runtimetypes.NewMetadata()},
	})
	history.UpdatedAt = now.Add(-2 * time.Minute)

	items, selectable := buildResumeFullScreenItems([]*runtimechat.Session{history}, current, now)
	if len(items) != 2 || len(selectable) != 2 {
		t.Fatalf("expected current + history rows, got items=%#v selectable=%#v", items, selectable)
	}
	if !items[0].Disabled || selectable[0] != nil {
		t.Fatalf("expected first row to be disabled current session, got item=%#v selectable=%#v", items[0], selectable[0])
	}
	if items[0].Title != "当前 · Renamed live title（不可选）" {
		t.Fatalf("unexpected current title: %q", items[0].Title)
	}
	if !strings.Contains(items[0].Detail, "当前 · 不可选") {
		t.Fatalf("expected current detail badge, got %q", items[0].Detail)
	}
	if items[1].Disabled || selectable[1] != history || items[1].Title != "History session" {
		t.Fatalf("expected second row to remain selectable history, got item=%#v selectable=%#v", items[1], selectable[1])
	}
}

func TestRenderRuntimeResumeSessionLineAlignsTitleColumn(t *testing.T) {
	now := time.Date(2026, 5, 2, 12, 0, 0, 0, time.Local)
	shortSession := runtimechat.NewSession("tester")
	shortSession.Metadata.Title = "短标题"
	shortSession.ReplaceHistory([]runtimetypes.Message{
		{Role: "user", Content: "a", Metadata: runtimetypes.NewMetadata()},
	})
	shortSession.UpdatedAt = now.Add(-time.Minute)

	longSession := runtimechat.NewSession("tester")
	longSession.Metadata.Title = "这是一个更长的会话标题"
	longSession.ReplaceHistory([]runtimetypes.Message{
		{Role: "user", Content: "b", Metadata: runtimetypes.NewMetadata()},
		{Role: "assistant", Content: "ok", Metadata: runtimetypes.NewMetadata()},
	})
	longSession.UpdatedAt = now.Add(-2 * time.Minute)

	titleWidth := maxRuntimeResumeSessionTitleWidth([]*runtimechat.Session{shortSession, longSession})
	shortLine := renderRuntimeResumeSessionLine(shortSession, now, titleWidth)
	longLine := renderRuntimeResumeSessionLine(longSession, now, titleWidth)

	shortPrefix, shortOK := splitResumeLineBeforeCounts(shortLine)
	longPrefix, longOK := splitResumeLineBeforeCounts(longLine)
	if !shortOK || !longOK {
		t.Fatalf("expected counts markers in resume lines, short=%q long=%q", shortLine, longLine)
	}
	if got, want := ui.DisplayWidth(shortPrefix), ui.DisplayWidth(longPrefix); got != want {
		t.Fatalf("expected aligned counts columns by display width, short=%d long=%d\nshort=%q\nlong=%q", got, want, shortLine, longLine)
	}
}

func TestMaxRuntimeResumeSessionTitleWidthCapsLongTitles(t *testing.T) {
	session := runtimechat.NewSession("tester")
	session.Metadata.Title = strings.Repeat("很长的会话标题", 8)
	width := maxRuntimeResumeSessionTitleWidth([]*runtimechat.Session{session})
	if width != resumeSessionTitleColumnMaxWidth {
		t.Fatalf("expected title width cap %d, got %d", resumeSessionTitleColumnMaxWidth, width)
	}

	now := time.Date(2026, 5, 2, 12, 0, 0, 0, time.Local)
	line := renderRuntimeResumeSessionLine(session, now, width)
	if !strings.Contains(line, "...") {
		t.Fatalf("expected truncated title ellipsis in resume line, got %q", line)
	}
	prefix, ok := splitResumeLineBeforeCounts(line)
	if !ok {
		t.Fatalf("expected counts marker in resume line, got %q", line)
	}
	if got := ui.DisplayWidth(prefix); got != width+2 { // title + two spaces before counts
		// Allow the two-space separator between padded title and counts.
		if got < width || got > width+4 {
			t.Fatalf("expected truncated title column near width %d, got prefix width %d (%q)", width, got, prefix)
		}
	}
}

func TestRenderRuntimeSessionSummaryLinesIncludesCompactBadge(t *testing.T) {
	now := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)
	session := runtimechat.NewSession("tester")
	session.ID = "compact-list-1"
	session.State = runtimechat.StateActive
	session.UpdatedAt = now.Add(-time.Minute)
	session.Metadata.Title = "检查登录流程为什么失败 · compact #2"
	session.Metadata.Context = map[string]interface{}{
		chatRuntimeContextProtocol:           "openai",
		runtimechat.ContextCompactGeneration: 2,
		runtimechat.ContextCompactRootTitle:  "检查登录流程为什么失败",
	}
	session.ReplaceHistory([]runtimetypes.Message{
		{Role: "user", Content: "hello", Metadata: runtimetypes.NewMetadata()},
		{Role: "assistant", Content: "hi", Metadata: runtimetypes.NewMetadata()},
	})
	session.UpdatedAt = now.Add(-time.Minute)

	joined := strings.Join(renderRuntimeSessionSummaryLines(session, now), "\n")
	if !strings.Contains(joined, "compact=#2") {
		t.Fatalf("expected compact generation badge in sessions summary, got %q", joined)
	}
	if strings.Contains(joined, "compact-list-1 [active]") {
		t.Fatalf("did not expect session id as first column, got %q", joined)
	}
}

func TestRenderRuntimeResumeSessionLineIncludesCompactBadge(t *testing.T) {
	now := time.Date(2026, 5, 2, 12, 0, 0, 0, time.Local)
	session := runtimechat.NewSession("tester")
	session.Metadata.Title = "检查登录流程为什么失败 · compact #2"
	session.Metadata.Context = map[string]interface{}{
		runtimechat.ContextCompactGeneration: 2,
		runtimechat.ContextCompactRootTitle:  "检查登录流程为什么失败",
	}
	session.ReplaceHistory([]runtimetypes.Message{
		{Role: "user", Content: "hello", Metadata: runtimetypes.NewMetadata()},
		{Role: "assistant", Content: "hi", Metadata: runtimetypes.NewMetadata()},
	})
	session.UpdatedAt = now.Add(-time.Minute)

	line := renderRuntimeResumeSessionLine(session, now, 0)
	if !strings.Contains(line, "1轮/2条消息") {
		t.Fatalf("expected turn/message counts in resume line, got %q", line)
	}
	// Format injects a dedicated "  compact #N  " badge between title and counts.
	// Titles already embed "· compact #N", so require the explicit double-space badge.
	if !strings.Contains(line, "  compact #2  ") {
		t.Fatalf("expected compact generation badge in resume line, got %q", line)
	}
	titleIndex := strings.Index(line, "检查登录流程为什么失败 · compact #2")
	badgeIndex := strings.Index(line, "  compact #2  ")
	countsIndex := strings.Index(line, "1轮/2条消息")
	if titleIndex < 0 || badgeIndex < 0 || countsIndex < 0 || !(titleIndex < badgeIndex && badgeIndex < countsIndex) {
		t.Fatalf("expected title, compact badge, then counts order, got %q", line)
	}
}

func TestRenderRuntimeResumeSessionLineUsesRelativeTimeOnly(t *testing.T) {
	now := time.Date(2026, 5, 2, 12, 0, 0, 0, time.Local)
	session := runtimechat.NewSession("tester")
	session.Metadata.Title = "Relative time session"
	session.ReplaceHistory([]runtimetypes.Message{
		{Role: "user", Content: "hello", Metadata: runtimetypes.NewMetadata()},
		{Role: "assistant", Content: "hi", Metadata: runtimetypes.NewMetadata()},
	})
	session.UpdatedAt = now.Add(-3 * time.Minute)

	line := renderRuntimeResumeSessionLine(session, now, 0)
	if !strings.Contains(line, "3分钟前") {
		t.Fatalf("expected relative update time in resume line, got %q", line)
	}
	if strings.Contains(line, "2026-05-02 11:57") {
		t.Fatalf("did not expect absolute update timestamp in resume line, got %q", line)
	}
	if strings.Contains(line, "(") || strings.Contains(line, ")") {
		t.Fatalf("did not expect absolute+relative parentheses form in resume line, got %q", line)
	}
}

func TestBuildResumeFullScreenItemUsesRelativeTimeOnly(t *testing.T) {
	now := time.Date(2026, 7, 17, 16, 0, 0, 0, time.Local)
	session := runtimechat.NewSession("tester")
	session.ID = "relative-only"
	session.Metadata.Title = "Relative full-screen"
	session.ReplaceHistory([]runtimetypes.Message{
		{Role: "user", Content: "hello", Metadata: runtimetypes.NewMetadata()},
	})
	session.UpdatedAt = now.Add(-8 * time.Minute)

	item := buildResumeFullScreenItem(session, now, false)
	if !strings.Contains(item.Detail, "8分钟前") {
		t.Fatalf("expected relative update time in full-screen detail, got %q", item.Detail)
	}
	if strings.Contains(item.Detail, "2026-07-17") || strings.Contains(item.Detail, "(") {
		t.Fatalf("did not expect absolute update timestamp in full-screen detail, got %q", item.Detail)
	}
}

func splitResumeLineBeforeCounts(line string) (string, bool) {
	marker := "轮/"
	index := strings.Index(line, marker)
	if index < 0 {
		return "", false
	}
	// Walk back over the leading digit(s) of the turn count so prefixes end
	// immediately before the shared counts column.
	start := index
	for start > 0 {
		r := rune(line[start-1])
		if r < '0' || r > '9' {
			break
		}
		start--
	}
	return line[:start], true
}

func TestResumeInteractiveSelectShowsCurrentAsNonSelectableAndHistory(t *testing.T) {
	storage := runtimechat.NewInMemoryStorage()
	manager := runtimechat.NewSessionManager(storage, nil)
	t.Cleanup(manager.Stop)

	previous := runtimechat.NewSession("tester")
	previous.ID = "previous-session"
	previous.Metadata.Title = "Previous session"
	previous.ReplaceHistory([]runtimetypes.Message{
		{Role: "user", Content: "previous prompt", Metadata: runtimetypes.NewMetadata()},
	})
	if err := storage.Save(context.Background(), previous); err != nil {
		t.Fatalf("save previous session: %v", err)
	}

	current := runtimechat.NewSession("tester")
	current.ID = "current-session"
	current.Metadata.Title = "Current session"
	current.ReplaceHistory([]runtimetypes.Message{
		{Role: "user", Content: "current prompt", Metadata: runtimetypes.NewMetadata()},
	})
	if err := storage.Save(context.Background(), current); err != nil {
		t.Fatalf("save current session: %v", err)
	}
	placeholder := runtimechat.NewSession("tester")
	placeholder.ID = "placeholder-session"
	placeholder.ReplaceHistory([]runtimetypes.Message{
		{Role: "system", Content: "instructions", Metadata: runtimetypes.NewMetadata()},
	})
	if err := storage.Save(context.Background(), placeholder); err != nil {
		t.Fatalf("save placeholder session: %v", err)
	}

	// Simulate an in-process rename that has already updated the live session.
	current.Metadata.Title = "Renamed live"

	session := &ChatSession{
		SessionManager: manager,
		SessionUserID:  "tester",
		RuntimeSession: current,
		InputReader:    bufio.NewReader(strings.NewReader("q\n")),
	}
	output := captureResumeStdout(t, func() {
		resumeInteractiveSelect(session)
	})

	for _, expected := range []string{
		"恢复历史会话（最近更新优先，共 1 个可恢复 · 当前会话仅展示）:",
		"[·]",
		"当前 · Renamed live（不可选）",
		"Previous session",
		"选择会话 (回车恢复 1，q 取消):",
		"当前会话保持不变",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected direct resume flow to contain %q, got %q", expected, output)
		}
	}
	for _, unexpected := range []string{"current-session", "placeholder-session", "恢复最近可恢复会话", "选择历史会话", "匹配会话:"} {
		if strings.Contains(output, unexpected) {
			t.Fatalf("did not expect direct resume flow to contain %q, got %q", unexpected, output)
		}
	}

	listOutput := captureResumeStdout(t, func() {
		if err := printChatSessionSummaries(manager, "tester", current.ID, ChatSessionListFilter{}); err != nil {
			t.Fatalf("printChatSessionSummaries: %v", err)
		}
	})
	if !strings.Contains(listOutput, "历史会话:") || !strings.Contains(listOutput, "Previous session") {
		t.Fatalf("expected historical session list, got %q", listOutput)
	}
	if strings.Contains(listOutput, "Current session") || strings.Contains(listOutput, "current-session") || strings.Contains(listOutput, "placeholder-session") || strings.Contains(listOutput, "【当前】") {
		t.Fatalf("did not expect current session in historical list, got %q", listOutput)
	}

	emptyOutput := captureResumeStdout(t, func() {
		if err := printChatSessionSummaries(manager, "tester", current.ID, ChatSessionListFilter{Query: "Current session"}); err != nil {
			t.Fatalf("print filtered chat sessions: %v", err)
		}
	})
	if !strings.Contains(emptyOutput, "暂无其他历史会话") || strings.Contains(emptyOutput, "Current session") {
		t.Fatalf("expected current-only result to render as an empty history list, got %q", emptyOutput)
	}
}

func TestResumeInteractiveSelectShowsCurrentOnlyWhenNoHistory(t *testing.T) {
	storage := runtimechat.NewInMemoryStorage()
	manager := runtimechat.NewSessionManager(storage, nil)
	t.Cleanup(manager.Stop)

	current := runtimechat.NewSession("tester")
	current.ID = "current-only"
	current.Metadata.Title = "Only live session"
	current.ReplaceHistory([]runtimetypes.Message{
		{Role: "user", Content: "current prompt", Metadata: runtimetypes.NewMetadata()},
	})
	if err := storage.Save(context.Background(), current); err != nil {
		t.Fatalf("save current session: %v", err)
	}

	session := &ChatSession{
		SessionManager: manager,
		SessionUserID:  "tester",
		RuntimeSession: current,
		InputReader:    bufio.NewReader(strings.NewReader("q\n")),
	}
	output := captureResumeStdout(t, func() {
		resumeInteractiveSelect(session)
	})
	for _, expected := range []string{
		"恢复历史会话（最近更新优先，共 0 个可恢复 · 当前会话仅展示）:",
		"当前 · Only live session（不可选）",
		"没有其他可恢复会话，输入 q 返回:",
		"当前没有其他可恢复的历史会话",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected current-only resume flow to contain %q, got %q", expected, output)
		}
	}
}

func TestResumeLatestWithoutOtherHistoryUsesFriendlyMessage(t *testing.T) {
	storage := runtimechat.NewInMemoryStorage()
	manager := runtimechat.NewSessionManager(storage, nil)
	t.Cleanup(manager.Stop)

	current := runtimechat.NewSession("tester")
	current.ID = "current-session"
	current.ReplaceHistory([]runtimetypes.Message{
		{Role: "user", Content: "current prompt", Metadata: runtimetypes.NewMetadata()},
	})
	if err := storage.Save(context.Background(), current); err != nil {
		t.Fatalf("save current session: %v", err)
	}

	output := captureResumeStdout(t, func() {
		resumeLatestAndPrint(&ChatSession{
			SessionManager: manager,
			SessionUserID:  "tester",
			RuntimeSession: current,
		})
	})
	if !strings.Contains(output, "当前没有其他可恢复的历史会话") || strings.Contains(output, "错误:") {
		t.Fatalf("expected a friendly empty-history message, got %q", output)
	}
}

func captureResumeStdout(t *testing.T, fn func()) string {
	t.Helper()

	oldStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = writer
	defer func() {
		os.Stdout = oldStdout
	}()

	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, reader)
		done <- buf.String()
	}()

	fn()

	_ = writer.Close()
	out := <-done
	_ = reader.Close()
	os.Stdout = oldStdout
	return out
}
