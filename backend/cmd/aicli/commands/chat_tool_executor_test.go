package commands

import (
	"context"
	"testing"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
)

func TestWithLiveChatToolOutput_AttachesMirrorForShellLikeInteractiveTool(t *testing.T) {
	session := &ChatSession{}

	ctx := withLiveChatToolOutput(context.Background(), session, "execute_shell_command")

	if runtimeexecutor.OutputMirrorFromContext(ctx) == nil {
		t.Fatalf("expected shell-like interactive tool to attach live output mirror")
	}
}

func TestWithLiveChatToolOutput_SkipsMirrorForNonShellTool(t *testing.T) {
	session := &ChatSession{}

	ctx := withLiveChatToolOutput(context.Background(), session, "web_search")

	if runtimeexecutor.OutputMirrorFromContext(ctx) != nil {
		t.Fatalf("did not expect non-shell tool to attach live output mirror")
	}
}

func TestWithLiveChatToolOutput_SkipsMirrorWhenInteractiveOutputDisabled(t *testing.T) {
	session := &ChatSession{NoInteractive: true}

	ctx := withLiveChatToolOutput(context.Background(), session, "bash")

	if runtimeexecutor.OutputMirrorFromContext(ctx) != nil {
		t.Fatalf("did not expect no-interactive session to attach live output mirror")
	}
}
