package commands

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

func TestChatLoginPrompterPromptTextAddsLineBreakAfterInteractiveTransientRead(t *testing.T) {
	oldChatIsInteractiveTerminal := chatIsInteractiveTerminal
	chatIsInteractiveTerminal = func() bool {
		return true
	}
	defer func() {
		chatIsInteractiveTerminal = oldChatIsInteractiveTerminal
	}()

	oldStdin := os.Stdin
	oldStdout := os.Stdout
	stdinRead, stdinWrite, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe stdin: %v", err)
	}
	stdoutRead, stdoutWrite, err := os.Pipe()
	if err != nil {
		_ = stdinRead.Close()
		_ = stdinWrite.Close()
		t.Fatalf("os.Pipe stdout: %v", err)
	}
	defer func() {
		os.Stdin = oldStdin
		os.Stdout = oldStdout
		_ = stdinRead.Close()
		_ = stdoutRead.Close()
		_ = stdoutWrite.Close()
	}()

	os.Stdin = stdinRead
	os.Stdout = stdoutWrite
	if _, err := stdinWrite.WriteString("https://example.cn/v1\n"); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
	if err := stdinWrite.Close(); err != nil {
		t.Fatalf("close stdin writer: %v", err)
	}

	session := &ChatSession{InputBox: ui.NewInputBox(nil)}
	got, err := (chatLoginPrompter{session: session}).PromptText("Base URL", "", true)
	if err != nil {
		t.Fatalf("PromptText: %v", err)
	}
	if got != "https://example.cn/v1" {
		t.Fatalf("unexpected prompt value: %q", got)
	}

	if err := stdoutWrite.Close(); err != nil {
		t.Fatalf("close stdout writer: %v", err)
	}
	output, err := io.ReadAll(stdoutRead)
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	if !strings.Contains(string(output), "Base URL: \n") {
		t.Fatalf("expected prompt line to terminate before the next prompt, got %q", string(output))
	}
}
