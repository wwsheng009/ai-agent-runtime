package agent

import (
	"context"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
)

func TestToolCallContextCarriesWorkspaceRoot(t *testing.T) {
	agent := &Agent{config: &Config{Options: map[string]interface{}{"workspace_path": "/ctx/root"}}}
	ctx := toolCallContext(context.Background(), nil, "", nil, agent, "session-ws-root", 1)
	if got := toolctx.WorkspaceRoot(ctx); got != "/ctx/root" {
		t.Fatalf("expected workspace root /ctx/root, got %q", got)
	}

	agent = &Agent{config: &Config{Options: map[string]interface{}{
		"tool_base_path": "/tools/root",
		"workspace_path": "/ctx/root",
	}}}
	ctx = toolCallContext(context.Background(), nil, "", nil, agent, "session-ws-root", 1)
	if got := toolctx.WorkspaceRoot(ctx); got != "/tools/root" {
		t.Fatalf("expected tool_base_path to win, got %q", got)
	}

	agent = &Agent{config: &Config{}}
	ctx = toolCallContext(context.Background(), nil, "", nil, agent, "session-ws-root", 1)
	if got := toolctx.WorkspaceRoot(ctx); got != "" {
		t.Fatalf("expected empty workspace root without options, got %q", got)
	}
}
