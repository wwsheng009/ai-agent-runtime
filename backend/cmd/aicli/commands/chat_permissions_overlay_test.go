package commands

import (
	"os"
	"path/filepath"
	"testing"

	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
)

func TestApplyChatPermissionsOverlayCLIDenyWinsOverProjectAllow(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".aicli")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "permissions.yaml")
	content := []byte(`
version: 1
rules:
  - name: allow-shell
    tools: [shell]
    decision: allow
    reason: project_allows_shell
`)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}

	session := &ChatSession{
		CLIDenyTools:  []string{"shell"},
		CLIAllowTools: []string{"view"},
	}
	applyChatPermissionsOverlay(session, root)

	if len(session.PermissionsOverlay.Rules) == 0 {
		t.Fatal("expected overlay rules")
	}
	if session.PermissionsOverlay.Rules[0].Decision != runtimepolicy.DecisionDeny {
		t.Fatalf("CLI deny should be first rule, got %+v", session.PermissionsOverlay.Rules[0])
	}
	if session.ToolPolicy == nil || !session.ToolPolicy.DeniedTools["shell"] {
		t.Fatalf("expected shell hard-denied in ToolPolicy: %+v", session.ToolPolicy)
	}
	if !session.ToolPolicy.AllowlistEnabled || !session.ToolPolicy.AllowedTools["view"] {
		t.Fatalf("expected view allowlisted: %+v", session.ToolPolicy)
	}

	// Engine evaluate: CLI deny shell wins over project allow shell.
	engine := &runtimepolicy.Engine{Mode: runtimepolicy.ModeDefault}
	applyChatPermissionsOverlayToAgent(&engineAdapter{engine: engine}, session)
	decision, err := engine.Evaluate(nil, runtimepolicy.EvalRequest{ToolName: "shell", Mode: runtimepolicy.ModeDefault})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Type != runtimepolicy.DecisionDeny {
		t.Fatalf("expected deny, got %+v", decision)
	}
}

func TestApplyChatPermissionsOverlayReapplyDoesNotDoubleIntersect(t *testing.T) {
	base := runtimepolicy.NewToolExecutionPolicy([]string{"view", "grep", "shell"}, false)
	session := &ChatSession{
		BaseToolPolicy: base.Clone(),
		ToolPolicy:     base.Clone(),
		CLIAllowTools:  []string{"view", "grep"},
	}
	applyChatPermissionsOverlay(session, t.TempDir())
	firstAllowed := cloneBoolMap(session.ToolPolicy.AllowedTools)

	// Second apply with same CLI/project must not shrink further.
	applyChatPermissionsOverlay(session, t.TempDir())
	if len(session.ToolPolicy.AllowedTools) != len(firstAllowed) {
		t.Fatalf("allowlist shrank on re-apply: first=%v second=%v", firstAllowed, session.ToolPolicy.AllowedTools)
	}
	for name := range firstAllowed {
		if !session.ToolPolicy.AllowedTools[name] {
			t.Fatalf("missing allowed tool after re-apply: %s", name)
		}
	}
}

type engineAdapter struct {
	engine *runtimepolicy.Engine
}

func (a *engineAdapter) GetPermissionEngine() *runtimepolicy.Engine { return a.engine }
func (a *engineAdapter) SetPermissionEngine(engine *runtimepolicy.Engine) {
	a.engine = engine
}

func cloneBoolMap(in map[string]bool) map[string]bool {
	if in == nil {
		return nil
	}
	out := make(map[string]bool, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
