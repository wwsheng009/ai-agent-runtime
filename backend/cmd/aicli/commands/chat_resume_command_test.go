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

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestRenderRuntimeSessionSummaryLinesIncludesProtocolUpdateTimeAndCounts(t *testing.T) {
	now := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)
	session := runtimechat.NewSession("tester")
	session.ID = "resume-1"
	session.State = runtimechat.StateActive
	session.UpdatedAt = now.Add(-3 * time.Minute)
	session.Metadata.Context = map[string]interface{}{
		chatRuntimeContextProtocol:     "openai",
		chatRuntimeContextProviderName: "openai",
		chatRuntimeContextModel:        "gpt-4.1",
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
	if !strings.Contains(joined, "标题:") {
		t.Fatalf("expected title line, got %q", joined)
	}
	if !strings.Contains(joined, "最后更新=2026-05-02 11:57 (3分钟前)") {
		t.Fatalf("expected exact and relative update time, got %q", joined)
	}
	if !strings.Contains(joined, "轮次=2 消息=4") {
		t.Fatalf("expected conversation counts, got %q", joined)
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

	var picked *runtimechat.Session
	lines := captureResumeStdout(t, func() {
		picked, _ = readResumeSessionPick(session, []*runtimechat.Session{session.RuntimeSession})
	})
	if picked != session.RuntimeSession {
		t.Fatalf("expected Enter to restore the first session, got %#v", picked)
	}

	if !strings.Contains(lines, "First session") {
		t.Fatalf("expected title in resume list, got %q", lines)
	}
	if !strings.Contains(lines, "2026-05-02 11:57") || !strings.Contains(lines, "2轮/4条消息") {
		t.Fatalf("expected update time and conversation counts in resume list, got %q", lines)
	}
	if strings.Contains(lines, "resume-1") {
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
	session := runtimechat.NewSession("tester")
	session.ID = "resume-fullscreen"
	session.Metadata.Title = "Resume full-screen picker"
	session.Metadata.Context = map[string]interface{}{
		chatRuntimeContextProtocol:     "anthropic",
		chatRuntimeContextProviderName: "provider-a",
		chatRuntimeContextModel:        "model-a",
	}
	session.ReplaceHistory([]runtimetypes.Message{
		{Role: "user", Content: "improve resume", Metadata: runtimetypes.NewMetadata()},
		{Role: "assistant", Content: "working", Metadata: runtimetypes.NewMetadata()},
	})
	session.UpdatedAt = now.Add(-5 * time.Minute)

	items, selectable := buildResumeFullScreenItems([]*runtimechat.Session{nil, session}, now)
	if len(items) != 1 || len(selectable) != 1 || selectable[0] != session {
		t.Fatalf("expected nil sessions to be skipped while preserving selection mapping, got items=%#v selectable=%#v", items, selectable)
	}
	item := items[0]
	if item.Title != "Resume full-screen picker" {
		t.Fatalf("unexpected full-screen title: %q", item.Title)
	}
	for _, expected := range []string{"5分钟前", "1轮/2条"} {
		if !strings.Contains(item.Detail, expected) {
			t.Fatalf("expected detail to contain %q, got %q", expected, item.Detail)
		}
	}
	for _, expected := range []string{"resume-fullscreen", "anthropic", "provider-a", "model-a"} {
		if !strings.Contains(item.SearchText, expected) {
			t.Fatalf("expected search metadata to contain %q, got %q", expected, item.SearchText)
		}
	}
}

func TestResumeInteractiveSelectShowsHistoryDirectlyAndExcludesCurrent(t *testing.T) {
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

	session := &ChatSession{
		SessionManager: manager,
		SessionUserID:  "tester",
		RuntimeSession: current,
		InputReader:    bufio.NewReader(strings.NewReader("q\n")),
	}
	output := captureResumeStdout(t, func() {
		resumeInteractiveSelect(session)
	})

	for _, expected := range []string{"恢复历史会话（最近更新优先，共 1 个）:", "Previous session", "选择会话 (回车恢复 1，q 取消):", "当前会话保持不变"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected direct resume flow to contain %q, got %q", expected, output)
		}
	}
	for _, unexpected := range []string{"Current session", "current-session", "placeholder-session", "恢复最近可恢复会话", "选择历史会话", "匹配会话:"} {
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
