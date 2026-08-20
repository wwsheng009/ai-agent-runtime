package commands

import (
	"context"
	"runtime"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

func TestExecuteStructuredShellCommandEmptyArgument(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	session := &ChatSession{cancelCtx: context.Background()}
	result := executeStructuredShellCommand(session, "/shell")
	plain := ui.RenderDocumentPlain(result.Document())
	if !strings.Contains(plain, "错误: 需要指定 shell 命令") {
		t.Fatalf("expected usage error in cell, got %q", plain)
	}
	if result.SendMessageAfterCommit != "" {
		t.Fatalf("usage error must not set post-commit send effect, got %q", result.SendMessageAfterCommit)
	}

	cmdResult := executeStructuredShellCommand(session, "/cmd")
	if plain := ui.RenderDocumentPlain(cmdResult.Document()); !strings.Contains(plain, "错误: 需要指定 shell 命令") {
		t.Fatalf("expected /cmd alias usage error, got %q", plain)
	}
}

func TestExecuteStructuredShellCommandCommitsCellAndSendEffect(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	session := &ChatSession{
		ChatExecutor: &fakeChatExecutor{output: "structured shell response"},
		cancelCtx:    context.Background(),
	}
	session.Interaction = newChatInteractionCoordinator(session)

	result := executeStructuredShellCommand(session, "/shell echo structured-shell-cell")
	plain := ui.RenderDocumentPlain(result.Document())
	if !strings.Contains(plain, "执行命令: echo structured-shell-cell") {
		t.Fatalf("expected command cell to show executed command, got %q", plain)
	}
	if !strings.Contains(plain, "structured-shell-cell") {
		t.Fatalf("expected command cell to contain captured output, got %q", plain)
	}
	if result.Action != CommandContinue {
		t.Fatalf("expected /shell result to continue, got action %v", result.Action)
	}
	if !strings.Contains(result.SendMessageAfterCommit, "我执行了命令: echo structured-shell-cell") {
		t.Fatalf("expected post-commit send effect to carry the AI input, got %q", result.SendMessageAfterCommit)
	}
}

func TestExecuteStructuredShellCommandSuccessfulNoOutputIsStillCommitted(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	session := &ChatSession{cancelCtx: context.Background()}
	command := "true"
	if runtime.GOOS == "windows" {
		command = "cmd /c exit 0"
	}
	result := executeStructuredShellCommand(session, "/shell "+command)
	plain := ui.RenderDocumentPlain(result.Document())
	if strings.Contains(plain, "命令执行成功，但没有输出") {
		t.Fatalf("successful empty command was reported as an error: %q", plain)
	}
	if !strings.Contains(plain, "(无输出)") {
		t.Fatalf("successful empty command did not render an empty-output marker: %q", plain)
	}
	if !strings.Contains(result.SendMessageAfterCommit, "我执行了命令: "+command) {
		t.Fatalf("successful empty command did not produce post-commit AI input: %q", result.SendMessageAfterCommit)
	}
}
