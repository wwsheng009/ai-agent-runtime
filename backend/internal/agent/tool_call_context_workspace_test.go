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

// Approved replays (chat/session actors resume an already-approved tool call)
// must be anchored to the same workspace root the executor resolves relative
// paths against; otherwise the static policy validates paths against the
// process working directory and the sandbox check can be passed by a relative
// argument that still escapes the bound project directory.
func TestApprovedToolCallContextCarriesWorkspaceRoot(t *testing.T) {
	agent := &Agent{config: &Config{Options: map[string]interface{}{"tool_base_path": "/tools/root"}}}
	if got := toolctx.WorkspaceRoot(approvedToolCallContext(nil, agent)); got != "/tools/root" {
		t.Fatalf("expected approved replay to carry workspace root /tools/root, got %q", got)
	}

	agent = &Agent{config: &Config{Options: map[string]interface{}{"workspace_path": "  /ctx/root  "}}}
	if got := toolctx.WorkspaceRoot(approvedToolCallContext(context.Background(), agent)); got != "/ctx/root" {
		t.Fatalf("expected trimmed workspace_path fallback, got %q", got)
	}

	// No agent option means no binding: keep whatever the caller bound.
	agent = &Agent{config: &Config{}}
	bound := toolctx.WithWorkspaceRoot(context.Background(), "/existing/root")
	if got := toolctx.WorkspaceRoot(approvedToolCallContext(bound, agent)); got != "/existing/root" {
		t.Fatalf("expected caller-bound workspace root to be preserved, got %q", got)
	}
	if got := toolctx.WorkspaceRoot(approvedToolCallContext(context.Background(), nil)); got != "" {
		t.Fatalf("expected empty workspace root without an agent, got %q", got)
	}
}
