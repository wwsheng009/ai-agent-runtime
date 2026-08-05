package commands

import (
	"context"
	"strings"
	"testing"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
)

func TestWithLiveChatToolOutput_AttachesMirrorForShellLikeInteractiveTool(t *testing.T) {
	session := &ChatSession{}

	ctx := withLiveChatToolOutput(context.Background(), session, "call-1", "execute_shell_command")

	if runtimeexecutor.OutputMirrorFromContext(ctx) == nil {
		t.Fatalf("expected shell-like interactive tool to attach live output mirror")
	}
	if _, ok := runtimeexecutor.OutputMirrorFromContext(ctx).(*chatLimitedSystemOutputWriter); !ok {
		t.Fatalf("expected shell-like interactive tool to attach limited live output mirror")
	}
}

func TestWithLiveChatToolOutput_SkipsMirrorForNonShellTool(t *testing.T) {
	session := &ChatSession{}

	ctx := withLiveChatToolOutput(context.Background(), session, "call-1", "web_search")

	if runtimeexecutor.OutputMirrorFromContext(ctx) != nil {
		t.Fatalf("did not expect non-shell tool to attach live output mirror")
	}
}

func TestWithLiveChatToolOutput_SkipsMirrorWhenInteractiveOutputDisabled(t *testing.T) {
	session := &ChatSession{NoInteractive: true}

	ctx := withLiveChatToolOutput(context.Background(), session, "call-1", "bash")

	if runtimeexecutor.OutputMirrorFromContext(ctx) != nil {
		t.Fatalf("did not expect no-interactive session to attach live output mirror")
	}
}

func TestWithLiveChatToolOutput_UsesActiveBandOnlyWriterForStableCall(t *testing.T) {
	interaction := newChatInteractionCoordinator(&ChatSession{})
	t.Cleanup(interaction.Shutdown)
	session := &ChatSession{Interaction: interaction}

	ctx := withLiveChatToolOutput(context.Background(), session, "call-1", "execute_shell_command")

	if _, ok := runtimeexecutor.OutputMirrorFromContext(ctx).(*chatLiveToolOutputWriter); !ok {
		t.Fatalf("expected stable tool call to use active-band-only writer, got %T", runtimeexecutor.OutputMirrorFromContext(ctx))
	}
}

func TestWithLiveChatToolOutput_UnifiedStableCallStaysInActiveBand(t *testing.T) {
	interaction := newChatInteractionCoordinator(&ChatSession{})
	interaction.mu.Lock()
	interaction.unifiedRenderer = true
	interaction.mu.Unlock()
	t.Cleanup(interaction.Shutdown)

	session := &ChatSession{Interaction: interaction}
	ctx := withLiveChatToolOutput(context.Background(), session, "call-unified-1", "execute_shell_command")
	mirror := runtimeexecutor.OutputMirrorFromContext(ctx)
	if _, ok := mirror.(*chatLiveToolOutputWriter); !ok {
		t.Fatalf("unified stable mirror=%T, want ActiveBand-only writer", mirror)
	}
	if _, err := mirror.Write([]byte("unified-raw-marker\n")); err != nil {
		t.Fatalf("write live output: %v", err)
	}

	interaction.mu.Lock()
	tool, exists := interaction.activeTools["call:call-unified-1"]
	interaction.mu.Unlock()
	if !exists || !strings.Contains(tool.detail, "unified-raw-marker") {
		t.Fatalf("raw unified tool output was not confined to its ActiveBand stage: %+v", tool)
	}
}

func TestWithLiveChatToolOutput_UnifiedCallWithoutIDSuppressesRawMirror(t *testing.T) {
	interaction := newChatInteractionCoordinator(&ChatSession{})
	interaction.mu.Lock()
	interaction.unifiedRenderer = true
	interaction.mu.Unlock()
	t.Cleanup(interaction.Shutdown)

	session := &ChatSession{Interaction: interaction}
	ctx := withLiveChatToolOutput(context.Background(), session, "", "execute_shell_command")
	if mirror := runtimeexecutor.OutputMirrorFromContext(ctx); mirror != nil {
		t.Fatalf("identity-less unified tool installed a raw output mirror: %T", mirror)
	}
}
