package commands

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestRenderRuntimeSessionSummaryLinesIncludesProtocolCurrentAndRelativeTime(t *testing.T) {
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
		{Role: "user", Content: "hello", Metadata: runtimetypes.NewMetadata()},
	})

	lines := renderRuntimeSessionSummaryLines(session, "resume-1", now)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "协议=openai") {
		t.Fatalf("expected protocol to be rendered, got %q", joined)
	}
	if !strings.Contains(joined, "【当前】") {
		t.Fatalf("expected current session marker, got %q", joined)
	}
	if !strings.Contains(joined, "标题:") {
		t.Fatalf("expected title line, got %q", joined)
	}
}

func TestReadResumeSessionPickRendersOnlyUpdatedTimeAndTitle(t *testing.T) {
	session := &ChatSession{
		RuntimeSession: runtimechat.NewSession("resume-1"),
		InputReader:    bufio.NewReader(strings.NewReader("q\n")),
	}
	session.RuntimeSession.ID = "resume-1"
	session.RuntimeSession.State = runtimechat.StateActive
	session.RuntimeSession.UpdatedAt = time.Now().Add(-3 * time.Minute)
	session.RuntimeSession.Metadata.Title = "First session"
	session.RuntimeSession.Metadata.Context = map[string]interface{}{
		chatRuntimeContextProtocol:     "openai",
		chatRuntimeContextProviderName: "openai",
		chatRuntimeContextModel:        "gpt-4.1",
	}

	lines := captureResumeStdout(t, func() {
		_, _ = readResumeSessionPick(session, []*runtimechat.Session{session.RuntimeSession})
	})

	if !strings.Contains(lines, "First session") {
		t.Fatalf("expected title in resume list, got %q", lines)
	}
	if strings.Contains(lines, "resume-1") {
		t.Fatalf("did not expect session id in resume list, got %q", lines)
	}
	if strings.Contains(lines, "协议=") || strings.Contains(lines, "provider=") || strings.Contains(lines, "【当前】") {
		t.Fatalf("did not expect session metadata in compact resume list, got %q", lines)
	}
	if !strings.Contains(lines, "编号 (回车=1, q取消):") {
		t.Fatalf("expected visible resume pick prompt, got %q", lines)
	}
}

func TestReadResumeMenuChoiceRendersVisiblePrompt(t *testing.T) {
	session := &ChatSession{
		InputReader: bufio.NewReader(strings.NewReader("3\n")),
	}

	output := captureResumeStdout(t, func() {
		choice, err := readResumeMenuChoice(session, startupSessionOptionLabelWidth())
		if err != nil {
			t.Fatalf("readResumeMenuChoice: %v", err)
		}
		if choice != resumeChoiceCancel {
			t.Fatalf("expected cancel choice, got %v", choice)
		}
	})

	if !strings.Contains(output, "选项 (回车=1):") {
		t.Fatalf("expected visible resume menu prompt, got %q", output)
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
